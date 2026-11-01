package main

// The one screen: calendar first. Left: the month, Göteborg (or wherever the device is) now.
// Right: the selected day and everything after it, one line per event, over the next hours.
// SMHI warnings ride in the header (nav).

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"time"
)

const (
	split = 408 // left column ends here, the rule is at split+6, the right column starts at split+16
	rx    = split + 16
)

func (a *App) drawMain(img *image.Gray) {
	if os.Getenv("QBAND") != "" { // PROTO
		a.drawMainQ(img)
		return
	}
	a.nav(img)
	a.monthNav(img, image.Rect(mx, 54, W-mx, 100))
	fill(img, image.Rect(mx, 102, W-mx, 104), black)
	fill(img, image.Rect(split+6, 104, split+8, H), black)
	a.dayGrid(img, image.Rect(mx, 104, split, 464), a.month, 6)
	fill(img, image.Rect(mx, 468, split, 470), black)
	a.drawNow(img, 470)
	a.drawAgenda(img, image.Rect(rx, 108, W-mx, 534), 42)
	fill(img, image.Rect(rx, 538, W-mx, 540), black)
	a.drawHours(img, rx, 540, 4)
}

// PROTO: the same screen over a one-line passage band along the bottom (48 px).
func (a *App) drawMainQ(img *image.Gray) {
	a.nav(img)
	a.monthNav(img, image.Rect(mx, 54, W-mx, 100))
	fill(img, image.Rect(mx, 102, W-mx, 104), black)
	fill(img, image.Rect(split+6, 104, split+8, 550), black)
	a.dayGrid(img, image.Rect(mx, 104, split, 420), a.month, 6)
	fill(img, image.Rect(mx, 424, split, 426), black)
	a.drawNow(img, 426)
	a.drawAgenda(img, image.Rect(rx, 108, W-mx, 488), 42)
	fill(img, image.Rect(rx, 492, W-mx, 494), black)
	a.drawHours(img, rx, 494, 4)
	fill(img, image.Rect(mx, 550, W-mx, 552), black)
	if q, ok := quoteFor(clock().In(a.loc)); ok {
		tiny := face(false, tTiny)
		text(img, mx, 574, ellipsis(quoteFace(), "“"+q.Text+"”", W-2*mx), quoteFace(), black)
		attr := ellipsis(tiny, "— "+q.Author+", "+q.Work, W-2*mx)
		text(img, W-mx-textW(tiny, attr), 594, attr, tiny, black)
	}
}

// ---------------------------------------------------------------- calendar data

// calDay: one day and the events touching it.
type calDay struct {
	Day time.Time
	Evs []Event
}

// horizon: n days from from's day; that day's timed events that ended before from are dropped
// (from at midnight keeps them all), multi-day events listed on every day they cover.
func (a *App) horizon(from time.Time, n int) []calDay {
	first := midnight(from, a.loc)
	days := make([]calDay, n)
	for i := range days {
		days[i].Day = first.AddDate(0, 0, i)
	}
	for _, e := range a.cal.Events(first, first.AddDate(0, 0, n-1), a.loc) {
		if !e.AllDay && !e.Multi() && e.Day().Equal(first) && e.Start.Before(from) {
			continue
		}
		for i := range days {
			d := days[i].Day
			if !d.Before(e.Day()) && !d.After(e.Last) {
				days[i].Evs = append(days[i].Evs, e)
			}
		}
	}
	return days
}

func dayLabel(d time.Time) string { return fmt.Sprintf("%s %d", trDays[d.Weekday()], d.Day()) }

type agItem struct {
	Day time.Time
	E   Event
}

// agendaItems: every event once, on the first day it touches inside the horizon.
func agendaItems(days []calDay) []agItem {
	seen := map[string]bool{}
	var out []agItem
	for _, d := range days {
		for _, e := range d.Evs {
			k := e.Summary + e.Start.String()
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, agItem{d.Day, e})
		}
	}
	return out
}

// ---------------------------------------------------------------- month

func (a *App) today() time.Time { return midnight(clock().In(a.loc), a.loc) }

