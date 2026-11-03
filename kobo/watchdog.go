package main

// Watchdog: the device hangs on a fridge, often with nobody near it. If the main loop stops
// making progress (a call stuck in the kernel), helpers keep getting stuck, or the device
// keeps failing to suspend, it would stay awake with Wi-Fi off, unreachable, until the
// battery is flat. So it heals itself, one step further each time the trouble comes back:
//
//  1. kill stuck helpers (fbink, the Wi-Fi scripts, rmmod...) and turn Wi-Fi back on
//  2. restart kobodash (supervise.sh starts a fresh process)
//  3. a release still on trial goes back to the previous binary, then the device reboots,
//     at most watchdogMax times a day so a fault that survives a reboot can't loop.
//     Past that it stays awake with Wi-Fi on (holdAwake), so someone can look.
//
// The step reached is kept on the card, so it survives the restart in step 2; an hour
// without trouble, or one good sleep cycle, starts over at step 1. Time is monotonic:
// suspended time doesn't count.

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	watchdogStuck    = 10 * time.Minute // awake without the main loop moving (a refresh takes ~3 min at most)
	watchdogFails    = 6                // suspend failures in a row (~15 min awake in normal config)
	watchdogLeft     = 3                // helpers left behind stuck since the last good sleep
	watchdogMax      = 3                // reboots per 24 h
	watchdogInterval = time.Minute
	healForget       = time.Hour // trouble this long after the last step starts over at step 1
	netFailWakes     = 3         // refreshes in a row without a route before the network ladder
	netForget        = 24 * time.Hour
	pinLease         = 30 * time.Minute // Wi-Fi switched on by hand, nobody touching: back to auto
	holdLease        = 6 * time.Hour    // awake with Wi-Fi on after the watchdog gave up: then try sleeping again
)

var errLeftBehind = errors.New("helper left behind")

var (
	beatMu       sync.Mutex
	lastBeat     time.Time
	healMu       sync.Mutex // one recovery or update-file change at a time
	suspendFails atomic.Int32
	leftBehind   atomic.Int32
	netFails     atomic.Int32
	sleptOK      atomic.Bool // a suspend and resume worked in this process
	// Wi-Fi switched off while awake on purpose (idle timer, the icon): the heartbeat says
	// so, and supervise.sh's rescue mode doesn't count the missing route
	wifiOffOnPurpose atomic.Bool
	holdAwake        atomic.Bool // out of reboots: keep Wi-Fi on and don't suspend (for holdLease)
	holdMu           sync.Mutex
	holdSince        time.Time
	beats            int
)

// beatFile: a counter supervise.sh watches from outside, so a kobodash that is wholly stuck
// (deadlock, runtime trouble: the watchdog goroutine stuck with it) still gets restarted.
const beatFile = "/tmp/kobodash.beat"

// beat: the main loop is alive.
func beat() {
	beatMu.Lock()
	lastBeat = time.Now()
	beats++
	n := beats
	beatMu.Unlock()
	if onDevice() {
		v := strconv.Itoa(n)
		if wifiOffOnPurpose.Load() {
			v += " off"
		}
		os.WriteFile(beatFile, []byte(v), 0o644) // tmpfs: cheap
	}
}

func setHoldAwake() {
	holdMu.Lock()
	holdSince = time.Now()
	holdMu.Unlock()
	holdAwake.Store(true)
}

// holdAwakeExpire: staying up with Wi-Fi on is for someone to look; after holdLease it
// would only flatten the battery, so try the normal cycle again.
func holdAwakeExpire() {
	holdMu.Lock()
	expired := holdAwake.Load() && time.Since(holdSince) > holdLease
	holdMu.Unlock()
	if expired {
		log.Printf("watchdog: held awake for %v, back to the normal cycle", holdLease)
		holdAwake.Store(false)
	}
}

// noteNetwork counts refreshes in a row that found no route. A good one ends any healing.
func noteNetwork(ok bool) {
	if ok { // only the network ladder: a route says nothing about a hang elsewhere
		netFails.Store(0)
		os.Remove(stateFile("heal-net"))
		return
	}
	if n := netFails.Add(1); n >= netFailWakes {
		netFails.Store(0)
		healNet(fmt.Sprintf("no network on %d refreshes in a row", n))
	}
}

func startWatchdog(stuck time.Duration) {
	beat()
	go func() {
		for range time.Tick(watchdogInterval) {
			beatMu.Lock()
			idle := time.Since(lastBeat)
			beatMu.Unlock()
			if idle > stuck {
				heal(fmt.Sprintf("main loop made no progress for %v", idle.Round(time.Second)))
			}
		}
	}()
}

