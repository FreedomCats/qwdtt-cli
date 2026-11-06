package cli

import (
	"bufio"
	"context"
	"io"
)

// lineInput owns stdin across prompts and captcha requests. Canceling a waiter
// leaves its pending read available to the next caller instead of losing a line.
type lineInput struct {
	reader  *bufio.Reader
	owner   chan struct{}
	pending *inputRead
}

type inputRead struct {
	done chan struct{}
	line string
	err  error
}

func newLineInput(in io.Reader, terminal bool) *lineInput {
	if terminal {
		// Do not buffer future password input before terminal echo is disabled.
		in = terminalLineReader{in}
	}
	return &lineInput{reader: bufio.NewReader(in), owner: make(chan struct{}, 1)}
}

func (in *lineInput) readLine(ctx context.Context) (string, error) {
	select {
	case in.owner <- struct{}{}:
		defer func() { <-in.owner }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if in.pending == nil {
		read := &inputRead{done: make(chan struct{})}
		in.pending = read
		go func() {
			read.line, read.err = in.reader.ReadString('\n')
			close(read.done)
		}()
	}
	read := in.pending
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-read.done:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		in.pending = nil
		return read.line, read.err
	}
}

// secret waits for any ordinary read to finish before handing stdin to hidden
// input. Its completed line stays pending for the next ordinary prompt.
func (in *lineInput) secret(read func() (string, error)) (string, error) {
	in.owner <- struct{}{}
	defer func() { <-in.owner }()
	if in.pending != nil {
		<-in.pending.done
	}
	return read()
}

type terminalLineReader struct{ io.Reader }

func (r terminalLineReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func (e *env) input() *lineInput {
	e.inputOnce.Do(func() {
		_, terminal := e.terminalFD()
		e.lines = newLineInput(e.stdin, terminal)
	})
	return e.lines
}
