//go:build whatsapp_native

package whatsapp

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	// WhatsApp drops a composing state after ~25s, so refresh well before that.
	typingRefresh     = 10 * time.Second
	maxTypingDuration = 5 * time.Minute
)

// sendAvailable shows the bot as online; it also makes the server deliver our read receipts.
func (c *WhatsAppNativeChannel) sendAvailable() {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil || client.Store.ID == nil {
		return
	}
	if err := client.SendPresence(c.runCtx, types.PresenceAvailable); err != nil {
		logger.DebugCF("whatsapp", "SendPresence failed", map[string]any{"error": err.Error()})
	}
}

// markRead sends a read receipt (blue ticks) for a message the bot will handle.
func (c *WhatsAppNativeChannel) markRead(evt *events.Message) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil || c.config.NoReadReceipts {
		return
	}
	err := client.MarkRead(c.runCtx, []types.MessageID{evt.Info.ID}, evt.Info.Timestamp, evt.Info.Chat, evt.Info.Sender)
	if err != nil {
		logger.DebugCF("whatsapp", "MarkRead failed", map[string]any{"error": err.Error()})
	}
}

// StartTyping implements channels.TypingCapable with WhatsApp's "typing..." chat state.
func (c *WhatsAppNativeChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil || !client.IsConnected() {
		return func() {}, nil
	}
	jid, err := parseJID(chatID)
	if err != nil {
		return func() {}, err
	}
	compose := func(state types.ChatPresence) {
		_ = client.SendChatPresence(context.WithoutCancel(ctx), jid, state, types.ChatPresenceMediaText)
	}
	compose(types.ChatPresenceComposing)

	typingCtx, cancel := context.WithTimeout(ctx, maxTypingDuration)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(typingRefresh)
		defer ticker.Stop()
		for {
			select {
			case <-typingCtx.Done():
				compose(types.ChatPresencePaused)
				return
			case <-ticker.C:
				compose(types.ChatPresenceComposing)
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}
