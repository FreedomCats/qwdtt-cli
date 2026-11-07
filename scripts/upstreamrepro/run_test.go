package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fixtureSession = "package main\nfunc read() {\n\t_ = activeConn.SetReadDeadline(time.Now().Add(sessionReadTimeout))\n}\n"

var fixtureDiagnostics = map[string]string{
	"TestReproClosedCallIsDead": "    repro_test.go:48: как есть: статус \"error\" (VK returns error: Invalid join link (error_code=954)), ожидался dead\n" +
		"    repro_test.go:48: путь vkcalls: статус \"error\" (step=step2 calls.getCallPreview kind=call_unavailable: VK returns error: Invalid join link (error_code=954)), ожидался dead\n" +
		"    repro_test.go:48: обёрнута в %w: статус \"error\" (креды: VK returns error: Invalid join link (error_code=954)), ожидался dead\n",
	"TestReproGroupPassesBatonOnCredsFailure": "    repro_test.go:78: эстафета не передана: следующая группа ждала бы вечно\n",
	"TestReproConfigDespiteQuota":             "    repro_test.go:322: конфиг не пришёл за 15 с: 486 отдано 1 раз, GETCONF 0, сеансов 3\n",
	"TestReproStopDuringConfigRequest":        "    repro_test.go:347: WorkerGroup не вернулась за 5s после отмены\n    repro_test.go:347: вернулась через 45.001s\n",
	"TestReproStopRightAfterSessionStart": "    repro_test.go:373: WorkerGroup не вернулась за 5s после отмены\n" +
		"    repro_test.go:373: и через 1m5s не вернулась — дальше не ждём\n    repro_test.go:267: WorkerGroup так и не вернулась\n",
	"TestReproDroppedUp":   "    repro_test.go:500: missing drop counter: Stats.DroppedUp\n",
	"TestReproDroppedDown": "    repro_test.go:600: missing drop counter: Stats.DroppedDown\n",
	"TestReproDroppedStatsOutput": "    repro_test.go:700: statistics output: [СТАТИСТИКА] Активных: 3 | Трафик: 7.00 МБ | ↓5.00 МБ / ↑2.00 МБ\n" +
		"    repro_test.go:701: statistics output omits drop counters: want DroppedUp=7 and DroppedDown=11\n",
}

func fixtureEvents(run testRun) []testEvent {
	const pkg = "example.invalid/upstream"
	events := []testEvent{{Action: "start", Package: pkg}}
	packageAction := "pass"
	for _, w := range run.want {
		events = append(events, testEvent{Action: "run", Package: pkg, Test: w.name})
		output := ""
		if w.action == "fail" {
			output, packageAction = fixtureDiagnostics[w.name], "fail"
		} else if w.action == "skip" {
			output = "    repro_test.go:359: нужна вставленная задержка: см. run.sh\n"
		}
		if output != "" {
			events = append(events, testEvent{Action: "output", Package: pkg, Test: w.name, Output: output})
		}
		events = append(events, testEvent{Action: w.action, Package: pkg, Test: w.name})
	}
	return append(events, testEvent{Action: packageAction, Package: pkg})
}

func jsonResult(events []testEvent) processResult {
	var data bytes.Buffer
	for _, event := range events {
		if err := json.NewEncoder(&data).Encode(event); err != nil {
			panic(err)
		}
	}
	code := 0
	if len(events) > 0 && events[len(events)-1].Action == "fail" {
		code = 1
	}
	return processResult{stdout: data.Bytes(), exitCode: code}
}

func TestExpectationMatrix(t *testing.T) {
	for _, run := range testRuns() {
		t.Run(run.name, func(t *testing.T) {
			if err := validateResult(jsonResult(fixtureEvents(run)), run.want); err != nil {
				t.Fatal(err)
			}
			pattern := regexp.MustCompile(run.pattern())
			for _, w := range run.want {
				if !pattern.MatchString(w.name) || pattern.MatchString(w.name+"Extra") || pattern.MatchString("Other"+w.name) {
					t.Fatalf("unanchored selection: %s", run.pattern())
				}
			}
			if pattern.MatchString("TestUnrelated") || pattern.MatchString("TestReproUnselected") {
				t.Fatalf("broad selection: %s", run.pattern())
			}
			if run.injected {
				for _, w := range run.want {
					if w.action == "skip" {
						t.Fatal("injected check must run")
					}
				}
			}
			if run.name == "patched-counters" {
				if !reflect.DeepEqual(run.patches, []string{"4-dropped-packets.patch"}) || len(run.want) != 3 {
					t.Fatalf("counter-only input is not isolated: %+v", run)
				}
				for _, c := range cases {
					if pattern.MatchString(c.baseline.name) != c.counters {
						t.Fatalf("wrong counter-only selection: %s", run.pattern())
					}
				}
			} else if run.patched != reflect.DeepEqual(run.patches, patches) || (!run.patched && len(run.patches) != 0) {
				t.Fatalf("wrong ordinary/injected patch list: %+v", run)
			}
		})
	}
}

