package datapath

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Настоящий wireguard.exe в тестах не запускается: ему нужны права
// администратора и он ставит службу в систему. Вместо него — сам тестовый
// бинарь, запущенный с переменной окружения ttFakeWireGuard (см. TestMain):
// он записывает свои аргументы и то, что видел на диске, в файл-журнал,
// и завершается кодом из ttFakeExit.

const (
	ttFakeWireGuard = "TT_FAKE_WIREGUARD" // журнал вызовов; непусто — работать подставным wireguard.exe
	ttFakeExit      = "TT_FAKE_EXIT"      // код возврата подставной программы
	ttFakeHang      = "TT_FAKE_HANG"      // непусто — зависнуть, чтобы проверить срок ожидания
)

func TestMain(m *testing.M) {
	if journal := os.Getenv(ttFakeWireGuard); journal != "" {
		os.Exit(fakeWireGuard(journal))
	}
	os.Exit(m.Run())
}

// fakeWireGuard — подставной wireguard.exe. Одна строка журнала на вызов:
// аргументы через пробел; для /installtunnelservice ещё и содержимое файла
// конфига, обрезанное до первой строки, — так видно, что файл был на месте
// в момент вызова и что в нём исходный текст.
func fakeWireGuard(journal string) int {
	if os.Getenv(ttFakeHang) != "" {
		time.Sleep(10 * time.Second)
	}
	args := os.Args[1:]
	line := strings.Join(args, " ")
	if len(args) == 2 && args[0] == "/installtunnelservice" {
		data, err := os.ReadFile(args[1])
		if err != nil {
			line += " | файл: " + err.Error()
		} else {
			first, _, _ := strings.Cut(string(data), "\n")
			line += " | файл: " + first
		}
	}
	f, err := os.OpenFile(journal, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 3
	}
	fmt.Fprintln(f, line)
	f.Close()

	if code := os.Getenv(ttFakeExit); code != "" {
		fmt.Fprintln(os.Stderr, "подставной wireguard.exe: отказ по заказу теста")
		return 1
	}
	return 0
}

// externalEnv — datapath с подставной программой и журналом её вызовов.
type externalEnv struct {
	t       *testing.T
	dir     string
	journal string

	// service подменяет проверку службы: настоящей службы в тестах нет.
	// По умолчанию служба «работает».
	service func(ctx context.Context, name string) error
}

func newExternalEnv(t *testing.T) *externalEnv {
	t.Helper()
	dir := t.TempDir()
	journal := filepath.Join(dir, "journal.txt")
	t.Setenv(ttFakeWireGuard, journal)
	return &externalEnv{t: t, dir: dir, journal: journal,
		service: func(context.Context, string) error { return nil }}
}

func (x *externalEnv) datapath(opts ExternalOptions) *External {
	x.t.Helper()
	exe, err := os.Executable()
	if err != nil {
		x.t.Fatalf("os.Executable: %v", err)
	}
	opts.Program = exe
	if opts.Dir == "" {
		opts.Dir = filepath.Join(x.dir, "conf")
	}
	dp := NewExternal(opts)
	dp.serviceRunning = x.service
	return dp
}

func (x *externalEnv) confPath() string {
	return filepath.Join(x.dir, "conf", "tt.conf")
}

