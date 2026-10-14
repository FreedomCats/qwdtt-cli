package wintun

import (
	"archive/zip"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	encasn1 "encoding/asn1"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/cryptobyte"
	"golang.org/x/crypto/cryptobyte/asn1"
)

var entryTime = time.Date(2021, 10, 17, 4, 39, 14, 0, time.UTC)

// fakeArchive пишет zip с записями files и подменяет хеш на его.
func fakeArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: entryTime})
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wintun-"+Version+".zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	old, oldVerify := archiveSHA256, verify
	archiveSHA256 = sum
	verify = func(string) error { return nil }
	t.Cleanup(func() { archiveSHA256, verify = old, oldVerify })
	return path
}

var archiveFiles = map[string]string{
	"wintun/LICENSE.txt":          "лицензия",
	"wintun/bin/amd64/wintun.dll": "dll amd64",
	"wintun/bin/x86/wintun.dll":   "dll x86",
	"wintun/include/wintun.h":     "h",
}

// DLL нужной архитектуры и лицензия — в Dest, со временем из архива.
func TestFetch(t *testing.T) {
	archive := fakeArchive(t, archiveFiles)
	dest := filepath.Join(t.TempDir(), "out")
	if err := Fetch(Options{Arch: "386", Dest: dest, Archive: archive}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"wintun.dll": "dll x86", "wintun-LICENSE.txt": "лицензия"} {
		path := filepath.Join(dest, name)
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v", name, got, err)
		}
		if fi, err := os.Stat(path); err != nil || !fi.ModTime().Equal(entryTime) {
			t.Errorf("%s: время %v, ожидалось %v", name, fi.ModTime(), entryTime)
		}
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 2 {
		t.Errorf("в Dest лишнее: %v", entries)
	}
}

// Хеш не тот — ничего не распаковано и не положено.
func TestFetchHashMismatch(t *testing.T) {
	archive := fakeArchive(t, archiveFiles)
	archiveSHA256 = strings.Repeat("0", 64)
	dest := filepath.Join(t.TempDir(), "out")
	err := Fetch(Options{Arch: "amd64", Dest: dest, Archive: archive})
	if err == nil || !strings.Contains(err.Error(), "SHA256") {
		t.Errorf("Fetch: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("Dest создан: %v", err)
	}
}

func TestFetchMissingEntry(t *testing.T) {
	archive := fakeArchive(t, archiveFiles)
	err := Fetch(Options{Arch: "ARM64", Dest: t.TempDir(), Archive: archive})
	if err == nil || !strings.Contains(err.Error(), "wintun/bin/arm64/wintun.dll") {
		t.Errorf("Fetch: %v", err)
	}
}

func TestFetchBadArch(t *testing.T) {
	if err := Fetch(Options{Arch: "mips", Dest: t.TempDir()}); err == nil {
		t.Error("Fetch с mips прошёл")
	}
}

// Подписант в PKCS#7 находится по издателю и номеру, имя — в виде Windows.
func TestParseSignedData(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newCert := func(serial int64, subject pkix.Name) []byte {
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: subject,
			NotBefore: entryTime, NotAfter: entryTime.Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		return der
	}
	other := newCert(1, pkix.Name{CommonName: "Другой"})
	signer := newCert(2, pkix.Name{CommonName: "WireGuard LLC", Organization: []string{"WireGuard LLC"},
		Locality: []string{"Boulder"}, Province: []string{"Colorado"}, Country: []string{"US"}})
	parsed, _ := x509.ParseCertificate(signer)

	var b cryptobyte.Builder
	b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
		b.AddASN1ObjectIdentifier(oidSignedData)
		b.AddASN1(asn1.Tag(0).Constructed().ContextSpecific(), func(b *cryptobyte.Builder) {
			b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
				b.AddASN1Int64(1)
				b.AddASN1(asn1.SET, func(*cryptobyte.Builder) {})
				b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
					b.AddASN1ObjectIdentifier(encasn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1})
				})
				b.AddASN1(asn1.Tag(0).Constructed().ContextSpecific(), func(b *cryptobyte.Builder) {
					b.AddBytes(other)
					b.AddBytes(signer)
				})
				b.AddASN1(asn1.SET, func(b *cryptobyte.Builder) {
					b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
						b.AddASN1Int64(1)
						b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
							b.AddBytes(parsed.RawIssuer)
							b.AddASN1BigInt(parsed.SerialNumber)
						})
					})
				})
			})
		})
	})
	der, err := b.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	cert, err := parseSignedData(der)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := x500String(cert.RawSubject)
	if err != nil {
		t.Fatal(err)
	}
	if want := "CN=WireGuard LLC, O=WireGuard LLC, L=Boulder, S=Colorado, C=US"; subject != want {
		t.Errorf("субъект %q, ожидался %q", subject, want)
	}
}

func TestQuoteValue(t *testing.T) {
	for in, want := range map[string]string{
		"WireGuard LLC": "WireGuard LLC",
		"A, B":          `"A, B"`,
		`say "hi"=`:     `"say ""hi""="`,
		" x":            `" x"`,
	} {
		if got := quoteValue(in); got != want {
			t.Errorf("%q: %q, ожидалось %q", in, got, want)
		}
	}
}
