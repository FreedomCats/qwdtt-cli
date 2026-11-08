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
	"TestReproCaptchaManualTimeout":   "    repro_test.go:1100: manual captcha timeout: got 1m0s, want 3m0s\n",
	"TestReproCaptchaSelectedTimeout": "    repro_test.go:1110: selected captcha timeout: got 2m0s, want 3m0s\n",
	"TestReproCaptchaWVLog":           "    repro_test.go:1120: wv diagnostic still claims Android settings\n",
}

type matrixSpec struct {
	name              string
	patches           []string
	tests             []string
	patched, injected bool
}

// This association table is intentionally independent of cases and testRuns.
var expectedCasePatches = map[string]struct {
	patch    patchID
	injected bool
}{
	"TestReproClosedCallIsDead":               {closedCallPatch, false},
	"TestReproGroupPassesBatonOnCredsFailure": {batonPatch, false},
	"TestReproConfigDespiteQuota":             {configQuotaPatch, false},
	"TestReproStopDuringConfigRequest":        {stopPatch, false},
	"TestReproStopRightAfterSessionStart":     {stopPatch, true},
	"TestReproDroppedUp":                      {droppedPatch, false},
	"TestReproDroppedDown":                    {droppedPatch, false},
	"TestReproDroppedStatsOutput":             {droppedPatch, false},
	"TestReproCaptchaManualTimeout":           {captchaTimeouts, false},
	"TestReproCaptchaSelectedTimeout":         {captchaTimeouts, false},
	"TestReproCaptchaAutoTimeout":             {captchaTimeouts, false},
	"TestReproCaptchaWVLog":                   {captchaWVLog, false},
}

