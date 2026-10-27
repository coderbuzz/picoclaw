//go:build whatsapp_native

package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// msg_hub: a WebSocket hub pushes outbound notifications; one queue sends them to known contacts, paced like a person.
const (
	hubContactsFile = "dm_contacts.json"
	hubSentFile     = "msg_hub_sent.json"
	hubContactTTL   = 30 * 24 * time.Hour
	hubContactSkip  = time.Hour // a contact seen this recently is not rewritten
	hubFlushDelay   = 30 * time.Second
	hubQueueSize    = 500
	hubSentKeep     = 2000
	hubPerRcptMax   = 5
	hubPerRcptSpan  = time.Hour
	hubGlobalMax    = 200
	hubGlobalSpan   = 24 * time.Hour
	hubPaceMin      = 2 * time.Second
	hubPaceMax      = 6 * time.Second
	hubTypeMin      = time.Second
	hubTypeMax      = 3 * time.Second
	hubConnectPoll  = 2 * time.Second
	hubPingEvery    = 30 * time.Second
	hubBackoffMin   = time.Second
	hubBackoffMax   = time.Minute
	hubGoodSession  = time.Minute    // a session this long resets the reconnect backoff
	hubMaxAge       = 24 * time.Hour // the hub expires older messages; never send them late
	hubGroupMax     = 10             // posts per linked group per hubGroupSpan
	hubGroupSpan    = 24 * time.Hour
)

var (
	hubToRe    = regexp.MustCompile(`^[0-9]{8,15}$`)
	hubGroupRe = regexp.MustCompile(`^[0-9]{5,30}(-[0-9]{1,15})?@g\.us$`)

	// errNotParticipant is what Send returns when the bot is no longer in the target group.
	errNotParticipant = errors.New("not_participant")
)

// dmContacts remembers when each phone number last sent the bot a DM (number -> unix seconds).
type dmContacts struct {
	mu    sync.Mutex
	path  string
	now   func() time.Time
	seen  map[string]int64
	dirty bool
	timer *time.Timer
}

func loadDMContacts(path string, now func() time.Time) *dmContacts {
	c := &dmContacts{path: path, now: now, seen: map[string]int64{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c.seen) // a corrupt file just starts empty
	}
	if c.seen == nil {
		c.seen = map[string]int64{}
	}
	c.prune()
	return c
}

func (c *dmContacts) prune() {
	cut := c.now().Add(-hubContactTTL).Unix()
	for k, t := range c.seen {
		if t < cut {
			delete(c.seen, k)
			c.dirty = true
		}
	}
}

// record notes a DM from pn; the write is debounced. Safe on a nil receiver (feature off).
func (c *dmContacts) record(pn string) {
	if c == nil || pn == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.now().Unix()
	if old, ok := c.seen[pn]; ok && t-old < int64(hubContactSkip/time.Second) {
		return
	}
	c.seen[pn] = t
	c.dirty = true
	if c.timer == nil {
		c.timer = time.AfterFunc(hubFlushDelay, c.flush)
	}
}

func (c *dmContacts) has(pn string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.seen[pn]
	return ok && c.now().Unix()-t <= int64(hubContactTTL/time.Second)
}

// flush prunes and writes the file if anything changed.
func (c *dmContacts) flush() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.prune()
	if !c.dirty {
		return
	}
	b, _ := json.Marshal(c.seen)
	if err := fileutil.WriteFileAtomic(c.path, b, 0o600); err != nil {
		logger.WarnCF("whatsapp", "dm contacts write failed", map[string]any{"error": err.Error()})
		return
	}
	c.dirty = false
}

