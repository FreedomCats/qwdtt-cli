// lifecycle.go — цикл подключения (задача F3): то единственное место в
// супервизоре, где есть время, горутины и внешний мир.
//
// Автомат (state.go) помнит, где мы; политика (restart.go) считает задержки
// и годность хешей; здесь они связываются с ядром и datapath. Цикл запускает
// попытку, раздаёт события ядра всем троим, поднимает datapath по конфигу от
// сервера, опрашивает watchdog своим тикером и ждёт назначенную задержку перед
// следующей попыткой.
//
// Главное обязательство файла — datapath снимается всегда: при обычном
// возврате из ядра, при остановке по Ctrl+C и при панике внутри ядра
// (architecture.md §07).
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
)

// DefaultStallPoll — как часто цикл спрашивает watchdog, жив ли туннель.
// Чаще секунды незачем: ядро присылает статистику раз в три секунды, а окно
// терпения измеряется десятками секунд.
const DefaultStallPoll = time.Second

// ErrHashesExhausted — годных хешей звонка не осталось, пробовать больше нечем.
// Единственная причина, по которой цикл заканчивается сам, а не по просьбе.
var ErrHashesExhausted = errors.New("годных хешей звонка не осталось")

// Options — из чего собирается цикл подключения.
type Options struct {
	// Config — настройки ядра. Поле Hashes заполняет сам цикл, из пула перед
	// каждой попыткой: негодный хеш не должен попасть в следующую.
	Config core.Config

	// Hashes — хеши звонков VK, как их дал профиль. Порядок сохраняется.
	Hashes []string

	// NewDatapath строит datapath для одной попытки. Обязателен.
	//
	// Именно конструктор, а не готовый экземпляр: реализация одноразовая —
	// Start у неё зовётся один раз, Close закрывает её навсегда, — а конфиг
	// WireGuard сервер выдаёт новый на каждое подключение. Проверить Requires
	// и объяснить нехватку прав нужно до входа в цикл, и это дело CLI.
	//
	// Исключение — datapath.Persistent (tun): такой экземпляр цикл
	// строит один раз, на первой попытке, зовёт его Start на каждую
	// попытку, Suspend — после каждой неудачной и Close — при выходе из Run.
	NewDatapath func() (datapath.Datapath, error)

	// DatapathStage — какой шаг закрывает подъём datapath: core.StageSocks
	// или core.StageVPN. Пусто — core.StageSocks.
	//
	// Сам datapath своего имени не знает и знать не должен (D1); какой из двух
	// шагов показать пользователю, известно тому, кто выбрал реализацию.
	DatapathStage core.Stage

	// ProbeDNS проверяет резолвер до запуска ядра и закрывает шаг «dns» —
	// единственный шаг линии, который идёт до core.Run (core/events.go, «Кто
	// какие шаги отправляет»). Отказ означает отказ попытки: без резолвера
	// ядру не за чем идти, а сеть, где его нет, обычно возвращается — и
	// следующая попытка это увидит. nil — шаг не проверяется и не отправляется.
	//
	// Срок ожидания задаёт сама функция: цикл передаёт ей контекст попытки,
	// который живёт до её конца. Готовая реализация — core.ProbeDNS.
	ProbeDNS func(ctx context.Context) error

	// Control — ручка управления ядром: пауза, ответ на капчу, ответ на запрос
	// TURN-кредов. nil допустим и означает, что управлять не понадобится.
	Control *core.Control

	// OnEvent получает все события ядра, включая те, что супервизор собрал сам
	// (шаги datapath и done). nil — молчать.
	//
	// Зовётся из горутин воркеров ядра, но не одновременно: цикл сериализует
	// OnEvent и OnTransition между собой, чтобы получателю — а это вывод в
	// терминал — не понадобился собственный замок.
	OnEvent func(core.Event)

	// OnTransition получает состоявшуюся смену состояния: §07 требует печатать
	// её строкой. nil — молчать.
	OnTransition func(Transition)

	// Logf получает строки самого цикла, которые не события ядра и не смена
	// состояния: что маршруты сняты на время переподключения и что они
	// возвращены (plan-e2.md, решение 3). Сериализован с OnEvent и
	// OnTransition. nil — молчать.
	Logf func(format string, args ...any)

	// StallWindow — сколько молчания терпит watchdog, прежде чем объявить
	// туннель деградировавшим. 0 — DefaultStallWindow (25 с).
	StallWindow time.Duration

	// StallPoll — как часто опрашивается watchdog. 0 — DefaultStallPoll.
	StallPoll time.Duration

	// StallBytes — сколько восходящих байт без ответа считается отправкой,
	// а не keepalive WireGuard. 0 — DefaultStallBytes.
	StallBytes int64

	// StallRestartAfter — сколько ещё длится молчание в состоянии «трафика
	// нет», прежде чем попытка обрывается и начинается новая. 0 — столько же,
	// сколько StallWindow: деградация в 25 с, перезапуск на пятидесятой.
	//
	// Две величины, а не одна, потому что молчание в двадцать пять секунд
	// бывает и у живого туннеля: сначала мы говорим об этом пользователю
	// и только потом рвём соединение.
	StallRestartAfter time.Duration
}

