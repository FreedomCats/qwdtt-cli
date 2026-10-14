// Package wintun скачивает wintun.dll фиксированной версии, проверяет её и
// кладёт рядом с qwdtt-cli.exe. Его зовут команды wintun и release.
//
// wintun.dll нужна только режиму -datapath tun на Windows (свой адаптер). В
// репозитории её нет: это чужой бинарь под собственной лицензией WireGuard
// LLC, а не наш код. Этот пакет — единственный источник DLL и для сборки из
// исходников, и для релиза.
//
// Что делается:
//
//  1. архив wintun-<версия>.zip берётся с https://www.wintun.net/builds/
//     (или из Options.Archive, если он уже скачан);
//  2. SHA256 архива сверяется с зашитым в код; не совпал — ошибка, и ничего
//     не распаковано;
//  3. из архива берётся bin/<arch>/wintun.dll, и на Windows проверяется её
//     подпись Authenticode: действительная, подписант — WireGuard LLC;
//  4. DLL и лицензия Wintun (как wintun-LICENSE.txt) копируются в Options.Dest
//     с временем изменения из архива.
//
// Лицензия Wintun разрешает распространять DLL без изменений вместе с
// программой, которая обращается к ней только через API wintun.h, — так её
// и использует wireguard-go. Поэтому лицензия кладётся рядом с DLL.
//
// Сменить версию Wintun — значит поменять Version и ArchiveSHA256 ниже,
// сверив хеш с архивом, скачанным вручную с wintun.net.
package wintun

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/FreedomCats/qwdtt-cli/scripts/internal/script"
)

// Версия и хеш архива. Хеш — от архива с wintun.net/builds
// (plan-e2.md, врезка W2).
const (
	Version       = "0.14.1"
	ArchiveSHA256 = "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51"
	ArchiveURL    = "https://www.wintun.net/builds/wintun-" + Version + ".zip"
)

// ExpectedSigner — подписант DLL. Проверяется организация, а не отпечаток
// сертификата: сертификаты продлеваются, организация остаётся.
const ExpectedSigner = "O=WireGuard LLC"

// archiveSHA256 и verify подменяются в тестах: настоящего архива и
// подписанной DLL в них нет.
var (
	archiveSHA256 = ArchiveSHA256
	verify        = verifySignature
)

// archDirs — каталоги внутри архива: они называются по-виндовому, а не
// по-гошному.
var archDirs = map[string]string{"amd64": "amd64", "arm64": "arm64", "386": "x86", "arm": "arm"}

// Options — что и куда брать.
type Options struct {
	// Arch — архитектура в терминах Go: amd64, arm64, 386, arm. Регистр не
	// важен.
	Arch string
	// Dest — куда положить wintun.dll.
	Dest string
	// Archive — уже скачанный wintun-<версия>.zip: тогда в сеть не ходим.
	// Хеш проверяется всё равно.
	Archive string
}

// ArchDir — каталог архитектуры в архиве; ok — архитектура известна.
func ArchDir(arch string) (dir string, ok bool) {
	dir, ok = archDirs[strings.ToLower(arch)]
	return dir, ok
}

// Fetch делает всё по шагам из описания пакета.
func Fetch(o Options) (err error) {
	archDir, ok := ArchDir(o.Arch)
	if !ok {
		return fmt.Errorf("архитектура %q: допустимы amd64, arm64, 386, arm", o.Arch)
	}

	// Всё скачанное и распакованное живёт в своём пустом временном каталоге и
	// удаляется в конце, что бы ни случилось.
	work, err := os.MkdirTemp("", "qwdtt-cli-wintun-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	var archive string
	if o.Archive != "" {
		if archive, err = filepath.Abs(o.Archive); err != nil {
			return err
		}
		if _, err := os.Stat(archive); err != nil {
			return err
		}
		script.Say("Архив: " + archive)
	} else {
		archive = filepath.Join(work, "wintun-"+Version+".zip")
		script.Say("Скачиваю " + ArchiveURL)
		if err := download(ArchiveURL, archive); err != nil {
			return err
		}
	}

	actual, err := fileSHA256(archive)
	if err != nil {
		return err
	}
	if actual != archiveSHA256 {
		return fmt.Errorf("SHA256 архива не совпадает: ждали %s, получили %s. "+
			"Архив подменён или это другая версия — ничего не распаковано", archiveSHA256, actual)
	}
	script.Say("SHA256 архива совпал: " + actual)

	unpacked := filepath.Join(work, "unpacked")
	dll, err := extract(archive, "wintun/bin/"+archDir+"/wintun.dll", unpacked)
	if err != nil {
		return err
	}
	license, err := extract(archive, "wintun/LICENSE.txt", unpacked)
	if err != nil {
		return err
	}

	if err := verify(dll); err != nil {
		return err
	}

	if err := os.MkdirAll(o.Dest, 0o755); err != nil {
		return err
	}
	if err := script.CopyFile(dll, filepath.Join(o.Dest, "wintun.dll")); err != nil {
		return err
	}
	if err := script.CopyFile(license, filepath.Join(o.Dest, "wintun-LICENSE.txt")); err != nil {
		return err
	}
	script.Sayf("Wintun %s (%s) → %s", Version, o.Arch, filepath.Join(o.Dest, "wintun.dll"))
	return nil
}

func download(url, path string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extract достаёт из архива запись name в dir/name со временем изменения
// из архива. Нужны две записи, поэтому распаковывается не весь архив.
func extract(archive, name, dir string) (string, error) {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return "", err
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != name {
			continue
		}
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		r, err := f.Open()
		if err != nil {
			return "", err
		}
		defer r.Close()
		out, err := os.Create(path)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(out, r); err != nil {
			out.Close()
			return "", err
		}
		if err := out.Close(); err != nil {
			return "", err
		}
		return path, os.Chtimes(path, f.Modified, f.Modified)
	}
	return "", fmt.Errorf("в архиве нет %s", name)
}
