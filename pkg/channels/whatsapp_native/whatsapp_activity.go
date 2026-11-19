//go:build whatsapp_native

package whatsapp

import (
	"path/filepath"
	"time"

	"go.mau.fi/whatsmeow/types/events"

	"github.com/sipeed/picoclaw/pkg/groupactivity"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// startGroupActivity opens the counter store when group_activity is configured. A failure only disables counting.
func (c *WhatsAppNativeChannel) startGroupActivity() {
	ga := c.config.GroupActivity
	if ga == nil || len(ga.Groups) == 0 {
		return
	}
	s, err := groupactivity.Open(filepath.Join(c.storePath, groupactivity.DBName), groupactivity.Retention(ga))
	if err != nil {
		logger.ErrorCF("whatsapp", "group_activity disabled: cannot open store", map[string]any{"error": err.Error()})
		return
	}
	c.reconnectMu.Lock()
	if c.stopping.Load() {
		c.reconnectMu.Unlock()
		_ = s.Close()
		return
	}
	c.wg.Add(1)
	c.reconnectMu.Unlock()
	c.activity = s
	go func() { defer c.wg.Done(); s.Run(c.runCtx) }()
}

// countGroupMessage adds one to the sender's tally for a counted group. Only messages with content reach it
// (reactions, edits, revokes and protocol messages carry none), and never the bot's own.
func (c *WhatsAppNativeChannel) countGroupMessage(evt *events.Message) {
	if c.activity == nil || !groupactivity.Counts(c.config.GroupActivity, evt.Info.Chat.String()) {
		return
	}
	sender := evt.Info.Sender.ToNonAD().String()
	if err := c.activity.Record(evt.Info.Chat.String(), sender, evt.Info.PushName, time.Now()); err != nil {
		logger.WarnCF("whatsapp", "group_activity record failed", map[string]any{"error": err.Error()})
	}
}