// Tunnel — цикл подключения: ядро, datapath, автомат и политика перезапуска,
// связанные вместе.
//
// Экземпляр одноразовый: Run зовётся один раз и возвращается, когда туннель
// остановлен окончательно.
type Tunnel struct {
	opts      Options
	machine   *Machine
	hashes    *HashPool
	watchdog  *Watchdog
	restarter *Restarter

	// outMu сериализует OnEvent, OnTransition и Logf: события приходят из
	// горутин воркеров, переходы watchdog'а — из горутины тикера, а получатель
	// у них один — вывод в терминал.
	outMu sync.Mutex

	// dpMu охраняет то, что переживает попытку: долгоживущий datapath
	// и состояние маршрутов. Пишут сюда горутина ядра (подъём datapath)
	// и горутина Run (конец попытки).
	dpMu sync.Mutex
	// persistent — datapath.Persistent, построенный первой попыткой; живёт
	// до конца Run. nil — datapath строится на каждую попытку.
	persistent datapath.Datapath
	// routed — трафик системы сейчас идёт в туннель: поднят datapath,
	// который забирает весь трафик (Requirements.Netguard).
	routed bool
	// bypassed — маршруты сняты на время переподключения, и об этом сказано
	// строкой; когда datapath поднимется снова, нужно сказать, что вернулись.
	bypassed bool

	// restartMu охраняет намеренный перезапуск (Restart): что прерывать
	// и что ещё не исполнено.
	restartMu sync.Mutex
	// current — идущая попытка; nil между попытками.
	current *attempt
	// wake прерывает идущую задержку; nil вне задержки.
	wake context.CancelFunc
	// restartReq — причина перезапуска, который ещё не исполнен: просьба
	// пришла, когда прерывать было нечего, или попытка уже кончалась.
	// Снимается в начале следующей попытки — она и есть перезапуск.
	restartReq error

	// runCore и sleep подменяются в тестах: без первого понадобился бы живой
	// сервер VK, без второго тест на последовательность задержек шёл бы минуту.
	runCore func(context.Context, core.Config, core.Sink, *core.Control) error
	sleep   func(context.Context, time.Duration) bool
}

// NewTunnel собирает цикл. Ошибку возвращает только на неполных настройках:
// сеть до Run не трогается.
func NewTunnel(opts Options) (*Tunnel, error) {
	if opts.NewDatapath == nil {
		return nil, errors.New("супервизор: не задан конструктор datapath")
	}
	if opts.DatapathStage == "" {
		opts.DatapathStage = core.StageSocks
	}
	if opts.StallWindow <= 0 {
		opts.StallWindow = DefaultStallWindow
	}
	if opts.StallPoll <= 0 {
		opts.StallPoll = DefaultStallPoll
	}
	if opts.StallRestartAfter <= 0 {
		opts.StallRestartAfter = opts.StallWindow
	}
	if opts.StallBytes <= 0 {
		opts.StallBytes = DefaultStallBytes
	}

	machine := New()
	hashes := NewHashPool(opts.Hashes)
	watchdog := NewWatchdog(opts.StallWindow)
	watchdog.minBytes = opts.StallBytes
	return &Tunnel{
		opts:      opts,
		machine:   machine,
		hashes:    hashes,
		watchdog:  watchdog,
		restarter: NewRestarter(machine, hashes),
		runCore:   core.Run,
		sleep:     sleepCtx,
	}, nil
}

// State — текущее состояние подключения. Нужно статусной строке (G4), которая
// перерисовывается по таймеру, а не по переходам.
func (t *Tunnel) State() State { return t.machine.State() }

