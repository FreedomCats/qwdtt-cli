// Package captcha — внешний решатель Smart Captcha VK.
//
// Своего решателя у ядра для нас нет: нативный решатель Go v2 перестал
// разбирать страницу капчи VK, и оболочка запускает ядро в режиме wv
// (config.Profile.Core), где он не зовётся вовсе (architecture.md, Р5 и §08).
// На каждую капчу ядро сразу выходит наружу: отправляет
// core.EventCaptchaRequired с режимом selected и ждёт ответа с таймаутом.
//
// solver.go — каркас (задача E1): интерфейс Solver, разбор события в Request и
// Dispatcher, который связывает одно с другим и отвечает ядру через
// core.Control.SubmitCaptcha. Сами решатели живут рядом: manual.go — ручной
// режим (E2), webview2_windows.go — окно WebView2 на Windows (E3),
// browser_linux.go — браузер под управлением по CDP на Linux. Какой из них
// взять, решает CLI.
package captcha

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

// Mode — чем ядро занималось до того, как обратилось наружу. От режима зависит
// только одно, зато важное: сколько ядро согласно ждать ответа.
//
// В режиме ядра wv, в котором его запускает оболочка, приходит только
// ModeSelected. ModeAuto и ModeManual — шаги цепочки auto: их ядро шлёт,
// если запустить его в auto или rjs, и диспетчер разбирает их по-прежнему.
type Mode string

const (
	// ModeAuto — автоцепочка не прошла, идёт запасной путь WBV Auto.
	// Ядро ждёт десять секунд: человеку этого не хватит заведомо.
	ModeAuto Mode = "auto"
	// ModeManual — автоцепочка кончилась, ядро открывает ручной режим.
	// Три минуты на всё: открыть ссылку, решить капчу, вернуть токен.
	ModeManual Mode = "manual"
	// ModeSelected — режим wv: ядро сразу идёт наружу, своим решателем капчу
	// не пробует. Окно то же, что у ModeManual, — человеку, и срок тот же,
	// три минуты (правило в scripts/vendorcore; у апстрима — две).
	ModeSelected Mode = "selected"
)

// DefaultMargin — запас, на который решатель обязан ответить раньше, чем ядро
// перестанет слушать. Токен, пришедший после таймаута, не просто бесполезен:
// ядро уже пошло дальше, а лишний ответ останется в канале и будет выброшен
// перед следующим запросом (core.Control.SubmitCaptcha).
const DefaultMargin = time.Second

// DefaultHumanWindow — меньше этого срока человека просить бессмысленно.
//
// Десятисекундное окно ModeAuto закрывается отказом сразу, и это не потеря,
// а выигрыш: в автоцепочке ядра таких окон два подряд, и без немедленного
// отказа каждая капча стоила бы двадцати секунд пустого ожидания, прежде чем
// ядро дойдёт до ручного режима. Окна ModeManual и ModeSelected — по три
// минуты, за них человек берётся.
const DefaultHumanWindow = 30 * time.Second

// ErrNoSolver — решателя нет вовсе. Ядру такой отказ отправляется немедленно.
var ErrNoSolver = errors.New("внешний решатель капчи не задан")

// ErrBusy — решатель ещё занят прошлой капчей, хотя ядро её уже не ждёт.
var ErrBusy = errors.New("решатель уже занят другой капчей")

// ErrTooShort — до конца ожидания ядра осталось меньше, чем нужно человеку.
var ErrTooShort = errors.New("окна ядра не хватит на ручное решение")

// Request — просьба решить капчу, разобранная из core.EventCaptchaRequired.
type Request struct {
	// Session — номер воркера, который просит. Воркеров бывает до 108, и знать,
	// чей запрос на экране, полезно.
	Session int
	// Mode — чем ядро занималось до обращения наружу.
	Mode Mode
	// RedirectURI — страница капчи VK, которую нужно открыть.
	RedirectURI string
	// SessionToken — токен сессии капчи; без него ответ не примут.
	SessionToken string
	// Deadline — момент, после которого ответ уже не нужен. Решателю его стоит
	// показать: человеку важно знать, сколько у него времени.
	Deadline time.Time
}

// Left — сколько осталось до конца ожидания ядра.
func (r Request) Left(now time.Time) time.Duration {
	if r.Deadline.IsZero() {
		return 0
	}
	return r.Deadline.Sub(now)
}