func TestCounterResultValidation(t *testing.T) {
	runs := testRuns()
	baseline, patched := runs[0], runs[len(runs)-1]
	baseline.want = slices.DeleteFunc(slices.Clone(baseline.want), func(w expectation) bool {
		return !regexp.MustCompile(patched.pattern()).MatchString(w.name)
	})
	for _, run := range []testRun{baseline, patched} {
		for _, w := range run.want {
			for _, mode := range []string{"wrong assertion", "missing", "skip", "setup", "build", "timeout"} {
				t.Run(run.name+"/"+w.name+"/"+mode, func(t *testing.T) {
					events := fixtureEvents(run)
					switch mode {
					case "missing":
						events = slices.DeleteFunc(events, func(e testEvent) bool { return e.Test == w.name })
					case "skip", "wrong assertion", "setup":
						for i := range events {
							if events[i].Test != w.name {
								continue
							}
							if mode == "skip" && (events[i].Action == "pass" || events[i].Action == "fail") {
								events[i].Action = "skip"
							}
							if mode == "wrong assertion" && events[i].Action == "output" {
								events[i].Output = "    repro_test.go:500: wrong drop counter type: Stats.DroppedUp is uint64, want atomic.Uint64\n"
							}
							if (mode == "wrong assertion" || mode == "setup") && events[i].Action == "pass" {
								events[i].Action = "fail"
							}
						}
						if mode == "setup" {
							terminal := slices.IndexFunc(events, func(e testEvent) bool { return e.Test == w.name && e.Action == "fail" })
							events = slices.Insert(events, terminal, testEvent{Action: "output", Package: events[0].Package, Test: w.name,
								Output: "    repro_test.go:600: downlink server setup: handshake failed\n"})
						}
					}
					result := jsonResult(events)
					if mode == "timeout" {
						result.err = context.DeadlineExceeded
					} else if mode == "build" {
						result.stdout = []byte("FAIL example.invalid/upstream [build failed]\n")
					}
					if err := validateResult(result, run.want); err == nil {
						t.Fatal("invalid counter evidence accepted")
					}
				})
			}
		}
	}
}