// Machine — автомат: таблица шагов текущей попытки и причина последнего отказа.
func (t *Tunnel) Machine() *Machine { return t.machine }

// Hashes — пул хешей: какие ещё годны и почему исключены остальные.
func (t *Tunnel) Hashes() *HashPool { return t.hashes }

// Attempts — сколько попыток подряд кончилось неудачей.
func (t *Tunnel) Attempts() int { return t.restarter.Attempts() }

// Restart обрывает идущую попытку с причиной reason и начинает новую сразу,
// без задержки и с обнулённым счётчиком задержек (Restarter.Restart). Если
// цикл ждёт задержку, ожидание прерывается. Зовётся при смене сети (S2 Э2)
// из любой горутины; до Run и после него ничего не делает.
//
// Маршруты на время перезапуска снимаются, как после любой неудачной
// попытки: на новой сети может понадобиться капча.
func (t *Tunnel) Restart(reason error) {
	t.restartMu.Lock()
	t.restartReq = reason
	a, wake := t.current, t.wake
	t.restartMu.Unlock()

	if a != nil {
		a.fail(reason)
		a.cancel()
	}
	if wake != nil {
		wake()
	}
}

// takeRestart забирает неисполненную просьбу о перезапуске.
func (t *Tunnel) takeRestart() error {
	t.restartMu.Lock()
	defer t.restartMu.Unlock()
	reason := t.restartReq
	t.restartReq = nil
	return reason
}

// track отмечает идущую попытку (nil — попытка кончилась). Начало попытки
// исполняет просьбу о перезапуске: попытка и есть перезапуск.
func (t *Tunnel) track(a *attempt) {
	t.restartMu.Lock()
	defer t.restartMu.Unlock()
	t.current = a
	if a != nil {
		t.restartReq = nil
	}
}

// pause ждёт задержку перед следующей попыткой. Restart прерывает её
// и обнуляет счётчик задержек. Возвращает false, если цикл остановлен.
func (t *Tunnel) pause(ctx context.Context, d time.Duration) bool {
	sctx, wake := context.WithCancel(ctx)
	defer wake()

	t.restartMu.Lock()
	if t.restartReq != nil {
		// Перезапуск попросили после конца попытки, но до задержки.
		t.restartMu.Unlock()
		t.restarter.Reset()
		return true
	}
	t.wake = wake
	t.restartMu.Unlock()

	slept := t.sleep(sctx, d)

	t.restartMu.Lock()
	t.wake = nil
	woken := t.restartReq != nil
	t.restartMu.Unlock()

	if ctx.Err() != nil || (!slept && !woken) {
		return false
	}
	if woken {
		t.restarter.Reset()
	}
	return true
}

// Run держит туннель, пока не отменят ctx или пока не кончатся годные хеши.
//
// Остановка по ctx ошибкой не считается и даёт nil — как и у core.Run. Ошибка
// возвращается только вместе с ErrHashesExhausted: другого способа кончиться
// сам у цикла нет, отказ попытки означает следующую попытку.
//
// К возврату datapath снят, горутины цикла закончились, а автомат стоит
// в StateStopping.
func (t *Tunnel) Run(ctx context.Context) error {
	// Остановка отмечается в любом случае: по Ctrl+C, по исчерпанным хешам
	// и по панике, которую цикл не удержал.
	defer func() { t.report(t.machine.Stop()) }()
	// Долгоживущий datapath снимается при любом выходе — после последней
	// попытки, как одноразовый снимается в конце своей.
	defer t.closePersistent()

	for {
		if ctx.Err() != nil {
			return nil
		}

		err := t.attempt(ctx)
		if ctx.Err() != nil {
			// Остановку попросили — не отказ, и следующей попытки не будет.
			return nil
		}
		// До задержки, а не после: пока маршруты ведут в мёртвый туннель,
		// капча следующей попытки не загрузится.
		t.suspend()

		var decision Decision
		if reason := t.takeRestart(); reason != nil {
			// Перезапуск попросили, пока шла попытка или сразу после неё.
			// Сменилась сеть — её причина важнее отказа на старой.
			decision = t.restarter.Restart(reason)
		} else {
			decision = t.restarter.Failed(err)
		}
		t.reportAll(decision.Transitions)

		if decision.Stop {
			if err != nil {
				return fmt.Errorf("%w: %w", ErrHashesExhausted, err)
			}
			return ErrHashesExhausted
		}
		if !t.pause(ctx, decision.Delay) {
			return nil
		}
	}
}

