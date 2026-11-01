package main

// Dashboard calendar pane: the next events in full, over a 10-day date strip.

import (
	"fmt"
	"image"
	"time"
)

// calDay: one calendar day in the dashboard's horizon and the events touching it.
type calDay struct {
	Day time.Time
	Evs []Event
}

// horizon: today plus the next n-1 days; today's finished timed events dropped,
// multi-day events listed on every day they cover.
func (a *App) horizon(now time.Time, n int) []calDay {
	today := midnight(now, a.loc)
	days := make([]calDay, n)
	for i := range days {
		days[i].Day = today.AddDate(0, 0, i)
	}
	for _, e := range a.cal.Events(today, today.AddDate(0, 0, n-1), a.loc) {
		if !e.AllDay && !e.Multi() && e.Day().Equal(today) && e.Start.Before(now) {
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

// subLine: "Cmt 7 · yarın · 14:00"; a multi-day event carries its own range: "10:00 24 Ara–3 Oca"
func subLine(it agItem, today time.Time) string {
	s := evTime(it.E)
	if it.E.Multi() {
		return s
	}
	if it.E.Tag == "red" {
		s = "resmî tatil"
	}
	switch {
	case it.Day.Equal(today):
		s = "bugün · " + s
	case it.Day.Equal(today.AddDate(0, 0, 1)):
		s = "yarın · " + s
	}
	return dayLabel(it.Day) + " · " + s
}

// drawCal fills x rail+18..W-mx, y top..limit. Left: the next events as date line + 30 px title
// (one line each once a card no longer fits). Right: all ten days as 2 columns x 5 rows, read
// top-down (bold + tick = own plans, grey cell = red day, thick frame = today).
func (a *App) drawCal(img *image.Gray, now time.Time, top, limit int) {
	const card, line, cw = 66, 44, 94
	px0 := rail + 18
	days := a.horizon(now, 10)
	today := days[0].Day
	items := agendaItems(days)
	small, bold, title := face(false, tSmall), face(true, tSmall), face(true, tMid)
	ch := min((limit-top)/5, 56)
	sx := W - mx - 2*cw
	right := sx - 14 // cards end here
	y := top
	for i, it := range items {
		day := it.Day
		switch {
		case i < 4 && limit-y >= card:
			text(img, px0, y+24, ellipsis(small, subLine(it, today), right-px0), small, black)
			text(img, px0, y+56, ellipsis(title, it.E.Summary, right-px0), title, black)
			a.tap(image.Rect(px0, y, right, y+card), func() { a.openDay(day) })
			y += card
		case i < 5 && limit-y >= line: // no room for a card: date, time and title on one line
			x := text(img, px0, y+32, dayLabel(it.Day)+" "+evTime(it.E), bold, black)
			text(img, x+12, y+32, ellipsis(title, it.E.Summary, right-x-12), title, black)
			a.tap(image.Rect(px0, y, right, y+line), func() { a.openDay(day) })
			y += line
		}
	}
	if len(items) == 0 {
		text(img, px0, top+32, "10 gün boyunca", small, black)
		text(img, px0, top+60, "etkinlik yok", small, black)
	}

	for i, d := range days {
		x, y := sx+(i/5)*cw, top+(i%5)*ch
		r := image.Rect(x, y, x+cw, y+ch)
		busy, red := false, false
		for _, e := range d.Evs {
			busy = busy || e.Tag == "cal" // own plans; observances only show as cards
			red = red || e.Tag == "red"
		}
		if red { // e-ink can't do red: shade the cell, as in the month view
			fill(img, r, shade)
		}
		box(img, r, 1, black)
		if i == 0 {
			box(img, r.Inset(1), 3, black)
		}
		f := small
		if busy {
			f = bold
			if ch >= 50 { // a short cell has no room under the label: bold alone marks it
				fill(img, image.Rect(x+cw/2-16, y+ch-9, x+cw/2+16, y+ch-5), black)
			}
		}
		s := dayLabel(d.Day)
		text(img, x+cw/2-textW(f, s)/2, y+ch/2+10, s, f, black)
		day := d.Day
		a.tap(r, func() { a.openDay(day) })
	}
}