var expectedMatrix = []matrixSpec{
	{name: "baseline", tests: []string{
		"TestReproClosedCallIsDead", "TestReproGroupPassesBatonOnCredsFailure", "TestReproConfigDespiteQuota",
		"TestReproStopDuringConfigRequest", "TestReproStopRightAfterSessionStart", "TestReproDroppedUp",
		"TestReproDroppedDown", "TestReproDroppedStatsOutput", "TestReproCaptchaManualTimeout",
		"TestReproCaptchaSelectedTimeout", "TestReproCaptchaAutoTimeout", "TestReproCaptchaWVLog",
	}},
	{name: "baseline-injected", injected: true, tests: []string{"TestReproStopRightAfterSessionStart"}},
	{name: "patched", patched: true, patches: []string{
		"1-closed-call.patch", "2-baton.patch", "3-config-quota.patch", "4-dropped-packets.patch",
		"5-stop.patch", "6-captcha-timeouts.patch", "7-captcha-wv-log.patch",
	}, tests: []string{
		"TestReproClosedCallIsDead", "TestReproGroupPassesBatonOnCredsFailure", "TestReproConfigDespiteQuota",
		"TestReproStopDuringConfigRequest", "TestReproStopRightAfterSessionStart", "TestReproDroppedUp",
		"TestReproDroppedDown", "TestReproDroppedStatsOutput", "TestReproCaptchaManualTimeout",
		"TestReproCaptchaSelectedTimeout", "TestReproCaptchaAutoTimeout", "TestReproCaptchaWVLog",
	}},
	{name: "patched-injected", patched: true, injected: true, patches: []string{
		"1-closed-call.patch", "2-baton.patch", "3-config-quota.patch", "4-dropped-packets.patch",
		"5-stop.patch", "6-captcha-timeouts.patch", "7-captcha-wv-log.patch",
	}, tests: []string{"TestReproStopRightAfterSessionStart"}},
	{name: "patched-closed-call", patched: true, patches: []string{"1-closed-call.patch"}, tests: []string{"TestReproClosedCallIsDead"}},
	{name: "patched-baton", patched: true, patches: []string{"2-baton.patch"}, tests: []string{"TestReproGroupPassesBatonOnCredsFailure"}},
	{name: "patched-config-quota", patched: true, patches: []string{"3-config-quota.patch"}, tests: []string{"TestReproConfigDespiteQuota"}},
	{name: "patched-counters", patched: true, patches: []string{"4-dropped-packets.patch"}, tests: []string{
		"TestReproDroppedUp", "TestReproDroppedDown", "TestReproDroppedStatsOutput",
	}},
	{name: "patched-stop", patched: true, patches: []string{"5-stop.patch"}, tests: []string{"TestReproStopDuringConfigRequest"}},
	{name: "patched-stop-injected", patched: true, injected: true, patches: []string{"5-stop.patch"}, tests: []string{"TestReproStopRightAfterSessionStart"}},
	{name: "patched-captcha-timeouts", patched: true, patches: []string{"6-captcha-timeouts.patch"}, tests: []string{
		"TestReproCaptchaManualTimeout", "TestReproCaptchaSelectedTimeout", "TestReproCaptchaAutoTimeout",
	}},
	{name: "patched-captcha-wv-log", patched: true, patches: []string{"7-captcha-wv-log.patch"}, tests: []string{"TestReproCaptchaWVLog"}},
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

func recomputeResult(events []testEvent) processResult {
	updated := slices.Clone(events)
	packageAction := "pass"
	for _, event := range updated {
		if event.Test != "" && event.Action == "fail" {
			packageAction = "fail"
		}
	}
	for i := range updated {
		if updated[i].Test == "" && updated[i].Package != "" && (updated[i].Action == "pass" || updated[i].Action == "fail") {
			updated[i].Action = packageAction
		}
	}
	return jsonResult(updated)
}

func TestExpectationMatrix(t *testing.T) {
	runs := testRuns()
	byName := make(map[string]testRun, len(runs))
	for _, run := range runs {
		if _, exists := byName[run.name]; exists {
			t.Fatalf("duplicate input name: %s", run.name)
		}
		byName[run.name] = run
	}
	if len(byName) != len(expectedMatrix) {
		t.Fatalf("unexpected input inventory: %v", sortedRunNames(byName))
	}
	for _, spec := range expectedMatrix {
		run, exists := byName[spec.name]
		if !exists {
			t.Fatalf("missing input: %s", spec.name)
		}
		if run.patched != spec.patched || run.injected != spec.injected || !slices.Equal(run.patches, spec.patches) {
			t.Fatalf("wrong input configuration for %s: patches=%v patched=%t injected=%t", spec.name, run.patches, run.patched, run.injected)
		}
		if len(run.want) == 0 {
			t.Fatalf("empty selection for %s", spec.name)
		}
		gotTests := make([]string, 0, len(run.want))
		seen := make(map[string]bool, len(run.want))
		for _, want := range run.want {
			if want.name == "" || seen[want.name] {
				t.Fatalf("empty or duplicate test association in %s: %q", spec.name, want.name)
			}
			seen[want.name] = true
			gotTests = append(gotTests, want.name)
			if want.action != expectedAction(spec.patched, spec.injected, want.name) {
				t.Fatalf("wrong expected action for %s/%s: %q", spec.name, want.name, want.action)
			}
			if !spec.injected && want.name == "TestReproStopRightAfterSessionStart" &&
				(spec.name == "baseline" || spec.name == "patched") &&
				(len(want.evidence) != 1 || want.evidence[0].String() != diagnostic(`нужна вставленная задержка: см\. run\.sh`).String()) {
				t.Fatalf("ordinary deadline skip reason changed for %s", spec.name)
			}
		}
		if !slices.Equal(gotTests, spec.tests) {
			t.Fatalf("wrong test membership for %s: got %v, want %v", spec.name, gotTests, spec.tests)
		}
	}
	if err := assertCasePatchInventory(runs); err != nil {
		t.Fatal(err)
	}
	for _, spec := range expectedMatrix {
		run := byName[spec.name]
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
		})
	}
}

