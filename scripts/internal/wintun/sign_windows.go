package wintun

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/FreedomCats/qwdtt-cli/scripts/internal/script"
)

// verifySignature проверяет подпись Authenticode у dll: WinVerifyTrust
// считает её действительной, а подписант — ExpectedSigner. Это та же
// проверка, что делал Get-AuthenticodeSignature.
func verifySignature(dll string) error {
	if status, err := winVerifyTrust(dll); status != "Valid" {
		return fmt.Errorf("подпись wintun.dll недействительна: %s — %v", status, err)
	}
	subject, err := SignerSubject(dll)
	if err != nil {
		return fmt.Errorf("подписант wintun.dll: %w", err)
	}
	if !strings.Contains(strings.ToLower(subject), strings.ToLower(ExpectedSigner)) {
		return fmt.Errorf("wintun.dll подписана не WireGuard LLC: %s", subject)
	}
	script.Say("Подпись wintun.dll действительна: " + ExpectedSigner)
	return nil
}

// Коды WinVerifyTrust, которые PowerShell называл по-своему.
const (
	nteBadAlgID           = 0x80090008
	cryptBadMsg           = 0x8009200D
	trustProviderUnknown  = 0x800B0001
	trustNoSignature      = 0x800B0100
	trustBadDigest        = 0x80096010
	trustExplicitDistrust = 0x800B0111
)

// winVerifyTrust возвращает статус подписи под тем же именем, что
// SignatureStatus в PowerShell, и ошибку Windows для текста.
func winVerifyTrust(path string) (string, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "UnknownError", err
	}
	data := &windows.WinTrustData{
		Size:             uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:         windows.WTD_UI_NONE,
		RevocationChecks: windows.WTD_REVOKE_NONE,
		UnionChoice:      windows.WTD_CHOICE_FILE,
		StateAction:      windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(&windows.WinTrustFileInfo{
			Size:     uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})),
			FilePath: path16,
		}),
	}
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	_ = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)

	if verifyErr == nil {
		return "Valid", nil
	}
	var errno windows.Errno
	if !errors.As(verifyErr, &errno) {
		return "UnknownError", verifyErr
	}
	switch uint32(errno) {
	case nteBadAlgID:
		return "Incompatible", errno
	case trustNoSignature:
		return "NotSigned", errno
	case trustBadDigest, cryptBadMsg:
		return "HashMismatch", errno
	case trustProviderUnknown:
		return "NotSupportedFileFormat", errno
	case trustExplicitDistrust:
		return "NotTrusted", errno
	}
	return "UnknownError", errno
}
