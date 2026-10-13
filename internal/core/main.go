package core

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// CaptchaResultChan — канал для получения токена капчи из внешнего решателя (WebView)
var CaptchaResultChan = make(chan string, 1)

var captchaModeValue atomic.Value

func init() {
	captchaModeValue.Store("auto")
}

func normalizeCaptchaMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto", "rjs", "wv":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "auto"
	}
}

func setCaptchaMode(mode string) string {
	normalized := normalizeCaptchaMode(mode)
	captchaModeValue.Store(normalized)
	return normalized
}

func getCaptchaMode() string {
	mode, _ := captchaModeValue.Load().(string)
	if mode == "" {
		return "auto"
	}
	return mode
}

// drainCaptchaResult удаляет устаревший результат капчи из канала
func drainCaptchaResult() {
	select {
	case <-CaptchaResultChan:
	default:
	}
}

func runHashChecks(ctx context.Context, hashes []string) {
	logf("[CHECK] Проверка VK-хешей: %d", len(hashes))
	for i, hash := range hashes {
		emitHashCheck(i+1, len(hashes), hash, HashCheckRunning, "")
		checkCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		_, _, turnURLs, err := GetCreds(checkCtx, hash, 9000+i)
		cancel()

		status, message := classifyHashCheckError(err)
		if err == nil {
			status = "ok"
			message = fmt.Sprintf("TURN urls=%d", len(turnURLs))
		}
		emitHashCheck(i+1, len(hashes), hash, HashCheckStatus(status), sanitizeHashCheckMessage(message))
	}
}

func classifyHashCheckError(err error) (string, string) {
	if err == nil {
		return "ok", ""
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "captcha_required") || strings.Contains(text, "captcha_wait_required"):
		return "captcha", "VK просит капчу"
	case strings.Contains(text, "call not found") ||
		strings.Contains(text, "joinconversationbylink") ||
		strings.Contains(text, "missing turn_server") ||
		strings.Contains(text, "9000") ||
		strings.Contains(text, "callunavailable"):
		return "dead", "Звонок не найден или закрыт"
	case strings.Contains(text, "flood") || strings.Contains(text, "rate limit") || strings.Contains(text, "error_code:29"):
		return "limited", "VK временно ограничил запросы"
	case strings.Contains(text, "timeout") || strings.Contains(text, "deadline") || strings.Contains(text, "lookup") || strings.Contains(text, "network"):
		return "network", "Сетевая ошибка"
	default:
		return "error", err.Error()
	}
}

func sanitizeHashCheckMessage(message string) string {
	message = strings.ReplaceAll(message, "\n", " ")
	message = strings.ReplaceAll(message, "\r", " ")
	message = strings.ReplaceAll(message, "|", "/")
	if len(message) > 180 {
		return message[:180]
	}
	return message
}
