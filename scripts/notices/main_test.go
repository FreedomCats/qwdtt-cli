package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	const redist = "Redistribution and use in source and binary forms, with or without\nmodification, are permitted provided that the following conditions are met: "
	cases := []struct {
		name, text, want string
	}{
		{"apache", "Apache License\n   Version 2.0, January 2004", "Apache-2.0"},
		{"mit", "Permission is hereby granted, free of charge, to any person", "MIT"},
		{"isc", "Permission to use, copy, modify, and/or distribute this software", "ISC"},
		{"isc без or", "Permission to use, copy, modify, and distribute this software", "ISC"},
		{"bsd-4", redist + "3. All advertising materials mentioning features", "BSD-4-Clause"},
		{"bsd-3", redist + "Neither the name of Google Inc. nor the names of its\ncontributors may be used to endorse or promote products", "BSD-3-Clause"},
		{"bsd-2", redist + "2. Redistributions in binary form must reproduce", "BSD-2-Clause"},
		{"неизвестная", "All rights reserved. Do whatever.", ""},
	}
	for _, c := range cases {
		if got := classify(c.text); got != c.want {
			t.Errorf("%s: classify = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLicenseFilesMainFirst(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"PATENTS", "LICENSE.md", "NOTICE", "README.md", "license_test.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "LICENSES"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := licenseFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range got {
		names = append(names, filepath.Base(p))
	}
	if want := "LICENSE.md NOTICE PATENTS"; strings.Join(names, " ") != want {
		t.Errorf("licenseFiles = %v, want %s", names, want)
	}
}

func TestParseModules(t *testing.T) {
	list := `{"Standard": true}
{"Module": {"Path": "golang.org/x/sys", "Version": "v0.1.0", "Dir": "/m/sys"}}
{"Module": {"Path": "golang.org/x/sys", "Version": "v0.1.0", "Dir": "/m/sys"}}
{"Module": {"Path": "github.com/tailscale/wf", "Version": "v1", "Dir": "/m/wf",
  "Replace": {"Path": "github.com/FreedomCats/tailscale-wf", "Version": "v2", "Dir": "/m/fork"}}}
{"Module": {"Path": "github.com/FreedomCats/qwdtt-cli", "Dir": "/src"}}
{"Module": {"Path": "github.com/BurntSushi/toml", "Version": "v1.6.0", "Dir": "/m/toml"}}
`
	mods, err := parseModules([]byte(list))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range mods {
		got = append(got, m.Path+"@"+m.Version+":"+m.Dir)
	}
	want := []string{
		"github.com/BurntSushi/toml@v1.6.0:/m/toml",
		"github.com/FreedomCats/tailscale-wf@v2:/m/fork",
		"golang.org/x/sys@v0.1.0:/m/sys",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("parseModules:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestCollect(t *testing.T) {
	goroot := t.TempDir()
	write(t, goroot, "LICENSE", "Redistribution and use in source and binary forms ... endorse or promote")
	write(t, goroot, "PATENTS", "grant")

	withLicense := t.TempDir()
	write(t, withLicense, "COPYING", "Permission is hereby granted, free of charge\r\n")
	forked := t.TempDir() // без файла лицензии, но описан в noLicenseFile
	unknown := t.TempDir()
	write(t, unknown, "LICENSE", "Proprietary")
	missing := t.TempDir()

	sections, err := collect([]module{
		{Path: "example.com/mit", Version: "v1", Dir: withLicense},
		{Path: "github.com/bogdanfinn/fhttp", Version: "v0.6.8", Dir: forked},
	}, goroot, "go1.26.2")
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 3 || sections[0].Title != "Go 1.26.2 (рантайм и стандартная библиотека)" || sections[0].Kind != "BSD-3-Clause" {
		t.Fatalf("раздел Go: %+v", sections)
	}
	if s := sections[1]; s.Kind != "MIT" || len(s.Files) != 1 || s.Files[0].Name != "COPYING" || s.Files[0].Text != "Permission is hereby granted, free of charge" {
		t.Errorf("модуль с COPYING: %+v", s)
	}
	if s := sections[2]; s.Kind != "BSD-3-Clause" || s.Note == "" || len(s.Files) != 2 || s.Files[1].Name != "PATENTS" {
		t.Errorf("fhttp — лицензия Go с пометкой: %+v", s)
	}

	_, err = collect([]module{
		{Path: "example.com/unknown", Dir: unknown},
		{Path: "example.com/missing", Dir: missing},
	}, goroot, "go1.26.2")
	if err == nil || !strings.Contains(err.Error(), "example.com/unknown: вид лицензии") || !strings.Contains(err.Error(), "example.com/missing: нет файла лицензии") {
		t.Errorf("ошибка должна назвать оба модуля: %v", err)
	}
}

func TestRenderLineEndings(t *testing.T) {
	sections := []section{{Title: "m v1", Kind: "MIT", Files: []licenseText{{Name: "LICENSE", Text: "a\nb"}}}}
	win := render(sections, "windows", "amd64")
	if strings.Count(win, "\n") != strings.Count(win, "\r\n") {
		t.Error("для windows все переводы строк должны быть CRLF")
	}
	if !strings.Contains(win, "wintun-LICENSE.txt") {
		t.Error("для windows нужна ссылка на wintun-LICENSE.txt")
	}
	linux := render(sections, "linux", "amd64")
	if strings.Contains(linux, "\r") || strings.Contains(linux, "wintun") {
		t.Error("для linux — LF и без wintun")
	}
}

// TestRealModule собирает уведомления по настоящему ./cmd/tt: у каждой
// зависимости бинаря должна найтись распознанная лицензия.
func TestRealModule(t *testing.T) {
	if testing.Short() {
		t.Skip("go list по всему модулю")
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	goroot, goversion, err := goEnv()
	if err != nil {
		t.Fatal(err)
	}
	mods, err := listModules(root, "windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collect(mods, goroot, goversion); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
