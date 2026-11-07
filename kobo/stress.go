package main

// Wi-Fi stress test, on the charger only: the card file wifi-stress holds phases, one per
// line, "<cycles> <dhd module args or ->". Each cycle switches Wi-Fi off (rmmod dhd), back
// on and waits for an address, the round a timed wake does minus the sleep, so the dhd
// unload deadlock (about 1 in 45 wakes, 3 h apart) shows within hours instead of weeks.
// A hang is logged with the blocked tasks' kernel stacks and rebooted at once, outside the
// daily reboot budget: a cycle is taken off the file before it runs, so the reboots are as
// many as the cycles at most, and the file goes once it hasn't moved for testStale.
// Results go to wifi-stress.log on the card, one line per cycle.

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// dhdArgs: module parameters for the next insmod of dhd (wifiOnScript's $KD_DHD).
var dhdArgs atomic.Value

// dhdDefault: the dhd watchdog wakes every 10 ms, and its SDIO access is one side of the
// unload deadlock (rmmod waiting on ksdioirqd, that on the driver lock the watchdog holds
// while it waits for the SDIO host). Re-armed 60 s ahead on every clock change, it never
// runs in a ~10 s wake. Not 0: a build with SDTEST divides by it at load. Stress test of
// 7 Nov 2026 on this device: 2 hangs in 35 unloads without it, 0 in 99 with it, connect
// time the same (5.2 s vs 5.1 s). On the charger it still runs, once a minute.
const dhdDefault = "dhd_watchdog_ms=60000"

func init() { dhdArgs.Store(dhdDefault) }

var dhdArgsOK = regexp.MustCompile(`^([a-z_]+=[0-9]+)( [a-z_]+=[0-9]+)*$`)

const stressFile = "wifi-stress"

var stressNoIP int // cycles in a row without an address

// stressPhase: the first phase line, or n=0.
func stressPhase() (n int, args string, rest []string) {
	p := stateFile(stressFile)
	st, err := os.Stat(p)
	if err != nil {
		return 0, "", nil
	}
	if time.Since(st.ModTime()) > testStale {
		log.Printf("stress: not moved for %v, ending it", testStale)
		rmState(stressFile)
		return 0, "", nil
	}
	b, _ := os.ReadFile(p)
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	for len(lines) > 0 {
		f := strings.SplitN(lines[0], " ", 2)
		n, _ = strconv.Atoi(f[0])
		args = "-"
		if len(f) == 2 {
			args = strings.TrimSpace(f[1])
		}
		if n > 0 && (args == "-" || dhdArgsOK.MatchString(args)) {
			if args == "-" {
				args = ""
			}
			return n, args, lines[1:]
		}
		log.Printf("stress: bad line %q, skipped", lines[0])
		lines = lines[1:]
	}
	rmState(stressFile)
	return 0, "", nil
}

// stressTake takes one cycle off the file: its phase's args and the cycles left in it after
// this one; ok false when there is none (or the file can't be counted down). The new count
// is on the card for sure before the cycle runs: a hang reboots outside the budget, and an
// old count coming back after the reboot could repeat that without end.
func stressTake() (args string, left int, ok bool) {
	if stressOff {
		return "", 0, false
	}
	n, args, rest := stressPhase()
	if n == 0 {
		return "", 0, false
	}
	next := rest
	if n > 1 {
		next = append([]string{fmt.Sprintf("%d %s", n-1, orDash(args))}, rest...)
	}
	var err error
	if len(next) == 0 {
		err = os.Remove(stateFile(stressFile))
	} else {
		err = writeAtomic(stateFile(stressFile), strings.Join(next, "\n")+"\n")
	}
	if err == nil && !within(10*time.Second, func() { syscall.Sync() }) {
		err = fmt.Errorf("sync stuck")
	}
	if err != nil { // can't count down: never reboot outside the budget on an endless file
		log.Printf("stress: %v; ending it", err)
		stressOff = true
		rmState(stressFile)
		return "", 0, false
	}
	return args, n - 1, true
}

var (
	stressOff    bool        // ended for this process (the file couldn't be counted down)
	stressLoaded = "unknown" // the args the loaded dhd went in with, as the log names them
)

