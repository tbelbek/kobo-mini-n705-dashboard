package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A fake GitHub serving one release; checks install / reject / no-op paths.
func TestCheckUpdate(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	newBin := []byte("NEW BINARY")

	cases := []struct {
		name, tag string
		sig       []byte
		installed bool
	}{
		{"good signature installs", "v2", ed25519.Sign(priv, newBin), true},
		{"wrong key is rejected", "v2", ed25519.Sign(otherPriv, newBin), false},
		{"same version is a no-op", "v1", ed25519.Sign(priv, newBin), false},
		{"older tag is a no-op", "v0.9", ed25519.Sign(priv, newBin), false},
		{"rejected tag is a no-op", "v3", ed25519.Sign(priv, newBin), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/releases/latest":
					fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":"kobodash-armv7","browser_download_url":"%s/bin"},`+
						`{"name":"kobodash-armv7.sig","browser_download_url":"%s/sig"}]}`, c.tag, srv.URL, srv.URL)
				case "/bin":
					w.Write(newBin)
				case "/sig":
					w.Write(c.sig)
				}
			}))
			defer srv.Close()

			updateDir = t.TempDir()
			cur := filepath.Join(updateDir, "kobodash")
			os.WriteFile(cur, []byte("OLD BINARY"), 0755)
			os.WriteFile(filepath.Join(updateDir, "rejected"), []byte("v3"), 0o644)
			version = "v1"
			a := &App{cfg: Config{UpdateRepo: "o/r", UpdateAPI: srv.URL, UpdatePubKey: hex.EncodeToString(pub)}}

			if got := a.checkUpdate(); got != c.installed {
				t.Fatalf("checkUpdate() = %v, want %v", got, c.installed)
			}
			now, _ := os.ReadFile(cur)
			prev, prevErr := os.ReadFile(cur + ".prev")
			if c.installed {
				if string(now) != "NEW BINARY" || string(prev) != "OLD BINARY" {
					t.Fatalf("after install: current=%q prev=%q", now, prev)
				}
				if tr, _ := os.ReadFile(filepath.Join(updateDir, "trying")); string(tr) != c.tag {
					t.Fatalf("trying = %q, want %q", tr, c.tag)
				}
			} else if string(now) != "OLD BINARY" || prevErr == nil {
				t.Fatalf("must not touch files: current=%q prev exists=%v", now, prevErr == nil)
			}
		})
	}
}

// A .prev that can't be written must stop the install: a rollback would restore it.
func TestInstallAbortsWithoutBackup(t *testing.T) {
	updateDir = t.TempDir()
	cur := filepath.Join(updateDir, "kobodash")
	os.WriteFile(cur, []byte("OLD"), 0755)
	os.Mkdir(cur+".prev", 0755) // writing a file over a directory fails
	if err := installBinary([]byte("NEW"), "v2"); err == nil {
		t.Fatal("install succeeded without a backup")
	}
	if b, _ := os.ReadFile(cur); string(b) != "OLD" {
		t.Fatalf("current = %q", b)
	}
	for _, f := range []string{".new"} {
		if _, err := os.Stat(cur + f); err == nil {
			t.Errorf("%s left behind", f)
		}
	}
	if _, err := os.Stat(filepath.Join(updateDir, "trying")); err == nil {
		t.Error("trying left behind")
	}
}

// A release on trial that keeps restarting puts the previous binary back and is rejected;
// the binary supervise.sh rolled back to rejects it too; a passed trial clears the marker.
func TestTrial(t *testing.T) {
	var exited bool
	exit = func(int) { exited = true }
	defer func() { exit = os.Exit }()
	setup := func() string {
		updateDir = t.TempDir()
		cur := filepath.Join(updateDir, "kobodash")
		os.WriteFile(cur, []byte("NEW"), 0755)
		os.WriteFile(cur+".prev", []byte("OLD"), 0755)
		os.WriteFile(filepath.Join(updateDir, "trying"), []byte("v2"), 0o644)
		exited = false
		return cur
	}
	read := func(f string) string { b, _ := os.ReadFile(filepath.Join(updateDir, f)); return string(b) }

	version = "v2"
	cur := setup()
	for i := 1; i < trialStarts; i++ {
		trialStart()
		if exited || read("kobodash") != "NEW" {
			t.Fatalf("rolled back after %d starts", i)
		}
	}
	trialStart()
	if !exited || read("kobodash") != "OLD" || read("rejected") != "v2" || read("trying") != "" {
		t.Fatalf("no rollback: exited=%v current=%q rejected=%q", exited, read("kobodash"), read("rejected"))
	}
	if _, err := os.Stat(cur + ".prev"); err == nil {
		t.Error(".prev still there")
	}

	setup()
	version = "v1" // supervise.sh restored v1
	trialStart()
	if exited || read("rejected") != "v2" || read("trying") != "" {
		t.Fatalf("rolled-back binary: exited=%v rejected=%q trying=%q", exited, read("rejected"), read("trying"))
	}

	setup() // clean stops (killall, deploy) don't use up the trial
	version = "v2"
	for i := 0; i < 3*trialStarts; i++ {
		trialStart()
		trialStopped()
	}
	if exited || read("kobodash") != "NEW" || read("rejected") != "" {
		t.Fatalf("clean restarts rolled back: exited=%v rejected=%q", exited, read("rejected"))
	}

	setup()
	version = "v2"
	trialStart()
	trialPassed()
	if read("trying") != "" || read("starts") != "" || read("rejected") != "" {
		t.Fatalf("passed trial left trying=%q starts=%q rejected=%q", read("trying"), read("starts"), read("rejected"))
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.7.26", "v0.7.25", true}, {"v0.7.10", "v0.7.9", true}, {"v0.8", "v0.7.30", true},
		{"v0.7.25", "v0.7.25", false}, {"v0.7.3", "v0.7.25", false}, {"v1", "v0.9.9", true},
		{"nightly", "v0.7.25", true}, {"v1.0", "v1", false}, {"v1", "v1.0", false},
	} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestGetIfChanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte("body"))
	}))
	defer srv.Close()
	f := filepath.Join(t.TempDir(), "etag")
	b, etag, err := getIfChanged(srv.URL, f)
	if err != nil || string(b) != "body" || etag != `"v1"` {
		t.Fatalf("first: %q %q %v", b, etag, err)
	}
	os.WriteFile(f, []byte(etag), 0o644)
	if b, _, err := getIfChanged(srv.URL, f); err != nil || b != nil {
		t.Fatalf("second should be a 304: %q %v", b, err)
	}
}

// A missing state file is empty and known; one that can't be read is unknown.
func TestReadState(t *testing.T) {
	d := t.TempDir()
	if s, ok := readState(d + "/none"); s != "" || !ok {
		t.Errorf("missing: %q %v", s, ok)
	}
	os.WriteFile(d+"/f", []byte("x"), 0o644)
	if s, ok := readState(d + "/f"); s != "x" || !ok {
		t.Errorf("file: %q %v", s, ok)
	}
	if _, ok := readState(d); ok { // a directory: read error
		t.Error("unreadable counted as known")
	}
}

// The wrapped transport still closes idle connections (done before every suspend).
func TestAnsweredClosesIdle(t *testing.T) {
	var _ interface{ CloseIdleConnections() } = answered{http.DefaultTransport}
}
