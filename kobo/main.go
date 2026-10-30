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
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	Places        [2]Place
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
	KeyInput      string
	WarnRegion    string // SMHI county for official warnings
	UpdateRepo    string // GitHub owner/repo whose signed releases it installs ("" = off)
	UpdatePubKey  string // hex ed25519 public key that release binaries must verify against
	UpdateHours   int
	UpdateAPI     string // GitHub API base (tests point it at a local server)
}

func loadConfig(path string) Config {
	c := Config{
		Places: [2]Place{
			{"İstanbul", 41.0082, 28.9784, "Europe/Istanbul"},
			{"Göteborg", 57.7089, 11.9746, "Europe/Stockholm"},
		},
		TZ:            "Europe/Stockholm",
		Holiday:       "https://calendar.google.com/calendar/ical/sv.swedish%23holiday%40group.v.calendar.google.com/public/basic.ics",
		Input:         "/dev/input/event1",
		Fbink:         "/opt/bin/fbink/fbink",
		MaxX:          PW,
		MaxY:          PH,
		Rotate:        "cw",
		RefreshMin:    60, // Open-Meteo is hourly anyway; each wake costs a Wi-Fi association
		CalRefreshMin: 240,
		IdleBackMin:   3,
		WifiIdleMin:   10,
		Sleep:         true,
		AwakeSec:      120,
		KeyInput:      "/dev/input/event0",
		WarnRegion:    "Västra Götaland",
		UpdateRepo:    "tbelbek/kobo-mini-n705-dashboard",
		UpdatePubKey:  releasePubKey,
		UpdateHours:   6,
		UpdateAPI:     "https://api.github.com",
	}
	f, err := os.Open(path)
	if err != nil {
		log.Printf("config %s: %v (using defaults)", path, err)
		return c
	}
	defer f.Close()
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
		case "PLACE", "PLACE2":
			c.Places[idx(k)].Name = v
		case "LAT", "LAT2":
			c.Places[idx(k)].Lat = num
		case "LON", "LON2":
			c.Places[idx(k)].Lon = num
		case "TZ1", "TZ2":
			c.Places[idx(k)].TZ = v
		case "INPUT":
			c.Input = v
		case "TOUCH_SWAP":
			c.SwapXY = b
		case "TOUCH_INVX":
			c.InvX = b
		case "TOUCH_INVY":
			c.InvY = b
		case "TOUCH_MAXX":
			c.MaxX = int(num)
		case "TOUCH_MAXY":
			c.MaxY = int(num)
		case "ROTATE":
			c.Rotate = v
		case "REFRESH_MIN":
			c.RefreshMin = int(num)
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
		case "KEY_INPUT":
			c.KeyInput = v
		case "UPDATE_REPO":
			c.UpdateRepo = v
		case "UPDATE_PUBKEY":
			c.UpdatePubKey = v
		case "UPDATE_API":
			c.UpdateAPI = v
		case "UPDATE_HOURS":
			c.UpdateHours = int(num)
		case "WARN_REGION":
			c.WarnRegion = v
		}
	}
	return c
}

func idx(k string) int {
	if strings.HasSuffix(k, "2") {
		return 1
	}
	return 0
}

type App struct {
	cfg              Config
	loc              *time.Location
	cal              Calendar
	wGlance, wDetail *Weather
	wErr             error
	updated          time.Time

	view  string    // "dash" | "month"
	month time.Time // first day of the shown month
	sel   time.Time // selected day in the month view
	page  int       // day panel page
	hits  []hit

	nextWake    time.Time
	warnings    []Warning
	calUpdated  time.Time
	forceCal    bool      // refresh button: fetch calendars too, not just weather
	wantUpdate  bool      // refresh button: also check for a new release right away
	wifiPinned  bool      // switched on by hand: stays up until tapped again
	skipWake    bool      // this tap switched Wi-Fi off; don't let it wake the radio again
	shownOnline bool      // Wi-Fi state in the last drawn frame
	busy        bool      // refresh button pressed, fetch in progress
	weatherAt   time.Time // last successful weather fetch
}

// toggleWifi: the Wi-Fi icon. Off -> on and pinned; on (pinned or auto) -> off, back to auto.
func (a *App) toggleWifi() {
	if a.wifiPinned || online() {
		a.wifiPinned, a.skipWake = false, true
		wifiDown()
	} else {
		a.wifiPinned = true
		reconnectWifi()
	}
}

// onDevice: InkBox's Wi-Fi scripts exist only on the Kobo (desk renders skip the radio dance).
func onDevice() bool {
	_, err := os.Stat("/usr/local/bin/wifi/toggle.sh")
	return err == nil
}

