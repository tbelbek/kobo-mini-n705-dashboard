#!/bin/sh
# Off-device check of supervise.sh: fake kobodash, Wi-Fi scripts and /proc files in a temp
# dir, one scenario per run, each asserting what the guard must (not) do.
#   image/test-supervise.sh
set -u
SV=$(cd "$(dirname "$0")" && pwd)/overlay/opt/kobodash/supervise.sh
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/w" "$T/wifi" "$T/bin"
export T

printf 'Iface\tDestination\tGateway\neth0\t00000000\t0101A8C0\n' >"$T/route.on"
printf 'Iface\tDestination\tGateway\n' >"$T/route.off"
echo 'WIFI_SSID=x WIFI_PSK=y SERVER=' >"$T/kconf"
printf 'TELEGRAM_TOKEN="x"\nTELEGRAM_CHAT=1\n' >"$T/conf"
for c in curl wget pidof ip; do printf '#!/bin/sh\nexit 1\n' >"$T/bin/$c"; done # no network from a test
printf '#!/bin/sh\nshift; exec "$@"\n' >"$T/bin/timeout"
printf '#!/bin/sh\nexec "$@"\n' >"$T/bin/setsid"
printf '#!/bin/sh\necho reboot >>"$T/events"\n' >"$T/bin/reboot"
cat >"$T/bin/date" <<'EOF'
#!/bin/sh
[ "$1" = "+%s" ] && echo $(($(/bin/date +%s) + $(cat "$T/clockjump" 2>/dev/null || echo 0))) || exec /bin/date "$@"
EOF
cat >"$T/wifi/toggle.sh" <<'EOF'
#!/bin/sh
echo "toggle $1" >>"$T/events"
[ "$1" = on ] && [ -f "$T/wifi_works" ] && cp "$T/route.on" "$T/route"
[ "$1" = off ] && cp "$T/route.off" "$T/route"
exit 0
EOF
cat >"$T/wifi/connect_to_network.sh" <<'EOF'
#!/bin/sh
echo connect >>"$T/events"; [ -f "$T/wifi_works" ] && cp "$T/route.on" "$T/route"; exit 0
EOF
cat >"$T/kobodash.fake" <<'EOF'
#!/bin/sh
[ "${3:-}" = -notify ] && { echo "notify" >>"$T/events"; exit 0; }
echo "start" >>"$T/events"
n=0
while :; do
	case $(cat "$T/mode") in
	healthy) n=$((n + 1)); echo $n >"$T/beat" ;;
	hang) ;;
	nonet) n=$((n + 1)); echo $n >"$T/beat"; cp "$T/route.off" "$T/route" ;;
	offline) n=$((n + 1)); echo "$n off" >"$T/beat"; cp "$T/route.off" "$T/route" ;;
	crash) exit 3 ;;
	esac
	sleep 1
