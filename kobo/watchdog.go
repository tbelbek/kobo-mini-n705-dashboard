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
)

var (
	beatMu       sync.Mutex
	lastBeat     time.Time
	healMu       sync.Mutex // one recovery or update-file change at a time
	suspendFails atomic.Int32
	leftBehind   atomic.Int32
	holdAwake    atomic.Bool // out of reboots: keep Wi-Fi on and don't suspend
)

// beat: the main loop is alive.
func beat() {
	beatMu.Lock()
	lastBeat = time.Now()
	beatMu.Unlock()
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
	if ok {
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
func healStep(last int, at, now int64) int {
	if last < 1 || now-at > int64(healForget/time.Second) || at > now {
		return 1
	}
	if last >= 3 {
		return 3
	}
	return last + 1
}

func heal(reason string) {
	healMu.Lock()
	defer healMu.Unlock()
	if holdAwake.Load() {
		return
	}
	p := stateFile("heal")
	var last int
	var at int64
	fmt.Sscan(readString(p), &last, &at)
	now := time.Now().Unix()
	step := healStep(last, at, now)
	if step == 2 && !supervised() {
		step = 3 // nothing would restart us
	}
	if err := writeAtomic(p, fmt.Sprintf("%d %d\n", step, now)); err != nil {
		// the ladder can't move on without its state: go to the end (a reboot can't be
		// recorded either, so that ends awake with Wi-Fi on) instead of repeating a step
		log.Printf("watchdog: can't record the heal step (%v)", err)
		step = 3
	}
	switch step {
	case 1:
		log.Printf("watchdog: %s: step 1, killing stuck helpers (%s), Wi-Fi back on", reason, killHelpers())
		reconnectWifi()
		beat() // give it a full period to recover
	case 2:
		log.Printf("watchdog: %s: step 2, restarting kobodash", reason)
		exit(0) // supervise.sh starts a fresh process; on trial this start counts against it
	default:
		rolledBack := onTrial() && trialRollback("hung ("+reason+")")
		rebootDevice(reason, rolledBack)
	}
}

// supervised: supervise.sh is alive to restart us.
func supervised() bool {
	pid, err := strconv.Atoi(strings.TrimSpace(readString("/run/kobodash-supervise.pid")))
	return err == nil && pid > 0 && syscall.Kill(pid, 0) == nil
}

// killHelpers SIGKILLs the programs kobodash runs that could be stuck; returns their names.
func killHelpers() string {
	helpers := map[string]bool{"fbink": true, "toggle.sh": true, "connect_to_netw": true,
		"prepare_changin": true, "udhcpc": true, "wpa_supplicant": true, "wpa_passphrase": true,
		"wlarm_le": true, "rmmod": true, "insmod": true, "dmesg": true}
	var killed []string
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		comm := strings.TrimSpace(readString("/proc/" + d.Name() + "/comm")) // 15 chars at most
		if helpers[comm] && syscall.Kill(pid, syscall.SIGKILL) == nil {
			killed = append(killed, comm)
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

// writeAtomic replaces a small card file whole, or not at all, and gives up after 5 s:
// a stuck card must not stall the watchdog.
func writeAtomic(p, s string) error {
	done := make(chan error, 1)
	go func() { done <- writeWhole(p, s) }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("%s: write stuck", p)
	}
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

func rebootDevice(reason string, rolledBack bool) {
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
		log.Printf("watchdog: %s, no reboot left: staying awake with Wi-Fi on", reason)
		holdAwake.Store(true)
		reconnectWifi()
		return
	}
	log.Printf("watchdog: %s: step 3, rebooting", reason)
	synced := make(chan struct{})
	go func() { syscall.Sync(); close(synced) }()
	select { // a stuck filesystem must not stop the reboot
	case <-synced:
	case <-time.After(10 * time.Second):
	}
	if err := sysReboot(); err != nil {
		log.Printf("watchdog: reboot: %v", err)
		holdAwake.Store(true)
		reconnectWifi()
	}
}