func sortedRunNames(runs map[string]testRun) []string {
	names := make([]string, 0, len(runs))
	for name := range runs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func expectedAction(patched, injected bool, testName string) string {
	if patched {
		if !injected && testName == "TestReproStopRightAfterSessionStart" {
			return "skip"
		}
		return "pass"
	}
	if injected {
		return "fail"
	}
	if testName == "TestReproStopRightAfterSessionStart" {
		return "skip"
	}
	if testName == "TestReproCaptchaAutoTimeout" {
		return "pass"
	}
	return "fail"
}

func assertCasePatchInventory(runs []testRun) error {
	seen := make(map[string]bool, len(cases))
	for _, c := range cases {
		name := c.baseline.name
		association, ok := expectedCasePatches[name]
		if !ok || association.patch != c.patch || association.injected != c.injected {
			return fmt.Errorf("unexpected patch association for %s: patch=%q injected=%t", name, c.patch, c.injected)
		}
		if seen[name] {
			return fmt.Errorf("duplicate case association: %s", name)
		}
		seen[name] = true
		if !slices.Contains(patches, string(c.patch)) {
			return fmt.Errorf("case %s references an undeclared patch %q", name, c.patch)
		}
	}
	if len(seen) != len(expectedCasePatches) {
		return fmt.Errorf("case inventory differs: got %v", sortedCaseNames(seen))
	}
	declared := make(map[string]bool, len(patches))
	for _, name := range patches {
		if declared[name] {
			return fmt.Errorf("duplicate declared patch: %s", name)
		}
		declared[name] = true
		associated := false
		for _, association := range expectedCasePatches {
			if string(association.patch) == name {
				associated = true
				break
			}
		}
		if !associated {
			return fmt.Errorf("declared patch lacks expected test coverage: %s", name)
		}
	}
	for _, association := range expectedCasePatches {
		if !declared[string(association.patch)] {
			return fmt.Errorf("expected association references undeclared patch: %s", association.patch)
		}
	}
	for _, spec := range expectedMatrix {
		want := make([]string, 0, len(expectedCasePatches))
		for name, association := range expectedCasePatches {
			include := true
			if spec.patched && len(spec.patches) == 1 {
				include = string(association.patch) == spec.patches[0] && association.injected == spec.injected
			} else if spec.injected {
				include = association.injected
			}
			if include {
				want = append(want, name)
			}
		}
		got := slices.Clone(spec.tests)
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			return fmt.Errorf("matrix selection %s differs from independent patch associations: got %v, want %v", spec.name, got, want)
		}
	}
	for _, c := range cases {
		if c.injected && c.patch != stopPatch {
			return fmt.Errorf("unexpected injected case: %s", c.baseline.name)
		}
		var individual *matrixSpec
		for i := range expectedMatrix {
			spec := &expectedMatrix[i]
			if spec.patched && spec.injected == c.injected && slices.Equal(spec.patches, []string{string(c.patch)}) {
				individual = spec
				break
			}
		}
		if individual == nil || !slices.Contains(individual.tests, c.baseline.name) {
			return fmt.Errorf("case %s missing from independent single-patch matrix", c.baseline.name)
		}
		run := findRun(runs, individual.name)
		if run == nil || !slices.ContainsFunc(run.want, func(w expectation) bool { return w.name == c.baseline.name && w.action == "pass" }) {
			return fmt.Errorf("case %s missing from individual input %s", c.baseline.name, individual.name)
		}
	}
	return nil
}

func findRun(runs []testRun, name string) *testRun {
	for i := range runs {
		if runs[i].name == name {
			return &runs[i]
		}
	}
	return nil
}