// noteSuspend counts failed suspends in a row; a good one also ends any healing.
func noteSuspend(ok bool) {
	if ok { // got past drawing, Wi-Fi off and the kernel's freeze: whatever hung has cleared
		suspendFails.Store(0)
		leftBehind.Store(0)
		os.Remove(stateFile("heal"))
		return
	}
	if n := suspendFails.Add(1); n >= watchdogFails {
		suspendFails.Store(0)
		heal(fmt.Sprintf("%d suspend failures in a row", n))
	}
}

// noteLeftBehind: runBounded gave up on a helper that wouldn't exit.
func noteLeftBehind(name string) {
	if n := leftBehind.Add(1); n >= watchdogLeft {
		leftBehind.Store(0)
		heal(fmt.Sprintf("%d helpers stuck (last %s)", n, name))
	}
}

// healStep: the step to take now, given the last step taken and when (unix seconds).
func healStep(last int, at, now int64, forget time.Duration) int {
	if last < 1 || now-at > int64(forget/time.Second) || at > now {
		return 1
	}
	return min(last+1, 3)
}

// ladder moves the ladder kept in card file name one step on and returns the step to take.
// Step 2 (restart) needs supervise.sh; a step that can't be recorded becomes onFail.
func ladder(name string, forget time.Duration, onFail int) int {
	p := stateFile(name)
	var last int
	var at int64
	fmt.Sscan(readString(p), &last, &at)
	now := time.Now().Unix()
	step := healStep(last, at, now, forget)
	if step == 2 && !supervised() {
		step = 3 // nothing would restart us
	}
	if err := writeAtomic(p, fmt.Sprintf("%d %d\n", step, now)); err != nil {
		log.Printf("watchdog: can't record the %s step (%v)", name, err)
		step = onFail
	}
	return step
}

func heal(reason string) {
	healMu.Lock()
	defer healMu.Unlock()
	if holdAwake.Load() {
		return
	}
	// unrecorded: go to the end (a reboot can't be recorded either, so that ends awake
	// with Wi-Fi on) rather than repeat a step forever
	switch ladder("heal", healForget, 3) {
	case 1:
		log.Printf("watchdog: %s: step 1, killing stuck helpers (%s), Wi-Fi back on", reason, killHelpers())
		wifiOff()
		reconnectWifi()
		beat() // give it a full period to recover
	case 2:
		log.Printf("watchdog: %s: step 2, restarting kobodash", reason)
		exit(0) // supervise.sh starts a fresh process; on trial this start counts against it
	default:
		rolledBack := onTrial() && trialRollback("hung ("+reason+")")
		rebootDevice(reason, rolledBack, true)
	}
}

// healNet: the radio keeps failing while everything else works. Its own ladder, kept for a
// day (wakes are 2 h apart, an hour would always start over): 1 a clean Wi-Fi reset
// (stale connect killed, module out and in), 2 restart kobodash, 3 reboot. Out of reboots
// it carries on with the normal cycle: a router that is simply down must not keep the
// device awake.
func healNet(reason string) {
	healMu.Lock()
	defer healMu.Unlock()
	switch ladder("heal-net", netForget, 1) { // unrecorded: a Wi-Fi reset is always safe
	case 1:
		log.Printf("watchdog: %s: network step 1, Wi-Fi reset (%s killed)", reason, killHelpers())
		wifiOff()
		reconnectWifi()
	case 2:
		log.Printf("watchdog: %s: network step 2, restarting kobodash", reason)
		exit(0)
	default: // no rollback: a router that is down says nothing about the release
		rebootDevice(reason, false, false)
	}
}

// cleanWifi: before a wake's connect, clear what an earlier one left behind (a connect or
// udhcpc still running, the Wi-Fi module still loaded after a failed off, a stale pid file
// that makes connect_to_network.sh refuse to run). A clean wake finds nothing and pays nothing.
func cleanWifi() {
	wifiMu.Lock()
	busy := connPgid > 0 && syscall.Kill(-connPgid, 0) == nil && time.Since(connStarted) < 100*time.Second
	wifiMu.Unlock()
	if busy {
		return // a connect is on its way: that is not a leftover
	}
	var left []string
	if pids, _ := helperPids(); len(pids) > 0 {
		left = append(left, "helpers")
	}
	for _, l := range strings.Split(readString("/proc/modules"), "\n") {
		if strings.HasPrefix(l, "dhd ") {
			left = append(left, "dhd loaded")
		}
	}
	if _, err := os.Stat("/run/connect_to_network.sh.pid"); err == nil {
		left = append(left, "pid file")
	}
	if len(left) == 0 {
		return
	}
	log.Printf("wifi: clean start (%s): %s killed", strings.Join(left, ", "), killHelpers())
	if wifiOff() == errLeftBehind {
		noteLeftBehind("toggle.sh")
	}
	os.Remove("/run/connect_to_network.sh.pid")
}

