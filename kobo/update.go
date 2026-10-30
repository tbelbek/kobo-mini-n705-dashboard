package main

// Self-update from GitHub Releases. A release carries kobodash-armv7 and
// kobodash-armv7.sig, an ed25519 signature of the binary made with a private key
// that never leaves the maintainer's machine. The device only installs a binary
// whose signature verifies against the public key compiled in here (or UPDATE_PUBKEY).
//
// Install: write next to the running binary in the unionfs write layer, keep the old
// one as kobodash.prev, exit; run.sh restarts it and rolls back if it dies quickly.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
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
	b, err := get(a.cfg.UpdateAPI + "/repos/" + a.cfg.UpdateRepo + "/releases/latest")
	if err != nil {
		log.Printf("update: %v", err)
		return false
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(b, &rel); err != nil || rel.Tag == "" || rel.Tag == version {
		return false
	}
	urls := map[string]string{}
	for _, as := range rel.Assets {
		urls[as.Name] = as.URL
	}
	bin, err1 := get(urls["kobodash-armv7"])
	sig, err2 := get(urls["kobodash-armv7.sig"])
	if err := firstErr(err1, err2); err != nil {
		log.Printf("update %s: download: %v", rel.Tag, err)
		return false
	}
	if !ed25519.Verify(pub, bin, sig) {
		log.Printf("update %s: BAD SIGNATURE, ignoring", rel.Tag)
		return false
	}
	if err := installBinary(bin); err != nil {
		log.Printf("update %s: install: %v", rel.Tag, err)
		return false
	}
	log.Printf("update: %s -> %s installed, restarting", version, rel.Tag)
	return true
}

func installBinary(bin []byte) error {
	if strings.HasPrefix(updateDir, "/tmp/p3/") {
		if err := ensureP3(); err != nil {
			return err
		}
	}
	cur := filepath.Join(updateDir, "kobodash")
	tmp := cur + ".new"
	if err := os.WriteFile(tmp, bin, 0755); err != nil {
		return err
	}
	if old, err := os.ReadFile(cur); err == nil {
		os.WriteFile(cur+".prev", old, 0755) // run.sh restores this if the new one dies at start
	}
	return os.Rename(tmp, cur)
}

// ensureP3 mounts partition 3 (the unionfs write branch) at /tmp/p3 if needed.
func ensureP3() error {
	if m, _ := os.ReadFile("/proc/mounts"); strings.Contains(string(m), " /tmp/p3 ") {
		return nil
	}
	os.MkdirAll("/tmp/p3", 0755)
	return runCmd("mount", "-t", "ext4", "/dev/mmcblk0p3", "/tmp/p3")
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
