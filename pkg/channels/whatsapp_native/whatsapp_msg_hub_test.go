//go:build whatsapp_native

package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time { return f.t }
func (f *fakeClock) sleep(_ context.Context, d time.Duration) error {
	f.t = f.t.Add(d)
	return nil
}

type hubRig struct {
	q     *hubQueue
	clk   *fakeClock
	acks  []hubAck
	sends []string
	times []time.Time
	up    func() bool
}

func newHubRig(t *testing.T, numbers ...string) *hubRig {
	t.Helper()
	r := &hubRig{clk: &fakeClock{t: time.Unix(1_800_000_000, 0)}, up: func() bool { return true }}
	dir := t.TempDir()
	q := newHubQueue()
	q.now, q.sleep = r.clk.now, r.clk.sleep
	q.contacts = loadDMContacts(filepath.Join(dir, hubContactsFile), r.clk.now)
	for _, n := range numbers {
		q.contacts.record(n)
	}
	q.sent = loadHubSent(filepath.Join(dir, hubSentFile))
	q.ack = func(a hubAck) { r.acks = append(r.acks, a) }
	q.connected = func() bool { return r.up() }
	q.typing = func(context.Context, string, time.Duration) {}
	q.send = func(_ context.Context, to, text string) error {
		r.sends = append(r.sends, to+":"+text)
		r.times = append(r.times, r.clk.now())
		return nil
	}
	r.q = q
	return r
}

func (r *hubRig) handle(id, to string) {
	r.q.handle(context.Background(), hubMsg{App: "app", ID: id, To: to, Text: "hello"})
}

func (r *hubRig) lastStatus() string { return r.acks[len(r.acks)-1].Status }

func TestDMContactsRecordPrunePersist(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	path := filepath.Join(t.TempDir(), hubContactsFile)
	c := loadDMContacts(path, clk.now)
	c.record("628111111111")
	c.record("") // ignored
	c.flush()
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stat: %v %v", st, err)
	}
	if !loadDMContacts(path, clk.now).has("628111111111") {
		t.Fatal("contact not persisted")
	}
	clk.t = clk.t.Add(hubContactTTL + time.Hour)
	if c.has("628111111111") {
		t.Fatal("expired contact still known")
	}
	c2 := loadDMContacts(path, clk.now) // load prunes
	c2.flush()
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "6281") {
		t.Fatalf("expired entry kept on disk: %s", b)
	}
	(*dmContacts)(nil).record("1") // feature off must not panic
}

func TestHubQueueDecisions(t *testing.T) {
	t.Run("unknown contact", func(t *testing.T) {
		r := newHubRig(t)
		r.handle("1", "628222222222")
		if r.lastStatus() != "rejected_unknown_contact" || len(r.sends) != 0 {
			t.Fatalf("acks=%v sends=%v", r.acks, r.sends)
		}
	})
	t.Run("dedupe re-acks without sending", func(t *testing.T) {
		r := newHubRig(t, "628111111111")
		r.handle("1", "628111111111")
		r.handle("1", "628111111111")
		if len(r.sends) != 1 || len(r.acks) != 2 || r.acks[1] != r.acks[0] || r.lastStatus() != "sent" {
			t.Fatalf("acks=%v sends=%v", r.acks, r.sends)
		}
	})
	t.Run("per recipient limit", func(t *testing.T) {
		r := newHubRig(t, "628111111111")
		for i := range hubPerRcptMax + 1 {
			r.handle(fmt.Sprint(i), "628111111111")
		}
		if len(r.sends) != hubPerRcptMax || r.lastStatus() != "rate_limited" {
			t.Fatalf("sends=%d last=%s", len(r.sends), r.lastStatus())
		}
		r.clk.t = r.clk.t.Add(hubPerRcptSpan + time.Minute)
		r.handle("later", "628111111111")
		if r.lastStatus() != "sent" {
			t.Fatalf("window did not reopen: %s", r.lastStatus())
		}
	})
	t.Run("global limit", func(t *testing.T) {
		r := newHubRig(t)
		for i := range hubGlobalMax + 1 {
			n := fmt.Sprintf("62800%08d", i)
			r.q.contacts.record(n)
			r.handle(fmt.Sprint(i), n)
		}
		if len(r.sends) != hubGlobalMax || r.lastStatus() != "rate_limited" {
			t.Fatalf("sends=%d last=%s", len(r.sends), r.lastStatus())
		}
	})
	t.Run("waits while disconnected, no ack", func(t *testing.T) {
		r := newHubRig(t, "628111111111")
		calls := 0
		r.up = func() bool { calls++; return calls > 3 }
		start := r.clk.now()
		r.handle("1", "628111111111")
		if r.clk.now().Sub(start) < 3*hubConnectPoll || len(r.acks) != 1 || r.lastStatus() != "sent" {
			t.Fatalf("waited %v acks=%v", r.clk.now().Sub(start), r.acks)
		}
	})
	t.Run("pacing between messages", func(t *testing.T) {
		r := newHubRig(t, "628111111111", "628222222222")
		r.handle("1", "628111111111")
		r.handle("2", "628222222222")
		if gap := r.times[1].Sub(r.times[0]); gap < hubPaceMin || gap > hubPaceMax {
			t.Fatalf("gap %v outside 2-6s", gap)
		}
	})
	t.Run("marker persisted before send", func(t *testing.T) {
		r := newHubRig(t, "628111111111")
		var during hubAck
		r.q.send = func(context.Context, string, string) error {
			during, _ = loadHubSent(r.q.sent.path).get("app", "1") // what a crash right now would leave
			return nil
		}
		r.handle("1", "628111111111")
		final, _ := loadHubSent(r.q.sent.path).get("app", "1")
		if during.Status != "failed" || final.Status != "sent" || len(r.q.sent.list) != 1 {
			t.Fatalf("during=%v final=%v list=%v", during, final, r.q.sent.list)
		}
	})
	t.Run("too old is dropped without ack", func(t *testing.T) {
		r := newHubRig(t, "628111111111")
		old := r.clk.now().Add(-hubMaxAge - time.Minute).Unix()
		r.q.handle(context.Background(), hubMsg{App: "app", ID: "1", To: "628111111111", Text: "hi", CreatedAt: old})
		if len(r.sends) != 0 || len(r.acks) != 0 {
			t.Fatalf("acks=%v sends=%v", r.acks, r.sends)
		}
	})
	t.Run("send failure acks failed without number", func(t *testing.T) {
		r := newHubRig(t, "628111111111")
		r.q.send = func(_ context.Context, to, _ string) error { return fmt.Errorf("boom %s", to) }
		r.handle("1", "628111111111")
		if a := r.acks[0]; a.Status != "failed" || strings.Contains(a.Error, "628111111111") {
			t.Fatalf("ack=%v", a)
		}
	})
}

