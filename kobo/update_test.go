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
			} else if string(now) != "OLD BINARY" || prevErr == nil {
				t.Fatalf("must not touch files: current=%q prev exists=%v", now, prevErr == nil)
			}
		})
	}
}
