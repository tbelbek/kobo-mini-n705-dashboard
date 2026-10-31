package main

// Screen side: draws the dashboard and the month view into a
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
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/gofont/gomediumitalic"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	W, H   = 800, 600 // logical canvas: the reader hangs in landscape
	PW, PH = 600, 800 // the panel itself (framebuffer and touch), portrait
)

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
	fontReg, _  = opentype.Parse(gomedium.TTF) // Medium, not Regular: thin strokes look faint on e-ink; Go fonts cover WGL4 (ş/ğ/İ/ı)
	fontBold, _ = opentype.Parse(gobold.TTF)
	fontItal, _ = opentype.Parse(gomediumitalic.TTF)
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
	"warn": `<path d="M12 2.5L22.5 21h-21z" fill="none" stroke="#000" stroke-width="2.4" stroke-linejoin="round"/><path d="M12 9v6" stroke="#000" stroke-width="2.6" stroke-linecap="round"/><circle cx="12" cy="18.2" r="1.5"/>`,
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

var fakeBattery = "" // set by -fakewarn for desk renders

func battery() string {
	b, err := os.ReadFile("/sys/class/power_supply/mc13892_bat/capacity")
	if err != nil {
		return fakeBattery
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
	case a.view == "month":
		a.drawMonth(img)
	default:
		a.drawDash(img)
	}
	return img
}

// panelFrame turns the landscape canvas into the portrait framebuffer the panel expects.
// "cw" rotates it a quarter turn clockwise, "ccw" the other way (which one depends on how
// the reader hangs on the fridge); toLogical is the inverse for taps.
func panelFrame(src *image.Gray, rot string) *image.Gray {
	dst := image.NewGray(image.Rect(0, 0, PW, PH))
	for ly := 0; ly < H; ly++ {
		for lx := 0; lx < W; lx++ {
			px, py := H-1-ly, lx
			if rot == "ccw" {
				px, py = ly, W-1-lx
			}
			dst.Pix[py*dst.Stride+px] = src.Pix[ly*src.Stride+lx]
		}
	}
	return dst
}

func toLogical(p image.Point, rot string) image.Point {
	if rot == "ccw" {
		return image.Pt(W-1-p.Y, p.X)
	}
	return image.Pt(p.Y, H-1-p.X)
}

