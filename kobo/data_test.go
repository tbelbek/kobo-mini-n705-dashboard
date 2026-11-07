package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// Quiet hours 23-6: a wake due at night moves to 06:00, a daytime one stays.
func TestWakeAt(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Stockholm")
	a := &App{loc: loc, cfg: Config{QuietFrom: 23, QuietTo: 6}}
	at := func(s string) time.Time { v, _ := time.ParseInLocation("2006-01-02 15:04", s, loc); return v }
	for _, c := range [][2]string{
		{"2026-11-01 22:40", "2026-11-01 22:40"},
		{"2026-11-01 23:10", "2026-11-02 06:00"},
		{"2026-11-02 03:00", "2026-11-02 06:00"},
		{"2026-11-02 06:00", "2026-11-02 06:00"},
	} {
		if got := a.wakeAt(at(c[0])); !got.Equal(at(c[1])) {
			t.Errorf("%s -> %s, want %s", c[0], got.Format("01-02 15:04"), c[1])
		}
	}
}

// Timed wakes stay on the 2-hour grid whatever time the last refresh ran.
func TestNextSlot(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Stockholm")
	a := &App{loc: loc, cfg: Config{QuietFrom: 23, QuietTo: 6, RefreshMin: 120}}
	at := func(s string) time.Time { v, _ := time.ParseInLocation("2006-01-02 15:04:05", s, loc); return v }
	for _, c := range [][2]string{
		{"2026-11-03 06:00:20", "2026-11-03 08:00:00"}, // timed wake
		{"2026-11-03 06:50:10", "2026-11-03 08:00:00"}, // power button in between: no shift
		{"2026-11-03 07:58:30", "2026-11-03 10:00:00"}, // under 30 min before a slot counts as it
		{"2026-11-03 07:31:00", "2026-11-03 10:00:00"},
		{"2026-11-03 07:29:00", "2026-11-03 08:00:00"},
		{"2026-11-03 22:00:15", "2026-11-04 06:00:00"}, // 00:00 is in quiet hours
		{"2026-11-04 01:30:00", "2026-11-04 06:00:00"},
	} {
		if got := a.nextSlot(at(c[0])); !got.Equal(at(c[1])) {
			t.Errorf("%s -> %s, want %s", c[0], got.Format("01-02 15:04:05"), c[1])
		}
	}
	// DST days keep even hours: 2026-03-29 (02:00 skipped) and 2026-10-25 (02:00 twice)
	for _, c := range [][2]string{
		{"2026-03-29 06:00:10", "2026-03-29 08:00:00"},
		{"2026-03-29 08:00:10", "2026-03-29 10:00:00"},
		{"2026-10-25 06:00:10", "2026-10-25 08:00:00"},
		{"2026-10-25 20:00:10", "2026-10-25 22:00:00"},
	} {
		if got := a.nextSlot(at(c[0])); !got.Equal(at(c[1])) {
			t.Errorf("DST %s -> %s, want %s", c[0], got.Format("01-02 15:04:05"), c[1])
		}
	}
	if !a.nextSlot(time.Time{}).IsZero() {
		t.Error("never refreshed must be due now")
	}
}

func TestAlertStep(t *testing.T) {
	for _, c := range []struct {
		pct, last, wantNext int
		send                bool
	}{
		{50, 0, 0, false},   // fine
		{20, 0, 20, true},   // first warning
		{18, 20, 20, false}, // already told
		{10, 20, 10, true},  // second warning
		{5, 10, 10, false},
		{25, 10, 10, false}, // charging, not yet full enough to rearm
		{31, 10, 0, false},  // charged: rearm
		{8, 0, 10, true},    // woke up already very low: one message, not two
	} {
		send, next := alertStep(c.pct, 20, c.last)
		if send != c.send || next != c.wantNext {
			t.Errorf("alertStep(%d, 20, %d) = %v,%d; want %v,%d", c.pct, c.last, send, next, c.send, c.wantNext)
		}
	}
}

