// kobodash: standalone e-ink dashboard for a Kobo Mini running InkBox's Alpine
// base system. Fetches weather + calendars itself, draws with Go, pushes the
// frame to the panel with FBInk and reads taps from the zForce touch panel.
//
//	kobodash                       run on the device (config: /opt/kobodash/dash.conf)
//	kobodash -once out.png         render one frame to a PNG and exit (desk testing)
//	kobodash -calib                print raw touch coordinates (for TOUCH_* settings)
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Config struct {
	Place         Place // home: weather and warnings; set in the conf, else found from the IP
	PlaceSet      bool  // PLACE/LAT/LON in the conf: never relocate
	Locate        bool  // LOCATE=0 turns the IP lookup off
	TZ            string
	Feeds         []string
	Holiday       string
	Input         string
	Fbink         string
	SwapXY        bool
	InvX, InvY    bool
	MaxX, MaxY    int
	Rotate        string // cw | ccw: which way the landscape picture is turned onto the portrait panel
	RefreshMin    int    // weather (and wake-up) interval
	CalRefreshMin int    // calendar fetch interval
	IdleBackMin   int
	WifiIdleMin   int  // keep Wi-Fi up this long after the last touch (and after boot)
	WifiAlways    bool // never switch the radio off (debugging)
	Sleep         bool // suspend between refreshes (RTC alarm wakes it, so does the power button)
	AwakeSec      int  // stay awake this long after the last touch / power-button wake
	QuietFrom     int  // no timed wakes from this hour...
	QuietTo       int  // ...until this one (local); equal = no quiet hours
	KeyInput      string
	WarnRegion    string // SMHI county for official warnings
	WarnSet       bool   // WARN_REGION in the conf: keep it even if the location says otherwise
	UpdateRepo    string // GitHub owner/repo whose signed releases it installs ("" = off)
	UpdatePubKey  string // hex ed25519 public key that release binaries must verify against
	UpdateAPI     string // GitHub API base (tests point it at a local server)
	TgToken       string // Telegram bot token for low-battery alerts ("" = off)
	TgChat        string // chat id the alert goes to
	TgThread      string // optional forum topic id
	TgLogThread   string // forum topic for a silent message on every refresh ("" = off)
	BattAlert     int    // alert at this %, again at half of it
}

func loadConfig(path string) Config {
	c := Config{
		Place:         Place{"Göteborg", 57.7089, 11.9746, "Europe/Stockholm"},
		Locate:        true,
		TZ:            "Europe/Stockholm",
		Holiday:       "https://calendar.google.com/calendar/ical/sv.swedish%23holiday%40group.v.calendar.google.com/public/basic.ics",
		Input:         "/dev/input/event1",
		Fbink:         "/opt/bin/fbink/fbink",
		MaxX:          PW,
		MaxY:          PH,
		Rotate:        "cw",
		RefreshMin:    120, // battery first: each wake costs a Wi-Fi association
		CalRefreshMin: 240,
		BattAlert:     20,
		IdleBackMin:   3,
		WifiIdleMin:   10,
		Sleep:         true,
		AwakeSec:      120,
		QuietFrom:     23, // nobody reads the fridge at night: each skipped wake saves a Wi-Fi association
		QuietTo:       6,
		KeyInput:      "/dev/input/event0",
		WarnRegion:    "Västra Götaland",
		UpdateRepo:    "tbelbek/kobo-mini-n705-dashboard",
		UpdatePubKey:  releasePubKey,
		UpdateAPI:     "https://api.github.com",
	}
	f, err := os.Open(path)
	if err != nil {
		log.Printf("config %s: %v (using defaults)", path, err)
		return c
	}
	defer f.Close()
	// PLACE/LAT/LON is home; older confs had a second city in PLACE and home in PLACE2: PLACE2 then wins
	var p1, p2 Place
	var set1, set2 bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		num, _ := strconv.ParseFloat(v, 64)
		b := v == "1" || v == "true"
		switch k {
		case "ICS_URL":
			c.Feeds = strings.Split(v, ",")
		case "HOLIDAY_ICS":
			c.Holiday = v
		case "TZ":
			c.TZ = v
		case "PLACE", "LAT", "LON", "TZ1":
			set1 = true
			setPlace(&p1, k, v, num)
		case "PLACE2", "LAT2", "LON2", "TZ2":
			set2 = true
			setPlace(&p2, strings.TrimSuffix(k, "2"), v, num)
		case "LOCATE":
			c.Locate = b
		case "INPUT":
			c.Input = v
		case "TOUCH_SWAP":
			c.SwapXY = b
		case "TOUCH_INVX":
			c.InvX = b
		case "TOUCH_INVY":
			c.InvY = b
		case "TOUCH_MAXX": // 0 or junk would divide by zero on the first tap
			if num > 0 {
				c.MaxX = int(num)
			}
		case "TOUCH_MAXY":
			if num > 0 {
				c.MaxY = int(num)
			}
		case "ROTATE":
			c.Rotate = v
		case "REFRESH_MIN": // 0 would wake every 30 s
			if num >= 1 {
				c.RefreshMin = int(num)
			}
		case "CAL_REFRESH_MIN":
			c.CalRefreshMin = int(num)
		case "WIFI_IDLE_MIN":
			c.WifiIdleMin = int(num)
		case "WIFI_ALWAYS":
			c.WifiAlways = b
		case "SLEEP":
			c.Sleep = b
		case "AWAKE_SEC":
			c.AwakeSec = int(num)
		case "QUIET": // "23-6", or "off"
			c.QuietFrom, c.QuietTo = 0, 0
			if f, t, ok := strings.Cut(v, "-"); ok {
				c.QuietFrom, _ = strconv.Atoi(f)
				c.QuietTo, _ = strconv.Atoi(t)
			}
		case "KEY_INPUT":
			c.KeyInput = v
		case "UPDATE_REPO":
			c.UpdateRepo = v
		case "UPDATE_PUBKEY":
			c.UpdatePubKey = v
		case "UPDATE_API":
			c.UpdateAPI = v
		case "WARN_REGION":
			c.WarnRegion, c.WarnSet = v, true
		case "TELEGRAM_TOKEN":
			c.TgToken = v
		case "TELEGRAM_CHAT":
			c.TgChat = v
		case "TELEGRAM_THREAD":
			c.TgThread = v
		case "TELEGRAM_LOG_THREAD":
			c.TgLogThread = v
		case "BATT_ALERT":
			c.BattAlert = int(num)
		}
	}
	if set2 {
		p1, set1 = p2, true
	}
	if set1 {
		if p1.TZ == "" {
			p1.TZ = c.TZ
		}
		c.Place, c.PlaceSet = p1, true
	}
	return c
}

