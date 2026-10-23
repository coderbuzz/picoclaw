//go:build whatsapp_native

package whatsapp

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func newRegDir(t *testing.T) (cfg, apps string) {
	t.Helper()
	cfg = t.TempDir()
	apps = filepath.Join(cfg, dmAppsDir)
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	config.SetConfigDir(cfg)
	writeFile(t, filepath.Join(cfg, "secrets", "k"), "key1", 0o600)
	return cfg, apps
}

func writeFile(t *testing.T, p, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(p, mode) // bypass umask
}

func app(prefix string, extra string) string {
	return `{"prefix":"` + prefix + `","url":"https://example.com/x","secret_file":"secrets/k"` + extra + `}`
}

func names(hs []dmWebhook) (out []string) {
	for _, h := range hs {
		out = append(out, h.name)
	}
	return out
}

func TestLoadDMApps_SkipRules(t *testing.T) {
	_, apps := newRegDir(t)
	writeFile(t, filepath.Join(apps, "a.json"), app("ALPHA", ""), 0o600)
	writeFile(t, filepath.Join(apps, "b.json"), "{broken", 0o600)
	writeFile(t, filepath.Join(apps, "c.json"), app("GAMMA", ""), 0o664) // group-writable
	writeFile(t, filepath.Join(apps, "d.json"), app("DELTA", `,"enabled":false`), 0o600)
	writeFile(t, filepath.Join(apps, "e.json"), app("EPSILON", `,"secret_file":"secrets/none"`), 0o600)
	writeFile(t, filepath.Join(apps, "f.json"), app("ZETA", ""), 0o600)
	writeFile(t, filepath.Join(apps, "g.json"), app("dup", ""), 0o600) // invalid prefix
	writeFile(t, filepath.Join(apps, "h1.json"), app("SAME", ""), 0o600)
	writeFile(t, filepath.Join(apps, "h2.json"), app("SAME", ""), 0o600)
	got := names(loadDMApps(apps))
	if len(got) != 2 || got[0] != "a" || got[1] != "f" {
		t.Fatalf("loaded %v", got)
	}
	if loadDMApps(filepath.Join(apps, "missing")) != nil {
		t.Fatal("missing folder must yield no apps")
	}
}

func TestLoadDMApps_DefaultPatternWholeText(t *testing.T) {
	_, apps := newRegDir(t)
	writeFile(t, filepath.Join(apps, "nalar.json"), app("NALAR", ""), 0o600)
	hs := loadDMApps(apps)
	for text, want := range map[string]bool{
		"NALAR ABCD1234": true, "nalar abcd1234": true, "NALAR   ABC123": true,
		"NALAR ABCD1234 x": false, "hai NALAR ABCD1234": false, "NALAR ABC": false, "NALAR ABCD1234567890": false,
	} {
		if got := matchDMWebhook(hs, text) != nil; got != want {
			t.Errorf("%q: got %v want %v", text, got, want)
		}
	}
}

func TestDMRegistry_Reload(t *testing.T) {
	cfg, apps := newRegDir(t)
	now := time.Unix(1000, 0)
	r := newDMRegistry(apps)
	r.now = func() time.Time { return now }
	if len(r.current()) != 0 {
		t.Fatal("empty folder")
	}
	writeFile(t, filepath.Join(apps, "a.json"), app("ALPHA", ""), 0o600)
	if len(r.current()) != 0 {
		t.Fatal("must not recheck within the interval")
	}
	now = now.Add(3 * time.Second)
	if got := r.current(); len(got) != 1 || string(got[0].secret) != "key1" {
		t.Fatalf("added file: %v", names(got))
	}
	writeFile(t, filepath.Join(cfg, "secrets", "k"), "key2-longer", 0o600) // secret change only
	now = now.Add(3 * time.Second)
	if got := r.current(); string(got[0].secret) != "key2-longer" {
		t.Fatal("secret change not picked up")
	}
	writeFile(t, filepath.Join(apps, "a.json"), app("ALPHA", `,"url":"https://example.com/y"`), 0o600)
	now = now.Add(3 * time.Second)
	if got := r.current(); got[0].url != "https://example.com/y" {
		t.Fatal("changed file not picked up")
	}
	_ = os.Remove(filepath.Join(apps, "a.json"))
	now = now.Add(3 * time.Second)
	if len(r.current()) != 0 {
		t.Fatal("removed file still loaded")
	}
}

func TestDMWebhook_ConfigFirstThenRegistry(t *testing.T) {
	_, apps := newRegDir(t)
	writeFile(t, filepath.Join(apps, "a.json"), app("MASUK", ""), 0o600)
	h := newDMHarness(t, "", nil)
	h.ch.dmReg = newDMRegistry(apps)
	if got := h.ch.dmHooks[0]; got.name != "test" {
		t.Fatal("harness changed")
	}
	// Both match "MASUK ABCD1234"; the config entry must win.
	hook := matchDMWebhook(h.ch.dmHooks, "MASUK ABCD1234")
	if hook == nil || hook.name != "test" {
		t.Fatal("config webhook must match first")
	}
	if got := matchDMWebhook(h.ch.dmReg.current(), "MASUK ABCD1234"); got == nil || got.name != "a" {
		t.Fatal("registry app must match when config does not")
	}
}

func TestDMRateLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := &dmRateLimiter{now: func() time.Time { return now }}
	for i := 0; i < dmRateMax; i++ {
		if !l.allow("a") {
			t.Fatalf("call %d dropped", i+1)
		}
	}
	if l.allow("a") {
		t.Fatal("6th within the minute must drop")
	}
	if !l.allow("b") {
		t.Fatal("other sender is independent")
	}
	now = now.Add(dmRateWindow + time.Second)
	if !l.allow("a") {
		t.Fatal("allowed again after the window")
	}
}
