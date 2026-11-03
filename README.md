# kobo-mini-n705-dashboard

A fridge-door e-ink dashboard for the **Kobo Mini (model N705)**: weather, official weather warnings and your calendars on the 800×600 screen, hung in landscape, that sleeps between updates.

It runs as a single Go binary on the Alpine Linux base that [InkBox / Quill OS](https://github.com/Quill-OS/quill) 2.0 ships for this device. InkBox's own GUI is switched off. The Kobo fetches everything itself, no server or browser needed.

![the calendar screen](docs/img/dashboard.png)

*(Screenshots use the demo calendar in `docs/demo.ics` and sample warnings. The UI language is Turkish.)*

## What it shows

One screen, calendar first:

- **The month** on the left, Monday first: bars under a date mark your own events, red days (public holidays and Sundays, as on Swedish calendars) are shaded, today is inverted. Tap a day to select it; arrows change the month, "Bugün" goes back to today.
- **The list** on the right: the selected day and every event after it, one line each (`Cmt 7  14:00  Bahar Ece Basketbol`), paged. Sources are your private ICS links plus Swedish public holidays.
- **The weather now** under the month: the home city with a large temperature and four short lines beside it: place, feels-like with the day's min/max (tomorrow's after 17:00, marked `yarın`), wind, and the next sunset or sunrise (`☀↓16:28`). Under the list, a thin strip with the next four hours. Both come from the [SMHI](https://opendata.smhi.se) point forecast (no API key; SMHI only covers the Nordics).
- **Official SMHI warnings** in the top bar, right of the date and ISO week, on one line: `Turuncu: şiddetli yağmur · Sarı: kuvvetli rüzgar`, shortened to `Turuncu: yağmur · Sarı: rüzgar` (and the month name to `Kas`) when space runs out.
- **Where it is**, unless the config names a place: the city is looked up from the public IP once a day ([geojs.io](https://www.geojs.io) and [ipwho.is](https://ipwho.is), no key). The two have to agree within 50 km, and a move further than that is only believed when a second lookup an hour later says the same, because IP databases put some ISPs in Stockholm one minute and Göteborg the next. The county found this way also picks the SMHI warnings, unless `WARN_REGION` is set.
- **One passage a day** along the bottom, on a single line with its author below: prose from public-domain novels and essays, and verse from public-domain poets, in Turkish (`kobo/quotes.tsv`).
- A top bar with the next scheduled update and battery level, Wi-Fi state (tap to pin it on) and a refresh button that also checks for a new release.

### Why it looks like this

Type sizes come from viewing distance, not taste. At 200 ppi one pixel is 0.127 mm and Go's capitals are 0.72 of the font size. ISO 9241-303 asks for at least 16 arcminutes of character height, so numbers read from 1 m need about 52 px (the temperature uses 96). Everything else is meant for reading up close at 24 px, and status text at 18. The layout follows what the best e-ink dashboards share (TRMNL, Petroff's Kindle weather display, MagInkCal): one dominant number, bands separated by rules, a capped list with "+N more", pure black on white. Go Medium/Bold beat Atkinson Hyperlegible Next and Fira Sans here because they have the largest capitals per pixel; the original Atkinson Hyperlegible has no Turkish İ.

## Power model

The device is asleep most of the time and the last frame stays on the panel (e-ink needs no power to hold it).

- The RTC alarm wakes it on a fixed clock grid every `REFRESH_MIN` (120 by default: 06:00, 08:00 … 22:00; on battery each wake costs a Wi-Fi association). It brings Wi-Fi up, fetches the weather (calendars every `CAL_REFRESH_MIN`, 240 by default), redraws and suspends again. A power-button wake in between refreshes too but doesn't move the grid; a refresh less than 30 minutes before a slot counts as that slot.
- **Quiet hours** (`QUIET`, 23-6 by default; `QUIET=off` disables them): no timed wakes at night. The alarm is set straight to 06:00, which saves a third of the daily Wi-Fi associations.
- The **power button** wakes it for `AWAKE_SEC` (120) of touch use. Pressing it while awake puts it to sleep.
- Touch can't wake the Mini from suspend: only the keypad and USB are wakeup sources.
- While awake the CPU uses the `conservative` governor (InkBox pins it at 800 MHz) and Wi-Fi is off unless needed.
- It doesn't suspend while an ssh session is open (3 hours at most: a connection whose other end vanished can look open for hours) or Wi-Fi is pinned on (30 minutes unused at most).
- A power-button press while it is going to sleep keeps it awake. A struck-through refresh icon means the weather is stale, or the calendars are more than a day old.
- **Wi-Fi:** each wake runs InkBox's own `connect_to_network.sh` and counts the network as up only once the default route exists (udhcpc sets the address a moment earlier, and fetches in between fail with "network is unreachable"). Anything an earlier wake left behind (a connect still running, the driver still loaded, a stale pid file) is cleared first. `wifi: up in …` in the log shows the time per wake, and `wifi.log` on the card has every line of the scripts stamped with seconds since the connect started. (A faster hand-rolled `wpa_supplicant` path was tried in v0.7.8–v0.7.18 and removed: once it actually ran, timed wakes stopped getting Wi-Fi.)
- Going to sleep, it draws the frame first and switches Wi-Fi off last, so a step that hangs on the way down leaves it reachable. That frame changes little from wake to wake, so it is drawn without a flash; every 6th one (about once or twice a day) and every page change from a tap flash fully to clear ghosting.
- `SUSPEND_SETTLE_MS` (2000) is the wait between `state-extended` and `mem`, Kobo's and KOReader's value. Less saves awake time, but KOReader notes shorter waits misbehaved, so try it only on a reader you can reach: a failed suspend or an instant wake puts it back to 2 s for the rest of the run.
- **Low-battery alert** (optional): set `TELEGRAM_TOKEN` (a bot from @BotFather) and `TELEGRAM_CHAT` (plus `TELEGRAM_THREAD` for a forum topic) and it sends one Telegram message when the battery drops to `BATT_ALERT` (20%), and one more at half of that. It rearms once the battery is back above `BATT_ALERT`+10.
- **Daily report** (with `TELEGRAM_LOG_THREAD`): after the quiet hours, one line with the last 24 h: timed wakes done/due, wakes without network, failed suspends, watchdog and rescue actions, restarts, median Wi-Fi time and the night's battery drain per hour.
- **Wake log** (optional): `TELEGRAM_LOG_THREAD` sends a silent one-liner to that forum topic on every refresh (version, what woke it, battery, Wi-Fi time). The N705's `mc13892_bat` has no real fuel gauge: `charge_now` and `current_now` are a constant 500000 and `voltage_now` repeats the percentage, so per-wake mAh can't be measured on the device. It goes out alongside the weather fetch, so it doesn't keep the radio up longer.
- After a timed wake it goes straight back to sleep once the frame is drawn, and the weather, warning and calendar fetches run in parallel: both shorten the time the radio is on.
- The update check is a conditional request: when the latest release hasn't changed GitHub answers with an empty 304.

Battery life on the 1000 mAh (3.7 Wh) cell is still being measured: every sleep and wake line in the log ends with the battery level, so the drain per day can be read off `kobodash.log` (the night's 22:00–06:00 sleep gives the suspend drain). Measurements will be added here once there are numbers.

## Before you start

This rewrites the Kobo's internal microSD card. You keep a full backup and can always go back, but you will open the device.

You need:

- A Kobo Mini N705 and a way to open it ([iFixit teardown](https://www.ifixit.com/Guide/Kobo+Mini+Disassembly/110467)). The card sits inside under the back cover.
- A microSD card reader.
- macOS or Linux with Docker, Go (the build pins Go 1.23 through `GOTOOLCHAIN`), `xz` and an ssh key.
- The **InkBox 2.0 image for n705** from [SourceForge](https://sourceforge.net/projects/quill-os/files/n705/): `inkbox-2.0-n705.xz`, sha256 `2e103043cafe391b75d37907c25f5a5d1437f1e630008c612c882d19ff837446`.
- Your Wi-Fi name and password (2.4 GHz only; the Mini has no 5 GHz radio) and a private ICS link for your calendar.

## Step by step

### 1. Take the card out and back it up

Remove the back cover and the inner cover screws, pull the microSD card and put it in the reader. Find it by size, never by guessing the disk number:

```bash
diskutil list external physical      # macOS
lsblk                                # Linux
```

Back it up, so you can always restore the stock firmware:

```bash
sudo dd if=/dev/rdiskN of=kobo-mini-backup.img bs=4m      # macOS (Linux: /dev/sdX, bs=4M)
```

### 2. Build the image

```bash
WIFI_SSID='YourWifi' WIFI_PSK='your-password' \
  image/build.sh ~/Downloads/inkbox-2.0-n705.xz kobodash.img
```

It starts from the stock InkBox image and changes three things. The signed files stay untouched.

1. **Roots the device offline.** It writes `rooted` at sector 79872 and puts the root-compliant kernel (`uImage-root`, taken from the image's own recovery partition) at sector 81920. This is exactly what InkBox's recovery script `restore-root.sh` does. InkBox only accepts both together: one without the other makes it refuse to boot.
2. **Sets boot flags:** `X11_START=false` (no KoBox) and `RW_ROOTFS=true`.
3. **Adds files to the overlay.** The root filesystem is a signed squashfs that is verified on every boot, but `rootfs/write/` on partition 3 is the unsigned read-write branch of the unionfs on top of it. Files placed there replace the ones below. The build adds:
   - stub OpenRC scripts for `inkbox_gui`, `update_inkbox` and `ipd`, so the InkBox GUI and its power daemon stay off;
   - a `kobodash` service and a symlink that enables `sshd`;
   - your ssh public key (`PUBKEY`, default `~/.ssh/id_ed25519.pub`);
   - your Wi-Fi settings.

The script needs Docker for `e2fsprogs` and `squashfs-tools`; it builds the helper image from `image/Dockerfile` the first time. Set `SERVER=http://host:port` if you want the Kobo to report its IP to `web/server.py` on boot.

### 3. Write it to the card

macOS:

```bash
diskutil unmountDisk /dev/diskN
sudo dd if=kobodash.img of=/dev/rdiskN bs=4m && sync
sudo dd if=/dev/rdiskN bs=4m count=922 2>/dev/null | head -c "$(stat -f %z kobodash.img)" | md5     # must match: md5 kobodash.img
diskutil eject /dev/diskN
```

Linux:

```bash
sudo dd if=kobodash.img of=/dev/sdX bs=4M conv=fsync status=progress
sudo head -c "$(stat -c %s kobodash.img)" /dev/sdX | md5sum      # must match: md5sum kobodash.img
```

`count=922` is just enough 4 MiB blocks to cover the 3.87 GB image; `head -c` trims the read-back to the exact size. On macOS, run `dd` from your own Terminal. Sandboxed apps can be refused raw disk access even with sudo.

### 4. First boot

Put the card back and power on. After the InkBox boot splash you should see `kobodash`, then `Wi-Fi'ye bağlanıyor…`, then `kobodash hazır — IP: …`. The clock is set over NTP and written to the RTC.

```bash
ssh root@<kobo-ip>
```

The first boot can take a few minutes while sshd generates host keys.

Rooted InkBox ships root's password as `root` and its sshd accepts passwords, so anyone on your Wi-Fi could log in. The `kobodash` service blanks root's password on every boot (`*` in `/etc/shadow`, bind-mounted from `/tmp`), so only your key works.

### 5. Install the app

```bash
mkdir -p ~/.config/kobodash
cp kobo/dash.conf.example ~/.config/kobodash/dash.conf
chmod 600 ~/.config/kobodash/dash.conf      # it holds your private ICS link
$EDITOR ~/.config/kobodash/dash.conf
kobo/deploy.sh <kobo-ip>
```

`deploy.sh` cross-compiles for ARMv7, copies the binary and config into the overlay (it mounts partition 3 at `/tmp/p3` on the device) and restarts the app. Changes go live immediately and survive reboots; you never need to reflash for app changes. If the device is asleep, press the power button first.

### 6. Check the touch mapping

The included values (`TOUCH_SWAP=1`, `TOUCH_INVX=1`) are right for the Mini's zForce panel. If taps land in the wrong place, stop the app and run `/opt/kobodash/kobodash -calib`, tap the four corners and read the raw coordinates it prints.

The picture is drawn in landscape and turned a quarter turn onto the portrait panel. `ROTATE=cw` (default) suits the reader hung with its power button on the left; if the screen is upside down, set `ROTATE=ccw`. Taps are turned the same way, so calibration does not change.

## Updates

A device running a release build updates itself. After every timed refresh, while Wi-Fi is up, it asks GitHub for the latest release of `UPDATE_REPO` (a bodiless 304 when nothing changed). If the tag is a newer vX.Y.Z than its own, it downloads `kobodash-armv7` and `kobodash-armv7.sig` and checks the ed25519 signature against the public key compiled into it (`kobo/update.go`, or `UPDATE_PUBKEY`). Only a binary that verifies gets installed. The private key lives on the maintainer's machine and never in the repo, so a compromised GitHub account can't push code to devices.

`supervise.sh` runs the app. The app exits after installing an update and the supervisor starts the new binary. If that binary exits with an error within a minute, the supervisor puts the previous one (`kobodash.prev`) back. A new release is also **on trial** until it has slept, woken and reached the network once: started five times without that, it puts `kobodash.prev` back itself. A release that failed is remembered (`rejected`) and not downloaded again.

To put a device on the release track, deploy a release build once: `VERSION=v0.1.0 kobo/deploy.sh <ip>`. Builds deployed without `VERSION` report `dev` and never self-update.

To publish a release: `tools/release.sh v0.2.0` builds, signs, tags and uploads it. For your own fork, create a key with `go run ./kobo -genkey ~/.config/kobodash/release.key`, put the printed public key in `kobo/update.go` (or `UPDATE_PUBKEY`), and set `UPDATE_REPO`.

## Staying reachable

The reader hangs where nobody may press its button for days, so a fault must never leave it awake with the radio off. Three layers, each one working without the one above it:

1. **In kobodash.** Helpers that can hang in the kernel (FBInk, the Wi-Fi scripts) run in their own process group and are killed after 30 s instead of blocking the main loop. A failed suspend is tried again 20 s later, and Wi-Fi comes back from the third failure in a row. Writes to the card log never block (a stalled card loses lines, not the device). A watchdog goroutine climbs a ladder kept on the card when the main loop hasn't moved for 10 minutes awake, after 6 failed suspends in a row, or when 3 helpers got stuck: 1 kill the stuck helpers and turn Wi-Fi on, 2 restart kobodash, 3 roll a release on trial back and reboot. A separate network ladder starts after 3 refreshes in a row without a route: Wi-Fi reset, restart, reboot. Reboots are limited to 3 a day (`watchdog-reboots` on the card); past that it stays awake with Wi-Fi on for 6 hours so someone can look (a router that is merely down never keeps it awake). A good sleep, or an hour without trouble, starts over.
2. **In supervise.sh, without kobodash.** A busybox loop watches kobodash's heartbeat (`/tmp/kobodash.beat`) and the route. Awake for 5 minutes without a route (not counting a radio switched off on purpose, or someone touching the screen), or 12 minutes with the heartbeat frozen, means **rescue mode**: kill kobodash, bring Wi-Fi and sshd up, send a Telegram message with the IP, stay reachable an hour, then reboot within the same daily budget. Time asleep never counts: a wall-clock jump between two checks means the device was suspended. Crash loops back off (3, 10, 30, 60 s), and a missing binary never ends the loop. `image/test-supervise.sh` runs it against fakes.
3. **At boot (run.sh).** Every step is bounded the same way, so nothing can keep supervise.sh from starting.

`supervise.sh` and `run.sh` are part of the image; a device updated over the air keeps the copies it was built with. To give an older device the new ones, see `docs/RUNBOOK.md`.

What the card keeps (`/tmp/p3/rootfs/write/opt/kobodash/`): `kobodash.log` (the log, kept across reboots), `wifi.log` (the Wi-Fi scripts, time-stamped), `suspend-fail.log` (kernel log and stuck tasks after a failed suspend), `trying`/`starts`/`rejected` (update trial), `heal`/`heal-net` (ladder steps), `watchdog-reboots` (reboot budget).

## Daily passages

`kobo/quotes.tsv` holds 220 passages: 144 sentences from novels, 38 from essays and philosophy (Stevenson, Chesterton, Emerson, Marcus Aurelius and others), 34 extracts from English poems and 4 from Yunus Emre. Religious lines, including mystic verse about the love of God, are left out on purpose. Each row has the Turkish text, author, work, the original and its source (a Project Gutenberg book number, or `ws:` for a Turkish Wikisource page). Every passage is one line on screen: long ones were cut by hand to a self-contained excerpt (the `en` column holds the matching piece of the original), and verse lines are joined with ` / `. A novel gives at most one passage and a poetry collection at most three. Every author or translator died before 1955, so the originals are public domain in the US, the EU and Turkey. The English translations into Turkish are new; Yunus Emre is shown as written.

The display shows one passage per day, the same one all day. Each year gets its own shuffle, so the order changes every year and nothing repeats until the pool runs out.

How they were chosen: `tools/quote_candidates.py` and `tools/poem_candidates.py` pulled about 11,000 likely lines out of the 6,000 most downloaded books on Project Gutenberg (novels, essays, poetry). An LLM pass kept the joyful, self-contained ones and translated them; a stricter editing pass kept only optimistic lines with real meaning, dropped anything whose source is about grief, and rewrote weak translations. `tools/verify_quotes.py` checks that every original appears word for word in its source, and `go test ./kobo` checks that each passage fits the single line the dashboard gives it.

## Development

```bash
cd kobo
GOTOOLCHAIN=go1.23.12 go build -o /tmp/kd .
/tmp/kd -conf ~/.config/kobodash/dash.conf -once out.png                      # render one frame on your desk
/tmp/kd -conf ... -once out.png -sel 2026-11-20 -now 2026-11-02T08:00 -fakewarn  # other day / time / sample warnings
```

On the device:

| Flag | What it does |
|---|---|
| `-calib` | prints raw touch coordinates |
| `-suspendtest 60` | arms the RTC alarm, suspends, and reports how long it slept |
| `-version` | prints the build version |
| `-notify "text"` | sends one Telegram message to the alert topic (supervise.sh's rescue mode uses it) |
| `-watchdogtest` | hangs the main loop on purpose; the watchdog must act within ~3 minutes |

`go test ./kobo` runs the unit tests: self-update against a fake GitHub (signature, trial, rollback), the wake grid across DST, ICS recurrences, the watchdog's ladder and reboot budget. `image/test-supervise.sh` runs supervise.sh against a fake device (no network, frozen heartbeat, crash loop, stuck card and more).

`web/server.py` is the earlier browser version (stdlib Python, server-rendered HTML). It also serves `/kobo/probe`, a read-only device survey script, and accepts `/kobo/report`. It's a dev tool with no authentication: run it on a trusted LAN only.

## Things we learned the hard way

- **Go version:** the Mini runs kernel 2.6.35. Go 1.24+ needs Linux 3.2, so build with Go 1.23.
- **RTC wake:** there is no `/sys/class/rtc/rtc0/wakealarm`, so `rtcwake` refuses. Setting the alarm with the `RTC_WKALM_SET` ioctl on `/dev/rtc0` works fine.
- **Suspend:** use the Kobo sequence, `echo 1 > /sys/power/state-extended`, a short wait, then `echo mem > /sys/power/state`.
- **Timekeeping:** Go's monotonic clock stops during suspend. Keep wall-clock times (`t.Round(0)`) for anything compared across a sleep, or timer wakes look like button presses.
- **Overlay path:** the overlay's write branch is not reachable at `/rootfs-part` after `switch_root`. Mount `/dev/mmcblk0p3` yourself.
- **Touch:** zForce reports in 800×600 landscape. Screen x = 600 − raw y, screen y = raw x.
- **CPU governor:** there is no `ondemand`; the choices are `conservative`, `powersave`, `userspace` and `performance`.
- **InkBox's power daemon:** if you keep `ipd`, set `/data/config/20-sleep_daemon/7-idleSleep` to `0` to stop its auto-sleep. kobodash grabs the touch device, so ipd would think the device is idle.
- **Wi-Fi:** the interface is `eth0`. `toggle.sh off` unloads the driver and cuts its power; `connect_to_network.sh SSID PSK` connects.
- **Address is not network:** udhcpc sets the address before the default route; wait for the route, or every fetch in that gap fails with "network is unreachable".
- **`toggle.sh on` exit status** is `wlarm_le up`'s and can be 1 with a working radio: don't chain the connect behind it with `&&`.
- **RTC alarm read-back:** the MSP430 driver takes the alarm as seconds from now and reads it back as 1970-01-01 00:00:01, so only the enabled flag can be checked.
- **Root is FUSE** (unionfs-fuse over squashfuse). A task waiting on FUSE while its daemon is frozen can make suspend fail with "device or resource busy" after the kernel's 20 s freezer timeout.
- **busybox `timeout`** only signals its direct child, and a task stuck in the kernel ignores signals while the shell keeps waiting: run such steps in the background in their own session and stop waiting instead.
- **dhd's "dongle image file download failed"** at module load is harmless: the firmware loads when the interface comes up.
- **debugfs:** `-f cmdfile` must come before the image path. Otherwise it reads commands from stdin, which silently ate the rest of a heredoc script.

## Going back

Write `kobo-mini-backup.img` back to restore the stock Kobo firmware, or the plain InkBox image for stock InkBox.

## Credits

- [InkBox / Quill OS](https://github.com/Quill-OS/quill), whose recovery scripts, init and overlay design made this possible.
- [FBInk](https://github.com/NiLuJe/FBInk) draws to the panel.
- Forecast and warnings from [SMHI open data](https://opendata.smhi.se); location from [geojs.io](https://www.geojs.io) and [ipwho.is](https://ipwho.is).
- [Go fonts](https://go.dev/blog/go-fonts) and [oksvg](https://github.com/srwiley/oksvg).

## License

MIT. Use at your own risk: this modifies your device's firmware.
