package main

// Screen side: draws the dashboard, the month view and the day modal into a
// 600x800 grayscale image, and records tap targets (hits) while drawing.

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"net"
	"os"
	"strings"
	"time"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const W, H = 600, 800

var clock = time.Now // overridable with -now for desk renders

var (
	black = color.Gray{0}
	white = color.Gray{255}
	shade = color.Gray{0xBB} // red days; e-ink can't do red
)

var trDays = []string{"Paz", "Pzt", "Sal", "Çar", "Per", "Cum", "Cmt"} // indexed by time.Weekday
var trMonths = []string{"", "Ocak", "Şubat", "Mart", "Nisan", "Mayıs", "Haziran",
	"Temmuz", "Ağustos", "Eylül", "Ekim", "Kasım", "Aralık"}
var trDaysLong = []string{"Pazar", "Pazartesi", "Salı", "Çarşamba", "Perşembe", "Cuma", "Cumartesi"}
var gridDays = []string{"Pzt", "Sal", "Çar", "Per", "Cum", "Cmt", "Paz"}

// ---------------------------------------------------------------- text

var (
	fontReg, _  = opentype.Parse(goregular.TTF) // Go fonts cover WGL4, so ş/ğ/İ/ı render
	fontBold, _ = opentype.Parse(gobold.TTF)
	fontItal, _ = opentype.Parse(goitalic.TTF)
	faceCache   = map[string]font.Face{}
)

func face(bold bool, size float64) font.Face {
	key := fmt.Sprint(bold, size)
	if f, ok := faceCache[key]; ok {
		return f
	}
	src := fontReg
	if bold {
		src = fontBold
	}
	f, _ := opentype.NewFace(src, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	faceCache[key] = f
	return f
}

func text(dst draw.Image, x, baseline int, s string, f font.Face, c color.Color) int {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: f, Dot: fixed.P(x, baseline)}
	d.DrawString(s)
	return d.Dot.X.Round()
}

func faceItalic(size float64) font.Face {
	key := fmt.Sprint("italic", size)
	if f, ok := faceCache[key]; ok {
		return f
	}
	f, _ := opentype.NewFace(fontItal, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	faceCache[key] = f
	return f
}

func textW(f font.Face, s string) int { return font.MeasureString(f, s).Round() }

func ellipsis(f font.Face, s string, w int) string {
	if textW(f, s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && textW(f, string(r)+"…") > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

func wrap(f font.Face, s string, w int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			try := strings.TrimSpace(line + " " + word)
			if line != "" && textW(f, try) > w {
				out = append(out, line)
				line = word
			} else {
				line = try
			}
		}
		out = append(out, ellipsis(f, line, w))
	}
	return out
}

func short(s string) string { // first 3 runes, UTF-8 safe ("Şubat" -> "Şub")
	r := []rune(s)
	if len(r) > 3 {
		r = r[:3]
	}
	return string(r)
}

// ---------------------------------------------------------------- shapes

func fill(dst *image.Gray, r image.Rectangle, c color.Color) {
	draw.Draw(dst, r, image.NewUniform(c), image.Point{}, draw.Src)
}

func box(dst *image.Gray, r image.Rectangle, t int, c color.Color) {
	fill(dst, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+t), c)
	fill(dst, image.Rect(r.Min.X, r.Max.Y-t, r.Max.X, r.Max.Y), c)
	fill(dst, image.Rect(r.Min.X, r.Min.Y, r.Min.X+t, r.Max.Y), c)
	fill(dst, image.Rect(r.Max.X-t, r.Min.Y, r.Max.X, r.Max.Y), c)
}

func disc(dst *image.Gray, cx, cy, rad int, c color.Gray) {
	for y := -rad; y <= rad; y++ {
		for x := -rad; x <= rad; x++ {
			if x*x+y*y <= rad*rad {
				dst.SetGray(cx+x, cy+y, c)
			}
		}
	}
}

// ---------------------------------------------------------------- icons (same SVGs as server.py)

const cloudHi = `<path d="M7 14h10a4.2 4.2 0 0 0 .5-8.37 5.6 5.6 0 0 0-10.6-1.1A3.7 3.7 0 0 0 7 14z"/>`

