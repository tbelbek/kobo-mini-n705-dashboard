# Runbook

What to do when the dashboard misbehaves, and how to work on a device you can't reach by hand. `DEV` below is the reader's address (`root@<ip>`); the card's write layer is `W=/tmp/p3/rootfs/write/opt/kobodash`.

## Rules for a remote device

1. **Never stop supervise.sh to test.** A kobodash you started by hand has nothing to restart it, and no rescue mode around it. Test under supervise, with the test settings in `dash.conf`, and remove them afterwards.
2. **Every test ends by itself.** Before you start, know how the device gets back to normal without you: a timer, the watchdog, a reboot. If the only way back is a button press, don't run it remotely.
3. **Boot scripts last.** `run.sh` and `supervise.sh` are the only things that can make the device unrecoverable. Change them only with `image/test-supervise.sh` passing and `sh -n` checked on the device (see below), never as part of another change.
4. **Ship small.** One change per release, `go test ./kobo` green, then watch one timed wake in the log before the next.

## Daily report

With `TELEGRAM_LOG_THREAD` set, the first refresh with network after the quiet hours (06:00) sends one line counting the last 24 h of the card log:

```
✓ Kobo 24 sa · zamanlı uyanış 9/9 · tuş 1 · ağsız 0 · uyku hatası 0 · bekçi 0 · kurtarma 0 · yeniden başlama 0 · Wi-Fi ort. 8.3 sn · gece 8h uykuda %4 (0.50 %/sa)
```

`⚠` instead of `✓` means a timed wake was missed or something had to step in (no network, failed suspend, watchdog, rescue). Seven `✓` days in a row with the night drain recorded is the bar for calling a release stable. A missing report is itself a finding: the 06:00 and 08:00 wakes both got nowhere.

## Symptoms

| You see | Likely | Look at |
|---|---|---|
| No Telegram wake line for > 2 h (outside 23–06) | asleep with no alarm, hung, or out of battery | find it on the LAN; if it's gone, a button press |
| Telegram "kurtarma modu" | supervise.sh rescued it: kobodash hung or lost the network | ssh in within the hour; `$W/kobodash.log` |
| Lines but `Wi-Fi bağlanamadı` / no weather | network ladder working on it | `wifi.log`, `heal-net` |
| Screen shows a struck-through refresh icon | last refresh failed, retrying every minute while awake | `kobodash.log` |
| Version doesn't change after a release | update check failing, or the tag is `rejected` | `grep update: $W/kobodash.log`, `$W/rejected` |

## Reach it

The reader is only on the network while awake: a timed wake (~25 s), a power-button wake (2 min), or rescue mode (1 h).

```bash
# is it on the LAN at all? (its address may change)
for i in $(seq 1 254); do (ping -c1 -W 300 192.168.1.$i >/dev/null && echo .$i) & done; wait
# hammer ssh until it answers, then hold it awake (it never suspends while ssh is open)
until ssh -o ConnectTimeout=2 -o BatchMode=yes $DEV true 2>/dev/null; do sleep 1; done
ssh $DEV 'sleep 3600' &   # keep this running while you work; kill it when done
```

## Collect evidence first

Before restarting anything (a restart loses what was in RAM):

```bash
ssh $DEV 'W=/tmp/p3/rootfs/write/opt/kobodash
  date -u; cat /proc/uptime; $W/kobodash -version
  for p in /proc/[0-9]*; do printf "%s %s %s\n" ${p#/proc/} "$(cat $p/comm)" "$(cut -d" " -f3 $p/stat)"; done
  tail -n 150 $W/kobodash.log; tail -n 80 $W/wifi.log; cat $W/suspend-fail.log
  cat $W/heal $W/heal-net $W/watchdog-reboots $W/trying $W/rejected 2>/dev/null
  dmesg | tail -n 200'
```

Never print `/proc/*/cmdline`: `connect_to_network.sh` carries the Wi-Fi password in its arguments.

### Reading the log

| Line | Means |
|---|---|
| `woke: timer after …` / `woke: button …` | which wake it was |
| `wifi: up in 8.4s` | Wi-Fi including the default route |
| `wifi: no address after 90s` | that wake got no network |
| `wifi: clean start (…)` | leftovers from an earlier wake were cleared |
| `sleep: drawing took …`, `sleep: wifi off took …`, `sleep: alarm in …` | the way down, step by step |
| `suspend: … busy after 22s` | the kernel couldn't freeze a task (20 s freezer timeout): `suspend-fail.log` names it |
| `… no exit after 30s, killed` | a helper hung in the kernel and was left behind |
| `watchdog: … step N` | the in-process ladder acted |
| `supervise: RESCUE: …` | the shell guard took over |
| `update: … passed its first sleep cycle` | a new release is accepted |
| `update: … rolling back` | a new release failed its trial |
| `sleep: power button pressed on the way down, staying awake` | a press during the ~5 s it takes to go to sleep keeps it up |
| `ssh: open for 3h0m0s, no longer keeping it awake` | a session (often a dead one) held it up 3 h: it sleeps anyway now |
| `clock at 1970-…: syncing` | the RTC lost the time; ntpd fixes it, HTTPS fails until then |
| `calendar: … not a calendar` | a feed answered with something else (login page, empty): the last good copy stays |
| `show: panic: …` / `fetch: panic: …` | odd data tripped the code; the old frame stays and it carries on. Report it |

In `wifi.log`, each line is stamped `+ 2.31s` from the start of the connect: the gaps show where the time goes (driver load, scan, DHCP).

## Back to normal