func (a *App) refresh() {
	if onDevice() && !wifiUp(90*time.Second) {
		a.wErr = fmt.Errorf("Wi-Fi bağlanamadı")
		a.updated = time.Now().Round(0) // wall clock only: monotonic time stops while suspended
		return
	}
	g, err1 := fetchWeather(a.cfg.Places[0], 0, 0)
	d, err2 := fetchWeather(a.cfg.Places[1], 8, 2)
	if err1 == nil && err2 == nil {
		a.wGlance, a.wDetail, a.wErr = g, d, nil
		a.weatherAt = time.Now().Round(0)
	} else {
		a.wErr = firstErr(err1, err2)
		log.Printf("weather: %v", a.wErr)
	}
	if ws, err := fetchWarnings(a.cfg.WarnRegion); err == nil {
		a.warnings = ws
	} else {
		log.Printf("warnings: %v", err) // keep the last known list
	}
	// calendars change rarely: every CAL_REFRESH_MIN, or now when the refresh button asked
	if a.forceCal || time.Since(a.calUpdated) >= time.Duration(a.cfg.CalRefreshMin)*time.Minute {
		if err := a.cal.Refresh(); err != nil {
			log.Printf("calendar: %v", err)
		} else {
			a.calUpdated = time.Now().Round(0)
		}
		a.forceCal = false
	}
	a.updated = time.Now().Round(0)
}