var icons = map[string]string{
	"sun": `<circle cx="12" cy="12" r="5"/><g stroke="#000" stroke-width="2" stroke-linecap="round">` +
		`<line x1="12" y1="1" x2="12" y2="4"/><line x1="12" y1="20" x2="12" y2="23"/>` +
		`<line x1="1" y1="12" x2="4" y2="12"/><line x1="20" y1="12" x2="23" y2="12"/>` +
		`<line x1="4.2" y1="4.2" x2="6.3" y2="6.3"/><line x1="17.7" y1="17.7" x2="19.8" y2="19.8"/>` +
		`<line x1="19.8" y1="4.2" x2="17.7" y2="6.3"/><line x1="6.3" y1="17.7" x2="4.2" y2="19.8"/></g>`,
	"partly": `<circle cx="8" cy="8" r="3.4"/><g stroke="#000" stroke-width="1.6" stroke-linecap="round">` +
		`<line x1="8" y1="1.5" x2="8" y2="3"/><line x1="1.5" y1="8" x2="3" y2="8"/>` +
		`<line x1="3.6" y1="3.6" x2="4.7" y2="4.7"/><line x1="12.4" y1="3.6" x2="11.3" y2="4.7"/></g>` +
		`<path d="M9 20h8a3.6 3.6 0 0 0 .4-7.17 4.8 4.8 0 0 0-9.1-1A3.2 3.2 0 0 0 9 20z" stroke="#fff" stroke-width="1.2"/>`,
	"cloud": `<path d="M7 19h10a4.5 4.5 0 0 0 .6-8.96 6 6 0 0 0-11.3-1.2A4 4 0 0 0 7 19z"/>`,
	"fog": `<g stroke="#000" stroke-width="2.2" stroke-linecap="round"><line x1="3" y1="7" x2="21" y2="7"/>` +
		`<line x1="4" y1="11" x2="20" y2="11"/><line x1="3" y1="15" x2="21" y2="15"/><line x1="6" y1="19" x2="18" y2="19"/></g>`,
	"rain": cloudHi + `<g stroke="#000" stroke-width="2" stroke-linecap="round"><line x1="9" y1="17" x2="8" y2="21"/>` +
		`<line x1="13" y1="17" x2="12" y2="21"/><line x1="17" y1="17" x2="16" y2="21"/></g>`,
	"snow":  cloudHi + `<circle cx="9" cy="18.5" r="1.3"/><circle cx="13" cy="20.5" r="1.3"/><circle cx="16.5" cy="18.5" r="1.3"/>`,
	"storm": cloudHi + `<polygon points="12,15 8.5,20 11,20 9.5,24 15,18.5 12.3,18.5 14,15"/>`,
	"close": `<path d="M5 5L19 19M19 5L5 19" stroke="#000" stroke-width="4" stroke-linecap="round"/>`,
	"wifi":  wifiArcs,
	"wifi-off": wifiArcs + `<path d="M3 3L21 21" stroke="#fff" stroke-width="5" stroke-linecap="round"/>` +
		`<path d="M3 3L21 21" stroke="#000" stroke-width="2.6" stroke-linecap="round"/>`,
	"refresh": `<path d="M20 12a8 8 0 1 1-2.34-5.66" fill="none" stroke="#000" stroke-width="2.6" stroke-linecap="round"/>` +
		`<polygon points="21,2.5 21,10 13.5,10"/>`,
}

const wifiArcs = `<g fill="none" stroke="#000" stroke-width="2.4" stroke-linecap="round"><path d="M2 9a15 15 0 0 1 20 0"/>` +
	`<path d="M5.5 12.5a10 10 0 0 1 13 0"/><path d="M9 16a5 5 0 0 1 6 0"/></g><circle cx="12" cy="19.5" r="1.8"/>`

// online: the Kobo's Wi-Fi is eth0; connected means it holds an IPv4 address.
func online() bool {
	ifc, err := net.InterfaceByName("eth0")
	if err != nil {
		return false
	}
	addrs, _ := ifc.Addrs()
	for _, ad := range addrs {
		if ipn, ok := ad.(*net.IPNet); ok && ipn.IP.To4() != nil {
			return true
		}
	}
	return false
}

