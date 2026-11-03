package main

import (
	"slices"
	"testing"
)

// Sleep frames go on without a flash, every fullEvery-th one flashes; other frames always do.
func TestFbinkArgs(t *testing.T) {
	flashes := 0
	for p := 0; p < fullEvery; p++ {
		args, flash := fbinkArgs(true, p, true)
		if flash != slices.Contains(args, "-f") || !slices.Contains(args, "-w") {
			t.Fatalf("partials %d: %v flash %v", p, args, flash)
		}
		if flash {
			flashes++
		}
	}
	if flashes != 1 {
		t.Errorf("%d flashes in %d sleep frames, want 1", flashes, fullEvery)
	}
	if args, flash := fbinkArgs(false, 0, false); !flash || slices.Contains(args, "-w") {
		t.Errorf("interactive frame: %v", args)
	}
}
