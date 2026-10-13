//go:build !windows && !linux

package netguard

// На других системах Control ничего не делает: datapath, забирающих весь
// трафик, там пока нет. macOS (IP_BOUND_IF) — этап Э4.
const supported = false

const bindPerFamily = true

const (
	option4 = "IP_BOUND_IF"
	option6 = "IPV6_BOUND_IF"
)

func (g *Guard) setter() setter {
	return func(uintptr, bool, uint32) error { return nil }
}

// choose здесь выбирать нечего.
func choose(uint64) (Choice, error) { return Choice{}, nil }

// subscribe здесь событий не даёт: следить за сетью незачем, пока Control
// ничего не делает.
func subscribe(func(id uint64)) (func(), error) { return func() {}, nil }