func setPlace(p *Place, k, v string, num float64) {
	switch k {
	case "PLACE":
		p.Name = v
	case "LAT":
		p.Lat = num
	case "LON":
		p.Lon = num
	case "TZ1", "TZ":
		p.TZ = v
	}
}

type App struct {
	cfg     Config
	shown   []byte // pixels on the panel now: an identical frame isn't redrawn
	wokeBy  string // what started this refresh: timer, button, touch, boot
	loc     *time.Location
	cal     Calendar
	wDetail *Weather
	wErr    error
	updated time.Time

	place  Place     // where the weather is for: the conf's, or the last IP lookup
	region string    // SMHI county for warnings
	locAt  time.Time // last IP lookup
	maybe  *Place    // a far-away answer waiting for a second one to confirm it

	month time.Time // first day of the shown month
	sel   time.Time // selected day: the list starts here
	page  int       // list page
	hits  []hit

	nextWake    time.Time
	warnings    []Warning
	calUpdated  time.Time
	forceCal    bool // refresh button: fetch calendars too, not just weather
	wantUpdate  bool // refresh button: also check for a new release right away
	wifiPinned  bool // switched on by hand: stays up until tapped again, or pinLease
	pinnedAt    time.Time
	skipWake    bool          // this tap switched Wi-Fi off; don't let it wake the radio again
	shownOnline bool          // Wi-Fi state in the last drawn frame
	sleeping    bool          // drawing the frame that stays up while asleep: Wi-Fi shown off
	busy        bool          // refresh button pressed, fetch in progress
	keys        chan struct{} // power-button presses
	sshSince    time.Time     // first seen the current ssh session (monotonic: awake time)
	sshExpired  bool
	reported    string    // day of the last daily report sent by this process
	weatherAt   time.Time // last successful weather fetch

}

// home: today selected, its month shown, the list on its first page.
func (a *App) home() {
	t := midnight(clock().In(a.loc), a.loc)
	a.month, a.sel, a.page = firstOfMonth(t), t, 0
}

func (a *App) atHome() bool {
	t := midnight(clock().In(a.loc), a.loc)
	return a.sel.Equal(t) && a.month.Equal(firstOfMonth(t)) && a.page == 0
}

// toggleWifi: the Wi-Fi icon. Off -> on and pinned; on (pinned or auto) -> off, back to auto.
func (a *App) toggleWifi() {
	if a.wifiPinned || online() {
		a.wifiPinned, a.skipWake = false, true
		wifiOffOnPurpose.Store(true)
		wifiDown()
	} else {
		a.wifiPinned, a.pinnedAt = true, time.Now()
		reconnectWifi()
	}
}

// onDevice: InkBox's Wi-Fi scripts exist only on the Kobo (desk renders skip the radio
// dance). Asked on every beat: looked up once.
var onDevice = sync.OnceValue(func() bool {
	_, err := os.Stat("/usr/local/bin/wifi/toggle.sh")
	return err == nil
})

func (a *App) refresh() {
	t0 := time.Now()
	// a network ladder step (Wi-Fi reset) gets its try now, in this wake: the way down would
	// switch it off again before it could help
	if onDevice() && !wifiUp(90*time.Second) && !(noteNetwork(false) && wifiUp(90*time.Second)) {
		a.wErr = fmt.Errorf("Wi-Fi bağlanamadı")
		log.Printf("wifi: no address after 90s %s", battery())
		a.updated = time.Now().Round(0) // wall clock only: monotonic time stops while suspended
		return
	}
	beat()
	if onDevice() && time.Now().Year() < 2025 { // RTC lost (flat battery): every HTTPS check fails until the clock is right
		log.Printf("clock at %s: syncing", time.Now().UTC().Format("2006-01-02"))
		if _, err := runBounded(30*time.Second, "ntpd", "-q", "-n", "-p", "pool.ntp.org"); err == nil {
			runBounded(10*time.Second, "hwclock", "-w", "-u")
		}
	}
	wifi := time.Since(t0)
	if onDevice() {
		log.Printf("wifi: up in %v", wifi.Round(100*time.Millisecond))
	}
	a.relocate()
	// the fetches run side by side: every second saved here is a second less of Wi-Fi radio
	var wg sync.WaitGroup
	var w *Weather
	var ws []Warning
	var wErr, warnErr, calErr error
	cal := a.forceCal || time.Since(a.calUpdated) >= time.Duration(a.cfg.CalRefreshMin)*time.Minute
	gotAnswer.Store(false)
	run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); defer recoverLog("fetch"); f() }() }
	run(a.batteryAlert)
	run(func() { a.wakeReport(wifi) })
	run(func() { w, wErr = fetchSMHI(a.place, 8) })
	run(func() { ws, warnErr = fetchWarnings(a.region) })
	if cal { // calendars change rarely: every CAL_REFRESH_MIN, or now when the refresh button asked
		run(func() { calErr = a.cal.Refresh() })
	}
	installed := false
	if onDevice() { // a new release? one GitHub call (a 304 when nothing is new), side by side with the rest
		run(func() { installed = a.checkUpdate() })
	}
	wg.Wait()
	if installed {
		exit(0) // supervise.sh starts the new binary, which refreshes and reports
	}
	// the network works when something came back over it, not when a route exists
	// the network works when a server answered (weather, warnings, calendars, Telegram:
	// any of them), not when a route exists and not when one service is down
	if fetched := gotAnswer.Load(); onDevice() {
		if noteNetwork(fetched) { // step 1 just reset Wi-Fi: back up for the rest of this wake
			wifiUp(90 * time.Second)
		}
		if fetched && sleptOK.Load() { // slept, woke, fetched: the release works
			trialPassed()
		}
	}
	if wErr == nil {
		a.wDetail, a.wErr = w, nil
		a.weatherAt = time.Now().Round(0)
	} else {
		a.wErr = wErr
		log.Printf("weather: %v", a.wErr)
	}
	if warnErr == nil {
		a.warnings = ws
	} else {
		log.Printf("warnings: %v", warnErr) // keep the last known list, minus those that ended
		a.warnings = current(a.warnings, time.Now())
	}
	if cal {
		if calErr != nil {
			log.Printf("calendar: %v", calErr)
		} else {
			a.calUpdated = time.Now().Round(0)
		}
		a.forceCal = false
	}
	a.updated = time.Now().Round(0)
	if onDevice() && gotAnswer.Load() {
		a.maybeDailyReport()
	}
}