// monthNav: ‹ Kasım 2026 › [Bugün]
func (a *App) monthNav(img *image.Gray, r image.Rectangle) {
	big, mid := face(false, tLarge), face(true, tMid)
	cy := r.Min.Y + r.Dy()/2
	move := func(n int) func() { return func() { a.month, a.page = a.month.AddDate(0, n, 0), 0 } }
	centerText(img, "‹", big, r.Min.X+24, cy+18)
	a.tap(image.Rect(r.Min.X, r.Min.Y, r.Min.X+56, r.Max.Y), move(-1))
	const bw = 104
	nx := r.Max.X - bw - 8 - 56
	centerText(img, fmt.Sprintf("%s %d", trMonths[a.month.Month()], a.month.Year()), mid, (r.Min.X+56+nx)/2, cy+11)
	centerText(img, "›", big, nx+28, cy+18)
	a.tap(image.Rect(nx, r.Min.Y, nx+56, r.Max.Y), move(1))
	a.button(img, image.Rect(r.Max.X-bw, r.Min.Y+2, r.Max.X, r.Max.Y-2), "Bugün", false, a.home)
}

// dayGrid: the month as weeks x 7 cells, Monday first. Bars under a number = own events (max 3),
// grey = red day (holidays and Sundays), inverted = today, frame = selected; a tap selects the day.
func (a *App) dayGrid(img *image.Gray, r image.Rectangle, month time.Time, weeks int) {
	const head = 26
	cw, ch := r.Dx()/7, (r.Dy()-head)/weeks
	numF := face(true, tSmall)
	for i, dn := range gridDays {
		centerText(img, dn, face(true, tTiny), r.Min.X+i*cw+cw/2, r.Min.Y+19)
	}
	start := month.AddDate(0, 0, -((int(month.Weekday()) + 6) % 7))
	today := a.today()
	for i, cd := range a.horizon(start, weeks*7) {
		x, y := r.Min.X+(i%7)*cw, r.Min.Y+head+(i/7)*ch
		box(img, image.Rect(x, y, x+cw+1, y+ch+1), 1, black)
		d := cd.Day
		if d.Month() != month.Month() {
			continue
		}
		own, red := 0, false
		for _, e := range cd.Evs {
			if e.Tag == "cal" {
				own++
			}
			red = red || e.Tag == "red"
		}
		red = red || d.Weekday() == time.Sunday // a röd dag, as on Swedish calendars
		fg := color.Gray{0}
		switch {
		case d.Equal(today):
			fill(img, image.Rect(x+1, y+1, x+cw, y+ch), black)
			fg = white
		case red: // e-ink can't do red
			fill(img, image.Rect(x+1, y+1, x+cw, y+ch), shade)
		}
		ny := y + ch/2 + 9
		if own > 0 {
			ny = y + ch/2 + 5
		}
		n := fmt.Sprint(d.Day())
		text(img, x+cw/2-textW(numF, n)/2, ny, n, numF, fg)
		if k := min(own, 3); k > 0 {
			bx := x + cw/2 - (k*10-2)/2
			for j := 0; j < k; j++ {
				fill(img, image.Rect(bx+j*10, ny+5, bx+j*10+8, ny+9), fg)
			}
		}
		if d.Equal(a.sel) {
			box(img, image.Rect(x+3, y+3, x+cw-2, y+ch-2), 3, fg)
		}
		day := d
		a.tap(image.Rect(x, y, x+cw, y+ch), func() { a.sel, a.page = day, 0 })
	}
}

// ---------------------------------------------------------------- agenda

// dateCol: "Cmt 7", or "1 Oca" once the list has left the selected month.
func (a *App) dateCol(d time.Time) string {
	if d.Month() != a.sel.Month() {
		return fmt.Sprintf("%d %s", d.Day(), short(trMonths[d.Month()]))
	}
	return dayLabel(d)
}

func timeCol(e Event) string {
	switch {
	case e.Multi():
		return fmt.Sprintf("–%d %s", e.Last.Day(), short(trMonths[e.Last.Month()]))
	case e.AllDay:
		return "" // no time = all day
	}
	return e.Start.Format("15:04")
}

