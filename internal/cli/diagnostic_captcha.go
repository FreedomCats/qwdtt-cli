package cli

import (
	"context"

	"github.com/FreedomCats/qwdtt-cli/internal/captcha"
	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

type diagnosticCaptcha struct {
	dispatcher *captcha.Dispatcher
}

func newDiagnosticCaptcha(ctx context.Context, e *env, out *printer, dial captcha.DialFunc) *diagnosticCaptcha {
	control := core.NewControl()
	if e.captchaControl != nil {
		control = e.captchaControl()
	}
	solver := e.captchaSolver
	if solver == nil {
		solver = captchaSolver(e, out, nil, dial)
	}
	return &diagnosticCaptcha{
		dispatcher: captcha.New(captcha.Options{
			Solver: solver, Control: control, Logf: out.logf, Stop: ctx,
		}),
	}
}

func (d *diagnosticCaptcha) close() { d.dispatcher.Close() }

func (d *diagnosticCaptcha) sink(handle func(core.Event)) core.Sink {
	return core.SinkFunc(func(ev core.Event) {
		handle(ev)
		d.dispatcher.Observe(ev)
	})
}
