package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func writeZip(t *testing.T, path string, names ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, n := range names {
		if _, err := zw.Create(n); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func writeTarGz(t *testing.T, path string, modes map[string]int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for n, m := range modes {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: m, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	f.Close()
}

// Полный tar.gz и tar.gz без права на исполнение у qwdtt-cli.
func TestTarGz(t *testing.T) {
	dir := t.TempDir()
	full := map[string]int64{"r_linux/qwdtt-cli": 0o755}
	for _, d := range docs {
		full["r_linux/"+d] = 0o644
	}
	writeTarGz(t, filepath.Join(dir, "r_linux.tar.gz"), full)
	if failed, err := run(dir); err != nil || failed {
		t.Fatalf("годный архив: %v, %v", failed, err)
	}

	full["r_linux/qwdtt-cli"] = 0o644
	writeTarGz(t, filepath.Join(dir, "r_linux.tar.gz"), full)
	if failed, err := run(dir); err != nil || !failed {
		t.Errorf("без права на исполнение: %v, %v", failed, err)
	}
}

// zip с записью вне каталога и без wintun.dll — провал.
func TestZipComposition(t *testing.T) {
	dir := t.TempDir()
	names := []string{"r_windows/qwdtt-cli.exe", "r_windows/wintun-LICENSE.txt", "stray.txt"}
	for _, d := range docs {
		names = append(names, "r_windows/"+d)
	}
	writeZip(t, filepath.Join(dir, "r_windows.zip"), names...)
	c := &checker{}
	if err := c.zipEntries(filepath.Join(dir, "r_windows.zip"), "r_windows.zip"); err != nil {
		t.Fatal(err)
	}
	if !c.failed {
		t.Error("архив без wintun.dll и с файлом вне каталога прошёл")
	}
}