// stressCycle runs one cycle if a phase is left; false when there is none. The unload is
// logged under the args the module was loaded with, the load under the phase's: a phase's
// first unload is of the module the one before (or the boot) loaded.
func stressCycle() bool {
	if wifiSeqInkBox { // toggle.sh loads dhd itself, without $KD_DHD: the test would lie
		if stressPhaseLeft() {
			log.Printf("stress: not with WIFI_SEQ=inkbox, ending it")
			rmState(stressFile)
		}
		return false
	}
	args, n, ok := stressTake()
	if !ok {
		dhdArgs.Store(dhdDefault)
		return false
	}
	beat()

	t0 := time.Now()
	wifiOffOnPurpose.Store(true)
	if wifiOff() == errLeftBehind && stuckInDriver() {
		stressNote("unload", stressLoaded, "hang", time.Since(t0), -1)
		log.Printf("stress: rmmod stuck in the Wi-Fi driver, rebooting (%d left in the phase)", n)
		dumpBlocked()
		kernelLog()
		flushLog()
		within(10*time.Second, func() { syscall.Sync() })
		if err := sysReboot(); err != nil {
			log.Printf("stress: reboot: %v", err)
		}
		return false
	}
	beat()
	if dhdLoaded() { // not unloaded: the next insmod would be skipped and the load would test nothing
		stressNote("unload", stressLoaded, "left", time.Since(t0), -1)
		return true
	}
	stressNote("unload", stressLoaded, "ok", time.Since(t0), -1)
	stressLoaded = orDash(args)
	dhdArgs.Store(args)
	t1 := time.Now()
	reconnectWifi() // insmod refuses a parameter the module doesn't know: no dhd, no address
	for !online() && time.Since(t1) < 40*time.Second {
		time.Sleep(250 * time.Millisecond)
	}
	up := time.Since(t1)
	beat()
	if !online() {
		stressNoIP++
		stressNote("load", stressLoaded, "noip", up, watchdogTicks())
		if stressNoIP >= 5 {
			log.Printf("stress: %d cycles without an address, ending it", stressNoIP)
			rmState(stressFile)
			stressNoIP = 0
			dhdArgs.Store(dhdDefault)
			wifiOff()
			reconnectWifi() // back to a normal load
			stressLoaded = "-"
		}
		return true
	}
	stressNoIP = 0
	time.Sleep(2 * time.Second) // a bit of traffic (timesync) before the next unload, as on a wake
	stressNote("load", stressLoaded, "ok", up, watchdogTicks())
	return true
}

func stressPhaseLeft() bool { _, err := os.Stat(stateFile(stressFile)); return err == nil }

func dhdLoaded() bool {
	for _, l := range strings.Split(readProc("/proc/modules"), "\n") {
		if strings.HasPrefix(l, "dhd ") {
			return true
		}
	}
	return false
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func stressNote(step, args, what string, d time.Duration, wd int) {
	f, err := os.OpenFile(stateFile("wifi-stress.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "%s %s %s %s %.1fs wd=%d\n", time.Now().Format("2006-01-02T15:04:05"), step, args, what, d.Seconds(), wd)
	f.Close()
}

// watchdogTicks: CPU ticks of the dhd_watchdog thread (new with each insmod), -1 if none.
// With dhd_watchdog_ms large it should stay 0 through a short session.
func watchdogTicks() int {
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		if _, err := strconv.Atoi(d.Name()); err != nil || strings.TrimSpace(readProc("/proc/"+d.Name()+"/comm")) != "dhd_watchdog" {
			continue
		}
		s := readProc("/proc/" + d.Name() + "/stat")
		if i := strings.LastIndexByte(s, ')'); i >= 0 {
			if f := strings.Fields(s[i+1:]); len(f) > 12 { // utime, stime: fields 14, 15
				u, _ := strconv.Atoi(f[11])
				t, _ := strconv.Atoi(f[12])
				return u + t
			}
		}
	}
	return -1
}

// dumpBlocked asks the kernel to log every task in D state with its stack (sysrq w), for
// kernelLog to pick up: where exactly a hang waits, beyond wchan.
func dumpBlocked() {
	within(5*time.Second, func() { os.WriteFile("/proc/sysrq-trigger", []byte("w"), 0o200) })
}
