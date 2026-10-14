package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"time"

	"github.com/FreedomCats/qwdtt-cli/scripts/internal/script"
)

// files — обычные файлы каталога dir в порядке Sort-Object. Вложенных
// каталогов в релизе нет; встретится — ошибка, а не тихий пропуск.
func files(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			return nil, fmt.Errorf("в %s не файл: %s", dir, e.Name())
		}
		names = append(names, e.Name())
	}
	script.SortNames(names)
	return names, nil
}

// writeZip пишет каталог dir в path так же, как Compress-Archive: внутри —
// каталог с именем dir, записи без отдельных записей каталогов, deflate,
// время — время изменения файла по местным часам (формат DOS). Права — как
// у .NET: на Windows атрибутов нет вовсе, на прочих системах у всех файлов
// 0644, что бы ни стояло на диске. Сжатые байты у .NET другие, остальное в
// записях то же.
func writeZip(dir, path string) (err error) {
	names, err := files(dir)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	zw := zip.NewWriter(f)
	prefix := filepath.Base(dir)
	for _, name := range names {
		src := filepath.Join(dir, name)
		fi, err := os.Stat(src)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		var packed bytes.Buffer
		fw, err := flate.NewWriter(&packed, flate.BestCompression)
		if err != nil {
			return err
		}
		if _, err := fw.Write(data); err != nil {
			return err
		}
		if err := fw.Close(); err != nil {
			return err
		}

		// Размеры и CRC — в заголовке записи, без дескриптора после данных:
		// так пишет .NET, поэтому запись — «сырая», сжатая заранее.
		h := &zip.FileHeader{
			Name:               prefix + "/" + name,
			Method:             zip.Deflate,
			CreatorVersion:     zipCreator,
			ReaderVersion:      20,
			CRC32:              crc32.ChecksumIEEE(data),
			CompressedSize64:   uint64(packed.Len()),
			UncompressedSize64: uint64(len(data)),
			ExternalAttrs:      zipAttrs,
		}
		h.ModifiedDate, h.ModifiedTime = dosTime(fi.ModTime().Local())
		w, err := zw.CreateRaw(h)
		if err != nil {
			return err
		}
		if _, err := w.Write(packed.Bytes()); err != nil {
			return err
		}
	}
	return zw.Close()
}

// zipCreator и zipAttrs — «чем создано» и атрибуты записи, как у .NET:
// Windows (0) и без атрибутов там, Unix (3) и обычный файл 0644 на прочих.
var zipCreator, zipAttrs = func() (uint16, uint32) {
	if runtime.GOOS == "windows" {
		return 20, 0
	}
	return 3<<8 | 20, (0o100000 | 0o644) << 16
}()

// dosTime — дата и время в формате DOS, с точностью до двух секунд.
func dosTime(t time.Time) (date, clock uint16) {
	if t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, t.Location())
	}
	date = uint16(t.Day() + int(t.Month())<<5 + (t.Year()-1980)<<9)
	clock = uint16(t.Second()/2 + t.Minute()<<5 + t.Hour()<<11)
	return date, clock
}

// tarTime — время всех записей tar.gz: время файлов на машине сборки в архив
// не попадает, и одинаковые файлы дают одинаковый архив.
var tarTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// writeTarGz пишет каталог dir в path: внутри — каталог prefix с файлами dir.
// executables получают 0755, остальные — 0644, на какой бы системе ни шла
// сборка. Формат — ustar: pax дописал бы каждой записи atime и ctime.
//
// Заголовки — байт в байт как у System.Formats.Tar (старый
// build-release.ps1): нулевые uid, gid и размер каталога не пишутся вовсе,
// контрольная сумма — восьмеричными цифрами без ведущих нулей. Сжатые байты
// gzip у .NET другие, распакованный tar — тот же.
func writeTarGz(dir, path, prefix string, executables []string) (err error) {
	names, err := files(dir)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	gz, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return err
	}
	gz.OS = 3 // Unix, как у .NET

	if err := writeTarHeader(gz, prefix+"/", '5', 0o755, -1); err != nil {
		return err
	}
	for _, name := range names {
		src := filepath.Join(dir, name)
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if slices.Contains(executables, name) {
			mode = 0o755
		}
		if err := writeTarHeader(gz, prefix+"/"+name, '0', mode, int64(len(data))); err != nil {
			return err
		}
		if _, err := gz.Write(data); err != nil {
			return err
		}
		if pad := (512 - len(data)%512) % 512; pad > 0 {
			if _, err := gz.Write(make([]byte, pad)); err != nil {
				return err
			}
		}
	}
	// Конец архива — два пустых блока.
	if _, err := gz.Write(make([]byte, 1024)); err != nil {
		return err
	}
	return gz.Close()
}

// writeTarHeader пишет заголовок ustar. size < 0 — без поля размера
// (каталог).
func writeTarHeader(w io.Writer, name string, typeflag byte, mode, size int64) error {
	var h [512]byte
	prefix := ""
	if len(name) > 100 {
		// Длинное имя делится по «/»: начало — в поле prefix (до 155 байт).
		split := -1
		for i := len(name) - 1; i > 0; i-- {
			if name[i] == '/' && i <= 155 && len(name)-i-1 <= 100 {
				split = i
				break
			}
		}
		if split < 0 {
			return fmt.Errorf("имя слишком длинное для ustar: %s", name)
		}
		prefix, name = name[:split], name[split+1:]
	}
	copy(h[0:100], name)
	octal(h[100:108], mode)
	// uid и gid — нули, .NET их не пишет: поля остаются пустыми.
	if size >= 0 {
		octal(h[124:136], size)
	}
	octal(h[136:148], tarTime.Unix())
	h[156] = typeflag
	copy(h[257:263], "ustar\x00")
	copy(h[263:265], "00")
	copy(h[345:500], prefix)

	// Контрольная сумма считается с пробелами на её месте.
	copy(h[148:156], "        ")
	var sum int64
	for _, b := range h {
		sum += int64(b)
	}
	clear(h[148:156])
	copy(h[148:155], strconv.FormatInt(sum, 8))
	h[155] = ' '

	_, err := w.Write(h[:])
	return err
}

// octal пишет v восьмеричными цифрами с ведущими нулями на всё поле, кроме
// последнего байта — он NUL.
func octal(field []byte, v int64) {
	s := strconv.FormatInt(v, 8)
	if len(s) > len(field)-1 {
		panic(errors.New("tar: значение не помещается в поле"))
	}
	for i := range field[:len(field)-1] {
		field[i] = '0'
	}
	copy(field[len(field)-1-len(s):], s)
	field[len(field)-1] = 0
}
