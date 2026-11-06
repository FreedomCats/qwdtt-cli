package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/config"
	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

const (
	deadHash = "bbbbbbbbbbbbbbbb"
	newHash  = "dddddddddddddddd"
)

func TestMenuCheckHashesDispatchesCaptchaAndPreservesFiltering(t *testing.T) {
	isolateCaptchaResults(t)
	solver := &diagnosticSolver{}
	e, stdout, _ := diagnosticControlEnv(solver)
	hashes := []string{"dead-hash-madeup", "live-hash-madeup", "captcha-hash-madeup", "limited-hash-madeup", "network-hash-madeup", "error-hash-madeup", "unchecked-hash-madeup"}
	statuses := []core.HashCheckStatus{
		core.HashCheckDead, core.HashCheckOK, core.HashCheckCaptcha, core.HashCheckLimited, core.HashCheckNetwork, core.HashCheckError,
	}
	e.hashCheck = func(_ context.Context, cfg core.Config, sink core.Sink) error {
		sink.Emit(core.EventCaptchaRequired{Mode: "selected", RedirectURI: "https://captcha.example/", SessionToken: "made-up-session"})
		var wg sync.WaitGroup
		for i, status := range statuses {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sink.Emit(core.EventHashCheck{Index: i + 1, Total: len(cfg.Hashes), Hash: cfg.Hashes[i], Status: core.HashCheckRunning})
				sink.Emit(core.EventHashCheck{Index: i + 1, Total: len(cfg.Hashes), Hash: cfg.Hashes[i], Status: status})
			}()
		}
		wg.Wait()
		if got := receiveCaptcha(t); got != "made-up-captcha-token" {
			t.Errorf("captcha response = %q", got)
		}
		return nil
	}
	kept := (&menu{e: e}).checkHashes(context.Background(), &config.Profile{}, hashes)
	if strings.Join(kept, " ") != strings.Join(hashes[1:], " ") {
		t.Fatalf("kept hashes = %v, want %v", kept, hashes[1:])
	}
	for i, status := range statuses {
		want := fmt.Sprintf("  %d. %-9s  %s\n", i+1, hashes[i][:8]+"…", hashStatusText(status))
		if strings.Count(stdout.String(), want) != 1 {
			t.Errorf("ordinary hash row %q missing or duplicated:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "хеш 1/") || strings.Contains(stdout.String(), "проверяется") {
		t.Errorf("menu printed command-style or running events:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Закрытых звонков: 1 из 7") {
		t.Errorf("dead-hash summary missing:\n%s", stdout.String())
	}
	solver.assertRequest(t)
	assertCaptchaResultsEmpty(t)
}

type manualPromptNotice struct{ ready chan struct{} }

func (w manualPromptNotice) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "  4. ") {
		w.ready <- struct{}{}
	}
	return len(p), nil
}