// relocate: unless the conf names a place, look the city up from the IP once a day.
// The result is kept on the card so a reboot doesn't ask again; on failure the last one stays.
func (a *App) relocate() {
	every := 24 * time.Hour
	if a.maybe != nil {
		every = time.Hour // confirm a move soon
	}
	if a.cfg.PlaceSet || !a.cfg.Locate || time.Since(a.locAt) < every {
		return
	}
	p, region, err := locate()
	a.locAt = time.Now().Round(0) // also after a failure: try again later, not every wake
	if err != nil {
		log.Printf("location: %v", err)
		return
	}
	if km(p, a.place) > 50 { // a move (or both databases wrong at once): believe it the second time
		if a.maybe == nil || km(p, *a.maybe) > 50 {
			a.maybe = &p
			log.Printf("location: %s? asking again in an hour", p.Name)
			return
		}
	}
	a.maybe = nil
	a.setPlace(p, region)
	log.Printf("location: %s (%.2f, %.2f), %s", a.place.Name, p.Lat, p.Lon, a.region)
	if f := stateFile("location"); f != "" {
		os.WriteFile(f, []byte(fmt.Sprintf("%f\t%f\t%s\t%s\t%s\t%d\n", p.Lat, p.Lon, p.Name, region, p.TZ, a.locAt.Unix())), 0o644)
	}
}

// setPlace takes a looked-up place; near the conf's own one it keeps that name ("Göteborg", not "Gothenburg").
func (a *App) setPlace(p Place, region string) {
	if km(p, a.cfg.Place) < 30 {
		p.Name = a.cfg.Place.Name
	}
	if p.TZ == "" {
		p.TZ = a.cfg.TZ
	}
	a.place = p
	if !a.cfg.WarnSet && region != "" {
		a.region = region
	}
}

// loadLocation: the last lookup from the card, if any.
func (a *App) loadLocation() {
	b, err := os.ReadFile(stateFile("location"))
	if err != nil || a.cfg.PlaceSet || !a.cfg.Locate {
		return
	}
	f := strings.Split(strings.TrimSpace(string(b)), "\t")
	if len(f) != 6 {
		return
	}
	lat, _ := strconv.ParseFloat(f[0], 64)
	lon, _ := strconv.ParseFloat(f[1], 64)
	at, _ := strconv.ParseInt(f[5], 10, 64)
	a.setPlace(Place{f[2], lat, lon, f[4]}, f[3])
	a.locAt = time.Unix(at, 0)
}

// stateFile: a small file that survives reboots (on the card on the device, the user cache dir on a desk).
func stateFile(name string) string {
	if onDevice() {
		return "/tmp/p3/rootfs/write/opt/kobodash/" + name
	}
	if d, err := os.UserCacheDir(); err == nil {
		os.MkdirAll(d+"/kobodash", 0o755)
		return d + "/kobodash/" + name
	}
	return ""
}

// reconnectWifi re-runs InkBox's own Wi-Fi scripts in the background, like run.sh does at boot:
// connect even when toggle.sh on reports a failure (its status is wlarm_le up's).
// Wi-Fi operations one at a time. The connect script runs in its own process group so a
// stale one can be killed whole: left alone it waits up to 320 s for DHCP and then runs
// toggle.sh off, which would cut down a later, working connection.
var (
	wifiMu      sync.Mutex
	connPgid    int
	connStarted time.Time
)

// killConnect ends a connect still running (wifiMu held).
func killConnect() {
	if connPgid > 0 {
		syscall.Kill(-connPgid, syscall.SIGKILL)
		connPgid = 0
	}
}

func reconnectWifi() {
	wifiMu.Lock()
	busy := connPgid > 0 && syscall.Kill(-connPgid, 0) == nil && time.Since(connStarted) < 100*time.Second
	wifiMu.Unlock()
	if busy {
		return // one is already on its way
	}
	t0 := time.Now()
	wifiOffOnPurpose.Store(false)
	// trap '' PIPE: inherited by every child, so if this process dies (update, crash) while the
	// backgrounded udhcpc still holds the log pipe, its scripts get EPIPE instead of being killed.
	cmd := exec.Command("sh", "-c", `trap '' PIPE; . /opt/kobodash/kobodash.conf; /usr/local/bin/wifi/toggle.sh on; echo "toggle on: $?"; `+
		`/usr/local/bin/wifi/connect_to_network.sh "$WIFI_SSID" "$WIFI_PSK"; echo "connect: $?"`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// the scripts' output goes to the card: when a wake gets no address, this is the only trace
	f := openWifiLog(t0) // card I/O, bounded and kept out of wifiMu: a stuck card must not block Wi-Fi recovery
	// Each line is stamped with seconds since t0, to show which step (module, scan, DHCP)
	// the time goes to. Our own pipe, read to EOF: udhcpc forks into the background with
	// this stdout, and closing the read end under it would kill it with SIGPIPE.
	var pr, pw *os.File
	if f != nil {
		var err error
		if pr, pw, err = os.Pipe(); err == nil {
			cmd.Stdout, cmd.Stderr = pw, pw
		} else {
			cmd.Stdout, cmd.Stderr = f, f
		}
	}
	wifiMu.Lock()
	killConnect()
	err := cmd.Start()
	if err == nil {
		connPgid, connStarted = cmd.Process.Pid, t0
	}
	wifiMu.Unlock()
	if pw != nil {
		pw.Close() // the children hold their own copies
	}
	if err != nil {
		log.Printf("wifi: start: %v", err)
		if pr != nil {
			pr.Close()
		}
		if f != nil {
			f.Close()
		}
		return
	}
	go func() {
		if pr != nil {
			io.Copy(&stamped{w: f, t0: t0, bol: true}, pr) // ends when the last child lets go
			pr.Close()
		}
		if f != nil {
			f.Close()
		}
	}()
	pgid := cmd.Process.Pid
	go func() {
		err := cmd.Wait() // exit 255 = connect_to_network.sh thinks another copy is running
		wifiMu.Lock()
		if connPgid == pgid { // gone: its number may be handed to someone else
			connPgid = 0
		}
		wifiMu.Unlock()
		log.Printf("wifi: connect script done after %v: %v", time.Since(t0).Round(100*time.Millisecond), err)
	}()
}

// openWifiLog rotates wifi.log, opens it and writes the connect header, all of it given up
// after 5 s (nil: connect without a log).
func openWifiLog(t0 time.Time) *os.File {
	done := make(chan *os.File, 1) // buffered: a late open after the timeout still finishes
	if !within(5*time.Second, func() {
		wl := stateFile("wifi.log")
		if st, err := os.Stat(wl); err == nil && st.Size() > 256<<10 {
			os.Rename(wl, wl+".old")
		}
		f, err := os.OpenFile(wl, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "== %s connect\n", t0.UTC().Format("2006-01-02 15:04:05"))
		}
		done <- f
	}) {
		go func() { // opened after all, too late: close it
			if f := <-done; f != nil {
				f.Close()
			}
		}()
		return nil
	}
	return <-done
}

