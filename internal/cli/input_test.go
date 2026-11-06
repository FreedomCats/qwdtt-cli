package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type observedInput struct {
	io.Reader
	reads chan struct{}
}

func (r observedInput) Read(p []byte) (int, error) {
	r.reads <- struct{}{}
	return r.Reader.Read(p)
}

func inputPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r.Close()
		w.Close()
	})
	return r, w
}

func awaitInput(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("input operation did not start or complete promptly")
	}
}

func TestLineInputBufferingAndEOF(t *testing.T) {
	in := newLineInput(strings.NewReader("first\n\nlast"), false)
	for _, tc := range []struct {
		line string
		err  error
	}{
		{line: "first\n"},
		{line: "\n"},
		{line: "last", err: io.EOF},
		{err: io.EOF},
	} {
		line, err := in.readLine(context.Background())
		if line != tc.line || !errors.Is(err, tc.err) {
			t.Errorf("readLine = %q, %v; want %q, %v", line, err, tc.line, tc.err)
		}
	}
	e := &env{stdin: strings.NewReader("  choice  \nlast")}
	for _, want := range []string{"choice", "last"} {
		if got, err := e.readLine(); err != nil || got != want {
			t.Errorf("prompt readLine = %q, %v; want %q", got, err, want)
		}
	}
	if _, err := e.readLine(); !errors.Is(err, errNoInput) {
		t.Errorf("prompt EOF = %v, want errNoInput", err)
	}
}

func TestLineInputCancellationRetainsPendingRead(t *testing.T) {
	r, w := inputPipe(t)
	reads := make(chan struct{}, 16)
	in := newLineInput(observedInput{Reader: r, reads: reads}, false)
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := in.readLine(ctx)
			done <- err
		}()
		if i == 0 {
			awaitInput(t, reads)
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled read = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("input waiter did not return on cancellation")
		}
	}
	select {
	case <-reads:
		t.Fatal("cancellation started a competing stdin read")
	default:
	}
	if _, err := io.WriteString(w, "first\nsecond\n"); err != nil {
		t.Fatal(err)
	}
	awaitInput(t, in.pending.done)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := in.readLine(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("already canceled read = %v", err)
	}
	for _, want := range []string{"first\n", "second\n"} {
		if got, err := in.readLine(context.Background()); err != nil || got != want {
			t.Errorf("resumed read = %q, %v; want %q", got, err, want)
		}
	}
}

func TestLineInputHandsOffToSecretWithoutConcurrentReads(t *testing.T) {
	r, w := inputPipe(t)
	reads := make(chan struct{}, 64)
	reader := observedInput{Reader: r, reads: reads}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	e := &env{stdin: reader, secrets: func(string) (string, error) {
		close(entered)
		raw := make([]byte, len("made-up-password\n"))
		_, err := io.ReadFull(reader, raw)
		<-release
		return strings.TrimSpace(string(raw)), err
	}}
	// Exercise the terminal mode without changing any host terminal settings.
	e.inputOnce.Do(func() { e.lines = newLineInput(reader, true) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := e.input().readLine(ctx)
		done <- err
	}()
	awaitInput(t, reads)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	secret := make(chan string, 1)
	go func() {
		value, err := e.askSecret("Password: ")
		if err != nil {
			value = err.Error()
		}
		secret <- value
	}()
	select {
	case <-entered:
		t.Fatal("secret reader started before the pending read finished")
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := io.WriteString(w, "menu\nmade-up-password\nnext\n"); err != nil {
		t.Fatal(err)
	}
	awaitInput(t, entered)
	blockedCtx, unblock := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer unblock()
	if _, err := e.input().readLine(blockedCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ordinary read during hidden input = %v", err)
	}
	finish()
	select {
	case got := <-secret:
		if got != "made-up-password" {
			t.Errorf("hidden input = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("hidden input did not complete")
	}
	for _, want := range []string{"menu", "next"} {
		if got, err := e.readLine(); err != nil || got != want {
			t.Errorf("prompt after hidden input = %q, %v; want %q", got, err, want)
		}
	}
}
