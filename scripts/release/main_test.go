package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// tar после распаковки — байт в байт тот, что писал System.Formats.Tar в
// старом build-release.ps1 (testdata/dotnet.tar записан им из тех же файлов).
func TestTarGzMatchesDotNet(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"LICENSE":   "license text\n",
		"qwdtt-cli": "#!/bin/sh\necho hi\n",
		"README.md": "readme\n",
	})
	path := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := writeTarGz(dir, path, "qwdtt-cli_1.2.3_linux_amd64", []string{"qwdtt-cli"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "dotnet.tar"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		for i := range min(len(got), len(want)) {
			if got[i] != want[i] {
				t.Fatalf("расхождение с .NET на байте %d (блок %d, смещение %d): %q, ожидалось %q; длина %d и %d",
					i, i/512, i%512, got[i], want[i], len(got), len(want))
			}
		}
		t.Fatalf("длина %d, у .NET %d", len(got), len(want))
	}
}

// zip — как у Compress-Archive: каталог с именем архива, записи по порядку
// Sort-Object, без записей каталогов и дескрипторов данных, время файла.
func TestZip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "qwdtt-cli_1.2.3_windows_amd64")
	writeFiles(t, dir, map[string]string{
		"wintun.dll":         "dll",
		"wintun-LICENSE.txt": "w",
		"qwdtt-cli.exe":      "exe",
		"README.md":          "readme",
		"LICENSE":            "license",
	})
	mtime := time.Date(2021, 10, 17, 4, 39, 20, 0, time.Local)
	if err := os.Chtimes(filepath.Join(dir, "wintun.dll"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "out.zip")
	if err := writeZip(dir, path); err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()

	want := []string{"LICENSE", "qwdtt-cli.exe", "README.md", "wintun-LICENSE.txt", "wintun.dll"}
	if len(z.File) != len(want) {
		t.Fatalf("записей %d, ожидалось %d", len(z.File), len(want))
	}
	for i, f := range z.File {
		if name := "qwdtt-cli_1.2.3_windows_amd64/" + want[i]; f.Name != name {
			t.Errorf("запись %d: %s, ожидалась %s", i, f.Name, name)
		}
		if f.Flags&0x8 != 0 || f.Method != zip.Deflate || f.CreatorVersion != zipCreator || f.ExternalAttrs != zipAttrs {
			t.Errorf("%s: флаги %#x, метод %d, создатель %#x, атрибуты %#x", f.Name, f.Flags, f.Method, f.CreatorVersion, f.ExternalAttrs)
		}
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if orig, _ := os.ReadFile(filepath.Join(dir, want[i])); !bytes.Equal(body, orig) {
			t.Errorf("%s: содержимое %q", f.Name, body)
		}
	}
	last := z.File[len(z.File)-1]
	if d, c := dosTime(mtime); last.ModifiedDate != d || last.ModifiedTime != c {
		t.Errorf("время wintun.dll: %v", last.Modified)
	}
}

func TestSanitizeVersion(t *testing.T) {
	if got := sanitizeVersion("feature/x y\\z w"); got != "feature-x-y-z-w" {
		t.Errorf("%q", got)
	}
}

func TestSizeMB(t *testing.T) {
	for size, want := range map[int64]string{
		18 << 20:                "18",
		(18 << 20) + (2<<20)/10: "18.2",
		(1 << 20) / 4:           "0.2", // 0.25 к чётному
		(1 << 20) * 3 / 4:       "0.8", // 0.75 к чётному
	} {
		path := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, size); err != nil {
			t.Fatal(err)
		}
		if got, _ := sizeMB(path); got != want {
			t.Errorf("%d байт: %s, ожидалось %s", size, got, want)
		}
	}
}