// cardLog writes the log to the card from its own goroutine. A card that stalls must not
// stall whoever logs (the main loop, the watchdog): lines that don't fit the queue are
// dropped, the copy in /tmp (stderr) still has them.
// It also rotates the file (256 KB, one .old): the process can run for weeks.
func cardLog(path string) io.Writer {
	q := make(chan []byte, 256)
	go func() {
		var f *os.File
		var size int64
		for b := range q {
			if f == nil || size > 256<<10 {
				if f != nil {
					f.Close()
					os.Rename(path, path+".old")
				}
				if st, err := os.Stat(path); err == nil && st.Size() > 256<<10 {
					os.Rename(path, path+".old")
				}
				f, _ = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
				size = 0
				if st, err := os.Stat(path); err == nil {
					size = st.Size()
				}
			}
			if f != nil {
				n, _ := f.Write(b)
				size += int64(n)
			}
			cardPending.Add(-1)
		}
	}()
	return writerFunc(func(p []byte) (int, error) {
		cardPending.Add(1)
		select {
		case q <- append([]byte(nil), p...):
		default:
			cardPending.Add(-1)
		}
		return len(p), nil
	})
}

var cardPending atomic.Int32

// flushLog gives the card log up to 2 s to catch up before the process exits or reboots.
func flushLog() {
	for end := time.Now().Add(2 * time.Second); cardPending.Load() > 0 && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
	}
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

// stamped prefixes every line written through it with the seconds since t0. It never reports
// a write error (a full card, say): io.Copy would stop draining the pipe and block the scripts.
type stamped struct {
	w   io.Writer
	t0  time.Time
	bol bool
}

func (s *stamped) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		if s.bol {
			fmt.Fprintf(s.w, "+%5.2fs ", time.Since(s.t0).Seconds())
			s.bol = false
		}
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.w.Write(p)
			break
		}
		s.w.Write(p[:i+1])
		p, s.bol = p[i+1:], true
	}
	return n, nil
}

// wifiUp brings the radio up and waits until it can reach the internet: udhcpc sets
// the address a moment before the default route, and fetches started in between all
// fail with "network is unreachable".
func wifiUp(timeout time.Duration) bool {
	if routed() {
		return true
	}
	cleanWifi()
	reconnectWifi()
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(250 * time.Millisecond) {
		if routed() {
			return true
		}
	}
	return false
}

// routed: eth0 has an IPv4 address and the default route.
func routed() bool {
	b, _ := os.ReadFile("/proc/net/route")
	return online() && hasDefaultRoute(string(b))
}

func hasDefaultRoute(table string) bool {
	for _, l := range strings.Split(table, "\n")[1:] {
		if f := strings.Fields(l); len(f) > 2 && f[0] == "eth0" && f[1] == "00000000" {
			return true
		}
	}
	return false
}

// The radio is the biggest battery drain, so it's off unless something needs it.
// Bounded: on 2 Nov a v0.7.11 process logged nothing after its first refresh and
// never reached "sleep:" for 5 hours; toggle.sh off is the blocking call on that path.
func wifiDown() {
	if wifiOff() == errLeftBehind {
		noteLeftBehind("toggle.sh") // after wifiMu is released: healing takes it again
	}
}

// wifiOff switches the radio off; the watchdog calls it directly (no escalation from inside).
func wifiOff() error {
	wifiMu.Lock()
	defer wifiMu.Unlock()
	killConnect()
	_, err := runBounded(30*time.Second, "/usr/local/bin/wifi/toggle.sh", "off")
	offFailed.Store(err != nil)
	if err != nil {
		log.Printf("wifi off: %v", err)
	}
	return err
}

// runBounded runs a command but gives up waiting after d. A process stuck in the kernel
// (an e-ink update that never completes, an rmmod) can't be killed, and waiting for it
// would stall the main loop with Wi-Fi already off: the device would neither sleep nor
// be reachable. Past d it is left behind and the loop goes on.
func runBounded(d time.Duration, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // its own group: a timeout kills the children too
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.Bytes(), err
	case <-time.After(d):
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		log.Printf("%s: no exit after %v, killed", filepath.Base(name), d)
		return nil, errLeftBehind // callers report it with noteLeftBehind once their locks are free
	}
}