func TestHasDefaultRoute(t *testing.T) {
	hdr := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\n"
	link := "eth0\t0001A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\n"
	def := "eth0\t00000000\t0101A8C0\t0003\t0\t0\t218\t00000000\n"
	if hasDefaultRoute(hdr + link) {
		t.Error("link route alone counted as default")
	}
	if !hasDefaultRoute(hdr + link + def) {
		t.Error("default route not found")
	}
}

func TestStamped(t *testing.T) {
	var b bytes.Buffer
	w := &stamped{w: &b, t0: time.Now(), bol: true}
	for _, c := range []string{"Turning Wi", "-Fi ON\nSending disc", "over...\n"} {
		if n, err := w.Write([]byte(c)); n != len(c) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", c, n, err)
		}
	}
	got := b.String()
	if !strings.HasPrefix(got, "+ 0.00s Turning Wi-Fi ON\n+ 0.00s Sending discover...\n") || strings.Count(got, "+") != 2 {
		t.Errorf("stamped output %q", got)
	}
}

// Recurrence rules calendars really export (Google, iCloud): several weekdays, the nth
// weekday of a month, month-end days, UTC starts across DST, long-running series, and
// moved or cancelled single occurrences (RECURRENCE-ID).
func TestICSRecurrence(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Stockholm")
	ev := func(body string) string {
		return "BEGIN:VEVENT\r\n" + strings.ReplaceAll(body, "\n", "\r\n") + "\r\nEND:VEVENT\r\n"
	}
	day := func(s string) time.Time { v, _ := time.ParseInLocation("2006-01-02", s, loc); return v }
	for _, c := range []struct {
		name, ics, lo, hi string
		want              []string
	}{
		{"weekly on two days", ev("DTSTART;TZID=Europe/Stockholm:20261102T100000\nRRULE:FREQ=WEEKLY;BYDAY=MO,WE\nSUMMARY:x"),
			"2026-11-01", "2026-11-15", []string{"11-02 10:00", "11-04 10:00", "11-09 10:00", "11-11 10:00"}},
		{"weekly with count", ev("DTSTART;TZID=Europe/Stockholm:20261102T100000\nRRULE:FREQ=WEEKLY;BYDAY=MO,WE;COUNT=3\nSUMMARY:x"),
			"2026-11-01", "2026-11-30", []string{"11-02 10:00", "11-04 10:00", "11-09 10:00"}},
		{"every other week", ev("DTSTART;TZID=Europe/Stockholm:20261103T180000\nRRULE:FREQ=WEEKLY;INTERVAL=2;BYDAY=TU\nSUMMARY:x"),
			"2026-11-01", "2026-11-30", []string{"11-03 18:00", "11-17 18:00"}},
		{"first Monday", ev("DTSTART;TZID=Europe/Stockholm:20261102T090000\nRRULE:FREQ=MONTHLY;BYDAY=1MO\nSUMMARY:x"),
			"2026-11-01", "2027-01-31", []string{"11-02 09:00", "12-07 09:00", "01-04 09:00"}},
		{"last Friday", ev("DTSTART;TZID=Europe/Stockholm:20261030T170000\nRRULE:FREQ=MONTHLY;BYDAY=-1FR\nSUMMARY:x"),
			"2026-11-01", "2026-12-31", []string{"11-27 17:00", "12-25 17:00"}},
		{"Monday in the first week (BYDAY with BYMONTHDAY)", ev("DTSTART;TZID=Europe/Stockholm:20261102T090000\nRRULE:FREQ=MONTHLY;BYDAY=MO;BYMONTHDAY=1,2,3,4,5,6,7;COUNT=3\nSUMMARY:x"),
			"2026-11-01", "2027-02-28", []string{"11-02 09:00", "12-07 09:00", "01-04 09:00"}},
		{"every weekday (DAILY;BYDAY)", ev("DTSTART;TZID=Europe/Stockholm:20261106T080000\nRRULE:FREQ=DAILY;BYDAY=MO,TU,WE,TH,FR\nSUMMARY:x"),
			"2026-11-06", "2026-11-10", []string{"11-06 08:00", "11-09 08:00", "11-10 08:00"}},
		{"absurd nth weekday doesn't crash", ev("DTSTART;TZID=Europe/Stockholm:20261102T090000\nRRULE:FREQ=MONTHLY;BYDAY=-2147483648MO\nSUMMARY:x"),
			"2026-11-01", "2026-12-31", nil},
		{"UNTIL keeps its time", ev("DTSTART:20261102T180000Z\nRRULE:FREQ=DAILY;UNTIL=20261104T090000Z\nSUMMARY:x"),
			"2026-11-01", "2026-11-10", []string{"11-02 19:00", "11-03 19:00"}},
		{"31st skips short months", ev("DTSTART;VALUE=DATE:20270131\nRRULE:FREQ=MONTHLY\nSUMMARY:x"),
			"2027-01-01", "2027-05-31", []string{"01-31 00:00", "03-31 00:00", "05-31 00:00"}},
		{"UTC start keeps UTC across DST", ev("DTSTART:20260901T080000Z\nRRULE:FREQ=WEEKLY\nSUMMARY:x"),
			"2026-11-02", "2026-11-08", []string{"11-03 09:00"}},
		{"daily series from 2015", ev("DTSTART;TZID=Europe/Stockholm:20150101T070000\nRRULE:FREQ=DAILY\nSUMMARY:x"),
			"2026-11-03", "2026-11-04", []string{"11-03 07:00", "11-04 07:00"}},
		{"yearly birthday", ev("DTSTART;VALUE=DATE:20100315\nRRULE:FREQ=YEARLY\nSUMMARY:x"),
			"2027-03-01", "2027-03-31", []string{"03-15 00:00"}},
		{"moved occurrence", ev("UID:a\nDTSTART;TZID=Europe/Stockholm:20261103T100000\nRRULE:FREQ=WEEKLY\nSUMMARY:x") +
			ev("UID:a\nRECURRENCE-ID;TZID=Europe/Stockholm:20261110T100000\nDTSTART;TZID=Europe/Stockholm:20261111T120000\nSUMMARY:x"),
			"2026-11-09", "2026-11-18", []string{"11-11 12:00", "11-17 10:00"}},
		{"cancelled occurrence", ev("UID:b\nDTSTART;TZID=Europe/Stockholm:20261103T100000\nRRULE:FREQ=WEEKLY\nSUMMARY:x") +
			ev("UID:b\nRECURRENCE-ID;TZID=Europe/Stockholm:20261110T100000\nDTSTART;TZID=Europe/Stockholm:20261110T100000\nSTATUS:CANCELLED\nSUMMARY:x"),
			"2026-11-09", "2026-11-18", []string{"11-17 10:00"}},
	} {
		var got []string
		for _, e := range parseICS("BEGIN:VCALENDAR\r\n"+c.ics+"END:VCALENDAR\r\n", day(c.lo), day(c.hi), loc) {
			got = append(got, e.Start.Format("01-02 15:04"))
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Events is parsed once and reused until a feed's text changes.
func TestEventsMemo(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Stockholm")
	f := t.TempDir() + "/a.ics"
	ics := func(day string) {
		os.WriteFile(f, []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART;VALUE=DATE:"+day+"\r\nSUMMARY:x\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"), 0o644)
	}
	c := Calendar{Feeds: []string{"file://" + f}}
	lo, hi := time.Date(2026, 11, 1, 0, 0, 0, 0, loc), time.Date(2026, 11, 30, 0, 0, 0, 0, loc)
	ics("20261105")
	c.Refresh()
	if e := c.Events(lo, hi, loc); len(e) != 1 || e[0].Start.Day() != 5 {
		t.Fatalf("first parse: %v", e)
	}
	ics("20261107")
	if e := c.Events(lo, hi, loc); e[0].Start.Day() != 5 {
		t.Fatal("parsed again without a refresh")
	}
	c.Refresh()
	if e := c.Events(lo, hi, loc); len(e) != 1 || e[0].Start.Day() != 7 {
		t.Fatalf("changed feed not picked up: %v", e)
	}
}

// Any wind angle maps to a direction, negative and huge ones too (SMHI data is not ours to trust).
func TestCompass(t *testing.T) {
	for deg, want := range map[float64]string{0: "K", 90: "D", 359: "K", -90: "B", -400: "KB", 720: "K", 1e9: "B"} { // 1e9 mod 360 = 280
		if got := compass(deg); got != want {
			t.Errorf("compass(%v) = %s, want %s", deg, got, want)
		}
	}
}

// A feed that answers 200 with something that isn't a calendar keeps the last good copy.
func TestRefreshKeepsGoodFeed(t *testing.T) {
	f := t.TempDir() + "/a.ics"
	good := "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"
	os.WriteFile(f, []byte(good), 0o644)
	c := Calendar{Feeds: []string{"file://" + f}}
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(f, []byte("<html>please log in</html>"), 0o644)
	if err := c.Refresh(); err == nil {
		t.Error("a login page was accepted as a calendar")
	}
	if c.raw["file://"+f] != good {
		t.Error("the good copy was replaced")
	}
}

// A meeting that started before now but is still on stays in today's list.
func TestHorizonKeepsOngoing(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Stockholm")
	raw := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART;TZID=Europe/Stockholm:20261103T090000\r\n" +
		"DTEND;TZID=Europe/Stockholm:20261103T120000\r\nSUMMARY:Toplanti\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nDTSTART;TZID=Europe/Stockholm:20261103T080000\r\nDTEND;TZID=Europe/Stockholm:20261103T083000\r\nSUMMARY:Bitti\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	f := t.TempDir() + "/a.ics"
	os.WriteFile(f, []byte(raw), 0o644)
	a := &App{loc: loc, cal: Calendar{Feeds: []string{"file://" + f}}}
	a.cal.Refresh()
	days := a.horizon(time.Date(2026, 11, 3, 10, 0, 0, 0, loc), 1)
	if len(days[0].Evs) != 1 || days[0].Evs[0].Summary != "Toplanti" {
		t.Fatalf("today at 10:00: %+v", days[0].Evs)
	}
}

// A warning that has ended is dropped from a kept list.
func TestCurrentWarnings(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	ws := []Warning{{"RED", "a", now.Add(-time.Hour)}, {"YELLOW", "b", now.Add(time.Hour)}, {"ORANGE", "c", time.Time{}}}
	if got := current(ws, now); len(got) != 2 || got[0].Text != "b" || got[1].Text != "c" {
		t.Fatalf("got %+v", got)
	}
}

// A huge title is cut before measuring: drawing it stays fast.
func TestEllipsisHuge(t *testing.T) {
	start := time.Now()
	s := ellipsis(face(false, 22), strings.Repeat("ş", 200000), 300)
	if time.Since(start) > 2*time.Second || !strings.HasSuffix(s, "…") {
		t.Fatalf("took %v, %d runes", time.Since(start), len([]rune(s)))
	}
}

// A secret ICS link never reaches the log: calendar errors name the host only.
func TestCalendarErrorHidesLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer srv.Close()
	secret := srv.URL + "/calendar/ical/private-0123456789abcdef/basic.ics"
	c := Calendar{Feeds: []string{secret}}
	err := c.Refresh()
	if err == nil || strings.Contains(err.Error(), "private-0123456789abcdef") {
		t.Fatalf("error %v", err)
	}
}

func TestCalendarCardCopy(t *testing.T) {
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			http.Error(w, "down", 500)
			return
		}
		fmt.Fprint(w, "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n")
	}))
	defer srv.Close()
	u := srv.URL + "/secret.ics"
	defer os.Remove(feedCache(u))
	if err := (&Calendar{Feeds: []string{u}}).Refresh(); err != nil {
		t.Fatal(err)
	}
	up = false // a boot with the network not there yet: the card's copy, not an empty calendar
	c := &Calendar{Feeds: []string{u}}
	if err := c.Refresh(); err == nil {
		t.Fatal("no error from a failing feed")
	}
	if !strings.Contains(c.raw[u], "BEGIN:VCALENDAR") {
		t.Fatal("card copy not loaded")
	}
}
