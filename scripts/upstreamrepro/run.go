package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const pinnedCommit = "fae121efc3ef57b633516601d3c0d6b1be1fde7c"

const droppedPatch = "4-dropped-packets.patch"
const captchaTimeoutsPatch = "6-captcha-timeouts.patch"
const captchaWVLogPatch = "7-captcha-wv-log.patch"

var patches = []string{"1-closed-call.patch", "2-baton.patch", "3-config-quota.patch", droppedPatch, "5-stop.patch", captchaTimeoutsPatch, captchaWVLogPatch}

type expectation struct {
	name, action string
	evidence     []*regexp.Regexp
	allowed      []*regexp.Regexp
}

type reproCase struct {
	baseline, patched expectation
	injected          bool
	counters          bool
	captchaTimeouts   bool
	captchaWVLog      bool
}

func diagnostic(text string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^\s+repro_test\.go:[0-9]+: ` + text + `$`)
}

// Each failure needs its original assertion, not just a nonzero exit status.
var cases = []reproCase{
	{
		baseline: expectation{name: "TestReproClosedCallIsDead", action: "fail", evidence: []*regexp.Regexp{
			diagnostic(`как есть: статус "error" \([^\n]*\), ожидался dead`),
			diagnostic(`путь vkcalls: статус "error" \([^\n]*\), ожидался dead`),
			diagnostic(`обёрнута в %w: статус "error" \([^\n]*\), ожидался dead`),
		}},
	},
	{
		baseline: expectation{name: "TestReproGroupPassesBatonOnCredsFailure", action: "fail", evidence: []*regexp.Regexp{
			diagnostic(`эстафета не передана: следующая группа ждала бы вечно`),
		}},
	},
	{
		baseline: expectation{name: "TestReproConfigDespiteQuota", action: "fail", evidence: []*regexp.Regexp{
			diagnostic(`конфиг не пришёл за 15 с: 486 отдано [1-9][0-9]* раз, GETCONF 0, сеансов [1-9][0-9]*`),
		}},
	},
	{
		baseline: expectation{name: "TestReproStopDuringConfigRequest", action: "fail", evidence: []*regexp.Regexp{
			diagnostic(`WorkerGroup не вернулась за 5s после отмены`),
		}, allowed: []*regexp.Regexp{diagnostic(`вернулась через [0-9][0-9.hmsµ]*`)}},
	},
	{
		baseline: expectation{name: "TestReproStopRightAfterSessionStart", action: "fail", evidence: []*regexp.Regexp{
			diagnostic(`WorkerGroup не вернулась за 5s после отмены`),
			diagnostic(`и через [0-9][0-9.hmsµ]* не вернулась — дальше не ждём`),
		}, allowed: []*regexp.Regexp{diagnostic(`WorkerGroup так и не вернулась`)}},
		injected: true,
	},
	{
		baseline: expectation{name: "TestReproDroppedUp", action: "fail", evidence: []*regexp.Regexp{
			diagnostic(`missing drop counter: Stats\.DroppedUp`),
		}},
		counters: true,
	},
	{
		baseline: expectation{name: "TestReproDroppedDown", action: "fail", evidence: []*regexp.Regexp{
			diagnostic(`missing drop counter: Stats\.DroppedDown`),
		}},
		counters: true,
	},
	{
		baseline: expectation{name: "TestReproDroppedStatsOutput", action: "fail", evidence: []*regexp.Regexp{
			diagnostic(`statistics output omits drop counters: want DroppedUp=7 and DroppedDown=11`),
		}, allowed: []*regexp.Regexp{diagnostic(`statistics output: \[СТАТИСТИКА\] Активных: 3 \| Трафик: 7\.00 МБ \| ↓5\.00 МБ / ↑2\.00 МБ`)}},
		counters: true,
	},
	{
		baseline: expectation{name: "TestReproCaptchaManualTimeout", action: "fail", evidence: []*regexp.Regexp{
			diagnostic(`manual captcha timeout: got 1m0s, want 3m0s`),
		}},
		captchaTimeouts: true,
	},
	{
		baseline: expectation{name: "TestReproCaptchaSelectedTimeout", action: "fail", evidence: []*regexp.Regexp{
			diagnostic(`selected captcha timeout: got 2m0s, want 3m0s`),
		}},
		captchaTimeouts: true,
	},
	{
		baseline:        expectation{name: "TestReproCaptchaAutoTimeout", action: "pass"},
		captchaTimeouts: true,
	},
	{
		baseline: expectation{name: "TestReproCaptchaWVLog", action: "fail", evidence: []*regexp.Regexp{
			diagnostic(`wv diagnostic still claims Android settings`),
		}},
		captchaWVLog: true,
	},
}

type testRun struct {
	name              string
	patched, injected bool
	patches           []string
	want              []expectation
}

func testRuns() []testRun {
	var runs []testRun
	for _, patched := range []bool{false, true} {
		for _, injected := range []bool{false, true} {
			r := testRun{patched: patched, injected: injected, name: "baseline"}
			if patched {
				r.name = "patched"
				r.patches = slices.Clone(patches)
			}
			if injected {
				r.name += "-injected"
			}
			for _, c := range cases {
				if injected && !c.injected {
					continue
				}
				want := c.baseline
				if patched {
					want = c.patched
					if want.name == "" {
						want = expectation{name: c.baseline.name, action: "pass"}
					}
				}
				if c.injected && !injected {
					want = expectation{name: c.baseline.name, action: "skip", evidence: []*regexp.Regexp{
						diagnostic(`нужна вставленная задержка: см\. run\.sh`),
					}}
				}
				r.want = append(r.want, want)
			}
			runs = append(runs, r)
		}
	}
	counters := testRun{name: "patched-counters", patched: true, patches: []string{droppedPatch}}
	for _, c := range cases {
		if c.counters {
			counters.want = append(counters.want, expectation{name: c.baseline.name, action: "pass"})
		}
	}
	runs = append(runs, counters)
	timeouts := testRun{name: "patched-captcha-timeouts", patched: true, patches: []string{captchaTimeoutsPatch}}
	wvLog := testRun{name: "patched-captcha-wv-log", patched: true, patches: []string{captchaWVLogPatch}}
	for _, c := range cases {
		if c.captchaTimeouts {
			timeouts.want = append(timeouts.want, expectation{name: c.baseline.name, action: "pass"})
		}
		if c.captchaWVLog {
			wvLog.want = append(wvLog.want, expectation{name: c.baseline.name, action: "pass"})
		}
	}
	runs = append(runs, timeouts, wvLog)
	return runs
}

func (r testRun) pattern() string {
	names := make([]string, len(r.want))
	for i, want := range r.want {
		names[i] = regexp.QuoteMeta(want.name)
	}
	return "^(" + strings.Join(names, "|") + ")$"
}

type command struct {
	name string
	args []string
	dir  string
	env  []string
}

type processResult struct {
	stdout, stderr []byte
	exitCode       int
	err            error
}

type executor func(context.Context, command) processResult

func execute(ctx context.Context, c command) processResult {
	cmd := exec.CommandContext(ctx, c.name, c.args...)
	cmd.Dir, cmd.Env = c.dir, c.env
	cmd.WaitDelay = 2 * time.Second
	configureProcess(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	finishProcess(cmd)
	result := processResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: -1}
	if cmd.ProcessState != nil {
		result.exitCode = cmd.ProcessState.ExitCode()
	}
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || result.exitCode < 0) {
		result.err = err
	}
	if ctx.Err() != nil {
		result.err = ctx.Err()
	}
	return result
}

// Ignore inherited command/configuration overrides that can enable downloads,
// replace test execution, or redirect Git to a caller's index or work tree.
func offlineEnv(inherited []string) []string {
	var env []string
	for _, entry := range inherited {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") || strings.HasPrefix(key, "REPRO_") || slices.Contains([]string{
			"GOENV", "GOFLAGS", "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GOWORK", "GOVCS", "GOTELEMETRY", "GO111MODULE",
			"GOOS", "GOARCH", "GOEXPERIMENT", "GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GOINSECURE", "GOAUTH", "GOCACHEPROG",
		}, key) {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off", "GOENV=off", "GOFLAGS=", "GOVCS=*:off",
		"GOTELEMETRY=off", "GO111MODULE=on", "GOOS=", "GOARCH=", "GOEXPERIMENT=",
		"GOPRIVATE=", "GONOPROXY=none", "GONOSUMDB=none", "GOINSECURE=", "GOAUTH=off", "GOCACHEPROG=",
		"GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_ATTR_NOSYSTEM=1",
	)
}

type runner struct {
	root, source, pin, tempParent string
	env                           []string
	output                        io.Writer
	exec                          executor
}

func newRunner(root, source string, output io.Writer) runner {
	return runner{root: root, source: source, pin: pinnedCommit, env: offlineEnv(os.Environ()), output: output, exec: execute}
}

func (r runner) command(ctx context.Context, c command, showOutput bool) processResult {
	if c.env == nil {
		c.env = r.env
	}
	fmt.Fprintf(r.output, "$ %s %s\n", c.name, strings.Join(c.args, " "))
	result := r.exec(ctx, c)
	if showOutput {
		r.output.Write(result.stdout)
	}
	r.output.Write(result.stderr)
	return result
}

func processError(result processResult) error {
	if result.err != nil {
		return result.err
	}
	if result.exitCode != 0 {
		return fmt.Errorf("process exited with status %d", result.exitCode)
	}
	return nil
}

func gitCommand(dir string, args ...string) command {
	return command{name: "git", dir: dir, args: append([]string{
		"-c", "protocol.allow=never", "-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false", "-c", "core.attributesFile=" + os.DevNull,
		"-c", "gc.auto=0", "-c", "maintenance.auto=false",
	}, args...)}
}

func (r runner) run(ctx context.Context) (err error) {
	return r.runInputs(ctx, testRuns())
}

// Focused evidence can select existing inputs without duplicating preparation or
// relaxing the per-test result validator. The prepared-source entry point uses all inputs.
func (r runner) runInputs(ctx context.Context, runs []testRun) (err error) {
	if r.source == "" {
		return errors.New("a prepared local source is required; usage: upstreamrepro <prepared-local-source>")
	}
	source, err := filepath.Abs(r.source)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(source); err != nil || !fi.IsDir() {
		return errors.New("local source directory not found; supply a prepared repository containing the pinned commit (no clone or fetch is attempted)")
	}
	assets := filepath.Join(r.root, "doc", "upstream-repro")
	repro, err := os.ReadFile(filepath.Join(assets, "repro_test.go"))
	if err != nil {
		return fmt.Errorf("read reproduction assets; run from the qwdtt-cli root: %w", err)
	}
	tempParent := r.tempParent
	if tempParent == "" {
		tempParent = os.TempDir()
	}
	tempParent, err = filepath.EvalSymlinks(tempParent)
	if err != nil {
		return err
	}
	for _, protected := range []string{source, r.root} {
		protected, err = filepath.EvalSymlinks(protected)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(protected, tempParent)
		if err != nil || filepath.IsLocal(rel) {
			return errors.New("temporary directory must be outside the source and project; set TMPDIR to a separate local directory")
		}
	}
	work, err := os.MkdirTemp(tempParent, "upstreamrepro-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(work)) }()
	tmp := filepath.Join(work, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return err
	}
	r.env = append(slices.DeleteFunc(slices.Clone(r.env), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return key == "GOCACHE" || slices.Contains([]string{"TMPDIR", "TMP", "TEMP", "GOTMPDIR"}, key)
	}), "TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp, "GOTMPDIR="+tmp, "GOCACHE="+filepath.Join(work, "build-cache"), "GIT_CEILING_DIRECTORIES="+work)

	setupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	cache := r.command(setupCtx, command{name: "go", dir: work, args: []string{"env", "GOMODCACHE"}}, true)
	cancel()
	if err := processError(cache); err != nil {
		return fmt.Errorf("locate offline module cache: %w; install a compatible local Go toolchain on PATH (downloads are disabled)", err)
	}
	cacheDir, err := filepath.EvalSymlinks(strings.TrimSpace(string(cache.stdout)))
	if err != nil {
		return fmt.Errorf("local dependency cache is unavailable: %w; prepare it separately before running (downloads are disabled)", err)
	}
	for _, protected := range []string{source, r.root} {
		protected, err = filepath.EvalSymlinks(protected)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(protected, cacheDir)
		if err != nil || filepath.IsLocal(rel) {
			return errors.New("GOMODCACHE must be outside the source and project; use a separately prepared local dependency cache")
		}
	}
	r.env = append(slices.DeleteFunc(r.env, func(entry string) bool {
		return strings.HasPrefix(entry, "GOMODCACHE=")
	}), "GOMODCACHE="+cacheDir)
	setupCtx, cancel = context.WithTimeout(ctx, 2*time.Minute)
	store := r.command(setupCtx, gitCommand(work, "-C", source, "rev-parse", "--path-format=absolute", "--git-path", "objects"), true)
	cancel()
	if err := processError(store); err != nil {
		return fmt.Errorf("locate local Git objects: %w; supply a prepared local repository and an installed Git", err)
	}
	objects := strings.TrimSpace(string(store.stdout))
	if !filepath.IsAbs(objects) {
		return errors.New("Git did not return an absolute local object-store path")
	}
	// Private metadata has no remotes, promisor configuration, replace refs or
	// info/attributes. Even older Git cannot lazily fetch through the source config.
	metadata := filepath.Join(work, "export.git")
	if err := os.MkdirAll(filepath.Join(metadata, "refs"), 0o700); err != nil {
		return err
	}
	if err := writeNew(filepath.Join(metadata, "HEAD"), []byte("ref: refs/heads/unborn\n")); err != nil {
		return err
	}
	if err := writeNew(filepath.Join(metadata, "config"), []byte("[core]\n\trepositoryformatversion = 0\n\tbare = true\n")); err != nil {
		return err
	}
	export := gitCommand(work, "--git-dir="+metadata, "archive", "--format=tar", r.pin, "go_client")
	export.env = append(slices.Clone(r.env), "GIT_OBJECT_DIRECTORY="+objects)
	setupCtx, cancel = context.WithTimeout(ctx, 2*time.Minute)
	archive := r.command(setupCtx, export, false)
	cancel()
	if err := processError(archive); err != nil {
		return fmt.Errorf("export pinned commit %s: %w; supply its objects in the prepared local repository and an installed Git (implicit fetching is disabled)", r.pin, err)
	}
	for _, test := range runs {
		if err := ctx.Err(); err != nil {
			return err
		}
		input := filepath.Join(work, test.name)
		if err := os.Mkdir(input, 0o700); err != nil {
			return err
		}
		if err := extractArchive(ctx, archive.stdout, input); err != nil {
			return fmt.Errorf("%s archive: %w", test.name, err)
		}
		for _, name := range test.patches {
			patch := filepath.Join(assets, name)
			for _, flags := range [][]string{{"--check"}, {}} {
				args := append([]string{"apply"}, flags...)
				args = append(args, "--", patch)
				setupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				result := r.command(setupCtx, gitCommand(input, args...), true)
				cancel()
				if err := processError(result); err != nil {
					return fmt.Errorf("%s patch %s: %w", test.name, name, err)
				}
			}
		}
		gc := filepath.Join(input, "go_client")
		if err := checkInput(gc); err != nil {
			return fmt.Errorf("%s input: %w", test.name, err)
		}
		if test.injected {
			if err := injectDelay(filepath.Join(gc, "session.go")); err != nil {
				return fmt.Errorf("%s: %w", test.name, err)
			}
		}
		if err := writeNew(filepath.Join(gc, "repro_test.go"), repro); err != nil {
			return err
		}
		setupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		prepared := r.command(setupCtx, command{name: "go", dir: gc, args: []string{"mod", "tidy"}}, true)
		cancel()
		if err := processError(prepared); err != nil {
			return fmt.Errorf("%s offline module preparation: %w; install a compatible local Go toolchain and prepare the dependency cache separately (downloads are disabled)", test.name, err)
		}
		fmt.Fprintf(r.output, "== %s (%s)\n", test.name, r.pin)
		env := slices.Clone(r.env)
		if test.injected {
			env = append(env, "REPRO_INJECT_DELAY=1")
		}
		testCtx, cancel := context.WithTimeout(ctx, 12*time.Minute)
		result := r.command(testCtx, command{name: "go", dir: gc, env: env, args: []string{
			"test", "-json", "-tags=upstream_repro", "-count=1", "-timeout=10m", "-shuffle=off", "-parallel=1", "-run", test.pattern(), ".",
		}}, true)
		cancel()
		if err := validateResult(result, test.want); err != nil {
			return fmt.Errorf("%s: %w", test.name, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintln(r.output, "All selected baseline and patched expectations matched.")
	return nil
}

func writeNew(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Close())
}

func checkInput(dir string) error {
	if err := filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.EqualFold(entry.Name(), ".git") || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("unsafe patched input entry %q", name)
		}
		return nil
	}); err != nil {
		return err
	}
	if fi, err := os.Lstat(filepath.Join(dir, "go.mod")); err != nil || !fi.Mode().IsRegular() {
		return errors.New("pinned go_client/go.mod is missing or not a regular file; refusing module preparation outside the disposable module")
	}
	return nil
}

// Links and special files are unnecessary for go_client. Rejecting them also
// prevents an extracted input from redirecting later patch or Go commands.
func extractArchive(ctx context.Context, data []byte, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	seen := make(map[string]bool)
	tr := tar.NewReader(bytes.NewReader(data))
	files := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := strings.TrimSuffix(h.Name, "/")
		if path.Clean(name) != name || strings.ContainsAny(name, `\:`) ||
			(name != "go_client" && !strings.HasPrefix(name, "go_client/")) ||
			slices.ContainsFunc(strings.Split(name, "/"), func(part string) bool { return strings.EqualFold(part, ".git") }) || seen[name] {
			return fmt.Errorf("unsafe or duplicate archive path %q", h.Name)
		}
		seen[name] = true
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(filepath.FromSlash(name), 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := root.MkdirAll(filepath.FromSlash(path.Dir(name)), 0o700); err != nil {
				return err
			}
			mode := os.FileMode(0o600)
			if h.Mode&0o111 != 0 {
				mode = 0o700
			}
			f, err := root.OpenFile(filepath.FromSlash(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if err := errors.Join(err, f.Close()); err != nil {
				return err
			}
			files++
		default:
			return fmt.Errorf("unsupported archive entry %q (type %d, link %q)", h.Name, h.Typeflag, h.Linkname)
		}
	}
	if files == 0 {
		return errors.New("archive has no go_client files")
	}
	return nil
}

const delayMarker = "// REPRO: widen the read-deadline race"

func injectDelay(name string) error {
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	anchor := regexp.MustCompile(`(?m)^(\t*)_ = activeConn\.SetReadDeadline\(time\.Now\(\)\.Add\(sessionReadTimeout\)\)$`)
	if matches := len(anchor.FindAll(data, -1)); matches != 1 || bytes.Contains(data, []byte(delayMarker)) {
		return fmt.Errorf("deadline-delay injection requires exactly one fresh anchor, found %d", matches)
	}
	data = anchor.ReplaceAll(data, []byte("${1}time.Sleep(300 * time.Millisecond) "+delayMarker+"\n${0}"))
	if bytes.Count(data, []byte(delayMarker)) != 1 {
		return errors.New("deadline-delay injection did not occur exactly once")
	}
	return os.WriteFile(name, data, 0o600)
}

type testEvent struct {
	Action, Package, Test, Output string
}

type observedTest struct {
	started, paused bool
	action          string
	output          strings.Builder
}

var fatalOutput = regexp.MustCompile(`(?m)(^|\n)\s*(panic:|fatal error:|runtime:|SIGSEGV|SIGBUS|SIGABRT|WARNING: DATA RACE|testing: warning: no tests to run)`)
var setupFailure = regexp.MustCompile(`(?mi)\[build failed\]|\[setup failed\]|(?:bind|socket): [^\n]+`)
var assertionLine = regexp.MustCompile(`(?m)^\s+repro_test\.go:[0-9]+: [^\n]*`)

func validateResult(result processResult, want []expectation) error {
	if result.err != nil {
		return fmt.Errorf("test process did not complete normally: %w", result.err)
	}
	wantPackage, wantExit := "pass", 0
	observed := make(map[string]*observedTest)
	for _, w := range want {
		if w.action == "fail" {
			wantPackage, wantExit = "fail", 1
		}
		observed[w.name] = &observedTest{}
	}
	if result.exitCode != wantExit {
		return fmt.Errorf("test process exit status %d, want %d", result.exitCode, wantExit)
	}
	var output strings.Builder
	output.Write(result.stderr)
	output.WriteByte('\n')
	decoder := json.NewDecoder(bytes.NewReader(result.stdout))
	pkg, packageAction := "", ""
	for {
		var event testEvent
		err := decoder.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid go test JSON: %w", err)
		}
		if event.Package == "" || packageAction != "" {
			return errors.New("missing package or event after package completion in go test JSON")
		}
		output.WriteString(event.Output)
		if pkg == "" {
			if event.Action != "start" || event.Test != "" {
				return errors.New("missing package start in go test JSON (possible build/setup failure)")
			}
			pkg = event.Package
			continue
		}
		if event.Package != pkg {
			return fmt.Errorf("unexpected package %q in go test JSON", event.Package)
		}
		if event.Test == "" {
			switch event.Action {
			case "output":
			case "pass", "fail":
				packageAction = event.Action
			default:
				return fmt.Errorf("unexpected package action %q", event.Action)
			}
			continue
		}
		test := observed[event.Test]
		if test == nil || test.action != "" {
			return fmt.Errorf("unexpected test or event after test completion: %s", event.Test)
		}
		switch event.Action {
		case "run":
			if test.started {
				return fmt.Errorf("duplicate run of %s", event.Test)
			}
			test.started = true
		case "output", "pass", "fail", "skip", "pause", "cont":
			if !test.started {
				return fmt.Errorf("missing run of %s", event.Test)
			}
			switch event.Action {
			case "output":
				test.output.WriteString(event.Output)
			case "pause", "cont":
				if test.paused != (event.Action == "cont") {
					return fmt.Errorf("invalid pause/continue of %s", event.Test)
				}
				test.paused = event.Action == "pause"
			default:
				test.action = event.Action
			}
		default:
			return fmt.Errorf("unexpected test action %q", event.Action)
		}
	}
	if fatalOutput.MatchString(output.String()) {
		return errors.New("panic, timeout or runtime failure in test diagnostics")
	}
	if setupFailure.MatchString(output.String()) {
		return errors.New("build/setup or loopback bind failure in test diagnostics")
	}
	if packageAction != wantPackage {
		return fmt.Errorf("package result %q, want %q (missing JSON, build/setup failure or incomplete run)", packageAction, wantPackage)
	}
	for _, w := range want {
		test := observed[w.name]
		if test.action != w.action {
			return fmt.Errorf("%s result %q, want %q", w.name, test.action, w.action)
		}
		text := test.output.String()
		for _, evidence := range w.evidence {
			if !evidence.MatchString(text) {
				return fmt.Errorf("%s lacks intended assertion %s", w.name, evidence)
			}
		}
		if w.action != "pass" {
			allowed := append(slices.Clone(w.evidence), w.allowed...)
			for _, line := range assertionLine.FindAllString(text, -1) {
				if !slices.ContainsFunc(allowed, func(pattern *regexp.Regexp) bool { return pattern.MatchString(line) }) {
					return fmt.Errorf("%s has an unexpected assertion/setup diagnostic: %s", w.name, line)
				}
			}
		}
	}
	return nil
}