// sshHold: an ssh session keeps it awake, for sshLease of awake time at most. A connection
// whose other end vanished can stay ESTABLISHED for hours while the address is still there,
// and would keep the device up until the battery is flat.
func (a *App) sshHold() bool {
	if !sshActive() {
		a.sshSince = time.Time{}
		return false
	}
	if a.sshSince.IsZero() {
		a.sshSince = time.Now()
	}
	if time.Since(a.sshSince) > sshLease {
		if !a.sshExpired {
			log.Printf("ssh: open for %v, no longer keeping it awake", sshLease)
		}
		a.sshExpired = true
		return false
	}
	a.sshExpired = false
	return true
}

const sshLease = 3 * time.Hour

// sshActive: an established connection on local port 22 (0x0016) — never cut it.
func sshActive() bool {
	if !online() { // without Wi-Fi a session can't be alive; a dead one can stay ESTABLISHED for hours
		return false
	}
	b, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(l)
		if len(f) > 3 && strings.HasSuffix(f[1], ":0016") && f[3] == "01" {
			return true
		}
	}
	return false
}

const cpufreq = "/sys/devices/system/cpu/cpu0/cpufreq/"

// ensureScaling: InkBox leaves the CPU pinned at 800 MHz ("userspace"). Scale with load
// instead: conservative, the best the Mini's 2.6.35 kernel has (no ondemand); measured
// 3 Nov: half the awake time at 160 MHz, a fifth at 800.
func ensureScaling() {
	const want = "conservative"
	cur, err := os.ReadFile(cpufreq + "scaling_governor")
	if err != nil {
		return // not on the device
	}
	if strings.TrimSpace(string(cur)) != want {
		if err := os.WriteFile(cpufreq+"scaling_governor", []byte(want), 0644); err != nil {
			log.Printf("governor: %v", err)
		}
	}
}

const framePath = "/tmp/kobodash.png"

func (a *App) show() {
	// odd data (a feed, a forecast) that trips the drawing code must not kill the process:
	// supervise.sh would restart it into the same data, over and over. Keep the old frame.
	defer recoverLog("show")
	frame := panelFrame(a.render(), a.cfg.Rotate)
	if bytes.Equal(frame.Pix, a.shown) {
		return // a full e-ink flash costs about a second of panel power for nothing
	}
	if err := writePNG(framePath, frame); err != nil {
		log.Printf("frame: %v", err)
		return
	}
	// -f: full flashing refresh, clears ghosting on every page change
	if out, err := runBounded(30*time.Second, a.cfg.Fbink, "-q", "-f", "-g", "file="+framePath); err != nil {
		if err == errLeftBehind {
			noteLeftBehind("fbink")
		}
		log.Printf("fbink: %v %s", err, out)
		return
	}
	a.shown = frame.Pix
}

// recoverLog, deferred, turns a panic into a log line.
func recoverLog(where string) {
	if r := recover(); r != nil {
		log.Printf("%s: panic: %v", where, r)
	}
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// forever runs an input reader again 30 s after it gives up (a read error, a device that
// went away), so a glitch doesn't leave the screen deaf until the next restart.
func forever(f func()) {
	for {
		f()
		time.Sleep(30 * time.Second)
	}
}

// readTouch turns zForce events into taps (on finger lift) in screen coordinates.
func readTouch(c Config, calib bool, taps chan<- image.Point) {
	f, err := os.Open(c.Input)
	if err != nil {
		log.Printf("touch: %v", err)
		return
	}
	defer f.Close()
	const evioCGrab = 0x40044590 // keep InkBox daemons from also acting on our taps
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), evioCGrab, 1)
	buf := make([]byte, 16) // struct input_event on 32-bit ARM
	var x, y int
	down := false
	for {
		if _, err := io.ReadFull(f, buf); err != nil {
			log.Printf("touch read: %v", err)
			return
		}
		typ, code := binary.LittleEndian.Uint16(buf[8:]), binary.LittleEndian.Uint16(buf[10:])
		val := int(int32(binary.LittleEndian.Uint32(buf[12:])))
		switch {
		case typ == 3 && (code == 0x00 || code == 0x35): // ABS_X / ABS_MT_POSITION_X
			x = val
		case typ == 3 && (code == 0x01 || code == 0x36): // ABS_Y / ABS_MT_POSITION_Y
			y = val
		case typ == 1 && code == 0x14a: // BTN_TOUCH
			if val == 1 {
				down = true
			} else if down {
				down = false
				if calib {
					fmt.Printf("raw x=%d y=%d\n", x, y)
				}
				taps <- c.toScreen(x, y)
			}
		}
	}
}

func (c Config) toScreen(x, y int) image.Point {
	if c.SwapXY {
		x, y = y, x
	}
	sx, sy := x*PW/c.MaxX, y*PH/c.MaxY // panel pixels (portrait)
	if c.InvX {
		sx = PW - 1 - sx
	}
	if c.InvY {
		sy = PH - 1 - sy
	}
	return toLogical(image.Pt(sx, sy), c.Rotate)
}

