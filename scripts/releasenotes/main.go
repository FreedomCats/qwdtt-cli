// Команда releasenotes пишет начало описания релиза — блок «Скачать» со
// ссылками на архивы в порядке Windows, Linux, macOS, затем исходники и
// SHA256SUMS.txt. Шаг «Описание релиза» в .github/workflows/release.yml.
//
//	go run ./scripts/releasenotes [-dir dist] [-repo владелец/имя] [-out файл]
//
// Блок нужен потому, что GitHub показывает ассеты релиза по алфавиту, а
// архивы «Source code» — всегда последними, и задать им порядок нельзя.
// Описание стоит над списком ассетов: там порядок наш. Остальное описание
// (что изменилось) дописывает gh release create --generate-notes.
//
// Версия и тег — из имён архивов qwdtt-cli_<версия>_<ОС>_<арх>: релиз
// собирается с -version, равным тегу. Архивы разных версий в одном каталоге,
// каталог без архивов или без SHA256SUMS.txt — ошибка. Написанное печатается.
package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/FreedomCats/qwdtt-cli/scripts/internal/script"
)

const (
	self     = "releasenotes"
	prefix   = "qwdtt-cli_"
	sumsName = "SHA256SUMS.txt"
)

// osOrder — порядок систем в блоке; незнакомые идут после, по алфавиту.
var osOrder = []string{"windows", "linux", "darwin"}

var osTitles = map[string]string{"windows": "Windows", "linux": "Linux", "darwin": "macOS"}

var archTitles = map[string]string{"amd64": "x86-64", "arm64": "ARM64", "386": "x86"}

// osNotes — оговорка к системе, как в README.
var osNotes = map[string]string{"darwin": "только режим `socks`"}

func main() {
	dir := flag.String("dir", "dist", "каталог релиза")
	repo := flag.String("repo", os.Getenv("GITHUB_REPOSITORY"), "репозиторий на GitHub, владелец/имя")
	out := flag.String("out", "", "куда записать описание; пусто — только напечатать")
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	text, err := run(*dir, *repo)
	if err != nil {
		script.Fail(self, err)
	}
	if *out != "" {
		if err := os.WriteFile(*out, []byte(text), 0o644); err != nil {
			script.Fail(self, err)
		}
	}
	fmt.Print(text)
}

// archive — архив релиза, разобранный по имени.
type archive struct {
	name, version, goos, goarch string
}

func run(dir, repo string) (string, error) {
	if repo == "" {
		return "", errors.New("не задан репозиторий: -repo владелец/имя (в Actions — GITHUB_REPOSITORY)")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var archives []archive
	sums := false
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if e.Name() == sumsName {
			sums = true
			continue
		}
		if a, ok := parseArchive(e.Name()); ok {
			archives = append(archives, a)
		}
	}
	if len(archives) == 0 {
		return "", fmt.Errorf("в %s нет архивов %s<версия>_<ОС>_<арх>", dir, prefix)
	}
	if !sums {
		return "", fmt.Errorf("в %s нет %s", dir, sumsName)
	}
	tag := archives[0].version
	for _, a := range archives[1:] {
		if a.version != tag {
			return "", fmt.Errorf("архивы разных версий: %s и %s", archives[0].name, a.name)
		}
	}
	slices.SortFunc(archives, func(a, b archive) int {
		if c := osRank(a.goos) - osRank(b.goos); c != 0 {
			return c
		}
		if c := strings.Compare(a.goos, b.goos); c != 0 {
			return c
		}
		return strings.Compare(a.goarch, b.goarch)
	})

	base := "https://github.com/" + repo
	download := func(name string) string {
		return base + "/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(name)
	}

	var b strings.Builder
	b.WriteString("## Скачать\n\n")
	for _, a := range archives {
		fmt.Fprintf(&b, "- **%s** (%s", title(osTitles, a.goos), title(archTitles, a.goarch))
		if note := osNotes[a.goos]; note != "" {
			b.WriteString(", " + note)
		}
		fmt.Fprintf(&b, "): [%s](%s)\n", a.name, download(a.name))
	}
	src := base + "/archive/refs/tags/" + url.PathEscape(tag)
	fmt.Fprintf(&b, "- **Исходники**: [zip](%s.zip) · [tar.gz](%s.tar.gz)\n", src, src)
	fmt.Fprintf(&b, "- **Контрольные суммы SHA-256**: [%s](%s)\n", sumsName, download(sumsName))
	fmt.Fprintf(&b, "\nС чего начать — [README](%s/blob/%s/README.md).\n", base, url.PathEscape(tag))
	return b.String(), nil
}

// parseArchive разбирает qwdtt-cli_<версия>_<ОС>_<арх>.zip или .tar.gz.
// Версия может содержать «_» только теоретически: ОС и арх берутся с конца.
func parseArchive(name string) (archive, bool) {
	stem, ok := strings.CutSuffix(name, ".zip")
	if !ok {
		if stem, ok = strings.CutSuffix(name, ".tar.gz"); !ok {
			return archive{}, false
		}
	}
	rest, ok := strings.CutPrefix(stem, prefix)
	if !ok {
		return archive{}, false
	}
	parts := strings.Split(rest, "_")
	if len(parts) < 3 {
		return archive{}, false
	}
	n := len(parts)
	return archive{
		name:    name,
		version: strings.Join(parts[:n-2], "_"),
		goos:    parts[n-2],
		goarch:  parts[n-1],
	}, true
}

func osRank(goos string) int {
	if i := slices.Index(osOrder, goos); i >= 0 {
		return i
	}
	return len(osOrder)
}

func title(titles map[string]string, key string) string {
	if t, ok := titles[key]; ok {
		return t
	}
	return key
}
