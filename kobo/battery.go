package main

// Low-battery shutdown, in place of InkBox's battery_watchdog. That one powers off at the
// first reading <= 5%, and the N705's driver returns 0% when it fails to read the MSP430
// (five failed tries) or its critical flag is set (mx50_ntx_io.c): one bad read would take a
// healthy reader off the wall for good. Here it takes three readings 5 s apart, all at or
// under critPct while not charging, and a 0 only counts when something real backs it.

import (
	"log"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const critPct = 3

// battRead: the capacity in %, and whether it is charging; ok false if it can't be read.
func battRead() (pct int, charging, ok bool) {
	b, err := os.ReadFile(battDir + "capacity")
	if err != nil {
		return 0, false, false
	}
	pct, err = strconv.Atoi(strings.TrimSpace(string(b)))
	st, _ := os.ReadFile(battDir + "status")
	return pct, strings.Contains(string(st), "Charging"), err == nil
}

// battCritical: the readings say the cell is really empty. lastGood is the last reading
// above 0 seen before.
func battCritical(readings []int, charging bool, lastGood int) bool {
	if charging || len(readings) == 0 {
		return false
	}
	real := lastGood > 0 && lastGood <= 2*critPct // a 0 right after 6% is believable
	for _, p := range readings {
		if p > critPct {
			return false
		}
		if p > 0 {
			real = true
		}
	}
	return real
}

var lastGoodPct int

// checkBattery runs after a refresh. Usually one read; three only when the first is low.
func (a *App) checkBattery() {
	pct, charging, ok := battRead()
	if !ok || charging || pct > critPct {
		if ok && pct > 0 {
			lastGoodPct = pct
		}
		return
	}
	readings := []int{pct}
	for len(readings) < 3 {
		time.Sleep(5 * time.Second)
		p, c, ok := battRead()
		if !ok || c {
			return
		}
		readings = append(readings, p)
	}
	if !battCritical(readings, false, lastGoodPct) {
		log.Printf("battery: readings %v, last good %d%%: not believed", readings, lastGoodPct)
		return
	}
	log.Printf("battery: %v%%, powering off", readings)
	a.lowBattery = true
	a.show() // "pil bitti" frame stays on the panel
	if a.cfg.TgToken != "" {
		a.telegram(a.cfg.TgThread, "Kobo pano: pil bitti, kapanıyor. Şarja tak.", false)
	}
	flushLog()
	within(10*time.Second, func() { syscall.Sync() })
	if err := sysPowerOff(); err != nil {
		log.Printf("battery: power off: %v", err)
	}
}
