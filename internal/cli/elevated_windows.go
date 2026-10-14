package cli

import "golang.org/x/sys/windows"

// isElevated — запущен ли процесс с правами администратора. Нужно datapath
// external: службу туннеля ставит только повышенный процесс, и объяснить это
// лучше одной строкой до подключения, чем отказом wireguard.exe на середине.
func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// elevationHint — что сказать, когда прав не хватает.
const elevationHint = "нужны права администратора: запустите qwdtt-cli из консоли от имени администратора"
