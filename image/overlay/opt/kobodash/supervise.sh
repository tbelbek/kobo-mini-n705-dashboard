#!/bin/sh
# Keeps kobodash running from the unionfs write layer on partition 3, and stands guard over
# it without relying on it (busybox sh only, so a broken kobodash can't take the guard down):
#
# - A self-update exits 0 to load the new binary. A binary that crashes (non-zero exit, or
#   a signal other than TERM/INT) within a minute is replaced by the previous one; crashes in a
#   row back off.
# - Rescue mode: awake for RESCUE_NONET seconds in a row without a default route, or with
#   kobodash's heartbeat (/tmp/kobodash.beat) standing still for RESCUE_STALE seconds, means
#   kobodash can't bring the device back: kill it, bring Wi-Fi and sshd up here, send a
#   Telegram message, stay reachable RESCUE_HOLD seconds, then reboot (3 times a day at most;
#   past that it just restarts kobodash). Time asleep doesn't count: a wall-clock jump
#   between two checks means the device was suspended, and the counts start over. Someone
#   using the touch screen holds the no-network count at zero.
#
# Paths and timings come from KD_* variables so the logic can be tried off the device.
. "${KD_KCONF:-/opt/kobodash/kobodash.conf}" # WIFI_SSID, WIFI_PSK, SERVER
W=${KD_W:-/tmp/p3/rootfs/write/opt/kobodash}
BEAT=${KD_BEAT:-/tmp/kobodash.beat}
LOG=${KD_LOG:-/tmp/kobodash.log}
ROUTE=${KD_ROUTE:-/proc/net/route}
IRQ=${KD_IRQ:-/proc/interrupts}
WIFI=${KD_WIFI:-/usr/local/bin/wifi}
CONF=${KD_CONF:-/opt/kobodash/dash.conf}
PIDFILE=${KD_PIDFILE:-/run/kobodash-supervise.pid}
REBOOT=${KD_REBOOT:-reboot}
SSHD=${KD_SSHD:-/usr/sbin/sshd}
TICK=${KD_TICK:-5}
RESCUE_NONET=${KD_NONET:-300}
RESCUE_STALE=${KD_STALE:-720}
RESCUE_HOLD=${KD_HOLD:-3600}
RESCUE_MAX=3 # reboots per 24 h

echo $$ >"$PIDFILE" # deploy.sh and kobodash check this
if [ -z "$KD_W" ]; then
	mkdir -p /tmp/p3
	grep -q " /tmp/p3 " /proc/mounts || mount -t ext4 /dev/mmcblk0p3 /tmp/p3
fi

# say: tmpfs log at once, the card copy in the background (a stuck card must not stall the guard)
say() {
	m="$(date '+%Y/%m/%d %H:%M:%S') supervise: $*"
	echo "$m" >>"$LOG"
	{ echo "$m" >>"$W/kobodash.log"; } 2>/dev/null &
}

# bounded SECS CMD...: run CMD in its own session and kill the whole tree after SECS
# (busybox timeout only signals the direct child; a helper's children would live on)
bounded() {
	s=$1
	shift
	setsid "$@" >>"$LOG" 2>&1 </dev/null &
	bp=$!
	i=0
	while kill -0 $bp 2>/dev/null; do
		if [ $i -ge "$s" ]; then # killed and left behind, never waited for
			kill -9 -$bp 2>/dev/null
			kill -9 $bp 2>/dev/null
			return 124
		fi
		sleep 1
		i=$((i + 1))
	done
	wait $bp
}

# a connect started by the rescue, killed as a whole before another one or when done
connect_start() {
	connect_stop
	setsid "$WIFI/connect_to_network.sh" "$WIFI_SSID" "$WIFI_PSK" >>"$LOG" 2>&1 </dev/null &
	cpid=$!
}
connect_stop() {
	[ -n "$cpid" ] && kill -9 -$cpid 2>/dev/null
	cpid=""
}

routed() { awk '$1=="eth0" && $2=="00000000" {f=1} END {exit !f}' "$ROUTE" 2>/dev/null; }

touches() { awk '/zForce/ {print $2}' "$IRQ" 2>/dev/null; }

# conf VALUE of KEY from dash.conf, quotes stripped (never echoed: it holds the bot token)
conf() { sed -n "s/^$1=//p" "$CONF" 2>/dev/null | tail -n 1 | sed 's/^["'\'']//; s/["'\'']$//'; }

