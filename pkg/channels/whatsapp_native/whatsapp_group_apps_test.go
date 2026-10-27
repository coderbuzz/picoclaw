//go:build whatsapp_native

package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

var testGroup = types.NewJID("1203630000000000", types.GroupServer)

const groupPattern = `(?i)^SIRKEL (HUBUNGKAN [A-Z0-9]{4}-?[A-Z0-9]{4}|PUTUSKAN)$`

type groupRig struct {
	*dmHarness
	dir string
	mu  sync.Mutex
	got []byte
	hdr http.Header
	n   int
}

func newGroupRig(t *testing.T, reply string) *groupRig {
	t.Helper()
	g := &groupRig{dir: t.TempDir()}
	g.dmHarness = newDMHarness(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.got, _ = io.ReadAll(r.Body)
		g.hdr = r.Header.Clone()
		g.n++
		g.mu.Unlock()
		_, _ = w.Write([]byte(reply))
	}))
	hook := g.ch.dmHooks[0]
	hook.re, hook.name = regexp.MustCompile("^(?:"+groupPattern+")$"), "sirkel"
	g.ch.dmHooks = nil
	g.ch.groupReg = &dmRegistry{hooks: []dmWebhook{hook}, next: time.Now().Add(time.Hour), now: time.Now}
	g.ch.appGroups = &appGroups{dir: filepath.Join(g.dir, "app_groups")}
	g.ch.fetchGroupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) {
		return &types.GroupInfo{GroupName: types.GroupName{Name: "Grup Uji"}, Participants: []types.GroupParticipant{
			{JID: dmLID, PhoneNumber: dmPN, LID: dmLID, IsAdmin: true},
		}}, nil
	}
	g.ch.config.GroupPolicy = "allowlist" // the test group is NOT in the allowlist
	return g
}

func (g *groupRig) send(id, text string) {
	evt := dmEvent(testGroup, text)
	evt.Info.ID = id
	g.ch.handleIncoming(evt)
}

func (g *groupRig) calls() int { g.mu.Lock(); defer g.mu.Unlock(); return g.n }

func TestGroupApp_PayloadSignatureReplyAndLink(t *testing.T) {
	g := newGroupRig(t, `{"reply":"terhubung","link":true}`)
	g.send("g1", "  sirkel   hubungkan  ab12-cd34 ") // lower case, double spaces, outside the allowlist
	if got := g.wantSent(t); got != testGroup.String()+"|terhubung" {
		t.Fatalf("sent %q", got)
	}
	g.wantForwarded(t, false)

	g.mu.Lock()
	body, hdr := g.got, g.hdr
	g.mu.Unlock()
	var p groupAppPayload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if p.GroupJID != testGroup.String() || p.Text != "SIRKEL HUBUNGKAN AB12-CD34" || p.MessageID != "g1" ||
		*p.GroupName != "Grup Uji" || *p.SenderPN != "628000000001" || *p.SenderLID != "100000000000002" ||
		p.SenderIsAdmin == nil || !*p.SenderIsAdmin {
		t.Fatalf("payload %+v", p)
	}
	ts, _ := strconv.ParseInt(hdr.Get("X-Picoclaw-Timestamp"), 10, 64)
	if hdr.Get("X-Picoclaw-Signature") != signDMWebhook([]byte("s3cret"), ts, body) {
		t.Fatal("bad signature")
	}

	path := filepath.Join(g.dir, "app_groups", "sirkel.json")
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("app_groups file: %v %v", st, err)
	}
	if !g.ch.appGroups.has("sirkel", testGroup.String()) {
		t.Fatal("group not linked")
	}
}

func TestGroupApp_UnlinkAndContradiction(t *testing.T) {
	g := newGroupRig(t, `{"reply":"putus","unlink":true}`)
	if err := g.ch.appGroups.set("sirkel", testGroup.String(), true); err != nil {
		t.Fatal(err)
	}
	g.send("g1", "SIRKEL PUTUSKAN")
	g.wantSent(t)
	if g.ch.appGroups.has("sirkel", testGroup.String()) {
		t.Fatal("still linked")
	}
	b, _ := os.ReadFile(filepath.Join(g.dir, "app_groups", "sirkel.json"))
	if string(b) != `{"groups":[]}` {
		t.Fatalf("file %s", b)
	}

	both := newGroupRig(t, `{"reply":"x","link":true,"unlink":true}`)
	both.send("g1", "SIRKEL PUTUSKAN")
	both.wantSent(t)
	if both.ch.appGroups.has("sirkel", testGroup.String()) {
		t.Fatal("contradictory reply must change nothing")
	}
}

func TestGroupApp_NonMatchingNeverCallsApp(t *testing.T) {
	g := newGroupRig(t, `{"reply":"x"}`)
	g.ch.config.GroupPolicy = "open"
	for i, txt := range []string{"halo semua", "SIRKEL PUTUSKAN sekarang", "SIRKEL HUBUNGKAN AB12", "x SIRKEL PUTUSKAN"} {
		g.send(fmt.Sprint("n", i), txt)
	}
	time.Sleep(300 * time.Millisecond)
	if g.calls() != 0 {
		t.Fatalf("app called %d times", g.calls())
	}
}