func wcat(code int) string {
	switch {
	case in(code, 0, 1):
		return "sun"
	case code == 2:
		return "partly"
	case in(code, 45, 48):
		return "fog"
	case in(code, 71, 73, 75, 77, 85, 86):
		return "snow"
	case in(code, 95, 96, 99):
		return "storm"
	case in(code, 51, 53, 55, 56, 57, 61, 63, 65, 66, 67, 80, 81, 82):
		return "rain"
	}
	return "cloud"
}

func icon(dst *image.Gray, name string, x, y, size int) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">` + icons[name] + `</svg>`
	ic, err := oksvg.ReadIconStream(strings.NewReader(svg))
	if err != nil {
		return
	}
	ic.SetTarget(0, 0, float64(size), float64(size))
	rgba := image.NewRGBA(image.Rect(0, 0, size, size))
	ic.Draw(rasterx.NewDasher(size, size, rasterx.NewScannerGV(size, size, rgba, rgba.Bounds())), 1)
	draw.Draw(dst, image.Rect(x, y, x+size, y+size), rgba, image.Point{}, draw.Over)
}

// ---------------------------------------------------------------- labels

func evTime(e Event) string {
	if e.Multi() {
		a, b := e.Day(), e.Last
		rng := fmt.Sprintf("%d %s–%d %s", a.Day(), short(trMonths[a.Month()]), b.Day(), short(trMonths[b.Month()]))
		if e.AllDay {
			return rng
		}
		return e.Start.Format("15:04") + " " + rng
	}
	if e.AllDay {
		return "tüm gün"
	}
	return e.Start.Format("15:04")
}

func title(e Event) string {
	if e.Tag == "red" {
		return e.Summary + " (kırmızı gün)"
	}
	return e.Summary
}

func cellLabel(e Event, day time.Time) string {
	if !e.AllDay && day.Equal(e.Day()) {
		return e.Start.Format("15:04") + " " + e.Summary
	}
	return e.Summary // red days are marked by the shaded cell
}

func dayLabel(d time.Time) string {
	return fmt.Sprintf("%d %s %s", d.Day(), trMonths[d.Month()], trDays[d.Weekday()])
}

func battery() string {
	b, err := os.ReadFile("/sys/class/power_supply/mc13892_bat/capacity")
	if err != nil {
		return ""
	}
	return "pil %" + strings.TrimSpace(string(b))
}

// ---------------------------------------------------------------- views

type hit struct {
	r  image.Rectangle
	fn func()
}

func (a *App) tap(r image.Rectangle, fn func()) { a.hits = append(a.hits, hit{r, fn}) }

func (a *App) render() *image.Gray {
	img := image.NewGray(image.Rect(0, 0, W, H))
	fill(img, img.Bounds(), white)
	a.hits = nil
	switch {
	case a.modal != nil:
		a.drawModal(img, *a.modal)
	case a.view == "month":
		a.drawMonth(img)
	default:
		a.drawDash(img)
	}
	return img
}

func (a *App) button(img *image.Gray, r image.Rectangle, label string, active bool, fn func()) {
	f := face(false, 22)
	fg := color.Color(black)
	if active {
		fill(img, r, black)
		fg = white
	} else {
		box(img, r, 2, black)
	}
	text(img, r.Min.X+(r.Dx()-textW(f, label))/2, r.Min.Y+r.Dy()/2+8, label, f, fg)
	a.tap(r, fn)
}

func (a *App) nav(img *image.Gray) {
	a.button(img, image.Rect(14, 14, 114, 62), "Pano", a.view == "dash", func() { a.view = "dash" })
	a.button(img, image.Rect(124, 14, 244, 62), "Takvim", a.view == "month", func() {
		a.view, a.month = "month", firstOfMonth(clock().In(a.loc))
	})

	// right side: Wi-Fi state · last update · refresh now
	ref := image.Rect(538, 14, 586, 62)
	box(img, ref, 2, black)
	icon(img, "refresh", ref.Min.X+9, ref.Min.Y+9, 30)
	a.tap(ref, func() { a.forceCal = true; a.refresh() })
	x := ref.Min.X - 6
	wifi := "wifi"
	a.shownOnline = online()
	if !a.shownOnline {
		wifi = "wifi-off"
	}
	wr := image.Rect(x-44, 14, x-4, 62)
	if a.wifiPinned {
		box(img, wr, 2, black) // framed = switched on by hand
	}
	icon(img, wifi, x-38, 24, 28)
	a.tap(wr, a.toggleWifi)
}

