#!/bin/bash
# Build a kobodash SD image from the stock InkBox 2.0 n705 image.
#
# What it changes (everything else is stock InkBox):
#   - roots the device offline, exactly like restore-root.sh does:
#       "rooted" at sector 79872 + root-compliant kernel (uImage-root) at sector 81920
#       (both are required: one without the other makes init refuse to boot)
#   - p1 flags: X11_START=false, RW_ROOTFS=true
#   - p3 rootfs/write/ (the unsigned unionfs upper layer over the signed rootfs):
#       stub inkbox_gui, update_inkbox and ipd, add kobodash service, enable sshd
#
# Usage: WIFI_SSID=... WIFI_PSK=... [SERVER=http://host:port] [PUBKEY=~/.ssh/id_ed25519.pub] \
#          image/build.sh <inkbox-2.0-n705.xz> <out.img>
# Needs Docker (for e2fsprogs/squashfs-tools); the helper image is built from image/Dockerfile.
set -euo pipefail

SRC=${1:?inkbox .xz image}
OUT=${2:?output .img}
: "${WIFI_SSID:?set WIFI_SSID}" "${WIFI_PSK:?set WIFI_PSK}"
SERVER=${SERVER:-} # optional: the Kobo POSTs its IP to $SERVER/kobo/report on boot (see web/server.py)
PUBKEY=${PUBKEY:-$HOME/.ssh/id_ed25519.pub}
HERE=$(cd "$(dirname "$0")" && pwd)

docker image inspect kobo-tools >/dev/null 2>&1 || docker build -q -t kobo-tools "$HERE"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
cp -R "$HERE/overlay" "$WORK/overlay"
cp "$PUBKEY" "$WORK/overlay/opt/kobodash/authorized_keys"
printf 'WIFI_SSID=%q\nWIFI_PSK=%q\nSERVER=%q\n' "$WIFI_SSID" "$WIFI_PSK" "$SERVER" \
	> "$WORK/overlay/opt/kobodash/kobodash.conf"

echo "== decompressing $SRC"
xzcat "$SRC" > "$OUT"

OUTDIR=$(cd "$(dirname "$OUT")" && pwd)
docker run --rm -i -v "$OUTDIR:/out" -v "$WORK:/work" kobo-tools sh -s "$(basename "$OUT")" <<'EOF'
set -eu
IMG=/out/$1
P1_START=40959;   P1_LEN=38913      # boot (flags)
P2_START=104448;  P2_LEN=1024000    # recovery
P3_START=1128448; P3_LEN=262144     # rootfs (signed squashfs + unsigned overlay dirs)

extract() { dd if="$IMG" of="$2" bs=512 skip="$1" count="$3" status=none; e2fsck -fy "$2" >/dev/null 2>&1 || true; }
putback() { dd if="$2" of="$IMG" bs=512 seek="$1" conv=notrunc status=none; }

echo "== root: kernel + flag sector"
extract $P2_START /work/p2.img $P2_LEN
debugfs -c -R "dump /recoveryfs.squashfs /work/r.sq" /work/p2.img 2>/dev/null
unsquashfs -q -d /work/r /work/r.sq opt/recovery/restore/uImage-root >/dev/null
dd if=/work/r/opt/recovery/restore/uImage-root of="$IMG" bs=512 seek=81920 conv=notrunc status=none
printf 'rooted\n' | dd of="$IMG" bs=512 seek=79872 conv=notrunc status=none

echo "== p1 flags"
extract $P1_START /work/p1.img $P1_LEN
echo false > /work/X11_START
echo true > /work/RW_ROOTFS
debugfs -w -f - /work/p1.img >/dev/null 2>&1 <<CMD
rm /flags/X11_START
write /work/X11_START /flags/X11_START
write /work/RW_ROOTFS /flags/RW_ROOTFS
CMD
putback $P1_START /work/p1.img

echo "== p3 overlay"
extract $P3_START /work/p3.img $P3_LEN
W=/rootfs/write
{
	for d in /rootfs $W $W/etc $W/etc/init.d $W/etc/runlevels $W/etc/runlevels/default $W/opt $W/opt/kobodash; do
		echo "mkdir $d"; echo "sif $d mode 040755"
	done
	cd /work/overlay
	find . -type f | sed 's#^\./##' | while read -r f; do
		echo "write /work/overlay/$f $W/$f"
		case "$f" in
			*kobodash.conf) m=0100600 ;;
			*authorized_keys) m=0100644 ;;
			*) m=0100755 ;;
		esac
		echo "sif $W/$f mode $m"; echo "sif $W/$f uid 0"; echo "sif $W/$f gid 0"
	done
	echo "symlink $W/etc/runlevels/default/kobodash /etc/init.d/kobodash"
	echo "symlink $W/etc/runlevels/default/sshd /etc/init.d/sshd"
} > /work/p3.cmd
debugfs -w -f /work/p3.cmd /work/p3.img >/dev/null 2>&1
e2fsck -fn /work/p3.img >/dev/null 2>&1 && echo "p3 fsck clean"
putback $P3_START /work/p3.img

echo "== verify"
dd if="$IMG" bs=512 skip=79872 count=1 status=none | head -c 6; echo
K_IMG=$(dd if="$IMG" bs=512 skip=81920 count=1 status=none | md5sum | cut -d' ' -f1)
K_SRC=$(head -c 512 /work/r/opt/recovery/restore/uImage-root | md5sum | cut -d' ' -f1)
[ "$K_IMG" = "$K_SRC" ] && echo "uImage-root at 81920: ok" || echo "uImage-root at 81920: MISMATCH"
extract $P1_START /work/v1.img $P1_LEN
for f in FIRST_BOOT X11_START RW_ROOTFS; do printf "%s=" $f; debugfs -c -R "cat /flags/$f" /work/v1.img 2>/dev/null; done
extract $P3_START /work/v3.img $P3_LEN
debugfs -c -R "ls -l $W/etc/init.d" /work/v3.img 2>/dev/null
debugfs -c -R "ls -l $W/etc/runlevels/default" /work/v3.img 2>/dev/null
debugfs -c -R "ls -l $W/opt/kobodash" /work/v3.img 2>/dev/null
EOF
echo "== done: $OUT"
