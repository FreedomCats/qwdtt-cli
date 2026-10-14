package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Своя консоль без человека — планировщик, обвязка с перенаправлением: Enter
// не нажмёт никто, и пауза повесила бы процесс. Паузы нет, если stdin или
// stderr — не консоль.
func TestNoPauseWithoutConsoleStreams(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	owns := func() bool { return true }
	cases := []struct {
		name string
		e    *env
	}{
		{"stdin и stderr в памяти", &env{stdin: strings.NewReader("\n"), stderr: &bytes.Buffer{}, ownsConsole: owns}},
		{"stdin из канала", &env{stdin: r, stderr: os.Stderr, ownsConsole: owns}},
		{"stderr в канал", &env{stdin: os.Stdin, stderr: w, ownsConsole: owns}},
	}
	for _, c := range cases {
		if shouldPause(c.e) {
			t.Errorf("%s: пауза есть, а ответить на неё некому", c.name)
		}
	}

	var errOut bytes.Buffer
	e := &env{stdin: strings.NewReader(""), stderr: &errOut, ownsConsole: owns}
	pauseBeforeExit(e)
	if errOut.Len() != 0 {
		t.Errorf("pauseBeforeExit напечатал %q", errOut.String())
	}
}

// Консоль не своя или человек сам ушёл из меню — паузы нет.
func TestNoPauseWhenNotOwnConsole(t *testing.T) {
	if shouldPause(&env{stdin: os.Stdin, stderr: os.Stderr, ownsConsole: func() bool { return false }}) {
		t.Error("пауза в чужой консоли")
	}
	if shouldPause(&env{stdin: os.Stdin, stderr: os.Stderr, noPause: true, ownsConsole: func() bool { return true }}) {
		t.Error("пауза после ухода из меню")
	}
}
