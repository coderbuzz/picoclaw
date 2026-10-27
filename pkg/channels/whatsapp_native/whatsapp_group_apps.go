//go:build whatsapp_native

package whatsapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// group_apps.d: group messages whose normalized text matches an app's pattern go to that app's webhook (same
// signing as dm_apps.d) instead of the agent, from any group. The app's reply is posted back and may link or
// unlink the group, which is what lets the app post to it later (app_groups/<app>.json, checked by msg_hub).
const (
	groupAppsDir   = "group_apps.d"
	appGroupsDir   = "app_groups"
	groupReplyMax  = 1000 // runes
	groupInfoTTL   = 5 * time.Minute
	groupInfoLimit = 15 * time.Second
	groupReplyDay  = 20 // webhook replies per group per day
)

var appNameRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// normalizeGroupText trims, upper-cases and collapses whitespace so "sirkel  putuskan " matches "SIRKEL PUTUSKAN".
func normalizeGroupText(s string) string {
	return strings.ToUpper(strings.Join(strings.Fields(s), " "))
}

type groupAppPayload struct {
	GroupJID      string  `json:"group_jid"`
	GroupName     *string `json:"group_name"`
	SenderPN      *string `json:"sender_pn"`
	SenderLID     *string `json:"sender_lid"`
	SenderIsAdmin *bool   `json:"sender_is_admin"`
	Text          string  `json:"text"`
	MessageID     string  `json:"message_id"`
}