func sortedCaseNames(cases map[string]bool) []string {
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func fixtureRun(t *testing.T, name string) testRun {
	t.Helper()
	runs := testRuns()
	i := slices.IndexFunc(runs, func(run testRun) bool { return run.name == name })
	if i < 0 {
		t.Fatalf("missing test input: %s", name)
	}
	return runs[i]
}

func TestCounterResultValidation(t *testing.T) {
	baseline, patched := fixtureRun(t, "baseline"), fixtureRun(t, "patched-counters")
	baseline.want = slices.DeleteFunc(slices.Clone(baseline.want), func(w expectation) bool {
		return !regexp.MustCompile(patched.pattern()).MatchString(w.name)
	})
	for _, run := range []testRun{baseline, patched} {
		for _, w := range run.want {
			for _, mode := range []string{"wrong assertion", "missing assertion", "unexpected pass", "missing", "skip", "setup", "build", "timeout"} {
				if (mode == "missing assertion" || mode == "unexpected pass") && w.action != "fail" {
					continue
				}
				t.Run(run.name+"/"+w.name+"/"+mode, func(t *testing.T) {
					events := fixtureEvents(run)
					switch mode {
					case "missing":
						events = slices.DeleteFunc(events, func(e testEvent) bool { return e.Test == w.name })
					case "missing assertion":
						events = mutateTestOutput(events, w.name, func(string) string { return "" })
					case "unexpected pass":
						events = mutateTestAction(events, w.name, "pass")
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
					result := recomputeResult(events)
					if mode == "timeout" {
						result.err = context.DeadlineExceeded
					} else if mode == "build" {
						result.stdout = []byte("FAIL example.invalid/upstream [build failed]\n")
					}
					err := validateResult(result, run.want)
					if err == nil {
						t.Fatal("invalid counter evidence accepted")
					}
					if mode == "unexpected pass" && !strings.Contains(err.Error(), w.name+` result "pass", want "fail"`) {
						t.Fatalf("unexpected counter pass was rejected for the wrong reason: %v", err)
					}
				})
			}
		}
	}
}

func TestCaptchaResultValidation(t *testing.T) {
	baseline := fixtureRun(t, "baseline")
	baseline.want = slices.DeleteFunc(slices.Clone(baseline.want), func(w expectation) bool {
		return !strings.HasPrefix(w.name, "TestReproCaptcha")
	})
	for _, run := range []testRun{baseline, fixtureRun(t, "patched-captcha-timeouts"), fixtureRun(t, "patched-captcha-wv-log")} {
		for _, w := range run.want {
			for _, mode := range []string{
				"wrong assertion", "missing assertion", "unexpected pass", "failure", "missing test", "skip", "build", "process timeout",
				"capture setup", "call-site setup", "profile setup", "pipe setup", "parse setup", "teardown", "setup plus assertion",
			} {
				if (mode == "unexpected pass" || mode == "wrong assertion" || mode == "missing assertion" || mode == "setup plus assertion") && w.action != "fail" {
					continue
				}
				if mode == "failure" && w.action != "pass" {
					continue
				}
				t.Run(run.name+"/"+w.name+"/"+mode, func(t *testing.T) {
					events := fixtureEvents(run)
					if mode == "missing test" {
						events = slices.DeleteFunc(events, func(e testEvent) bool { return e.Test == w.name })
					} else {
						for i := range events {
							e := &events[i]
							if e.Test != w.name {
								continue
							}
							if e.Action == "output" {
								switch mode {
								case "wrong assertion":
									e.Output = "    repro_test.go:1100: selected captcha timeout: got 1m0s, want 3m0s\n"
								case "missing assertion":
									e.Output = ""
								}
							}
							if e.Action == "pass" || e.Action == "fail" {
								switch mode {
								case "unexpected pass":
									e.Action = "pass"
								case "skip":
									e.Action = "skip"
								default:
									e.Action = "fail"
								}
							}
						}
						setup := map[string]string{
							"capture setup": "captcha stdout capture: read failed", "call-site setup": "captcha call-site contract: missing captcha request",
							"profile setup": "captcha profile-write setup: rotation failed", "pipe setup": "captcha stdout pipe setup: pipe failed",
							"parse setup": "captcha source parse: invalid source", "teardown": "captcha stdout reader teardown timed out",
							"setup plus assertion": "captcha response: wrong synthetic token",
						}[mode]
						if setup != "" {
							terminal := slices.IndexFunc(events, func(e testEvent) bool { return e.Test == w.name && e.Action == "fail" })
							events = slices.Insert(events, terminal, testEvent{Action: "output", Package: events[0].Package, Test: w.name,
								Output: "    repro_test.go:1050: " + setup + "\n"})
						}
					}
					result := recomputeResult(events)
					if mode == "process timeout" {
						result.err = context.DeadlineExceeded
					} else if mode == "build" {
						result.stdout = []byte("FAIL example.invalid/upstream [build failed]\n")
					}
					if err := validateResult(result, run.want); err == nil {
						t.Fatal("invalid captcha evidence accepted")
					}
				})
			}
		}
	}
}

func TestRejectResults(t *testing.T) {
	baseline, patched := fixtureRun(t, "baseline"), fixtureRun(t, "patched")
	tests := []struct {
		name   string
		run    testRun
		change func(*processResult)
	}{
		{"missing JSON", baseline, func(r *processResult) { r.stdout = nil }},
		{"malformed JSON", baseline, func(r *processResult) { r.stdout = append(r.stdout, []byte("not JSON\n")...) }},
		{"truncated JSON", baseline, func(r *processResult) { r.stdout = []byte(`{"Action":`) }},
		{"null JSON", baseline, func(r *processResult) { r.stdout = []byte("null\n") }},
		{"build failure", baseline, func(r *processResult) {
			r.stdout = []byte("# example.invalid/upstream\n./source.go:1: undefined: missing\n")
			r.stderr = []byte("FAIL example.invalid/upstream [build failed]\n")
		}},
		{"start failure", baseline, func(r *processResult) { r.err = exec.ErrNotFound; r.exitCode = -1 }},
		{"signal", baseline, func(r *processResult) { r.err = errors.New("signal: terminated"); r.exitCode = -1 }},
		{"timeout", baseline, func(r *processResult) { r.err = context.DeadlineExceeded }},
		{"canceled", baseline, func(r *processResult) { r.err = context.Canceled }},
		{"wrong process status", baseline, func(r *processResult) { r.exitCode = 2 }},
		{"baseline success", baseline, func(r *processResult) { *r = jsonResult(fixtureEvents(patched)) }},
		{"patched process failure", patched, func(r *processResult) { r.exitCode = 1 }},
		{"panic on stderr", baseline, func(r *processResult) { r.stderr = []byte("panic: fixture\n") }},
		{"bind failure on stderr", baseline, func(r *processResult) { r.stderr = []byte("listen udp4 127.0.0.1:0: bind: operation not permitted\n") }},
		{"setup failure on stderr", baseline, func(r *processResult) { r.stderr = []byte("FAIL example.invalid/upstream [setup failed]\n") }},
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
	t.Run("ordinary handshake logs", func(t *testing.T) {
		events := fixtureEvents(baseline)
		events = slices.Insert(events, 1, testEvent{Action: "output", Package: "example.invalid/upstream",
			Output: "INFO DTLS handshake: completed; setup: ready\n"})
		if err := validateResult(recomputeResult(events), baseline.want); err != nil {
			t.Fatalf("ordinary upstream log was rejected: %v", err)
		}
	})

	eventTests := []struct {
		name   string
		run    testRun
		change func([]testEvent) []testEvent
	}{
		{"missing test", baseline, func(events []testEvent) []testEvent {
			return slices.DeleteFunc(events, func(e testEvent) bool { return e.Test == baseline.want[0].name })
		}},
		{"missing test completion", baseline, func(events []testEvent) []testEvent { return slices.Delete(events, 3, 4) }},
		{"missing package start", baseline, func(events []testEvent) []testEvent { return events[1:] }},
		{"missing package result", baseline, func(events []testEvent) []testEvent { return events[:len(events)-1] }},
		{"missing assertion", baseline, func(events []testEvent) []testEvent {
			return mutateTestOutput(events, baseline.want[0].name, func(string) string { return "" })
		}},
		{"wrong baseline assertion", baseline, func(events []testEvent) []testEvent {
			return mutateTestOutput(events, baseline.want[0].name, func(string) string {
				return "    repro_test.go:115: listen udp4 127.0.0.1:0: bind: operation not permitted\n"
			})
		}},
		{"loopback setup failure after assertion", baseline, func(events []testEvent) []testEvent {
			return mutateTestOutput(events, baseline.want[0].name, func(output string) string {
				return output + "    repro_test.go:115: listen udp4 127.0.0.1:0: bind: operation not permitted\n"
			})
		}},
		{"handshake setup failure after quota assertion", baseline, func(events []testEvent) []testEvent {
			return mutateTestOutput(events, "TestReproConfigDespiteQuota", func(output string) string {
				return output + "    repro_test.go:330: quota stand handshake: certificate rejected\n"
			})
		}},
		{"quota stand never rejected", baseline, func(events []testEvent) []testEvent {
			for i := range events {
				events[i].Output = strings.ReplaceAll(events[i].Output, "486 отдано 1 раз", "486 отдано 0 раз")
			}
			return events
		}},
		{"panic", baseline, func(events []testEvent) []testEvent {
			return mutateTestOutput(events, baseline.want[0].name, func(output string) string { return output + "panic: fixture\n" })
		}},
		{"test timeout", baseline, func(events []testEvent) []testEvent {
			return mutateTestOutput(events, baseline.want[0].name, func(output string) string { return output + "panic: test timed out after 10m0s\n" })
		}},
		{"unattributed bind failure", baseline, func(events []testEvent) []testEvent {
			return slices.Insert(events, 1, testEvent{Action: "output", Package: events[0].Package, Output: "listen udp4 127.0.0.1:0: socket: operation not permitted\n"})
		}},
		{"split panic", baseline, func(events []testEvent) []testEvent {
			name := baseline.want[0].name
			events = mutateTestOutput(events, name, func(output string) string { return output + "pa" })
			i := slices.IndexFunc(events, func(e testEvent) bool { return e.Test == name && e.Action == "output" })
			return slices.Insert(events, i+1, testEvent{Action: "output", Package: events[0].Package, Test: name, Output: "nic: fixture\n"})
		}},
		{"extra package failure", baseline, func(events []testEvent) []testEvent {
			return append(events, testEvent{Action: "fail", Package: "example.invalid/other"})
		}},
		{"unexpected test", baseline, func(events []testEvent) []testEvent { events[1].Test = "TestUnrelated"; return events }},
		{"duplicate test", baseline, func(events []testEvent) []testEvent { return slices.Insert(events, 4, events[1:4]...) }},
		{"unexpected skip", baseline, func(events []testEvent) []testEvent { events[3].Action = "skip"; return events }},
		{"unexpected baseline pass", baseline, func(events []testEvent) []testEvent { events[3].Action = "pass"; return events }},
		{"injected baseline skipped", fixtureRun(t, "baseline-injected"), func(events []testEvent) []testEvent {
			return mutateTestAction(events, "TestReproStopRightAfterSessionStart", "skip")
		}},
		{"injected baseline unexpectedly passes", fixtureRun(t, "baseline-injected"), func(events []testEvent) []testEvent {
			return mutateTestAction(events, "TestReproStopRightAfterSessionStart", "pass")
		}},
		{"missing expected deadline skip", baseline, func(events []testEvent) []testEvent {
			return slices.DeleteFunc(events, func(e testEvent) bool { return e.Test == "TestReproStopRightAfterSessionStart" })
		}},
		{"wrong deadline skip reason", baseline, func(events []testEvent) []testEvent {
			return mutateTestOutput(events, "TestReproStopRightAfterSessionStart", func(string) string { return "    repro_test.go:359: another skip reason\n" })
		}},
		{"missing deadline skip reason", baseline, func(events []testEvent) []testEvent {
			return mutateTestOutput(events, "TestReproStopRightAfterSessionStart", func(string) string { return "" })
		}},
		{"baseline deadline unexpectedly passes", baseline, func(events []testEvent) []testEvent {
			return mutateTestAction(events, "TestReproStopRightAfterSessionStart", "pass")
		}},
		{"baseline deadline unexpectedly fails", baseline, func(events []testEvent) []testEvent {
			return mutateTestAction(events, "TestReproStopRightAfterSessionStart", "fail")
		}},
		{"patched deadline skip missing", fixtureRun(t, "patched"), func(events []testEvent) []testEvent {
			return slices.DeleteFunc(events, func(e testEvent) bool { return e.Test == "TestReproStopRightAfterSessionStart" })
		}},
		{"patched deadline skip wrong reason", fixtureRun(t, "patched"), func(events []testEvent) []testEvent {
			return mutateTestOutput(events, "TestReproStopRightAfterSessionStart", func(string) string { return "    repro_test.go:359: another skip reason\n" })
		}},
		{"patched deadline skip missing reason", fixtureRun(t, "patched"), func(events []testEvent) []testEvent {
			return mutateTestOutput(events, "TestReproStopRightAfterSessionStart", func(string) string { return "" })
		}},
		{"patched deadline unexpectedly passes", fixtureRun(t, "patched"), func(events []testEvent) []testEvent {
			return mutateTestAction(events, "TestReproStopRightAfterSessionStart", "pass")
		}},
		{"injected deadline unexpectedly skips", fixtureRun(t, "baseline-injected"), func(events []testEvent) []testEvent {
			return mutateTestAction(events, "TestReproStopRightAfterSessionStart", "skip")
		}},
		{"patched injected deadline unexpectedly skips", fixtureRun(t, "patched-injected"), func(events []testEvent) []testEvent {
			return mutateTestAction(events, "TestReproStopRightAfterSessionStart", "skip")
		}},
		{"patched full deadline unexpectedly fails", fixtureRun(t, "patched"), func(events []testEvent) []testEvent {
			return mutateTestAction(events, "TestReproStopRightAfterSessionStart", "fail")
		}},
		{"unexpected skip on passing control", baseline, func(events []testEvent) []testEvent {
			return mutateTestAction(events, "TestReproCaptchaAutoTimeout", "skip")
		}},
		{"patched failure", patched, func(events []testEvent) []testEvent {
			return mutateTestAction(events, "TestReproClosedCallIsDead", "fail")
		}},
		{"patched injected failure", fixtureRun(t, "patched-injected"), func(events []testEvent) []testEvent {
			return mutateTestAction(events, "TestReproStopRightAfterSessionStart", "fail")
		}},
		{"package skip", baseline, func(events []testEvent) []testEvent { events[len(events)-1].Action = "skip"; return events }},
		{"mismatched package result", baseline, func(events []testEvent) []testEvent { events[len(events)-1].Action = "pass"; return events }},
		{"build JSON", baseline, func(events []testEvent) []testEvent {
			return []testEvent{{Action: "build-output", Package: "example.invalid/upstream", Output: "undefined: missing"}, {Action: "build-fail", Package: "example.invalid/upstream"}}
		}},
	}
	for _, tc := range eventTests {
		t.Run(tc.name, func(t *testing.T) {
			events := tc.change(fixtureEvents(tc.run))
			var result processResult
			if tc.name == "mismatched package result" {
				result = jsonResult(events)
			} else {
				result = recomputeResult(events)
			}
			err := validateResult(result, tc.run.want)
			if err == nil {
				t.Fatal("invalid event stream accepted")
			}
			if tc.name == "handshake setup failure after quota assertion" &&
				!strings.Contains(err.Error(), "build/setup or loopback bind failure") {
				t.Fatalf("handshake setup diagnostic was rejected for the wrong reason: %v", err)
			}
		})
	}
	for _, run := range testRuns() {
		for _, mode := range []string{"extra test", "build failure", "panic", "process timeout"} {
			t.Run(run.name+"/"+mode, func(t *testing.T) {
				events := fixtureEvents(run)
				if mode == "extra test" {
					events = slices.Insert(events, len(events)-1,
						testEvent{Action: "run", Package: events[0].Package, Test: "TestReproUnselected"},
						testEvent{Action: "pass", Package: events[0].Package, Test: "TestReproUnselected"})
				}
				result := recomputeResult(events)
				switch mode {
				case "build failure":
					result.stderr = []byte("FAIL example.invalid/upstream [build failed]\n")
				case "panic":
					result.stderr = []byte("panic: fixture\n")
				case "process timeout":
					result.err = context.DeadlineExceeded
				}
				if err := validateResult(result, run.want); err == nil {
					t.Fatal("invalid input evidence accepted")
				}
			})
		}
		for _, w := range run.want {
			for _, mode := range []string{"missing test", "unexpected skip"} {
				if mode == "unexpected skip" && w.action == "skip" {
					continue
				}
				t.Run(run.name+"/"+w.name+"/"+mode, func(t *testing.T) {
					events := fixtureEvents(run)
					if mode == "missing test" {
						events = slices.DeleteFunc(events, func(e testEvent) bool { return e.Test == w.name })
					} else {
						events = mutateTestAction(events, w.name, "skip")
					}
					if err := validateResult(recomputeResult(events), run.want); err == nil {
						t.Fatal("invalid selected test evidence accepted")
					}
				})
			}
		}
	}

	retained := []struct {
		name, baselineInput, patchedInput string
	}{
		{"closed call", "baseline", "patched"},
		{"baton", "baseline", "patched"},
		{"config quota", "baseline", "patched"},
		{"stop during config", "baseline", "patched"},
		{"stop after session start", "baseline-injected", "patched-injected"},
	}
	retainedTestNames := []string{
		"TestReproClosedCallIsDead",
		"TestReproGroupPassesBatonOnCredsFailure",
		"TestReproConfigDespiteQuota",
		"TestReproStopDuringConfigRequest",
		"TestReproStopRightAfterSessionStart",
	}
	for i, tc := range retained {
		baselineRun, patchedRun := fixtureRun(t, tc.baselineInput), fixtureRun(t, tc.patchedInput)
		name := retainedTestNames[i]
		caseIndex := slices.IndexFunc(cases, func(c reproCase) bool { return c.baseline.name == name })
		if caseIndex < 0 {
			t.Fatalf("missing retained case: %s", name)
		}
		for evidenceIndex, evidence := range cases[caseIndex].baseline.evidence {
			for _, mode := range []string{"wrong assertion", "missing assertion"} {
				t.Run(fmt.Sprintf("%s/assertion-%d/%s", tc.name, evidenceIndex+1, mode), func(t *testing.T) {
					events := fixtureEvents(baselineRun)
					replacement := "    repro_test.go:999: unrelated diagnostic\n"
					if mode == "missing assertion" {
						replacement = ""
					}
					events = mutateTestOutput(events, name, func(output string) string {
						return evidence.ReplaceAllString(output, replacement)
					})
					if err := validateResult(recomputeResult(events), baselineRun.want); err == nil {
						t.Fatal("invalid retained baseline assertion accepted")
					}
				})
			}
		}
		for _, mode := range []string{"unexpected baseline pass", "missing baseline test"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				events := fixtureEvents(baselineRun)
				if mode == "unexpected baseline pass" {
					events = mutateTestAction(events, name, "pass")
				} else {
					events = slices.DeleteFunc(events, func(e testEvent) bool { return e.Test == name })
				}
				if err := validateResult(recomputeResult(events), baselineRun.want); err == nil {
					t.Fatal("invalid retained baseline evidence accepted")
				}
			})
		}
		for _, mode := range []string{"patched failure", "missing patched test"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				events := fixtureEvents(patchedRun)
				if mode == "patched failure" {
					events = mutateTestAction(events, name, "fail")
				} else {
					events = slices.DeleteFunc(events, func(e testEvent) bool { return e.Test == name })
				}
				if err := validateResult(recomputeResult(events), patchedRun.want); err == nil {
					t.Fatal("invalid retained patched evidence accepted")
				}
			})
		}
	}
}