// reconnectWifi re-runs InkBox's own Wi-Fi scripts in the background (they refuse
// to run twice, so overlapping calls are harmless).
func reconnectWifi() {
	cmd := exec.Command("sh", "-c", `. /opt/kobodash/kobodash.conf && /usr/local/bin/wifi/toggle.sh on && `+
		`/usr/local/bin/wifi/connect_to_network.sh "$WIFI_SSID" "$WIFI_PSK"`)
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

// wifiUp brings the radio up and waits for an address.
func wifiUp(timeout time.Duration) bool {
	if online() {
		return true
	}
	reconnectWifi()
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(2 * time.Second) {
		if online() {
			return true
		}
	}
	return false
}

// The radio is the biggest battery drain, so it's off unless something needs it.
func wifiDown() {
	exec.Command("/usr/local/bin/wifi/toggle.sh", "off").Run()
}

// sshActive: an established connection on local port 22 (0x0016) — never cut it.
func sshActive() bool {
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

// ensureScaling: InkBox leaves the CPU pinned at 800 MHz ("userspace"). Scale with
// load instead: ondemand if the kernel has it, else conservative (the Mini's
// 2.6.35 kernel only offers conservative/powersave/userspace/performance).
func ensureScaling() {
	avail, err := os.ReadFile(cpufreq + "scaling_available_governors")
	if err != nil {
		return
	}
	want := "conservative"
	if strings.Contains(string(avail), "ondemand") {
		want = "ondemand"
	}
	if cur, _ := os.ReadFile(cpufreq + "scaling_governor"); strings.TrimSpace(string(cur)) != want {
		if err := os.WriteFile(cpufreq+"scaling_governor", []byte(want), 0644); err != nil {
			log.Printf("governor: %v", err)
		}
	}
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

const framePath = "/tmp/kobodash.png"

func (a *App) show() {
	if err := writePNG(framePath, panelFrame(a.render(), a.cfg.Rotate)); err != nil {
		log.Printf("frame: %v", err)
		return
	}
	// -f: full flashing refresh, clears ghosting on every page change
	if out, err := exec.Command(a.cfg.Fbink, "-q", "-f", "-g", "file="+framePath).CombinedOutput(); err != nil {
		log.Printf("fbink: %v %s", err, out)
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

// readTouch turns zForce events into taps (on finger lift) in screen coordinates.
func readTouch(c Config, calib bool, taps chan<- image.Point) {
	f, err := os.Open(c.Input)
	if err != nil {
		log.Printf("touch: %v", err)
		return
	}
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
	view := flag.String("view", "dash", "with -once: dash | month")
	calib := flag.Bool("calib", false, "print raw touch coordinates")
	flag.String("server", "", "ignored; run.sh from the image still passes it")
	stest := flag.Int("suspendtest", 0, "arm the RTC alarm N seconds ahead, suspend, report how long it slept")
	fakeNow := flag.String("now", "", "with -once: pretend it is this local time (2006-01-02T15:04)")
	fakeWarn := flag.Bool("fakewarn", false, "with -once: draw sample SMHI warnings and battery")
	genkey := flag.String("genkey", "", "release tooling: write a new ed25519 private key here, print the public key")
	sign := flag.String("sign", "", "release tooling: sign the file given as argument with this private key")
	showVersion := flag.Bool("version", false, "print version")
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
	a := &App{cfg: cfg, loc: loc, cal: Calendar{Feeds: cfg.Feeds, Holiday: cfg.Holiday}, view: "dash"}

	if *once != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04", *fakeNow, loc); err == nil {
			clock = func() time.Time { return t }
		}
		a.refresh()
		if *fakeWarn {
			a.warnings = []Warning{{"ORANGE", reasonTR("Rain")}, {"YELLOW", reasonTR("Wind")}}
			fakeBattery = "pil %85"
		}
		now := clock().In(loc)
		switch *view {
		case "month":
			a.openDay(midnight(now, loc))
		}
		if err := writePNG(*once, a.render()); err != nil { // landscape, as people see it
			log.Fatal(err)
		}
		return
	}

	log.Printf("kobodash %s starting", version)
	exec.Command("killall", "-9", "inkbox-splash").Run()                     // InkBox's boot splash keeps drawing otherwise
	os.WriteFile("/sys/class/leds/pmic_ledsg/brightness", []byte("0"), 0644) // ipd used to switch the LED off
	taps := make(chan image.Point, 8)
	keys := make(chan struct{}, 4)
	go readTouch(cfg, *calib, taps)
	go readPowerKey(cfg.KeyInput, keys)

	ensureScaling()
	a.refresh()
	a.show()
	tick := time.NewTicker(10 * time.Second)
	lastTouch, lastTry, resumed := time.Now(), time.Now(), time.Time{}
	var lastUpdateCheck time.Time
	maybeUpdate := func() { // only right after a refresh, while Wi-Fi is up
		if time.Now().Round(0).Sub(lastUpdateCheck) < time.Duration(cfg.UpdateHours)*time.Hour {
			return
		}
		lastUpdateCheck = time.Now().Round(0)
		if a.checkUpdate() {
			os.Exit(0) // run.sh starts the new binary
		}
	}
	maybeUpdate()
	wifiIdle := time.Duration(cfg.WifiIdleMin) * time.Minute
	refreshEvery := time.Duration(cfg.RefreshMin) * time.Minute
	awakeIdle := time.Duration(cfg.AwakeSec) * time.Second
	sleepNow := false
	for {
		select {
		case <-keys:
			if time.Since(resumed) > 3*time.Second { // the press that woke us is delivered after resume; ignore it
				sleepNow = true
			}
		case p := <-taps:
			lastTouch = time.Now()
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
			if a.wantUpdate { // refresh button: don't wait for the 6-hour update check
				a.wantUpdate, lastUpdateCheck = false, time.Time{}
				maybeUpdate()
			}
		case <-tick.C:
			if a.view != "dash" && time.Since(lastTouch) > time.Duration(cfg.IdleBackMin)*time.Minute {
				a.view = "dash" // nobody is looking: go back to the dashboard
				a.show()
			}
			due := time.Since(a.updated) >= time.Duration(cfg.RefreshMin)*time.Minute
			retry := a.wErr != nil && time.Since(lastTry) >= time.Minute
			if due || retry {
				lastTry = time.Now()
				a.refresh()
				if a.view == "dash" {
					a.show()
				}
			}
			ensureScaling()
			if !cfg.WifiAlways && !a.wifiPinned && online() && time.Since(lastTouch) > wifiIdle && !sshActive() {
				wifiDown()
			}
			if online() != a.shownOnline {
				a.show() // keep the Wi-Fi icon honest
			}
		}

		// Power model: asleep unless someone is using it. Never while ssh'd in or Wi-Fi is pinned.
		if cfg.Sleep && (sleepNow || time.Since(lastTouch) > awakeIdle) && !a.wifiPinned && !sshActive() {
			sleepNow = false
			woke := a.sleepUntil(a.updated.Add(refreshEvery))
			resumed = time.Now()
			if woke == "timer" {
				lastTry = time.Now()
				a.refresh()
				maybeUpdate()
				a.show() // lastTouch stays old, so it goes straight back to sleep
			} else {
				lastTouch = time.Now() // power button (or a failed sleep): stay up for a while
				a.show()
				if time.Since(a.updated) > 15*time.Minute {
					a.refresh()
					a.show()
				}
			}
		}
	}
}

// sleepUntil suspends until due (RTC alarm) or the power button. Returns "timer",
// "button", or "error" when it could not sleep safely.
func (a *App) sleepUntil(due time.Time) string {
	a.view, a.nextWake = "dash", due
	wifiDown()
	a.show() // this frame stays on the panel while asleep
	due = due.Round(0)
	d := due.Sub(time.Now().Round(0))
	if d < 30*time.Second {
		d = 30 * time.Second
	}
	if _, err := setWakeAlarm(d); err != nil {
		log.Printf("alarm: %v", err) // never suspend without a way back
		return "error"
	}
	log.Printf("sleep: alarm in %v (next update %s)", d.Round(time.Second), due.In(a.loc).Format("15:04:05"))
	start := time.Now().Round(0) // wall clock; monotonic time stops in suspend
	if err := suspend(); err != nil {
		log.Printf("suspend: %v", err)
		return "error"
	}
	woke := "button"
	if time.Now().Round(0).After(due.Add(-45 * time.Second)) {
		woke = "timer"
	}
	log.Printf("woke: %s after %v", woke, time.Now().Round(0).Sub(start).Round(time.Second))
	return woke
}

// readPowerKey reports power-button presses (the Mini's only key, on mxckpd).
func readPowerKey(dev string, keys chan<- struct{}) {
	f, err := os.Open(dev)
	if err != nil {
		log.Printf("power key: %v", err)
		return
	}
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

func runCmd(name string, args ...string) error {
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %v %s", name, err, out)
	}
	return nil
}