// hubAck is the final-status frame sent to the hub; it is also what the sent store keeps.
type hubAck struct {
	Type   string `json:"type,omitempty"`
	App    string `json:"app"`
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// hubSent keeps the last hubSentKeep decided (app,id) so a redelivery is answered, not sent twice.
type hubSent struct {
	mu   sync.Mutex
	path string
	list []hubAck
	idx  map[string]hubAck
}

func hubKey(app, id string) string { return app + "\x00" + id }

func loadHubSent(path string) *hubSent {
	s := &hubSent{path: path, idx: map[string]hubAck{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.list)
	}
	if len(s.list) > hubSentKeep {
		s.list = s.list[len(s.list)-hubSentKeep:]
	}
	for _, a := range s.list {
		s.idx[hubKey(a.App, a.ID)] = a
	}
	return s
}

func (s *hubSent) get(app, id string) (hubAck, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.idx[hubKey(app, id)]
	return a, ok
}

func (s *hubSent) add(a hubAck) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.Type = ""
	k := hubKey(a.App, a.ID)
	if _, dup := s.idx[k]; dup {
		for i := range s.list { // replace the pre-send marker with the final status
			if hubKey(s.list[i].App, s.list[i].ID) == k {
				s.list[i] = a
			}
		}
	} else {
		s.list = append(s.list, a)
	}
	s.idx[k] = a
	if len(s.list) > hubSentKeep {
		delete(s.idx, hubKey(s.list[0].App, s.list[0].ID))
		s.list = s.list[1:]
	}
	b, _ := json.Marshal(s.list)
	if err := fileutil.WriteFileAtomic(s.path, b, 0o600); err != nil {
		logger.WarnCF("whatsapp", "msg_hub sent store write failed", map[string]any{"error": err.Error()})
	}
}

type hubMsg struct {
	App, ID, To, Text string
	CreatedAt         int64 // unix seconds, set by the hub
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// hubQueue is the single sender: it decides (dedupe, contact, limits), waits for WA, paces, sends, acks.
type hubQueue struct {
	contacts  *dmContacts
	linked    func(app, group string) bool // nil = no group is linked
	day       *dayCounter                  // bot-wide daily sends, shared with group app replies
	sent      *hubSent
	ack       func(hubAck)
	connected func() bool
	typing    func(ctx context.Context, to string, d time.Duration)
	send      func(ctx context.Context, to, text string) error
	now       func() time.Time
	sleep     func(ctx context.Context, d time.Duration) error
	rnd       func(lo, hi time.Duration) time.Duration

	ch      chan hubMsg
	mu      sync.Mutex
	pending map[string]struct{} // queued but not yet decided: a hub redelivery must not queue twice
	// below: sender goroutine only
	last   time.Time
	byRcpt map[string][]time.Time
}

func newHubQueue() *hubQueue {
	return &hubQueue{
		now:     time.Now,
		sleep:   sleepCtx,
		rnd:     func(lo, hi time.Duration) time.Duration { return lo + rand.N(hi-lo+1) },
		ch:      make(chan hubMsg, hubQueueSize),
		pending: map[string]struct{}{},
		byRcpt:  map[string][]time.Time{},
		day:     &dayCounter{},
	}
}

// enqueue returns false when the queue is full; the caller then leaves the message un-acked so the hub redelivers.
func (q *hubQueue) enqueue(m hubMsg) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	k := hubKey(m.App, m.ID)
	if _, dup := q.pending[k]; dup {
		return true
	}
	select {
	case q.ch <- m:
		q.pending[k] = struct{}{}
		return true
	default:
		return false
	}
}

func (q *hubQueue) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-q.ch:
			q.handle(ctx, m)
			q.mu.Lock()
			delete(q.pending, hubKey(m.App, m.ID))
			q.mu.Unlock()
		}
	}
}

// finish records the final status and tells the hub (the ack is best-effort: a redelivery re-acks from the store).
func (q *hubQueue) finish(m hubMsg, status, errStr string) {
	a := hubAck{App: m.App, ID: m.ID, Status: status, Error: errStr}
	q.sent.add(a)
	q.ack(a)
	logger.InfoCF("whatsapp", "msg_hub message done", map[string]any{"app": m.App, "status": status})
}

// limited reports whether sending to `to` now would break the per-recipient or global cap.
func (q *hubQueue) limited(to string) bool {
	cut := q.now().Add(-hubGlobalSpan)
	max, span := hubPerRcptMax, hubPerRcptSpan
	if hubGroupRe.MatchString(to) {
		max, span = hubGroupMax, hubGroupSpan
	}
	cut = q.now().Add(-span)
	r := q.byRcpt[to]
	for len(r) > 0 && r[0].Before(cut) {
		r = r[1:]
	}
	if len(r) == 0 {
		delete(q.byRcpt, to)
	} else {
		q.byRcpt[to] = r
	}
	return len(r) >= max || q.day.count("", q.now(), hubGlobalSpan) >= hubGlobalMax
}

