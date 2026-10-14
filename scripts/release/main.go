// Команда release собирает релиз qwdtt-cli: три бинаря, zip для Windows и
// tar.gz для Linux и macOS.
//
//	go run ./scripts/release
//	go run ./scripts/release -version 0.1.0
//	go run ./scripts/release -platforms windows/amd64 -out C:\tmp\qwdtt-cli
//
// Сборка воспроизводима из чистого клона: команде нужны go, git и доступ
// к wintun.net (или уже скачанный архив Wintun, см. -wintun-archive).
// Для каждой платформы из -platforms делается
//
//	go build -trimpath -ldflags "-s -w -X main.version=… -X main.commit=…"
//
// с CGO_ENABLED=0 — cgo в проекте не нужен, а без него кросс-сборка не ищет
// чужой компилятор. Версия и коммит попадают в `qwdtt-cli version` через
// переменные cmd/qwdtt-cli/main.go.
//
// Раскладка результата (по умолчанию dist/):
//
//	dist/qwdtt-cli_<версия>_windows_amd64/qwdtt-cli.exe
//	dist/qwdtt-cli_<версия>_linux_amd64/qwdtt-cli
//	dist/qwdtt-cli_<версия>_darwin_arm64/qwdtt-cli
//	dist/qwdtt-cli_<версия>_windows_amd64.zip
//	    каталог qwdtt-cli_<версия>_windows_amd64/ с qwdtt-cli.exe, wintun.dll,
//	    wintun-LICENSE.txt, THIRD_PARTY_NOTICES.txt, qwdtt-cli.example.toml,
//	    LICENSE, README.md
//	dist/qwdtt-cli_<версия>_linux_amd64.tar.gz
//	dist/qwdtt-cli_<версия>_darwin_arm64.tar.gz
//	    каталог qwdtt-cli_<версия>_<платформа>/ с qwdtt-cli,
//	    THIRD_PARTY_NOTICES.txt, qwdtt-cli.example.toml, LICENSE, README.md
//
// wintun.dll (режим -datapath tun) кладётся в zip пакетом
// scripts/internal/wintun — тем же, что у команды wintun: фиксированная
// версия, проверка SHA256 архива и, на Windows, подписи DLL. В репозитории её
// нет. THIRD_PARTY_NOTICES.txt — лицензии всего, что слинковано в бинарь, —
// собирает `go run ./scripts/notices` на каждую сборку.
//
// Внутри и zip, и tar.gz файлы лежат в каталоге с именем архива: распакованное
// не смешается с соседними файлами. В tar.gz у qwdtt-cli стоит право на
// исполнение (0755), у остальных файлов — 0644, на какой бы системе ни шла
// сборка: архив пишется здесь же, а не tar системы — tar на Windows права на
// исполнение не ставит.
//
// В конце команда запускает `qwdtt-cli version` у бинаря для своей платформы
// и проверяет, что версия и коммит подставились.
//
// Флаги:
//
//	-version         строка версии; по умолчанию `git describe --tags --always --dirty`,
//	                 то есть тег, если он есть, иначе короткий коммит; суффикс
//	                 -dirty при незакоммиченных изменениях
//	-out             каталог результата; по умолчанию dist в корне репозитория.
//	                 Содержимое для собираемых платформ перезаписывается, чужие
//	                 файлы не трогаются
//	-platforms       платформы через запятую в виде os/arch; по умолчанию
//	                 windows/amd64,linux/amd64,darwin/arm64
//	-nozip           не собирать архивы — ни zip, ни tar.gz (и не брать wintun.dll)
//	-wintun-archive  уже скачанный wintun-<версия>.zip — сборка без сети
//
// Перенесена с PowerShell (scripts/build-release.ps1). Те же бинари с тем же
// хешем, те же tar.gz после распаковки, те же записи в zip.
package main

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/FreedomCats/qwdtt-cli/scripts/internal/script"
	"github.com/FreedomCats/qwdtt-cli/scripts/internal/wintun"
)

const self = "release"

type options struct {
	Version       string
	Out           string
	Platforms     []string
	NoZip         bool
	WintunArchive string
}

