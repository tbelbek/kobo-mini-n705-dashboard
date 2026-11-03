package main

import (
	"os"
	"testing"
	"time"
)

// The sleep test counts down to nothing, and a file that stopped moving ends it.
func TestTestCycles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if testLeft() != 0 {
		t.Fatal("no file: no test")
	}
	os.WriteFile(stateFile("test-cycles"), []byte("2\n"), 0644)
	if n := testLeft(); n != 2 {
		t.Fatalf("left %d, want 2", n)
	}
	testDone(2)
	if n := testLeft(); n != 1 {
		t.Fatalf("left %d, want 1", n)
	}
	testDone(1)
	if _, err := os.Stat(stateFile("test-cycles")); err == nil {
		t.Fatal("file still there after the last cycle")
	}
	os.WriteFile(stateFile("test-cycles"), []byte("5"), 0644)
	old := time.Now().Add(-testStale - time.Minute)
	os.Chtimes(stateFile("test-cycles"), old, old)
	if testLeft() != 0 {
		t.Fatal("a stale file still runs the test")
	}
}
