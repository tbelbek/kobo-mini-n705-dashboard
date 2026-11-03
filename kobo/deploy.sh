#!/bin/bash
# Build kobodash and install it on the Kobo over ssh, without reflashing.
# Files go to the unionfs write layer on partition 3 (mounted by hand at /tmp/p3),
# so they show up under / immediately and survive reboots.
#
# Usage: kobo/deploy.sh <kobo-ip>      (config: ~/.config/kobodash/dash.conf, holds the ICS secret)
set -euo pipefail
IP=${1:?kobo ip}
HERE=$(cd "$(dirname "$0")" && pwd)
CONF=${CONF:-$HOME/.config/kobodash/dash.conf}
WL=/tmp/p3/rootfs/write # the initrd's /rootfs-part mount isn't reachable after switch_root


# VERSION=dev (default) never self-updates; deploy with a release tag (VERSION=v0.1.0)
# to put the device on the release track.
VERSION=${VERSION:-dev}
(cd "$HERE" && GOTOOLCHAIN=go1.23.12 GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
	go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o kobodash .)

scp -q "$HERE/kobodash" root@"$IP":/tmp/kobodash.new
scp -q "$CONF" root@"$IP":/tmp/dash.conf.new
scp -q "$HERE/../image/overlay/etc/init.d/ipd" root@"$IP":/tmp/ipd.new
scp -q "$HERE/../image/overlay/etc/init.d/kobodash" root@"$IP":/tmp/kobodash.rc
scp -q "$HERE/../image/overlay/opt/kobodash/run.sh" root@"$IP":/tmp/run.sh.new
scp -q "$HERE/../image/overlay/opt/kobodash/supervise.sh" root@"$IP":/tmp/supervise.sh.new
ssh root@"$IP" "set -e
	mkdir -p /tmp/p3
	grep -q ' /tmp/p3 ' /proc/mounts || mount -t ext4 /dev/mmcblk0p3 /tmp/p3
	install -m 755 /tmp/kobodash.new $WL/opt/kobodash/kobodash
	install -m 600 /tmp/dash.conf.new $WL/opt/kobodash/dash.conf
	install -m 755 /tmp/ipd.new $WL/etc/init.d/ipd   # kobodash owns sleep + power button
	install -m 755 /tmp/kobodash.rc $WL/etc/init.d/kobodash
	sh -n /tmp/run.sh.new && sh -n /tmp/supervise.sh.new   # a broken boot script would strand the device
	# copy next to the target, then rename: never a half-written script, and a running
	# supervise.sh keeps reading its old copy
	install -m 755 /tmp/run.sh.new $WL/opt/kobodash/run.sh.tmp && mv $WL/opt/kobodash/run.sh.tmp $WL/opt/kobodash/run.sh
	install -m 755 /tmp/supervise.sh.new $WL/opt/kobodash/supervise.sh.tmp && mv $WL/opt/kobodash/supervise.sh.tmp $WL/opt/kobodash/supervise.sh
	rm -f $WL/opt/kobodash/kobodash.prev   # a hand deploy is trusted; never roll back to an older build
	killall -9 ipd 2>/dev/null || true
	rm -f /tmp/kobodash.new /tmp/dash.conf.new /tmp/ipd.new /tmp/kobodash.rc /tmp/run.sh.new /tmp/supervise.sh.new
	killall kobodash 2>/dev/null || true   # supervise.sh restarts the new build
	# grepping ps would match this very command line; ask the pidfile instead
	kill -0 \$(cat /run/kobodash-supervise.pid 2>/dev/null) 2>/dev/null || { nohup /bin/sh /opt/kobodash/supervise.sh >/dev/null 2>&1 </dev/null & }
	sleep 6
	echo '--- kobodash log'; tail -5 /tmp/kobodash.log; pidof kobodash >/dev/null && echo running"
