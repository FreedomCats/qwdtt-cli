package core

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// Канал капчи и карта injected-кредов — пакетные переменные, поэтому тесты
// управления не параллелятся и убирают за собой сами.

func TestControlPause(t *testing.T) {
	ctrl := NewControl()

	if got := atomic.LoadInt32(ctrl.pauseFlag()); got != 0 {
		t.Fatalf("новая ручка уже на паузе: флаг = %d", got)
	}

	ctrl.Pause()
	ctrl.Pause() // повтор ничего не меняет
	if got := atomic.LoadInt32(ctrl.pauseFlag()); got != 1 {
		t.Errorf("после Pause флаг = %d, ожидалась 1", got)
	}

	ctrl.Resume()
	ctrl.Resume()
	if got := atomic.LoadInt32(ctrl.pauseFlag()); got != 0 {
		t.Errorf("после Resume флаг = %d, ожидался 0", got)
	}
}

// Ручка без запуска — не падение: CLI может отдать ответ и после того, как Run
// уже вернулся.
func TestControlNilIsHarmless(t *testing.T) {
	var ctrl *Control
	ctrl.Pause()
	ctrl.Resume()
	ctrl.SubmitCaptcha("token")
	ctrl.SubmitTurnCreds(TurnCreds{})
}

func TestControlSubmitCaptcha(t *testing.T) {
	t.Cleanup(drainCaptchaResult)
	ctrl := NewControl()

	ctrl.SubmitCaptcha("token")
	select {
	case got := <-CaptchaResultChan:
		if got != "token" {
			t.Errorf("в канал ушло %q, ожидалось %q", got, "token")
		}
	default:
		t.Fatal("SubmitCaptcha не дошёл до канала")
	}
}

// Ответ на позапрошлую капчу не должен достаться следующему ожидающему:
// перед отправкой канал чистится, как это делало и ядро перед каждым ожиданием.
func TestControlSubmitCaptchaReplacesStale(t *testing.T) {
	t.Cleanup(drainCaptchaResult)
	ctrl := NewControl()

	ctrl.SubmitCaptcha("старый")
	ctrl.SubmitCaptcha("новый")

	select {
	case got := <-CaptchaResultChan:
		if got != "новый" {
			t.Errorf("в канале %q, ожидался %q", got, "новый")
		}
	default:
		t.Fatal("канал пуст")
	}
	select {
	case got := <-CaptchaResultChan:
		t.Errorf("в канале остался лишний ответ %q", got)
	default:
	}
}

// Пустой токен — это «решить не удалось». Ядро отличает отказ от ответа по
// префиксу error:, иначе оно посчитало бы пустую строку испорченным токеном
// и написало бы об этом невнятное.
func TestControlSubmitCaptchaEmptyMeansFailure(t *testing.T) {
	t.Cleanup(drainCaptchaResult)
	ctrl := NewControl()

	ctrl.SubmitCaptcha("   ")
	select {
	case got := <-CaptchaResultChan:
		if got != "error:cancelled" {
			t.Errorf("в канал ушло %q, ожидался отказ", got)
		}
	default:
		t.Fatal("SubmitCaptcha не дошёл до канала")
	}
}

// Креды доходят до группы, которая их ждёт, и остаются в кэше под ссылкой:
// спросившая позже группа сходит уже за готовым.
func TestControlSubmitTurnCredsReachesWaiter(t *testing.T) {
	const link = "control-test-link"
	rec := install(t)
	t.Cleanup(func() {
		invalidateInjectedTurnCreds(link)
		drainTurnCredsResult()
	})
	setVkAuthMode(VkAuthAccount)
	t.Cleanup(func() { setVkAuthMode(VkAuthAnonymous) })

	type result struct {
		user, pass string
		urls       []string
		err        error
	}
	done := make(chan result, 1)
	go func() {
		user, pass, urls, err := requestAccountTurnCreds(context.Background(), link, 1)
		done <- result{user, pass, urls, err}
	}()

	waitForEvent[EventVkAuthRequired](t, rec, 5*time.Second)

	NewControl().SubmitTurnCreds(TurnCreds{
		Link:     link,
		Username: "user",
		Password: "pass",
		URLs:     []string{"turn:198.51.100.7:3478"},
	})

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("ожидающая группа получила ошибку: %v", got.err)
		}
		if got.user != "user" || got.pass != "pass" {
			t.Errorf("получены креды %q/%q, ожидались user/pass", got.user, got.pass)
		}
		if len(got.urls) != 1 || got.urls[0] != "198.51.100.7:3478" {
			t.Errorf("получены адреса %v, ожидался [198.51.100.7:3478]", got.urls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("креды не дошли до ожидающей группы за 5 с")
	}

	if _, _, _, ok := getInjectedTurnCreds(link); !ok {
		t.Error("креды не попали в кэш: следующая группа пойдёт спрашивать заново")
	}
}