// drawAgenda: the selected day and everything after it (120 days), one line per event:
// day (only where it changes) · time · title. Paged; a tap on a line selects its day.
func (a *App) drawAgenda(img *image.Gray, r image.Rectangle, rowH int) {
	bold, tf, tm := face(true, 20), face(true, 22), face(false, tTiny)
	const dateW, timeW = 76, 62
	type row struct {
		it    agItem
		empty bool
	}
	var rows []row
	items := agendaItems(a.horizon(a.sel, 120))
	if len(items) == 0 || !items[0].Day.Equal(a.sel) {
		rows = append(rows, row{agItem{Day: a.sel}, true})
	}
	for _, it := range items {
		rows = append(rows, row{it, false})
	}
	n, pages := r.Dy()/rowH, 1
	if len(rows) > n {
		n = (r.Dy() - 44) / rowH
		pages = (len(rows) + n - 1) / n
	}
	a.page = min(a.page, pages-1)
	y := r.Min.Y
	var last time.Time
	for _, rw := range rows[a.page*n : min(len(rows), (a.page+1)*n)] {
		base := y + rowH/2 + 9
		d := rw.it.Day
		if !d.Equal(last) && y > r.Min.Y { // a rule where the day changes
			fill(img, image.Rect(r.Min.X, y, r.Max.X, y+1), black)
		}
		if rw.it.E.Tag == "red" {
			fill(img, image.Rect(r.Min.X, y+2, r.Min.X+dateW-6, y+rowH-2), shade)
		}
		if !d.Equal(last) {
			text(img, r.Min.X+2, base, a.dateCol(d), bold, black)
		}
		last = d
		if rw.empty {
			text(img, r.Min.X+dateW, base, "etkinlik yok", tm, black)
		} else {
			text(img, r.Min.X+dateW, base, timeCol(rw.it.E), tm, black)
			tx := r.Min.X + dateW + timeW
			text(img, tx, base, ellipsis(tf, rw.it.E.Summary, r.Max.X-tx), tf, black)
		}
		day := d
		a.tap(image.Rect(r.Min.X, y, r.Max.X, y+rowH), func() { a.sel, a.page = day, 0 })
		y += rowH
	}
	if pages > 1 {
		py, big, small := r.Max.Y-44, face(false, tLarge), face(false, tSmall)
		if a.page > 0 {
			centerText(img, "‹", big, r.Min.X+24, py+38)
			a.tap(image.Rect(r.Min.X, py, r.Min.X+64, r.Max.Y+4), func() { a.page-- })
		}
		centerText(img, fmt.Sprintf("%d / %d", a.page+1, pages), small, (r.Min.X+r.Max.X)/2, py+32)
		if a.page < pages-1 {
			centerText(img, "›", big, r.Max.X-24, py+38)
			a.tap(image.Rect(r.Max.X-64, py, r.Max.X, r.Max.Y+4), func() { a.page++ })
		}
	}
}

// ---------------------------------------------------------------- weather