# notify TEXT: Telegram, through whatever can speak HTTPS here, kobodash itself last
notify() {
	tok=$(conf TELEGRAM_TOKEN)
	chat=$(conf TELEGRAM_CHAT)
	thread=$(conf TELEGRAM_THREAD)
	[ -n "$tok" ] && [ -n "$chat" ] || return 1
	text=$(echo "$1" | sed 's/ /%20/g; s/·/%C2%B7/g')
	data="chat_id=$chat&text=$text"
	[ -n "$thread" ] && data="$data&message_thread_id=$thread"
	url="https://api.telegram.org/bot$tok/sendMessage"
	# the URL carries the token: curl reads it from stdin, so it never shows in ps
	if command -v curl >/dev/null 2>&1 && printf 'url = "%s"\n' "$url" | timeout 30 curl -fsS -o /dev/null -d "$data" -K - 2>/dev/null; then
		say "rescue: message sent (curl)"
		return 0
	fi
	if timeout 30 wget -q -O /dev/null --post-data "$data" "$url" 2>/dev/null; then
		say "rescue: message sent (wget)"
		return 0
	fi
	for b in "$W/kobodash.prev" "$W/kobodash"; do
		if [ -x "$b" ] && timeout 40 "$b" -conf "$CONF" -notify "$1" >/dev/null 2>&1; then
			say "rescue: message sent (${b##*/})"
			return 0
		fi
	done
	say "rescue: no way to send the message"
	return 1
}

# reboot times (unix seconds, one per line) shared with kobodash's own watchdog: 3 a day
# between them. reboots_kept prints the ones from the last 24 h.
BUDGET=$W/watchdog-reboots
# card reads and writes for it are bounded: the rescue must never hang on a stuck card
reboots_kept() {
	now=$(date +%s)
	rm -f /tmp/kobodash.budget
	budget_ok=1
	if [ -e "$BUDGET" ]; then
		bounded 5 sh -c 'cat "$1" >/tmp/kobodash.budget.part && mv /tmp/kobodash.budget.part /tmp/kobodash.budget' sh "$BUDGET"
		[ -f /tmp/kobodash.budget ] || budget_ok=0 # unreadable: unknown, so no reboot
	fi
	for t in $(cat /tmp/kobodash.budget 2>/dev/null); do
		case $t in '' | *[!0-9]*) continue ;; esac
		d=$((now - t))
		[ $d -lt 86400 ] && [ $d -gt -86400 ] && echo "$t"
	done
}