done
EOF
printf '#!/bin/sh\nwhile :; do sleep 1; done\n' >"$T/kobodash.old" # an older release: never beats
chmod +x "$T"/bin/* "$T"/wifi/* "$T"/kobodash.*

# run MODE SECONDS [wifi_works] [hang_after_s]: start the guard, let it run, stop everything
run() {
	: >"$T/events"
	: >"$T/log"
	rm -f "$T/beat" "$T/clockjump"
	echo "$1" >"$T/mode"
	cp "$T/route.on" "$T/route"
	printf ' 271:        157           -  zForce-ir-touch\n' >"$T/irq"
	if [ "${3:-}" = wifi_works ]; then touch "$T/wifi_works"; else rm -f "$T/wifi_works"; fi
	PATH=$T/bin:$PATH KD_KCONF=$T/kconf KD_W=$T/w KD_BEAT=$T/beat KD_LOG=$T/log KD_ROUTE=$T/route \
		KD_IRQ=$T/irq KD_WIFI=$T/wifi KD_CONF=$T/conf KD_PIDFILE=$T/pid KD_REBOOT=$T/bin/reboot KD_SSHD=true \
		KD_TICK=1 KD_NONET=6 KD_STALE=6 KD_HOLD=3 KD_BACKOFF="1 1 1 1" sh "$SV" 2>/dev/null &
	sv=$!
	[ -n "${4:-}" ] && { sleep "$4"; echo hang >"$T/mode"; }
	sleep $(($2 - ${4:-0}))
	for c in $(pgrep -P $sv); do pkill -P "$c" 2>/dev/null; kill "$c" 2>/dev/null; done
	kill $sv 2>/dev/null
	wait $sv 2>/dev/null
	sleep 1
}

fails=0
check() { # check NAME CONDITION...
	n=$1
	shift
	if "$@"; then echo "ok   $n"; else echo "FAIL $n"; grep supervise "$T/log" | sed 's/^/     /'; fails=$((fails + 1)); fi
}
rescued() { grep -q RESCUE "$T/log"; }
rebooted() { grep -q '^reboot$' "$T/events"; }
not() { ! "$@"; }

use() { cp "$T/kobodash.$1" "$T/w/kobodash"; rm -f "$T/w/kobodash.prev" "$T/w/watchdog-reboots"; }

use fake; run healthy 10
check "healthy: no rescue" not rescued
use old; run healthy 10
check "release without heartbeat: no rescue" not rescued
use fake; run nonet 12
check "awake without network: rescue" rescued
use fake; run offline 12
check "Wi-Fi off on purpose: no rescue" not rescued
use fake; (j=0; for _ in 1 2 3 4 5 6; do j=$((j + 600)); echo $j >"$T/clockjump"; sleep 2; done) & run nonet 13; wait
check "no network across suspends: no rescue" not rescued
use fake; run healthy 26 wifi_works 4
check "frozen heartbeat: rescue and reboot" sh -c 'grep -q RESCUE "$T/log" && grep -q "^reboot$" "$T/events"'
check "frozen heartbeat: reboot recorded" test "$(grep -c . "$T/w/watchdog-reboots")" -eq 1
use fake; now=$(date +%s); printf '%s\n%s\n%s\n' "$now" "$now" "$now" >"$T/w/watchdog-reboots"; run healthy 26 wifi_works 4
check "3 reboots today: rescue, no reboot" sh -c 'grep -q RESCUE "$T/log" && ! grep -q "^reboot$" "$T/events"'
use fake; mkfifo "$T/w/watchdog-reboots"; run healthy 36 wifi_works 4
check "unreadable budget (stuck card): no reboot" sh -c 'grep -q "read the reboot budget" "$T/log" && ! grep -q "^reboot$" "$T/events"'
rm -f "$T/w/watchdog-reboots"
use fake; printf 'abc\n12x\n\n' >"$T/w/watchdog-reboots"; run healthy 26 wifi_works 4
check "garbage in the budget: still works" rebooted
use fake; (sleep 3; echo crash >"$T/mode"; sleep 2; echo hang >"$T/mode") & run healthy 24 wifi_works; wait
check "hang before the first beat after a run that beat: rescue" rescued
use fake; run crash 14 wifi_works
check "crash loop with nothing to roll back to: rescue" grep -q "ust uste coktu" "$T/log"
use fake; printf '08\n09\n' >"$T/w/watchdog-reboots"; run healthy 26 wifi_works 4
check "octal-looking budget entries: guard survives" grep -q "rescue: over" "$T/log"
use fake; cp "$T/kobodash.old" "$T/w/kobodash.prev"; run crash 8
check "crash at start with .prev: rolled back" grep -q "rolling back" "$T/log"
rm -f "$T/w/kobodash" "$T/w/kobodash.prev"; run healthy 9 wifi_works
check "no binary: rescue, guard keeps going" rescued
echo "$fails failed"
[ $fails -eq 0 ]
