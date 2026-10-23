package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMsgHubValidate(t *testing.T) {
	ok := MsgHubConfig{URL: "wss://hub.example.com", TokenFile: "secrets/hub"}
	tests := []struct {
		name string
		mod  func(m *MsgHubConfig)
		ok   bool
	}{
		{"valid", func(*MsgHubConfig) {}, true},
		{"loopback ws", func(m *MsgHubConfig) { m.URL = "ws://127.0.0.1:8787" }, true},
		{"remote ws", func(m *MsgHubConfig) { m.URL = "ws://hub.example.com" }, false},
		{"https scheme", func(m *MsgHubConfig) { m.URL = "https://hub.example.com" }, false},
		{"no url", func(m *MsgHubConfig) { m.URL = "" }, false},
		{"no token file", func(m *MsgHubConfig) { m.TokenFile = " " }, false},
	}
	for _, tt := range tests {
		m := ok
		tt.mod(&m)
		if err := m.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: err=%v", tt.name, err)
		}
	}
}

func TestMsgHubToken(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tok"), []byte("abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	updateResolver(dir)
	if got, err := (MsgHubConfig{TokenFile: "tok"}).Token(); err != nil || got != "abc" {
		t.Fatalf("got %q, %v", got, err)
	}
}
