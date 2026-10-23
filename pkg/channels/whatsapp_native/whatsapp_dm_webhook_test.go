//go:build whatsapp_native

package whatsapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

var (
	dmPN  = types.NewJID("628000000001", types.DefaultUserServer)
	dmLID = types.NewJID("100000000000002", types.HiddenUserServer)
)

func TestMatchDMWebhook(t *testing.T) {
	hooks := []dmWebhook{{re: regexp.MustCompile(`^(?:MASUK [A-Z0-9]{8})$`)}}
	for text, want := range map[string]bool{
		"MASUK ABCD1234":       true,
		"MASUK ABCD1234 extra": false,
		"halo MASUK ABCD1234":  false,
		"masuk abcd1234":       false,
		"":                     false,
	} {
		if got := matchDMWebhook(hooks, text) != nil; got != want {
			t.Errorf("%q: got %v want %v", text, got, want)
		}
	}
}

func TestSignDMWebhook(t *testing.T) {
	// Vector from: printf '1700000000.{}' | openssl dgst -sha256 -hmac key
	got := signDMWebhook([]byte("key"), 1700000000, []byte("{}"))
	const want = "sha256=9d713ed406bb7076d4123f0dc2c39d2df5c654ed4b0cd56b52c8b4c940bd63ae"
	if got != want {
		t.Fatalf("signature %q", got)
	}
	if signDMWebhook([]byte("key"), 1700000001, []byte("{}")) == got || signDMWebhook([]byte("k2"), 1700000000, []byte("{}")) == got {
		t.Fatal("signature must depend on timestamp and secret")
	}
}

func TestDMSenderPNLID(t *testing.T) {
	lidOf := func(types.JID) types.JID { return dmPN }
	tests := []struct {
		name         string
		sender, alt  types.JID
		counterpart  func(types.JID) types.JID
		wantPN, want string
	}{
		{"pn sender", types.JID{User: "628000000001", Device: 12, Server: types.DefaultUserServer}, types.EmptyJID, nil, "628000000001", ""},
		{"lid with alt pn", dmLID, dmPN, nil, "628000000001", "100000000000002"},
		{"lid resolved via store", dmLID, types.EmptyJID, lidOf, "628000000001", "100000000000002"},
		{"lid unresolved", dmLID, types.EmptyJID, func(types.JID) types.JID { return types.EmptyJID }, "", "100000000000002"},
	}
	for _, tt := range tests {
		pn, lid := dmSenderPNLID(tt.sender, tt.alt, tt.counterpart)
		if pn != tt.wantPN || lid != tt.want {
			t.Errorf("%s: pn=%q lid=%q", tt.name, pn, lid)
		}
	}
}

type dmHarness struct {
	ch   *WhatsAppNativeChannel
	bus  *bus.MessageBus
	sent chan string
}

func newDMHarness(t *testing.T, url string, handler http.Handler) *dmHarness {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	h := &dmHarness{bus: bus.NewMessageBus(), sent: make(chan string, 4)}
	h.ch = &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, h.bus, nil),
		config:      &config.WhatsAppSettings{},
		runCtx:      context.Background(),
		dmHooks: []dmWebhook{{
			re: regexp.MustCompile(`^(?:MASUK [A-Z0-9]{8})$`), name: "test", url: srv.URL,
			secret: []byte("s3cret"), errorReply: "oops",
		}},
		dmClient: srv.Client(),
		sendText: func(_ context.Context, chatID, text string) error {
			h.sent <- chatID + "|" + text
			return nil
		},
	}
	return h
}

func dmEvent(chat types.JID, text string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Sender: dmLID, SenderAlt: dmPN, Chat: chat},
			ID:            "mid1", Timestamp: time.Unix(1700000000, 0),
		},
		Message: &waE2E.Message{Conversation: proto.String(text)},
	}
}

func (h *dmHarness) wantSent(t *testing.T) string {
	t.Helper()
	select {
	case s := <-h.sent:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("no reply sent")
		return ""
	}
}