func (q *hubQueue) handle(ctx context.Context, m hubMsg) {
	if a, ok := q.sent.get(m.App, m.ID); ok {
		q.ack(a)
		return
	}
	if hubGroupRe.MatchString(m.To) {
		if q.linked == nil || !q.linked(m.App, m.To) {
			q.finish(m, "rejected_unlinked_group", "")
			return
		}
	} else if !q.contacts.has(m.To) {
		q.finish(m, "rejected_unknown_contact", "")
		return
	}
	if q.limited(m.To) {
		q.finish(m, "rate_limited", "")
		return
	}
	for !q.connected() {
		if q.sleep(ctx, hubConnectPoll) != nil {
			return
		}
	}
	if !q.last.IsZero() {
		if d := q.last.Add(q.rnd(hubPaceMin, hubPaceMax)).Sub(q.now()); d > 0 && q.sleep(ctx, d) != nil {
			return
		}
	}
	if m.CreatedAt > 0 && q.now().Unix()-m.CreatedAt > int64(hubMaxAge/time.Second) {
		logger.InfoCF("whatsapp", "msg_hub message too old, dropped", map[string]any{"app": m.App})
		return // the hub has marked it expired; an ack would be ignored
	}
	q.typing(ctx, m.To, q.rnd(hubTypeMin, hubTypeMax))
	if ctx.Err() != nil {
		return
	}
	// Persist before sending: a crash between send and ack must not send twice on redelivery.
	q.sent.add(hubAck{App: m.App, ID: m.ID, Status: "failed", Error: "unconfirmed: restarted while sending"})
	now := q.now()
	q.day.add("", now)
	q.byRcpt[m.To] = append(q.byRcpt[m.To], now)
	err := q.send(ctx, m.To, m.Text)
	q.last = q.now()
	if ctx.Err() != nil {
		return // shutting down: leave un-acked, the hub redelivers
	}
	if err != nil {
		e := strings.ReplaceAll(err.Error(), m.To, "")
		if errors.Is(err, errNotParticipant) {
			e = errNotParticipant.Error()
		}
		q.finish(m, "failed", e[:min(len(e), 100)])
		return
	}
	q.finish(m, "sent", "")
}

// hubClient keeps one WebSocket subscription to the hub alive and feeds the queue.
type hubClient struct {
	url, token string
	q          *hubQueue
	pingEvery  time.Duration
	minBackoff time.Duration
	maxBackoff time.Duration

	wmu  sync.Mutex // gorilla allows a single concurrent writer
	conn *websocket.Conn
}

func newHubClient(url, token string, q *hubQueue) *hubClient {
	return &hubClient{
		url: strings.TrimSuffix(url, "/") + "/subscribe?topic=whatsapp", token: token, q: q,
		pingEvery: hubPingEvery, minBackoff: hubBackoffMin, maxBackoff: hubBackoffMax,
	}
}

func (h *hubClient) write(mt int, data []byte) error {
	h.wmu.Lock()
	defer h.wmu.Unlock()
	if h.conn == nil {
		return websocket.ErrCloseSent
	}
	_ = h.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return h.conn.WriteMessage(mt, data)
}

func (h *hubClient) sendAck(a hubAck) {
	a.Type = "ack"
	b, _ := json.Marshal(a)
	if err := h.write(websocket.TextMessage, b); err != nil {
		logger.WarnCF("whatsapp", "msg_hub ack not sent, hub will redeliver", map[string]any{"app": a.App})
	}
}

func (h *hubClient) run(ctx context.Context) {
	backoff := h.minBackoff
	for ctx.Err() == nil {
		start := time.Now()
		err := h.session(ctx)
		if ctx.Err() != nil {
			return
		}
		logger.WarnCF("whatsapp", "msg_hub disconnected", map[string]any{"error": err.Error()})
		if time.Since(start) >= hubGoodSession {
			backoff = h.minBackoff
		}
		wait := backoff/2 + rand.N(backoff/2+1) // jitter so restarts do not align
		if h.q.sleep(ctx, wait) != nil {
			return
		}
		backoff = min(backoff*2, h.maxBackoff)
	}
}

