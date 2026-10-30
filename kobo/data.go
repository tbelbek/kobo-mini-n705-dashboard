package main

// Data side, ported from server.py: Open-Meteo weather, threshold alerts and
// an ICS reader (DTSTART/DTEND/TZID/RRULE subset, multi-day spans, red days).

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // the Kobo has no reliable zoneinfo; embed it
)

var httpClient = &http.Client{Timeout: 25 * time.Second}

func get(u string) ([]byte, error) {
	if p, ok := strings.CutPrefix(u, "file://"); ok { // local feeds: demos, desk tests
		return os.ReadFile(p)
	}
	resp, err := httpClient.Get(strings.Replace(u, "webcal://", "https://", 1))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// ---------------------------------------------------------------- weather

type Place struct {
	Name     string
	Lat, Lon float64
	TZ       string
}

type Weather struct {
	Current struct {
		Temp float64 `json:"temperature_2m"`
		Code int     `json:"weather_code"`
	} `json:"current"`
	Hourly struct {
		Time    []string  `json:"time"`
		Temp    []float64 `json:"temperature_2m"`
		Feels   []float64 `json:"apparent_temperature"`
		Code    []int     `json:"weather_code"`
		Wind    []float64 `json:"wind_speed_10m"`
		WindDir []float64 `json:"wind_direction_10m"`
	} `json:"hourly"`
	Daily struct {
		Time    []string  `json:"time"`
		Code    []int     `json:"weather_code"`
		PSum    []float64 `json:"precipitation_sum"`
		WindMax []float64 `json:"wind_speed_10m_max"`
		TMin    []float64 `json:"temperature_2m_min"`
		TMax    []float64 `json:"temperature_2m_max"`
	} `json:"daily"`
}

func fetchWeather(p Place, hours, days int) (*Weather, error) {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(p.Lat, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(p.Lon, 'f', 4, 64))
	q.Set("timezone", p.TZ)
	q.Set("current", "temperature_2m,weather_code")
	if hours > 0 {
		q.Set("hourly", "temperature_2m,apparent_temperature,weather_code,wind_speed_10m,wind_direction_10m")
		q.Set("forecast_hours", strconv.Itoa(hours))
	}
	if days > 0 {
		q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum,wind_speed_10m_max")
		q.Set("forecast_days", strconv.Itoa(days))
	}
	b, err := get("https://api.open-meteo.com/v1/forecast?" + q.Encode())
	if err != nil {
		return nil, err
	}
	var w Weather
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

var wmo = map[int]string{
	0: "Açık", 1: "Çoğunlukla açık", 2: "Parçalı bulutlu", 3: "Kapalı",
	45: "Sisli", 48: "Kırağılı sis", 51: "Hafif çisenti", 53: "Çisenti", 55: "Yoğun çisenti",
	56: "Dondurucu çisenti", 57: "Yoğun dondurucu çisenti", 61: "Hafif yağmur", 63: "Yağmur",
	65: "Şiddetli yağmur", 66: "Dondurucu yağmur", 67: "Şiddetli dondurucu yağmur",
	71: "Hafif kar", 73: "Kar", 75: "Yoğun kar", 77: "Kar taneleri", 80: "Hafif sağanak",
	81: "Sağanak", 82: "Şiddetli sağanak", 85: "Hafif kar sağanağı", 86: "Kar sağanağı",
	95: "Gök gürültülü fırtına", 96: "Dolulu fırtına", 99: "Şiddetli dolulu fırtına",
}

func in(c int, set ...int) bool {
	for _, s := range set {
		if c == s {
			return true
		}
	}
	return false
}

// alerts: threshold heuristic for today+tomorrow.
// ponytail: Open-Meteo has no official warning feed; swap for SMHI if authoritative alerts matter.
func alerts(w *Weather) []string {
	d := w.Daily
	var out []string
	for i := 0; i < 2 && i < len(d.Time); i++ {
		when := "bugün"
		if i == 1 {
			when = "yarın"
		}
		code := at(d.Code, i)
		if in(code, 95, 96, 99) {
			out = append(out, when+": "+wmo[code])
		}
		if in(code, 71, 73, 75, 77, 85, 86) {
			out = append(out, when+": kar ("+wmo[code]+")")
		}
		if p := atf(d.PSum, i); p >= 20 {
			out = append(out, fmt.Sprintf("%s: şiddetli yağış (%g mm)", when, p))
		}
		if v := atf(d.WindMax, i); v >= 50 {
			out = append(out, fmt.Sprintf("%s: kuvvetli rüzgar (%d km/s)", when, int(math.Round(v))))
		}
		if v := atf(d.TMin, i); v <= -5 {
			out = append(out, fmt.Sprintf("%s: don riski (%d°)", when, int(math.Round(v))))
		}
		if v := atf(d.TMax, i); v >= 34 {
			out = append(out, fmt.Sprintf("%s: aşırı sıcak (%d°)", when, int(math.Round(v))))
		}
	}
	return out
}

// compass: 8-point Turkish wind direction the wind comes FROM (K=kuzey, D=doğu, G=güney, B=batı).
func compass(deg float64) string {
	return []string{"K", "KD", "D", "GD", "G", "GB", "B", "KB"}[int(math.Mod(deg+22.5+360, 360)/45)]
}

// ---------------------------------------------------------------- official warnings (SMHI)

type Warning struct {
	Level string // YELLOW | ORANGE | RED
	Text  string // reason, in Turkish when known
}

var warnLabel = map[string]string{"YELLOW": "SARI UYARI", "ORANGE": "TURUNCU UYARI", "RED": "KIRMIZI UYARI"}

// reasonTR maps SMHI's English event name to a short Turkish reason.
func reasonTR(en string) string {
	l := strings.ToLower(en)
	for _, m := range []struct{ key, tr string }{
		{"thunder", "gök gürültülü fırtına"}, {"wind", "kuvvetli rüzgar"}, {"snow", "yoğun kar"},
		{"rain", "şiddetli yağmur"}, {"fire", "yangın riski"}, {"temperature", "aşırı sıcak"},
		{"heat", "aşırı sıcak"}, {"cold", "aşırı soğuk"}, {"ice", "buzlanma"}, {"slipp", "buzlanma"},
		{"flood", "taşkın"}, {"high flow", "taşkın"}, {"water level", "yüksek su seviyesi"},
		{"shortage", "su kıtlığı"}, {"fog", "yoğun sis"},
	} {
		if strings.Contains(l, m.key) {
			return m.tr
		}
	}
	return en
}

// fetchWarnings returns SMHI's impact-based warnings (yellow and up) covering region,
// e.g. "Västra Götaland". Plain "MESSAGE" notices are skipped.
func fetchWarnings(region string) ([]Warning, error) {
	b, err := get("https://opendata-download-warnings.smhi.se/ibww/api/version/1/warning.json")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Event struct {
			En string `json:"en"`
		} `json:"event"`
		WarningAreas []struct {
			ApproximateEnd string `json:"approximateEnd"`
			WarningLevel   struct {
				Code string `json:"code"`
			} `json:"warningLevel"`
			EventDescription struct {
				Sv string `json:"sv"`
			} `json:"eventDescription"`
			AffectedAreas []struct {
				Sv string `json:"sv"`
			} `json:"affectedAreas"`
		} `json:"warningAreas"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	rank := map[string]int{"YELLOW": 1, "ORANGE": 2, "RED": 3}
	seen := map[string]bool{}
	var out []Warning
	for _, w := range raw {
		for _, a := range w.WarningAreas {
			if rank[a.WarningLevel.Code] == 0 {
				continue
			}
			if end, err := time.Parse(time.RFC3339, a.ApproximateEnd); err == nil && end.Before(time.Now()) {
				continue
			}
			hit := false
			for _, ar := range a.AffectedAreas {
				hit = hit || strings.Contains(ar.Sv, region)
			}
			reason := reasonTR(w.Event.En)
			key := a.WarningLevel.Code + reason
			if hit && !seen[key] {
				seen[key] = true
				out = append(out, Warning{a.WarningLevel.Code, reason})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Level] > rank[out[j].Level] })
	return out, nil
}

func at(s []int, i int) int {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func atf(s []float64, i int) float64 {
	if i < len(s) {
		return s[i]
	}
	return 0
}

// ---------------------------------------------------------------- calendar

type Event struct {
	Start   time.Time // display location; all-day events at midnight
	AllDay  bool
	Last    time.Time // last day the event covers (midnight)
	Summary string
	Loc     string
	Desc    string
	Tag     string // "cal", "red" (official red day) or "day" (observance)
}

func midnight(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

func (e Event) Day() time.Time { return midnight(e.Start, e.Start.Location()) }
func (e Event) Multi() bool    { return !e.Last.Equal(e.Day()) }

func daysBetween(a, b time.Time) int { return int(math.Round(b.Sub(a).Hours() / 24)) }

func unescapeICS(v string) string {
	r := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)
	return strings.TrimSpace(r.Replace(v))
}

func parseDT(v string, params map[string]string, loc *time.Location) (time.Time, bool, error) {
	v = strings.TrimSpace(v)
	if params["VALUE"] == "DATE" || (len(v) == 8 && !strings.Contains(v, "T")) {
		t, err := time.ParseInLocation("20060102", v, loc)
		return t, true, err
	}
	zone := loc
	if strings.HasSuffix(v, "Z") {
		v, zone = strings.TrimSuffix(v, "Z"), time.UTC
	} else if tz := params["TZID"]; tz != "" {
		if z, err := time.LoadLocation(tz); err == nil {
			zone = z
		}
	}
	t, err := time.ParseInLocation("20060102T150405", v, zone)
	if err != nil {
		t, err = time.ParseInLocation("20060102T1504", v, zone)
	}
	return t.In(loc), false, err
}

// spanDays: extra days a multi-day event covers after its start day (0 = one day).
func spanDays(start, end time.Time, allDay, hasEnd bool, loc *time.Location) int {
	if !hasEnd {
		return 0
	}
	sd := midnight(start, loc)
	var ed time.Time
	if allDay {
		ed = midnight(end, loc).AddDate(0, 0, -1) // DATE DTEND is exclusive
	} else {
		ed = midnight(end, loc)
		if end.Equal(ed) && ed.After(sd) {
			ed = ed.AddDate(0, 0, -1) // ends at midnight -> previous day
		}
	}
	if n := daysBetween(sd, ed); n > 0 {
		return n
	}
	return 0
}

func addMonthsClamp(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, t.Location())
	last := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, t.Hour(), t.Minute(), t.Second(), 0, t.Location())
}

// expand yields occurrence starts whose day is within [lo, hi].
// ponytail: FREQ DAILY/WEEKLY/MONTHLY/YEARLY + INTERVAL + COUNT/UNTIL only, no BYDAY.
func expand(base time.Time, rule map[string]string, lo, hi time.Time, loc *time.Location) []time.Time {
	interval, _ := strconv.Atoi(rule["INTERVAL"])
	if interval < 1 {
		interval = 1
	}
	count := -1
	if c, err := strconv.Atoi(rule["COUNT"]); err == nil {
		count = c
	}
	var until time.Time
	if u := rule["UNTIL"]; len(u) >= 8 {
		until, _ = time.ParseInLocation("20060102", u[:8], loc)
	}
	var out []time.Time
	cur := base
	for n := 0; n < 2000; n++ {
		d := midnight(cur, loc)
		if (!until.IsZero() && d.After(until)) || (count >= 0 && n >= count) || d.After(hi) {
			break
		}
		if !d.Before(lo) {
			out = append(out, cur)
		}
		switch rule["FREQ"] {
		case "DAILY":
			cur = cur.AddDate(0, 0, interval)
		case "WEEKLY":
			cur = cur.AddDate(0, 0, 7*interval)
		case "MONTHLY":
			cur = addMonthsClamp(cur, interval)
		case "YEARLY":
			cur = addMonthsClamp(cur, 12*interval)
		default:
			return out
		}
	}
	return out
}

// parseICS returns events overlapping the day window [lo, hi] (midnights in loc).
func parseICS(raw string, lo, hi time.Time, loc *time.Location) []Event {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
		} else {
			lines = append(lines, l)
		}
	}
	var out []Event
	var cur map[string]string
	var curParams map[string]map[string]string
	for _, l := range lines {
		switch {
		case l == "BEGIN:VEVENT":
			cur, curParams = map[string]string{}, map[string]map[string]string{}
		case l == "END:VEVENT" && cur != nil:
			out = append(out, buildEvents(cur, curParams, lo, hi, loc)...)
			cur = nil
		case cur != nil:
			i := strings.Index(l, ":")
			if i < 0 {
				continue
			}
			parts := strings.Split(l[:i], ";")
			key := strings.ToUpper(parts[0])
			params := map[string]string{}
			for _, p := range parts[1:] {
				if kv := strings.SplitN(p, "=", 2); len(kv) == 2 {
					params[strings.ToUpper(kv[0])] = kv[1]
				}
			}
			cur[key], curParams[key] = l[i+1:], params
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Start.Before(out[b].Start) })
	return out
}

func buildEvents(p map[string]string, params map[string]map[string]string, lo, hi time.Time, loc *time.Location) []Event {
	if _, ok := p["DTSTART"]; !ok {
		return nil
	}
	start, allDay, err := parseDT(p["DTSTART"], params["DTSTART"], loc)
	if err != nil {
		return nil
	}
	var end time.Time
	_, hasEnd := p["DTEND"]
	if hasEnd {
		var endAllDay bool
		if end, endAllDay, err = parseDT(p["DTEND"], params["DTEND"], loc); err != nil || endAllDay != allDay {
			hasEnd = false
		}
	}
	span := spanDays(start, end, allDay, hasEnd, loc)
	summary := unescapeICS(p["SUMMARY"])
	if summary == "" {
		summary = "(başlıksız)"
	}
	starts := []time.Time{start}
	if r, ok := p["RRULE"]; ok {
		rule := map[string]string{}
		for _, kv := range strings.Split(r, ";") {
			if x := strings.SplitN(kv, "=", 2); len(x) == 2 {
				rule[strings.ToUpper(x[0])] = x[1]
			}
		}
		starts = expand(start, rule, lo.AddDate(0, 0, -span), hi, loc)
	}
	var out []Event
	for _, s := range starts {
		d := midnight(s, loc)
		last := d.AddDate(0, 0, span)
		if !d.After(hi) && !last.Before(lo) {
			out = append(out, Event{Start: s, AllDay: allDay, Last: last, Summary: summary,
				Loc: unescapeICS(p["LOCATION"]), Desc: unescapeICS(p["DESCRIPTION"]), Tag: "cal"})
		}
	}
	return out
}

// Calendar keeps the raw feeds and caches them between refreshes.
type Calendar struct {
	Feeds   []string // personal feeds
	Holiday string   // Google's SE holiday feed ("Allmän helgdag" = red day)
	raw     map[string]string
}

func (c *Calendar) Refresh() error {
	if c.raw == nil {
		c.raw = map[string]string{}
	}
	var firstErr error
	for _, u := range append(append([]string{}, c.Feeds...), c.Holiday) {
		if u == "" {
			continue
		}
		b, err := get(u)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue // keep the previous copy of this feed
		}
		c.raw[u] = string(b)
	}
	return firstErr
}

func (c *Calendar) Events(lo, hi time.Time, loc *time.Location) []Event {
	var out []Event
	for u, raw := range c.raw {
		for _, e := range parseICS(raw, lo, hi, loc) {
			if u == c.Holiday {
				e.Tag = "day"
				if strings.Contains(e.Desc, "Allmän helgdag") {
					e.Tag = "red"
				}
			}
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Start.Before(out[b].Start) })
	return out
}
