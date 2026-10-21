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
		{"hello @100000000000001 there", "hello there"},
		{"@628000000000 @100000000000001 hi @628111111111", "hi @628111111111"},
		{"no mention", "no mention"},
		{"@628000000000 list:\n- a\n- b", "list:\n- a\n- b"},
		{"@6280000000001 keep", "@6280000000001 keep"},
		{"@628000000000", ""},
	}
	for _, tt := range tests {
		if got := stripBotMentions(tt.in, bots); got != tt.want {
			t.Errorf("stripBotMentions(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPrependQuoted(t *testing.T) {
	quote := func(participant string, q *waE2E.Message) *waE2E.ContextInfo {
		return &waE2E.ContextInfo{Participant: proto.String(participant), QuotedMessage: q}
	}
	conv := &waE2E.Message{Conversation: proto.String("rapat Sabtu jam 9")}
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("denah")}}
	pdf := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("iuran.pdf")}}
	cases := []struct {
		name, content, want, note string
		ci                        *waE2E.ContextInfo
	}{
		{"no quote", "halo", "halo", "", nil},
		{"user text", "@bot ini jam berapa?", "[quoted user message from 6281111]: rapat Sabtu jam 9\n\n@bot ini jam berapa?", "", quote("6281111@s.whatsapp.net", conv)},
		{"bot text", "lanjut", "[quoted assistant message from 628000000000]: rapat Sabtu jam 9\n\nlanjut", "", quote(botPN.String(), conv)},
		{"image", "", "[quoted user message from 6281111]: [image] denah", "", quote("6281111@s.whatsapp.net", img)},
		{"file failed", "cek", "[quoted user message from 6281111]: [file: iuran.pdf, download failed]\n\ncek", "download failed", quote("6281111@s.whatsapp.net", pdf)},
	}
	for _, tc := range cases {
		if got := prependQuoted(tc.content, tc.ci, bots, tc.note); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestGroupListJSON(t *testing.T) {
	g := func(user, name string, members int) *types.GroupInfo {
		info := &types.GroupInfo{JID: types.NewJID(user, types.GroupServer), Participants: make([]types.GroupParticipant, members)}
		info.Name = name
		return info
	}
	data, err := groupListJSON([]*types.GroupInfo{g("2", "Zeta", 3), nil, g("1", "Alpha", 5)})
	if err != nil {
		t.Fatal(err)
	}
	want := `[
  {
    "jid": "1@g.us",
    "name": "Alpha",
    "participants": 5
  },
  {
    "jid": "2@g.us",
    "name": "Zeta",
    "participants": 3
  }
]`
	if string(data) != want {
		t.Errorf("got %s", data)
	}
}
