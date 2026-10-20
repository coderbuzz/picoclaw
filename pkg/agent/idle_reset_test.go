package agent

import (
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestIdleExpired(t *testing.T) {
	now := time.Now()
	old, recent := now.Add(-3*time.Hour), now.Add(-time.Minute)
	msg := func(at *time.Time) []providers.Message { return []providers.Message{{Role: "user", CreatedAt: at}} }
	cases := []struct {
		name    string
		history []providers.Message
		idle    time.Duration
		want    bool
	}{
		{"disabled", msg(&old), 0, false},
		{"empty", nil, time.Hour, false},
		{"no timestamp", msg(nil), time.Hour, false},
		{"recent", msg(&recent), time.Hour, false},
		{"stale", msg(&old), time.Hour, true},
	}
	for _, tc := range cases {
		if got := idleExpired(tc.history, tc.idle, now); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