func main() {
	var o options
	platforms := flag.String("platforms", "windows/amd64,linux/amd64,darwin/arm64", "платформы через запятую в виде os/arch")
	flag.StringVar(&o.Version, "version", "", "строка версии (по умолчанию git describe --tags --always --dirty)")
	flag.StringVar(&o.Out, "out", "", "каталог результата (по умолчанию dist в корне репозитория)")
	flag.BoolVar(&o.NoZip, "nozip", false, "не собирать архивы и не брать wintun.dll")
	flag.StringVar(&o.WintunArchive, "wintun-archive", "", "уже скачанный wintun-"+wintun.Version+".zip — сборка без сети")
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	o.Platforms = strings.Split(*platforms, ",")

	root, err := script.Root()
	if err != nil {
		script.Fail(self, err)
	}
	if err := run(root, o); err != nil {
		script.Fail(self, err)
	}
}

type build struct {
	Platform, GOOS, GOARCH string
	Dir, Name, Exe         string
}

// buildEnv — переменные, которые сборка задаёт go build для платформы и
// снимает после: notices и прочие запуски go идут без них.
var buildEnv = []string{"GOOS", "GOARCH", "CGO_ENABLED"}

// cleanEnv — окружение без buildEnv.
func cleanEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.ContainsFunc(buildEnv, func(v string) bool { return strings.EqualFold(v, name) })
	})
}

// gitOutput — вывод git в корне без пробелов по краям; "" — git завершился
// с ошибкой. Нет самого git — ошибка.
func gitOutput(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// sanitizeVersion — версия попадает в имена каталогов и файлов: слэши и
// пробелы там не нужны.
func sanitizeVersion(v string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || unicode.IsSpace(r) {
			return '-'
		}
		return r
	}, v)
}

