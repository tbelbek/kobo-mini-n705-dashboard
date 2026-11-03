package main

import (
	"strings"
	"testing"
	"time"
)

// A day of log lines in the device's format, counted into one report line.
func TestDailyReport(t *testing.T) {
	now := time.Date(2026, 11, 4, 6, 0, 30, 0, time.Local)
	lines := []string{
		"2026/11/03 05:59:00 woke: timer after 8h0m0s pil %90", // outside the 24 h window
		"2026/11/03 20:00:05 woke: timer after 1h59m50s pil %80",
		"2026/11/03 20:00:14 wifi: up in 8.4s",
		"2026/11/03 20:00:30 sleep: alarm in 1h59m30s (next update 22:00:00) pil %80",
		"2026/11/03 21:10:00 woke: button after 1h9m30s pil %80",
		"2026/11/03 21:30:00 woke: usb after 15m0s pil %80",
		"2026/11/03 21:12:00 suspend: write /sys/power/state: device or resource busy after 22s",
		"2026/11/03 22:00:05 woke: timer after 48m0s pil %79",
		"2026/11/03 22:00:12 wifi: up in 7.2s",
		"2026/11/03 22:00:20 wifi: up in 0s",
		"2026/11/03 22:00:30 sleep: alarm in 8h0m0s (next update 06:00:00) pil %79",
		"2026/11/04 06:00:01 woke: timer after 7h59m30s pil %75",
		"2026/11/04 06:00:09 wifi: up in 8.8s",
		"2026/11/04 06:00:10 supervise: RESCUE: kobodash 720s donuk",
		"2026/11/04 06:00:20 kobodash v0.7.33 starting",
	}
	got := dailyReport(strings.Join(lines, "\n"), now, 9)
	for _, want := range []string{"⚠", "zamanlı uyanış 3/9", "tuş 1", "şarj kablosu 1", "uyku hatası 1", "kurtarma 1", "yeniden başlama 1", "Wi-Fi ort. 8.4 sn", "gece 8h0m0s uykuda %4 (0.50 %/sa)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// Expected timed wakes in a day: 06, 08 ... 22 with quiet hours 23-06.
func TestExpectedWakes(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Stockholm")
	a := &App{loc: loc, cfg: Config{QuietFrom: 23, QuietTo: 6, RefreshMin: 120}}
	now := time.Date(2026, 11, 4, 6, 0, 30, 0, loc)
	if n := a.expectedWakes(now.Add(-24*time.Hour), now); n != 9 {
		t.Errorf("expected wakes = %d, want 9", n)
	}
}

// Quiet hours ending off the grid: their end is the wake (07:00 with 23-7 and a 2 h grid).
func TestExpectedWakesQuietOffGrid(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Stockholm")
	a := &App{loc: loc, cfg: Config{QuietFrom: 23, QuietTo: 7, RefreshMin: 120}}
	now := time.Date(2026, 11, 4, 7, 0, 30, 0, loc)
	if n := a.expectedWakes(now.Add(-24*time.Hour), now); n != 9 { // 08 10 12 14 16 18 20 22 + 07
		t.Errorf("expected wakes = %d, want 9", n)
	}
}
