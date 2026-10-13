//go:build !windows

package netguard

// На других системах Control ничего не делает: datapath, забирающих весь
// трафик, там пока нет. Linux (SO_BINDTODEVICE) и macOS (IP_BOUND_IF) —
// этап Э4.
const supported = false

func setUnicastIf(uintptr, bool, uint32) error { return nil }
