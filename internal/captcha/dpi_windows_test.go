//go:build windows

package captcha

import (
	"context"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

// realPrimaryDPI — настоящий DPI основного монитора: процесс тестов не
// осведомлён о DPI и видит 96, поэтому спрашивает нить, осведомлённая о нём.
func realPrimaryDPI() uint32 {
	got := make(chan uint32)
	go func() {
		runtime.LockOSThread() // нить с другой осведомлённостью не отпускать
		dpiAwareThread()
		got <- primaryDPI()
	}()
	return <-got
}

// Окна капчи — в родном DPI основного монитора, а не растянутые картинкой
// из 96 DPI: и сразу открытое человеку (manual), и показанное после
// автофазы. На мониторе со 100 % проверка вырождается (96 = 96).
func TestWebView2DPI(t *testing.T) {
	s := newStandSolver(t)
	srv := newCaptchaStand(t)
	want := realPrimaryDPI()
	if want == 0 {
		t.Skip("DPI монитора не узнать")
	}
	t.Logf("DPI основного монитора %d", want)
	s.opts.Out = io.Discard

	t.Run("manual", func(t *testing.T) {
		before := profilesNow()
		s.opened = func(w webview2.WebView) {
			if got := windowDPI(uintptr(w.Window())); got != want {
				t.Errorf("DPI окна %d, ожидался %d (монитора)", got, want)
			}
		}
		defer func() { s.opened = nil }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := s.Solve(ctx, standRequest(srv.page("checkbox", "fetch"), 30*time.Second)); err != nil {
			t.Fatalf("Solve: %v", err)
		}
		q := srv.checkQueries()
		t.Logf("devicePixelRatio страницы %s", q[len(q)-1].Get("dpr"))
		waitProfilesGone(t, before)
	})

	t.Run("reveal", func(t *testing.T) {
		before := profilesNow()
		s.quietReveal = true
		s.autoTiming = &autoTiming{find: 2 * time.Second, click: defaultAutoTiming.click,
			poll: defaultAutoTiming.poll, reply: 5 * time.Second}
		var revealed sync.WaitGroup
		revealed.Add(1)
		s.revealed = func(w webview2.WebView) {
			defer revealed.Done()
			if got := windowDPI(uintptr(w.Window())); got != want {
				t.Errorf("DPI показанного окна %d, ожидался %d (монитора)", got, want)
			}
			w.Eval("standHuman()")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := s.Solve(ctx, standAutoRequest(srv.page("autonone", "fetch"), 30*time.Second)); err != nil {
			t.Fatalf("Solve: %v", err)
		}
		revealed.Wait()
		q := srv.checkQueries()
		t.Logf("devicePixelRatio страницы %s", q[len(q)-1].Get("dpr"))
		waitProfilesGone(t, before)
	})
}

// Скрытое окно — под масштаб монитора, ближайшего к месту за краем экрана, а
// не основного; при показе размер пересчитывается под основной и выходит
// тем же, что у окна, сразу открытого на основном. Мониторы подменены:
// основной 100 %, ближайший к месту за краем — 200 %.
func TestHiddenWindowScale(t *testing.T) {
	saved := monitorDPIAt
	defer func() { monitorDPIAt = saved }()
	var asked [2]int32
	monitorDPIAt = func(x, y int32) uint32 {
		asked = [2]int32{x, y}
		return 192
	}
	got := make(chan *hiddenWindow, 1)
	go func() {
		runtime.LockOSThread() // перехватчик ставится и снимается на одной нити
		hw, err := hideNextWindow(true)
		if err != nil {
			t.Error(err)
			got <- nil
			return
		}
		hw.unhook()
		got <- hw
	}()
	hw := <-got
	if hw == nil {
		return
	}
	if x, y := offscreen(); asked != [2]int32{x, y} {
		t.Errorf("DPI спрошен для точки %v, а окно встанет в (%d, %d)", asked, x, y)
	}
	if s := hw.scale(1); s != 2 {
		t.Fatalf("масштаб скрытого окна %v, ожидался 2 (монитор за краем), а не 1 (основной)", s)
	}
	w, h := int32(scaledSize(500, hw.scale(1))), int32(scaledSize(680, hw.scale(1)))
	if w != 1000 || h != 1360 {
		t.Errorf("размер скрытого окна %d×%d", w, h)
	}
	if rw, rh := rescale(w, h, hw.dpi, 96); rw != int32(scaledSize(500, 1)) || rh != int32(scaledSize(680, 1)) {
		t.Errorf("после показа на основном %d×%d, ожидалось 500×680", rw, rh)
	}
	if rw, rh := rescale(750, 1020, 144, 120); rw != 625 || rh != 850 {
		t.Errorf("150 %% → 125 %%: %d×%d, ожидалось 625×850", rw, rh)
	}
	if rw, rh := rescale(875, 1190, 0, 96); rw != 875 || rh != 1190 {
		t.Errorf("DPI не узнать — размер прежний, а вышло %d×%d", rw, rh)
	}

	// Монитор не узнать — масштаб основного.
	monitorDPIAt = func(int32, int32) uint32 { return 0 }
	if s := (&hiddenWindow{}).scale(1.75); s != 1.75 {
		t.Errorf("без DPI монитора масштаб %v, ожидался основного 1.75", s)
	}
}
