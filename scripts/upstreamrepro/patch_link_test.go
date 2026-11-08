package main

// Patch-link mode coverage keeps Git's link materialization explicit.

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

type patchLinkObservation struct {
	target       string
	checks       []processResult
	applications []processResult
	entries      []patchLinkEntry
	commands     [][]string
	fallbackStop bool
	goMod        int
	goTest       int
}

type patchLinkEntry struct {
	exists  bool
	missing bool
	mode    fs.FileMode
	data    []byte
	target  string
	errText string
}

type fixtureAsset struct {
	data []byte
	mode fs.FileMode
}

func TestPatchLinkModes(t *testing.T) {
	t.Run("fallback", func(t *testing.T) {
		f, run, target := preparePatchLinkFixture(t)
		observation := patchLinkObservation{target: target}
		f.exec = f.executor(t, patchLinkInterceptor(t, f, &observation, false))
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			_ = f.runInputs(context.Background(), []testRun{run})
		}()
		<-finished
		if !observation.fallbackStop {
			t.Fatal("fallback fixture did not stop at the apply/filesystem boundary")
		}
		if len(observation.checks) != 1 || processError(observation.checks[0]) != nil {
			t.Fatalf("explicit core.symlinks=false check did not succeed: %+v", observation.checks)
		}
		if len(observation.applications) != 1 || processError(observation.applications[0]) != nil {
			t.Fatalf("explicit core.symlinks=false apply did not succeed: %+v", observation.applications)
		}
		if len(observation.commands) != 2 {
			t.Fatalf("explicit core.symlinks=false commands: %d", len(observation.commands))
		}
		for _, args := range observation.commands {
			if !configBeforeSubcommand(args, "core.symlinks=false") {
				t.Fatalf("fallback command lacks explicit false mode: %v", args)
			}
		}
		if len(observation.entries) != 1 {
			t.Fatalf("explicit core.symlinks=false apply did not produce an entry observation: %+v", observation.entries)
		}
		entry := observation.entries[0]
		if !entry.exists || !entry.mode.IsRegular() || entry.mode&fs.ModeSymlink != 0 || entry.target != "" {
			t.Fatalf("explicit false did not materialize an ordinary file: %+v", observation.entries)
		}
		if !bytes.Equal(entry.data, []byte(target)) {
			t.Fatalf("wrong fallback target text: %q", entry.data)
		}
		if observation.goMod != 0 || observation.goTest != 0 {
			t.Fatalf("fallback reached unexpected Go commands after apply inspection: mod=%d test=%d", observation.goMod, observation.goTest)
		}
		t.Logf("fallback materialized an ordinary target-text file before the unchanged fake Go patch-state assertion could run: mode=%v bytes=%q; Go mod=%d test=%d", entry.mode, entry.data, observation.goMod, observation.goTest)
	})

	t.Run("strict", func(t *testing.T) {
		f, run, target := preparePatchLinkFixture(t)
		observation := patchLinkObservation{target: target}
		f.exec = f.executor(t, patchLinkStrictExecutor(t, f, &observation))
		err := f.runInputs(context.Background(), []testRun{run})
		assertStrictPatchLinkOutcome(t, observation, err)
	})
}

func TestPatchLinkRefusalDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result processResult
		want   bool
	}{
		{"unix permission refusal", processResult{exitCode: 1, stderr: []byte("error: unable to create symlink go_client/escape: Operation not permitted\n")}, true},
		{"windows privilege refusal", processResult{exitCode: 1, stderr: []byte("error: unable to create symlink go_client/escape: A required privilege is not held by the client\n")}, true},
		{"unrelated apply failure", processResult{exitCode: 1, stderr: []byte("error: patch failed: go_client/escape\nfatal: operation not supported\n")}, false},
		{"unrelated diagnostic after symlink text", processResult{exitCode: 1, stderr: []byte("error: unable to create symlink go_client/escape\nfatal: operation not supported\n")}, false},
		{"process start failure", processResult{err: exec.ErrNotFound, exitCode: -1, stderr: []byte("unable to create symlink: permission denied")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNativeLinkRefusal(tc.result); got != tc.want {
				t.Fatalf("isNativeLinkRefusal() = %t, want %t", got, tc.want)
			}
		})
	}
}

func preparePatchLinkFixture(t *testing.T) (fixture, testRun, string) {
	t.Helper()
	f := newFixture(t)
	target := filepath.Join(f.source, "untracked sentinel.txt")
	writeFixture(t, filepath.Join(f.root, "doc", "upstream-repro", string(closedCallPatch)),
		"diff --git a/go_client/escape b/go_client/escape\nnew file mode 120000\n--- /dev/null\n+++ b/go_client/escape\n@@ -0,0 +1 @@\n+"+
			target+"\n\\ No newline at end of file\n")
	assets := snapshotFixtureAssets(t, f)
	t.Cleanup(func() {
		assertFixtureAssets(t, f, assets)
		f.checkPreserved(t)
	})
	return f, fixtureRun(t, "patched-closed-call"), target
}

