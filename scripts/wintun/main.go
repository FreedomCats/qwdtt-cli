// Команда wintun скачивает wintun.dll фиксированной версии, проверяет её и
// кладёт рядом с qwdtt-cli.exe: хеш архива, на Windows — подпись Authenticode
// (WireGuard LLC). Подробности — в пакете scripts/internal/wintun.
//
//	go run ./scripts/wintun
//	go run ./scripts/wintun -arch arm64 -dest C:\tools\qwdtt-cli
//	go run ./scripts/wintun -archive %USERPROFILE%\Downloads\wintun-0.14.1.zip
//
// -arch — архитектура в терминах Go: amd64 (по умолчанию), arm64, 386, arm.
// -dest — куда положить wintun.dll; по умолчанию build/out в корне
// репозитория — туда же README предлагает собирать qwdtt-cli.exe.
// -archive — уже скачанный wintun-<версия>.zip: тогда в сеть команда не
// ходит. Хеш проверяется всё равно.
//
// Перенесена с PowerShell (scripts/fetch-wintun.ps1).
package main

import (
	"flag"
	"os"
	"path/filepath"

	"github.com/FreedomCats/qwdtt-cli/scripts/internal/script"
	"github.com/FreedomCats/qwdtt-cli/scripts/internal/wintun"
)

const self = "wintun"

func main() {
	var o wintun.Options
	flag.StringVar(&o.Arch, "arch", "amd64", "архитектура: amd64, arm64, 386, arm")
	flag.StringVar(&o.Dest, "dest", "", "куда положить wintun.dll (по умолчанию build/out в корне репозитория)")
	flag.StringVar(&o.Archive, "archive", "", "уже скачанный wintun-"+wintun.Version+".zip")
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	if o.Dest == "" {
		root, err := script.Root()
		if err != nil {
			script.Fail(self, err)
		}
		o.Dest = filepath.Join(root, "build", "out")
	}
	if err := wintun.Fetch(o); err != nil {
		script.Fail(self, err)
	}
}