func TestMenuManualCaptchaCancellationKeepsNextPrompt(t *testing.T) {
	isolateCaptchaResults(t)
	r, w := inputPipe(t)
	reads, prompts := make(chan struct{}, 16), make(chan struct{}, 16)
	responses := make(chan string, 3)
	e := &env{
		stdin: observedInput{Reader: r, reads: reads}, stdout: io.Discard,
		stderr: manualPromptNotice{ready: prompts},
	}
	e.hashCheck = func(_ context.Context, _ core.Config, sink core.Sink) error {
		sink.Emit(core.EventCaptchaRequired{Mode: "selected", RedirectURI: "https://captcha.example/", SessionToken: "made-up-session"})
		response, err := receiveCaptchaResult()
		if err != nil {
			return err
		}
		responses <- response
		return nil
	}
	m := &menu{e: e, signals: &menuSignals{exit: func() { t.Error("Ctrl+C exited the menu") }}}
	for i := 0; i < 3; i++ {
		done := make(chan []string, 1)
		go func() {
			done <- m.checkHashes(context.Background(), &config.Profile{}, []string{"made-up-hash"})
		}()
		awaitInput(t, prompts)
		if i == 0 {
			awaitInput(t, reads)
		}
		m.signals.handle()
		select {
		case kept := <-done:
			if len(kept) != 1 || kept[0] != "made-up-hash" {
				t.Fatalf("canceled check kept hashes = %v", kept)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("manual captcha did not send a prompt refusal on Ctrl+C")
		}
		select {
		case response := <-responses:
			if response != "error:cancelled" {
				t.Fatalf("captcha response = %q, want error:cancelled", response)
			}
		default:
			t.Fatal("core waiter did not receive the manual captcha refusal")
		}
		assertCaptchaResultsEmpty(t)
	}
	select {
	case <-reads:
		t.Fatal("repeated captcha cancellation started another stdin read")
	default:
	}
	done := make(chan string, 1)
	go func() {
		line, err := m.ask("Menu: ")
		if err != nil {
			line = err.Error()
		}
		done <- line
	}()
	if _, err := io.WriteString(w, "0\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got != "0" {
			t.Errorf("first menu response after captcha cancellation = %q, want 0", got)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled captcha swallowed the first menu response")
	}
}

func TestMenuManualCaptchaSharesBufferedInput(t *testing.T) {
	isolateCaptchaResults(t)
	const token = "made-up-captcha-token"
	e := &env{stdin: strings.NewReader("4\n" + token + "\n0\n"), stdout: io.Discard, stderr: io.Discard}
	if got, err := e.readLine(); err != nil || got != "4" {
		t.Fatalf("initial menu response = %q, %v", got, err)
	}
	if e.input().reader.Buffered() == 0 {
		t.Fatal("test did not buffer the captcha response")
	}
	var checkErr error
	e.hashCheck = func(_ context.Context, cfg core.Config, sink core.Sink) error {
		sink.Emit(core.EventCaptchaRequired{Mode: "selected", RedirectURI: "https://captcha.example/", SessionToken: "made-up-session"})
		response, err := receiveCaptchaResult()
		if err != nil {
			checkErr = err
		} else if response != token {
			checkErr = fmt.Errorf("buffered captcha response = %q, want %q", response, token)
		}
		if checkErr != nil {
			return checkErr
		}
		sink.Emit(core.EventHashCheck{Index: 1, Total: 1, Hash: cfg.Hashes[0], Status: core.HashCheckOK})
		return nil
	}
	m := &menu{e: e}
	m.checkHashes(context.Background(), &config.Profile{}, []string{"made-up-hash"})
	if checkErr != nil {
		t.Fatal(checkErr)
	}
	if got, err := m.ask("Menu: "); err != nil || got != "0" {
		t.Errorf("menu response after captcha = %q, %v; want 0", got, err)
	}
	assertCaptchaResultsEmpty(t)
}

// fakeHashCheck — проверка хешей без сети: названные хеши — закрытые звонки,
// остальные работают.
func fakeHashCheck(dead ...string) func(context.Context, core.Config, core.Sink) error {
	return func(_ context.Context, cfg core.Config, sink core.Sink) error {
		for i, h := range cfg.Hashes {
			status := core.HashCheckOK
			for _, d := range dead {
				if h == d {
					status = core.HashCheckDead
				}
			}
			sink.Emit(core.EventHashCheck{Index: i + 1, Total: len(cfg.Hashes), Hash: h, Status: status})
		}
		return nil
	}
}

func storedHashes(t *testing.T, p *profileEnv) []string {
	t.Helper()
	h := loadTestFile(t, p).Profiles["home"]
	raw, err := config.NewSecretStoreFor(p.configPath()).Get(h.VkHashesRef)
	if err != nil {
		t.Fatal(err)
	}
	return core.ParseHashes(raw)
}

// Закрытый звонок убирается, новые ссылки добавляются к остальным.
func TestMenuUpdateHashesDropsDead(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.hashCheck = fakeHashCheck(deadHash)
	p.answer("https://vk.com/call/join/" + newHash)
	p.input("4\n\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"звонок закрыт", "Закрытых звонков: 1 из 3", "теперь: 3"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("в выводе нет %q:\n%s", want, stdout)
		}
	}
	assertNoSecrets(t, "stdout", stdout)

	got := strings.Join(storedHashes(t, p), " ")
	if got != "aaaaaaaaaaaaaaaa cccccccccccccccc "+newHash {
		t.Errorf("хеши после обновления: %s", got)
	}
}

// Без новых ссылок — только убираются закрытые.
func TestMenuUpdateHashesOnlyRemoves(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.hashCheck = fakeHashCheck(deadHash)
	p.answer("")
	p.input("4\n1\n0\n")

	if code, _, stderr := p.run("menu"); code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if got := strings.Join(storedHashes(t, p), " "); got != "aaaaaaaaaaaaaaaa cccccccccccccccc" {
		t.Errorf("хеши после обновления: %s", got)
	}
}

func TestMenuUpdateHashesReplaceAll(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.hashCheck = fakeHashCheck()
	// Заменить все: пустой ответ не годится, нужна хоть одна ссылка.
	p.answer("", newHash)
	p.input("4\n2\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "Закрытых звонков нет") || !strings.Contains(stderr, "Не нашлось ни одной ссылки") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if got := strings.Join(storedHashes(t, p), " "); got != newHash {
		t.Errorf("хеши после замены: %s", got)
	}
}

// Назад — ничего не меняется; проверка, которая не удалась, ничего не
// выбрасывает.
func TestMenuUpdateHashesBackAfterFailedCheck(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.hashCheck = func(context.Context, core.Config, core.Sink) error { return errors.New("нет сети") }
	p.input("4\n0\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "Проверить ссылки не удалось: нет сети") || !strings.Contains(stdout, "Ссылки не изменены") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if got := len(storedHashes(t, p)); got != 3 {
		t.Errorf("хешей %d, ожидалось 3", got)
	}
}

// Пароль — дважды, несовпадение переспрашивается.
func TestMenuReplacesPassword(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.answer("новый", "другой", "новый", "новый")
	p.input("5\n1\n0\n")

	code, _, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "Пароли не совпали") {
		t.Errorf("нет переспроса:\n%s", stderr)
	}
	h := loadTestFile(t, p).Profiles["home"]
	if got, _ := config.NewSecretStoreFor(p.configPath()).Get(h.PasswordRef); got != "новый" {
		t.Errorf("пароль не заменён: %q", got)
	}
}

// Неверный адрес переспрашивается; без порта дописывается порт по умолчанию.
func TestMenuReplacesPeerRepeatsBadAddress(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.input("5\n2\nexample.org:порт\n198.51.100.7\n0\n")

	code, _, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "Не похоже на адрес") {
		t.Errorf("нет переспроса:\n%s", stderr)
	}
	if got := loadTestFile(t, p).Profiles["home"].Peer; got != "198.51.100.7:56000" {
		t.Errorf("peer = %q", got)
	}
}