// attempt проводит одну попытку подключения: поднимает ядро, по его конфигу
// поднимает datapath и возвращается, когда попытка кончилась.
//
// Нулевая ошибка означает только одно: попытку остановил вызывающий отменой
// ctx. Все остальные исходы — отказ, по которому назначается новая попытка.
func (t *Tunnel) attempt(parent context.Context) (err error) {
	t.report(t.machine.BeginAttempt())
	t.watchdog.Reset()

	dp, persistent, derr := t.datapath()
	if derr != nil {
		return fmt.Errorf("datapath: %w", derr)
	}
	// Первым отложенным вызовом — то есть последним исполняемым: снятие
	// datapath обязано пережить и обычный возврат, и панику в ядре.
	// Close у реализаций безопасен до Start и при повторном вызове (D1).
	// Долгоживущий снимает Run, а после неудачной попытки — suspend.
	if !persistent {
		defer func() { _ = dp.Close() }()
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	a := &attempt{tunnel: t, dp: dp, ctx: ctx, cancel: cancel}
	t.track(a)
	defer t.track(nil)

	// Паника в ядре становится обычным отказом попытки: клиент, который
	// держит туннель часами, не должен умирать от бага в одном воркере.
	// Со стеком — иначе баг не найти, а отказ выглядел бы сетевым.
	//
	// Удержать так можно только панику в той горутине, что зовёт core.Run.
	// Паника в горутине воркера роняет процесс мимо любых defer — от неё
	// защиты нет ни у кого, кроме самого ядра.
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("паника в ядре: %v\n%s", p, debug.Stack())
		}
	}()

	// Резолвер проверяется до тикера watchdog'а: пока ядро не запущено,
	// молчание в сети — не повод для тревоги.
	if err := a.probeDNS(); err != nil {
		return err
	}

	// Тикер watchdog'а снимается раньше всего остального: он умеет обрывать
	// попытку, и пережить её ему нельзя.
	defer t.watchTraffic(ctx, cancel, a)()

	cfg := t.opts.Config
	cfg.Hashes = t.hashes.Alive()

	runErr := t.runCore(ctx, cfg, core.SinkFunc(a.emit), t.opts.Control)

	switch {
	case parent.Err() != nil:
		return nil
	case a.failure() != nil:
		// Попытку оборвали мы сами — watchdog или отказ datapath, — и ядро
		// вернуло nil, потому что для него это была отмена ctx. Своя причина
		// точнее.
		return a.failure()
	case runErr != nil:
		return runErr
	default:
		return errors.New("ядро остановилось само, без ошибки")
	}
}

// datapath — datapath для попытки: долгоживущий, если он уже есть, иначе
// новый. Второе значение — datapath долгоживущий, и закрывать его в конце
// попытки нельзя.
func (t *Tunnel) datapath() (datapath.Datapath, bool, error) {
	t.dpMu.Lock()
	defer t.dpMu.Unlock()
	if t.persistent != nil {
		return t.persistent, true, nil
	}
	dp, err := t.opts.NewDatapath()
	if err != nil {
		return nil, false, err
	}
	if _, ok := dp.(datapath.Persistent); ok {
		t.persistent = dp
		return dp, true, nil
	}
	return dp, false, nil
}

// datapathUp отмечает, что datapath поднят. Если до этого маршруты сняли на
// время переподключения, говорит, что они вернулись.
func (t *Tunnel) datapathUp(dp datapath.Datapath) {
	if !dp.Requires().Netguard {
		// socks трафик системы не забирает: снимать и возвращать нечего.
		return
	}
	t.dpMu.Lock()
	t.routed = true
	returned := t.bypassed
	t.bypassed = false
	t.dpMu.Unlock()
	if returned {
		t.logf("маршруты возвращены — трафик системы снова идёт через туннель")
	}
}