rescue() { # rescue REASON PID
	say "RESCUE: $1"
	# SIGKILL, never TERM: kobodash's TERM handler takes the start back off its update trial,
	# and a release that keeps needing rescue must use the trial up. Never "kill 0" (our group).
	[ "${2:-0}" -gt 1 ] && kill -9 "$2" 2>/dev/null
	killall -9 fbink connect_to_network.sh udhcpc wpa_supplicant 2>/dev/null
	bounded 40 "$WIFI/toggle.sh" off
	rm -f /run/connect_to_network.sh.pid
	bounded 40 "$WIFI/toggle.sh" on
	connect_start
	i=0
	while ! routed && [ $i -lt 120 ]; do
		sleep 1
		i=$((i + 1))
	done
	pidof sshd >/dev/null 2>&1 || bounded 20 "$SSHD"
	ip=$(ip -4 addr show eth0 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
	if routed; then
		say "rescue: Wi-Fi up (${ip:-no address}), sshd $(pidof sshd >/dev/null 2>&1 && echo running || echo DOWN)"
		notify "Kobo kurtarma modu · $1 · IP ${ip:-?} · ssh acik, $((RESCUE_HOLD / 60)) dk sonra yeniden baslar"
	else
		say "rescue: no network either"
	fi
	held=0
	while [ $held -lt "$RESCUE_HOLD" ]; do
		sleep "$TICK"
		held=$((held + TICK))
		if ! routed && [ $((held % 300)) -lt "$TICK" ]; then # lost it again: one fresh try every 5 min
			connect_start
		fi
	done
	connect_stop
	reboots_kept >/tmp/kobodash.kept
	kept=$(cat /tmp/kobodash.kept)
	if [ $budget_ok -eq 0 ]; then
		say "rescue: over, can't read the reboot budget: not rebooting"
	elif [ "$(echo "$kept" | grep -c .)" -lt $RESCUE_MAX ]; then
		{ echo "$kept"; date +%s; } | grep . >/tmp/kobodash.budget.new
		rm -f /tmp/kobodash.budget.ok
		bounded 5 sh -c 'cp "$1" "$2.tmp" && mv "$2.tmp" "$2" && touch /tmp/kobodash.budget.ok' sh /tmp/kobodash.budget.new "$BUDGET"
		if [ -f /tmp/kobodash.budget.ok ]; then
			say "rescue: over, rebooting"
			sync &
			sleep 5
			$REBOOT
			sleep 60
		else
			say "rescue: over, can't record a reboot: not rebooting"
		fi
	else
		say "rescue: over, $RESCUE_MAX reboots today already"
	fi
	say "rescue: restarting kobodash"
}

# guard PID: returns when PID exits, or after a rescue
guard() {
	last=""
	moved=$beats_seen # a binary that never writes the heartbeat (older releases) is never called stuck;
	# once one run has, a later run that hangs before its first beat counts as stuck too
	same=0
	nonet=0
	prev=$(date +%s)
	tp=$(touches)
	while kill -0 "$1" 2>/dev/null; do
		sleep "$TICK"
		now=$(date +%s)
		if [ $((now - prev)) -gt $((TICK * 3 + 30)) ]; then # the device slept in between
			same=0
			nonet=0
		fi
		prev=$now
		b=$(cat "$BEAT" 2>/dev/null)
		if [ "$b" = "$last" ]; then same=$((same + TICK)); else same=0; last=$b; [ -n "$b" ] && moved=1 beats_seen=1; fi
		t=$(touches)
		case $b in *off) off=1 ;; *) off=0 ;; esac # kobodash says the radio is off on purpose
		if routed || [ "$t" != "$tp" ] || [ $off -eq 1 ]; then nonet=0; else nonet=$((nonet + TICK)); fi
		tp=$t
		if [ $moved -eq 1 ] && [ $same -ge "$RESCUE_STALE" ]; then
			rescue "kobodash ${same}s donuk" "$1"
			return
		fi
		if [ $nonet -ge "$RESCUE_NONET" ]; then
			rescue "${nonet}s uyanik ve ag yok" "$1"
			return
		fi
	done
}

fails=0
beats_seen=0
while :; do
	bin=$W/kobodash
	[ -x "$bin" ] || bin=$W/kobodash.prev
	if [ ! -x "$bin" ]; then # never give up: a missing binary is still worth a rescue
		say "no runnable binary"
		rescue "kobodash yok" ""
		continue
	fi
	start=$(date +%s)
	rm -f "$BEAT" # a fresh run starts with no heartbeat: a hang before its first one shows
	[ -f "$LOG" ] && [ "$(wc -c <"$LOG")" -gt 1048576 ] && mv -f "$LOG" "$LOG.old" # tmpfs is RAM
	"$bin" -server "$SERVER" >>"$LOG" 2>&1 </dev/null &
	pid=$!
	guard $pid
	if kill -0 $pid 2>/dev/null; then # still there after SIGKILL: stuck in the kernel, waiting would hang us
		say "kobodash $pid won't die, starting another one"
		rc=137
	else
		wait $pid 2>/dev/null
		rc=$?
	fi
	ran=$(($(date +%s) - start))
	# any failure right after start rolls back, a signal too (SIGILL from a build for the wrong
	# CPU dies before Go can turn it into an exit code); not TERM/INT, which are someone stopping it
	if [ $rc -ne 0 ] && [ $rc -ne 143 ] && [ $rc -ne 130 ] && [ $ran -lt 60 ] && [ -x "$W/kobodash.prev" ] && [ "$bin" = "$W/kobodash" ]; then
		say "kobodash exited $rc right after start: rolling back"
		mv "$W/kobodash.prev" "$W/kobodash"
	fi
	if [ $ran -lt 60 ]; then fails=$((fails + 1)); else fails=0; fi
	case $fails in
	0 | 1) delay=3 ;;
	2) delay=10 ;;
	3) delay=30 ;;
	*) delay=60 ;;
	esac
	sleep $delay
done
