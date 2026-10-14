package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Порядок Sort-Object, LF, прежний SHA256SUMS.txt не считается.
func TestRun(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"b.zip": "", "A.tar.gz": "abc", outName: "old"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := run(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad  A.tar.gz\n" +
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  b.zip\n"
	if got != want {
		t.Errorf("получилось:\n%s", got)
	}
	if file, _ := os.ReadFile(filepath.Join(dir, outName)); string(file) != want {
		t.Errorf("в файле:\n%s", file)
	}
}