func (a *App) footer(img *image.Gray) {
	fill(img, image.Rect(14, 758, 586, 760), black)
	// big date on the left; no clock time, it would sit frozen on the panel while asleep
	now := clock().In(a.loc)
	text(img, 14, 788, fmt.Sprintf("%d %s %d, %s", now.Day(), trMonths[now.Month()], now.Year(), trDaysLong[now.Weekday()]),
		face(true, 24), black)

	// small status on the right
	var parts []string
	if b := battery(); b != "" {
		parts = append(parts, b)
	}
	next := a.nextWake // set when going to sleep; while awake, derive it from the last refresh
	if next.Before(a.updated) {
		next = a.updated.Add(time.Duration(a.cfg.RefreshMin) * time.Minute)
	}
	if a.cfg.Sleep && next.After(clock()) {
		parts = append(parts, "sonraki güncelleme "+next.In(a.loc).Format("15:04"))
	}
	if a.wErr != nil {
		parts = append(parts, "hava alınamadı")
	}
	fs := face(false, 14)
	s := strings.Join(parts, " · ")
	text(img, 586-textW(fs, s), 786, s, fs, black)
}

func firstOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

func round(v float64) int { return int(math.Round(v)) }

func (a *App) drawDash(img *image.Gray) {
	a.nav(img)
	y := 78

	// glance: each city with icon + current degree
	if a.wGlance != nil && a.wDetail != nil {
		fb := face(true, 28)
		type seg struct {
			s    string
			icon string
		}
		segs := []seg{{a.cfg.Places[0].Name + " ", ""}, {"", wcat(a.wGlance.Current.Code)},
			{fmt.Sprintf(" %d°", round(a.wGlance.Current.Temp)), ""}, {"   ·   ", ""},
			{a.cfg.Places[1].Name + " ", ""}, {"", wcat(a.wDetail.Current.Code)},
			{fmt.Sprintf(" %d°", round(a.wDetail.Current.Temp)), ""}}
		total := 0
		for _, s := range segs {
			if s.icon != "" {
				total += 34
			} else {
				total += textW(fb, s.s)
			}
		}
		x := (W - total) / 2
		for _, s := range segs {
			if s.icon != "" {
				icon(img, s.icon, x, y+6, 34)
				x += 34
			} else {
				x = text(img, x, y+34, s.s, fb, black)
			}
		}
	} else {
		text(img, 14, y+34, "Hava durumu alınamadı", face(true, 24), black)
	}
	y += 62

	// hourly strip
	if a.wDetail != nil {
		h := a.wDetail.Hourly
		n := len(h.Time)
		if n > 8 {
			n = 8
		}
		cw := 572 / 8
		for i := 0; i < n; i++ {
			cx := 14 + i*cw + cw/2
			lbl := h.Time[i]
			if len(lbl) >= 16 {
				lbl = lbl[11:16]
			}
			fs := face(false, 16)
			text(img, cx-textW(fs, lbl)/2, y+16, lbl, fs, black)
			icon(img, wcat(at(h.Code, i)), cx-14, y+22, 28)
			t := fmt.Sprintf("%d°", round(atf(h.Temp, i)))
			fb := face(true, 18)
			text(img, cx-textW(fb, t)/2, y+72, t, fb, black)
			fsm := face(false, 14)
			feel := fmt.Sprintf("his %d°", round(atf(h.Feels, i)))
			text(img, cx-textW(fsm, feel)/2, y+90, feel, fsm, black)
			wind := fmt.Sprintf("%s %d", compass(atf(h.WindDir, i)), round(atf(h.Wind, i)))
			text(img, cx-textW(fsm, wind)/2, y+107, wind, fsm, black)
		}
		text(img, 586-textW(face(false, 12), "rüzgar km/s"), y+122, "rüzgar km/s", face(false, 12), black)
		y += 130
	}
	// official SMHI warnings: "SARI UYARI — kuvvetli rüzgar"
	for _, w := range a.warnings {
		fill(img, image.Rect(14, y, 586, y+38), black)
		f := face(true, 19)
		text(img, 24, y+26, ellipsis(f, warnLabel[w.Level]+" — "+w.Text, 552), f, white)
		y += 46
	}

	// daily passage: laid out first so the calendar knows where to stop
	now := clock().In(a.loc)
	fq := faceItalic(17)
	quoteTop := 758
	var qLines []string
	var qAttr string
	if q, ok := quoteFor(now); ok {
		qLines = wrap(fq, "“"+q.Text+"”", 572)
		if len(qLines) > 3 {
			qLines = qLines[:3]
		}
		qAttr = "— " + q.Author + ", " + q.Work
		quoteTop = 744 - 20 - len(qLines)*22
	}

	// calendar: today through the end of this week (Sunday, Swedish/ISO weeks)
	y += 6
	today := midnight(now, a.loc)
	_, week := today.ISOWeek()
	end := today.AddDate(0, 0, (7-int(today.Weekday()))%7) // this Sunday
	head := fmt.Sprintf("Takvim · Hafta %d", week)
	sundayPM := today.Weekday() == time.Sunday && now.Hour() >= 12
	if sundayPM { // the week is nearly over: rest of today + all of next week
		end = today.AddDate(0, 0, 7)
		_, next := end.ISOWeek()
		head = fmt.Sprintf("Takvim · Hafta %d + %d", week, next)
	}
	text(img, 14, y+24, head, face(true, 23), black)
	fill(img, image.Rect(14, y+32, 586, y+34), black)
	y += 46
	var evs []Event
	for _, e := range a.cal.Events(today, end, a.loc) {
		if sundayPM && !e.AllDay && !e.Multi() && e.Day().Equal(today) && e.Start.Before(now) {
			continue // already happened today
		}
		evs = append(evs, e)
	}
	if len(evs) == 0 {
		text(img, 14, y+22, "Bu hafta başka etkinlik yok.", face(false, 19), black)
	}
	for i, e := range evs {
		if y > quoteTop-84 && len(evs)-i > 1 {
			text(img, 30, y+25, fmt.Sprintf("+%d etkinlik daha", len(evs)-i), face(false, 19), black)
			break
		}
		d := e.Day()
		if d.Before(today) {
			d = today // ongoing multi-day event
		}
		when := evTime(e)
		if !e.Multi() {
			when = fmt.Sprintf("%s %d %s %s", trDays[d.Weekday()], d.Day(), short(trMonths[d.Month()]), when)
		}
		fill(img, image.Rect(14, y, 20, y+34), black)
		fb, fr := face(true, 19), face(false, 19)
		x := text(img, 30, y+25, when, fb, black) + 10
		text(img, x, y+25, ellipsis(fr, title(e), 586-x), fr, black)
		day := d
		a.tap(image.Rect(14, y, 586, y+38), func() {
			a.view, a.month, a.modal = "month", firstOfMonth(day), &day
		})
		y += 42
	}

	if qLines != nil {
		fill(img, image.Rect(250, quoteTop, 350, quoteTop+1), black) // short rule above the passage
		qy := quoteTop
		for _, l := range qLines {
			qy += 22
			text(img, 14, qy, l, fq, black)
		}
		fa := face(false, 14)
		text(img, 586-textW(fa, qAttr), qy+20, ellipsis(fa, qAttr, 572), fa, black)
	}
	a.footer(img)
}

