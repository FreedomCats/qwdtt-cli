package script

import (
	"slices"
	"testing"
)

// Порядок — тот, что дал Sort-Object в pwsh 7.4 на тех же именах.
func TestSortNames(t *testing.T) {
	names := []string{"creds.go", "creds_vkcalls.go", "captcha_v2.go", "captcha_v2_slider.go",
		"README.md", "LICENSE", "qwdtt-cli", "qwdtt-cli.example.toml", "THIRD_PARTY_NOTICES.txt",
		"a-b.go", "a_b.go", "ab.go", "a.b.go", "A.go", "a.go", "listen_windows.go",
		"listen_unix.go", "listen.go", "raw_diag.go", "rawx.go", "x9.go", "x10.go"}
	want := []string{"a_b.go", "a-b.go", "a.b.go", "a.go", "A.go", "ab.go",
		"captcha_v2_slider.go", "captcha_v2.go", "creds_vkcalls.go", "creds.go", "LICENSE",
		"listen_unix.go", "listen_windows.go", "listen.go", "qwdtt-cli", "qwdtt-cli.example.toml",
		"raw_diag.go", "rawx.go", "README.md", "THIRD_PARTY_NOTICES.txt", "x10.go", "x9.go"}
	SortNames(names)
	if !slices.Equal(names, want) {
		t.Errorf("порядок:\n%q\nожидался\n%q", names, want)
	}
}
