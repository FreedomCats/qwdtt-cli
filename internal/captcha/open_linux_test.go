package captcha

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Программа, открывшая ссылку, после выхода забирается: зомби за qwdtt-cli не
// остаётся. Проверяется по /proc, поэтому только на Linux.
func TestStartDetachedLeavesNoZombie(t *testing.T) {
	if err := startDetached("true", nil); err != nil {
		t.Skipf("нет программы true: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		zombies := zombieChildren(t)
		if len(zombies) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("дочерние процессы остались зомби: %v", zombies)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// zombieChildren — PID дочерних процессов теста в состоянии Z.
func zombieChildren(t *testing.T) []int {
	t.Helper()
	stats, err := filepath.Glob("/proc/[0-9]*/stat")
	if err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	var zombies []int
	for _, path := range stats {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue // процесс успел выйти
		}
		// Формат: pid (comm) state ppid …; comm может содержать пробелы.
		s := string(raw)
		end := strings.LastIndexByte(s, ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(s[end+1:])
		if len(fields) < 2 {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		if ppid == self && fields[0] == "Z" {
			pid, _ := strconv.Atoi(strings.Fields(s)[0])
			zombies = append(zombies, pid)
		}
	}
	return zombies
}