func TestGroupApp_OutsideAllowlistDoesNotReachAgent(t *testing.T) {
	g := newGroupRig(t, `{"reply":""}`)
	g.send("g1", "halo") // allowlist policy, group not listed: dropped as before
	g.wantForwarded(t, false)
	g.send("g2", "SIRKEL PUTUSKAN")
	time.Sleep(300 * time.Millisecond)
	if g.calls() != 1 {
		t.Fatalf("calls=%d", g.calls())
	}
	g.wantForwarded(t, false)
}

func TestGroupApp_FailureIsSilentAndLinkUntouched(t *testing.T) {
	g := newGroupRig(t, ``)
	g.ch.groupReg.hooks[0].url = "http://127.0.0.1:1" // refused
	g.send("g1", "SIRKEL HUBUNGKAN AB12-CD34")
	select {
	case s := <-g.sent:
		t.Fatalf("unexpected reply %q", s)
	case <-time.After(500 * time.Millisecond):
	}
	if g.ch.appGroups.has("sirkel", testGroup.String()) {
		t.Fatal("linked on failure")
	}
}

func TestGroupApp_DuplicateIDAndRateLimit(t *testing.T) {
	g := newGroupRig(t, `{"reply":"ok"}`)
	g.send("same", "SIRKEL PUTUSKAN")
	g.send("same", "SIRKEL PUTUSKAN")
	for i := range 6 {
		g.send(fmt.Sprint("r", i), "SIRKEL PUTUSKAN")
	}
	time.Sleep(500 * time.Millisecond)
	if n := g.calls(); n != dmRateMax {
		t.Fatalf("calls=%d want %d (dedupe + per-group limit)", n, dmRateMax)
	}
}

func TestGroupApp_AdminUnknownIsNull(t *testing.T) {
	g := newGroupRig(t, `{"reply":"ok"}`)
	g.ch.fetchGroupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) { return nil, os.ErrDeadlineExceeded }
	g.send("g1", "SIRKEL PUTUSKAN")
	g.wantSent(t)
	g.mu.Lock()
	defer g.mu.Unlock()
	var raw map[string]any
	_ = json.Unmarshal(g.got, &raw)
	if v, ok := raw["sender_is_admin"]; !ok || v != nil {
		t.Fatalf("sender_is_admin=%v", v)
	}
	if v, ok := raw["group_name"]; !ok || v != nil {
		t.Fatalf("group_name=%v", v)
	}
}

func TestGroupAppsRegistryLoad(t *testing.T) {
	_, dir := newRegDir(t)
	put := func(name, prefix, extra string) {
		writeFile(t, filepath.Join(dir, name), app(prefix, extra), 0o600)
	}
	put("sirkel.json", "SIRKEL", `,"pattern":"^SIRKEL PUTUSKAN$"`)
	put("nopattern.json", "NOPAT", "")
	put("dup.json", "SIRKEL", `,"pattern":"^SIRKEL X$"`)
	if hs := loadApps(dir, false); len(hs) != 0 { // shared prefix rejects both; no default pattern for groups
		t.Fatalf("hooks %v", names(hs))
	}
	_ = os.Remove(filepath.Join(dir, "dup.json"))
	hs := loadApps(dir, false)
	if len(hs) != 1 || hs[0].name != "sirkel" || matchDMWebhook(hs, "SIRKEL PUTUSKAN") == nil || matchDMWebhook(hs, "sirkel putuskan") == nil {
		t.Fatalf("hooks %v", names(hs))
	}
}

func TestAppGroupsRejectsUnsafeAppName(t *testing.T) {
	a := &appGroups{dir: t.TempDir()}
	if a.set("../evil", "1@g.us", true) == nil || a.has("../evil", "1@g.us") {
		t.Fatal("unsafe app name accepted")
	}
}

func TestGroupApp_ReplyDailyCaps(t *testing.T) {
	g := newGroupRig(t, `{"reply":"ok"}`)
	for i := range groupReplyDay + 3 {
		if got, want := g.ch.allowReply(testGroup.String()), i < groupReplyDay; got != want {
			t.Fatalf("reply %d allowed=%v", i, got)
		}
	}
	if !g.ch.allowReply("other@g.us") {
		t.Fatal("cap must be per group")
	}
	// the bot-wide cap is shared: group replies count toward it
	for range hubGlobalMax {
		g.ch.day.add("", time.Now())
	}
	if g.ch.allowReply("third@g.us") {
		t.Fatal("global cap not applied")
	}
	// and hub sends count too, in the same counter
	r := newHubRig(t, "628111111111")
	r.q.day = &g.ch.day
	r.q.now = time.Now
	r.handle("1", "628111111111")
	if r.lastStatus() != "rate_limited" {
		t.Fatalf("hub ignored shared counter: %s", r.lastStatus())
	}
}
