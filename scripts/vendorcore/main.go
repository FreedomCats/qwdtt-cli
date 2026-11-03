// Команда vendorcore переносит ядро qWDTT из апстрима в internal/core.
//
//	go run ./scripts/vendorcore
//	go run ./scripts/vendorcore -upstream D:\src\proxy-turn-vk-android
//	go run ./scripts/vendorcore -check
//
// Единственный способ обновить ядро. Правки внутри апстримных файлов держатся
// механическими и живут только здесь (решение Р7 в doc/architecture.md), поэтому
// обновление сводится к запуску этой команды и просмотру диффа.
//
// Что делает:
//
//  1. Копирует *.go из <upstream>/go_client в internal/core, кроме файлов,
//     которые на десктопе не нужны вовсе (см. excludedFiles в rules.go).
//  2. Меняет package main на package core.
//  3. Применяет платформенные правки, без которых ядро не собирается
//     под Windows и macOS (fileOverrides, generatedFiles).
//  4. Превращает ядро в библиотеку: вырезает func main(), заменяет маркеры
//     stdout на события, а log.Printf — на logf, и снимает осиротевшие импорты.
//  5. Записывает internal/core/CORE_VERSION с коммитом апстрима.
//
// Команда идемпотентна: повторный запуск на том же апстриме даёт тот же результат.
// Файлы, которых нет ни в апстриме, ни в списке генерируемых, не трогаются —
// так собственный код (api.go, events.go) переживает обновление ядра.
//
// -upstream — каталог клона proxy-turn-vk-android; по умолчанию — соседний
// с этим репозиторием. -check ничего не пишет, только сообщает, разошёлся ли
// internal/core с апстримом; код возврата 1, если есть расхождения.
//
// With -check the clone must be on the commit recorded in
// internal/core/CORE_VERSION: on another commit the comparison would answer a
// different question, so the command says so, names both commits and what to
// do, and exits with code 1. Uncommitted changes to go_client in the clone are
// reported too: the comparison then runs against the clone's working tree, not
// against the commit.
//
// Перенесена с PowerShell (scripts/vendor-core.ps1) и даёт тот же
// internal/core байт в байт, кроме строки в CORE_VERSION о том, чем он создан.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/FreedomCats/qwdtt-cli/scripts/internal/script"
)

const self = "vendorcore"

func main() {
	upstream := flag.String("upstream", "", "каталог клона proxy-turn-vk-android (по умолчанию — соседний с репозиторием)")
	check := flag.Bool("check", false, "ничего не писать, только сообщить о расхождениях (код 1, если они есть)")
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}

	root, err := script.Root()
	if err != nil {
		script.Fail(self, err)
	}
	code, err := run(root, *upstream, *check)
	if err != nil {
		script.Fail(self, err)
	}
	os.Exit(code)
}

type upstreamVersion struct {
	Commit, Date string
	Dirty        bool
}

func getUpstreamVersion(path string) (upstreamVersion, error) {
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return upstreamVersion{Commit: "unknown", Date: "unknown"}, nil
	}
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = path
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return string(out), nil
	}
	commit, err := git("rev-parse", "HEAD")
	if err != nil {
		return upstreamVersion{}, err
	}
	date, err := git("log", "-1", "--format=%cI")
	if err != nil {
		return upstreamVersion{}, err
	}
	status, err := git("status", "--porcelain", "--", "go_client")
	if err != nil {
		return upstreamVersion{}, err
	}
	return upstreamVersion{
		Commit: strings.TrimSpace(commit),
		Date:   strings.TrimSpace(date),
		Dirty:  strings.TrimSpace(status) != "",
	}, nil
}

