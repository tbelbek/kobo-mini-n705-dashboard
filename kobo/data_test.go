package main

import (
	"fmt"
	"os"
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

// Reference: Open-Meteo's sunrise/sunset for Göteborg (its past dates ignore DST: August shifted +1 h here).
func TestSunTimes(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Stockholm")
	for _, c := range []struct{ day, rise, set string }{
		{"2026-10-31", "07:22", "16:28"},
		{"2026-08-01", "05:06", "21:28"},
	} {
		d, _ := time.ParseInLocation("2006-01-02", c.day, loc)
		rise, set, ok := sunTimes(57.7089, 11.9746, d)
		near := func(got time.Time, want string) bool {
			w, _ := time.ParseInLocation("2006-01-02 15:04", c.day+" "+want, loc)
			return ok && got.Sub(w).Abs() <= 4*time.Minute
		}
		if !near(rise, c.rise) || !near(set, c.set) {
			t.Errorf("%s: got %s-%s, want %s-%s", c.day, rise.Format("15:04"), set.Format("15:04"), c.rise, c.set)
		}
	}
}

// The dashboard's 10 days: a multi-day event marks every day it covers but is listed once,
// today's finished event is dropped, nothing past the 10th day shows.
func TestHorizon(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Stockholm")
	raw := "BEGIN:VCALENDAR\r\n" +
		"BEGIN:VEVENT\r\nDTSTART;TZID=Europe/Stockholm:20261101T080000\r\nSUMMARY:Bitti\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nDTSTART;VALUE=DATE:20261103\r\nDTEND;VALUE=DATE:20261106\r\nSUMMARY:Kamp\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nDTSTART;TZID=Europe/Stockholm:20261111T100000\r\nSUMMARY:Sonra\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	a := &App{loc: loc, cal: Calendar{raw: map[string]string{"u": raw}}}
	days := a.horizon(time.Date(2026, 11, 1, 9, 0, 0, 0, loc), 10)
	var marked []int
	for _, d := range days {
		if len(d.Evs) > 0 {
			marked = append(marked, d.Day.Day())
		}
	}
	items := agendaItems(days)
	if len(days) != 10 || fmt.Sprint(marked) != "[3 4 5]" || len(items) != 1 || items[0].E.Summary != "Kamp" {
		t.Fatalf("days %d, marked %v, items %v", len(days), marked, items)
	}
}

// Old confs named a second city in PLACE and home in PLACE2: home must be PLACE2, and a conf
// without any place leaves the default and lets the IP lookup run.
func TestConfigPlace(t *testing.T) {
	dir := t.TempDir()
	old := dir + "/old.conf"
	os.WriteFile(old, []byte("PLACE=İstanbul\nLAT=41.0082\nLON=28.9784\nTZ1=Europe/Istanbul\nPLACE2=Göteborg\nLAT2=57.7089\nLON2=11.9746\n"), 0o600)
	if c := loadConfig(old); c.Place.Name != "Göteborg" || c.Place.Lat != 57.7089 || !c.PlaceSet {
		t.Fatalf("legacy conf: %+v set=%v", c.Place, c.PlaceSet)
	}
	none := dir + "/none.conf"
	os.WriteFile(none, []byte("ICS_URL=file:///x.ics\n"), 0o600)
	if c := loadConfig(none); c.PlaceSet || !c.Locate || c.Place.Name != "Göteborg" {
		t.Fatalf("no place: %+v set=%v locate=%v", c.Place, c.PlaceSet, c.Locate)
	}
	if d := km(Place{Lat: 57.7089, Lon: 11.9746}, Place{Lat: 59.3293, Lon: 18.0686}); d < 390 || d > 405 {
		t.Fatalf("Göteborg-Stockholm %.0f km", d)
	}
}
