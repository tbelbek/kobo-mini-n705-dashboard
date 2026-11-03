package main

// Daily stability report: on the first refresh after the quiet hours, the last 24 h of the
// card log is counted and sent as one silent Telegram line. Nothing else has to watch the
// device for the numbers to pile up: missed timed wakes, refreshes without network, failed
// suspends, watchdog and rescue actions, restarts, the night's battery drain.

import (
	"fmt"
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const cardLogPath = "/tmp/p3/rootfs/write/opt/kobodash/kobodash.log"

var (
	logStamp = regexp.MustCompile(`^(\d{4}/\d\d/\d\d \d\d:\d\d:\d\d) (.*)$`)
	logPct   = regexp.MustCompile(`pil %(\d+)`)
	logAfter = regexp.MustCompile(`after ([0-9hms.]+)`)
	logWifi  = regexp.MustCompile(`^wifi: up in ([0-9.]+)(ms|s)$`)
)

// dailyReport counts the log lines of the 24 h before now. expected is the number of timed
// wakes due in that window.
func dailyReport(text string, now time.Time, expected int) string {
	var timer, button, nonet, susp, wd, rescue, starts, upd int
	var wifi []float64
	lastSleepPct, nightFrom, nightTo := -1, 0, 0
	var night time.Duration
	for _, l := range strings.Split(text, "\n") {
		m := logStamp.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		t, err := time.ParseInLocation("2006/01/02 15:04:05", m[1], time.Local)
		if err != nil || !t.After(now.Add(-24*time.Hour)) || t.After(now) {
			continue
		}
		msg := m[2]
		pct := -1
		if p := logPct.FindStringSubmatch(msg); p != nil {
			pct, _ = strconv.Atoi(p[1])
		}
		switch {
		case strings.HasPrefix(msg, "woke: timer"), strings.HasPrefix(msg, "woke: button"):
			if strings.HasPrefix(msg, "woke: timer") {
				timer++
			} else {
				button++
			}
			if a := logAfter.FindStringSubmatch(msg); a != nil && pct >= 0 && lastSleepPct >= 0 {
				if d, err := time.ParseDuration(a[1]); err == nil && d > night {
					night, nightFrom, nightTo = d, lastSleepPct, pct // the longest sleep: the night
				}
			}
		case strings.HasPrefix(msg, "sleep: alarm in"):
			lastSleepPct = pct
		case strings.HasPrefix(msg, "wifi: no address"):
			nonet++
		case strings.HasPrefix(msg, "suspend:"):
			susp++
		case strings.HasPrefix(msg, "watchdog:"):
			wd++
		case strings.HasPrefix(msg, "supervise: RESCUE"):
			rescue++
		case strings.HasPrefix(msg, "kobodash ") && strings.HasSuffix(msg, " starting"):
			starts++
			lastSleepPct = -1 // a sleep logged before a restart isn't the one the next wake ends
		case strings.HasPrefix(msg, "update: ") && strings.Contains(msg, " installed"):
			upd++
		default:
			if w := logWifi.FindStringSubmatch(msg); w != nil {
				v, _ := strconv.ParseFloat(w[1], 64)
				if w[2] == "ms" {
					v /= 1000
				}
				if v > 0.5 { // 0 s: Wi-Fi was already up (ssh, a tap), not a connect
					wifi = append(wifi, v)
				}
			}
		}
	}
	ok := "✓"
	if timer < expected || nonet+susp+wd+rescue > 0 {
		ok = "⚠"
	}
	s := fmt.Sprintf("%s Kobo 24 sa · zamanlı uyanış %d/%d · tuş %d · ağsız %d · uyku hatası %d · bekçi %d · kurtarma %d · yeniden başlama %d",
		ok, timer, expected, button, nonet, susp, wd, rescue, starts)
	if upd > 0 {
		s += fmt.Sprintf(" · güncelleme %d", upd)
	}
	if len(wifi) > 0 {
		slices.Sort(wifi)
		s += fmt.Sprintf(" · Wi-Fi ort. %.1f sn", wifi[len(wifi)/2])
	}
	if night >= 4*time.Hour {
		switch drop := nightFrom - nightTo; {
		case drop < 0, drop == 0 && nightTo >= 100: // rose, or sat at full: on the charger
			s += fmt.Sprintf(" · gece %v şarjda", night.Round(time.Hour))
		default:
			s += fmt.Sprintf(" · gece %v uykuda %%%d (%.2f %%/sa)", night.Round(time.Hour), drop, float64(drop)/night.Hours())
		}
	}
	return s
}

// expectedWakes: timed wakes due in (from, to]: the grid slots, moved by wakeAt the way the
// alarm is (a slot in the quiet hours becomes their end, which need not be on the grid).
func (a *App) expectedWakes(from, to time.Time) int {
	seen := map[time.Time]bool{}
	l := from.In(a.loc)
	for day := 0; day < 3; day++ {
		for m := 0; m < 24*60; m += a.cfg.RefreshMin {
			w := a.wakeAt(time.Date(l.Year(), l.Month(), l.Day()+day, 0, m, 0, 0, a.loc))
			if w.After(from) && !w.After(to) {
				seen[w] = true
			}
		}
	}
	return len(seen)
}

// maybeDailyReport sends the report once a day, on the first refresh with network after the
// quiet hours (06:00 normally; 08:00 if that one had no network).
func (a *App) maybeDailyReport() {
	if a.cfg.TgLogThread == "" || a.cfg.RefreshMin < 1 {
		return
	}
	now := time.Now().Round(0)
	l := now.In(a.loc)
	if a.cfg.QuietFrom == a.cfg.QuietTo || a.wakeAt(now) != now || l.Hour() < a.cfg.QuietTo {
		return // no quiet hours to anchor the day on, inside them, or before their end
	}
	day := l.Format("2006-01-02")
	if a.reported == day {
		return
	}
	if sent, ok := readState(stateFile("daily")); !ok || strings.TrimSpace(sent) == day {
		return // sent today, or can't tell: better one missing report than one per wake
	}
	old, _ := readState(cardLogPath + ".old") // a rotation just before the report leaves most of the day there
	text, _ := readState(cardLogPath)
	if text == "" {
		return
	}
	msg := dailyReport(old+text, now, a.expectedWakes(now.Add(-24*time.Hour), now))
	if a.telegram(a.cfg.TgLogThread, msg, true) {
		a.reported = day // even if the card won't take the marker
		if err := writeAtomic(stateFile("daily"), day); err != nil {
			log.Printf("report: marker: %v", err)
		}
		log.Printf("report: %s", msg)
	}
}
