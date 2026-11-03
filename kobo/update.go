package main

// Self-update from GitHub Releases. A release carries kobodash-armv7 and
// kobodash-armv7.sig, an ed25519 signature of the binary made with a private key
// that never leaves the maintainer's machine. The device only installs a binary
// whose signature verifies against the public key compiled in here (or UPDATE_PUBKEY).
//
// Install: write next to the running binary in the unionfs write layer, keep the old
// one as kobodash.prev, exit; run.sh restarts it and rolls back if it dies quickly.

import (
	"cmp"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

var version = "dev" // set with -ldflags "-X main.version=..."

// public half of the release key (tools/release.sh signs with the private half)
const releasePubKey = "c991c8e3c011782e9f5fdce219e240ffbe1db4992d9f256ca7ac27bbef1dddd9"

var updateDir = "/tmp/p3/rootfs/write/opt/kobodash" // p3 write layer, see deploy.sh (tests point it elsewhere)

// checkUpdate returns true if a new signed binary was installed (the caller exits).
func (a *App) checkUpdate() bool {
	if a.cfg.UpdateRepo == "" || version == "dev" {
		return false
	}
	pub, err := hex.DecodeString(a.cfg.UpdatePubKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		log.Printf("update: no valid public key")
		return false
	}
	b, etag, err := getIfChanged(a.cfg.UpdateAPI+"/repos/"+a.cfg.UpdateRepo+"/releases/latest", stateFile("update.etag"))
	if err != nil {
		log.Printf("update: %v", err)
		return false
	}
	if b == nil {
		return false // 304: same latest release as last time we looked
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(b, &rel); err != nil || rel.Tag == "" {
		return false
	}
	rejected, _ := os.ReadFile(filepath.Join(updateDir, "rejected"))
	if rel.Tag == version || !newer(rel.Tag, version) || rel.Tag == strings.TrimSpace(string(rejected)) {
		// up to date, an older tag, or a release that already failed here: remember the
		// ETag so the next check is a bodiless 304 instead of a download loop
		if rel.Tag != version {
			log.Printf("update: skipping %s (running %s)", rel.Tag, version)
		}
		os.WriteFile(stateFile("update.etag"), []byte(etag), 0o644)
		return false
	}
	urls := map[string]string{}
	for _, as := range rel.Assets {
		urls[as.Name] = as.URL
	}
	bin, err1 := get(urls["kobodash-armv7"])
	sig, err2 := get(urls["kobodash-armv7.sig"])
	if err := cmp.Or(err1, err2); err != nil {
		log.Printf("update %s: download: %v", rel.Tag, err)
		return false
	}
	if !ed25519.Verify(pub, bin, sig) {
		log.Printf("update %s: BAD SIGNATURE, ignoring", rel.Tag)
		return false
	}
	if err := installBinary(bin, rel.Tag); err != nil {
		log.Printf("update %s: install: %v", rel.Tag, err)
		return false
	}
	log.Printf("update: %s -> %s installed, restarting", version, rel.Tag)
	os.WriteFile(stateFile("updated"), []byte(version+" → "+rel.Tag), 0o644) // the new binary reports and sleeps
	return true
}

// installBinary puts bin in place of the running binary, keeps the old one as .prev and
// marks tag as on trial (see trialStart). Any failed write aborts before the swap: a
// truncated .prev would be what a rollback restores.
func installBinary(bin []byte, tag string) error {
	if strings.HasPrefix(updateDir, "/tmp/p3/") {
		if err := ensureP3(); err != nil {
			return err
		}
	}
	cur := filepath.Join(updateDir, "kobodash")
	tmp := cur + ".new"
	if err := os.WriteFile(tmp, bin, 0755); err != nil {
		os.Remove(tmp)
		return err
	}
	if old, err := os.ReadFile(cur); err == nil {
		if err := os.WriteFile(cur+".prev", old, 0755); err != nil { // supervise.sh restores this if the new one dies at start
			os.Remove(tmp)
			return err
		}
	}
	os.Remove(filepath.Join(updateDir, "starts"))
	if err := os.WriteFile(filepath.Join(updateDir, "trying"), []byte(tag), 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, cur); err != nil {
		os.Remove(filepath.Join(updateDir, "trying"))
		return err
	}
	return nil
}

// trialStart runs first thing on the device. A freshly installed release is on trial
// until it completes a sleep cycle (trialPassed). supervise.sh only rolls back a binary
// that dies within 60 s; one that crashes later, say after a 90 s Wi-Fi wait, would be
// restarted forever. So each start on trial is counted (a clean stop, trialStopped, is
// taken back off), and on the trialStarts-th start the binary puts .prev back itself.
// Either way the tag is remembered as rejected, so the old binary doesn't download it
// again on its next update check.
const trialStarts = 5

func trialStart() {
	t, err := os.ReadFile(filepath.Join(updateDir, "trying"))
	if err != nil {
		return
	}
	tag := strings.TrimSpace(string(t))
	if tag != version { // supervise.sh already put the previous binary back
		log.Printf("update: %s failed to start, rolled back to %s", tag, version)
		rejectTrial(tag)
		return
	}
	n, _ := strconv.Atoi(strings.TrimSpace(readString(filepath.Join(updateDir, "starts"))))
	n++
	os.WriteFile(filepath.Join(updateDir, "starts"), []byte(strconv.Itoa(n)), 0o644)
	if n < trialStarts {
		return
	}
	if trialRollback(fmt.Sprintf("restarted %d times without a full sleep cycle", n)) {
		exit(0) // supervise.sh starts the previous binary
	}
}

// onTrial: this binary is a release still on trial.
func onTrial() bool {
	t, err := os.ReadFile(filepath.Join(updateDir, "trying"))
	return err == nil && strings.TrimSpace(string(t)) == version
}

// trialRollback puts the previous binary back in place of this one (on trial) and
// rejects this release. True if the previous binary is now in place.
func trialRollback(why string) bool {
	cur := filepath.Join(updateDir, "kobodash")
	if _, err := os.Stat(cur + ".prev"); err != nil {
		log.Printf("update: %s %s, but no previous binary to go back to", version, why)
		rejectTrial(version)
		return false
	}
	log.Printf("update: %s %s: rolling back", version, why)
	if err := os.Rename(cur+".prev", cur); err != nil {
		log.Printf("update: rollback: %v", err)
		return false
	}
	rejectTrial(version)
	return true
}

// exit flushes the card log first; tests replace it.
var exit = func(code int) { flushLog(); os.Exit(code) }

func rejectTrial(tag string) {
	os.WriteFile(filepath.Join(updateDir, "rejected"), []byte(tag), 0o644)
	os.Remove(filepath.Join(updateDir, "trying"))
	os.Remove(filepath.Join(updateDir, "starts"))
}

// trialStopped: SIGTERM (killall, deploy.sh, OpenRC stop) is not a crash, so that start
// doesn't count against the trial.
func trialStopped() {
	p := filepath.Join(updateDir, "starts")
	if n, err := strconv.Atoi(strings.TrimSpace(readString(p))); err == nil && n > 0 {
		os.WriteFile(p, []byte(strconv.Itoa(n-1)), 0o644)
	}
}

// trialPassed: the new release slept and woke once, so it stays.
func trialPassed() {
	healMu.Lock()
	defer healMu.Unlock()
	if os.Remove(filepath.Join(updateDir, "trying")) == nil {
		log.Printf("update: %s passed its first sleep cycle", version)
	}
	os.Remove(filepath.Join(updateDir, "starts"))
}

func readString(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

// newer: tag is a later vX.Y.Z than cur. Tags that don't parse are let through, as before.
func newer(tag, cur string) bool {
	parse := func(v string) ([]int, bool) {
		var out []int
		for _, p := range strings.Split(strings.TrimPrefix(v, "v"), ".") {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil, false
			}
			out = append(out, n)
		}
		return out, len(out) > 0
	}
	a, ok1 := parse(tag)
	b, ok2 := parse(cur)
	for len(a) < len(b) { // v1 == v1.0
		a = append(a, 0)
	}
	for len(b) < len(a) {
		b = append(b, 0)
	}
	return !ok1 || !ok2 || slices.Compare(a, b) > 0
}

// ensureP3 mounts partition 3 (the unionfs write branch) at /tmp/p3 if needed.
func ensureP3() error {
	if m, _ := os.ReadFile("/proc/mounts"); strings.Contains(string(m), " /tmp/p3 ") {
		return nil
	}
	os.MkdirAll("/tmp/p3", 0755)
	if out, err := runBounded(30*time.Second, "mount", "-t", "ext4", "/dev/mmcblk0p3", "/tmp/p3"); err != nil {
		return fmt.Errorf("mount: %v %s", err, out)
	}
	return nil
}

// release tooling: kobodash -genkey <keyfile>, kobodash -sign <keyfile> <file>
func genKey(path string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(priv)), 0600); err != nil {
		return err
	}
	fmt.Println(hex.EncodeToString(pub))
	return nil
}

func signFile(keyPath, file string) error {
	k, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	priv, err := hex.DecodeString(strings.TrimSpace(string(k)))
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("bad private key")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return os.WriteFile(file+".sig", ed25519.Sign(ed25519.PrivateKey(priv), data), 0644)
}

// getIfChanged is a conditional GET: with the ETag saved in etagFile, an unchanged
// resource comes back as a bodiless 304 (and GitHub doesn't count it against the rate limit).
// It returns a nil body for 304, and the new ETag otherwise; the caller decides whether to save it.
func getIfChanged(u, etagFile string) ([]byte, string, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, "", err
	}
	if e, _ := os.ReadFile(etagFile); len(e) > 0 {
		req.Header.Set("If-None-Match", strings.TrimSpace(string(e)))
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 304:
		return nil, "", nil
	case 200:
		b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return b, resp.Header.Get("ETag"), err
	}
	return nil, "", fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
}
