#!/bin/bash
# Build, sign and publish a kobodash release. Devices running a release build pick
# it up on their next update check (every UPDATE_HOURS) and verify the signature
# against the public key compiled into them before installing.
#
# Usage: tools/release.sh v0.2.0
# Needs: gh (logged in), the private key at ~/.config/kobodash/release.key
# (create one with `go run ./kobo -genkey <file>` and put the printed public key in
# kobo/update.go, or set UPDATE_PUBKEY in dash.conf for your fork).
set -euo pipefail
TAG=${1:?usage: tools/release.sh vX.Y.Z}
KEY=${KEY:-$HOME/.config/kobodash/release.key}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT

cd "$ROOT/kobo"
GOTOOLCHAIN=go1.23.12 GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
	go build -trimpath -ldflags="-s -w -X main.version=$TAG" -o "$OUT/kobodash-armv7" .
GOTOOLCHAIN=go1.23.12 go run . -sign "$KEY" "$OUT/kobodash-armv7"

git -C "$ROOT" tag "$TAG"
git -C "$ROOT" push origin "$TAG"
gh release create "$TAG" "$OUT/kobodash-armv7" "$OUT/kobodash-armv7.sig" --title "$TAG" --generate-notes
