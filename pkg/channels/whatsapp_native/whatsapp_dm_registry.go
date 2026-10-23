//go:build whatsapp_native

package whatsapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	dmAppsDir        = "dm_apps.d"
	dmRegistryEvery  = 2 * time.Second
	dmRateMax        = 5
	dmRateWindow     = time.Minute
	dmRateSweepAbove = 1024
)

var dmPrefixRe = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,15}$`)

// dmAppFile is one <config dir>/dm_apps.d/*.json entry.
type dmAppFile struct {
	Prefix     string `json:"prefix"`
	URL        string `json:"url"`
	SecretFile string `json:"secret_file"`
	Pattern    string `json:"pattern"`
	ErrorReply string `json:"error_reply"`
	Enabled    *bool  `json:"enabled"`
}

// dmRegistry holds the apps loaded from dir; current() reloads lazily when the folder changed.
type dmRegistry struct {
	dir      string
	interval time.Duration
	now      func() time.Time
	mu       sync.Mutex
	next     time.Time
	fp       string
	hooks    []dmWebhook
}

func newDMRegistry(dir string) *dmRegistry {
	return &dmRegistry{dir: dir, interval: dmRegistryEvery, now: time.Now}
}

// current returns the loaded apps, re-checking the folder at most once per interval (no watcher goroutine).
func (r *dmRegistry) current() []dmWebhook {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now := r.now(); !now.Before(r.next) {
		r.next = now.Add(r.interval)
		if fp := dmFingerprint(r.dir); fp != r.fp {
			r.fp, r.hooks = fp, loadDMApps(r.dir)
		}
	}
	return r.hooks
}

func dmAppNames(dir string) []string {
	names, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(names)
	return names
}

// dmFingerprint covers names, mtimes and sizes of the *.json files plus the mtimes and sizes of their secret files.
func dmFingerprint(dir string) string {
	var b strings.Builder
	cfgDir := config.ConfigDir()
	for _, p := range dmAppNames(dir) {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s|%d|%d;", filepath.Base(p), st.ModTime().UnixNano(), st.Size())
		var f dmAppFile
		if data, err := os.ReadFile(p); err == nil && json.Unmarshal(data, &f) == nil && f.SecretFile != "" {
			if sst, err := os.Stat(filepath.Join(cfgDir, f.SecretFile)); err == nil {
				fmt.Fprintf(&b, "%d|%d", sst.ModTime().UnixNano(), sst.Size())
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// loadDMApps loads every usable app sorted by file name; bad files are skipped with a warning (never the secret).
func loadDMApps(dir string) []dmWebhook {
	type app struct {
		hook   dmWebhook
		prefix string
	}
	var apps []app
	count := map[string]int{}
	for _, p := range dmAppNames(dir) {
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		h, prefix, enabled, err := loadDMApp(p, name)
		if err != nil {
			logger.WarnCF("whatsapp", "dm app skipped", map[string]any{"app": name, "reason": err.Error()})
			continue
		}
		if !enabled {
			continue
		}
		apps = append(apps, app{h, prefix})
		count[prefix]++
	}
	var out []dmWebhook
	for _, a := range apps {
		if count[a.prefix] > 1 {
			logger.WarnCF("whatsapp", "dm app skipped", map[string]any{"app": a.hook.name, "reason": "duplicate prefix"})
			continue
		}
		out = append(out, a.hook)
	}
	return out
}

func loadDMApp(path, name string) (h dmWebhook, prefix string, enabled bool, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return h, "", false, err
	}
	if st.Mode().Perm()&0o022 != 0 {
		return h, "", false, fmt.Errorf("file is group/other-writable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return h, "", false, err
	}
	var f dmAppFile
	if err := json.Unmarshal(data, &f); err != nil {
		return h, "", false, fmt.Errorf("invalid json")
	}
	if !dmPrefixRe.MatchString(f.Prefix) {
		return h, "", false, fmt.Errorf("invalid prefix")
	}
	if f.Pattern == "" {
		f.Pattern = `(?i)` + f.Prefix + `\s+[A-Z0-9]{6,12}`
	}
	if err := config.ValidateDMWebhookFields(f.Pattern, f.URL, f.SecretFile); err != nil {
		return h, "", false, err
	}
	if f.Enabled != nil && !*f.Enabled {
		return h, f.Prefix, false, nil
	}
	h, err = buildDMWebhook(name, config.DMWebhook{Pattern: f.Pattern, URL: f.URL, SecretFile: f.SecretFile, ErrorReply: f.ErrorReply})
	if err != nil {
		return h, "", false, fmt.Errorf("unreadable secret")
	}
	return h, strings.ToUpper(f.Prefix), true, nil
}

// dmRateLimiter allows dmRateMax webhook calls per sender in a rolling dmRateWindow.
type dmRateLimiter struct {
	mu   sync.Mutex
	now  func() time.Time // nil = time.Now
	hits map[string][]time.Time
}

func (l *dmRateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	if len(l.hits) > dmRateSweepAbove { // bound memory: drop senders with no recent hits
		for k, ts := range l.hits {
			if now.Sub(ts[len(ts)-1]) >= dmRateWindow {
				delete(l.hits, k)
			}
		}
	}
	recent := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < dmRateWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= dmRateMax {
		l.hits[key] = recent
		return false
	}
	l.hits[key] = append(recent, now)
	return true
}