// suspend — конец неудачной попытки: у долгоживущего datapath снимаются
// маршруты и защита DNS (одноразовый к этому моменту уже закрыт целиком),
// и пользователь видит строку о том, что трафик пошёл мимо туннеля
// (plan-e2.md, решение 3: окно нужно видеть, а не узнавать из README).
func (t *Tunnel) suspend() {
	t.dpMu.Lock()
	dp := t.persistent
	t.dpMu.Unlock()

	if p, ok := dp.(datapath.Persistent); ok {
		if err := p.Suspend(); err != nil {
			// Снять маршруты не вышло — снимаем datapath целиком: с маршрутами
			// на мёртвом туннеле не загрузится капча. Следующая попытка
			// построит новый.
			t.logf("маршруты не снялись: %v — datapath снимается целиком, следующая попытка поднимет его заново", err)
			t.closePersistent()
		}
	}

	t.dpMu.Lock()
	wasRouted := t.routed
	t.routed = false
	if wasRouted {
		t.bypassed = true
	}
	t.dpMu.Unlock()
	if wasRouted {
		t.logf("маршруты сняты на время переподключения — трафик и DNS системы идут мимо туннеля")
	}
}

// closePersistent снимает долгоживущий datapath, если он есть.
func (t *Tunnel) closePersistent() {
	t.dpMu.Lock()
	dp := t.persistent
	t.persistent = nil
	t.dpMu.Unlock()
	if dp != nil {
		_ = dp.Close()
	}
}

// logf отдаёт наружу строку самого цикла.
func (t *Tunnel) logf(format string, args ...any) {
	if t.opts.Logf == nil {
		return
	}
	t.outMu.Lock()
	defer t.outMu.Unlock()
	t.opts.Logf(format, args...)
}

// watchTraffic заводит тикер watchdog'а и возвращает функцию его снятия.
//
// Опрос идёт своим тикером, а не по событиям, потому что беда здесь — это как
// раз отсутствие событий: молчащее ядро ничего не пришлёт и о своём молчании
// не сообщит.
func (t *Tunnel) watchTraffic(ctx context.Context, cancel context.CancelFunc, a *attempt) func() {
	done := make(chan struct{})

	go func() {
		defer close(done)

		ticker := time.NewTicker(t.opts.StallPoll)
		defer ticker.Stop()

		// since — когда молчание началось. Отсчёт до перезапуска идёт отсюда,
		// а не от начала попытки: туннель мог проработать час и замолчать.
		var since time.Time

		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				reason := t.watchdog.Stall(now)
				if reason == StallNone {
					since = time.Time{}
					t.report(t.machine.Recover())
					continue
				}
				if since.IsZero() {
					since = now
				}
				t.report(t.machine.Degrade())

				if now.Sub(since) < t.opts.StallRestartAfter {
					continue
				}
				a.fail(fmt.Errorf("туннель молчит дольше %s: %s",
					t.opts.StallWindow+t.opts.StallRestartAfter, reason))
				cancel()
				return
			}
		}
	}()

	// Сначала отменить, потом дождаться: горутина уходит только по ctx, и
	// снятие без отмены повисло бы навсегда. Отмена здесь безвредна — попытка
	// к этому моменту уже кончилась.
	return func() {
		cancel()
		<-done
	}
}

// report отдаёт состоявшийся переход наружу. Пару (Transition, bool) принимает
// как есть, чтобы вызов писался в одну строку: t.report(t.machine.Degrade()).
func (t *Tunnel) report(tr Transition, moved bool) {
	if !moved || t.opts.OnTransition == nil {
		return
	}
	t.outMu.Lock()
	defer t.outMu.Unlock()
	t.opts.OnTransition(tr)
}

// reportAll отдаёт наружу переходы, сделанные политикой перезапуска.
func (t *Tunnel) reportAll(moves []Transition) {
	for _, tr := range moves {
		t.report(tr, true)
	}
}

// ──────────────────────────── попытка ────────────────────────────

// attempt — состояние одной попытки подключения. Живёт ровно столько, сколько
// длится один core.Run.
type attempt struct {
	tunnel *Tunnel
	dp     datapath.Datapath
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	cause   error // почему попытку оборвал сам супервизор
	started bool  // datapath уже поднимали
}

// emit — получатель событий ядра. Раздаёт каждое событие автомату, пулу хешей
// и watchdog'у, отдаёт наружу и поднимает datapath по конфигу от сервера.
//
// Зовётся из горутин воркеров: всё, к чему обращается, — под замком.
func (a *attempt) emit(ev core.Event) {
	t := a.tunnel

	tr, moved := t.machine.Observe(ev)
	t.hashes.Observe(ev)
	t.watchdog.Observe(ev, time.Now())

	// Событие и вызванный им переход уходят под одним замком: иначе строка
	// о смене состояния могла бы вклиниться между чужим событием и его
	// переходом.
	t.outMu.Lock()
	if t.opts.OnEvent != nil {
		t.opts.OnEvent(ev)
	}
	if moved && t.opts.OnTransition != nil {
		t.opts.OnTransition(tr)
	}
	t.outMu.Unlock()

	if moved && tr.To == StateConnected {
		// Туннель поднялся: задержки, заработанные прошлыми неудачами,
		// больше не в счёт.
		t.restarter.Connected()
	}

	if conf, ok := ev.(core.EventConfig); ok {
		a.startDatapath(conf)
	}
}

