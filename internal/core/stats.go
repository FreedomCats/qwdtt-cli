package core

import (
	"sync/atomic"
	"time"
)

type Stats struct {
	TotalBytesUp      atomic.Int64
	TotalBytesDown    atomic.Int64
	ActiveConnections atomic.Int32

	// DroppedUp — пакеты от WireGuard, выброшенные потому, что очереди
	// всех воркеров полны (Dispatcher.readLoop). DroppedDown — ответы
	// сервера, выброшенные из-за полной очереди к WireGuard (RunSession).
	DroppedUp   atomic.Uint64
	DroppedDown atomic.Uint64
}

func NewStats() *Stats {
	return &Stats{}
}

func (s *Stats) RunLoop(shutdown <-chan struct{}) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-shutdown:
			return
		case <-ticker.C:
			active := s.ActiveConnections.Load()
			up := s.TotalBytesUp.Load()
			down := s.TotalBytesDown.Load()
			totalMB := float64(up+down) / (1024.0 * 1024.0)
			upMB := float64(up) / (1024.0 * 1024.0)
			downMB := float64(down) / (1024.0 * 1024.0)

			logf("[СТАТИСТИКА] Активных: %d | Трафик: %.2f МБ | ↓%.2f МБ / ↑%.2f МБ", active, totalMB, downMB, upMB)
			emit(EventStats{ActiveWorkers: active, BytesUp: up, BytesDown: down, DroppedUp: s.DroppedUp.Load(), DroppedDown: s.DroppedDown.Load()})
		}
	}
}