// helper programs kobodash runs that could be left stuck (comm names, 15 chars at most)
var helperNames = map[string]bool{"fbink": true, "toggle.sh": true, "connect_to_netw": true,
	"prepare_changin": true, "udhcpc": true, "wpa_supplicant": true, "wpa_passphrase": true,
	"wlarm_le": true, "rmmod": true, "insmod": true, "dmesg": true}

// helperPids: the helpers running now, with their names.
func helperPids() (pids []int, names []string) {
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if comm := strings.TrimSpace(readString("/proc/" + d.Name() + "/comm")); err == nil && pid != os.Getpid() && helperNames[comm] {
			pids, names = append(pids, pid), append(names, comm)
		}
	}
	return
}

// supervised: supervise.sh is alive to restart us.
func supervised() bool {
	pid, err := strconv.Atoi(strings.TrimSpace(readString("/run/kobodash-supervise.pid")))
	return err == nil && pid > 0 && syscall.Kill(pid, 0) == nil
}

// killHelpers SIGKILLs the programs kobodash runs that could be stuck; returns their names.
func killHelpers() string {
	pids, names := helperPids()
	var killed []string
	for i, p := range pids {
		if syscall.Kill(p, syscall.SIGKILL) == nil {
			killed = append(killed, names[i])
		}
	}
	if len(killed) == 0 {
		return "none running"
	}
	return strings.Join(killed, " ")
}

// rebootAllowed: the past reboot times (unix seconds) that still count, and whether another
// reboot fits under watchdogMax. A time up to a day ahead still counts (the clock was set
// back), so a clock jump can't hand out fresh reboots.
func rebootAllowed(past []int64, now int64) ([]int64, bool) {
	var keep []int64
	for _, t := range past {
		if d := now - t; d < 24*3600 && d > -24*3600 {
			keep = append(keep, t)
		}
	}
	return keep, len(keep) < watchdogMax
}

// within runs f but stops waiting for it after d (false): a stuck card or filesystem
// must not stall the watchdog or Wi-Fi. f carries on in the background.
func within(d time.Duration, f func()) bool {
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// writeAtomic replaces a small card file whole, or not at all, within 5 s.
func writeAtomic(p, s string) error {
	var err error
	if !within(5*time.Second, func() { err = writeWhole(p, s) }) {
		return fmt.Errorf("%s: write stuck", p)
	}
	return err
}

func writeWhole(p, s string) error {
	tmp := p + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = f.WriteString(s)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

// rebootDevice reboots if today's budget allows. Otherwise: into the previous binary if
// one was just put back, else (hold) stay awake with Wi-Fi on for holdLease.
func rebootDevice(reason string, rolledBack, hold bool) {
	p := stateFile("watchdog-reboots")
	var past []int64
	for _, f := range strings.Fields(readString(p)) {
		if t, err := strconv.ParseInt(f, 10, 64); err == nil {
			past = append(past, t)
		}
	}
	now := time.Now().Unix()
	keep, ok := rebootAllowed(past, now)
	var b strings.Builder
	for _, t := range append(keep, now) {
		fmt.Fprintf(&b, "%d\n", t)
	}
	if ok {
		if err := writeAtomic(p, b.String()); err != nil {
			ok = false // can't record it: a reboot loop could never be stopped
			log.Printf("watchdog: can't record the reboot (%v)", err)
		}
	}
	if !ok {
		if rolledBack && supervised() {
			log.Printf("watchdog: %s, no reboot left: restarting into the previous binary instead", reason)
			exit(0)
			return
		}
		if !hold {
			log.Printf("watchdog: %s, no reboot left: carrying on", reason)
			return
		}
		log.Printf("watchdog: %s, no reboot left: staying awake with Wi-Fi on", reason)
		setHoldAwake()
		reconnectWifi()
		return
	}
	log.Printf("watchdog: %s: step 3, rebooting", reason)
	flushLog()
	within(10*time.Second, func() { syscall.Sync() }) // a stuck filesystem must not stop the reboot
	if err := sysReboot(); err != nil {
		log.Printf("watchdog: reboot: %v", err)
		setHoldAwake()
		reconnectWifi()
	}
}
