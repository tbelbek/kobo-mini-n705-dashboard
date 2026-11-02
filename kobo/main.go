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
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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
	UpdateHours   int
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
		UpdateHours:   0, // 0 = every timed refresh: one GitHub API call while Wi-Fi is already up
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
		case "UPDATE_HOURS":
			c.UpdateHours = int(num)
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
	for _, p := range []struct {
		p   Place
		set bool
	}{{p2, set2}, {p1, set1}} {
		if p.set {
			if p.p.TZ == "" {
				p.p.TZ = c.TZ
			}
			c.Place, c.PlaceSet = p.p, true
			break
		}
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
	forceCal    bool      // refresh button: fetch calendars too, not just weather
	wantUpdate  bool      // refresh button: also check for a new release right away
	wifiPinned  bool      // switched on by hand: stays up until tapped again
	skipWake    bool      // this tap switched Wi-Fi off; don't let it wake the radio again
	shownOnline bool      // Wi-Fi state in the last drawn frame
	busy        bool      // refresh button pressed, fetch in progress
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
	t0 := time.Now()
	if onDevice() && !wifiUp(90*time.Second) {
		a.wErr = fmt.Errorf("Wi-Fi bağlanamadı")
		a.updated = time.Now().Round(0) // wall clock only: monotonic time stops while suspended
		return
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
	run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	run(a.batteryAlert)
	run(func() { a.wakeReport(wifi) })
	run(func() { w, wErr = fetchSMHI(a.place, 8) })
	run(func() { ws, warnErr = fetchWarnings(a.region) })
	if cal { // calendars change rarely: every CAL_REFRESH_MIN, or now when the refresh button asked
		run(func() { calErr = a.cal.Refresh() })
	}
	wg.Wait()
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
		log.Printf("warnings: %v", warnErr) // keep the last known list
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

// reconnectWifi re-runs InkBox's own Wi-Fi scripts in the background (they refuse
// to run twice, so overlapping calls are harmless).
func reconnectWifi() {
	cmd := exec.Command("sh", "-c", `. /opt/kobodash/kobodash.conf && /usr/local/bin/wifi/toggle.sh on && `+fastConnect())
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

const slowConnect = `/usr/local/bin/wifi/connect_to_network.sh "$WIFI_SSID" "$WIFI_PSK"`

// fastConnect skips what InkBox's connect script spends its time on: the PSK is hashed
// once per boot instead of on every association, and DHCP retries every second instead
// of every three. Anything that goes wrong falls back to the InkBox script.
func fastConnect() string {
	if wpaConf() != nil {
		return slowConnect
	}
	return `killall wpa_supplicant udhcpc 2>/dev/null; ` +
		`wpa_supplicant -D wext -i eth0 -c /run/kd-wpa.conf -O /run/wpa_supplicant -B && ` +
		`udhcpc -i eth0 -q -n -t 12 -T 1 -A 1 && ip -4 addr show eth0 | grep -q inet || ` +
		`{ killall wpa_supplicant udhcpc 2>/dev/null; ` + slowConnect + `; }`
}

var wpaOnce struct {
	sync.Once
	err error
}

// wpaConf writes the supplicant config with a precomputed PSK (4096 SHA-1 rounds on
// this CPU are worth skipping on every wake).
func wpaConf() error {
	wpaOnce.Do(func() {
		b, err := os.ReadFile("/opt/kobodash/kobodash.conf")
		if err != nil {
			wpaOnce.err = err
			return
		}
		var ssid, psk string
		for _, l := range strings.Split(string(b), "\n") {
			k, v, _ := strings.Cut(strings.TrimSpace(l), "=")
			v = strings.Trim(v, `"'`)
			switch k {
			case "WIFI_SSID":
				ssid = v
			case "WIFI_PSK":
				psk = v
			}
		}
		if ssid == "" || len(psk) < 8 || strings.ContainsAny(ssid, "\"\n") {
			wpaOnce.err = fmt.Errorf("wifi: no usable SSID/PSK")
			return
		}
		key := fmt.Sprintf("%x", pbkdf2SHA1([]byte(psk), []byte(ssid), 4096, 32))
		if _, err := hex.DecodeString(psk); err == nil && len(psk) == 64 {
			key = psk // already the raw 256-bit key
		}
		conf := fmt.Sprintf("ctrl_interface=/run/wpa_supplicant\nnetwork={\n\tssid=\"%s\"\n\tpsk=%s\n}\n", ssid, key)
		wpaOnce.err = os.WriteFile("/run/kd-wpa.conf", []byte(conf), 0o600)
	})
	return wpaOnce.err
}

// pbkdf2SHA1 is RFC 8018 PBKDF2 with HMAC-SHA1 (crypto/pbkdf2 arrives in Go 1.24).
func pbkdf2SHA1(pass, salt []byte, iter, n int) []byte {
	var out []byte
	for blk := uint32(1); len(out) < n; blk++ {
		h := hmac.New(sha1.New, pass)
		h.Write(salt)
		h.Write([]byte{byte(blk >> 24), byte(blk >> 16), byte(blk >> 8), byte(blk)})
		u := h.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iter; i++ {
			h = hmac.New(sha1.New, pass)
			h.Write(u)
			u = h.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:n]
}

// wifiUp brings the radio up and waits for an address.
func wifiUp(timeout time.Duration) bool {
	if online() {
		return true
	}
	reconnectWifi()
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(250 * time.Millisecond) {
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
	frame := panelFrame(a.render(), a.cfg.Rotate)
	if bytes.Equal(frame.Pix, a.shown) {
		return // a full e-ink flash costs about a second of panel power for nothing
	}
	if err := writePNG(framePath, frame); err != nil {
		log.Printf("frame: %v", err)
		return
	}
	// -f: full flashing refresh, clears ghosting on every page change
	if out, err := exec.Command(a.cfg.Fbink, "-q", "-f", "-g", "file="+framePath).CombinedOutput(); err != nil {
		log.Printf("fbink: %v %s", err, out)
		return
	}
	a.shown = frame.Pix
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
	sel := flag.String("sel", "", "with -once: select this day (2006-01-02) instead of today")
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
	a := &App{cfg: cfg, loc: loc, cal: Calendar{Feeds: cfg.Feeds, Holiday: cfg.Holiday}, place: cfg.Place, region: cfg.WarnRegion, wokeBy: "açılış"}
	a.loadLocation()
	a.home()

	if *once != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04", *fakeNow, loc); err == nil {
			clock = func() time.Time { return t }
		}
		a.refresh()
		if *fakeWarn {
			a.warnings = []Warning{{"ORANGE", reasonTR("Rain")}, {"YELLOW", reasonTR("Wind")}}
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
		const keep = "/tmp/p3/rootfs/write/opt/kobodash/kobodash.log"
		if st, err := os.Stat(keep); err == nil && st.Size() > 256<<10 {
			os.Rename(keep, keep+".old")
		}
		if f, err := os.OpenFile(keep, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
		}
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
			if a.wantUpdate { // refresh button: don't wait for the 6-hour update check
				a.wantUpdate, lastUpdateCheck = false, time.Time{}
				maybeUpdate()
			}
		case <-tick.C:
			if !a.atHome() && time.Since(lastTouch) > time.Duration(cfg.IdleBackMin)*time.Minute {
				a.home() // nobody is looking: back to today
				a.show()
			}
			due := time.Since(a.updated) >= time.Duration(cfg.RefreshMin)*time.Minute
			retry := a.wErr != nil && time.Since(lastTry) >= time.Minute
			if due || retry {
				lastTry = time.Now()
				a.refresh()
				a.show()
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
		// A loop, not an if: after a timed wake it goes straight back down instead of
		// idling with Wi-Fi up until the next 10 s tick.
		for cfg.Sleep && (sleepNow || time.Since(lastTouch) > awakeIdle) && !a.wifiPinned && !sshActive() {
			sleepNow = false
			woke := a.sleepUntil(a.wakeAt(a.updated.Add(refreshEvery)))
			a.wokeBy = map[string]string{"timer": "zamanlayıcı", "button": "güç tuşu"}[woke]
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
	log.Printf("sleep: alarm in %v (next update %s) %s", d.Round(time.Second), due.In(a.loc).Format("15:04:05"), battery())
	start := time.Now().Round(0) // wall clock; monotonic time stops in suspend
	if err := suspend(); err != nil {
		log.Printf("suspend: %v", err)
		return "error"
	}
	woke := "button"
	if time.Now().Round(0).After(due.Add(-45 * time.Second)) {
		woke = "timer"
	}
	log.Printf("woke: %s after %v %s", woke, time.Now().Round(0).Sub(start).Round(time.Second), battery())
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
	b, err := os.ReadFile("/sys/class/power_supply/mc13892_bat/capacity")
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
	return true
}

// wakeReport is the silent per-refresh line: enough to see the battery curve and Wi-Fi times.
func (a *App) wakeReport(wifi time.Duration) {
	if a.cfg.TgLogThread == "" {
		return
	}
	a.telegram(a.cfg.TgLogThread, fmt.Sprintf("Kobo pano %s · uyanış: %s · %s · Wi-Fi %v",
		version, a.wokeBy, battery(), wifi.Round(100*time.Millisecond)), true)
}