func main() {
	confPath := flag.String("conf", "/opt/kobodash/dash.conf", "config file")
	once := flag.String("once", "", "render one frame to this PNG and exit")
	sel := flag.String("sel", "", "with -once: select this day (2006-01-02) instead of today")
	calib := flag.Bool("calib", false, "print raw touch coordinates")
	flag.String("server", "", "ignored; run.sh from the image still passes it")
	stest := flag.Int("suspendtest", 0, "arm the RTC alarm N seconds ahead, suspend, report how long it slept")
	fakeNow := flag.String("now", "", "with -once: pretend it is this local time (2006-01-02T15:04)")
	fakeWarn := flag.Bool("fakewarn", false, "with -once: draw sample SMHI warnings and battery")
	genkey := flag.String("genkey", "", "release tooling: write a new ed25519 private key here, print the public key")
	sign := flag.String("sign", "", "release tooling: sign the file given as argument with this private key")
	showVersion := flag.Bool("version", false, "print version")
	notify := flag.String("notify", "", "send this text to the Telegram alert chat and exit (supervise.sh's rescue mode uses it)")
	wdTest := flag.Bool("watchdogtest", false, "on the device: start, then hang the main loop; the watchdog must reboot within ~3 min")
	flag.Parse()

	switch {
	case *showVersion:
		fmt.Println(version)
		return
	case *genkey != "":
		if err := genKey(*genkey); err != nil {
			log.Fatal(err)
		}
		return
	case *sign != "":
		if err := signFile(*sign, flag.Arg(0)); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *stest > 0 {
		suspendTest(*stest)
		return
	}

	cfg := loadConfig(*confPath)
	loc, err := time.LoadLocation(cfg.TZ)
	if err != nil {
		loc = time.UTC
	}
	a := &App{cfg: cfg, loc: loc, cal: Calendar{Feeds: cfg.Feeds, Holiday: cfg.Holiday}, place: cfg.Place, region: cfg.WarnRegion, wokeBy: "açılış"}
	if *notify != "" {
		if !a.telegram(cfg.TgThread, *notify, false) {
			os.Exit(1)
		}
		return
	}
	a.loadLocation()
	a.home()

	if *once != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04", *fakeNow, loc); err == nil {
			clock = func() time.Time { return t }
		}
		a.refresh()
		if *fakeWarn {
			a.warnings = []Warning{{"ORANGE", reasonTR("Rain"), time.Time{}}, {"YELLOW", reasonTR("Wind"), time.Time{}}}
			fakeBattery = "pil %85"
		}
		a.home()
		if t, err := time.ParseInLocation("2006-01-02", *sel, loc); err == nil {
			a.month, a.sel = firstOfMonth(t), t
		}
		if err := writePNG(*once, a.render()); err != nil { // landscape, as people see it
			log.Fatal(err)
		}
		return
	}

	if onDevice() { // /tmp is gone after a reboot; keep a copy on the card to see what happened overnight
		log.SetOutput(io.MultiWriter(os.Stderr, cardLog("/tmp/p3/rootfs/write/opt/kobodash/kobodash.log")))
	}
	log.Printf("kobodash %s starting", version)
	if onDevice() && *wdTest {
		startWatchdog(2 * time.Minute)
		log.Printf("watchdog test: main loop hangs now")
		select {}
	}
	if onDevice() && cfg.Sleep {
		startWatchdog(watchdogStuck)
	}
	if onDevice() {
		trialStart()
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			s := <-stop
			trialStopped()
			log.Printf("stopped by %v", s)
			exit(0)
		}()
	}
	runBounded(10*time.Second, "killall", "-9", "inkbox-splash")             // InkBox's boot splash keeps drawing otherwise
	os.WriteFile("/sys/class/leds/pmic_ledsg/brightness", []byte("0"), 0644) // ipd used to switch the LED off
	taps := make(chan image.Point, 8)
	keys := make(chan struct{}, 4)
	go forever(func() { readTouch(cfg, *calib, taps) })
	go forever(func() { readPowerKey(cfg.KeyInput, keys) })
	a.keys = keys

	ensureScaling()
	updated, _ := os.ReadFile(stateFile("updated")) // set by the binary that installed us
	rmState("updated")
	if len(updated) > 0 {
		a.wokeBy = "güncelleme"
	}
	a.refresh()
	a.show()
	tick := time.NewTicker(10 * time.Second)
	lastTouch, lastTry, resumed := time.Now(), time.Now(), time.Time{}
	if up, _ := strconv.ParseFloat(strings.Fields(readProc("/proc/uptime") + " 0")[0], 64); up > 600 {
		// not a boot: a restart (crash, watchdog, deploy) with likely nobody looking; don't
		// stay up two minutes with Wi-Fi on, sleep once the refresh is done
		lastTouch = time.Time{}
	}
	lastBeat := time.Now()
	if len(updated) > 0 {
		if a.wErr == nil { // came up fine: say so and go straight back to sleep, nobody is looking
			if cfg.TgLogThread != "" {
				a.telegram(cfg.TgLogThread, "Kobo pano güncellendi: "+string(updated)+" · çalışıyor, uyuyor", true)
			}
			lastTouch = time.Time{}
			keys <- struct{}{} // wakes the select below, whose power-key branch puts it to sleep
		} else if cfg.TgLogThread != "" {
			a.telegram(cfg.TgLogThread, "Kobo pano güncellendi: "+string(updated)+" · ama: "+a.wErr.Error(), true)
		}
	}
	wifiIdle := time.Duration(cfg.WifiIdleMin) * time.Minute
	awakeIdle := time.Duration(cfg.AwakeSec) * time.Second
	sleepNow := false
	for {
		beat()
		select {
		case <-keys:
			if time.Since(resumed) > 3*time.Second { // the press that woke us is delivered after resume; ignore it
				sleepNow = true
			}
		case p := <-taps:
			lastTouch, sleepNow = time.Now(), false // someone is using it: an earlier press to sleep is void
			a.wokeBy = "dokunma"
			a.skipWake = false
			for i := len(a.hits) - 1; i >= 0; i-- {
				if p.In(a.hits[i].r) {
					a.hits[i].fn()
					a.show()
					break
				}
			}
			if !online() && !a.skipWake {
				reconnectWifi() // a touch opens a Wi-Fi window (ssh, refresh) without blocking the UI
			}
		case <-tick.C:
			if !a.atHome() && time.Since(lastTouch) > time.Duration(cfg.IdleBackMin)*time.Minute {
				a.home() // nobody is looking: back to today
				a.show()
			}
			due := !time.Now().Before(a.nextSlot(a.updated))
			retry := a.wErr != nil && time.Since(lastTry) >= time.Minute
			if due || retry {
				lastTry = time.Now()
				a.refresh()
				a.show()
			}
			ensureScaling()
			if time.Since(lastBeat) >= 10*time.Minute { // awake this long means something is holding it up
				lastBeat = time.Now()
				log.Printf("awake: idle %v, ssh %v, pinned %v, online %v %s", time.Since(lastTouch).Round(time.Second), sshActive(), a.wifiPinned, online(), battery())
			}
			if a.wifiPinned && time.Since(a.pinnedAt) > pinLease && time.Since(lastTouch) > pinLease && !sshActive() {
				a.wifiPinned = false // forgotten on: a pinned radio would keep it awake until the battery is flat
				log.Printf("wifi: pinned for %v with nobody around, unpinning", pinLease)
			}
			holdAwakeExpire()
			if !cfg.WifiAlways && !a.wifiPinned && online() && time.Since(lastTouch) > wifiIdle && !a.sshHold() && !holdAwake.Load() {
				wifiOffOnPurpose.Store(true) // awake without Wi-Fi by design: supervise.sh must not call it lost
				wifiDown()
			}
			if online() != a.shownOnline {
				a.show() // keep the Wi-Fi icon honest
			}
		}

		// Power model: asleep unless someone is using it. Never while ssh'd in or Wi-Fi is pinned.
		// A loop, not an if: after a timed wake it goes straight back down instead of
		// idling with Wi-Fi up until the next 10 s tick.
		for cfg.Sleep && (sleepNow || time.Since(lastTouch) > awakeIdle) && !a.wifiPinned && !a.sshHold() && !holdAwake.Load() {
			sleepNow = false
			woke := a.sleepUntil(a.nextSlot(a.updated))
			a.wokeBy = map[string]string{"timer": "zamanlayıcı", "button": "güç tuşu"}[woke]
			resumed = time.Now()
			if woke == "timer" {
				// lastTouch from before the sleep still looks recent (monotonic time stopped while
				// asleep): forget it, or a tap just before a press-to-sleep keeps this wake up 2 min
				lastTouch = time.Time{}
				lastTry = time.Now()
				a.refresh()
				if a.sshHold() || a.wifiPinned || holdAwake.Load() {
					a.show() // it won't go straight back down, so show the new data now
				}
				// no frame here: sleepUntil draws the one that stays up, and an e-ink flash
				// per wake costs panel power
			} else {
				lastTouch = time.Now() // power button (or a failed sleep): stay up for a while
				a.show()
				if time.Since(a.updated) > 15*time.Minute {
					a.refresh()
					a.show()
				}
				// the press that woke it is delivered after this work, which can take longer
				// than the 3 s grace: count the grace from here, or that press sends it back down
				resumed = time.Now()
			}
		}
	}
}

