#!/bin/sh
# Keeps kobodash running from the unionfs write layer on partition 3.
# A self-update exits 0 to load the new binary. A binary that crashes (non-zero
# exit, not a signal) within a minute is replaced by the previous one.
. /opt/kobodash/kobodash.conf
echo $$ >/run/kobodash-supervise.pid # deploy.sh checks this instead of grepping ps
W=/tmp/p3/rootfs/write/opt/kobodash
mkdir -p /tmp/p3
grep -q " /tmp/p3 " /proc/mounts || mount -t ext4 /dev/mmcblk0p3 /tmp/p3

while [ -x "$W/kobodash" ]; do
	start=$(date +%s)
	"$W/kobodash" -server "$SERVER" >>/tmp/kobodash.log 2>&1
	rc=$?
	if [ $rc -ne 0 ] && [ $rc -lt 128 ] && [ $(($(date +%s) - start)) -lt 60 ] && [ -x "$W/kobodash.prev" ]; then
		echo "$(date) kobodash exited $rc right after start: rolling back" >>/tmp/kobodash.log
		mv "$W/kobodash.prev" "$W/kobodash"
	fi
	sleep 3
done