Look for the process, not the pid file (by its command line: its `comm` is just `sh`; print pids only, never a command line, the connect script's carries the Wi-Fi password): right after boot (and the card mounts a minute or so in) the file isn't there yet while supervise already runs, and a second supervise means a second kobodash. On 3 Nov exactly that had two of them fighting over Wi-Fi and the update. kobodash now waits while an older healthy one runs, but don't start a second supervise.

```bash
ssh $DEV 'up=$(cut -d. -f1 /proc/uptime); n=0
  for p in /proc/[0-9]*; do grep -q "supervise\.sh" $p/cmdline 2>/dev/null && n=$((n+1)) && echo "supervise ${p#/proc/}"; done
  if [ $n -eq 0 ] && [ $up -gt 180 ]; then
    killall kobodash; sleep 2
    nohup /bin/sh /opt/kobodash/supervise.sh >/dev/null 2>&1 </dev/null &
    sleep 3; echo restarted
  fi'
```

Two `supervise` lines: kill the newer one (higher pid), then the kobodash whose parent it was (it is left behind, adopted by init).

On the device itself: a short power-button press wakes it; if it doesn't react within a minute, hold the button to reboot. Both bring it up under supervise.

## Test settings

**Sleep test that ends by itself** (no config edits, no restart, runs under supervise):

```bash
ssh $DEV 'echo 10 > /tmp/p3/rootfs/write/opt/kobodash/test-cycles'
```

The next 10 sleeps last 3 minutes each, on the charger too, and each wake does the full Wi-Fi and refresh round. The log shows `test: cycle done, N left` per wake. The file goes after the last cycle, or after 30 minutes without a timed wake. Keep ssh closed meanwhile: an open session keeps it awake.

For longer experiments, append to `/opt/kobodash/dash.conf` (later keys win), restart kobodash (not supervise: `killall kobodash`), and delete the lines when done:

```
REFRESH_MIN=3
AWAKE_SEC=15
WIFI_IDLE_MIN=1
QUIET=off
```

The suspend settle is 500 ms; `suspend: back to a 2 s settle` in the log means the short wait failed on this reader (then set `SUSPEND_SETTLE_MS=2000`). `fast: …` lines in `wifi.log` show the fast connect; `fast connect failed, InkBox's script` means it fell back (`WIFI_CONNECT=inkbox` skips it).

A 3-minute loop shows a dozen wakes in under an hour. Count them:

```bash
ssh $DEV 'L=/tmp/p3/rootfs/write/opt/kobodash/kobodash.log
  for k in "woke: timer" "wifi: up" "no address" "telegram: sent" "unreachable" "suspend:" "watchdog:" "RESCUE"; do
    printf "%-16s %s\n" "$k" "$(grep -c "$k" $L)"; done'
```

`kobodash -suspendtest 60` checks suspend and the RTC alarm alone (run it under supervise's nose only if you can reach the device afterwards: it brings Wi-Fi back itself).

## Installing new boot scripts on a device

The binary updates itself; `run.sh` and `supervise.sh` don't. To move a device to the ones in `image/overlay/opt/kobodash/`:

```bash
image/test-supervise.sh                                   # must say 0 failed
scp image/overlay/opt/kobodash/supervise.sh $DEV:/tmp/supervise.sh.new
scp image/overlay/opt/kobodash/run.sh $DEV:/tmp/run.sh.new
ssh $DEV 'set -e; W=/tmp/p3/rootfs/write/opt/kobodash
  sh -n /tmp/supervise.sh.new; sh -n /tmp/run.sh.new
  cp -p $W/supervise.sh $W/supervise.sh.bak; cp -p $W/run.sh $W/run.sh.bak
  chmod 755 /tmp/*.new
  cp /tmp/supervise.sh.new $W/supervise.sh.tmp && mv $W/supervise.sh.tmp $W/supervise.sh
  cp /tmp/run.sh.new $W/run.sh.tmp && mv $W/run.sh.tmp $W/run.sh
  echo installed'
```

`set -e` stops at the first failure, before anything is replaced. `mv` replaces the file whole, so a supervise.sh that is running keeps reading its old copy. The new one takes over when supervise restarts (it writes OpenRC's pid file too, so `rc-service kobodash stop` still finds it):

```bash
ssh $DEV 'kill $(cat /run/kobodash-supervise.pid); killall kobodash; sleep 2
  nohup /bin/sh /opt/kobodash/supervise.sh >/dev/null 2>&1 </dev/null &
  sleep 5; kill -0 $(cat /run/kobodash-supervise.pid) && echo "new supervise running"'
```

Watch one timed wake, then one reboot (`reboot`; it comes back through run.sh) before leaving it. To undo: copy the `.bak` files back the same way.

## Trying the safety nets

Only with the device reachable and supervise running:

- **Rescue mode:** `ssh $DEV 'kill -STOP $(pidof kobodash)'` freezes kobodash; within ~12 minutes awake the guard kills it, brings Wi-Fi up and sends the Telegram message. Keep an ssh session open (it holds the device awake).
- **In-process watchdog:** `-watchdogtest` hangs the main loop and must be started by hand, outside supervise (stop supervise first, or two copies run). Its last step is a reboot, so only try it with someone at the device.
- **Trial rollback:** write the running tag to `$W/trying`, `4` to `$W/starts`, and `killall -9 kobodash`: the next start puts `kobodash.prev` back and writes `rejected`. Restore `.prev` and delete `rejected` afterwards.

## Releasing

```bash
GOTOOLCHAIN=go1.23.12 go -C kobo test -race ./... && image/test-supervise.sh
tools/release.sh vX.Y.Z
```

Devices pick it up on their next timed wake. It stays on trial until it has slept and reached the network once.