func (a *App) button(img *image.Gray, r image.Rectangle, label string, active bool, fn func()) {
	f := face(false, tSmall)
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

// nav is the header every view shares: tabs · date + ISO week · next update/battery, Wi-Fi, refresh.
func (a *App) nav(img *image.Gray) {
	now := clock().In(a.loc)
	a.button(img, image.Rect(mx, 4, 104, 52), "Pano", a.view != "month", func() { a.view = "dash" })
	a.button(img, image.Rect(112, 4, 216, 52), "Takvim", a.view == "month", func() { a.openDay(midnight(now, a.loc)) })

	ref := image.Rect(W-64, 4, W-mx, 52)
	if a.busy {
		fill(img, ref, black) // pressed: instant feedback, the fetch below can take a while
	} else {
		box(img, ref, 2, black)
		icon(img, "refresh", ref.Min.X+9, ref.Min.Y+9, 30)
	}
	// touch targets reach the screen edge: the panel reads edge taps a few px off
	a.tap(image.Rect(W-72, 0, W, 64), func() {
		a.busy = true
		a.show()
		a.forceCal, a.wantUpdate = true, true
		a.refresh()
		a.busy = false
	})
	wr := image.Rect(W-120, 4, W-72, 52)
	wifi := "wifi"
	a.shownOnline = online()
	if !a.shownOnline {
		wifi = "wifi-off"
	}
	if a.wifiPinned {
		box(img, wr, 2, black) // framed = switched on by hand
	}
	icon(img, wifi, wr.Min.X+10, 14, 28)
	a.tap(image.Rect(wr.Min.X-8, 0, W-72, 64), a.toggleWifi)

	// next update and battery: small, stacked, left of the Wi-Fi icon
	var parts []string
	next := a.nextWake // set when going to sleep; while awake, derive it from the last refresh
	if next.Before(a.updated) {
		next = a.updated.Add(time.Duration(a.cfg.RefreshMin) * time.Minute)
	}
	if a.cfg.Sleep && next.After(clock()) {
		parts = append(parts, next.In(a.loc).Format("15:04"))
	}
	if b := battery(); b != "" {
		parts = append(parts, b)
	}
	fs, stW := face(false, tTiny), 0
	for i, p := range parts[:min(len(parts), 2)] {
		text(img, wr.Min.X-8-textW(fs, p), 24+i*21, p, fs, black)
		stW = max(stW, textW(fs, p))
	}

	// date without the year, ISO week as "W44" (Sunday afternoon the calendar already shows next week)
	_, week := now.ISOWeek()
	wk := fmt.Sprintf("  ·  W%d", week)
	if now.Weekday() == time.Sunday && now.Hour() >= 12 {
		_, nw := now.AddDate(0, 0, 7).ISOWeek()
		wk = fmt.Sprintf("  ·  W%d+%d", week, nw)
	}
	date := fmt.Sprintf("%d %s, %s", now.Day(), trMonths[now.Month()], trDaysLong[now.Weekday()])
	room := wr.Min.X - 8 - stW - 16 - 232
	for _, sz := range []float64{28, 26, 24, 22} {
		if fb, fr := face(true, sz), face(false, sz); textW(fb, date)+textW(fr, wk) <= room {
			x := text(img, 232, 39, date, fb, black)
			text(img, x, 39, wk, fr, black)
			break
		}
	}
	fill(img, image.Rect(0, 56, W, 58), black)
}

func firstOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

func round(v float64) int { return int(math.Round(v)) }

func centerText(img *image.Gray, s string, f font.Face, cx, base int) {
	text(img, cx-textW(f, s)/2, base, s, f, black)
}

func hourLabel(h string) string {
	if len(h) >= 16 {
		return h[11:16]
	}
	return h
}

// weekEvents: today through Sunday (Sunday afternoon: through next Sunday); today's past timed events dropped.
func (a *App) weekEvents(now time.Time) (today time.Time, evs []Event) {
	today = midnight(now, a.loc)
	end := today.AddDate(0, 0, (7-int(today.Weekday()))%7)
	if today.Weekday() == time.Sunday && now.Hour() >= 12 {
		end = today.AddDate(0, 0, 7)
	}
	for _, e := range a.cal.Events(today, end, a.loc) {
		if !e.AllDay && !e.Multi() && e.Day().Equal(today) && e.Start.Before(now) {
			continue
		}
		evs = append(evs, e)
	}
	return today, evs
}

// Layout for a fridge in landscape, sized by viewing distance (1 px = 0.127 mm, Go cap height = 0.72 em):
//
//	hero  140 px  Göteborg now, read from 2 m (cap 101 px = 22 arcmin; ISO 9241-303 min 16', preferred 20-22')
//	large  52 px  temperatures, read from 1 m (cap 37 px = 16')
//	mid    30 px  titles, city names: 1 m at road-sign legibility (cap 22 px)
//	small  24 px  details and the passage, read up close
//	tiny   18 px  status
const (
	mx                                 = 16 // side margin
	rail                               = 262
	tHero, tLarge, tMid, tSmall, tTiny = 140, 52, 30, 24, 18
	quoteLH                            = 27
)

// quoteFace is shared with the fit test: upright, not italic, reads better from a step away.
func quoteFace() font.Face { return face(false, tSmall) }

// drawDash: header; left rail with both cities; right pane with the next 5 hours over warnings
// and this week's events; a bottom band as tall as today's passage.
func (a *App) drawDash(img *image.Gray) {
	a.nav(img)
	small, tiny := face(false, tSmall), face(false, tTiny)
	now := clock().In(a.loc)

	q, hasQ := quoteFor(now)
	var qLines []string
	attr, own := "", 0
	if hasQ {
		qLines = wrap(quoteFace(), "“"+q.Text+"”", W-2*mx)
		qLines = qLines[:min(4, len(qLines))]
		// attribution shares the last line when it fits, else gets its own row; all ends at y=588
		attr = ellipsis(tiny, "— "+q.Author+", "+q.Work, W-2*mx)
		if textW(small, qLines[len(qLines)-1])+textW(tiny, attr)+24 > W-2*mx {
			own = 22
		}
	}
	band := 588 - len(qLines)*quoteLH - own - 2
	fill(img, image.Rect(rail, 58, rail+2, band), black)

	// rail: Göteborg huge with its icon right beside the number, İstanbul below
	if a.wDetail != nil && a.wGlance != nil {
		g, h := a.wDetail.Current, a.wDetail.Hourly
		text(img, mx, 94, a.cfg.Places[1].Name, face(true, tMid), black)
		t := fmt.Sprintf("%d°", round(g.Temp))
		const tx = 24 // the bezel overlaps the panel's left edge: keep the big numbers clear of it
		hf := face(true, tHero)
		for _, sz := range []float64{tHero, 120, 104} { // "-12°" is wider than "8°"; the icon must stay in the rail
			if hf = face(true, sz); tx+textW(hf, t)+4+64 <= rail-8 {
				break
			}
		}
		x := text(img, tx, 234, t, hf, black)
		icon(img, wcat(g.Code), x+4, 128, 64)
		feels := fmt.Sprintf("his %d°", round(atf(h.Feels, 0)))
		wind := fmt.Sprintf("%s %d km/sa", compass(atf(h.WindDir, 0)), round(atf(h.Wind, 0)))
		text(img, mx, 274, feels, small, black)
		text(img, mx, 305, wind, small, black)
		if a.wErr != nil && !a.weatherAt.IsZero() { // last fetch failed: say how old these numbers are
			s := "veri " + a.weatherAt.In(a.loc).Format("15:04")
			text(img, rail-12-textW(small, s), 94, s, small, black)
		}
		// the day's range, right-aligned beside feels-like/wind: coat for the whole day, not just now
		if w := a.wDetail; w.Span != "" {
			r := fmt.Sprintf("%d° / %d°", round(w.Lo), round(w.Hi))
			bold := face(true, tSmall)
			rw := textW(bold, r)
			if rail-12-rw >= mx+max(textW(small, feels), textW(small, wind))+12 {
				text(img, rail-12-textW(small, w.Span), 274, w.Span, small, black)
				text(img, rail-12-rw, 305, r, bold, black)
			}
		}

		// İstanbul (and its rule) sit just above the version line at the foot of the rail, which follows the passage band
		ty := min(max(band-38, 433), 470)
		rule := max(ty-116, 317)
		if rule >= 351 { // room for daylight under the wind line
			p := a.cfg.Places[1]
			if rise, set, ok := sunTimes(p.Lat, p.Lon, now); ok {
				s := "gün batımı " + set.Format("15:04")
				if now.Before(rise) {
					s = "gün doğumu " + rise.Format("15:04")
				} else if now.After(set) {
					if r2, _, ok2 := sunTimes(p.Lat, p.Lon, now.AddDate(0, 0, 1)); ok2 {
						s = "gün doğumu " + r2.Format("15:04")
					}
				}
				text(img, mx, 337, s, small, black)
			}
		}
		fill(img, image.Rect(mx, rule, rail-mx, rule+2), black)
		text(img, mx, rule+40, a.cfg.Places[0].Name, face(true, tMid), black)
		gl := a.wGlance
		x = text(img, tx+2, ty, fmt.Sprintf("%d°", round(gl.Current.Temp)), face(true, 68), black)
		r := ""
		if gl.Span != "" {
			r = fmt.Sprintf("%d° / %d°", round(gl.Lo), round(gl.Hi))
		}
		if r != "" && x+10+textW(small, r) <= rail-8 { // icon above its range, both beside the number
			icon(img, wcat(gl.Current.Code), x+10, ty-66, 40)
			text(img, x+10, ty, r, small, black)
		} else {
			icon(img, wcat(gl.Current.Code), x+8, ty-48, 48)
		}
		if band-12 >= ty+22 { // a very long passage leaves no room: drop the version, not the weather
			text(img, mx, band-12, version, face(false, tTiny), black) // which release is on the wall, without ssh
		}

		// right pane, top: next 5 hours
		px0 := rail + 18
		cw := (W - mx - px0) / 5
		for i := 1; i <= 5 && i < len(h.Time); i++ {
			cx := px0 + (i-1)*cw + cw/2
			centerText(img, hourLabel(h.Time[i]), small, cx, 86)
			icon(img, wcat(at(h.Code, i)), cx-24, 92, 48)
			centerText(img, fmt.Sprintf("%d°", round(atf(h.Temp, i))), face(true, tLarge), cx, 186)
			centerText(img, fmt.Sprintf("his %d°", round(atf(h.Feels, i))), small, cx, 214)
			centerText(img, fmt.Sprintf("%s %d", compass(atf(h.WindDir, i)), round(atf(h.Wind, i))), small, cx, 240)
		}
	} else {
		for i, l := range wrap(face(true, tMid), "Hava durumu alınamadı", rail-2*mx) {
			text(img, mx, 110+i*36, l, face(true, tMid), black)
		}
	}
	px0 := rail + 18
	fill(img, image.Rect(px0, 252, W-mx, 254), black)

	// right pane, below: official SMHI warnings, then this week's events
	y := 260
	for _, w := range a.warnings {
		icon(img, "warn", px0, y+6, 28)
		x := text(img, px0+38, y+28, warnLabel[w.Level], face(true, tSmall), black)
		text(img, x+10, y+28, ellipsis(small, w.Text, W-mx-x-10), small, black)
		y += 40
	}
	today, evs := a.weekEvents(now)
	const tall, flat, moreH = 66, 44, 30
	limit := band - 6 // keep text off the band rule
	row := tall       // two-line rows when they all fit, otherwise one line each
	if y+len(evs)*tall > limit {
		row = flat
	}
	title := face(true, tMid)
	for i, e := range evs {
		if y+row > limit || i < len(evs)-1 && y+row+moreH > limit {
			text(img, px0+16, y+22, fmt.Sprintf("+%d etkinlik daha", len(evs)-i), face(false, 20), black)
			a.tap(image.Rect(px0, y, W, y+48), func() { a.openDay(today) })
			break
		}
		d := e.Day()
		if d.Before(today) {
			d = today // ongoing multi-day event
		}
		when := evTime(e)
		if row == tall {
			if !e.Multi() {
				when = fmt.Sprintf("%s %d %s · %s", trDays[d.Weekday()], d.Day(), trMonths[d.Month()], when)
			}
			if e.Tag == "red" {
				when += " · RESMİ TATİL"
			}
			fill(img, image.Rect(px0, y+4, px0+5, y+row-10), black)
			text(img, px0+16, y+22, ellipsis(small, when, W-mx-px0-16), small, black)
			text(img, px0+16, y+54, ellipsis(title, e.Summary, W-mx-px0-16), title, black)
		} else {
			if !e.Multi() {
				when = fmt.Sprintf("%s %d · %s", trDays[d.Weekday()], d.Day(), when)
			}
			if e.Tag == "red" {
				when += " · tatil"
			}
			fill(img, image.Rect(px0, y+6, px0+5, y+row-6), black)
			wx := W - mx - textW(small, when)
			text(img, px0+16, y+32, ellipsis(title, e.Summary, wx-px0-28), title, black) // title first, day/time right
			text(img, wx, y+32, when, small, black)
		}
		day := d
		a.tap(image.Rect(px0, y, W, y+row), func() { a.openDay(day) })
		y += row
	}
	if len(evs) == 0 {
		text(img, px0, y+30, "Bu hafta başka etkinlik yok", small, black)
	}

	// bottom band: the passage (24 px, up to 4 lines)
	fill(img, image.Rect(mx, band, W-mx, band+2), black)
	qy := band + 2
	for _, l := range qLines {
		qy += quoteLH
		text(img, mx, qy, l, quoteFace(), black)
	}
	if hasQ {
		text(img, W-mx-textW(tiny, attr), qy+own, attr, tiny, black)
	}
}

// openDay shows d's month with d selected (header tab, a home-screen event, a grid tap).
func (a *App) openDay(d time.Time) { a.view, a.month, a.sel, a.page = "month", firstOfMonth(d), d, 0 }

// drawMonth: the month as a grid of dates with activity marks (no titles: they would be
// unreadably small), and a panel with the selected day's events in full.
func (a *App) drawMonth(img *image.Gray) {
	a.nav(img)
	today := midnight(clock().In(a.loc), a.loc)
	first := a.month
	next := first.AddDate(0, 1, 0)
	if a.sel.Before(first) || !a.sel.Before(next) {
		a.sel, a.page = first, 0
	}

	// month navigation
	big, mid, small, tiny := face(false, tLarge), face(true, tMid), face(false, tSmall), face(true, tTiny)
	arrow := func(r image.Rectangle, s string, fn func()) {
		centerText(img, s, big, r.Min.X+r.Dx()/2, r.Min.Y+40)
		a.tap(r, fn)
	}
	arrow(image.Rect(mx, 62, 68, 114), "‹", func() { a.openDay(first.AddDate(0, -1, 0)) })
	centerText(img, fmt.Sprintf("%s %d", trMonths[first.Month()], first.Year()), mid, 184, 98)
	arrow(image.Rect(300, 62, 352, 114), "›", func() { a.openDay(next) })
	a.button(img, image.Rect(362, 66, 464, 110), "Bugün", false, func() { a.openDay(today) })

	// grid, Monday first
	byDay := map[time.Time][]Event{}
	for _, e := range a.cal.Events(first, next.AddDate(0, 0, -1), a.loc) {
		for d := e.Day(); !d.After(e.Last); d = d.AddDate(0, 0, 1) {
			if !d.Before(first) && d.Before(next) {
				byDay[d] = append(byDay[d], e)
			}
		}
	}
	const x0, cw, top = mx, 64, 160
	for i, dn := range gridDays {
		centerText(img, dn, tiny, x0+i*cw+cw/2, 148)
	}
	offset := (int(first.Weekday()) + 6) % 7
	days := daysBetween(first, next)
	rows := (offset + days + 6) / 7
	ch := min(86, (H-8-top)/rows) // 5 rows: 86 px, 6 rows: 72 px
	fb := face(true, tSmall)
	for i := 0; i < rows*7; i++ {
		cx, cy := x0+(i%7)*cw, top+(i/7)*ch
		r := image.Rect(cx, cy, cx+cw, cy+ch)
		box(img, image.Rect(cx, cy, cx+cw+1, cy+ch+1), 1, black)
		dayN := i - offset + 1
		if dayN < 1 || dayN > days {
			continue
		}
		d := first.AddDate(0, 0, dayN-1)
		evs := byDay[d]
		for _, e := range evs {
			if e.Tag == "red" { // e-ink can't do red: shade the cell
				fill(img, image.Rect(cx+1, cy+1, cx+cw, cy+ch), shade)
				break
			}
		}
		num := fmt.Sprint(dayN)
		if d.Equal(today) { // ring around today's number
			disc(img, cx+cw/2, cy+21, 18, black)
			disc(img, cx+cw/2, cy+21, 15, white)
		}
		centerText(img, num, fb, cx+cw/2, cy+30)
		if n := len(evs); n > 4 {
			centerText(img, "5+", tiny, cx+cw/2, cy+ch-10)
		} else if n > 0 { // one bar per event
			bx := cx + cw/2 - (n*10-2)/2
			for k := 0; k < n; k++ {
				fill(img, image.Rect(bx+k*10, cy+ch-16, bx+k*10+8, cy+ch-10), black)
			}
		}
		if d.Equal(a.sel) {
			box(img, image.Rect(cx+3, cy+3, cx+cw-2, cy+ch-2), 3, black)
		}
		day := d
		a.tap(r, func() { a.sel, a.page = day, 0 })
	}

	// day panel
	const px, pw = 492, W - mx - 492
	fill(img, image.Rect(476, 64, 478, H-8), black)
	text(img, px, 96, fmt.Sprintf("%d %s", a.sel.Day(), trMonths[a.sel.Month()]), mid, black)
	text(img, px, 128, trDaysLong[a.sel.Weekday()], small, black)
	fill(img, image.Rect(px, 140, W-mx, 142), black)

	// each event is a block: time line, title (up to 3 lines), place and notes (up to 2 + 4 lines)
	type line struct {
		s    string
		f    font.Face
		lh   int
		rule bool
	}
	var blocks [][]line
	for _, e := range a.cal.Events(a.sel, a.sel, a.loc) {
		when := evTime(e)
		if e.Tag == "red" {
			when += " · resmi tatil"
		}
		b := []line{{when, fb, 30, false}}
		for _, l := range clip(wrap(mid, e.Summary, pw), 3) {
			b = append(b, line{l, mid, 34, false})
		}
		if e.Loc != "" {
			for _, l := range clip(wrap(small, e.Loc, pw), 2) {
				b = append(b, line{l, small, 28, false})
			}
		}
		if e.Desc != "" && e.Tag == "cal" { // the holiday feed's text is Google boilerplate
			for _, l := range clip(wrap(small, e.Desc, pw), 4) {
				b = append(b, line{l, small, 28, false})
			}
		}
		blocks = append(blocks, b)
	}
	// pages: as many whole blocks as fit between y=146 and the pager
	const bodyTop, bodyEnd = 146, 538
	var pages [][][]line
	h := 0
	for _, b := range blocks {
		bh := 16
		for _, l := range b {
			bh += l.lh
		}
		if len(pages) == 0 || h+bh > bodyEnd-bodyTop {
			pages, h = append(pages, nil), 0
		}
		pages[len(pages)-1] = append(pages[len(pages)-1], b)
		h += bh
	}
	if len(blocks) == 0 {
		text(img, px, bodyTop+34, "Etkinlik yok", small, black)
		return
	}
	a.page = min(a.page, len(pages)-1)
	y := bodyTop
	for _, b := range pages[a.page] {
		y0 := y + 6
		for _, l := range b {
			y += l.lh
			text(img, px+14, y, l.s, l.f, black)
		}
		fill(img, image.Rect(px, y0, px+5, y+6), black)
		y += 16
	}
	if len(pages) > 1 {
		if a.page > 0 {
			arrow(image.Rect(px, 542, px+56, H), "‹", func() { a.page-- })
		}
		centerText(img, fmt.Sprintf("%d / %d", a.page+1, len(pages)), small, px+pw/2, 580)
		if a.page < len(pages)-1 {
			arrow(image.Rect(W-mx-56, 542, W, H), "›", func() { a.page++ })
		}
	}
}

// clip keeps the first n lines, marking the cut with an ellipsis.
func clip(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	lines = lines[:n]
	lines[n-1] += "…"
	return lines
}
