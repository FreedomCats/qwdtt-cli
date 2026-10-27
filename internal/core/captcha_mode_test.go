package core

import (
	"context"
	"strings"
	"testing"
)

// В режиме wv — а в нём ядро запускает оболочка (config.Profile.Core) — на
// капчу сразу уходит просьба окна selected: ни попыток нативного решателя
// Go v2, ни окон WBV Auto, ни ручного окна после них. Тест — сторож на случай
// обновления ядра: апстрим может переставить ветки solveCaptchaBySelectedMode.
func TestWebViewModeAsksWindowAtOnce(t *testing.T) {
	// Перед решением ядро обновляет профиль капчи и пишет его в текущий
	// каталог (vk_profile.json, captcha_browser_fp).
	t.Chdir(t.TempDir())

	prev := getCaptchaMode()
	setCaptchaMode("wv")
	drainCaptchaResult()
	t.Cleanup(func() {
		setCaptchaMode(prev)
		drainCaptchaResult()
	})

	ctrl := NewControl()
	var asked []EventCaptchaRequired
	var logs []string
	setSink(SinkFunc(func(ev Event) {
		switch ev := ev.(type) {
		case EventCaptchaRequired:
			asked = append(asked, ev)
			// Канал перед просьбой уже очищен: ответ, отданный прямо из
			// получателя события, ядро и заберёт.
			ctrl.SubmitCaptcha("токен-из-окна")
		case EventLog:
			logs = append(logs, ev.Tag+" "+ev.Msg)
		}
	}))
	t.Cleanup(func() { setSink(nil) })

	captchaErr := &VkCaptchaError{
		RedirectURI:  "https://id.vk.com/not_robot_captcha?session_token=x",
		SessionToken: "сессия-капчи",
	}
	// Клиента нет: решатель ядра без него не прошёл бы и первого запроса,
	// а в wv его звать не должны вовсе.
	token, err := solveCaptchaBySelectedMode(context.Background(), 7, 1, captchaErr, nil, Profile{}, nil)
	if err != nil {
		t.Fatalf("капча не решена: %v", err)
	}
	if token != "токен-из-окна" {
		t.Errorf("токен %q, ожидался ответ окна", token)
	}

	if len(asked) != 1 {
		t.Fatalf("окно попросили %d раз, ожидался один", len(asked))
	}
	if ev := asked[0]; ev.Mode != "selected" || ev.Session != 7 || ev.RedirectURI != captchaErr.RedirectURI || ev.SessionToken != captchaErr.SessionToken {
		t.Errorf("просьба окна %+v", ev)
	}
	for _, line := range logs {
		if strings.Contains(line, "AUTO") || strings.Contains(line, "Go v2") {
			t.Errorf("в журнале ядра попытка решателя: %q", line)
		}
	}
}

// Окно selected — то же окно для человека, что manual, и срок у него тот же
// (правило в scripts/vendorcore: в апстриме — две минуты).
func TestSelectedWindowAsLongAsManual(t *testing.T) {
	if got, want := CaptchaTimeout("selected"), CaptchaTimeout("manual"); got != want {
		t.Errorf("окно selected %v, у manual %v", got, want)
	}
}
