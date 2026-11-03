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
		"\t\tcaptchaManualWebViewTimeout   = 60 * time.Second\n" +
		"\t\tcaptchaSelectedWebViewTimeout = 120 * time.Second\n" +
		"\t\tlog.Printf(\"[STREAM %d] [КАПЧА] WBV: режим из настроек Android (attempt %d)\", streamID, attempt)\n}\n"
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
		"captchaSelectedWebViewTimeout = 180 * time.Second",
		"\t\tlogf(\"[STREAM %d] [КАПЧА] режим wv: сразу окно капчи (attempt %d)\", streamID, attempt)\n",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("нет %q в\n%s", line, got)
		}
	}
	if strings.Contains(got, "Android") {
		t.Errorf("строка режима wv по-прежнему ссылается на Android:\n%s", got)
	}
}

// Окна, на которые ядро зовёт человека, — manual и selected — по три минуты;
// машинное auto остаётся как в апстриме. Фрагмент — как в апстримном creds.go.
func TestConvertCaptchaTimeouts(t *testing.T) {
	in := "package main\n\nconst (\n" +
		"\tcaptchaAutoWebViewTimeout     = 10 * time.Second\n" +
		"\tcaptchaManualWebViewTimeout   = 60 * time.Second\n" +
		"\tcaptchaSelectedWebViewTimeout = 120 * time.Second\n)\n"

	// Остальные правила creds.go в этот фрагмент не попадают, а каждое
	// обязано совпасть: оставляем только сроки окон.
	saved := replacements
	t.Cleanup(func() { replacements = saved })
	replacements = nil
	for _, r := range saved {
		if r.File == "creds.go" && strings.Contains(r.Pattern, "WebViewTimeout") {
			replacements = append(replacements, r)
		}
	}
	if len(replacements) != 2 {
		t.Fatalf("правил сроков окон капчи %d, ожидалось 2", len(replacements))
	}

	got, err := convertFile("creds.go", in)
	if err != nil {
		t.Fatal(err)
	}
	want := "package core\n\nconst (\n" +
		"\tcaptchaAutoWebViewTimeout     = 10 * time.Second\n" +
		"\tcaptchaManualWebViewTimeout   = 180 * time.Second\n" +
		"\tcaptchaSelectedWebViewTimeout = 180 * time.Second\n)\n"
	if got != want {
		t.Errorf("получилось:\n%s\nожидалось:\n%s", got, want)
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

// Конфиг WireGuard просит каждый воркер, пока он не получен, а группа без
// кредов передаёт эстафету следующей. Фрагмент — как в апстримном group.go.
func TestConvertGroupConfig(t *testing.T) {
	in := "package main\n\nfunc g() {\n" +
		"\t\tlog.Printf(\"[ГРУППА #%d] Ошибка кредов: %v\", groupID, err)\n\t\treturn\n" +
		"\tvar configRequestInFlight int32\n" +
		"\t\t\t\tgetConf := false\n" +
		"\t\t\t\tif shouldGetConfig && atomic.LoadInt32(&configSent) == 0 {\n" +
		"\t\t\t\t\tgetConf = atomic.CompareAndSwapInt32(&configRequestInFlight, 0, 1)\n" +
		"\t\t\t\t}\n" +
		"\t\t\t\tif getConf {\n" +
		"\t\t\t\t\tif configDelivered {\n" +
		"\t\t\t\t\t\tatomic.StoreInt32(&configSent, 1)\n" +
		"\t\t\t\t\t} else {\n" +
		"\t\t\t\t\t\tatomic.StoreInt32(&configRequestInFlight, 0)\n" +
		"\t\t\t\t\t}\n" +
		"\t\t\t\t}\n}\n"

	// Остальные правила group.go в этот фрагмент не попадают, а каждое
	// обязано совпасть: оставляем только проверяемые.
	saved := replacements
	t.Cleanup(func() { replacements = saved })
	replacements = nil
	for _, r := range saved {
		if r.File == "group.go" && (strings.Contains(r.Pattern, "Ошибка кредов") || strings.Contains(r.Pattern, "configRequestInFlight") ||
			strings.Contains(r.Pattern, "configDelivered")) {
			replacements = append(replacements, r)
		}
	}
	if len(replacements) != 4 {
		t.Fatalf("правил конфига и эстафеты %d, ожидалось 4", len(replacements))
	}

	got, err := convertFile("group.go", in)
	if err != nil {
		t.Fatal(err)
	}
	want := "package core\n\nfunc g() {\n" +
		"\t\tlogf(\"[ГРУППА #%d] Ошибка кредов: %v\", groupID, err)\n" +
		"\t\temitHashFailed(hash, groupID, err)\n" +
		"\t\tif signalReady != nil {\n\t\t\tclose(signalReady)\n\t\t}\n\t\treturn\n" +
		"\t\t\t\tgetConf := shouldGetConfig && atomic.LoadInt32(&configSent) == 0\n" +
		"\t\t\t\tif getConf && configDelivered {\n" +
		"\t\t\t\t\tatomic.StoreInt32(&configSent, 1)\n" +
		"\t\t\t\t}\n}\n"
	if got != want {
		t.Errorf("получилось:\n%s\nожидалось:\n%s", got, want)
	}
}

// CORE_VERSION is written by run itself: keys padded with spaces, and dirty
// carries a trailing comment after #. The header lines are the ones run writes.
// Empty text and text without a commit line mean nothing is pinned.
func TestCoreCommit(t *testing.T) {
	cases := map[string]string{
		"# Версия ядра qWDTT, перенесённого в internal/core.\n" +
			"# Файл создаётся командой go run ./scripts/vendorcore — руками не править.\n" +
			"\n" +
			"upstream = https://github.com/SpaceNeuroX/proxy-turn-vk-android\n" +
			"commit   = fae121efc3ef57b633516601d3c0d6b1be1fde7c\n" +
			"date     = 2026-08-26T17:50:02+02:00\n" +
			"files    = 19\n": "fae121efc3ef57b633516601d3c0d6b1be1fde7c",

		"upstream = https://github.com/SpaceNeuroX/proxy-turn-vk-android\n" +
			"commit   = a296c57eaba69bb9479a24f9157856490890e47d\n" +
			"date     = 2026-08-01T00:00:00+00:00\n" +
			"files    = 19\n" +
			"dirty    = true  # в апстриме есть незакоммиченные правки go_client\n": "a296c57eaba69bb9479a24f9157856490890e47d",

		"upstream = https://github.com/SpaceNeuroX/proxy-turn-vk-android\n" +
			"date     = 2026-08-01T00:00:00+00:00\n" +
			"files    = 19\n": "",

		"": "",
	}
	for text, want := range cases {
		if got := coreCommit(text); got != want {
			t.Errorf("coreCommit(%q) = %q, want %q", text, got, want)
		}
	}
}

const (
	pinnedA = "fae121efc3ef57b633516601d3c0d6b1be1fde7c"
	otherB  = "a296c57eaba69bb9479a24f9157856490890e47d"
)

// Same commit, clean clone: compare silently.
func TestCheckPreambleSameCommit(t *testing.T) {
	stop, notes := checkPreamble(pinnedA, upstreamVersion{Commit: pinnedA})
	if stop || len(notes) != 0 {
		t.Errorf("stop=%v, notes: %v", stop, notes)
	}
}

// Another commit: stop, name both commits and the checkout command.
func TestCheckPreambleOtherCommit(t *testing.T) {
	stop, notes := checkPreamble(pinnedA, upstreamVersion{Commit: otherB})
	if !stop {
		t.Fatalf("stop=false, notes: %v", notes)
	}
	text := ""
	for _, n := range notes {
		text += n.Text + "\n"
	}
	for _, want := range []string{pinnedA, otherB, "git -C <clone> checkout " + pinnedA} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
}

// Hashes are compared case-insensitively: git prints them lower case, but they
// may end up in the file in any case.
func TestCheckPreambleCommitCase(t *testing.T) {
	stop, notes := checkPreamble(strings.ToUpper(pinnedA), upstreamVersion{Commit: pinnedA})
	if stop || len(notes) != 0 {
		t.Errorf("stop=%v, notes: %v", stop, notes)
	}
}

// Clone without .git: the commit cannot be checked, but that does not prevent
// comparing.
func TestCheckPreambleUnknownCommit(t *testing.T) {
	stop, notes := checkPreamble(pinnedA, upstreamVersion{Commit: "unknown", Date: "unknown"})
	if stop {
		t.Fatalf("stop=true, notes: %v", notes)
	}
	text := ""
	for _, n := range notes {
		text += n.Text + "\n"
	}
	if !strings.Contains(text, "cannot be checked") {
		t.Errorf("no line about the commit in\n%s", text)
	}
}

// run writes commit = unknown when the upstream has no .git. There is nothing
// to compare with and no checkout to suggest: vendor the core again.
func TestCheckPreamblePinnedUnknown(t *testing.T) {
	for _, v := range []upstreamVersion{
		{Commit: pinnedA},
		{Commit: pinnedA, Dirty: true},
		{Commit: "unknown", Date: "unknown"},
	} {
		stop, notes := checkPreamble("unknown", v)
		if stop {
			t.Fatalf("%v: stop=true, notes: %v", v, notes)
		}
		if len(notes) != 1 {
			t.Fatalf("%v: %d notes, want one: %v", v, len(notes), notes)
		}
		if !strings.Contains(notes[0].Text, "has no commit") || strings.Contains(notes[0].Text, "checkout") {
			t.Errorf("%v: %q", v, notes[0].Text)
		}
	}
}

// Pinned commit, but go_client is dirty: warn and compare on.
func TestCheckPreambleDirty(t *testing.T) {
	stop, notes := checkPreamble(pinnedA, upstreamVersion{Commit: pinnedA, Dirty: true})
	if stop {
		t.Fatalf("stop=true, notes: %v", notes)
	}
	if len(notes) != 1 {
		t.Fatalf("%d notes, want one: %v", len(notes), notes)
	}
	for _, want := range []string{"uncommitted changes", pinnedA, "git -C <clone> status -- go_client"} {
		if !strings.Contains(notes[0].Text, want) {
			t.Errorf("no %q in %q", want, notes[0].Text)
		}
	}
}

// Nothing pinned (no CORE_VERSION yet): everything as before, silently.
func TestCheckPreambleNothingPinned(t *testing.T) {
	for _, v := range []upstreamVersion{
		{Commit: otherB, Dirty: true},
		{Commit: otherB},
		{Commit: "unknown", Date: "unknown"},
	} {
		if stop, notes := checkPreamble("", v); stop || len(notes) != 0 {
			t.Errorf("%v: stop=%v, notes: %v", v, stop, notes)
		}
	}
}
