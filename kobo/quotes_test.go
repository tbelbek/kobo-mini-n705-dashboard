package main

import (
	"strings"
	"testing"
	"time"
)

// Every passage must fit the 4 lines the dashboard reserves for it.
func TestQuotesFit(t *testing.T) {
	if len(quotes) == 0 {
		t.Fatal("no quotes embedded")
	}
	for _, q := range quotes {
		lines := wrap(quoteFace(), "“"+q.Text+"”", W-2*mx)
		if len(lines) > 4 || strings.HasSuffix(lines[len(lines)-1], "…") {
			t.Errorf("%d lines (max 4): %s", len(lines), q.Text)
		}
	}
}

// Same passage all day; no repeats within a year until the pool runs out.
func TestQuoteForStableAndVaried(t *testing.T) {
	day := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	a, _ := quoteFor(day)
	b, _ := quoteFor(day.Add(12 * time.Hour))
	if a != b {
		t.Fatal("passage changed within the same day")
	}
	seen := map[Quote]bool{}
	for i := 0; i < len(quotes) && i < 365; i++ {
		q, _ := quoteFor(time.Date(2026, 1, 1+i, 9, 0, 0, 0, time.UTC))
		if seen[q] {
			t.Fatalf("repeat on day %d before the pool of %d ran out", i+1, len(quotes))
		}
		seen[q] = true
	}
}