func (a *App) drawMonth(img *image.Gray) {
	a.nav(img)
	now := clock().In(a.loc)
	a.button(img, image.Rect(14, 72, 154, 116), "< Önceki", false, func() { a.month = a.month.AddDate(0, -1, 0) })
	a.button(img, image.Rect(164, 72, 264, 116), "Bugün", false, func() { a.month = firstOfMonth(now) })
	a.button(img, image.Rect(274, 72, 414, 116), "Sonraki >", false, func() { a.month = a.month.AddDate(0, 1, 0) })

	first := a.month
	text(img, 14, 158, fmt.Sprintf("%s %d", trMonths[first.Month()], first.Year()), face(true, 30), black)
	fill(img, image.Rect(14, 168, 586, 171), black)

	next := first.AddDate(0, 1, 0)
	byDay := map[time.Time][]Event{}
	for _, e := range a.cal.Events(first, next.AddDate(0, 0, -1), a.loc) {
		for d := e.Day(); !d.After(e.Last); d = d.AddDate(0, 0, 1) {
			if !d.Before(first) && d.Before(next) {
				byDay[d] = append(byDay[d], e)
			}
		}
	}

	x0, cw, top := 14, 82, 180
	fill(img, image.Rect(x0, top, x0+7*cw, top+34), black)
	for i, dn := range gridDays {
		f := face(true, 17)
		text(img, x0+i*cw+(cw-textW(f, dn))/2, top+23, dn, f, white)
	}
	offset := (int(first.Weekday()) + 6) % 7
	days := daysBetween(first, next)
	rows := (offset + days + 6) / 7
	ch := (752 - (top + 34)) / rows
	if ch > 100 {
		ch = 100
	}
	today := midnight(now, a.loc)
	for i := 0; i < rows*7; i++ {
		cx, cy := x0+(i%7)*cw, top+34+(i/7)*ch
		r := image.Rect(cx, cy, cx+cw+2, cy+ch+2)
		dayN := i - offset + 1
		if dayN < 1 || dayN > days {
			box(img, r, 2, black)
			continue
		}
		d := first.AddDate(0, 0, dayN-1)
		evs := byDay[d]
		for _, e := range evs {
			if e.Tag == "red" {
				fill(img, r, shade)
				break
			}
		}
		box(img, r, 2, black)
		num := fmt.Sprint(dayN)
		fb := face(true, 20)
		if d.Equal(today) {
			disc(img, cx+19, cy+18, 15, black)
			text(img, cx+19-textW(fb, num)/2, cy+25, num, fb, white)
		} else {
			text(img, cx+6, cy+23, num, fb, black)
		}
		fs := face(false, 14)
		ly := cy + 42
		for j, e := range evs {
			if j == 2 {
				text(img, cx+8, ly, fmt.Sprintf("+%d", len(evs)-2), fs, black)
				break
			}
			fill(img, image.Rect(cx+5, ly-12, cx+8, ly+3), black)
			text(img, cx+11, ly, ellipsis(fs, cellLabel(e, d), cw-14), fs, black)
			ly += 18
		}
		if len(evs) > 0 {
			day := d
			a.tap(r, func() { a.modal = &day })
		}
	}
	a.footer(img)
}