// session dials, reads frames until the connection ends, and always returns the reason.
func (h *hubClient) session(ctx context.Context) error {
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	conn, _, err := d.DialContext(ctx, h.url, http.Header{"Authorization": {"Bearer " + h.token}})
	if err != nil {
		return err
	}
	conn.SetReadLimit(64 << 10)
	h.wmu.Lock()
	h.conn = conn
	h.wmu.Unlock()
	logger.InfoC("whatsapp", "msg_hub connected")

	sctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(h.pingEvery)
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				_ = h.write(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				_ = conn.Close()
				return
			case <-t.C:
				if h.write(websocket.TextMessage, []byte("ping")) != nil {
					_ = conn.Close()
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-done
		h.wmu.Lock()
		h.conn = nil
		h.wmu.Unlock()
	}()

	for {
		// the hub answers each ping with pong, so silence for 3 pings means a dead link
		_ = conn.SetReadDeadline(time.Now().Add(3 * h.pingEvery))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if mt == websocket.TextMessage {
			h.receive(data)
		}
	}
}

func (h *hubClient) receive(data []byte) {
	var raw struct {
		Type, App, ID, To, Text string
		CreatedAt               int64 `json:"created_at"`
	}
	if json.Unmarshal(data, &raw) != nil || raw.Type != "msg" || raw.App == "" || raw.ID == "" {
		return // pong and unknown frames
	}
	m := hubMsg{App: raw.App, ID: raw.ID, To: raw.To, Text: raw.Text, CreatedAt: raw.CreatedAt}
	if !(hubToRe.MatchString(m.To) || hubGroupRe.MatchString(m.To)) || m.Text == "" {
		h.q.finish(m, "failed", "invalid message")
		return
	}
	if !h.q.enqueue(m) {
		logger.WarnCF("whatsapp", "msg_hub queue full, left un-acked", map[string]any{"app": m.App})
	}
}

// --- channel wiring ---

// startMsgHub runs the sender and the subscriber as tracked goroutines; false means the channel is stopping.
func (c *WhatsAppNativeChannel) startMsgHub() bool {
	if c.config.MsgHub == nil {
		return true
	}
	q := newHubQueue()
	q.contacts = c.contacts
	q.linked = c.appGroups.has
	q.day = &c.day
	q.sent = loadHubSent(filepath.Join(c.storePath, hubSentFile))
	q.connected = c.waReady
	q.typing = c.hubTyping
	q.send = func(ctx context.Context, to, text string) error {
		_, err := c.Send(ctx, bus.OutboundMessage{Channel: "whatsapp", ChatID: hubChatID(to), Content: text})
		return err
	}
	h := newHubClient(c.config.MsgHub.URL, c.hubToken, q)
	q.ack = h.sendAck
	c.reconnectMu.Lock()
	if c.stopping.Load() {
		c.reconnectMu.Unlock()
		return false
	}
	c.wg.Add(2)
	c.reconnectMu.Unlock()
	go func() { defer c.wg.Done(); q.run(c.runCtx) }()
	go func() { defer c.wg.Done(); h.run(c.runCtx) }()
	return true
}

// waReady is true once the channel is started, logged in and connected.
func (c *WhatsAppNativeChannel) waReady() bool {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	return c.IsRunning() && client != nil && client.IsConnected() && client.Store.ID != nil
}

// hubTyping shows "typing..." for d before a hub message.
func (c *WhatsAppNativeChannel) hubTyping(ctx context.Context, to string, d time.Duration) {
	stop, err := c.StartTyping(ctx, hubChatID(to))
	if err != nil {
		return
	}
	defer stop()
	_ = sleepCtx(ctx, d)
}

// hubChatID maps a hub recipient (number or group JID) to a chat ID.
func hubChatID(to string) string {
	if hubGroupRe.MatchString(to) {
		return to
	}
	return to + "@s.whatsapp.net"
}