func run(root string, o options) error {
	if o.Out == "" {
		o.Out = filepath.Join(root, "dist")
	}

	// --- сведения о сборке ---------------------------------------------------

	version := o.Version
	if version == "" {
		v, err := gitOutput(root, "describe", "--tags", "--always", "--dirty")
		if err != nil {
			return err
		}
		version = v
		if version == "" {
			version = "dev"
		}
	}
	version = sanitizeVersion(version)

	commit, err := gitOutput(root, "rev-parse", "--short", "HEAD")
	if err != nil {
		return err
	}
	if commit == "" {
		commit = "unknown"
	} else {
		status, err := gitOutput(root, "status", "--porcelain")
		if err != nil {
			return err
		}
		if status != "" {
			commit += "-dirty"
		}
	}

	ldflags := fmt.Sprintf("-s -w -X main.version=%s -X main.commit=%s", version, commit)

	script.Sayf("Версия: %s, коммит: %s", version, commit)
	script.Say("Каталог: " + o.Out)

	// --- сборка --------------------------------------------------------------

	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return err
	}

	var built []build
	for _, platform := range o.Platforms {
		parts := strings.Split(platform, "/")
		if len(parts) != 2 {
			return fmt.Errorf("платформа должна быть в виде os/arch: '%s'", platform)
		}
		goos, goarch := parts[0], parts[1]

		name := fmt.Sprintf("qwdtt-cli_%s_%s_%s", version, goos, goarch)
		dir := filepath.Join(o.Out, name)
		exe := "qwdtt-cli"
		if goos == "windows" {
			exe = "qwdtt-cli.exe"
		}
		target := filepath.Join(dir, exe)

		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}

		script.Sayf("Сборка %s/%s → %s", goos, goarch, target)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", target, "./cmd/qwdtt-cli")
		cmd.Dir = root
		cmd.Env = append(cleanEnv(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go build для %s завершился %s", platform, exitText(err))
		}

		built = append(built, build{Platform: platform, GOOS: goos, GOARCH: goarch, Dir: dir, Name: name, Exe: target})
	}

	// --- zip для Windows -----------------------------------------------------

	var archives []string
	if !o.NoZip {
		for _, b := range built {
			if b.GOOS != "windows" {
				continue
			}
			if err := addDocs(root, b); err != nil {
				return err
			}
			if err := wintun.Fetch(wintun.Options{Arch: b.GOARCH, Dest: b.Dir, Archive: o.WintunArchive}); err != nil {
				return err
			}
			if err := notices(root, b); err != nil {
				return err
			}
			archive := filepath.Join(o.Out, b.Name+".zip")
			if err := removeFile(archive); err != nil {
				return err
			}
			// Внутри архива — каталог с именем архива, как в tar.gz: «Извлечь
			// всё» не рассыплет файлы по Загрузкам, а qwdtt-cli.exe не потеряет
			// wintun.dll.
			if err := writeZip(b.Dir, archive); err != nil {
				return err
			}
			archives = append(archives, archive)
			script.Say("Архив: " + archive)
		}
	}

	// --- tar.gz для Linux и macOS --------------------------------------------

	if !o.NoZip {
		for _, b := range built {
			if b.GOOS == "windows" {
				continue
			}
			if err := addDocs(root, b); err != nil {
				return err
			}
			if err := notices(root, b); err != nil {
				return err
			}
			tgz := filepath.Join(o.Out, b.Name+".tar.gz")
			if err := removeFile(tgz); err != nil {
				return err
			}
			if err := writeTarGz(b.Dir, tgz, b.Name, []string{"qwdtt-cli"}); err != nil {
				return err
			}
			archives = append(archives, tgz)
			script.Say("Архив: " + tgz)
		}
	}

	// --- проверка ------------------------------------------------------------

	hostOS, hostArch := runtime.GOOS, runtime.GOARCH
	if hostArch != "amd64" && hostArch != "arm64" {
		hostArch = ""
	}
	native := slices.IndexFunc(built, func(b build) bool { return b.Platform == hostOS+"/"+hostArch })
	if native >= 0 {
		cmd := exec.Command(built[native].Exe, "version")
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("qwdtt-cli version завершился %s", exitText(err))
		}
		versionOut := strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n"))
		lower := strings.ToLower(versionOut)
		if !strings.Contains(lower, strings.ToLower(version)) || !strings.Contains(lower, strings.ToLower(commit)) {
			return fmt.Errorf("qwdtt-cli version не показывает версию и коммит: '%s'", versionOut)
		}
		script.Say("Проверка: " + versionOut)
	} else {
		script.Sayf("Проверка qwdtt-cli version пропущена: бинаря для %s/%s среди собранных нет", hostOS, hostArch)
	}

	// --- итог ----------------------------------------------------------------

	script.Say("")
	script.Say("Готово:")
	for _, b := range built {
		size, err := sizeMB(b.Exe)
		if err != nil {
			return err
		}
		script.Sayf("  %-14s %s  (%s МБ)", b.Platform, b.Exe, size)
	}
	for _, archive := range archives {
		size, err := sizeMB(archive)
		if err != nil {
			return err
		}
		kind := "tar.gz"
		if strings.HasSuffix(archive, ".zip") {
			kind = "zip"
		}
		script.Sayf("  %-14s %s  (%s МБ)", kind, archive, size)
	}
	return nil
}

// addDocs кладёт рядом с бинарём файлы из корня, которые идут в архив.
func addDocs(root string, b build) error {
	for _, extra := range []string{"qwdtt-cli.example.toml", "LICENSE", "README.md"} {
		if err := script.CopyFile(filepath.Join(root, extra), filepath.Join(b.Dir, extra)); err != nil {
			return err
		}
	}
	return nil
}

func notices(root string, b build) error {
	cmd := exec.Command("go", "-C", root, "run", "./scripts/notices", "-dest", b.Dir, "-goos", b.GOOS, "-goarch", b.GOARCH)
	cmd.Env = cleanEnv()
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("уведомления для %s не собраны", b.Platform)
	}
	return nil
}

// exitText — «с кодом N» для завершившейся команды, иначе текст ошибки.
func exitText(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("с кодом %d", exit.ExitCode())
	}
	return "с ошибкой: " + err.Error()
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// sizeMB — размер в мегабайтах с одной цифрой после точки, округлённый к
// чётному, как Math.Round; целое — без «.0».
func sizeMB(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	mb := math.RoundToEven(float64(fi.Size())/(1<<20)*10) / 10
	return strconv.FormatFloat(mb, 'f', -1, 64), nil
}
