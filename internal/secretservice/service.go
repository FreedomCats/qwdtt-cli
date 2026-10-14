//go:build linux

// Package secretservice хранит секреты в связке ключей Linux — по протоколу
// Secret Service на шине D-Bus сеанса. Его реализуют gnome-keyring, KeePassXC,
// KWallet; значения там лежат зашифрованными ключом, который открывается
// входом в систему, как DPAPI на Windows.
//
// Связка — только та, что уже работает в сеансе: сервис не запускается через
// D-Bus activation. В сеансе без графики (SSH, сервер) запущенный так
// gnome-keyring просил бы пароль в окне, которого нет, и qwdtt-cli там
// пишет секреты в файл (config, схема file).
//
// Под sudo шина пользователя root'а не пускает (dbus-daemon сверяет uid),
// поэтому с ней говорит тот же qwdtt-cli, запущенный от имени вызвавшего
// (helper.go).
package secretservice

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/FreedomCats/qwdtt-cli/internal/app"
)

const (
	serviceName      = "org.freedesktop.secrets"
	servicePath      = dbus.ObjectPath("/org/freedesktop/secrets")
	serviceInterface = "org.freedesktop.Secret.Service"
	collectionIface  = "org.freedesktop.Secret.Collection"
	itemInterface    = "org.freedesktop.Secret.Item"
	promptInterface  = "org.freedesktop.Secret.Prompt"

	// noPrompt — «окна не нужно» в ответах сервиса.
	noPrompt = dbus.ObjectPath("/")

	// attrApp и attrID — атрибуты записи, по которым она находится. Метка
	// (Label) — только для человека в Seahorse или KeePassXC.
	attrApp = "application"
	attrID  = "qwdtt-cli-id"
)

var (
	// ErrUnavailable — связки ключей в этом сеансе нет: шины сеанса нет,
	// сервис не запущен или у него нет коллекции по умолчанию. Тогда секреты
	// идут в файл.
	ErrUnavailable = errors.New("связки ключей (Secret Service) в этом сеансе нет")
	// ErrNotFound — записи с таким идентификатором в связке нет.
	ErrNotFound = errors.New("записи нет в связке ключей")
	// ErrLocked — связка осталась заблокированной: окно разблокировки закрыли
	// или оно не дождалось ответа. Записи целы — их прочитает следующий
	// запуск, когда связку разблокируют.
	ErrLocked = errors.New("связка ключей не разблокирована")
)

// promptTimeout — сколько ждать окна разблокировки. Окно может оказаться там,
// где его никто не видит (вход по SSH при заблокированном экране), и без
// предела qwdtt-cli висел бы молча.
const promptTimeout = 2 * time.Minute

// Keyring — связка ключей: значения по идентификатору записи.
type Keyring interface {
	// Put записывает значение, заменяя прежнее с тем же идентификатором.
	Put(id, label, value string) error
	// Get возвращает значение; записи нет — ErrNotFound.
	Get(id string) (string, error)
	// Delete удаляет запись; записи нет — ErrNotFound.
	Delete(id string) error
	Close() error
}

// secret — структура Secret из спецификации: (oayays).
type secret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

// client — прямое соединение с Secret Service своего сеанса.
type client struct {
	conn    *dbus.Conn
	session dbus.ObjectPath
	// notify — куда сказать человеку про окно разблокировки.
	notify  io.Writer
	timeout time.Duration
}

// dial подключается к связке ключей сеанса из окружения getenv.
func dial(getenv func(string) string, notify io.Writer) (*client, error) {
	addr := busAddress(getenv)
	if addr == "" {
		return nil, ErrUnavailable
	}
	conn, err := dbus.Connect(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: шина сеанса: %v", ErrUnavailable, err)
	}
	c, err := open(conn, notify)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// open начинает работу со связкой по готовому соединению.
func open(conn *dbus.Conn, notify io.Writer) (*client, error) {
	var running bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, serviceName).Store(&running); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !running {
		return nil, ErrUnavailable
	}

	// Секрет идёт по шине открытым текстом (алгоритм plain): шина сеанса —
	// локальный сокет пользователя, и кто может его слушать, может и
	// спросить связку сам.
	var output dbus.Variant
	var session dbus.ObjectPath
	err := conn.Object(serviceName, servicePath).
		Call(serviceInterface+".OpenSession", 0, "plain", dbus.MakeVariant("")).
		Store(&output, &session)
	if err != nil {
		return nil, fmt.Errorf("связка ключей: открыть сеанс: %w", err)
	}
	return &client{conn: conn, session: session, notify: notify, timeout: promptTimeout}, nil
}

// busAddress — адрес шины сеанса: из DBUS_SESSION_BUS_ADDRESS или сокет
// в каталоге сеанса. Без них — пусто: запускать свою шину (autolaunch, как
// умеет godbus) незачем — связки на ней не будет.
func busAddress(getenv func(string) string) string {
	if addr := getenv("DBUS_SESSION_BUS_ADDRESS"); addr != "" {
		return addr
	}
	if dir := getenv("XDG_RUNTIME_DIR"); dir != "" {
		bus := filepath.Join(dir, "bus")
		if _, err := os.Stat(bus); err == nil {
			return "unix:path=" + bus
		}
	}
	return ""
}