func TestRejectResults(t *testing.T) {
	runs := testRuns()
	tests := []struct {
		name   string
		run    testRun
		change func(*processResult)
	}{
		{"missing JSON", runs[0], func(r *processResult) { r.stdout = nil }},
		{"malformed JSON", runs[0], func(r *processResult) { r.stdout = append(r.stdout, []byte("not JSON\n")...) }},
		{"truncated JSON", runs[0], func(r *processResult) { r.stdout = []byte(`{"Action":`) }},
		{"null JSON", runs[0], func(r *processResult) { r.stdout = []byte("null\n") }},
		{"build failure", runs[0], func(r *processResult) {
			r.stdout = []byte("# example.invalid/upstream\n./source.go:1: undefined: missing\n")
			r.stderr = []byte("FAIL example.invalid/upstream [build failed]\n")
		}},
		{"start failure", runs[0], func(r *processResult) { r.err = exec.ErrNotFound; r.exitCode = -1 }},
		{"signal", runs[0], func(r *processResult) { r.err = errors.New("signal: terminated"); r.exitCode = -1 }},
		{"timeout", runs[0], func(r *processResult) { r.err = context.DeadlineExceeded }},
		{"canceled", runs[0], func(r *processResult) { r.err = context.Canceled }},
		{"wrong process status", runs[0], func(r *processResult) { r.exitCode = 2 }},
		{"baseline success", runs[0], func(r *processResult) { *r = jsonResult(fixtureEvents(runs[2])) }},
		{"patched process failure", runs[2], func(r *processResult) { r.exitCode = 1 }},
		{"panic on stderr", runs[0], func(r *processResult) { r.stderr = []byte("panic: fixture\n") }},
		{"bind failure on stderr", runs[0], func(r *processResult) { r.stderr = []byte("listen udp4 127.0.0.1:0: bind: operation not permitted\n") }},
		{"setup failure on stderr", runs[0], func(r *processResult) { r.stderr = []byte("FAIL example.invalid/upstream [setup failed]\n") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := jsonResult(fixtureEvents(tc.run))
			tc.change(&result)
			if err := validateResult(result, tc.run.want); err == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}

	eventTests := []struct {
		name   string
		run    testRun
		change func([]testEvent) []testEvent
	}{
		{"missing test", runs[0], func(events []testEvent) []testEvent {
			return slices.DeleteFunc(events, func(e testEvent) bool { return e.Test == runs[0].want[0].name })
		}},
		{"missing test completion", runs[0], func(events []testEvent) []testEvent { return slices.Delete(events, 3, 4) }},
		{"missing package start", runs[0], func(events []testEvent) []testEvent { return events[1:] }},
		{"missing package result", runs[0], func(events []testEvent) []testEvent { return events[:len(events)-1] }},
		{"missing assertion", runs[0], func(events []testEvent) []testEvent { events[2].Output = ""; return events }},
		{"wrong baseline assertion", runs[0], func(events []testEvent) []testEvent {
			events[2].Output = "    repro_test.go:115: listen udp4 127.0.0.1:0: bind: operation not permitted\n"
			return events
		}},
		{"loopback setup failure after assertion", runs[0], func(events []testEvent) []testEvent {
			events[2].Output += "    repro_test.go:115: listen udp4 127.0.0.1:0: bind: operation not permitted\n"
			return events
		}},
		{"quota stand never rejected", runs[0], func(events []testEvent) []testEvent {
			for i := range events {
				events[i].Output = strings.ReplaceAll(events[i].Output, "486 отдано 1 раз", "486 отдано 0 раз")
			}
			return events
		}},
		{"panic", runs[0], func(events []testEvent) []testEvent { events[2].Output += "panic: fixture\n"; return events }},
		{"test timeout", runs[0], func(events []testEvent) []testEvent {
			events[2].Output += "panic: test timed out after 10m0s\n"
			return events
		}},
		{"unattributed bind failure", runs[0], func(events []testEvent) []testEvent {
			return slices.Insert(events, 1, testEvent{Action: "output", Package: events[0].Package, Output: "listen udp4 127.0.0.1:0: socket: operation not permitted\n"})
		}},
		{"split panic", runs[0], func(events []testEvent) []testEvent {
			events[2].Output += "pa"
			return slices.Insert(events, 3, testEvent{Action: "output", Package: events[2].Package, Test: events[2].Test, Output: "nic: fixture\n"})
		}},
		{"extra package failure", runs[0], func(events []testEvent) []testEvent {
			return append(events, testEvent{Action: "fail", Package: "example.invalid/other"})
		}},
		{"unexpected test", runs[0], func(events []testEvent) []testEvent { events[1].Test = "TestUnrelated"; return events }},
		{"duplicate test", runs[0], func(events []testEvent) []testEvent { return slices.Insert(events, 4, events[1:4]...) }},
		{"unexpected skip", runs[0], func(events []testEvent) []testEvent { events[3].Action = "skip"; return events }},
		{"unexpected baseline pass", runs[0], func(events []testEvent) []testEvent { events[3].Action = "pass"; return events }},
		{"patched failure", runs[2], func(events []testEvent) []testEvent { events[2].Action = "fail"; return events }},
		{"package skip", runs[0], func(events []testEvent) []testEvent { events[len(events)-1].Action = "skip"; return events }},
		{"mismatched package result", runs[0], func(events []testEvent) []testEvent { events[len(events)-1].Action = "pass"; return events }},
		{"build JSON", runs[0], func(events []testEvent) []testEvent {
			return []testEvent{{Action: "build-output", Package: "example.invalid/upstream", Output: "undefined: missing"}, {Action: "build-fail", Package: "example.invalid/upstream"}}
		}},
	}
	for _, tc := range eventTests {
		t.Run(tc.name, func(t *testing.T) {
			result := jsonResult(tc.change(fixtureEvents(tc.run)))
			if err := validateResult(result, tc.run.want); err == nil {
				t.Fatal("invalid event stream accepted")
			}
		})
	}
}

func writeFixture(t *testing.T, name, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := gitCommand(dir, args...)
	c.env = append(offlineEnv(os.Environ()), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	result := execute(context.Background(), c)
	if err := processError(result); err != nil {
		t.Fatalf("fixture git %v: %v\n%s\n%s", args, err, result.stdout, result.stderr)
	}
	return string(result.stdout)
}

type fixture struct {
	runner
	log    *bytes.Buffer
	source string
	before map[string]string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	root, source := filepath.Join(base, "project"), filepath.Join(base, "source with spaces")
	writeFixture(t, filepath.Join(source, "go_client", "go.mod"), "module example.invalid/upstream\n\ngo 1.26.2\n")
	writeFixture(t, filepath.Join(source, "go_client", "session.go"), fixtureSession)
	var repro strings.Builder
	repro.WriteString("// Synthetic reproduction fixture.\npackage main\n")
	for _, c := range cases {
		fmt.Fprintf(&repro, "func %s() {}\n", c.baseline.name)
	}
	writeFixture(t, filepath.Join(root, "doc", "upstream-repro", "repro_test.go"), repro.String())
	for i, patch := range patches {
		name := fmt.Sprintf("fix%d.go", i)
		writeFixture(t, filepath.Join(source, "go_client", name), "package main\n// before\n")
		writeFixture(t, filepath.Join(root, "doc", "upstream-repro", patch), fmt.Sprintf(
			"diff --git a/go_client/%[1]s b/go_client/%[1]s\n--- a/go_client/%[1]s\n+++ b/go_client/%[1]s\n@@ -1,2 +1,2 @@\n package main\n-// before\n+// after\n", name))
	}
	fixtureGit(t, source, "init", "--quiet")
	fixtureGit(t, source, "add", ".")
	fixtureGit(t, source, "commit", "--quiet", "-m", "Fixture baseline")
	pin := strings.TrimSpace(fixtureGit(t, source, "rev-parse", "HEAD"))
	writeFixture(t, filepath.Join(source, "go_client", "head-only.txt"), "not pinned\n")
	fixtureGit(t, source, "add", ".")
	fixtureGit(t, source, "commit", "--quiet", "-m", "Fixture later HEAD")
	writeFixture(t, filepath.Join(source, "go_client", "session.go"), fixtureSession+"// staged edit\n")
	fixtureGit(t, source, "add", "go_client/session.go")
	writeFixture(t, filepath.Join(source, "go_client", "session.go"), fixtureSession+"// unstaged edit\n")
	writeFixture(t, filepath.Join(source, "untracked sentinel.txt"), "preserve this\n")
	writeFixture(t, filepath.Join(source, ".git", "info", "attributes"), "go_client/session.go export-ignore\n")
	fixtureGit(t, source, "config", "extensions.partialClone", "fixture")
	fixtureGit(t, source, "config", "remote.fixture.promisor", "true")
	fixtureGit(t, source, "config", "remote.fixture.url", "https://example.invalid/never-fetch")
	var log bytes.Buffer
	r := newRunner(root, source, &log)
	r.pin = pin
	r.tempParent = filepath.Join(base, "owned temporary inputs")
	if err := os.Mkdir(r.tempParent, 0o700); err != nil {
		t.Fatal(err)
	}
	return fixture{runner: r, log: &log, source: source, before: snapshotSource(t, source)}
}

func snapshotSource(t *testing.T, dir string) map[string]string {
	t.Helper()
	state := map[string]string{
		"HEAD":   fixtureGit(t, dir, "rev-parse", "HEAD"),
		"index":  fixtureGit(t, dir, "ls-files", "--stage"),
		"status": fixtureGit(t, dir, "status", "--porcelain=v1", "--untracked-files=all"),
	}
	err := filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == filepath.Join(dir, ".git") {
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			state["file:"+strings.TrimPrefix(name, dir)] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	state["raw index"] = string(index)
	for _, name := range []string{"HEAD", "config", "info/attributes"} {
		data, err := os.ReadFile(filepath.Join(dir, ".git", name))
		if err != nil {
			t.Fatal(err)
		}
		state["metadata:"+name] = string(data)
	}
	return state
}

func (f fixture) checkPreserved(t *testing.T) {
	t.Helper()
	if after := snapshotSource(t, f.source); !reflect.DeepEqual(after, f.before) {
		t.Error("source HEAD, index, tracked edits or untracked files changed")
	}
	entries, err := os.ReadDir(f.tempParent)
	if err != nil || len(entries) != 0 {
		t.Errorf("temporary inputs not cleaned: %v, %v", entries, err)
	}
}

func envMap(env []string) map[string]string {
	values := make(map[string]string)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	return values
}

func checkOffline(t *testing.T, env []string) {
	t.Helper()
	values := envMap(env)
	for key, want := range map[string]string{
		"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOWORK": "off", "GOENV": "off", "GOFLAGS": "",
		"GOVCS": "*:off", "GOTELEMETRY": "off", "GOAUTH": "off", "GOCACHEPROG": "", "GONOPROXY": "none",
	} {
		if got, ok := values[key]; !ok || got != want {
			t.Errorf("%s=%q, want %q (present %v)", key, got, want, ok)
		}
	}
}

func commandDirectoryOwned(parent, cwd string) bool {
	for _, dir := range []string{parent, cwd} {
		if !filepath.IsAbs(dir) {
			return false
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return false
		}
	}
	parent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return false
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(parent, cwd)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

func (f fixture) executor(t *testing.T, intercept func(context.Context, command) (processResult, bool)) executor {
	t.Helper()
	cache := t.TempDir()
	return func(ctx context.Context, c command) processResult {
		checkOffline(t, c.env)
		if !commandDirectoryOwned(f.tempParent, c.dir) {
			t.Fatalf("command cwd is not owned: cwd=%q, parent=%q", c.dir, f.tempParent)
		}
		if intercept != nil {
			if result, handled := intercept(ctx, c); handled {
				return result
			}
		}
		if c.name == "git" {
			if !slices.Contains(c.args, "archive") && !slices.Contains(c.args, "apply") && !slices.Contains(c.args, "rev-parse") {
				t.Fatalf("unexpected git operation: %v", c.args)
			}
			values := envMap(c.env)
			if values["GIT_NO_LAZY_FETCH"] != "1" || values["GIT_ALLOW_PROTOCOL"] != "" {
				t.Fatal("implicit Git fetching enabled")
			}
			if slices.Contains(c.args, "archive") && (!slices.Contains(c.args, f.pin) || slices.Contains(c.args, "HEAD")) {
				t.Fatalf("archive is not pinned: %v", c.args)
			}
			return execute(ctx, c)
		}
		if c.name != "go" {
			t.Fatalf("unexpected process: %s", c.name)
		}
		if reflect.DeepEqual(c.args, []string{"env", "GOMODCACHE"}) {
			return processResult{stdout: []byte(cache + "\n")}
		}
		if reflect.DeepEqual(c.args, []string{"mod", "tidy"}) {
			writeFixture(t, filepath.Join(c.dir, "go.sum"), "// Disposable module preparation.\n")
			return processResult{}
		}
		for _, run := range testRuns() {
			if filepath.Base(filepath.Dir(c.dir)) != run.name {
				continue
			}
			if got := envMap(c.env)["REPRO_INJECT_DELAY"]; (got == "1") != run.injected {
				t.Fatalf("injection env %q for %s", got, run.name)
			}
			data, err := os.ReadFile(filepath.Join(c.dir, "session.go"))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("edit")) || bytes.Count(data, []byte(delayMarker)) != boolInt(run.injected) {
				t.Fatalf("wrong pinned/injected source for %s: %s", run.name, data)
			}
			if _, err := os.Stat(filepath.Join(c.dir, "head-only.txt")); !os.IsNotExist(err) {
				t.Fatal("export used source HEAD instead of pin")
			}
			for i, patch := range patches {
				data, err := os.ReadFile(filepath.Join(c.dir, fmt.Sprintf("fix%d.go", i)))
				if err != nil || bytes.Contains(data, []byte("// after")) != slices.Contains(run.patches, patch) {
					t.Fatalf("wrong patch state for %s, %s: %s, %v", run.name, patch, data, err)
				}
			}
			copied, err := os.ReadFile(filepath.Join(c.dir, "repro_test.go"))
			original, originalErr := os.ReadFile(filepath.Join(f.root, "doc", "upstream-repro", "repro_test.go"))
			if err != nil || originalErr != nil || !bytes.Equal(copied, original) {
				t.Fatalf("full reproduction file was not copied: %v, %v", err, originalErr)
			}
			for _, c := range cases {
				if !bytes.Contains(copied, []byte("func "+c.baseline.name+"()")) {
					t.Fatalf("copied reproduction lacks %s", c.baseline.name)
				}
			}
			wantArgs := []string{"test", "-json", "-tags=upstream_repro", "-count=1", "-timeout=10m", "-shuffle=off", "-parallel=1", "-run", run.pattern(), "."}
			if !reflect.DeepEqual(c.args, wantArgs) {
				t.Fatalf("unselected or uncontrolled tests: %v", c.args)
			}
			return jsonResult(fixtureEvents(run))
		}
		t.Fatalf("unknown test input: %s", c.dir)
		return processResult{}
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestCommandDirectoryOwnership(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "owned temporary inputs")
	work := filepath.Join(parent, "work")
	module := filepath.Join(work, "go_client")
	spaced := filepath.Join(parent, "work with spaces", "module with spaces")
	sibling, outside := parent+"-sibling", filepath.Join(base, "outside")
	source, project := filepath.Join(base, "source with spaces"), filepath.Join(base, "project")
	for _, dir := range []string{module, spaced, sibling, outside, source, project} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(work, "file")
	writeFixture(t, file, "fixture\n")
	t.Chdir(base)
	sep := string(filepath.Separator)
	tests := []struct {
		name, parent, cwd string
		want              bool
	}{
		{"work", parent, work, true},
		{"module", parent, module, true},
		{"spaces", parent, spaced, true},
		{"parent itself", parent, parent, false},
		{"similarly prefixed sibling", parent, sibling, false},
		{"outside", parent, outside, false},
		{"source", parent, source, false},
		{"project", parent, project, false},
		{"escape via dot dot", parent, parent + sep + ".." + sep + "source with spaces", false},
		{"escape from work", parent, work + sep + ".." + sep + ".." + sep + "outside", false},
		{"missing parent", filepath.Join(base, "missing"), work, false},
		{"missing cwd", parent, filepath.Join(parent, "missing"), false},
		{"empty parent", "", work, false},
		{"empty cwd", parent, "", false},
		{"relative parent", filepath.Base(parent), work, false},
		{"relative cwd", parent, filepath.Join(filepath.Base(parent), "work"), false},
		{"both relative", filepath.Base(parent), filepath.Join(filepath.Base(parent), "work"), false},
		{"parent file", file, module, false},
		{"cwd file", parent, file, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandDirectoryOwned(tc.parent, tc.cwd); got != tc.want {
				t.Fatalf("commandDirectoryOwned(%q, %q) = %v, want %v", tc.parent, tc.cwd, got, tc.want)
			}
		})
	}
}

func TestWindowsTempParentAlias(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path normalization")
	}
	f := newFixture(t)
	parent := filepath.Join(f.tempParent, "MiXeD Parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	f.tempParent = filepath.Join(f.tempParent, "mIxEd pARENT")
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(f.tempParent)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(info, aliasInfo) {
		t.Fatalf("temporary parent alias is not the same directory: %q, %q", parent, f.tempParent)
	}
	testCount := 0
	f.exec = f.executor(t, func(ctx context.Context, c command) (processResult, bool) {
		resolved, err := filepath.EvalSymlinks(c.dir)
		if err != nil {
			t.Fatal(err)
		}
		if c.dir != resolved {
			t.Fatalf("command cwd is not resolved: %q, want %q", c.dir, resolved)
		}
		if strings.HasPrefix(c.dir, f.tempParent+string(filepath.Separator)) {
			t.Fatalf("raw prefix did not expose the alias: cwd=%q, parent=%q", c.dir, f.tempParent)
		}
		if c.name == "go" && c.args[0] == "test" {
			testCount++
		}
		return processResult{}, false
	})
	if err := f.run(context.Background()); err != nil {
		t.Fatalf("temporary parent alias: %v\n%s", err, f.log)
	}
	if testCount != len(testRuns()) {
		t.Fatalf("missing selected runs: %d", testCount)
	}
	f.checkPreserved(t)
}

func TestDirtySourceAndCleanup(t *testing.T) {
	for _, mode := range []string{"success", "missing pin", "toolchain failure", "setup failure", "archive failure", "missing module", "patch failure", "patch link", "injection failure", "result failure", "interruption"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "missing pin" {
				f.pin = strings.Repeat("0", 40)
			}
			if mode == "patch failure" {
				writeFixture(t, filepath.Join(f.root, "doc", "upstream-repro", patches[0]), "not a patch\n")
			}
			if mode == "patch link" {
				writeFixture(t, filepath.Join(f.root, "doc", "upstream-repro", patches[0]),
					"diff --git a/go_client/escape b/go_client/escape\nnew file mode 120000\n--- /dev/null\n+++ b/go_client/escape\n@@ -0,0 +1 @@\n+"+
						filepath.Join(f.source, "untracked sentinel.txt")+"\n\\ No newline at end of file\n")
			}
			testCount := 0
			f.exec = f.executor(t, func(ctx context.Context, c command) (processResult, bool) {
				if mode == "toolchain failure" && c.name == "go" {
					return processResult{err: exec.ErrNotFound, exitCode: -1}, true
				}
				if mode == "archive failure" && slices.Contains(c.args, "archive") {
					return processResult{stdout: tarFixture(t, []tar.Header{{Name: "../sentinel", Typeflag: tar.TypeReg}}, []string{"overwrite"})}, true
				}
				if (mode == "injection failure" || mode == "missing module") && slices.Contains(c.args, "archive") {
					headers := []tar.Header{{Name: "go_client/session.go", Typeflag: tar.TypeReg}}
					data := []string{"package main\n"}
					if mode == "injection failure" {
						headers = append(headers, tar.Header{Name: "go_client/go.mod", Typeflag: tar.TypeReg})
						data = append(data, "module example.invalid/upstream\n\ngo 1.26.2\n")
					}
					for i := range patches {
						headers = append(headers, tar.Header{Name: fmt.Sprintf("go_client/fix%d.go", i), Typeflag: tar.TypeReg})
						data = append(data, "package main\n// before\n")
					}
					return processResult{stdout: tarFixture(t, headers, data)}, true
				}
				if c.name == "go" && c.args[0] == "mod" && mode == "setup failure" {
					return processResult{exitCode: 1, stderr: []byte("offline dependency cache is incomplete\n")}, true
				}
				if c.name == "go" && c.args[0] == "test" {
					testCount++
					if mode == "result failure" {
						return processResult{exitCode: 1, stdout: []byte("unfiltered diagnostic\n"), stderr: []byte("fixture failure\n")}, true
					}
					if mode == "interruption" {
						cancel()
						return processResult{exitCode: -1, err: ctx.Err()}, true
					}
				}
				return processResult{}, false
			})
			err := f.run(ctx)
			if (err == nil) != (mode == "success") {
				t.Fatalf("%s: %v\n%s", mode, err, f.log)
			}
			if mode == "success" && testCount != len(testRuns()) {
				t.Fatalf("missing selected runs: %d", testCount)
			}
			if mode == "interruption" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if mode == "result failure" && (!strings.Contains(f.log.String(), "unfiltered diagnostic") || !strings.Contains(f.log.String(), "fixture failure")) {
				t.Fatalf("diagnostics lost: %s", f.log)
			}
			f.checkPreserved(t)
		})
	}
}

func TestMissingSource(t *testing.T) {
	for _, source := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		r := newRunner(t.TempDir(), source, &bytes.Buffer{})
		r.exec = func(context.Context, command) processResult {
			t.Fatal("process started without a local source")
			return processResult{}
		}
		if err := r.run(context.Background()); err == nil || !strings.Contains(err.Error(), "local source") {
			t.Fatalf("missing source: %v", err)
		}
	}
}

func tarFixture(t *testing.T, headers []tar.Header, data []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i, header := range headers {
		header.Mode = 0o644
		if header.Typeflag == tar.TypeReg {
			header.Size = int64(len(data[i]))
		}
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(data[i])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestArchiveContainment(t *testing.T) {
	for _, name := range []string{"../sentinel", "/absolute", "go_client/../../sentinel", "go_client/../sentinel", "other/file", `go_client\file`, "go_client/C:/file", "go_client/.git/config", "go_client/.GIT/config"} {
		t.Run(name, func(t *testing.T) {
			data := tarFixture(t, []tar.Header{{Name: name, Typeflag: tar.TypeReg}}, []string{"overwrite"})
			if err := extractArchive(context.Background(), data, t.TempDir()); err == nil {
				t.Fatal("escaping archive accepted")
			}
		})
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeFifo, tar.TypeChar} {
		t.Run(strconv.Itoa(int(kind)), func(t *testing.T) {
			base := t.TempDir()
			sentinel := filepath.Join(base, "sentinel")
			writeFixture(t, sentinel, "preserve\n")
			dir := filepath.Join(base, "input")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			data := tarFixture(t, []tar.Header{{Name: "go_client/link", Linkname: sentinel, Typeflag: kind}}, []string{""})
			if err := extractArchive(context.Background(), data, dir); err == nil {
				t.Fatal("link or special entry accepted")
			}
			if data, err := os.ReadFile(sentinel); err != nil || string(data) != "preserve\n" {
				t.Fatal("caller sentinel changed")
			}
		})
	}
	for _, data := range [][]byte{nil, []byte("not tar"), tarFixture(t, []tar.Header{
		{Name: "go_client/file", Typeflag: tar.TypeReg}, {Name: "go_client/file", Typeflag: tar.TypeReg},
	}, []string{"first", "second"})} {
		if err := extractArchive(context.Background(), data, t.TempDir()); err == nil {
			t.Fatal("empty, invalid or duplicate archive accepted")
		}
	}
	data := tarFixture(t, []tar.Header{{Name: "go_client/file", Typeflag: tar.TypeReg}}, []string{"fixture"})
	if err := extractArchive(context.Background(), data, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestDelayInjection(t *testing.T) {
	for _, data := range []string{"package main\n", fixtureSession + fixtureSession, fixtureSession + delayMarker} {
		name := filepath.Join(t.TempDir(), "session.go")
		writeFixture(t, name, data)
		if err := injectDelay(name); err == nil {
			t.Fatal("invalid injection accepted")
		}
		got, err := os.ReadFile(name)
		if err != nil || string(got) != data {
			t.Fatal("failed injection changed input")
		}
	}
	name := filepath.Join(t.TempDir(), "session.go")
	writeFixture(t, name, fixtureSession)
	if err := injectDelay(name); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(name)
	if err != nil || bytes.Count(data, []byte("time.Sleep(300 * time.Millisecond)")) != 1 || bytes.Count(data, []byte(delayMarker)) != 1 {
		t.Fatalf("wrong delay injection: %s, %v", data, err)
	}
	if err := injectDelay(name); err == nil {
		t.Fatal("repeated injection accepted")
	}
}

func TestOfflineEnvironment(t *testing.T) {
	env := offlineEnv([]string{
		"PATH=fixture", "GOTOOLCHAIN=auto", "GOPROXY=direct", "GOSUMDB=sum.example.invalid", "GOWORK=fixture.work", "GOENV=fixture.env",
		"GOFLAGS=-exec=fixture", "GONOPROXY=*", "GOPRIVATE=*", "GOVCS=*:all", "GOAUTH=fixture", "GOCACHEPROG=fixture",
		"GIT_DIR=fixture", "GIT_INDEX_FILE=fixture", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=protocol.allow", "GIT_CONFIG_VALUE_0=always",
		"GIT_CONFIG_PARAMETERS=fixture", "GIT_SSH_COMMAND=fixture", "GIT_NO_LAZY_FETCH=0", "REPRO_INJECT_DELAY=1",
	})
	checkOffline(t, env)
	values := envMap(env)
	for _, key := range []string{"GIT_DIR", "GIT_INDEX_FILE", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_SSH_COMMAND", "REPRO_INJECT_DELAY"} {
		if _, ok := values[key]; ok {
			t.Errorf("inherited override retained: %s", key)
		}
	}
	if values["PATH"] != "fixture" || values["GIT_NO_LAZY_FETCH"] != "1" || values["GIT_ALLOW_PROTOCOL"] != "" {
		t.Fatal("offline environment is incomplete")
	}
}

func TestFixtureProcess(t *testing.T) {
	if os.Getenv("UPSTREAMREPRO_FIXTURE") != "1" {
		return
	}
	switch os.Getenv("FIXTURE_MODE") {
	case "exit":
		fmt.Fprintln(os.Stdout, "complete stdout")
		fmt.Fprintln(os.Stderr, "complete stderr")
		os.Exit(7)
	case "wait":
		for {
			time.Sleep(time.Hour)
		}
	case "tree":
		child := exec.Command("sh", "-c", `printf 'child ready\n'; cat`)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		input, err := child.StdinPipe()
		if err != nil || child.Start() != nil {
			os.Exit(4)
		}
		defer input.Close()
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(3)
}

func TestExecutor(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := command{name: exe, args: []string{"-test.run=^TestFixtureProcess$"}, dir: t.TempDir(), env: append(offlineEnv(os.Environ()), "UPSTREAMREPRO_FIXTURE=1", "FIXTURE_MODE=exit")}
	result := execute(context.Background(), c)
	if result.err != nil || result.exitCode != 7 || string(result.stdout) != "complete stdout\n" || string(result.stderr) != "complete stderr\n" {
		t.Fatalf("process result lost: %+v", result)
	}
	c.env = append(offlineEnv(os.Environ()), "UPSTREAMREPRO_FIXTURE=1", "FIXTURE_MODE=wait")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	result = execute(ctx, c)
	wantExit := -1
	if runtime.GOOS == "windows" {
		wantExit = 1 // CommandContext uses TerminateProcess on Windows.
	}
	if !errors.Is(result.err, context.DeadlineExceeded) || result.exitCode != wantExit {
		t.Fatalf("timeout not reported: %+v", result)
	}
	result = execute(context.Background(), command{name: filepath.Join(t.TempDir(), "missing"), env: offlineEnv(os.Environ())})
	if result.err == nil || result.exitCode != -1 {
		t.Fatalf("missing executable accepted: %+v", result)
	}
	if runtime.GOOS != "windows" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		c.env = append(offlineEnv(os.Environ()), "UPSTREAMREPRO_FIXTURE=1", "FIXTURE_MODE=tree")
		start := time.Now()
		result := execute(ctx, c)
		if !errors.Is(result.err, context.DeadlineExceeded) || !bytes.Contains(result.stdout, []byte("child ready")) || time.Since(start) >= 2500*time.Millisecond {
			t.Fatalf("child process group not canceled before pipe cleanup: %+v", result)
		}
	}
}

func TestWrapper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash compatibility entry point")
	}
	wrapper, err := os.ReadFile(filepath.Join("..", "..", "doc", "upstream-repro", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	root := filepath.Join(base, "project with spaces")
	entry := filepath.Join(root, "doc", "upstream-repro", "run.sh")
	writeFixture(t, entry, string(wrapper))
	writeFixture(t, filepath.Join(base, "source with spaces", "sentinel"), "preserve\n")
	bin := filepath.Join(base, "bin")
	fakeGo := filepath.Join(bin, "go")
	writeFixture(t, fakeGo, `#!/usr/bin/env bash
set -eu
[[ $GOTOOLCHAIN == local && $GOPROXY == off && $GOSUMDB == off && $GOWORK == off ]]
[[ $GOENV == off && -z $GOFLAGS && $GOVCS == '*:off' && $GOTELEMETRY == off ]]
[[ $GONOPROXY == none && $GOAUTH == off && -z $GOCACHEPROG ]]
[[ $PWD == "$FIXTURE_ROOT" && $# == 5 && $1 == build && $2 == -buildvcs=false && $3 == -o && $5 == ./scripts/upstreamrepro ]]
if [[ ${FIXTURE_BOOTSTRAP_WAIT:-0} == 1 ]]; then
	(
		trap '' TERM
		(
			printf 'fixture bootstrap ready\n'
			sleep 1
			mkdir -p -- "$GOTMPDIR"
			printf 'survived cleanup\n' > "$GOTMPDIR/survived"
			printf 'survived interruption\n' > "$FIXTURE_SURVIVOR"
		) &
		wait
	) &
	wait
	exit 99
fi
cat > "$4" <<'RUNNER'
#!/usr/bin/env bash
set -eu
[[ $# == 1 && $1 == "$FIXTURE_SOURCE" ]]
if [[ ${FIXTURE_WAIT:-0} == 1 ]]; then
	trap 'printf "fixture runner stopped\n"; exit 130' TERM
	printf 'fixture runner ready\n'
	while :; do sleep 1; done
fi
printf 'fixture runner invoked\n'
exit "${FIXTURE_EXIT:-0}"
RUNNER
chmod +x "$4"
`)
	if err := os.Chmod(fakeGo, 0o700); err != nil {
		t.Fatal(err)
	}
	launch := filepath.Join(base, "launch")
	if err := os.Mkdir(launch, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{0, 7} {
		cmd := exec.Command("bash", entry, "source with spaces")
		cmd.Dir = base
		cmd.Env = append(offlineEnv(os.Environ()), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"TMPDIR="+launch, "FIXTURE_ROOT="+root, "FIXTURE_SOURCE="+filepath.Join(base, "source with spaces"), "FIXTURE_EXIT="+strconv.Itoa(code),
			"GOTOOLCHAIN=auto", "GOPROXY=direct", "GOFLAGS=-exec=unexpected", "GOWORK=unexpected.work")
		output, err := cmd.CombinedOutput()
		if (err == nil) != (code == 0) || cmd.ProcessState.ExitCode() != code || !bytes.Contains(output, []byte("fixture runner invoked")) {
			t.Fatalf("wrapper status: %v\n%s", err, output)
		}
		entries, err := os.ReadDir(launch)
		if err != nil || len(entries) != 0 {
			t.Fatalf("wrapper leaked launch directory: %v, %v", entries, err)
		}
	}
	for _, args := range [][]string{nil, {"missing"}, {"source with spaces", "extra"}} {
		cmd := exec.Command("bash", append([]string{entry}, args...)...)
		cmd.Dir = base
		if output, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("invalid wrapper arguments accepted: %v\n%s", args, output)
		}
	}
	for _, stage := range []string{"runner", "bootstrap"} {
		t.Run("interrupt "+stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", entry, "source with spaces")
			cmd.Dir = base
			cmd.WaitDelay = 2 * time.Second
			survivor := filepath.Join(base, "survivor")
			cmd.Env = append(offlineEnv(os.Environ()), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TMPDIR="+launch, "FIXTURE_ROOT="+root, "FIXTURE_SOURCE="+filepath.Join(base, "source with spaces"), "FIXTURE_SURVIVOR="+survivor)
			if stage == "bootstrap" {
				cmd.Env = append(cmd.Env, "FIXTURE_BOOTSTRAP_WAIT=1")
			} else {
				cmd.Env = append(cmd.Env, "FIXTURE_WAIT=1")
			}
			ready := make(chan struct{})
			var output, stderr bytes.Buffer
			cmd.Stdout = &readyWriter{output: &output, ready: ready, marker: "fixture " + stage + " ready\n"}
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-ready:
			case <-ctx.Done():
				cmd.Wait()
				t.Fatalf("wrapper fixture did not start\n%s\n%s", &output, &stderr)
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil || cmd.ProcessState.ExitCode() != 143 || ctx.Err() != nil {
				t.Fatalf("wrapper interruption status: %v\n%s\n%s", err, &output, &stderr)
			}
			if stage == "runner" && !strings.Contains(output.String(), "fixture runner stopped") {
				t.Fatalf("wrapper failed to forward interruption\n%s\n%s", &output, &stderr)
			}
			// All fixture descendants inherit stdout: Wait also waits for their pipes
			// to close, so a late write cannot evade these checks.
			if _, err := os.Stat(survivor); !os.IsNotExist(err) {
				t.Fatalf("bootstrap descendant survived interruption: %v", err)
			}
			if entries, err := os.ReadDir(launch); err != nil || len(entries) != 0 {
				t.Fatalf("wrapper interruption leaked or recreated inputs: %v, %v", entries, err)
			}
		})
	}
}

type readyWriter struct {
	output *bytes.Buffer
	ready  chan struct{}
	marker string
	sent   bool
}

func (w *readyWriter) Write(data []byte) (int, error) {
	n, err := w.output.Write(data)
	if !w.sent && strings.Contains(w.output.String(), w.marker) {
		w.sent = true
		close(w.ready)
	}
	return n, err
}

func TestHandledSignalCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix signal fixture")
	}
	if os.Getenv("UPSTREAMREPRO_SIGNAL_FIXTURE") == "1" {
		f := newFixture(t)
		f.exec = f.executor(t, func(ctx context.Context, c command) (processResult, bool) {
			if c.name == "go" && c.args[0] == "test" {
				process, err := os.FindProcess(os.Getpid())
				if err != nil {
					t.Fatal(err)
				}
				if err := process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				<-ctx.Done()
				return processResult{exitCode: -1, err: ctx.Err()}, true
			}
			return processResult{}, false
		})
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := f.run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("handled signal lost: %v", err)
		}
		f.checkPreserved(t)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestHandledSignalCleanup$")
	cmd.Env = append(offlineEnv(os.Environ()), "UPSTREAMREPRO_SIGNAL_FIXTURE=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("signal fixture: %v\n%s", err, output)
	}
}
