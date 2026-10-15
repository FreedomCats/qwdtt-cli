package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Порядок — Windows, Linux, macOS, незнакомая ОС, исходники, суммы, а не
// алфавит; лишние файлы в каталоге не мешают.
func TestRun(t *testing.T) {
	dir := writeFiles(t,
		"qwdtt-cli_v0.2.0_darwin_arm64.tar.gz",
		"qwdtt-cli_v0.2.0_freebsd_amd64.tar.gz",
		"qwdtt-cli_v0.2.0_linux_amd64.tar.gz",
		"qwdtt-cli_v0.2.0_windows_amd64.zip",
		"SHA256SUMS.txt",
		"notes.txt",
	)
	got, err := run(dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	const d = "https://github.com/owner/repo/releases/download/v0.2.0/"
	want := "## Скачать\n\n" +
		"- **Windows** (x86-64): [qwdtt-cli_v0.2.0_windows_amd64.zip](" + d + "qwdtt-cli_v0.2.0_windows_amd64.zip)\n" +
		"- **Linux** (x86-64): [qwdtt-cli_v0.2.0_linux_amd64.tar.gz](" + d + "qwdtt-cli_v0.2.0_linux_amd64.tar.gz)\n" +
		"- **macOS** (ARM64, только режим `socks`): [qwdtt-cli_v0.2.0_darwin_arm64.tar.gz](" + d + "qwdtt-cli_v0.2.0_darwin_arm64.tar.gz)\n" +
		"- **freebsd** (x86-64): [qwdtt-cli_v0.2.0_freebsd_amd64.tar.gz](" + d + "qwdtt-cli_v0.2.0_freebsd_amd64.tar.gz)\n" +
		"- **Исходники**: [zip](https://github.com/owner/repo/archive/refs/tags/v0.2.0.zip) · " +
		"[tar.gz](https://github.com/owner/repo/archive/refs/tags/v0.2.0.tar.gz)\n" +
		"- **Контрольные суммы SHA-256**: [SHA256SUMS.txt](" + d + "SHA256SUMS.txt)\n" +
		"\nС чего начать — [README](https://github.com/owner/repo/blob/v0.2.0/README.md).\n"
	if got != want {
		t.Errorf("получилось:\n%s\nждали:\n%s", got, want)
	}
}

func TestRunErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		repo  string
		files []string
		want  string
	}{
		{"без репозитория", "", []string{"qwdtt-cli_v1_linux_amd64.tar.gz", "SHA256SUMS.txt"}, "не задан репозиторий"},
		{"без архивов", "o/r", []string{"SHA256SUMS.txt"}, "нет архивов"},
		{"без сумм", "o/r", []string{"qwdtt-cli_v1_linux_amd64.tar.gz"}, "нет SHA256SUMS.txt"},
		{"разные версии", "o/r", []string{"qwdtt-cli_v1_linux_amd64.tar.gz", "qwdtt-cli_v2_windows_amd64.zip", "SHA256SUMS.txt"}, "разных версий"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := run(writeFiles(t, tc.files...), tc.repo)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ошибка %v, ждали «%s»", err, tc.want)
			}
		})
	}
}

func TestParseArchive(t *testing.T) {
	for name, want := range map[string]archive{
		"qwdtt-cli_v0.1.0_windows_amd64.zip":           {"qwdtt-cli_v0.1.0_windows_amd64.zip", "v0.1.0", "windows", "amd64"},
		"qwdtt-cli_v0.1.0-3-gabc_linux_amd64.tar.gz":   {"qwdtt-cli_v0.1.0-3-gabc_linux_amd64.tar.gz", "v0.1.0-3-gabc", "linux", "amd64"},
		"qwdtt-cli_v0.1.0_windows_amd64/qwdtt-cli.exe": {},
		"tt_v0.1.0_linux_amd64.tar.gz":                 {},
		"qwdtt-cli_linux.tar.gz":                       {},
	} {
		got, ok := parseArchive(name)
		if ok != (want.name != "") || got != want {
			t.Errorf("%s: %+v, %v", name, got, ok)
		}
	}
}