// drawNow: under the month, from y: the big temperature, and beside it three short lines:
// "Göteborg ☁", "his 8° · 11° / 12°" (tomorrow's range after 17:00 says "yarın"), "GB 18 km/sa", "☀↓16:28".
func (a *App) drawNow(img *image.Gray, y int) {
	if a.wDetail == nil {
		text(img, mx, y+44, "Hava durumu alınamadı", face(true, tMid), black)
		return
	}
	g, h := a.wDetail.Current, a.wDetail.Hourly
	small, bold, tiny := face(false, tSmall), face(true, tSmall), face(false, tTiny)
	const tx = 24                                                                                    // the bezel overlaps the panel's left edge
	lx := max(text(img, tx, y+98, fmt.Sprintf("%d°", round(g.Temp)), face(true, 96), black)+16, 168) // centred on the four lines
	x := text(img, lx, y+28, a.place.Name, bold, black)
	icon(img, wcat(g.Code), x+8, y+4, 28)
	if a.wErr != nil && !a.weatherAt.IsZero() { // last fetch failed: how old these numbers are
		s := "veri " + a.weatherAt.In(a.loc).Format("15:04")
		text(img, split-textW(tiny, s), y+28, s, tiny, black)
	}
	x = text(img, lx, y+56, fmt.Sprintf("his %d°", round(atf(h.Feels, 0))), small, black)
	if w := a.wDetail; w.Span != "" {
		x = text(img, x, y+56, " · ", small, black)
		x = text(img, x, y+56, fmt.Sprintf("%d° / %d°", round(w.Lo), round(w.Hi)), bold, black)
		if w.Span == "yarın" && x+6+textW(tiny, w.Span) <= split {
			text(img, x+6, y+56, w.Span, tiny, black)
		}
	}
	text(img, lx, y+84, fmt.Sprintf("%s %d km/sa", compass(atf(h.WindDir, 0)), round(atf(h.Wind, 0))), small, black)
	if up, t, ok := a.sun(); ok {
		arrow := "↓"
		if up {
			arrow = "↑"
		}
		icon(img, "sun", lx-2, y+92, 26)
		text(img, lx+28, y+112, arrow+t, small, black)
	}
}

// sun: the next sunrise (up) or today's sunset.
func (a *App) sun() (up bool, at string, ok bool) {
	now, p := clock().In(a.loc), a.place
	rise, set, ok := sunTimes(p.Lat, p.Lon, now)
	switch {
	case !ok:
		return false, "", false
	case now.Before(rise):
		return true, rise.Format("15:04"), true
	case now.After(set):
		if r2, _, ok2 := sunTimes(p.Lat, p.Lon, now.AddDate(0, 0, 1)); ok2 {
			return true, r2.Format("15:04"), true
		}
	}
	return false, set.Format("15:04"), true
}

// drawHours: the next n hours in one thin strip from (x, y): time over icon + temperature.
func (a *App) drawHours(img *image.Gray, x, y, n int) {
	if a.wDetail == nil {
		return
	}
	h := a.wDetail.Hourly
	sw := (W - mx - x) / n
	for i := 1; i <= n && i < len(h.Time); i++ {
		cx := x + (i-1)*sw + sw/2
		centerText(img, hourLabel(h.Time[i]), face(false, tTiny), cx, y+22)
		icon(img, wcat(at(h.Code, i)), cx-34, y+26, 30)
		text(img, cx, y+53, fmt.Sprintf("%d°", round(atf(h.Temp, i))), face(true, tSmall), black)
	}
}

// warnLine: every warning on one line from x to right at baseline y: "Turuncu: şiddetli yağmur",
// shortened to "Turuncu: yağmur" when they don't all fit; what still won't fit is "+N".
func (a *App) warnLine(img *image.Gray, x, y, right int) {
	small, bold := face(false, tSmall), face(true, tSmall)
	icon(img, "warn", x, y-22, 28)
	x0 := x + 32
	seg := func(w Warning, form int) (string, string) {
		switch form {
		case 0:
			return warnLabel[w.Level] + ": ", w.Text
		}
		return warnLabel[w.Level] + ": ", shortReason(w.Text)
	}
	form := 0
	for ; form < 1; form++ {
		n := x0
		for i, w := range a.warnings {
			l, r := seg(w, form)
			n += textW(bold, l) + textW(small, r)
			if i > 0 {
				n += textW(small, " · ")
			}
		}
		if n <= right {
			break
		}
	}
	x = x0
	for i, w := range a.warnings {
		l, r := seg(w, form)
		sep, more := "", 0
		if i > 0 {
			sep = " · "
		}
		if i < len(a.warnings)-1 {
			more = textW(bold, fmt.Sprintf(" +%d", len(a.warnings)-i-1))
		}
		if i > 0 && x+textW(small, sep)+textW(bold, l)+textW(small, r)+more > right {
			text(img, x, y, fmt.Sprintf(" +%d", len(a.warnings)-i), bold, black)
			break
		}
		x = text(img, x, y, sep, small, black)
		x = text(img, x, y, l, bold, black)
		x = text(img, x, y, ellipsis(small, r, right-x), small, black)
	}
}
