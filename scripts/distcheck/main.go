// Команда distcheck проверяет состав архивов релиза в dist/ — шаг «Состав
// архивов» в .github/workflows/release.yml.
//
//	go run ./scripts/distcheck [-dir dist]
//
// В архиве — всё, что нужно для запуска у пользователя: без wintun.dll
// не работает режим tun, без README не понять, с чего начать. Список тот же,
// что раскладывает scripts/release; выпал файл — релиза нет.
//
//   - zip: все записи — внутри каталога с именем архива, с разделителем «/»
//     (с «\» архив на Linux и macOS распакуется в файлы с обратными слэшами в
//     именах); есть qwdtt-cli.exe, wintun.dll, wintun-LICENSE.txt и документы;
//     на Windows qwdtt-cli.exe из распакованного архива запускается
//     (`qwdtt-cli.exe version`);
//   - tar.gz: есть qwdtt-cli и документы, у qwdtt-cli — право на исполнение
//     (-rwxr-xr-x).
//
// Ошибки печатаются аннотациями GitHub (::error::), код возврата — 1, если
// была хоть одна.
//
// Перенесена с PowerShell (шаг release.yml).
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/FreedomCats/qwdtt-cli/scripts/internal/script"
)

const self = "distcheck"

var docs = []string{"THIRD_PARTY_NOTICES.txt", "qwdtt-cli.example.toml", "LICENSE", "README.md"}

func main() {
	dir := flag.String("dir", "dist", "каталог с архивами")
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	failed, err := run(*dir)
	if err != nil {
		script.Fail(self, err)
	}
	if failed {
		os.Exit(1)
	}
}

type checker struct{ failed bool }

func (c *checker) errorf(format string, a ...any) {
	script.Sayf("::error::"+format, a...)
	c.failed = true
}

// files сверяет имена got со списком want — без учёта регистра, как
// -notcontains.
func (c *checker) files(archive string, got, want []string) {
	var missing []string
	for _, w := range want {
		if !slices.ContainsFunc(got, func(g string) bool { return strings.EqualFold(g, w) }) {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		c.errorf("%s — нет файлов: %s", archive, strings.Join(missing, ", "))
		return
	}
	script.Sayf("%s — %s", archive, strings.Join(got, ", "))
}

// archives — файлы dir с суффиксом suffix, в порядке Sort-Object.
func archives(dir, suffix string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(strings.ToLower(e.Name()), suffix) {
			names = append(names, e.Name())
		}
	}
	script.SortNames(names)
	return names, nil
}

func run(dir string) (bool, error) {
	c := &checker{}

	zips, err := archives(dir, ".zip")
	if err != nil {
		return false, err
	}
	for _, name := range zips {
		if err := c.zip(dir, name); err != nil {
			return false, err
		}
	}

	tgzs, err := archives(dir, ".tar.gz")
	if err != nil {
		return false, err
	}
	for _, name := range tgzs {
		if err := c.tarGz(dir, name); err != nil {
			return false, err
		}
	}
	return c.failed, nil
}

func (c *checker) zip(dir, name string) error {
	path := filepath.Join(dir, name)
	if err := c.zipEntries(path, name); err != nil {
		return err
	}

	// qwdtt-cli.exe из распакованного архива запускается — только на Windows.
	if runtime.GOOS != "windows" {
		script.Sayf("%s — qwdtt-cli.exe version не запускался: это не Windows", name)
		return nil
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	tmp := os.Getenv("RUNNER_TEMP")
	if tmp == "" {
		tmp = os.TempDir()
	}
	unpacked := filepath.Join(tmp, base)
	if err := unzip(path, unpacked); err != nil {
		return err
	}
	cmd := exec.Command(filepath.Join(unpacked, base, "qwdtt-cli.exe"), "version")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return err
		}
		c.errorf("%s — qwdtt-cli.exe version завершился с кодом %d", name, exit.ExitCode())
	}
	return nil
}

// zipEntries проверяет записи zip: каталог и состав.
func (c *checker) zipEntries(path, name string) error {
	z, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	var entries []string
	for _, f := range z.File {
		entries = append(entries, f.Name)
	}
	z.Close()

	base := strings.TrimSuffix(name, filepath.Ext(name))
	prefix := base + "/"
	var outside, names []string
	for _, e := range entries {
		if !strings.HasPrefix(e, prefix) {
			outside = append(outside, e)
		}
		if rest := e[min(len(prefix), len(e)):]; rest != "" {
			names = append(names, rest)
		}
	}
	if len(outside) > 0 {
		c.errorf("%s — вне каталога %s: %s", name, prefix, strings.Join(outside, ", "))
	}
	c.files(name, names, append([]string{"qwdtt-cli.exe", "wintun.dll", "wintun-LICENSE.txt"}, docs...))
	return nil
}

// unzip распаковывает архив в dir поверх того, что там есть (Expand-Archive
// -Force). Запись, выходящая за dir, — ошибка.
func unzip(path, dir string) error {
	z, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, f := range z.File {
		target := filepath.Join(dir, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(filepath.Separator)) {
			return fmt.Errorf("%s: запись вне каталога: %s", path, f.Name)
		}
		if strings.HasSuffix(f.Name, "/") {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			r.Close()
			return err
		}
		_, err = io.Copy(out, r)
		r.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *checker) tarGz(dir, name string) error {
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("tar не прочитал %s: %w", name, err)
	}
	tr := tar.NewReader(gz)

	prefix := name[:len(name)-len(".tar.gz")] + "/"
	var names []string
	executable := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar не прочитал %s: %w", name, err)
		}
		entry := h.Name
		if len(entry) >= len(prefix) && strings.EqualFold(entry[:len(prefix)], prefix) {
			entry = entry[len(prefix):]
		}
		if entry != "" {
			names = append(names, entry)
		}
		// То же, что строка «-rwxr-xr-x … /qwdtt-cli» в tar -tv.
		if h.Typeflag == tar.TypeReg && h.Mode&0o7777 == 0o755 &&
			strings.HasSuffix(strings.ToLower(h.Name), "/qwdtt-cli") {
			executable = true
		}
	}
	c.files(name, names, append([]string{"qwdtt-cli"}, docs...))
	if !executable {
		c.errorf("%s — у qwdtt-cli нет права на исполнение", name)
	}
	return nil
}
