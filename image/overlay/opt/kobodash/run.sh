#!/bin/sh
# kobodash launcher: bring Wi-Fi up, announce the IP, then hand the screen to the client.
. /opt/kobodash/kobodash.conf   # WIFI_SSID, WIFI_PSK, SERVER

FB=/opt/bin/fbink/fbink
# every step is bounded: nothing here may keep supervise.sh (and its rescue mode) from starting
say() { timeout 15 "$FB" -q -c -f -m -y 12 "$1"; [ -n "$2" ] && timeout 15 "$FB" -q -m -y 15 "$2"; }

say "kobodash" "Wi-Fi'ye baglaniyor..."
timeout 40 /usr/local/bin/wifi/toggle.sh on >/tmp/kobodash-wifi.log 2>&1
# own session, so a connect that gets nowhere can be killed whole below: left alone it
# waits 320 s for DHCP and then switches Wi-Fi off under whatever connected meanwhile
setsid /usr/local/bin/wifi/connect_to_network.sh "$WIFI_SSID" "$WIFI_PSK" >>/tmp/kobodash-wifi.log 2>&1 &
CP=$!

IP=""
i=0
while [ $i -lt 90 ]; do
	IP=$(ip -4 addr show eth0 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
	[ -n "$IP" ] && break
	sleep 2
	i=$((i + 1))
done

[ -z "$IP" ] && kill -9 -$CP 2>/dev/null # kobodash starts its own connect
if [ -n "$IP" ]; then
	# no network time otherwise: boots at whatever the RTC drifted to (often 1970)
	timeout 30 ntpd -q -n -p pool.ntp.org >/dev/null 2>&1 && timeout 10 hwclock -w -u >/dev/null 2>&1
fi

say "kobodash hazir" "IP: ${IP:-yok}  $(date -u '+%Y-%m-%d %H:%M') UTC"
[ -n "$IP" ] && [ -n "$SERVER" ] && timeout 15 wget -q -O- --post-data="kobo up ip=$IP" "$SERVER/kobo/report" >/dev/null 2>&1

# Phase 2: the dashboard client, kept alive (and rolled back if needed) by supervise.sh.
exec /bin/sh /opt/kobodash/supervise.sh
