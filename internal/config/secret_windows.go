//go:build windows

package config

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// На Windows секрет шифруется DPAPI: CryptProtectData привязывает шифртекст
// к учётной записи, поэтому файл хранилища, скопированный на другую машину или
// открытый под другим пользователем, бесполезен. Отсюда схема ссылки — dpapi.
const (
	secretScheme    = "dpapi"
	secretStoreKind = "DPAPI, привязка к учётной записи Windows"
)

func protect(plaintext []byte) ([]byte, error) {
	out, err := dpapi(plaintext, false)
	if err != nil {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}
	return out, nil
}

func unprotect(ciphertext []byte) ([]byte, error) {
	out, err := dpapi(ciphertext, true)
	if err != nil {
		return nil, fmt.Errorf("CryptUnprotectData: %w — секрет доступен только на той же учётной записи Windows, где его записали", err)
	}
	return out, nil
}

// dpapi вызывает CryptUnprotectData (decrypt=true) или CryptProtectData
// (decrypt=false). Обе кладут результат в блоб, память под который освобождается
// LocalFree.
func dpapi(in []byte, decrypt bool) ([]byte, error) {
	var inBlob windows.DataBlob
	if len(in) > 0 {
		inBlob = windows.DataBlob{Size: uint32(len(in)), Data: &in[0]}
	}

	var outBlob windows.DataBlob
	var err error
	// CRYPTPROTECT_UI_FORBIDDEN: никаких диалогов — клиент консольный и может
	// работать без интерактивного рабочего стола.
	if decrypt {
		err = windows.CryptUnprotectData(&inBlob, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &outBlob)
	} else {
		err = windows.CryptProtectData(&inBlob, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &outBlob)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))

	out := make([]byte, outBlob.Size)
	copy(out, unsafe.Slice(outBlob.Data, outBlob.Size))
	return out, nil
}