func (x *externalEnv) calls() []string {
	x.t.Helper()
	data, err := os.ReadFile(x.journal)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		x.t.Fatalf("журнал: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func parsedServerConfig(t *testing.T) *WGConfig {
	t.Helper()
	conf, err := ParseWgQuick(serverConfig())
	if err != nil {
		t.Fatalf("ParseWgQuick: %v", err)
	}
	return conf
}

// Полный круг: Start пишет файл с исходным текстом и ставит службу по его
// полному пути; файл живёт, пока стоит служба, — она читает его при каждом
// запуске (живая проверка H2). Close снимает службу и только потом удаляет файл.
func TestExternalInstallsAndUninstalls(t *testing.T) {
	x := newExternalEnv(t)
	var logs []string
	dp := x.datapath(ExternalOptions{Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})

	if err := dp.Start(context.Background(), parsedServerConfig(t)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	confPath := x.confPath()
	if _, err := os.Stat(confPath); err != nil {
		t.Errorf("файла конфига нет, пока стоит служба, а она читает его при запуске: %v", err)
	}
	if got, want := dp.Endpoint().String(), "127.0.0.1:51820"; got != want {
		t.Errorf("Endpoint = %s, ожидался %s", got, want)
	}

	if err := dp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(confPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("файл конфига с приватным ключом остался после Close: %v", err)
	}
	if err := dp.Close(); err != nil {
		t.Errorf("повторный Close: %v", err)
	}

	calls := x.calls()
	if len(calls) != 2 {
		t.Fatalf("вызовов %d, ожидалось 2:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	abs, _ := filepath.Abs(confPath)
	if want := "/installtunnelservice " + abs + " | файл: [Interface]"; calls[0] != want {
		t.Errorf("установка:\n%q\nожидалось\n%q", calls[0], want)
	}
	if want := "/uninstalltunnelservice tt"; calls[1] != want {
		t.Errorf("снятие: %q, ожидалось %q", calls[1], want)
	}

	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "AllowedIPs покрывает всё") {
		t.Errorf("нет предупреждения о петле при 0.0.0.0/0:\n%s", joined)
	}
	if strings.Contains(joined, testPrivB64) {
		t.Errorf("в логе виден приватный ключ")
	}
}

// Close до Start и без установленной службы ничего не зовёт.
func TestExternalCloseBeforeStart(t *testing.T) {
	x := newExternalEnv(t)
	dp := x.datapath(ExternalOptions{})

	if err := dp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if calls := x.calls(); len(calls) != 0 {
		t.Errorf("wireguard.exe вызван без Start: %v", calls)
	}
	if err := dp.Start(context.Background(), parsedServerConfig(t)); err == nil {
		t.Error("Start после Close прошёл")
	}
}

// Отказ установки: ошибка называет туннель и повторяет вывод программы,
// файл удалён, службу снимать нечего, а повторный Start возможен.
func TestExternalInstallFailure(t *testing.T) {
	x := newExternalEnv(t)
	t.Setenv(ttFakeExit, "1")
	dp := x.datapath(ExternalOptions{})

	err := dp.Start(context.Background(), parsedServerConfig(t))
	if err == nil {
		t.Fatal("Start прошёл при отказе wireguard.exe")
	}
	for _, want := range []string{"установка службы туннеля", "отказ по заказу теста"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет %q: %v", want, err)
		}
	}
	if _, serr := os.Stat(x.confPath()); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("файл конфига остался после отказа: %v", serr)
	}

	if err := dp.Close(); err != nil {
		t.Errorf("Close после отказа: %v", err)
	}
	if calls := x.calls(); len(calls) != 1 {
		t.Errorf("после отказа установки ожидался один вызов, а не %d: %v", len(calls), calls)
	}
}

// Служба поставилась, но не заработала — так было на живой проверке H2,
// когда файл удалялся раньше, чем служба его читала. Start обязан это
// заметить: снять службу, убрать файл и вернуть ошибку с причиной.
func TestExternalServiceDidNotStart(t *testing.T) {
	x := newExternalEnv(t)
	x.service = func(_ context.Context, name string) error {
		if name != "tt" {
			t.Errorf("проверялась служба %q, ожидалась tt", name)
		}
		return errors.New("служба остановилась: Не удается найти указанный файл.")
	}
	dp := x.datapath(ExternalOptions{})

	err := dp.Start(context.Background(), parsedServerConfig(t))
	if err == nil {
		t.Fatal("Start прошёл, хотя служба не запустилась")
	}
	for _, want := range []string{"не запустилась", "Не удается найти указанный файл", "/dumplog"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет %q: %v", want, err)
		}
	}
	if _, serr := os.Stat(x.confPath()); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("файл конфига остался после отказа службы: %v", serr)
	}

	calls := x.calls()
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "/installtunnelservice ") ||
		calls[1] != "/uninstalltunnelservice tt" {
		t.Errorf("ожидались установка и снятие, а было:\n%s", strings.Join(calls, "\n"))
	}

	// Служба уже снята: Close ничего не зовёт, повторный Start возможен.
	if err := dp.Close(); err != nil {
		t.Errorf("Close после отказа: %v", err)
	}
	if got := len(x.calls()); got != 2 {
		t.Errorf("Close после отказа позвал wireguard.exe ещё раз: вызовов %d", got)
	}
}

// Отмена ctx равносильна Close: служба снимается сама.
func TestExternalStopsOnContextCancel(t *testing.T) {
	x := newExternalEnv(t)
	dp := x.datapath(ExternalOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	if err := dp.Start(ctx, parsedServerConfig(t)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if calls := x.calls(); len(calls) == 2 && strings.HasPrefix(calls[1], "/uninstalltunnelservice") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("служба не снята после отмены ctx: %v", x.calls())
}

// Зависшая программа не держит подключение: по сроку она убивается,
// а ошибка говорит, сколько ждали.
func TestExternalProgramTimeout(t *testing.T) {
	x := newExternalEnv(t)
	t.Setenv(ttFakeHang, "1")
	dp := x.datapath(ExternalOptions{Timeout: 200 * time.Millisecond})

	start := time.Now()
	err := dp.Start(context.Background(), parsedServerConfig(t))
	if err == nil {
		t.Fatal("Start прошёл с зависшей программой")
	}
	if !strings.Contains(err.Error(), "не ответил за") {
		t.Errorf("ошибка не про срок: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("ждали дольше срока: %s", time.Since(start))
	}
}

// Имя туннеля становится именем службы и адаптера — правило wireguard-windows.
func TestExternalRejectsBadName(t *testing.T) {
	x := newExternalEnv(t)
	dp := x.datapath(ExternalOptions{Name: "тт туннель"})

	err := dp.Start(context.Background(), parsedServerConfig(t))
	if err == nil || !strings.Contains(err.Error(), "имя туннеля") {
		t.Fatalf("плохое имя принято: %v", err)
	}
	if calls := x.calls(); len(calls) != 0 {
		t.Errorf("wireguard.exe вызван с плохим именем: %v", calls)
	}
}

// Requires: права администратора, найденная программа и предупреждение о петле.
func TestExternalRequires(t *testing.T) {
	x := newExternalEnv(t)
	dp := x.datapath(ExternalOptions{})

	req := dp.Requires()
	if !req.Admin {
		t.Error("Admin = false")
	}
	if len(req.Programs) != 1 || req.Programs[0] != dp.program {
		t.Errorf("Programs = %v, ожидался путь к программе", req.Programs)
	}
	if req.Warning == "" {
		t.Error("Warning пуст")
	}

	// Без программы Requires называет голое имя, а Start отказывает.
	missing := &External{opts: ExternalOptions{Name: "tt", Timeout: time.Second}, programErr: errors.New("нет"), done: make(chan struct{})}
	if got := missing.Requires().Programs; len(got) != 1 || got[0] != "wireguard.exe" {
		t.Errorf("Programs без программы = %v", got)
	}
	if err := missing.Start(context.Background(), parsedServerConfig(t)); err == nil {
		t.Error("Start без программы прошёл")
	}
}