// nextSlot: the timed wake after a refresh at t. Wakes sit on a fixed clock grid
// (REFRESH_MIN=120: 06:00, 08:00, ... 22:00) so a power-button wake in between doesn't
// shift them; a refresh up to 30 min before a slot counts as that slot (a quarter of the
// interval when that is shorter, so a 3-minute test loop still runs).
func (a *App) nextSlot(t time.Time) time.Time {
	every := time.Duration(a.cfg.RefreshMin) * time.Minute
	if every <= 0 || t.IsZero() { // never refreshed: due now
		return t
	}
	early := 30 * time.Minute
	if early > every/4 {
		early = every / 4
	}
	t = t.Add(early)
	// slots are local wall-clock times built with time.Date, not midnight + n*2h, so they
	// stay on even hours across a DST change (a nonexistent 02:00 normalises to 03:00)
	l := t.In(a.loc)
	for day := 0; day < 3; day++ {
		for m := 0; m < 24*60; m += a.cfg.RefreshMin {
			if s := time.Date(l.Year(), l.Month(), l.Day()+day, 0, m, 0, 0, a.loc); s.After(t) {
				return a.wakeAt(s)
			}
		}
	}
	return a.wakeAt(t)
}

// wakeAt: due, or the end of the quiet hours when due falls inside them.
func (a *App) wakeAt(due time.Time) time.Time {
	f, t := a.cfg.QuietFrom, a.cfg.QuietTo
	l := due.In(a.loc)
	h := l.Hour()
	if f == t || !(f < t && h >= f && h < t || f > t && (h >= f || h < t)) {
		return due
	}
	end := time.Date(l.Year(), l.Month(), l.Day(), t, 0, 0, 0, a.loc)
	if !end.After(l) {
		end = end.AddDate(0, 0, 1)
	}
	return end
}

// sleepUntil suspends until due (RTC alarm) or the power button. Returns "timer",
// "button", or "error" when it could not sleep safely.
func (a *App) sleepUntil(due time.Time) string {
	a.nextWake = due
	a.home()
	// draw first, Wi-Fi off last: if anything on the way down hangs or fails, the device
	// is still reachable instead of awake with its radio off
	a.sleeping = true
	defer func() { a.sleeping = false }()
	t := time.Now()
	a.show() // this frame stays on the panel while asleep
	log.Printf("sleep: drawing took %v", time.Since(t).Round(100*time.Millisecond))
	beat()
	if holdAwake.Load() { // the watchdog gave up on the way down: stay reachable
		return "error"
	}
	t = time.Now()
	wifiDown()
	log.Printf("sleep: wifi off took %v", time.Since(t).Round(100*time.Millisecond))
	beat()
	if holdAwake.Load() {
		reconnectWifi()
		return "error"
	}
	due = due.Round(0)
	d := due.Sub(time.Now().Round(0))
	if d < 30*time.Second {
		d = 30 * time.Second
	}
	if d > maxSleep { // a clock that jumped can't make it sleep for a day
		log.Printf("sleep: %v asked, capped at %v", d.Round(time.Second), maxSleep)
		d = maxSleep
	}
	due = time.Now().Round(0).Add(d) // what the alarm is really set for: wake classification and the screen use it
	if due.In(a.loc).Format("15:04") != a.nextWake.In(a.loc).Format("15:04") {
		a.nextWake = due
		a.show() // the frame says "sonraki HH:MM": the 30 s floor or the cap moved it
	}
	a.nextWake = due
	if err := setWakeAlarm(d); err != nil {
		log.Printf("alarm: %v", err) // never suspend without a way back
		awakeAfterFailedSleep()
		return "error"
	}
	wifiMu.Lock()
	killConnect() // a connect the watchdog started on the way down must not resume half-done
	wifiMu.Unlock()
	httpClient.CloseIdleConnections() // the router forgets them while we sleep; reusing one stalls for 25 s
	log.Printf("sleep: alarm in %v (next update %s) %s", d.Round(time.Second), due.In(a.loc).Format("15:04:05"), battery())
	start := time.Now().Round(0) // wall clock; monotonic time stops in suspend
	pressed := func() bool {     // pressed on the way down, up to the last moment: someone wants it awake
		select {
		case <-a.keys:
			return true
		default:
			return false
		}
	}
	if err := suspend(pressed); err == errStayAwake {
		log.Printf("sleep: power button pressed on the way down, staying awake")
		reconnectWifi()
		return "button"
	} else if err != nil {
		// how long the kernel tried tells the cause apart: ~22 s (2 s settle + 20 s) = the freezer
		// gave up on a task, ~2 s = a lock or a driver; the kernel log names the task or driver
		log.Printf("suspend: %v after %v", err, time.Now().Round(0).Sub(start).Round(100*time.Millisecond))
		kernelLog()
		awakeAfterFailedSleep()
		return "error"
	}
	noteSuspend(true)
	sleptOK.Store(true)
	woke := "button"
	if time.Now().Round(0).After(due.Add(-15 * time.Second)) { // the alarm fires within 1-2 s (logs); a press just before it is a button wake
		woke = "timer"
	}
	log.Printf("woke: %s after %v %s", woke, time.Now().Round(0).Sub(start).Round(time.Second), battery())
	return woke
}