// coreCommit takes the upstream commit the core was vendored from out of the
// CORE_VERSION text. An empty string means nothing is pinned: there is no file
// yet, or it has no commit line. run writes the file as `key = value`, lines
// starting with `#` are comments, and dirty may carry a trailing comment after
// `#`. The function only reads text: no files, no git.
func coreCommit(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "commit" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// checkNotice is a line -check prints before the comparison; an empty Color
// means plain output.
type checkNotice struct {
	Color, Text string
}

// checkPreamble decides whether internal/core can be compared with the clone at
// all and what to say about it. pinned is the commit from CORE_VERSION; empty
// means nothing is pinned and everything stays as before. stop means do not
// compare: on a foreign commit a replacement rule will not match, and its error
// would hide the real cause. Neither git nor the disk is touched here: the
// input is the already collected information about the clone.
func checkPreamble(pinned string, v upstreamVersion) (stop bool, notes []checkNotice) {
	if pinned == "" {
		return false, nil
	}
	// run writes `commit = unknown` when the upstream has no .git: there is no
	// commit to compare with and no checkout that would make sense.
	if strings.EqualFold(pinned, "unknown") {
		return false, []checkNotice{{Text: "CORE_VERSION has no commit: the core has to be vendored again. Comparing with go_client as it lies."}}
	}
	if v.Commit == "unknown" {
		return false, []checkNotice{{Text: "The upstream clone has no .git: the commit cannot be checked, comparing with the go_client files as they lie."}}
	}
	if !strings.EqualFold(pinned, v.Commit) {
		return true, []checkNotice{
			{Color: script.Yellow, Text: fmt.Sprintf("The upstream clone is on %s, but internal/core was vendored from %s.", v.Commit, pinned)},
			{Text: "  to check the core:            git -C <clone> checkout " + pinned},
			{Text: "  to vendor the clone's commit: run without -check"},
		}
	}
	if v.Dirty {
		return false, []checkNotice{
			{Color: script.Yellow, Text: fmt.Sprintf("go_client in the clone has uncommitted changes: the comparison runs against the working tree, not against commit %s. See git -C <clone> status -- go_client.", pinned)},
		}
	}
	return false, nil
}

// readText читает файл как Get-Content -Raw -Encoding UTF8: без BOM.
func readText(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))), nil
}

// goFiles — имена файлов *.go в dir (без учёта регистра расширения, как
// фильтр Get-ChildItem на Windows), в порядке Sort-Object.
func goFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(strings.ToLower(e.Name()), ".go") {
			names = append(names, e.Name())
		}
	}
	script.SortNames(names)
	return names, nil
}

func containsFold(list []string, name string) bool {
	return slices.ContainsFunc(list, func(s string) bool { return strings.EqualFold(s, name) })
}

func excludedNames() []string {
	names := make([]string, len(excludedFiles))
	for i, f := range excludedFiles {
		names[i] = f.Name
	}
	return names
}

// planned — что должно лежать в internal/core: имя и содержимое, по порядку.
type planned struct {
	names []string
	text  map[string]string
}

// set добавляет файл или заменяет его; имена — без учёта регистра.
func (p *planned) set(name, text string) {
	for _, n := range p.names {
		if strings.EqualFold(n, name) {
			p.text[n] = text
			return
		}
	}
	p.names = append(p.names, name)
	p.text[name] = text
}