func mutateTestOutput(events []testEvent, name string, change func(string) string) []testEvent {
	for i := range events {
		if events[i].Test == name && events[i].Action == "output" {
			events[i].Output = change(events[i].Output)
			return events
		}
	}
	return events
}

func mutateTestAction(events []testEvent, name, action string) []testEvent {
	for i := range events {
		if events[i].Test == name && (events[i].Action == "pass" || events[i].Action == "fail" || events[i].Action == "skip") {
			events[i].Action = action
			return events
		}
	}
	return events
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
			f.checkGitDelegation(t, c)
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

func (f fixture) checkGitDelegation(t *testing.T, c command) {
	t.Helper()
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
			t.Cleanup(func() { f.checkPreserved(t) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var patchLink patchLinkObservation
			if mode == "missing pin" {
				f.pin = strings.Repeat("0", 40)
			}
			if mode == "patch failure" {
				writeFixture(t, filepath.Join(f.root, "doc", "upstream-repro", patches[0]), "not a patch\n")
			}
			if mode == "patch link" {
				target := filepath.Join(f.source, "untracked sentinel.txt")
				writeFixture(t, filepath.Join(f.root, "doc", "upstream-repro", string(closedCallPatch)),
					"diff --git a/go_client/escape b/go_client/escape\nnew file mode 120000\n--- /dev/null\n+++ b/go_client/escape\n@@ -0,0 +1 @@\n+"+
						target+"\n\\ No newline at end of file\n")
				assets := snapshotFixtureAssets(t, f)
				t.Cleanup(func() { assertFixtureAssets(t, f, assets) })
				patchLink.target = target
			}
			testCount := 0
			intercept := func(ctx context.Context, c command) (processResult, bool) {
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
			}
			if mode == "patch link" {
				f.exec = f.executor(t, patchLinkStrictExecutor(t, f, &patchLink))
			} else {
				f.exec = f.executor(t, intercept)
			}
			var err error
			if mode == "patch link" {
				run := fixtureRun(t, "patched-closed-call")
				err = f.runInputs(ctx, []testRun{run})
				assertStrictPatchLinkOutcome(t, patchLink, err)
			} else {
				err = f.run(ctx)
			}
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