type groupAppReply struct {
	Reply  string `json:"reply"`
	Link   bool   `json:"link"`
	Unlink bool   `json:"unlink"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// senderAdmin is nil when info is missing or the sender is not among its participants.
func senderAdmin(info *types.GroupInfo, pn, lid string) *bool {
	if info == nil {
		return nil
	}
	for _, p := range info.Participants {
		if (pn != "" && (p.PhoneNumber.User == pn || p.JID.User == pn)) || (lid != "" && (p.LID.User == lid || p.JID.User == lid)) {
			admin := p.IsAdmin || p.IsSuperAdmin
			return &admin
		}
	}
	return nil
}

// dayCounter counts events per key in a rolling window; key "" is the bot-wide total.
type dayCounter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func (d *dayCounter) count(key string, now time.Time, span time.Duration) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := d.hits[key]
	for len(h) > 0 && h[0].Before(now.Add(-span)) {
		h = h[1:]
	}
	if len(h) == 0 {
		delete(d.hits, key)
	} else {
		d.hits[key] = h
	}
	return len(h)
}

func (d *dayCounter) add(key string, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.hits == nil {
		d.hits = map[string][]time.Time{}
	}
	d.hits[key] = append(d.hits[key], now)
}

// allowReply reserves one reply for the group: over the per-group daily cap or the bot-wide daily cap it is refused.
func (c *WhatsAppNativeChannel) allowReply(group string) bool {
	now := time.Now()
	if c.day.count(group, now, hubGlobalSpan) >= groupReplyDay || c.day.count("", now, hubGlobalSpan) >= hubGlobalMax {
		return false
	}
	c.day.add(group, now)
	c.day.add("", now)
	return true
}

// appGroups stores, per app, the groups it may post to: <dir>/<app>.json = {"groups":[...]}.
type appGroups struct {
	mu  sync.Mutex
	dir string
}

func (a *appGroups) path(app string) string { return filepath.Join(a.dir, app+".json") }

func (a *appGroups) read(app string) []string {
	var f struct {
		Groups []string `json:"groups"`
	}
	if b, err := os.ReadFile(a.path(app)); err == nil {
		_ = json.Unmarshal(b, &f) // a corrupt file reads as empty: fail closed
	}
	return f.Groups
}

func (a *appGroups) has(app, jid string) bool {
	if a == nil || !appNameRe.MatchString(app) {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Contains(a.read(app), jid)
}

// set adds or removes jid; the file is replaced atomically (0600) inside a 0700 folder.
func (a *appGroups) set(app, jid string, on bool) error {
	if !appNameRe.MatchString(app) {
		return os.ErrInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	groups := slices.DeleteFunc(a.read(app), func(g string) bool { return g == jid })
	if on {
		groups = append(groups, jid)
	}
	if groups == nil {
		groups = []string{}
	}
	b, _ := json.Marshal(map[string][]string{"groups": groups})
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(a.path(app), b, 0o600)
}

// groupInfoCache keeps GetGroupInfo results for groupInfoTTL; failures are not cached.
type groupInfoCache struct {
	mu sync.Mutex
	m  map[string]cachedGroupInfo
}

type cachedGroupInfo struct {
	info *types.GroupInfo
	exp  time.Time
}

func (c *WhatsAppNativeChannel) groupInfo(jid types.JID) *types.GroupInfo {
	key := jid.String()
	c.ginfo.mu.Lock()
	e, ok := c.ginfo.m[key]
	c.ginfo.mu.Unlock()
	if ok && time.Now().Before(e.exp) {
		return e.info
	}
	fetch := c.fetchGroupInfo
	if fetch == nil {
		fetch = func(ctx context.Context, j types.JID) (*types.GroupInfo, error) {
			c.mu.Lock()
			client := c.client
			c.mu.Unlock()
			if client == nil {
				return nil, os.ErrNotExist
			}
			return client.GetGroupInfo(ctx, j)
		}
	}
	ctx, cancel := context.WithTimeout(c.runCtx, groupInfoLimit)
	defer cancel()
	info, err := fetch(ctx, jid)
	if err != nil || info == nil {
		return nil
	}
	c.ginfo.mu.Lock()
	if c.ginfo.m == nil || len(c.ginfo.m) > 256 {
		c.ginfo.m = map[string]cachedGroupInfo{}
	}
	c.ginfo.m[key] = cachedGroupInfo{info, time.Now().Add(groupInfoTTL)}
	c.ginfo.mu.Unlock()
	return info
}

// tryGroupApp hands a plain-text group message to the matching app; true means it was consumed (never reaches the agent).
func (c *WhatsAppNativeChannel) tryGroupApp(evt *events.Message) bool {
	if c.groupReg == nil || inboundMediaOf(evt.Message) != nil {
		return false
	}
	text := normalizeGroupText(utils.SanitizeMessageContent(messageText(evt.Message)))
	h := matchDMWebhook(c.groupReg.current(), text)
	if h == nil {
		return false
	}
	chat := evt.Info.Chat.ToNonAD()
	if !c.groupLimit.allow(chat.String()) {
		logger.WarnCF("whatsapp", "group app rate limited, dropped", map[string]any{"app": h.name})
		return true
	}
	pn, lid := dmSenderPNLID(evt.Info.Sender, evt.Info.SenderAlt, c.counterpartJID)
	c.goTracked(func() { c.callGroupApp(h, chat, pn, lid, evt.Info.ID, text) })
	return true
}

func (c *WhatsAppNativeChannel) callGroupApp(h *dmWebhook, chat types.JID, pn, lid, msgID, text string) {
	info := c.groupInfo(chat)
	p := groupAppPayload{
		GroupJID: chat.String(), SenderPN: strPtr(pn), SenderLID: strPtr(lid),
		SenderIsAdmin: senderAdmin(info, pn, lid), Text: text, MessageID: msgID,
	}
	if info != nil {
		p.GroupName = strPtr(info.Name)
	}
	start := time.Now()
	var out groupAppReply
	status, err := h.post(c.runCtx, c.dmClient, p, &out)
	fields := map[string]any{"app": h.name, "status": status, "latency_ms": time.Since(start).Milliseconds()}
	if err != nil {
		logger.WarnCF("whatsapp", "group app failed", fields)
		return
	}
	logger.InfoCF("whatsapp", "group app handled", fields)
	if out.Link != out.Unlink { // both set is contradictory: change nothing
		if err := c.appGroups.set(h.name, p.GroupJID, out.Link); err != nil {
			logger.WarnCF("whatsapp", "app_groups write failed", map[string]any{"app": h.name, "error": err.Error()})
			return // the reply would claim a change that did not happen
		}
	}
	reply := strings.TrimSpace(out.Reply)
	if r := []rune(reply); len(r) > groupReplyMax {
		reply = string(r[:groupReplyMax])
	}
	if reply == "" {
		return
	}
	if !c.allowReply(p.GroupJID) {
		logger.WarnCF("whatsapp", "group app reply over daily cap, not sent", map[string]any{"app": h.name})
		return
	}
	if err := c.sendTo(chat.String(), reply); err != nil {
		logger.WarnCF("whatsapp", "group app reply not sent", map[string]any{"error": err.Error()})
	}
}

// sendTo posts text to chatID (sendText is overridden in tests).
func (c *WhatsAppNativeChannel) sendTo(chatID, text string) error {
	if c.sendText != nil {
		return c.sendText(c.runCtx, chatID, text)
	}
	_, err := c.Send(c.runCtx, bus.OutboundMessage{Channel: "whatsapp", ChatID: chatID, Content: text})
	return err
}

// goTracked runs f in a goroutine the channel waits for on Stop; false means the channel is stopping.
func (c *WhatsAppNativeChannel) goTracked(f func()) bool {
	c.reconnectMu.Lock()
	if c.stopping.Load() {
		c.reconnectMu.Unlock()
		return false
	}
	c.wg.Add(1)
	c.reconnectMu.Unlock()
	go func() { defer c.wg.Done(); f() }()
	return true
}