func run(root, upstream string, check bool) (int, error) {
	target := filepath.Join(root, "internal", "core")
	if upstream == "" {
		upstream = filepath.Join(filepath.Dir(root), "proxy-turn-vk-android")
	}
	source := filepath.Join(upstream, "go_client")
	if fi, err := os.Stat(source); err != nil || !fi.IsDir() {
		return 0, fmt.Errorf("не найден каталог ядра: %s. Укажите клон апстрима через -upstream", source)
	}

	// --- сборка нового содержимого ----------------------------------------------

	version, err := getUpstreamVersion(upstream)
	if err != nil {
		return 0, err
	}

	// With -check, first find out whether the clone can answer the question at
	// all: is it on the pinned commit, and is go_client clean. In write mode
	// switching to another commit is the point of the command, so there is no
	// such check there.
	if check {
		pinned := ""
		if text, err := readText(filepath.Join(target, "CORE_VERSION")); err == nil {
			pinned = coreCommit(text)
		}
		stop, notes := checkPreamble(pinned, version)
		for _, n := range notes {
			if n.Color != "" {
				script.Color(n.Color, n.Text)
			} else {
				script.Say(n.Text)
			}
		}
		if stop {
			return 1, nil
		}
	}

	upstreamNames, err := goFiles(source)
	if err != nil {
		return 0, err
	}
	if len(upstreamNames) == 0 {
		return 0, fmt.Errorf("в %s нет файлов *.go", source)
	}

	// Исключённый файл, пропавший из апстрима, — повод остановиться: скорее всего
	// его переименовали, и вместе с ним вернулось то, что мы выбрасывали.
	excluded := excludedNames()
	for _, name := range excluded {
		if !containsFold(upstreamNames, name) {
			return 0, fmt.Errorf("исключённого файла %s нет в %s — апстрим его переименовал или удалил", name, source)
		}
	}

	var sourceNames []string
	for _, name := range upstreamNames {
		if !containsFold(excluded, name) {
			sourceNames = append(sourceNames, name)
		}
	}

	// Проверка счётчиков внутри convertFile не сработает, если апстрим просто
	// переименовал файл: правило тогда не выполнится ни разу и пропадёт молча.
	for _, r := range replacements {
		if !containsFold(sourceNames, r.File) {
			return 0, fmt.Errorf("правило замены ссылается на %s, которого нет в %s", r.File, source)
		}
	}

	header := []string{
		"# Версия ядра qWDTT, перенесённого в internal/core.",
		"# Файл создаётся командой go run ./scripts/vendorcore — руками не править.",
		"",
		"upstream = https://github.com/SpaceNeuroX/proxy-turn-vk-android",
		"commit   = " + version.Commit,
		"date     = " + version.Date,
		fmt.Sprintf("files    = %d", len(sourceNames)),
	}
	if version.Dirty {
		header = append(header, "dirty    = true  # в апстриме есть незакоммиченные правки go_client")
	}

	plan := &planned{text: map[string]string{}}
	for _, name := range sourceNames {
		text, err := readText(filepath.Join(source, name))
		if err != nil {
			return 0, err
		}
		converted, err := convertFile(name, text)
		if err != nil {
			return 0, err
		}
		plan.set(name, converted)
	}
	var generated []string
	for name := range generatedFiles {
		generated = append(generated, name)
	}
	script.SortNames(generated)
	for _, name := range generated {
		plan.set(name, generatedFiles[name])
	}
	plan.set("CORE_VERSION", strings.Join(header, "\n")+"\n")

	// Единый вид записи: LF и ровно один перевод строки в конце.
	//
	// Хвостовой перевод строки обязателен: без него gofmt считает файл
	// неотформатированным — и тогда `gofmt -l` показывает всё ядро разом и
	// перестаёт что-либо значить.
	for _, name := range plan.names {
		text := strings.ReplaceAll(plan.text[name], "\r\n", "\n")
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		plan.text[name] = text
	}

	// Имена, которыми распоряжается команда. Всё остальное в internal/core — наше.
	owned := slices.Concat(sourceNames, generated, excluded)

	// --- режим проверки ---------------------------------------------------------

	if check {
		var problems []string
		for _, name := range plan.names {
			path := filepath.Join(target, name)
			if fi, err := os.Stat(path); err != nil || fi.IsDir() {
				problems = append(problems, "нет файла: "+name)
				continue
			}
			current, err := readText(path)
			if err != nil {
				return 0, err
			}
			if strings.ReplaceAll(current, "\r\n", "\n") != plan.text[name] {
				problems = append(problems, "расходится: "+name)
			}
		}
		for _, f := range excludedFiles {
			if fi, err := os.Stat(filepath.Join(target, f.Name)); err == nil && !fi.IsDir() {
				problems = append(problems, fmt.Sprintf("лишний файл: %s (%s)", f.Name, f.Reason))
			}
		}

		if len(problems) > 0 {
			script.Color(script.Yellow, fmt.Sprintf("internal/core разошёлся с апстримом %s:", version.Commit))
			for _, p := range problems {
				script.Say("  " + p)
			}
			return 1, nil
		}
		script.Color(script.Green, fmt.Sprintf("internal/core совпадает с апстримом %s.", version.Commit))
		return 0, nil
	}

	// --- запись -----------------------------------------------------------------

	if err := os.MkdirAll(target, 0o755); err != nil {
		return 0, err
	}

	// Свои файлы в internal/core команда не трогает, только называет.
	present, err := goFiles(target)
	if err != nil {
		return 0, err
	}
	for _, name := range present {
		if !containsFold(owned, name) {
			script.Say("  не трогаю свой файл: " + name)
		}
	}

	for _, name := range excluded {
		path := filepath.Join(target, name)
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			if err := os.Remove(path); err != nil {
				return 0, err
			}
			script.Say("  удалён исключённый файл: " + name)
		}
	}

	for _, name := range plan.names {
		// UTF-8 без BOM и с LF: так дифф против апстрима не забивается концами строк.
		if err := os.WriteFile(filepath.Join(target, name), []byte(plan.text[name]), 0o644); err != nil {
			return 0, err
		}
	}

	script.Color(script.Green, fmt.Sprintf("Перенесено из апстрима: %d", len(sourceNames)))
	script.Sayf("Создано скриптом:       %d", len(generatedFiles))
	script.Sayf("Исключено:              %d", len(excludedFiles))
	script.Sayf("Апстрим: %s от %s", version.Commit, version.Date)
	if version.Dirty {
		script.Color(script.Yellow, "ВНИМАНИЕ: в апстриме есть незакоммиченные правки go_client.")
	}
	script.Say("")
	script.Say("Дальше: go mod tidy && go build ./...")
	return 0, nil
}
