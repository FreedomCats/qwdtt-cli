//go:build !linux

// browser_other.go — заглушка браузера под управлением там, где его нет.
//
// На Windows капчу показывает окно WebView2, а без него — ручной режим:
// управление по трубе там требует передать браузеру дескрипторы, чего
// exec.Cmd на Windows не умеет. См. browser_linux.go.
package captcha

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// ErrNoBrowser — браузера на Chromium нет. См. browser_linux.go.
var ErrNoBrowser = errors.New("браузер на Chromium не найден")

// BrowserOptions — настройки решателя с браузером. См. browser_linux.go.
type BrowserOptions struct {
	Out  io.Writer
	Dial DialFunc
	Logf func(format string, args ...any)
}

// Browser — решатель в браузере под управлением; здесь его не бывает.
type Browser struct{}

// NewBrowser здесь всегда отказывает с errors.ErrUnsupported: CLI по нему
// молча обходится без браузера.
func NewBrowser(BrowserOptions) (*Browser, error) {
	return nil, fmt.Errorf("браузер под управлением есть только на Linux: %w", errors.ErrUnsupported)
}

// NeedsHuman — как у настоящего.
func (*Browser) NeedsHuman() bool { return true }

// Solve недостижим: NewBrowser экземпляра не возвращает.
func (*Browser) Solve(context.Context, Request) (string, error) {
	return "", errors.ErrUnsupported
}
