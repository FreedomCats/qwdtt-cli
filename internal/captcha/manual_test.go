package captcha

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func manualRequest() Request {
	return Request{
		Session:      3,
		Mode:         ModeManual,
		RedirectURI:  "https://id.vk.com/captcha?sid=12345&session_token=abc",
		SessionToken: "abc",
		Deadline:     time.Now().Add(3 * time.Minute),
	}
}

// solveWith прогоняет решатель на подставленном вводе.
func solveWith(t *testing.T, input string) (string, string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	m := NewManual(ManualOptions{In: strings.NewReader(input), Out: out})
	token, err := m.Solve(context.Background(), manualRequest())
	return token, out.String(), err
}

// ──────────────────────── удачный разбор ────────────────────────

func TestManualAcceptsBareToken(t *testing.T) {
	const token = "vk1.a.kTq9wS3nR8bXpL0yZ7hJ4mN2cV6dF1gA"

	got, _, err := solveWith(t, token+"\n")
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if got != token {
		t.Errorf("токен = %q, ожидался %q", got, token)
	}
}

// Человек под таймером копирует то, что попалось: целую строку из консоли,
// кусок ответа с именем поля или ответ VK целиком. Всё это принимается.
func TestManualAcceptsSloppyInput(t *testing.T) {
	const token = "vk1.a.kTq9wS3nR8bXpL0yZ7hJ4mN2cV6dF1gA"

	cases := map[string]string{
		"строка из консоли целиком": "qwdtt-cli-captcha token " + token,
		"с лишними пробелами":       "   " + token + "   ",
		"ответ VK целиком":          `{"response":{"success_token":"` + token + `"}}`,
		"кусок JSON с полем":        `"success_token":"` + token + `"`,
		"поле через равно":          "success_token=" + token,
	}

	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			got, _, err := solveWith(t, input+"\n")
			if err != nil {
				t.Fatalf("Solve: %v", err)
			}
			if got != token {
				t.Errorf("токен = %q, ожидался %q", got, token)
			}
		})
	}
}

// ──────────────────────── три случая неверного ввода ────────────────────────

// Первый: пользователь ничего не ввёл.
func TestManualEmptyInput(t *testing.T) {
	for name, input := range map[string]string{
		"пустая строка": "\n",
		"конец ввода":   "",
		"одни пробелы":  "   \n",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := solveWith(t, input)
			if !errors.Is(err, ErrCancelled) {
				t.Errorf("ошибка = %v, ожидалась ErrCancelled", err)
			}
		})
	}
}

// Второй: ввёл мусор.
func TestManualGarbageInput(t *testing.T) {
	for name, input := range map[string]string{
		"короткое слово":     "готово",
		"несколько коротких": "не знаю что тут",
		"поле без значения":  `"success_token":""`,
		"JSON без токена":    `{"response":{}}`,
		"JSON с ошибкой VK":  `{"error":{"error_code":14}}`,
		"неразбираемый JSON": `{"response":`,
	} {
		t.Run(name, func(t *testing.T) {
			token, _, err := solveWith(t, input+"\n")
			if err == nil {
				t.Fatalf("мусор %q принят как токен %q", input, token)
			}
			if errors.Is(err, ErrCancelled) {
				t.Errorf("мусор принят за отказ: %v", err)
			}
		})
	}
}

// Третий: ввёл ссылку. Объяснение обязано быть по существу — это ровно то
// заблуждение, с которого начиналась задача.
func TestManualURLInput(t *testing.T) {
	_, _, err := solveWith(t, "https://id.vk.com/captcha?sid=12345&success=1\n")
	if err == nil {
		t.Fatal("ссылка принята как токен")
	}
	if !strings.Contains(err.Error(), "адресной строке") {
		t.Errorf("объяснение = %q, а оно должно говорить, что токена в адресе не бывает", err)
	}
}

// Слайдер — отдельный исход, не просто отказ: решать его можно, просто дольше.
func TestManualSliderIsItsOwnError(t *testing.T) {
	_, _, err := solveWith(t, `{"response":{"show_captcha_type":"slider"}}`+"\n")
	if !errors.Is(err, ErrSlider) {
		t.Errorf("ошибка = %v, ожидалась ErrSlider", err)
	}
}

// ──────────────────────── инструкция ────────────────────────

func TestManualPromptHasEverythingNeeded(t *testing.T) {
	req := manualRequest()
	out := &bytes.Buffer{}
	m := NewManual(ManualOptions{In: strings.NewReader("\n"), Out: out})
	_, _ = m.Solve(context.Background(), req)

	text := out.String()
	for _, want := range []string{
		req.RedirectURI,           // куда идти
		"captchaNotRobot.check",   // из перехватчика: он напечатан целиком
		"qwdtt-cli-captcha token", // что искать глазами
		"F12",                     // чем открыть консоль
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в инструкции нет %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "3m0s") {
		t.Errorf("в инструкции не сказано, сколько осталось времени:\n%s", text)
	}
}

