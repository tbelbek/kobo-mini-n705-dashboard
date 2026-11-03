package main

import (
	"os"
	"strings"
	"testing"
)

// The card log rotates at 256 KB while running, keeping one .old.
func TestCardLogRotates(t *testing.T) {
	p := t.TempDir() + "/k.log"
	w := cardLog(p)
	line := strings.Repeat("x", 1023) + "\n"
	for i := 0; i < 300; i++ { // 300 KB
		w.Write([]byte(line))
		if i%50 == 0 {
			flushLog() // the queue holds 256 lines; past that it drops, by design
		}
	}
	flushLog()
	old, err1 := os.Stat(p + ".old")
	cur, err2 := os.Stat(p)
	if err1 != nil || err2 != nil || old.Size() < 256<<10 || cur.Size() >= 256<<10 {
		t.Fatalf("old %v %v, current %v %v", old, err1, cur, err2)
	}
}