func (a *App) drawModal(img *image.Gray, day time.Time) {
	box(img, image.Rect(16, 16, 584, 784), 3, black)
	icon(img, "close", 538, 26, 32)
	a.tap(image.Rect(510, 16, 584, 80), func() { a.modal = nil })
	text(img, 32, 58, dayLabel(day), face(true, 24), black)
	fill(img, image.Rect(32, 70, 520, 72), black)
	y := 84
	for _, e := range a.cal.Events(day, day, a.loc) {
		start := y
		for _, l := range wrap(face(true, 20), evTime(e)+" — "+title(e), 520) {
			y += 26
			text(img, 44, y, l, face(true, 20), black)
		}
		var extra []string
		if e.Loc != "" {
			extra = append(extra, wrap(face(false, 18), "Yer: "+e.Loc, 520)...)
		}
		if e.Desc != "" && e.Tag == "cal" { // the holiday feed's text is Google boilerplate
			extra = append(extra, wrap(face(false, 18), e.Desc, 520)...)
		}
		for _, l := range extra {
			if y > 740 {
				break
			}
			y += 23
			text(img, 44, y, l, face(false, 18), black)
		}
		fill(img, image.Rect(32, start+6, 38, y+6), black)
		y += 18
		if y > 740 {
			break
		}
	}
}
