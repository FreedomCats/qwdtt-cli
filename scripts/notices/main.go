// Команда notices собирает THIRD_PARTY_NOTICES.txt — лицензии всего, что
// слинковано в qwdtt-cli.
//
//	go run ./scripts/notices -dest build/out [-goos windows] [-goarch amd64]
//
// Список берётся из того, что реально попадает в бинарь для заданной
// платформы, с учётом replace в go.mod:
//
//	go list -deps -json ./cmd/qwdtt-cli   (GOOS, GOARCH, CGO_ENABLED=0)
//
// `go list -m all` для этого не годится: в нём и тестовые зависимости, и
// модули, из которых в бинарь ничего не попало. Сам Go (рантайм и стандартная
// библиотека) в этот список не входит — его LICENSE и PATENTS берутся из
// GOROOT и идут первыми.
//
// У каждого модуля из корня его каталога берутся файлы LICENSE*, COPYING*,
// PATENTS* и NOTICE* и кладутся целиком. Вид лицензии определяется по тексту
// основного файла. Команда останавливается, если у модуля нет файла лицензии
// (и модуль не описан в noLicenseFile) или вид лицензии не распознан: новая
// зависимость должна пройти через человека, а не тихо уехать в релиз.
//
// wintun.dll сюда не входит: её лицензия лежит в архиве отдельным файлом
// wintun-LICENSE.txt (scripts/fetch-wintun.ps1), в уведомлениях — ссылка на
// него.
//
// Файл пишется в UTF-8 без BOM; для Windows — с переводами строк CRLF.
// В репозиторий не коммитится, генерируется на каждую сборку.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const self = "github.com/FreedomCats/qwdtt-cli"

// noLicenseFile — модули без файла лицензии, код которых взят из Go: вместо
// файла кладётся лицензия Go с пояснением.
var noLicenseFile = map[string]string{
	// Форк net/http. Файла лицензии в модуле нет, в заголовках исходников —
	// «Copyright … The Go Authors»: это код Go под его BSD-3.
	"github.com/bogdanfinn/fhttp": "В модуле нет файла лицензии. Это форк net/http из стандартной\n" +
		"библиотеки Go, в заголовках исходников — \"Copyright ... The Go\n" +
		"Authors\", поэтому ниже приведена лицензия Go.",
}

// notes — пояснения к отдельным модулям.
var notes = map[string]string{
	"github.com/bogdanfinn/tls-client": "Лицензия BSD-4-Clause. Пункт 3 касается рекламных материалов;\n" +
		"у qwdtt-cli их нет.",
}

func main() {
	dest := flag.String("dest", "", "каталог, куда положить THIRD_PARTY_NOTICES.txt (обязателен)")
	goos := flag.String("goos", "windows", "платформа, для которой собирается список")
	goarch := flag.String("goarch", "amd64", "архитектура")
	flag.Parse()
	if *dest == "" || flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*dest, *goos, *goarch); err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
}

func run(dest, goos, goarch string) error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	goroot, goversion, err := goEnv()
	if err != nil {
		return err
	}
	mods, err := listModules(root, goos, goarch)
	if err != nil {
		return err
	}
	sections, err := collect(mods, goroot, goversion)
	if err != nil {
		return err
	}
	text := render(sections, goos, goarch)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	out := filepath.Join(dest, "THIRD_PARTY_NOTICES.txt")
	if err := os.WriteFile(out, []byte(text), 0o644); err != nil {
		return err
	}
	fmt.Printf("Уведомления: %s (%d компонентов)\n", out, len(sections))
	return nil
}

// --- список модулей --------------------------------------------------------

type module struct {
	Path    string
	Version string
	Dir     string
	Replace *module
}

func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", errors.New("запускать из модуля qwdtt-cli")
	}
	return filepath.Dir(gomod), nil
}

func goEnv() (goroot, goversion string, err error) {
	out, err := exec.Command("go", "env", "GOROOT", "GOVERSION").Output()
	if err != nil {
		return "", "", fmt.Errorf("go env: %w", err)
	}
	// По строкам, а не по пробелам: GOROOT бывает «C:\Program Files\Go».
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n")), "\n")
	if len(lines) != 2 {
		return "", "", fmt.Errorf("go env: неожиданный вывод %q", out)
	}
	return lines[0], lines[1], nil
}

// listModules — модули, из которых в ./cmd/qwdtt-cli для goos/goarch попал хоть
// один пакет; replace уже применён, сам проект исключён.
func listModules(root, goos, goarch string) ([]module, error) {
	cmd := exec.Command("go", "list", "-deps", "-json=Standard,Module", "./cmd/qwdtt-cli")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	return parseModules(out)
}

func parseModules(listJSON []byte) ([]module, error) {
	seen := map[string]module{}
	dec := json.NewDecoder(bytes.NewReader(listJSON))
	for {
		var p struct {
			Standard bool
			Module   *module
		}
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("разбор go list: %w", err)
		}
		if p.Standard || p.Module == nil {
			continue
		}
		m := *p.Module
		if m.Replace != nil {
			m = *m.Replace
		}
		if m.Path == self {
			continue
		}
		seen[m.Path] = module{Path: m.Path, Version: m.Version, Dir: m.Dir}
	}
	mods := make([]module, 0, len(seen))
	for _, m := range seen {
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool {
		return strings.ToLower(mods[i].Path) < strings.ToLower(mods[j].Path)
	})
	return mods, nil
}

