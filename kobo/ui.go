package main

// Screen side: text, icons and the header shared by the screen (screen.go), drawn into an
// 800x600 landscape image that is rotated onto the 600x800 panel; taps are recorded as hits.

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"net"
	"os"
	"strconv"
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

func init() { // the last refresh failed: the refresh arrow struck through like wifi-off
	icons["refresh-off"] = icons["refresh"] + strings.TrimPrefix(icons["wifi-off"], wifiArcs)
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

func icon(dst *image.Gray, name string, x, y, size int) { drawSVG(dst, icons[name], x, y, size) }

// battIcon: a battery outline filled to pct.
func battIcon(pct int) string {
	return `<rect x="1.5" y="7" width="19" height="10" rx="1.5" fill="none" stroke="#000" stroke-width="2"/>` +
		`<rect x="21" y="10" width="2" height="4"/>` +
		fmt.Sprintf(`<rect x="3.5" y="9" width="%.1f" height="6"/>`, 15*float64(min(max(pct, 0), 100))/100)
}

func drawSVG(dst *image.Gray, body string, x, y, size int) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">` + body + `</svg>`
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

var fakeBattery = "" // set by -fakewarn for desk renders

func battery() string {
	b, err := os.ReadFile(battDir + "capacity")
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
	a.drawMain(img)
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

// drawStatus is the small footer line: version · battery · next update, then a struck
// refresh icon when the last refresh failed. Returns where it ends.
func (a *App) drawStatus(img *image.Gray, x, y int) int {
	f := face(false, tTiny)
	x = text(img, x, y, version, f, black)
	if b, err := os.ReadFile(battDir + "capacity"); err == nil || fakeBattery != "" {
		pct := strings.TrimSpace(string(b))
		if err != nil {
			pct = strings.TrimPrefix(fakeBattery, "pil %")
		}
		n, _ := strconv.Atoi(pct)
		x = text(img, x, y, " · ", f, black)
		drawSVG(img, battIcon(n), x, y-15, 20)
		x = text(img, x+22, y, "%"+pct, f, black)
	}
	next := a.nextWake // set when going to sleep; while awake, derive it from the last refresh
	if next.Before(a.updated) {
		next = a.updated.Add(time.Duration(a.cfg.RefreshMin) * time.Minute)
	}
	if a.cfg.Sleep && next.After(clock()) {
		x = text(img, x, y, " · sonraki "+next.In(a.loc).Format("15:04"), f, black)
	}
	if a.wErr != nil { // the last refresh failed: these numbers are old
		icon(img, "refresh-off", x+8, y-16, 20)
		x += 28
	}
	return x
}

// nav is the header every view shares: tabs · date + ISO week · next update/battery, Wi-Fi, refresh.
func (a *App) nav(img *image.Gray) {
	now := clock().In(a.loc)
	dx := mx

	ref := image.Rect(W-64, 4, W-mx, 46)
	if a.busy {
		fill(img, ref, black) // pressed: instant feedback, the fetch below can take a while
	} else {
		box(img, ref, 2, black)
		icon(img, "refresh", ref.Min.X+9, ref.Min.Y+6, 30)
	}
	// touch targets reach the screen edge: the panel reads edge taps a few px off
	a.tap(image.Rect(W-72, 0, W, 58), func() {
		a.busy = true
		a.show()
		a.forceCal, a.wantUpdate = true, true
		a.refresh()
		a.busy = false
	})
	wr := image.Rect(W-120, 4, W-72, 46)
	wifi := "wifi"
	a.shownOnline = online()
	if !a.shownOnline {
		wifi = "wifi-off"
	}
	if a.wifiPinned {
		box(img, wr, 2, black) // framed = switched on by hand
	}
	icon(img, wifi, wr.Min.X+10, 11, 28)
	a.tap(image.Rect(wr.Min.X-8, 0, W-72, 58), a.toggleWifi)

	// date without the year ("1 Kasım, Paz · W44"); warnings ride right of the week and get
	// the room of the long month name when there are any
	_, week := now.ISOWeek()
	room := wr.Min.X - 16 - dx
	date := fmt.Sprintf("%d %s, %s", now.Day(), trMonths[now.Month()], trDays[now.Weekday()])
	if len(a.warnings) > 0 {
		date = fmt.Sprintf("%d %s, %s", now.Day(), short(trMonths[now.Month()]), trDays[now.Weekday()])
	}
	x := text(img, dx, 34, date, face(true, tSmall), black)
	x = text(img, x, 34, fmt.Sprintf(" · W%d", week), face(false, tSmall), black)
	if len(a.warnings) > 0 {
		a.warnLine(img, x+10, 34, dx+room)
	}
	fill(img, image.Rect(0, 50, W, 52), black)
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

// Type sizes, by viewing distance (1 px = 0.127 mm, Go cap height = 0.72 em):
//
//	large  52 px  read from 1 m (cap 37 px = 16')
//	mid    30 px  titles: 1 m at road-sign legibility (cap 22 px)
//	small  24 px  details, read up close
//	tiny   18 px  status
const (
	mx                          = 16 // side margin
	tLarge, tMid, tSmall, tTiny = 52, 30, 24, 18
)