// Solver решает капчу и возвращает токен.
//
// Ошибка означает «не решил»; ядро получит отказ: в режиме wv воркер останется
// без кредов, в цепочке auto ядро пойдёт к следующему шагу.
// Пустой токен без ошибки читается так же — обещать успех и не дать токен
// нельзя.
//
// Solve обязан уважать ctx: он несёт и срок ядра, и остановку клиента по Ctrl+C.
// Решателю, который ждёт человека, этого мало — прочитать строку со stdin по
// отмене ctx стандартными средствами невозможно, — поэтому ждать ввод он должен
// в отдельной горутине, а возвращаться по ctx.Done() (см. manual.go).
type Solver interface {
	Solve(ctx context.Context, req Request) (string, error)

	// NeedsHuman — решателю нужен человек. От этого зависит, браться ли за
	// короткие окна: десять секунд машине хватит, а человеку нет.
	NeedsHuman() bool
}

// Options — настройки диспетчера.
type Options struct {
	// Solver — решатель. nil допустим и означает, что капчу решать некому:
	// ядро получит отказ немедленно, без ожидания.
	Solver Solver

	// Control — ручка ядра, через которую уходит ответ. nil допустим (вызовы
	// core.Control безопасны на nil), но тогда ответ никуда не уйдёт.
	Control *core.Control

	// Logf — куда писать; nil означает молчать. Как и у datapath, свой вывод
	// пакет не заводит: он принадлежит CLI.
	Logf func(format string, args ...any)

	// Margin — насколько раньше срока ядра обязан ответить решатель.
	// 0 — DefaultMargin.
	Margin time.Duration

	// HumanWindow — окно, короче которого не стоит просить человека.
	// 0 — DefaultHumanWindow.
	HumanWindow time.Duration

	// Stop — остановка клиента (Ctrl+C). С её отменой решатель получает
	// отмену, а ядро — отказ, не дожидаясь Close. Это не то же, что Close в
	// defer: ядро ждёт ответа на капчу без своего ctx, до трёх минут, и
	// остановка туннеля ждёт его воркер — до defer дело бы не дошло. nil —
	// только Close.
	Stop context.Context
}

// Dispatcher принимает события ядра и отвечает на них через решатель.
//
// Подключается в тот же Sink, что автомат супервизора: Observe разбирает
// core.EventCaptchaRequired, остальные события пропускает.
type Dispatcher struct {
	opts Options

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	busy   bool
	closed bool

	// timeout подменяется в тестах: настоящие окна ядра — десять секунд
	// и три минуты, и тест на опоздавший ответ шёл бы минуты.
	timeout func(mode string) time.Duration
}

// New собирает диспетчер. Решатель до первого события не трогается.
func New(opts Options) *Dispatcher {
	if opts.Margin <= 0 {
		opts.Margin = DefaultMargin
	}
	if opts.HumanWindow <= 0 {
		opts.HumanWindow = DefaultHumanWindow
	}

	parent := opts.Stop
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &Dispatcher{
		opts:    opts,
		ctx:     ctx,
		cancel:  cancel,
		timeout: core.CaptchaTimeout,
	}
}

// Observe — получатель событий ядра. Возвращается сразу: решение уходит
// в отдельную горутину.
//
// В фон это вынесено не ради скорости, а потому что иначе всё встало бы:
// Observe зовётся из Sink, а Sink супервизора держит на время вызова замок
// вывода (lifecycle.go). Решатель, ждущий человека минутами, заморозил бы на это
// время весь вывод клиента.
func (d *Dispatcher) Observe(ev core.Event) {
	required, ok := ev.(core.EventCaptchaRequired)
	if !ok {
		return
	}
	d.solve(d.request(required, time.Now()))
}

// Close отменяет незаконченное решение и дожидается его. Безопасен при
// повторном вызове.
func (d *Dispatcher) Close() {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()

	d.cancel()
	d.wg.Wait()
}

// request превращает событие ядра в просьбу, добавляя к ней срок.
//
// Срок в событии не приходит: ядро его знает, но не отправляет. Считается он
// по режиму — core.CaptchaTimeout берёт те же константы, по которым ядро
// и ждёт, — за вычетом запаса на дорогу ответа.
func (d *Dispatcher) request(ev core.EventCaptchaRequired, now time.Time) Request {
	budget := d.timeout(ev.Mode) - d.opts.Margin
	if budget < 0 {
		budget = 0
	}

	mode := Mode(strings.ToLower(strings.TrimSpace(ev.Mode)))
	switch mode {
	case ModeManual, ModeSelected:
	default:
		// Ядро приводит режим к одному из трёх и незнакомый читает как auto
		// (creds.go, requestWebViewCaptcha). Читаем так же.
		mode = ModeAuto
	}

	return Request{
		Session:      ev.Session,
		Mode:         mode,
		RedirectURI:  ev.RedirectURI,
		SessionToken: ev.SessionToken,
		Deadline:     now.Add(budget),
	}
}