// maxSleep: the longest single sleep. Quiet hours 23-06 plus the slot after need 9 h.
const maxSleep = 10 * time.Hour

// awakeAfterFailedSleep: staying awake after all, so be reachable (Wi-Fi back on) and
// let the watchdog count it.
func awakeAfterFailedSleep() {
	reconnectWifi()
	noteSuspend(false)
}

// kernelLog appends the kernel log's last 300 lines to suspend-fail.log on the card (kept small).
func kernelLog() {
	const lines = 300
	out, err := runBounded(10*time.Second, "dmesg")
	if err != nil {
		log.Printf("dmesg: %v", err)
		return
	}
	l := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(l) > lines {
		l = l[len(l)-lines:]
	}
	p := stateFile("suspend-fail.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 128<<10 {
		os.Rename(p, p+".old")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "== %s\n%s\n", time.Now().UTC().Format("2006-01-02 15:04:05"), strings.Join(l, "\n"))
	// tasks not plainly sleeping, with where they wait in the kernel; names only, never the
	// command line (connect_to_network.sh carries the Wi-Fi password in its arguments)
	fmt.Fprintln(f, "-- pid comm state wchan (state != S)")
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		if d.Name()[0] < '1' || d.Name()[0] > '9' {
			continue
		}
		st, _ := os.ReadFile("/proc/" + d.Name() + "/stat")
		i := bytes.LastIndexByte(st, ')')
		if i < 0 || i+2 >= len(st) || st[i+2] == 'S' {
			continue
		}
		comm, _ := os.ReadFile("/proc/" + d.Name() + "/comm")
		wchan, _ := os.ReadFile("/proc/" + d.Name() + "/wchan")
		fmt.Fprintf(f, "%s %s %c %s\n", d.Name(), bytes.TrimSpace(comm), st[i+2], wchan)
	}
}

// readPowerKey reports power-button presses (the Mini's only key, on mxckpd).
func readPowerKey(dev string, keys chan<- struct{}) {
	f, err := os.Open(dev)
	if err != nil {
		log.Printf("power key: %v", err)
		return
	}
	defer f.Close()
	const evioCGrab = 0x40044590
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), evioCGrab, 1)
	buf := make([]byte, 16)
	for {
		if _, err := io.ReadFull(f, buf); err != nil {
			log.Printf("power key read: %v", err)
			return
		}
		if binary.LittleEndian.Uint16(buf[8:]) == 1 && binary.LittleEndian.Uint32(buf[12:]) == 1 { // EV_KEY press
			select {
			case keys <- struct{}{}:
			default:
			}
		}
	}
}

// alertStep decides whether a battery reading is worth a message. last is the
// lowest level already reported (0 = none); it resets once the cell is charged.
func alertStep(pct, at, last int) (send bool, next int) {
	switch {
	case pct > at+10:
		return false, 0
	case pct <= at/2 && (last == 0 || last > at/2):
		return true, at / 2
	case pct <= at && last == 0:
		return true, at
	}
	return false, last
}

// batteryAlert sends a Telegram message when the battery drops past BATT_ALERT
// and again past half of it. Runs while Wi-Fi is up, so a missed send retries next wake.
func (a *App) batteryAlert() {
	if a.cfg.TgToken == "" || a.cfg.TgChat == "" {
		return
	}
	b, err := os.ReadFile(battDir + "capacity")
	if err != nil {
		return
	}
	pct, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return
	}
	f := stateFile("battalert")
	raw, _ := os.ReadFile(f)
	last, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	send, next := alertStep(pct, a.cfg.BattAlert, last)
	if send {
		if !a.telegram(a.cfg.TgThread, fmt.Sprintf("Kobo pano: pil %%%d, şarja tak.", pct), false) {
			return
		}
		log.Printf("telegram: battery alert at %d%%", pct)
	}
	if next != last {
		os.WriteFile(f, []byte(strconv.Itoa(next)), 0o644)
	}
}

// telegram sends one message to a forum topic of TELEGRAM_CHAT; false if it didn't go.
func (a *App) telegram(thread, text string, silent bool) bool {
	if a.cfg.TgToken == "" || a.cfg.TgChat == "" {
		return false
	}
	v := url.Values{"chat_id": {a.cfg.TgChat}, "text": {text}}
	if thread != "" {
		v.Set("message_thread_id", thread)
	}
	if silent {
		v.Set("disable_notification", "true")
	}
	resp, err := httpClient.PostForm("https://api.telegram.org/bot"+a.cfg.TgToken+"/sendMessage", v)
	if err != nil {
		log.Printf("telegram: send failed") // the error text carries the token in its URL
		return false
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("telegram: HTTP %d", resp.StatusCode)
		return false
	}
	log.Printf("telegram: sent (%d chars)", len(text))
	return true
}

// wakeReport is the silent per-refresh line: enough to see the battery curve and Wi-Fi times.
func (a *App) wakeReport(wifi time.Duration) {
	if a.cfg.TgLogThread == "" {
		return
	}
	msg := fmt.Sprintf("Kobo pano %s · uyanış: %s · %s · Wi-Fi %v",
		version, a.wokeBy, battery(), wifi.Round(100*time.Millisecond))
	a.telegram(a.cfg.TgLogThread, msg, true)
}

const battDir = "/sys/class/power_supply/mc13892_bat/"
