package groupactivity

import (
	"path/filepath"
	"slices"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	DBName           = "group_activity.db"
	DefaultRetention = 90
)

// Settings finds the group_activity block of the enabled native WhatsApp channel and the SQLite path next to the
// WhatsApp session store (same folder the channel uses). nil when not configured.
func Settings(cfg *config.Config) (*config.GroupActivityConfig, string) {
	for _, ch := range cfg.Channels {
		if ch == nil || !ch.Enabled || ch.Type != config.ChannelWhatsAppNative {
			continue
		}
		decoded, err := ch.GetDecoded()
		if err != nil {
			continue
		}
		wa, ok := decoded.(*config.WhatsAppSettings)
		if !ok || wa.GroupActivity == nil || len(wa.GroupActivity.Groups) == 0 {
			continue
		}
		dir := wa.SessionStorePath
		if dir == "" {
			dir = filepath.Join(cfg.WorkspacePath(), "whatsapp")
		}
		return wa.GroupActivity, filepath.Join(dir, DBName)
	}
	return nil, ""
}

// Retention is the configured retention in days, defaulting to 90.
func Retention(c *config.GroupActivityConfig) int {
	if c.RetentionDays > 0 {
		return c.RetentionDays
	}
	return DefaultRetention
}

// Counts reports whether chat is one of the counted groups.
func Counts(c *config.GroupActivityConfig, chat string) bool {
	return c != nil && slices.Contains(c.Groups, chat)
}