func (c *client) Close() error {
	// Сеанс закрывается и сам, вместе с соединением.
	_ = c.conn.Object(serviceName, c.session).Call("org.freedesktop.Secret.Session.Close", 0).Err
	return c.conn.Close()
}

func (c *client) service() dbus.BusObject { return c.conn.Object(serviceName, servicePath) }

func attributes(id string) map[string]string {
	return map[string]string{attrApp: app.Name, attrID: id}
}

func (c *client) Put(id, label, value string) error {
	var coll dbus.ObjectPath
	if err := c.service().Call(serviceInterface+".ReadAlias", 0, "default").Store(&coll); err != nil {
		return fmt.Errorf("связка ключей: коллекция по умолчанию: %w", err)
	}
	if coll == noPrompt {
		return fmt.Errorf("%w: нет коллекции по умолчанию", ErrUnavailable)
	}
	if err := c.unlock([]dbus.ObjectPath{coll}); err != nil {
		return err
	}

	props := map[string]dbus.Variant{
		itemInterface + ".Label":      dbus.MakeVariant(label),
		itemInterface + ".Attributes": dbus.MakeVariant(attributes(id)),
	}
	s := secret{Session: c.session, Parameters: []byte{}, Value: []byte(value), ContentType: "text/plain; charset=utf8"}
	var item, prompt dbus.ObjectPath
	err := c.conn.Object(serviceName, coll).Call(collectionIface+".CreateItem", 0, props, s, true).Store(&item, &prompt)
	if err != nil {
		return fmt.Errorf("связка ключей: запись: %w", err)
	}
	return c.prompt(prompt)
}

func (c *client) Get(id string) (string, error) {
	items, err := c.find(id)
	if err != nil {
		return "", err
	}
	var s secret
	if err := c.conn.Object(serviceName, items[0]).Call(itemInterface+".GetSecret", 0, c.session).Store(&s); err != nil {
		return "", fmt.Errorf("связка ключей: чтение: %w", err)
	}
	return string(s.Value), nil
}

func (c *client) Delete(id string) error {
	items, err := c.find(id)
	if err != nil {
		return err
	}
	for _, item := range items {
		var prompt dbus.ObjectPath
		if err := c.conn.Object(serviceName, item).Call(itemInterface+".Delete", 0).Store(&prompt); err != nil {
			return fmt.Errorf("связка ключей: удаление: %w", err)
		}
		if err := c.prompt(prompt); err != nil {
			return err
		}
	}
	return nil
}

// find ищет записи по идентификатору и разблокирует их, если нужно.
func (c *client) find(id string) ([]dbus.ObjectPath, error) {
	var unlocked, locked []dbus.ObjectPath
	if err := c.service().Call(serviceInterface+".SearchItems", 0, attributes(id)).Store(&unlocked, &locked); err != nil {
		return nil, fmt.Errorf("связка ключей: поиск: %w", err)
	}
	if len(locked) > 0 {
		if err := c.unlock(locked); err != nil {
			return nil, err
		}
	}
	items := append(unlocked, locked...)
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	return items, nil
}

// unlock разблокирует объекты; если связка закрыта — через её окно.
func (c *client) unlock(objects []dbus.ObjectPath) error {
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := c.service().Call(serviceInterface+".Unlock", 0, objects).Store(&unlocked, &prompt); err != nil {
		return fmt.Errorf("связка ключей: разблокировка: %w", err)
	}
	if prompt == noPrompt {
		return nil
	}
	fmt.Fprintln(c.notify, "Связка ключей заблокирована: введите её пароль в окне разблокировки.")
	return c.prompt(prompt)
}

// prompt показывает окно сервиса (разблокировка, подтверждение) и ждёт,
// пока человек его закроет.
func (c *client) prompt(path dbus.ObjectPath) error {
	if path == noPrompt || path == "" {
		return nil
	}

	match := []dbus.MatchOption{
		dbus.WithMatchObjectPath(path),
		dbus.WithMatchInterface(promptInterface),
		dbus.WithMatchMember("Completed"),
	}
	if err := c.conn.AddMatchSignal(match...); err != nil {
		return fmt.Errorf("связка ключей: окно: %w", err)
	}
	defer c.conn.RemoveMatchSignal(match...)
	signals := make(chan *dbus.Signal, 4)
	c.conn.Signal(signals)
	defer c.conn.RemoveSignal(signals)

	obj := c.conn.Object(serviceName, path)
	if err := obj.Call(promptInterface+".Prompt", 0, "").Err; err != nil {
		return fmt.Errorf("связка ключей: окно: %w", err)
	}

	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	for {
		select {
		case s := <-signals:
			if s.Path != path || s.Name != promptInterface+".Completed" || len(s.Body) == 0 {
				continue
			}
			if dismissed, _ := s.Body[0].(bool); dismissed {
				return fmt.Errorf("%w: окно закрыто без ответа", ErrLocked)
			}
			return nil
		case <-timer.C:
			_ = obj.Call(promptInterface+".Dismiss", 0).Err
			return fmt.Errorf("%w: окно разблокировки не ответило за %s — "+
				"оно могло открыться на экране, которого не видно (вход по SSH)", ErrLocked, c.timeout)
		}
	}
}
