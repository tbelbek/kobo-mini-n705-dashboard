package main

import "testing"

// At most watchdogMax reboots in any 24 h; older ones (and clock nonsense) drop out.
// Each return of trouble within the hour goes one step further; a quiet hour starts over.
func TestHealStep(t *testing.T) {
	now := int64(1_800_000_000)
	for _, c := range []struct{ last, ago, want int }{
		{0, 0, 1}, {1, 600, 2}, {2, 600, 3}, {3, 600, 3}, {2, 4000, 1}, {3, -60, 1},
	} {
		if got := healStep(c.last, now-int64(c.ago), now, healForget); got != c.want {
			t.Errorf("last %d %ds ago: step %d, want %d", c.last, c.ago, got, c.want)
		}
	}
}

func TestRebootAllowed(t *testing.T) {
	now := int64(1_800_000_000)
	h := int64(3600)
	for _, c := range []struct {
		past []int64
		keep int
		ok   bool
	}{
		{nil, 0, true},
		{[]int64{now - 2*h, now - h}, 2, true},
		{[]int64{now - 3*h, now - 2*h, now - h}, 3, false},
		{[]int64{now - 30*h, now - 25*h, now - h}, 1, true},
		{[]int64{now + 10*h, now - h, now - 2*h}, 3, false}, // clock set back: still counts
		{[]int64{now + 30*h}, 0, true},                      // nonsense far ahead is dropped
	} {
		keep, ok := rebootAllowed(c.past, now)
		if len(keep) != c.keep || ok != c.ok {
			t.Errorf("past %v: keep %d ok %v, want %d %v", c.past, len(keep), ok, c.keep, c.ok)
		}
	}
}