func TestHubQueueEnqueueDedupeAndFull(t *testing.T) {
	q := newHubQueue()
	q.ch = make(chan hubMsg, 1)
	if !q.enqueue(hubMsg{App: "a", ID: "1"}) || !q.enqueue(hubMsg{App: "a", ID: "1"}) {
		t.Fatal("redelivery of a pending message must be absorbed")
	}
	if q.enqueue(hubMsg{App: "a", ID: "2"}) {
		t.Fatal("full queue must refuse (message stays un-acked)")
	}
}

func TestHubClientRoundTrip(t *testing.T) {
	var mu sync.Mutex
	conns, pings := 0, 0
	var firstAuth, cleanClose bool
	var ackSeen hubAck
	redelivered := make(chan hubAck, 1)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/subscribe" || r.URL.Query().Get("topic") != "whatsapp" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		conns++
		n := conns
		if n == 1 {
			firstAuth = r.Header.Get("Authorization") == "Bearer tok"
		}
		mu.Unlock()
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		_ = ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"msg","app":"app","id":"m1","to":"628111111111","text":"hi","created_at":%d}`, time.Now().Unix())))
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				mu.Lock()
				cleanClose = n == 2 && websocket.IsCloseError(err, websocket.CloseNormalClosure)
				mu.Unlock()
				return
			}
			if string(data) == "ping" {
				mu.Lock()
				pings++
				mu.Unlock()
				_ = ws.WriteMessage(websocket.TextMessage, []byte("pong"))
				continue
			}
			var a hubAck
			if json.Unmarshal(data, &a) != nil || a.Type != "ack" {
				continue
			}
			if n == 1 {
				mu.Lock()
				ackSeen = a
				mu.Unlock()
				return // server closes after the ack: the client must reconnect
			}
			redelivered <- a // 2nd connection: the same id is re-acked from the store, not re-sent
		}
	}))
	defer srv.Close()

	r := newHubRig(t, "628111111111")
	r.q.now, r.q.sleep = time.Now, sleepCtx
	var smu sync.Mutex
	r.q.send = func(_ context.Context, to, text string) error {
		smu.Lock()
		r.sends = append(r.sends, to+":"+text)
		smu.Unlock()
		return nil
	}
	h := newHubClient("ws"+strings.TrimPrefix(srv.URL, "http"), "tok", r.q)
	h.pingEvery, h.minBackoff, h.maxBackoff = 20*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond
	r.q.ack = h.sendAck
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.q.run(ctx) }()
	go func() { defer wg.Done(); h.run(ctx) }()

	select {
	case a := <-redelivered:
		if a.Status != "sent" || a.ID != "m1" {
			t.Fatalf("redelivery ack %v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no ack after reconnect")
	}
	time.Sleep(60 * time.Millisecond) // let a ping tick on the 2nd connection
	cancel()
	wg.Wait()
	time.Sleep(50 * time.Millisecond) // server goroutine records the close

	mu.Lock()
	defer mu.Unlock()
	smu.Lock()
	defer smu.Unlock()
	if !firstAuth || conns < 2 || pings == 0 || ackSeen.Status != "sent" || len(r.sends) != 1 || !cleanClose {
		t.Fatalf("auth=%v conns=%d pings=%d ack=%v sends=%v clean=%v", firstAuth, conns, pings, ackSeen, r.sends, cleanClose)
	}
}