// Неполные креды — отказ. Без него ожидающая группа стояла бы до таймаута
// в пять минут, хотя ответ уже пришёл.
func TestControlSubmitTurnCredsIncompleteCancels(t *testing.T) {
	const link = "control-test-cancel"
	rec := install(t)
	t.Cleanup(func() {
		invalidateInjectedTurnCreds(link)
		drainTurnCredsResult()
	})
	setVkAuthMode(VkAuthAccount)
	t.Cleanup(func() { setVkAuthMode(VkAuthAnonymous) })

	errCh := make(chan error, 1)
	go func() {
		_, _, _, err := requestAccountTurnCreds(context.Background(), link, 1)
		errCh <- err
	}()

	waitForEvent[EventVkAuthRequired](t, rec, 5*time.Second)

	NewControl().SubmitTurnCreds(TurnCreds{Link: link, Username: "user"})

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("отказ принят за креды")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("отказ не дошёл до ожидающей группы за 5 с")
	}

	if _, _, _, ok := getInjectedTurnCreds(link); ok {
		t.Error("неполные креды попали в кэш")
	}
}

// Отмена ctx завершает Run, и это не ошибка. Заодно проверяется, что после
// возврата не остаётся горутин: статистика, диспетчер и раздача конфига живут
// ровно столько, сколько Run.
//
// Сети тест не касается: режим аккаунта без готовых кредов паркует группу на
// ожидании EventVkAuthRequired, не сходив ни к VK, ни к TURN.
func TestRunStopsOnContextCancel(t *testing.T) {
	rec := install(t)

	cfg := DefaultConfig()
	cfg.Peer = "127.0.0.1:59999"
	cfg.Password = "пароль"
	cfg.Hashes = []string{"controltesthash"}
	cfg.VkAuthMode = VkAuthAccount
	cfg.Workers = 1
	cfg.Listen = "127.0.0.1:0"
	t.Cleanup(func() { setVkAuthMode(VkAuthAnonymous) })

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, rec, NewControl()) }()

	// Ядро дошло до ожидания кредов — значит, поднялось всё, что Run поднимает.
	select {
	case err := <-done:
		t.Fatalf("Run вернулся, не дойдя до запроса кредов: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("ядро не запросило креды VK за 30 с")
	case <-waitForEventCh[EventVkAuthRequired](rec):
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("остановка по отмене ctx — не ошибка, получено: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run не вернулся через 20 с после отмены ctx")
	}

	// Горутины гаснут не мгновенно: последняя может дописывать лог в тот момент,
	// когда Run уже вернулся. Поэтому не снимок, а ожидание.
	deadline := time.Now().Add(10 * time.Second)
	for {
		after := runtime.NumGoroutine()
		if after <= before {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("после Run осталось %d горутин против %d до запуска", after, before)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForEvent ждёт события нужного типа среди записанных получателем.
func waitForEvent[E Event](t *testing.T, rec *recorder, timeout time.Duration) {
	t.Helper()
	select {
	case <-waitForEventCh[E](rec):
	case <-time.After(timeout):
		t.Fatalf("событие %T не пришло за %v", *new(E), timeout)
	}
}

// waitForEventCh закрывает канал, когда получатель увидел событие нужного типа.
// Опрос, а не подписка: recorder складывает события в память, и подписаться
// на него нечем.
func waitForEventCh[E Event](rec *recorder) <-chan struct{} {
	seen := make(chan struct{})
	go func() {
		for {
			for _, ev := range rec.all() {
				if _, ok := ev.(E); ok {
					close(seen)
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	return seen
}
