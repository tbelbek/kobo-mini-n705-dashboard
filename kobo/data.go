package main

// Data side: where the device is (geojs.io), SMHI weather and warnings, and
// an ICS reader (DTSTART/DTEND/TZID/RRULE subset, multi-day spans, red days).

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	_ "time/tzdata" // the Kobo has no reliable zoneinfo; embed it
)

var httpClient = &http.Client{Timeout: 25 * time.Second, Transport: answered{http.DefaultTransport}}

// answered notes that some server replied (any status): the network works, whatever SMHI
// or a feed is doing. refresh feeds that, not a route or one service, to the network ladder.
type answered struct{ http.RoundTripper }

var gotAnswer atomic.Bool

// CloseIdleConnections reaches the real transport (the embedded interface would hide it,
// and the pre-suspend cleanup would do nothing).
func (t answered) CloseIdleConnections() {
	if c, ok := t.RoundTripper.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

func (t answered) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.RoundTripper.RoundTrip(r)
	if err == nil {
		gotAnswer.Store(true)
	}
	return resp, err
}

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
		Temp float64
		Code int
	}
	Hourly struct {
		Time    []string
		Temp    []float64
		Feels   []float64
		Code    []int
		Wind    []float64
		WindDir []float64
	}
	Span   string  // "bugün" | "yarın": which day Lo/Hi cover; "" = no range
	Lo, Hi float64 // that day's min / max
}

// locate: the device's city from its public IP, with region ("Västra Götaland", for SMHI's
// warnings) and timezone. IP databases disagree about some ISPs (Göteborg one minute, Stockholm
// the next), so two independent ones must agree within 50 km, else it is an error and the
// caller keeps what it had.
func locate() (Place, string, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	fetch := func(u string, v any) error {
		resp, err := c.Get(u)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(v)
	}
	var g struct{ City, Region, Timezone, Latitude, Longitude string } // geojs: numbers as strings
	var w struct {
		Success             bool
		Latitude, Longitude float64
	}
	var ge, we error // both at once: two round trips of radio time become one
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); ge = fetch("https://get.geojs.io/v1/ip/geo.json", &g) }()
	go func() { defer wg.Done(); we = fetch("https://ipwho.is/?fields=success,latitude,longitude", &w) }()
	wg.Wait()
	if err := cmp.Or(ge, we); err != nil {
		return Place{}, "", err
	}
	lat, e1 := strconv.ParseFloat(g.Latitude, 64)
	lon, e2 := strconv.ParseFloat(g.Longitude, 64)
	if g.City == "" || e1 != nil || e2 != nil || !w.Success {
		return Place{}, "", fmt.Errorf("incomplete answer")
	}
	p := Place{g.City, lat, lon, g.Timezone}
	if d := km(p, Place{Lat: w.Latitude, Lon: w.Longitude}); d > 50 {
		return Place{}, "", fmt.Errorf("geojs says %s, ipwho.is %.0f km away", g.City, d)
	}
	return p, strings.TrimSuffix(g.Region, " County"), nil
}

// km between two places (haversine; good enough at city scale)
func km(a, b Place) float64 {
	const rad = math.Pi / 180
	dLat, dLon := (b.Lat-a.Lat)*rad, (b.Lon-a.Lon)*rad
	h := math.Pow(math.Sin(dLat/2), 2) + math.Cos(a.Lat*rad)*math.Cos(b.Lat*rad)*math.Pow(math.Sin(dLon/2), 2)
	return 2 * 6371 * math.Asin(math.Sqrt(h))
}

// smhiWMO maps SMHI's symbol_code (1-27) onto the WMO codes the icons and labels use.
var smhiWMO = [28]int{0, 0, 1, 2, 2, 3, 3, 45, 80, 81, 82, 95, 66, 66, 67, 85, 86, 86, 61, 63, 65, 95, 66, 66, 67, 71, 73, 75}

