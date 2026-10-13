package linuxsys

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeRoots — поддельные /proc и /etc на время теста.
func fakeRoots(t *testing.T) (proc, etc string) {
	t.Helper()
	proc, etc = t.TempDir(), t.TempDir()
	oldProc, oldEtc := procRoot, etcRoot
	procRoot, etcRoot = proc, etc
	t.Cleanup(func() { procRoot, etcRoot = oldProc, oldEtc })
	return proc, etc
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReversePathFilter(t *testing.T) {
	for _, tc := range []struct {
		all, own string
		want     int
	}{
		{"1\n", "2\n", 2}, // как на машине разработки: enp7s0 нестрогий
		{"2\n", "1\n", 2},
		{"1\n", "0\n", 1},
		{"0\n", "0\n", 0},
	} {
		proc, _ := fakeRoots(t)
		writeFile(t, filepath.Join(proc, "sys/net/ipv4/conf/all/rp_filter"), tc.all)
		writeFile(t, filepath.Join(proc, "sys/net/ipv4/conf/eth0/rp_filter"), tc.own)
		got, err := ReversePathFilter("eth0")
		if err != nil || got != tc.want {
			t.Errorf("all=%q eth0=%q: %d, %v; ждали %d", tc.all, tc.own, got, err, tc.want)
		}
	}
	fakeRoots(t)
	if _, err := ReversePathFilter("eth0"); err == nil {
		t.Error("нет файлов — нет ошибки")
	}
}

func TestFirewalldStrictIPv6(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running bool
		conf    string // "" — файла нет
		want    bool
	}{
		{"не запущен", false, "IPv6_rpfilter=yes\n", false},
		{"yes", true, "# IPv6_rpfilter=no\nIPv6_rpfilter=yes\n", true},
		{"без ключа — умолчание yes", true, "Lockdown=no\n", true},
		{"без файла", true, "", true},
		{"no", true, "IPv6_rpfilter=no\n", false},
		{"strict", true, "IPv6_rpfilter=strict\n", true},
		{"loose", true, "IPv6_rpfilter=loose\n", false},
		{"strict-forward", true, "IPv6_rpfilter=strict-forward\n", false},
	} {
		proc, etc := fakeRoots(t)
		writeFile(t, filepath.Join(proc, "1/comm"), "systemd\n")
		if tc.running {
			writeFile(t, filepath.Join(proc, "5512/comm"), "firewalld\n")
		}
		writeFile(t, filepath.Join(proc, "self/comm"), "firewalld\n") // не число — не в счёт
		if tc.conf != "" {
			writeFile(t, filepath.Join(etc, "firewalld/firewalld.conf"), tc.conf)
		}
		got, err := FirewalldStrictIPv6()
		if err != nil || got != tc.want {
			t.Errorf("%s: %v, %v; ждали %v", tc.name, got, err, tc.want)
		}
	}
}