// --- лицензии --------------------------------------------------------------

var (
	reLicenseFile = regexp.MustCompile(`^(?:LICEN[CS]E|COPYING|PATENTS|NOTICE)`)
	reMainFile    = regexp.MustCompile(`^(?:LICEN[CS]E|COPYING)`)
	reSpace       = regexp.MustCompile(`\s+`)
	reISC         = regexp.MustCompile(`Permission to use, copy, modify, and(?:/or)? distribute`)
)

// licenseFiles — файлы лицензии из корня dir, основной (LICENSE, COPYING)
// первым: по нему определяется вид лицензии.
func licenseFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var main, rest []string
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !reLicenseFile.MatchString(name) {
			continue
		}
		if reMainFile.MatchString(name) {
			main = append(main, filepath.Join(dir, name))
		} else {
			rest = append(rest, filepath.Join(dir, name))
		}
	}
	return append(main, rest...), nil
}

// classify — вид лицензии по тексту или "", если он не распознан.
func classify(text string) string {
	t := reSpace.ReplaceAllString(text, " ")
	has := func(s string) bool { return strings.Contains(t, s) }
	switch {
	case has("Apache License") && has("Version 2.0"):
		return "Apache-2.0"
	case has("Permission is hereby granted, free of charge"):
		return "MIT"
	case reISC.MatchString(t):
		return "ISC"
	case has("Redistribution and use in source and binary forms"):
		switch {
		case has("advertising materials"):
			return "BSD-4-Clause"
		case has("endorse or promote"):
			return "BSD-3-Clause"
		default:
			return "BSD-2-Clause"
		}
	}
	return ""
}

func readText(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), " \t\n"), nil
}

// --- разделы ---------------------------------------------------------------

type section struct {
	Title string
	Kind  string
	Note  string
	Files []licenseText
}

type licenseText struct {
	Name string
	Text string
}

func readAll(paths []string) ([]licenseText, error) {
	files := make([]licenseText, 0, len(paths))
	for _, p := range paths {
		text, err := readText(p)
		if err != nil {
			return nil, err
		}
		files = append(files, licenseText{Name: filepath.Base(p), Text: text})
	}
	return files, nil
}

func collect(mods []module, goroot, goversion string) ([]section, error) {
	goFiles, err := readAll([]string{filepath.Join(goroot, "LICENSE"), filepath.Join(goroot, "PATENTS")})
	if err != nil {
		return nil, fmt.Errorf("лицензия Go: %w", err)
	}
	goKind := classify(goFiles[0].Text)
	sections := []section{{
		Title: "Go " + strings.TrimPrefix(goversion, "go") + " (рантайм и стандартная библиотека)",
		Kind:  goKind,
		Files: goFiles,
	}}

	var problems []string
	for _, m := range mods {
		s := section{Title: m.Path + " " + m.Version, Note: notes[m.Path]}
		paths, err := licenseFiles(m.Dir)
		if err == nil {
			s.Files, err = readAll(paths)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.Path, err)
		}
		switch note, ok := noLicenseFile[m.Path]; {
		case len(s.Files) > 0:
			s.Kind = classify(s.Files[0].Text)
			if s.Kind == "" {
				problems = append(problems, fmt.Sprintf("%s: вид лицензии в %s не распознан", m.Path, s.Files[0].Name))
			}
		case ok:
			s.Kind, s.Files, s.Note = goKind, goFiles, note
		default:
			problems = append(problems, fmt.Sprintf("%s: нет файла лицензии в %s", m.Path, m.Dir))
		}
		sections = append(sections, s)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("уведомления не собраны — разберите вручную:\n  %s", strings.Join(problems, "\n  "))
	}
	return sections, nil
}

// --- текст -----------------------------------------------------------------

func render(sections []section, goos, goarch string) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\n") }
	rule := strings.Repeat("=", 79)

	line("Сторонние компоненты qwdtt-cli")
	line("=======================")
	line("")
	line(fmt.Sprintf("Сборка %s/%s. В qwdtt-cli входит код компонентов ниже; у каждого —", goos, goarch))
	line("вид лицензии и полный текст её файлов из исходников компонента.")
	line("Сам qwdtt-cli распространяется под GNU GPL v3.0 (файл LICENSE).")
	if goos == "windows" {
		line("")
		line("wintun.dll — отдельный бинарь от WireGuard LLC, распространяется")
		line("без изменений; его лицензия — в файле wintun-LICENSE.txt рядом.")
	}
	line("")
	line("Содержание:")
	line("")
	width := 0
	for _, s := range sections {
		width = max(width, len([]rune(s.Title)))
	}
	for _, s := range sections {
		pad := strings.Repeat(" ", width-len([]rune(s.Title)))
		line("  " + s.Title + pad + "  " + s.Kind)
	}
	for _, s := range sections {
		line("")
		line("")
		line(rule)
		line(s.Title + " — " + s.Kind)
		line(rule)
		if s.Note != "" {
			line("")
			line(s.Note)
		}
		for _, f := range s.Files {
			line("")
			line("--- " + f.Name + " ---")
			line("")
			line(f.Text)
		}
	}

	text := b.String()
	if goos == "windows" {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}