// fetchSMHI: Göteborg's hourly forecast from SMHI (snow1g point forecast), the next `hours` hours,
// plus the min/max of the rest of today, or of tomorrow once the evening starts (rangeFrom).
// SMHI gives no feels-like, so it is the Steadman apparent temperature, the formula Open-Meteo uses.
func fetchSMHI(p Place, hours int) (*Weather, error) {
	b, err := get(fmt.Sprintf("https://opendata-download-metfcst.smhi.se/api/category/snow1g/version/1/geotype/point/lon/%.4f/lat/%.4f/data.json", p.Lon, p.Lat))
	if err != nil {
		return nil, err
	}
	var r struct {
		TimeSeries []struct {
			Time time.Time `json:"time"`
			Data struct {
				Temp   *float64 `json:"air_temperature"` // nil: missing, not 0 °C
				Wind   float64  `json:"wind_speed"`      // m/s
				Dir    float64  `json:"wind_from_direction"`
				RH     float64  `json:"relative_humidity"`
				Symbol int      `json:"symbol_code"`
			} `json:"data"`
		} `json:"timeSeries"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(p.TZ)
	if err != nil {
		return nil, err
	}
	var w Weather
	h := &w.Hourly
	now := clock()
	from := now.Truncate(time.Hour)
	day := now.In(loc)
	w.Span = "bugün"
	if day.Hour() >= rangeFrom {
		w.Span, day = "yarın", day.AddDate(0, 0, 1)
	}
	ymd := day.Format("2006-01-02")
	n := 0
	for _, t := range r.TimeSeries {
		if t.Time.Before(from) || t.Data.Temp == nil {
			continue
		}
		temp := *t.Data.Temp
		if len(h.Time) == 0 && t.Time.Sub(now) > 3*time.Hour { // "now" from a forecast hours ahead
			return nil, fmt.Errorf("smhi: forecast starts %s", t.Time.Format("01-02 15:04"))
		}
		if t.Time.In(loc).Format("2006-01-02") == ymd {
			if n == 0 || temp < w.Lo {
				w.Lo = temp
			}
			if n == 0 || temp > w.Hi {
				w.Hi = temp
			}
			n++
		}
		if len(h.Time) == hours {
			continue
		}
		d := t.Data
		code := 3
		if d.Symbol > 0 && d.Symbol < len(smhiWMO) {
			code = smhiWMO[d.Symbol]
		}
		e := d.RH / 100 * 6.105 * math.Exp(17.27*temp/(237.7+temp))
		h.Time = append(h.Time, t.Time.In(loc).Format("2006-01-02T15:04"))
		h.Temp = append(h.Temp, temp)
		h.Feels = append(h.Feels, temp+0.33*e-0.70*d.Wind-4.00)
		h.Code = append(h.Code, code)
		h.Wind = append(h.Wind, d.Wind*3.6)
		h.WindDir = append(h.WindDir, d.Dir)
	}
	if len(h.Time) == 0 {
		return nil, fmt.Errorf("smhi: no forecast hours")
	}
	if n == 0 {
		w.Span = ""
	}
	w.Current.Temp, w.Current.Code = h.Temp[0], h.Code[0]
	return &w, nil
}

func in(c int, set ...int) bool {
	for _, s := range set {
		if c == s {
			return true
		}
	}
	return false
}

// rangeFrom: from this hour on, the rail's day range is tomorrow's (the rest of today is a few hours).
const rangeFrom = 17

// sunTimes: sunrise and sunset on day's calendar date (NOAA sunrise equation, ~1 min).
// ok is false in polar day/night.
func sunTimes(lat, lon float64, day time.Time) (rise, set time.Time, ok bool) {
	rad := math.Pi / 180
	noon := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, time.UTC)
	j := float64(noon.Unix())/86400 + 2440587.5 - 2451545.0 + 0.0008 - lon/360
	m := math.Mod(357.5291+0.98560028*j, 360)
	c := 1.9148*math.Sin(m*rad) + 0.02*math.Sin(2*m*rad) + 0.0003*math.Sin(3*m*rad)
	l := math.Mod(m+c+180+102.9372, 360)
	transit := 2451545.0 + j + 0.0053*math.Sin(m*rad) - 0.0069*math.Sin(2*l*rad)
	decl := math.Asin(math.Sin(l*rad) * math.Sin(23.4397*rad))
	cosW := (math.Sin(-0.833*rad) - math.Sin(lat*rad)*math.Sin(decl)) / (math.Cos(lat*rad) * math.Cos(decl))
	if cosW < -1 || cosW > 1 {
		return time.Time{}, time.Time{}, false
	}
	w := math.Acos(cosW) / rad / 360
	jd := func(x float64) time.Time {
		return time.Unix(int64(math.Round((x-2440587.5)*86400)), 0).In(day.Location())
	}
	return jd(transit - w), jd(transit + w), true
}

// compass: 8-point Turkish wind direction the wind comes FROM (K=kuzey, D=doğu, G=güney, B=batı).
func compass(deg float64) string {
	a := math.Mod(math.Mod(deg+22.5, 360)+360, 360) // any angle, negative too
	return []string{"K", "KD", "D", "GD", "G", "GB", "B", "KB"}[int(a/45)%8]
}

// ---------------------------------------------------------------- official warnings (SMHI)

type Warning struct {
	Level string    // YELLOW | ORANGE | RED
	Text  string    // reason, in Turkish when known
	End   time.Time // SMHI's approximate end; zero = open-ended
}

// current drops the warnings that have ended: a list kept through failed fetches must not
// show a storm that was over days ago.
func current(ws []Warning, now time.Time) []Warning {
	var out []Warning
	for _, w := range ws {
		if w.End.IsZero() || w.End.After(now) {
			out = append(out, w)
		}
	}
	return out
}

var warnLabel = map[string]string{"YELLOW": "Sarı", "ORANGE": "Turuncu", "RED": "Kırmızı"}

// warnReasons: SMHI's English event name -> Turkish reason, and the bare noun for a crowded line
// (the level already says how bad).
var warnReasons = []struct{ key, tr, short string }{
	{"thunder", "gök gürültülü fırtına", "fırtına"}, {"wind", "kuvvetli rüzgar", "rüzgar"}, {"snow", "yoğun kar", "kar"},
	{"rain", "şiddetli yağmur", "yağmur"}, {"fire", "yangın riski", "yangın"}, {"temperature", "aşırı sıcak", "sıcak"},
	{"heat", "aşırı sıcak", "sıcak"}, {"cold", "aşırı soğuk", "soğuk"}, {"ice", "buzlanma", "buzlanma"}, {"slipp", "buzlanma", "buzlanma"},
	{"flood", "taşkın", "taşkın"}, {"high flow", "taşkın", "taşkın"}, {"water level", "yüksek su seviyesi", "su seviyesi"},
	{"shortage", "su kıtlığı", "su kıtlığı"}, {"fog", "yoğun sis", "sis"},
}

// reasonTR maps SMHI's English event name to a short Turkish reason.
func reasonTR(en string) string {
	l := strings.ToLower(en)
	for _, m := range warnReasons {
		if strings.Contains(l, m.key) {
			return m.tr
		}
	}
	return en
}

// shortReason: "şiddetli yağmur" -> "yağmur"; unknown text unchanged.
func shortReason(tr string) string {
	for _, m := range warnReasons {
		if m.tr == tr {
			return m.short
		}
	}
	return tr
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
			end, err := time.Parse(time.RFC3339, a.ApproximateEnd)
			if err != nil {
				end = time.Time{}
			} else if end.Before(time.Now()) {
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
				out = append(out, Warning{a.WarningLevel.Code, reason, end})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Level] > rank[out[j].Level] })
	return out, nil
}

// at: s[i], or the zero value past the end
func at[T any](s []T, i int) (v T) {
	if i < len(s) {
		v = s[i]
	}
	return
}

// ---------------------------------------------------------------- calendar

type Event struct {
	Start   time.Time // display location; all-day events at midnight
	AllDay  bool
	Last    time.Time // last day the event covers (midnight)
	End     time.Time // when a timed event ends (its start if it gave none)
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
	return t, false, err // in its own zone: a recurrence repeats on that zone's clock
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

var weekdays = map[string]time.Weekday{"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday,
	"WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday}

// monthDays: the days of month m (1-based) a MONTHLY/YEARLY rule picks: BYDAY ("1MO",
// "-1FR", or "TU" for every Tuesday), else BYMONTHDAY, else the start's own day.
// A day the month doesn't have is skipped, not moved (31 Feb is no date).
func monthDays(y int, m time.Month, rule map[string]string, base time.Time) []int {
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
	var out []int
	var monthDay map[int]bool // BYMONTHDAY with BYDAY: only the days in both
	if md := rule["BYMONTHDAY"]; md != "" && rule["BYDAY"] != "" {
		monthDay = map[int]bool{}
		for _, d := range monthDays(y, m, map[string]string{"BYMONTHDAY": md}, base) {
			monthDay[d] = true
		}
	}
	if bd := rule["BYDAY"]; bd != "" {
		for _, v := range strings.Split(bd, ",") {
			wd, ok := weekdays[strings.ToUpper(v[max(len(v)-2, 0):])]
			if !ok {
				continue
			}
			var all []int
			for d := 1; d <= last; d++ {
				if time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Weekday() == wd {
					all = append(all, d)
				}
			}
			n, err := strconv.Atoi(v[:len(v)-2])
			switch {
			case len(v) == 2 || err != nil || n == 0:
				out = append(out, all...)
			case n > 0 && n <= len(all):
				out = append(out, all[n-1])
			case n < 0 && n >= -len(all): // not -n: -MinInt overflows
				out = append(out, all[len(all)+n])
			}
		}
		if monthDay != nil {
			kept := out[:0]
			for _, d := range out {
				if monthDay[d] {
					kept = append(kept, d)
				}
			}
			out = kept
		}
	} else if md := rule["BYMONTHDAY"]; md != "" {
		for _, v := range strings.Split(md, ",") {
			if n, err := strconv.Atoi(v); err == nil && n != 0 {
				if n < 0 {
					n = last + 1 + n
				}
				if n >= 1 && n <= last {
					out = append(out, n)
				}
			}
		}
	} else if base.Day() <= last {
		out = []int{base.Day()}
	}
	sort.Ints(out)
	return out
}

// expand yields occurrence starts whose day (in loc) is within [lo, hi]. It walks the rule
// period by period (day, week, month, year) in the start's own zone, so a 09:00 UTC series
// stays 09:00 UTC across DST and a local one stays on local time.
// ponytail: FREQ DAILY/WEEKLY/MONTHLY/YEARLY, INTERVAL, COUNT, UNTIL, BYDAY, BYMONTHDAY,
// BYMONTH (yearly); no BYSETPOS/BYWEEKNO/BYYEARDAY, week start fixed to Monday.
func expand(base time.Time, rule map[string]string, lo, hi time.Time, loc *time.Location) []time.Time {
	interval, _ := strconv.Atoi(rule["INTERVAL"])
	if interval < 1 {
		interval = 1
	}
	count := -1
	if c, err := strconv.Atoi(rule["COUNT"]); err == nil {
		count = c
	}
	// UNTIL: a date (that whole day counts) or a date-time, UTC with Z, else in the start's zone
	var until time.Time
	if u := rule["UNTIL"]; len(u) >= 15 {
		z := base.Location()
		if strings.HasSuffix(u, "Z") {
			z = time.UTC
		}
		until, _ = time.ParseInLocation("20060102T150405", strings.TrimSuffix(u, "Z"), z)
	} else if len(u) >= 8 {
		if d, err := time.ParseInLocation("20060102", u[:8], loc); err == nil {
			until = d.AddDate(0, 0, 1).Add(-time.Second)
		}
	}
	z := base.Location()
	at := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, base.Hour(), base.Minute(), base.Second(), 0, z)
	}
	by, bm, bd := base.Date()
	ly, lm, _ := lo.In(z).Date()
	// period k: its first day (to stop past hi) and its occurrences in order
	var period func(k int) (time.Time, []time.Time)
	skip := 0 // periods wholly before lo; jumped over when nothing is counted
	switch rule["FREQ"] {
	case "DAILY":
		days := map[time.Weekday]bool{} // BYDAY on a daily rule: only those weekdays ("every weekday")
		for _, v := range strings.Split(rule["BYDAY"], ",") {
			if wd, ok := weekdays[strings.ToUpper(strings.TrimSpace(v))]; ok {
				days[wd] = true
			}
		}
		period = func(k int) (time.Time, []time.Time) {
			d := at(by, bm, bd+k*interval)
			if len(days) > 0 && !days[d.Weekday()] {
				return d, nil
			}
			return d, []time.Time{d}
		}
		skip = daysBetween(midnight(base, loc), lo) / interval
	case "WEEKLY":
		monday := bd - (int(base.Weekday())+6)%7
		var offs []int
		for _, v := range strings.Split(rule["BYDAY"], ",") {
			if wd, ok := weekdays[strings.ToUpper(strings.TrimSpace(v))]; ok {
				offs = append(offs, (int(wd)+6)%7)
			}
		}
		if len(offs) == 0 {
			offs = []int{(int(base.Weekday()) + 6) % 7}
		}
		sort.Ints(offs)
		period = func(k int) (time.Time, []time.Time) {
			var out []time.Time
			for _, o := range offs {
				out = append(out, at(by, bm, monday+7*k*interval+o))
			}
			return at(by, bm, monday+7*k*interval), out
		}
		skip = daysBetween(midnight(base, loc), lo) / (7 * interval)
	case "MONTHLY":
		period = func(k int) (time.Time, []time.Time) {
			first := time.Date(by, bm+time.Month(k*interval), 1, 0, 0, 0, 0, z) // month overflow rolls the year
			var out []time.Time
			for _, d := range monthDays(first.Year(), first.Month(), rule, base) {
				out = append(out, at(first.Year(), first.Month(), d))
			}
			return first, out
		}
		skip = ((ly-by)*12 + int(lm-bm)) / interval
	case "YEARLY":
		months := []time.Month{bm}
		if v := rule["BYMONTH"]; v != "" {
			months = nil
			for _, x := range strings.Split(v, ",") {
				if n, err := strconv.Atoi(x); err == nil && n >= 1 && n <= 12 {
					months = append(months, time.Month(n))
				}
			}
			sort.Slice(months, func(i, j int) bool { return months[i] < months[j] })
		}
		period = func(k int) (time.Time, []time.Time) {
			y := by + k*interval
			var out []time.Time
			for _, m := range months {
				for _, d := range monthDays(y, m, rule, base) {
					out = append(out, at(y, m, d))
				}
			}
			return time.Date(y, 1, 1, 0, 0, 0, 0, z), out
		}
		skip = (ly - by) / interval
	default:
		return []time.Time{base}
	}
	k := 0
	if count < 0 && skip > 1 {
		k = skip - 1 // one period of slack: lo may fall mid-period
	}
	var out []time.Time
	n := 0
	// bounded: a window is months, not thousands of periods (a counted series is walked from its
	// start, so it gets as many periods as it counts)
	for limit := k + max(5000, min(count, 200000)); k < limit; k++ {
		pstart, occ := period(k)
		if midnight(pstart, loc).After(hi) {
			break
		}
		for _, o := range occ {
			if o.Before(base) {
				continue
			}
			d := midnight(o, loc)
			if (!until.IsZero() && o.After(until)) || (count >= 0 && n >= count) || d.After(hi) {
				return out
			}
			n++
			if !d.Before(lo) {
				out = append(out, o)
			}
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
	type vevent struct {
		p      map[string]string
		params map[string]map[string]string
	}
	var evs []vevent
	var cur map[string]string
	var curParams map[string]map[string]string
	for _, l := range lines {
		switch {
		case l == "BEGIN:VEVENT":
			cur, curParams = map[string]string{}, map[string]map[string]string{}
		case l == "END:VEVENT" && cur != nil:
			evs = append(evs, vevent{cur, curParams})
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
			v := l[i+1:]
			if key == "EXDATE" && cur[key] != "" { // several EXDATE lines: keep them all
				v = cur[key] + "," + v
			}
			cur[key], curParams[key] = v, params
		}
	}
	// A moved or cancelled single occurrence comes as its own VEVENT with the series' UID
	// and RECURRENCE-ID = the original start; the series must then skip that start.
	moved := map[string]map[time.Time]bool{}
	for _, e := range evs {
		if rid, ok := e.p["RECURRENCE-ID"]; ok {
			if t, _, err := parseDT(rid, e.params["RECURRENCE-ID"], loc); err == nil {
				if moved[e.p["UID"]] == nil {
					moved[e.p["UID"]] = map[time.Time]bool{}
				}
				moved[e.p["UID"]][t.UTC()] = true
			}
		}
	}
	var out []Event
	for _, e := range evs {
		var skip map[time.Time]bool
		if _, ok := e.p["RECURRENCE-ID"]; !ok {
			skip = moved[e.p["UID"]]
		}
		out = append(out, buildEvents(e.p, e.params, lo, hi, loc, skip)...)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Start.Before(out[b].Start) })
	return out
}

func buildEvents(p map[string]string, params map[string]map[string]string, lo, hi time.Time, loc *time.Location, moved map[time.Time]bool) []Event {
	if _, ok := p["DTSTART"]; !ok || strings.EqualFold(p["STATUS"], "CANCELLED") {
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
	var dur time.Duration // carried to every occurrence
	if hasEnd && !allDay && end.After(start) {
		dur = end.Sub(start)
	}
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
	if ex := p["EXDATE"]; ex != "" || len(moved) > 0 { // occurrences deleted from, or moved out of, a series
		skip := map[time.Time]bool{}
		for t := range moved {
			skip[t] = true
		}
		for _, v := range strings.Split(ex, ",") {
			if v == "" {
				continue
			}
			if t, _, err := parseDT(v, params["EXDATE"], loc); err == nil {
				skip[t.UTC()] = true
			}
		}
		kept := starts[:0]
		for _, s := range starts {
			if !skip[s.UTC()] {
				kept = append(kept, s)
			}
		}
		starts = kept
	}
	var out []Event
	for _, s := range starts {
		d := midnight(s, loc)
		last := d.AddDate(0, 0, span)
		if !d.After(hi) && !last.Before(lo) {
			out = append(out, Event{Start: s.In(loc), End: s.Add(dur).In(loc), AllDay: allDay, Last: last, Summary: summary,
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
	// the last Events answer: every frame asks for the same days, and parsing the
	// feeds again costs CPU time (battery) on each one
	memo map[string][]Event // per day range asked
}

func (c *Calendar) Refresh() error {
	if c.raw == nil {
		c.raw = map[string]string{}
	}
	// side by side: every second here is a second of Wi-Fi radio
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	for _, u := range append(append([]string{}, c.Feeds...), c.Holiday) {
		if u == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := get(u)
			if err == nil && !strings.Contains(string(b[:min(len(b), 4096)]), "BEGIN:VCALENDAR") {
				err = fmt.Errorf("%s: not a calendar (%d bytes)", u, len(b)) // a login page or an empty answer must not wipe it
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return // keep the previous copy of this feed
			}
			if c.raw[u] != string(b) {
				c.raw[u], c.memo = string(b), nil
			}
		}()
	}
	wg.Wait()
	beat()
	return firstErr
}

func (c *Calendar) Events(lo, hi time.Time, loc *time.Location) []Event {
	key := lo.String() + hi.String() + loc.String()
	if e, ok := c.memo[key]; ok {
		return e
	}
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
	if c.memo == nil || len(c.memo) > 8 { // the screen asks for two ranges; days roll over
		c.memo = map[string][]Event{}
	}
	c.memo[key] = out
	return out
}