func patchLinkInterceptor(t *testing.T, f fixture, observation *patchLinkObservation, strict bool) func(context.Context, command) (processResult, bool) {
	t.Helper()
	return func(ctx context.Context, c command) (processResult, bool) {
		if isPatchLinkCommand(c) {
			f.checkGitDelegation(t, c)
			configured := c
			if strict {
				configured = insertGitConfig(configured, "core.symlinks=true")
			} else {
				configured = insertGitConfig(configured, "core.symlinks=false")
			}
			result := execute(ctx, configured)
			observation.commands = append(observation.commands, slices.Clone(configured.args))
			if slices.Contains(c.args, "--check") {
				observation.checks = append(observation.checks, result)
				logPatchLinkResult(t, map[bool]string{false: "fallback", true: "strict"}[strict], true, result, patchLinkEntry{})
				return result, true
			}
			entry := observePatchLinkEntry(filepath.Join(c.dir, "go_client", "escape"))
			observation.applications = append(observation.applications, result)
			observation.entries = append(observation.entries, entry)
			logPatchLinkResult(t, map[bool]string{false: "fallback", true: "strict"}[strict], false, result, entry)
			if strict {
				return result, true
			}
			if err := processError(result); err != nil {
				t.Fatalf("explicit core.symlinks=false apply failed: %v\n%s", err, processDiagnostic(result))
			}
			if !entry.exists || !entry.mode.IsRegular() || entry.mode&fs.ModeSymlink != 0 || !bytes.Equal(entry.data, []byte(observation.target)) {
				t.Fatalf("fallback did not produce the expected ordinary target-text file: %+v", entry)
			}
			if data, err := os.ReadFile(filepath.Join(f.source, "untracked sentinel.txt")); err != nil || string(data) != "preserve this\n" {
				t.Fatalf("synthetic source sentinel changed: %q, %v", data, err)
			}
			if data, err := os.ReadFile(filepath.Join(c.dir, "go_client", "fix0.go")); err != nil || string(data) != "package main\n// before\n" {
				t.Fatalf("fix0.go changed before fake Go assertion: %q, %v", data, err)
			}
			observation.fallbackStop = true
			runtime.Goexit()
		}
		if !strict && c.name == "go" && len(c.args) > 0 {
			switch c.args[0] {
			case "mod":
				observation.goMod++
			case "test":
				observation.goTest++
				t.Fatalf("fallback reached fake Go ordinary patch-state assertion after apply inspection")
			}
		}
		if strict && c.name == "go" && len(c.args) > 0 {
			switch c.args[0] {
			case "mod":
				observation.goMod++
			case "test":
				observation.goTest++
			}
		}
		return processResult{}, false
	}
}

func patchLinkStrictExecutor(t *testing.T, f fixture, observation *patchLinkObservation) func(context.Context, command) (processResult, bool) {
	t.Helper()
	intercept := patchLinkInterceptor(t, f, observation, true)
	return func(ctx context.Context, c command) (processResult, bool) {
		if isPatchLinkCommand(c) {
			c = insertGitConfig(c, "core.symlinks=false")
			return intercept(ctx, c)
		}
		return intercept(ctx, c)
	}
}

func isPatchLinkCommand(c command) bool {
	return c.name == "git" && slices.Contains(c.args, "apply") && slices.ContainsFunc(c.args, func(arg string) bool {
		return filepath.Base(arg) == string(closedCallPatch)
	})
}

func insertGitConfig(c command, setting string) command {
	index := slices.Index(c.args, "apply")
	if index < 0 {
		return c
	}
	args := make([]string, 0, len(c.args)+2)
	args = append(args, c.args[:index]...)
	args = append(args, "-c", setting)
	args = append(args, c.args[index:]...)
	c.args = args
	return c
}

func configIndex(args []string, setting string) int {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c" && args[i+1] == setting {
			return i
		}
	}
	return -1
}

func configBeforeSubcommand(args []string, setting string) bool {
	index := slices.Index(args, "apply")
	config := configIndex(args, setting)
	return index >= 0 && config >= 0 && config+1 < index
}

func observePatchLinkEntry(name string) patchLinkEntry {
	info, err := os.Lstat(name)
	if err != nil {
		return patchLinkEntry{missing: errors.Is(err, fs.ErrNotExist), errText: err.Error()}
	}
	entry := patchLinkEntry{exists: true, mode: info.Mode()}
	if info.Mode()&fs.ModeSymlink != 0 {
		entry.target, err = os.Readlink(name)
	} else if info.Mode().IsRegular() {
		entry.data, err = os.ReadFile(name)
	}
	if err != nil {
		entry.errText = err.Error()
	}
	return entry
}

