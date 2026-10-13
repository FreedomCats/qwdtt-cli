package linuxsys

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/vishvananda/netlink"
)

// receiveBuffer — сколько событий netlink копится в сокете, пока их не
// прочли. Переключение сети даёт их десятки; переполнение — это
// ENOBUFS и обрыв подписки (см. follow). Ядро урежет значение до
// net.core.rmem_max, это не ошибка.
const receiveBuffer = 1 << 20

// resubscribeDelay — пауза перед новой подпиской, если прежняя оборвалась.
const resubscribeDelay = time.Second

// WatchNetwork подписывается через netlink на смену маршрутов, интерфейсов
// и адресов обоих семейств и зовёт notify с индексом интерфейса, которого
// коснулось изменение; 0 — интерфейс неизвестен (маршрут с несколькими
// шлюзами, обрыв подписки). Аналог winsys.WatchNetwork.
//
// Сами события ничего не решают: сворачивает серию и решает, сменилась ли
// сеть, вызывающий (netguard.Guard.Watch).
//
// notify зовётся из нескольких горутин, возможно одновременно. stop снимает
// подписку и ждёт уже начатых вызовов notify; после stop новых нет.
func WatchNetwork(notify func(index uint64)) (stop func(), err error) {
	w := &watch{stop: make(chan struct{})}
	w.notify = func(index uint64) {
		if !w.stopping.Load() {
			notify(index)
		}
	}

	routes := func(done <-chan struct{}) (chan netlink.RouteUpdate, error) {
		ch := make(chan netlink.RouteUpdate, 64)
		return ch, netlink.RouteSubscribeWithOptions(ch, done,
			netlink.RouteSubscribeOptions{ReceiveBufferSize: receiveBuffer})
	}
	links := func(done <-chan struct{}) (chan netlink.LinkUpdate, error) {
		ch := make(chan netlink.LinkUpdate, 64)
		return ch, netlink.LinkSubscribeWithOptions(ch, done,
			netlink.LinkSubscribeOptions{ReceiveBufferSize: receiveBuffer})
	}
	addrs := func(done <-chan struct{}) (chan netlink.AddrUpdate, error) {
		ch := make(chan netlink.AddrUpdate, 64)
		return ch, netlink.AddrSubscribeWithOptions(ch, done,
			netlink.AddrSubscribeOptions{ReceiveBufferSize: receiveBuffer})
	}

	stop = w.close
	if err := follow(w, routes, func(u netlink.RouteUpdate) int { return u.LinkIndex }); err != nil {
		stop()
		return nil, err
	}
	if err := follow(w, links, func(u netlink.LinkUpdate) int { return int(u.Index) }); err != nil {
		stop()
		return nil, err
	}
	if err := follow(w, addrs, func(u netlink.AddrUpdate) int { return u.LinkIndex }); err != nil {
		stop()
		return nil, err
	}
	return stop, nil
}

// watch — общее для трёх подписок: остановка и ожидание читателей.
type watch struct {
	notify   func(index uint64)
	stopping atomic.Bool
	stop     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
}

func (w *watch) close() {
	w.once.Do(func() {
		w.stopping.Store(true)
		close(w.stop)
	})
	w.wg.Wait()
}

// follow подписывается и читает события, пока не вызван stop.
//
// netlink шлёт события в канал блокирующей записью и закрывает канал, когда
// сокет закрыт или приём отказал (ENOBUFS при переполнении буфера). Поэтому
// канал читается до закрытия и после stop, а закрытие без stop — это обрыв:
// подписка заводится заново, пока не выйдет или не придёт stop, и затем
// notify(0) просит перепроверить выбор — события за время обрыва потеряны.
// Иначе после одного переполнения смена сети больше не замечалась бы.
func follow[T any](w *watch, subscribe func(done <-chan struct{}) (chan T, error), index func(T) int) error {
	ch, end, err := attempt(w, subscribe)
	if err != nil {
		return err
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for {
			for u := range ch {
				w.notify(uint64(index(u)))
			}
			end()
			for {
				select {
				case <-w.stop:
					return
				case <-time.After(resubscribeDelay):
				}
				if ch, end, err = attempt(w, subscribe); err == nil {
					break
				}
			}
			w.notify(0)
		}
	}()
	return nil
}

// attempt — одна подписка со своим done: end закрывает её сокет (после
// обрыва он больше не нужен), stop закрывает его тоже.
func attempt[T any](w *watch, subscribe func(done <-chan struct{}) (chan T, error)) (ch chan T, end func(), err error) {
	done := make(chan struct{})
	var once sync.Once
	end = func() { once.Do(func() { close(done) }) }
	ch, err = subscribe(done)
	if err != nil {
		end()
		return nil, nil, err
	}
	go func() {
		select {
		case <-w.stop:
			end()
		case <-done:
		}
	}()
	return ch, end, nil
}
