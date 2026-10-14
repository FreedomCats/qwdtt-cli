//go:build linux

package secretservice

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
)

// startBus поднимает отдельный dbus-daemon на время теста и возвращает его
// адрес. Без dbus-daemon тест пропускается, но не в CI (QWDTT_TEST_DBUS=1):
// там пропуск значил бы, что связку не проверяли вовсе.
func startBus(t *testing.T) string {
	t.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		if os.Getenv("QWDTT_TEST_DBUS") == "1" {
			t.Fatal("нет dbus-daemon, а QWDTT_TEST_DBUS=1")
		}
		t.Skip("нет dbus-daemon")
	}

	// Сокет — в /tmp, а не в t.TempDir(): путь оттуда бывает длиннее предела
	// адреса сокета (108 байт). Политика — как у шины сеанса: без eavesdrop
	// dbus-daemon не пропускает клиентам даже ответы.
	conf := filepath.Join(t.TempDir(), "bus.conf")
	err = os.WriteFile(conf, []byte(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:tmpdir=/tmp</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(daemon, "--config-file="+conf, "--nofork", "--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("адрес dbus-daemon: %v", err)
	}
	return strings.TrimSpace(addr)
}

// fakeService — Secret Service в памяти: одна коллекция, записи, блокировка
// и окно разблокировки, которое отвечает само.
type fakeService struct {
	conn *dbus.Conn

	mu    sync.Mutex
	items map[dbus.ObjectPath]*fakeItem
	next  int
	// locked — коллекция заблокирована; окно разблокировки снимает это.
	locked bool
	// dismiss — окно закрывают без ответа; silent — окно не отвечает вовсе.
	dismiss, silent bool
	// noDefault — у связки нет коллекции по умолчанию.
	noDefault bool
	prompts   int
	dismissed int
	// algorithm — с каким алгоритмом открыт сеанс.
	algorithm string
}

type fakeItem struct {
	svc   *fakeService
	path  dbus.ObjectPath
	label string
	attrs map[string]string
	value []byte
}

const collectionPath = dbus.ObjectPath("/org/freedesktop/secrets/collection/login")

// newFakeService регистрирует сервис на шине addr и возвращает его и клиента.
func newFakeService(t *testing.T) (*fakeService, *client) {
	t.Helper()
	addr := startBus(t)

	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	f := &fakeService{conn: conn, items: map[dbus.ObjectPath]*fakeItem{}}

	export := func(v any, path dbus.ObjectPath, iface string) {
		if err := conn.Export(v, path, iface); err != nil {
			t.Fatal(err)
		}
	}
	export(fakeServiceObj{f}, servicePath, serviceInterface)
	export(fakeCollection{f}, collectionPath, collectionIface)
	if reply, err := conn.RequestName(serviceName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("имя %s: %v %v", serviceName, reply, err)
	}

	c, err := dial(func(key string) string {
		if key == "DBUS_SESSION_BUS_ADDRESS" {
			return addr
		}
		return ""
	}, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return f, c
}

type fakeServiceObj struct{ f *fakeService }

func (s fakeServiceObj) OpenSession(algorithm string, _ dbus.Variant) (dbus.Variant, dbus.ObjectPath, *dbus.Error) {
	s.f.mu.Lock()
	s.f.algorithm = algorithm
	s.f.mu.Unlock()
	path := dbus.ObjectPath("/org/freedesktop/secrets/session/1")
	if err := s.f.conn.Export(fakeSession{}, path, "org.freedesktop.Secret.Session"); err != nil {
		return dbus.Variant{}, "", dbus.MakeFailedError(err)
	}
	return dbus.MakeVariant(""), path, nil
}

func (s fakeServiceObj) ReadAlias(name string) (dbus.ObjectPath, *dbus.Error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if name != "default" || s.f.noDefault {
		return noPrompt, nil
	}
	return collectionPath, nil
}

func (s fakeServiceObj) SearchItems(attrs map[string]string) ([]dbus.ObjectPath, []dbus.ObjectPath, *dbus.Error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	var found []dbus.ObjectPath
	for path, item := range s.f.items {
		if matches(item.attrs, attrs) {
			found = append(found, path)
		}
	}
	if s.f.locked {
		return []dbus.ObjectPath{}, found, nil
	}
	return found, []dbus.ObjectPath{}, nil
}

func (s fakeServiceObj) Unlock(objects []dbus.ObjectPath) ([]dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
	f := s.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.locked {
		return objects, noPrompt, nil
	}
	f.next++
	path := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/secrets/prompt/p%d", f.next))
	if err := f.conn.Export(fakePrompt{f, path}, path, promptInterface); err != nil {
		return nil, "", dbus.MakeFailedError(err)
	}
	return []dbus.ObjectPath{}, path, nil
}

func matches(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

type fakeSession struct{}

func (fakeSession) Close() *dbus.Error { return nil }

type fakeCollection struct{ f *fakeService }

func (c fakeCollection) CreateItem(props map[string]dbus.Variant, s secret, replace bool) (dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
	f := c.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.locked {
		return "", "", dbus.NewError("org.freedesktop.Secret.Error.IsLocked", nil)
	}
	attrs, _ := props[itemInterface+".Attributes"].Value().(map[string]string)
	label, _ := props[itemInterface+".Label"].Value().(string)

	if replace {
		for path, item := range f.items {
			if matches(item.attrs, attrs) && len(item.attrs) == len(attrs) {
				item.label, item.value = label, s.Value
				return path, noPrompt, nil
			}
		}
	}
	f.next++
	path := dbus.ObjectPath(fmt.Sprintf("%s/i%d", collectionPath, f.next))
	item := &fakeItem{svc: f, path: path, label: label, attrs: attrs, value: s.Value}
	if err := f.conn.Export(fakeItemObj{item}, path, itemInterface); err != nil {
		return "", "", dbus.MakeFailedError(err)
	}
	f.items[path] = item
	return path, noPrompt, nil
}

type fakeItemObj struct{ item *fakeItem }

func (o fakeItemObj) GetSecret(session dbus.ObjectPath) (secret, *dbus.Error) {
	f := o.item.svc
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.locked {
		return secret{}, dbus.NewError("org.freedesktop.Secret.Error.IsLocked", nil)
	}
	return secret{Session: session, Parameters: []byte{}, Value: o.item.value, ContentType: "text/plain"}, nil
}

func (o fakeItemObj) Delete() (dbus.ObjectPath, *dbus.Error) {
	f := o.item.svc
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, o.item.path)
	return noPrompt, nil
}

type fakePrompt struct {
	f    *fakeService
	path dbus.ObjectPath
}

func (p fakePrompt) Prompt(string) *dbus.Error {
	f := p.f
	f.mu.Lock()
	f.prompts++
	dismiss, silent := f.dismiss, f.silent
	if !dismiss && !silent {
		f.locked = false
	}
	f.mu.Unlock()
	if silent {
		return nil
	}
	go f.conn.Emit(p.path, promptInterface+".Completed", dismiss, dbus.MakeVariant(""))
	return nil
}

func (p fakePrompt) Dismiss() *dbus.Error {
	p.f.mu.Lock()
	p.f.dismissed++
	p.f.mu.Unlock()
	return nil
}

// with выполняет fn под замком сервиса: его методы идут в горутинах
// соединения, и тест трогает состояние только так.
func (f *fakeService) with(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// memKeyring — связка в памяти для проверок помощника.
type memKeyring struct {
	values map[string]string
	err    error
}

func (m *memKeyring) Put(id, _, value string) error {
	if m.err != nil {
		return m.err
	}
	m.values[id] = value
	return nil
}

func (m *memKeyring) Get(id string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	v, ok := m.values[id]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (m *memKeyring) Delete(id string) error {
	if m.err != nil {
		return m.err
	}
	if _, ok := m.values[id]; !ok {
		return ErrNotFound
	}
	delete(m.values, id)
	return nil
}

func (m *memKeyring) Close() error { return nil }

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }

func writeEmpty(path string) error { return os.WriteFile(path, nil, 0o600) }