// probeDNS закрывает шаг «dns»: проверяет резолвер, если цикл попросили об
// этом. Отказ возвращается как причина отказа попытки.
func (a *attempt) probeDNS() error {
	probe := a.tunnel.opts.ProbeDNS
	if probe == nil {
		return nil
	}

	a.stage(core.StageDNS, core.StageRunning, "")
	if err := probe(a.ctx); err != nil {
		a.stage(core.StageDNS, core.StageFailed, err.Error())
		return fmt.Errorf("dns: %w", err)
	}
	a.stage(core.StageDNS, core.StageOK, "")
	return nil
}

// startDatapath поднимает datapath по конфигу WireGuard, который сервер вернул
// в GETCONF, и закрывает шаги vpn/socks и done.
//
// Работа идёт прямо в вызове emit, а не в отдельной горутине: EventConfig ядро
// отправляет один раз и из собственной горутины serveConfig, которой больше
// нечем заняться (core/run.go). Ни один воркер на этом не стоит, а поднимать
// datapath в фоне значило бы сказать «туннель готов» раньше, чем он готов.
func (a *attempt) startDatapath(ev core.EventConfig) {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return
	}
	a.started = true
	a.mu.Unlock()

	stage := a.tunnel.opts.DatapathStage
	a.stage(stage, core.StageRunning, "")

	conf, err := datapath.ParseWgQuick(ev.WireGuard)
	if err == nil {
		err = a.dp.Start(a.ctx, conf)
	}
	if err != nil {
		a.stage(stage, core.StageFailed, err.Error())
		a.fail(fmt.Errorf("datapath: %w", err))
		// Ядро живо и будет держать воркеров вечно, а подавать в них нечего:
		// попытку обрываем мы.
		a.cancel()
		return
	}

	a.tunnel.datapathUp(a.dp)
	a.stage(stage, core.StageOK, "")
	a.stage(core.StageDone, core.StageOK, "")
}

// stage отправляет шаг, который знает супервизор, а не ядро (core/events.go,
// «Кто какие шаги отправляет»). Идёт тем же путём, что события ядра: разница
// между «шаг закрыло ядро» и «шаг закрыли мы» получателя не касается.
func (a *attempt) stage(stage core.Stage, status core.StageStatus, msg string) {
	a.emit(core.EventStage{Stage: stage, Status: status, Msg: msg})
}

// fail запоминает, почему попытку оборвал сам супервизор. Первая причина
// побеждает: отказ datapath роняет воркеров, и молчание туннеля после этого —
// следствие, а не причина.
func (a *attempt) fail(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cause == nil {
		a.cause = err
	}
}

// failure — причина, по которой попытку оборвал супервизор. nil означает, что
// попытка кончилась сама.
func (a *attempt) failure() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cause
}

// ──────────────────────────── Ctrl+C ────────────────────────────

// Interrupt возвращает контекст, отменяемый по первому Ctrl+C (SIGINT) или
// SIGTERM, и функцию снятия обработчика.
//
// Второй Ctrl+C убивает процесс немедленно: по первому сигналу обработчик
// снимается, и следующий уходит системе, у которой поведение по умолчанию —
// завершить процесс. Ждать корректной остановки дважды пользователь не обязан,
// а собственного os.Exit это не требует — выход из библиотеки дело плохое, да
// и на зависшем ядре он бы не помог: горутина, печатающая «выхожу», может
// стоять в том же замке, что и всё остальное.
//
// Возвращённую функцию нужно вызвать при выходе: она снимает обработчик
// и отменяет контекст.
func Interrupt(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case <-signals:
			signal.Stop(signals)
			cancel()
		case <-ctx.Done():
			signal.Stop(signals)
		}
	}()

	return ctx, func() {
		signal.Stop(signals)
		cancel()
	}
}

// sleepCtx ждёт d или отмены ctx. Возвращает false, если ждать перестали.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