// Однострочник обязан быть однострочником: он идёт в консоль браузера одним
// куском, и перенос строки посреди него пользователь вставит как две команды.
func TestConsoleSnippetIsOneLine(t *testing.T) {
	snippet := consoleSnippet()
	if strings.ContainsAny(snippet, "\n\r\t") {
		t.Error("в однострочнике есть перевод строки или табуляция")
	}
	if !strings.Contains(snippet, "captchaNotRobot.check") {
		t.Error("однострочник растерял то, ради чего он нужен")
	}
}

// Открытие браузера — дело вызывающего, и его неудача не срывает решение.
func TestManualBrowserFailureIsNotFatal(t *testing.T) {
	const token = "vk1.a.kTq9wS3nR8bXpL0yZ7hJ4mN2cV6dF1gA"
	out := &bytes.Buffer{}

	var asked string
	m := NewManual(ManualOptions{
		In:   strings.NewReader(token + "\n"),
		Out:  out,
		Open: func(u string) error { asked = u; return errors.New("браузера нет") },
	})

	got, err := m.Solve(context.Background(), manualRequest())
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if got != token {
		t.Errorf("токен = %q", got)
	}
	if asked != manualRequest().RedirectURI {
		t.Errorf("открыть просили %q", asked)
	}
	if !strings.Contains(out.String(), "откройте вручную") {
		t.Errorf("про неудачу браузера пользователю не сказали:\n%s", out.String())
	}
}

// ──────────────────────── отмена ────────────────────────

// Ctrl+C во время ожидания ответа: Solve возвращается, не дожидаясь Enter.
func TestManualReturnsOnCancel(t *testing.T) {
	blocked, unblock := context.WithCancel(context.Background())
	defer unblock()

	// Читатель, который никогда ничего не отдаёт, — это stdin терминала,
	// за которым никого нет.
	m := NewManual(ManualOptions{In: blockingReader{done: blocked.Done()}, Out: &bytes.Buffer{}})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.Solve(ctx, manualRequest())
		done <- err
	}()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ошибка = %v, ожидалась context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Solve не вернулся после отмены")
	}
}

type blockingReader struct{ done <-chan struct{} }

func (r blockingReader) Read([]byte) (int, error) {
	<-r.done
	return 0, os.ErrClosed
}

// ──────────────────── перехватчик в настоящем движке ────────────────────

// Однострочник проверяется там, где ему предстоит работать: в движке JS.
// Разбор ответа и Go-сторона могут быть безупречны, а строка — не выполняться
// вовсе, и узнать об этом на живой капче под таймером было бы поздно.
func TestInterceptorRunsInJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node не найден — проверка перехватчика пропущена")
	}

	const token = "vk1.a.kTq9wS3nR8bXpL0yZ7hJ4mN2cV6dF1gA"

	// Заглушки браузера: fetch и XMLHttpRequest ровно настолько, насколько
	// их трогает перехватчик.
	harness := `
const body = JSON.stringify({response: {success_token: ` + quoteJS(token) + `}});
const seen = [];
globalThis.window = globalThis;
globalThis.console = {log: (...args) => seen.push(args.join(' '))};
globalThis.fetch = async (url) => ({clone: () => ({text: async () => body})});
// open и send обязаны лежать на прототипе: перехватчик подменяет именно его,
// а собственное свойство экземпляра эту подмену затенило бы.
globalThis.XMLHttpRequest = function () { this.listeners = {}; };
XMLHttpRequest.prototype.addEventListener = function (name, fn) { this.listeners[name] = fn; };
XMLHttpRequest.prototype.open = function () {};
XMLHttpRequest.prototype.send = function () {
	this.responseText = body;
	if (this.listeners.load) { this.listeners.load(); }
};
` + consoleSnippet() + `
(async () => {
	await fetch('https://id.vk.com/method/captchaNotRobot.check?v=5.275');
	await new Promise(r => setImmediate(r));
	const xhr = new XMLHttpRequest();
	xhr.open('POST', 'https://id.vk.com/method/captchaNotRobot.check');
	xhr.send();
	process.stdout.write(seen.join('\n'));
})();
`

	path := filepath.Join(t.TempDir(), "harness.mjs")
	if err := os.WriteFile(path, []byte(harness), 0o600); err != nil {
		t.Fatalf("запись стенда: %v", err)
	}

	out, err := exec.Command(node, path).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}

	got := string(out)
	if strings.Count(got, "qwdtt-cli-captcha token "+token) != 2 {
		t.Errorf("перехватчик не поймал токен дважды (fetch и XHR):\n%s", got)
	}
}

// quoteJS — строковый литерал JS. Токен в тесте свой и безобидный, но собирать
// код конкатенацией без кавычек — привычка, которую лучше не заводить.
func quoteJS(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}
