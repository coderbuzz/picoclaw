package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateDMWebhooks(t *testing.T) {
	ok := DMWebhook{Pattern: `MASUK \w+`, URL: "https://example.test/hook", SecretFile: "secrets/x"}
	tests := []struct {
		name string
		mod  func(w *DMWebhook)
		ok   bool
	}{
		{"valid", func(*DMWebhook) {}, true},
		{"loopback http", func(w *DMWebhook) { w.URL = "http://127.0.0.1:8080/h" }, true},
		{"remote http", func(w *DMWebhook) { w.URL = "http://example.test/h" }, false},
		{"bad regex", func(w *DMWebhook) { w.Pattern = "(" }, false},
		{"empty pattern", func(w *DMWebhook) { w.Pattern = "" }, false},
		{"no secret file", func(w *DMWebhook) { w.SecretFile = "" }, false},
		{"no url", func(w *DMWebhook) { w.URL = "" }, false},
	}
	for _, tt := range tests {
		w := ok
		tt.mod(&w)
		err := (&WhatsAppSettings{DMWebhooks: []DMWebhook{w}}).ValidateDMWebhooks()
		if (err == nil) != tt.ok {
			t.Errorf("%s: err=%v", tt.name, err)
		}
	}
	if err := (&WhatsAppSettings{}).ValidateDMWebhooks(); err != nil {
		t.Fatalf("empty must be valid: %v", err)
	}
}

func TestDMWebhookSecret(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets", "hook"), []byte("abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	updateResolver(dir)
	got, err := DMWebhook{SecretFile: "secrets/hook"}.Secret()
	if err != nil || got != "abc" {
		t.Fatalf("got %q, %v", got, err)
	}
}