func (h *dmHarness) wantForwarded(t *testing.T, forwarded bool) {
	t.Helper()
	select {
	case <-h.bus.InboundChan():
		if !forwarded {
			t.Fatal("message must not reach the bus")
		}
	case <-time.After(300 * time.Millisecond):
		if forwarded {
			t.Fatal("message should reach the bus")
		}
	}
}

func TestDMWebhook_PayloadSignatureAndReply(t *testing.T) {
	var mu sync.Mutex
	var gotBody []byte
	var gotHdr http.Header
	h := newDMHarness(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotBody, _ = io.ReadAll(r.Body)
		gotHdr = r.Header.Clone()
		mu.Unlock()
		_, _ = w.Write([]byte(`{"reply":"selamat datang"}`))
	}))
	h.ch.handleIncoming(dmEvent(dmLID, "MASUK ABCD1234"))
	if got := h.wantSent(t); got != dmLID.String()+"|selamat datang" {
		t.Fatalf("sent %q", got)
	}
	h.wantForwarded(t, false)

	mu.Lock()
	defer mu.Unlock()
	var p dmWebhookPayload
	if err := json.Unmarshal(gotBody, &p); err != nil {
		t.Fatal(err)
	}
	want := dmWebhookPayload{"mid1", "MASUK ABCD1234", "628000000001", "100000000000002", dmLID.String(), 1700000000}
	if p != want {
		t.Fatalf("payload %+v", p)
	}
	ts, _ := strconv.ParseInt(gotHdr.Get("X-Picoclaw-Timestamp"), 10, 64)
	if sig := signDMWebhook([]byte("s3cret"), ts, gotBody); gotHdr.Get("X-Picoclaw-Signature") != sig {
		t.Fatalf("bad signature %q", gotHdr.Get("X-Picoclaw-Signature"))
	}
	if gotHdr.Get("Content-Type") != "application/json" {
		t.Fatal("content type")
	}
}

func TestDMWebhook_ErrorReply(t *testing.T) {
	h := newDMHarness(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	h.ch.handleIncoming(dmEvent(dmLID, "MASUK ABCD1234"))
	if got := h.wantSent(t); got != dmLID.String()+"|oops" {
		t.Fatalf("sent %q", got)
	}
}

func TestDMWebhook_TimeoutSendsErrorReply(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	h := newDMHarness(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	h.ch.dmClient = &http.Client{Timeout: 100 * time.Millisecond}
	h.ch.handleIncoming(dmEvent(dmLID, "MASUK ABCD1234"))
	if got := h.wantSent(t); got != dmLID.String()+"|oops" {
		t.Fatalf("sent %q", got)
	}
}

func TestDMWebhook_NoMatchAndGroupPassThrough(t *testing.T) {
	called := make(chan struct{}, 1)
	h := newDMHarness(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called <- struct{}{} }))
	h.ch.handleIncoming(dmEvent(dmLID, "halo"))
	h.wantForwarded(t, true)

	group := types.NewJID("1203630000000000", types.GroupServer)
	h.ch.handleIncoming(dmEvent(group, "MASUK ABCD1234"))
	select {
	case <-called:
		t.Fatal("group message must not call the webhook")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestDMWebhook_DuplicateMessageIDHandledOnce(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	h := newDMHarness(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"reply":"ok"}`))
	}))
	h.ch.handleIncoming(dmEvent(dmLID, "MASUK ABCD1234"))
	h.ch.handleIncoming(dmEvent(dmLID, "MASUK ABCD1234")) // same ID redelivered after a decrypt retry
	h.wantSent(t)
	select {
	case s := <-h.sent:
		t.Fatalf("second reply %q", s)
	case <-time.After(300 * time.Millisecond):
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("webhook called %d times", calls)
	}
}

func TestSeenIDsExpire(t *testing.T) {
	var s seenIDs
	t0 := time.Unix(1700000000, 0)
	if !s.first("a", t0) || s.first("a", t0.Add(time.Minute)) || !s.first("a", t0.Add(seenTTL)) {
		t.Fatal("seenIDs")
	}
}
