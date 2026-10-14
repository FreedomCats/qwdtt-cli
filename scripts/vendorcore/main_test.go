package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Сверка с апстримом целиком — `go run ./scripts/vendorcore -check`: клона
// апстрима в тестах нет. Здесь — правила по отдельности.

func TestConvertMarkers(t *testing.T) {
	in := "package main\n\nimport (\n\t\"fmt\"\n\t\"log\"\n\t\"os\"\n)\n\n" +
		"func check(hashes []string) {\n\tfor i, hash := range hashes {\n" +
		"\t\tfmt.Printf(\"HASH_CHECK_START|%d|%s\\n\", i+1, hash)\n" +
		"\t\tfmt.Printf(\"HASH_CHECK|%d|%s|%s|%s\\n\", i+1, hash, status, sanitizeHashCheckMessage(message))\n" +
		"\t\tlog.Printf(\"x\")\n\t}\n}\n\n" +
		"func classify(err error) {\n\ttext := strings.ToLower(err.Error())\n}\n\nfunc main() {\n\tos.Exit(1)\n}\n"
	got, err := convertFile("main.go", in)
	if err != nil {
		t.Fatal(err)
	}
	want := "package core\n\nimport (\n\t\"fmt\"\n)\n\n" +
		"func check(hashes []string) {\n\tfor i, hash := range hashes {\n" +
		"\t\temitHashCheck(i+1, len(hashes), hash, HashCheckRunning, \"\")\n" +
		"\t\temitHashCheck(i+1, len(hashes), hash, HashCheckStatus(status), sanitizeHashCheckMessage(message))\n" +
		"\t\tlogf(\"x\")\n\t}\n}\n\n" +
		"func classify(err error) {\n\tif callErr, ok := asCallUnavailableError(err); ok {\n" +
		"\t\treturn \"dead\", fmt.Sprintf(\"Звонок не найден или закрыт (error_code=%d)\", callErr.Code)\n" +
		"\t}\n\ttext := strings.ToLower(err.Error())\n}\n"
	if got != want {
		t.Errorf("получилось:\n%s\nожидалось:\n%s", got, want)
	}
}

// Вставка встаёт с отступом совпавшей строки — до или после неё.
func TestConvertInsert(t *testing.T) {
	in := "package main\n\nfunc f() {\n\t\tdialer := &net.Dialer{\n\t\t\tTimeout: 1,\n\t\t}\n" +
		"\t\tsuccessToken, solveErr := solveCaptchaBySelectedMode(ctx, streamID, attempt+1, captchaErr, client, profile, savedProfile)\n" +
		"\t\ttlsclient.WithCookieJar(jar),\n" +
		"\t\tlog.Printf(\"[STREAM %d] [Captcha] Solve failed: %v\", streamID, solveErr)\n" +
		"\t\tcaptchaAttempt := captchaErr.CaptchaAttempt\n" +
		"\t\tfmt.Printf(\"CAPTCHA_SOLVE|%s|%s|%s\\n\", mode, captchaErr.RedirectURI, captchaErr.SessionToken)\n" +
		"\t\tcaptchaManualWebViewTimeout   = 60 * time.Second\n}\n"
	got, err := convertFile("creds.go", in)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"\t\tdialer := &net.Dialer{\n\t\t\tControl:   guardedControl,\n\t\t\tTimeout: 1,\n",
		"\t\temitStage(StageCaptcha, StageRunning, streamID, \"\")\n\t\tsuccessToken, solveErr :=",
		"\t\ttlsclient.WithCookieJar(jar),\n\t\ttlsclient.WithDialer(guardedDialer()),\n",
		"solveErr)\n\t\temitStage(StageCaptcha, StageFailed, streamID, solveErr.Error())\n",
		"\t\temitStage(StageCaptcha, StageOK, streamID, \"\")\n\t\tcaptchaAttempt :=",
		"\t\temit(EventCaptchaRequired{Session: streamID,",
		"captchaManualWebViewTimeout   = 180 * time.Second",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("нет %q в\n%s", line, got)
		}
	}
}

// Шаблон не совпал — ошибка с именем файла и шаблоном, а не тихий пропуск.
func TestConvertCountMismatch(t *testing.T) {
	_, err := convertFile("vk_account.go", "package main\n")
	if err == nil || !strings.Contains(err.Error(), "vk_account.go") || !strings.Contains(err.Error(), "найдено 0") {
		t.Errorf("ошибка: %v", err)
	}
}

// log.Fatal ищется без учёта регистра и только с границы слова.
func TestConvertLogFatal(t *testing.T) {
	for in, fails := range map[string]bool{
		"package main\nfunc f() { log.Fatal(1) }\n":   true,
		"package main\nfunc f() { Log.fatalf(1) }\n":  true,
		"package main\nfunc f() { mylog.Fatal(1) }\n": false,
		"package main\nfunc f() { бlog.Fatal(1) }\n":  false,
	} {
		_, err := convertFile("x.go", in)
		if (err != nil) != fails {
			t.Errorf("%q: %v", in, err)
		}
	}
}

// \b у .NET знает кириллицу: перед «log.» буква — замены нет. RE2 со своим
// ASCII-\b заменил бы.
func TestReplaceWord(t *testing.T) {
	cases := map[string]string{
		"log.Printf(a) log.Printf(b)": "logf(a) logf(b)",
		"xlog.Printf(a)":              "xlog.Printf(a)",
		"жlog.Printf(a)":              "жlog.Printf(a)",
		"(log.Printf(a)":              "(logf(a)",
		"log.Printf(log.Printf(a))":   "logf(logf(a))",
	}
	for in, want := range cases {
		if got := replaceWord(in, "log.Printf(", "logf("); got != want {
			t.Errorf("%q: %q, ожидалось %q", in, got, want)
		}
	}
}

// Импорт снимается, только если имени пакета в файле больше нет.
func TestPruneImports(t *testing.T) {
	in := "package main\n\nimport (\n\t\"net\"\n\t\"os/signal\"\n\t\"strconv\"\n\t\"sync\"\n)\n\nvar _ = strconv.Itoa\nvar s sync.Mutex\n"
	got, err := convertFile("x.go", in)
	if err != nil {
		t.Fatal(err)
	}
	want := "package core\n\nimport (\n\t\"strconv\"\n\t\"sync\"\n)\n\nvar _ = strconv.Itoa\nvar s sync.Mutex\n"
	if got != want {
		t.Errorf("получилось:\n%s", got)
	}
}

// Апстрим без нужных правилам файлов — ошибка до записи.
func TestRunMissingRuleFile(t *testing.T) {
	root := t.TempDir()
	up := filepath.Join(t.TempDir(), "up")
	src := filepath.Join(up, "go_client")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range excludedFiles {
		if err := os.WriteFile(filepath.Join(src, f.Name), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := run(root, up, false)
	if err == nil || !strings.Contains(err.Error(), "правило замены ссылается") {
		t.Errorf("run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "core")); !os.IsNotExist(err) {
		t.Errorf("internal/core создан: %v", err)
	}
}
