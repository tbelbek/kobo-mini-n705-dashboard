package main

import "testing"

// Power off only on believable readings: a lone 0 (the driver's read failure) is not.
func TestBattCritical(t *testing.T) {
	for _, c := range []struct {
		r        []int
		charging bool
		lastGood int
		want     bool
	}{
		{[]int{3, 2, 2}, false, 4, true},
		{[]int{0, 0, 0}, false, 80, false}, // a healthy cell reading 0: the MSP430 read failed
		{[]int{0, 0, 0}, false, 5, true},   // 0 right after 5%: believable
		{[]int{2, 0, 2}, false, 0, true},
		{[]int{3, 4, 2}, false, 4, false}, // one over the line
		{[]int{1, 1, 1}, true, 4, false},  // charging
	} {
		if got := battCritical(c.r, c.charging, c.lastGood); got != c.want {
			t.Errorf("%+v: %v, want %v", c, got, c.want)
		}
	}
}
