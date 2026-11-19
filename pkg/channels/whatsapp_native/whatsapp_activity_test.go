//go:build whatsapp_native

package whatsapp

import (
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/groupactivity"
)

func TestGroupActivityCountsOnlyRealContentOfListedGroups(t *testing.T) {
	h := newDMHarness(t, "", http.NotFoundHandler())
	c := h.ch
	c.config.GroupPolicy = "allowlist" // the group is not allowlisted: counting must not depend on it
	c.config.GroupActivity = &config.GroupActivityConfig{Groups: []string{testGroup.String()}}
	c.storePath = t.TempDir()
	c.startGroupActivity()
	defer func() { _ = c.activity.Close() }()

	n := 0
	send := func(chat types.JID, fromMe bool, m *waE2E.Message) {
		n++
		evt := &events.Message{Info: dmEvent(chat, "").Info, Message: m}
		evt.Info.ID = "m" + strconv.Itoa(n)
		evt.Info.IsFromMe = fromMe
		evt.Info.PushName = "Ani"
		c.handleIncoming(evt)
	}
	text := &waE2E.Message{Conversation: proto.String("halo")}
	send(testGroup, false, text)
	send(testGroup, false, &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("balas")}})
	send(testGroup, false, &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}})
	send(testGroup, false, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("foto")}})
	// not counted
	send(testGroup, false, &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍")}})
	send(testGroup, false, &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum()}})
	send(testGroup, false, &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), EditedMessage: text}})
	send(testGroup, true, text)                                            // the bot itself
	send(types.NewJID("1203639999999999", types.GroupServer), false, text) // group not listed
	send(dmLID, false, text)                                               // direct message

	s, err := groupactivity.Open(filepath.Join(c.storePath, groupactivity.DBName), 90)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sum, _ := s.Top(testGroup.String(), "2000-01-01", 10)
	if sum.Total != 4 || sum.People != 1 || sum.Members[0].PushName != "Ani" || sum.Members[0].Sender != dmLID.String() {
		t.Errorf("summary = %+v", sum)
	}
	if other, _ := s.Top("1203639999999999@g.us", "2000-01-01", 10); other.Total != 0 {
		t.Errorf("unlisted group counted: %+v", other)
	}
}
