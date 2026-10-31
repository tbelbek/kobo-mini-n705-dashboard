package main

import (
	"testing"
	"time"
)

// A deleted occurrence (EXDATE) and a cancelled event must not show.
func TestICSExdateAndCancelled(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Stockholm")
	raw := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\n" +
		"DTSTART;TZID=Europe/Stockholm:20261011T103000\r\nDTEND;TZID=Europe/Stockholm:20261011T113000\r\n" +
		"RRULE:FREQ=WEEKLY;UNTIL=20261224T230000Z;BYDAY=SU\r\n" +
		"EXDATE;TZID=Europe/Stockholm:20261101T103000\r\nEXDATE;TZID=Europe/Stockholm:20261108T103000\r\n" +
		"SUMMARY:Voleybol\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nDTSTART:20261102T120000Z\r\nSTATUS:CANCELLED\r\nSUMMARY:İptal\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	lo := time.Date(2026, 10, 26, 0, 0, 0, 0, loc)
	hi := time.Date(2026, 11, 16, 0, 0, 0, 0, loc)
	var got []string
	for _, e := range parseICS(raw, lo, hi, loc) {
		got = append(got, e.Start.Format("01-02 ")+e.Summary)
	}
	want := []string{"11-15 Voleybol"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("got %v, want %v", got, want)
	}
}
