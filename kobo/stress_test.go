package main

import (
	"os"
	"testing"
)

func TestStressTake(t *testing.T) {
	p := stateFile(stressFile)
	defer os.Remove(p)
	os.WriteFile(p, []byte("2 -\nbogus line\n1 dhd_watchdog_ms=60000\n1 rm -rf /\n"), 0o644)
	want := []struct {
		args string
		left int
	}{{"", 1}, {"", 0}, {"dhd_watchdog_ms=60000", 0}}
	for i, w := range want {
		args, left, ok := stressTake()
		if !ok || args != w.args || left != w.left {
			t.Fatalf("take %d: %q %d %v, want %q %d", i, args, left, ok, w.args, w.left)
		}
	}
	if _, _, ok := stressTake(); ok { // the shell-injection line is refused, and the file goes
		t.Fatal("bad line taken")
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("file left behind")
	}
}