func assertStrictPatchLinkOutcome(t *testing.T, observation patchLinkObservation, runErr error) {
	t.Helper()
	if len(observation.checks) != 1 || processError(observation.checks[0]) != nil {
		t.Fatalf("strict patch-link check did not succeed: %+v", observation.checks)
	}
	if len(observation.applications) != 1 || len(observation.commands) != 2 {
		t.Fatalf("strict patch-link commands: checks=%d applications=%d commands=%d", len(observation.checks), len(observation.applications), len(observation.commands))
	}
	for _, args := range observation.commands {
		if !configBeforeSubcommand(args, "core.symlinks=false") || !configBeforeSubcommand(args, "core.symlinks=true") || configIndex(args, "core.symlinks=true") < configIndex(args, "core.symlinks=false") {
			t.Fatalf("strict patch-link configuration was not ordered after the conflicting false setting: %v", args)
		}
	}
	if observation.goMod != 0 || observation.goTest != 0 {
		t.Fatalf("Go module/test commands ran before unsafe-link handling: mod=%d test=%d", observation.goMod, observation.goTest)
	}
	if len(observation.entries) != 1 {
		t.Fatalf("strict patch-link entry observations: %d", len(observation.entries))
	}
	application := observation.applications[0]
	if application.err != nil && errors.Is(application.err, exec.ErrNotFound) {
		t.Fatalf("Git is unavailable: %v", application.err)
	}
	if err := processError(application); err != nil {
		if runErr == nil || !strings.Contains(runErr.Error(), string(closedCallPatch)) || !isNativeLinkRefusal(application) {
			t.Fatalf("unrelated Git application failure: %v\n%s\nrun error: %v", err, processDiagnostic(application), runErr)
		}
		if len(observation.entries) != 1 || !observation.entries[0].missing || observation.entries[0].exists || observation.entries[0].errText == "" {
			t.Fatalf("failed native-link application left an input entry: %+v", observation.entries)
		}
		t.Logf("Git refused native symlink creation after a valid patch: %s", processDiagnostic(application))
		return
	}
	entry := observation.entries[0]
	if !entry.exists || entry.mode&fs.ModeSymlink == 0 || entry.target != observation.target {
		t.Fatalf("successful strict apply did not create the expected symlink: %+v", entry)
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "unsafe patched input entry") {
		t.Fatalf("real checkInput did not reject the created symlink: %v", runErr)
	}
	t.Logf("native symlink was rejected by checkInput before Go module preparation")
}

func processDiagnostic(result processResult) string {
	data := append(append([]byte{}, result.stderr...), result.stdout...)
	if result.err != nil {
		data = append(data, []byte("\n")...)
		data = append(data, result.err.Error()...)
	}
	return strings.TrimSpace(string(data))
}

func isNativeLinkRefusal(result processResult) bool {
	if result.err != nil || result.exitCode == 0 {
		return false
	}
	for _, line := range strings.Split(strings.ToLower(string(result.stderr)+"\n"+string(result.stdout)), "\n") {
		linkFailure := strings.Contains(line, "unable to create symlink") || strings.Contains(line, "could not create symlink") ||
			strings.Contains(line, "cannot create symlink") || strings.Contains(line, "failed to create symlink")
		if !linkFailure {
			continue
		}
		for _, phrase := range []string{"permission denied", "operation not permitted", "operation not supported", "not supported", "function not implemented", "invalid argument", "required privilege"} {
			if strings.Contains(line, phrase) {
				return true
			}
		}
	}
	return false
}

func logPatchLinkResult(t *testing.T, mode string, check bool, result processResult, entry patchLinkEntry) {
	t.Helper()
	op := "apply"
	if check {
		op = "apply --check"
	}
	t.Logf("%s git %s: exit=%d err=%v stdout=%q stderr=%q entry={exists:%t mode:%v target:%q bytes:%q error:%q}", mode, op, result.exitCode, result.err, result.stdout, result.stderr, entry.exists, entry.mode, entry.target, entry.data, entry.errText)
}

func snapshotFixtureAssets(t *testing.T, f fixture) map[string]fixtureAsset {
	t.Helper()
	dir := filepath.Join(f.root, "doc", "upstream-repro")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	assets := make(map[string]fixtureAsset, len(entries))
	for _, entry := range entries {
		name := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(name)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		assets[entry.Name()] = fixtureAsset{data: data, mode: info.Mode()}
	}
	return assets
}

func assertFixtureAssets(t *testing.T, f fixture, want map[string]fixtureAsset) {
	t.Helper()
	dir := filepath.Join(f.root, "doc", "upstream-repro")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Errorf("read fixture assets after run: %v", err)
		return
	}
	got := make(map[string]bool, len(entries))
	for _, entry := range entries {
		got[entry.Name()] = true
		name := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(name)
		if err != nil {
			t.Errorf("stat fixture asset %q after run: %v", entry.Name(), err)
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Errorf("read fixture asset %q after run: %v", entry.Name(), err)
			continue
		}
		wantAsset, ok := want[entry.Name()]
		if !ok {
			t.Errorf("unexpected fixture asset after run: %q", entry.Name())
			continue
		}
		if info.Mode() != wantAsset.mode || !bytes.Equal(data, wantAsset.data) {
			t.Errorf("fixture asset %q changed: mode %v/%v, bytes equal %t", entry.Name(), info.Mode(), wantAsset.mode, bytes.Equal(data, wantAsset.data))
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("fixture asset removed by run: %q", name)
		}
	}
}
