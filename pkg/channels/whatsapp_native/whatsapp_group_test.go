//go:build whatsapp_native

package whatsapp

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

var (
	botPN  = types.NewJID("628000000000", types.DefaultUserServer)
	botLID = types.NewJID("100000000000001", types.HiddenUserServer)
	bots   = []types.JID{botPN, botLID}
)

func textMsg(ci *waE2E.ContextInfo) *waE2E.Message {
	return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("hi"), ContextInfo: ci}}
}

func TestIsTriggeredToBot(t *testing.T) {
	tests := []struct {
		name string
		ci   *waE2E.ContextInfo
		want bool
	}{
		{"no context", nil, false},
		{"mention PN with device", &waE2E.ContextInfo{MentionedJID: []string{"628000000000:12@s.whatsapp.net"}}, true},
		{"mention LID", &waE2E.ContextInfo{MentionedJID: []string{"100000000000001@lid"}}, true},
		{"mention someone else", &waE2E.ContextInfo{MentionedJID: []string{"628111111111@s.whatsapp.net"}}, false},
		{"reply to bot PN", &waE2E.ContextInfo{Participant: proto.String("628000000000@s.whatsapp.net")}, true},
		{"reply to bot LID", &waE2E.ContextInfo{Participant: proto.String("100000000000001@lid")}, true},
		{"reply to other", &waE2E.ContextInfo{Participant: proto.String("628111111111@s.whatsapp.net")}, false},
		{"garbage jid", &waE2E.ContextInfo{MentionedJID: []string{"%%"}}, false},
	}
	for _, tt := range tests {
		if got := isTriggeredToBot(contextInfoOf(textMsg(tt.ci)), bots); got != tt.want {
			t.Errorf("%s: got %v want %v", tt.name, got, tt.want)
		}
	}
	if isTriggeredToBot(&waE2E.ContextInfo{MentionedJID: []string{"628000000000@s.whatsapp.net"}}, nil) {
		t.Error("unpaired bot must not match")
	}
}

func TestContextInfoOfImage(t *testing.T) {
	ci := &waE2E.ContextInfo{StanzaID: proto.String("x")}
	m := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{ContextInfo: ci}}
	if contextInfoOf(m) != ci {
		t.Error("image context info not found")
	}
}

func TestGroupAllowed(t *testing.T) {
	g := types.NewJID("1203630000000000", types.GroupServer)
	other := types.NewJID("1203630000000001", types.GroupServer)
	list := []string{"1203630000000000@g.us"}
	tests := []struct {
		policy string
		chat   types.JID
		want   bool
	}{
		{"", g, true}, {"open", other, true},
		{"allowlist", g, true}, {"allowlist", other, false},
		{"disabled", g, false}, {"bogus", g, false},
	}
	for _, tt := range tests {
		if got := groupAllowed(tt.policy, list, tt.chat); got != tt.want {
			t.Errorf("policy %q chat %s: got %v want %v", tt.policy, tt.chat, got, tt.want)
		}
	}
	if groupAllowed("allowlist", nil, g) {
		t.Error("empty allowlist must deny")
	}
}

func TestSenderIDs(t *testing.T) {
	pn := types.JID{User: "628111111111", Device: 12, Server: types.DefaultUserServer}
	lid := types.NewJID("100000000000002", types.HiddenUserServer)
	has := func(ids []string, w string) bool {
		for _, i := range ids {
			if i == w {
				return true
			}
		}
		return false
	}
	ids := senderIDs(pn, types.EmptyJID, nil)
	if !has(ids, "628111111111@s.whatsapp.net") || !has(ids, "628111111111") || has(ids, pn.String()) {
		t.Errorf("device suffix not stripped: %v", ids)
	}
	ids = senderIDs(lid, pn, nil) // SenderAlt supplies the number
	if !has(ids, "100000000000002@lid") || !has(ids, "628111111111") {
		t.Errorf("alt not included: %v", ids)
	}
	ids = senderIDs(lid, types.EmptyJID, func(types.JID) types.JID { return pn })
	if !has(ids, "628111111111") {
		t.Errorf("counterpart not included: %v", ids)
	}
}

func TestStripBotMentions(t *testing.T) {
	tests := []struct{ in, want string }{
		{"@628000000000 hello", "hello"},
		{"hello @100000000000001  there", "hello there"},
		{"@628000000000 @100000000000001 hi @628111111111", "hi @628111111111"},
		{"no mention", "no mention"},
		{"@628000000000", ""},
	}
	for _, tt := range tests {
		if got := stripBotMentions(tt.in, bots); got != tt.want {
			t.Errorf("stripBotMentions(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
