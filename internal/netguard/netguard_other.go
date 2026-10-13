//go:build !windows

package netguard

// На других системах Control ничего не делает: datapath, забирающих весь
// трафик, там пока нет. Linux (SO_BINDTODEVICE) и macOS (IP_BOUND_IF) —
// этап Э4.
const supported = false

func setUnicastIf(uintptr, bool, uint32) error { return nil }

// SelectInterfaces на других системах выбирать нечего: Control там ничего
// не делает.
func (g *Guard) SelectInterfaces(uint64) (string, error) {
	return "не нужен на этой системе", nil
}

// choose вне Windows выбирать нечего.
func choose(uint64) (Choice, error) { return Choice{}, nil }

// subscribe вне Windows событий не даёт: следить за сетью незачем, пока
// Control ничего не делает.
func subscribe(func(luid uint64)) (func(), error) { return func() {}, nil }