// solve берётся за просьбу или отказывается от неё.
func (d *Dispatcher) solve(req Request) {
	if err := d.claim(req); err != nil {
		d.decline(req, err)
		return
	}

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()

		ctx, cancel := context.WithDeadline(d.ctx, req.Deadline)
		defer cancel()

		d.logf("[КАПЧА] воркер %d просит решить капчу (%s, %s на ответ)",
			req.Session, req.Mode, req.Left(time.Now()).Round(time.Second))

		token, err := d.opts.Solver.Solve(ctx, req)
		// Решатель освобождается до ответа ядру: получив ответ, ядро может
		// сразу попросить следующую капчу, и она не должна застать решатель
		// занятым — её отбросили бы молча (claim), и воркер выстоял бы таймаут.
		d.release()
		switch {
		case err != nil:
			d.decline(req, err)
		case strings.TrimSpace(token) == "":
			d.decline(req, errors.New("решатель вернул пустой токен"))
		default:
			d.logf("[КАПЧА] капча решена, токен отправлен ядру")
			d.opts.Control.SubmitCaptcha(token)
		}
	}()
}

// claim проверяет, беря́сь ли мы за эту просьбу, и занимает решатель.
func (d *Dispatcher) claim(req Request) error {
	if d.opts.Solver == nil {
		return ErrNoSolver
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed || d.ctx.Err() != nil {
		return context.Canceled
	}
	if d.busy {
		// Двух просьб разом ядро не шлёт: капчу просят только из-под
		// глобального замка кредов, на всё время ожидания ответа
		// (fetchVkCredsSerialized; сторож — TestCaptchaRequestsSerialized
		// в core). Значит, новая просьба при занятом решателе приходит
		// только тогда, когда прошлую ядро уже бросило по таймауту, а
		// решатель не уложился в её срок и ещё не вернулся.
		//
		// Ей отказ не отправляется. Канал ответа у ядра один на процесс и
		// к просьбе не привязан, а решатель, вернувшись, положит туда
		// запоздалый ответ прошлой — токен или отказ, — и заберёт его новая
		// просьба. Второй ответ, наш, ничего бы не добавил. Не вернётся
		// решатель вовсе — новая просьба выстоит свой таймаут.
		return ErrBusy
	}
	if d.opts.Solver.NeedsHuman() && req.Left(time.Now()) < d.opts.HumanWindow {
		return ErrTooShort
	}

	d.busy = true
	return nil
}

// release освобождает решатель.
func (d *Dispatcher) release() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.busy = false
}

// decline отправляет ядру отказ — пустой токен, который core.Control.SubmitCaptcha
// превращает в error:cancelled. Ядро засчитает попытку провальной и пойдёт
// дальше, не выстояв таймаут: в режиме wv воркер не стоит три минуты перед
// закрытым окном, а на цепочке AUTO это экономит по десять секунд на каждом
// окне WBV.
//
// Молча отбрасывается ровно один случай — ErrBusy: см. claim.
func (d *Dispatcher) decline(req Request, cause error) {
	if errors.Is(cause, ErrBusy) {
		d.logf("[КАПЧА] воркер %d просит капчу, но решатель занят — пропускаем", req.Session)
		return
	}

	d.logf("[КАПЧА] воркер %d: капча не решена (%v)", req.Session, cause)
	d.opts.Control.SubmitCaptcha("")
}

func (d *Dispatcher) logf(format string, args ...any) {
	if d.opts.Logf == nil {
		return
	}
	d.opts.Logf(format, args...)
}

// Токен, который ждёт ядро, — это success_token из ответа VK на
// captchaNotRobot.check, а не параметр в ссылке редиректа. На Android его
// доставали инъекцией JS в WebView, перехватывая fetch и XMLHttpRequest
// (CaptchaWebViewManager.kt:70–120). Отдельного редиректа с токеном в URL
// у VK нет вовсе, поэтому разбора ссылки здесь и не заведено: окно WebView2
// перехватит ответ так же, как Android, а как добыть токен в консоли без
// браузера под управлением — вопрос задачи E2.
