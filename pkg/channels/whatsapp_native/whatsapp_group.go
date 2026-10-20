//go:build whatsapp_native

package whatsapp

import (
	"fmt"
	"regexp"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

const (
	groupPolicyOpen      = "open"
	groupPolicyAllowlist = "allowlist"
	groupPolicyDisabled  = "disabled"
)

// contextInfoOf returns the ContextInfo of whichever message type carries one.
func contextInfoOf(m *waE2E.Message) *waE2E.ContextInfo {
	switch {
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetContextInfo()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetContextInfo()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetContextInfo()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage().GetContextInfo()
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage().GetContextInfo()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetContextInfo()
	}
	return nil
}

// isBotJID reports whether raw (a JID string) is one of the bot's own identities
// (phone-number JID or LID), ignoring the device suffix.
func isBotJID(raw string, bots []types.JID) bool {
	jid, err := types.ParseJID(raw)
	if err != nil {
		return false
	}
	jid = jid.ToNonAD()
	for _, b := range bots {
		if !b.IsEmpty() && b.User == jid.User && b.Server == jid.Server {
			return true
		}
	}
	return false
}

// isTriggeredToBot is true when the message mentions the bot or quotes one of its messages.
func isTriggeredToBot(ci *waE2E.ContextInfo, bots []types.JID) bool {
	if ci == nil {
		return false
	}
	if isBotJID(ci.GetParticipant(), bots) {
		return true
	}
	for _, m := range ci.GetMentionedJID() {
		if isBotJID(m, bots) {
			return true
		}
	}
	return false
}

// groupAllowed applies group_policy / group_allow_from to a group chat JID.
// Unknown policies fail closed.
func groupAllowed(policy string, allow []string, chat types.JID) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", groupPolicyOpen:
		return true
	case groupPolicyAllowlist:
		for _, a := range allow {
			if strings.TrimSpace(a) == chat.ToNonAD().String() {
				return true
			}
		}
	}
	return false
}

// senderIDs lists the IDs a sender may be written as in allow_from: full JID and bare user,
// without device suffix, for every known address (PN and LID) of the same person.
func senderIDs(sender, alt types.JID, counterpart func(types.JID) types.JID) []string {
	jids := []types.JID{sender.ToNonAD()}
	if !alt.IsEmpty() {
		jids = append(jids, alt.ToNonAD())
	} else if counterpart != nil {
		if o := counterpart(sender.ToNonAD()); !o.IsEmpty() {
			jids = append(jids, o.ToNonAD())
		}
	}
	var ids []string
	for _, j := range jids {
		ids = append(ids, j.String(), j.User)
	}
	return ids
}

// stripBotMentions removes whole "@<user>" tokens of the bot's own identities
// (number and LID) plus one adjacent space; newlines and other mentions are kept.
func stripBotMentions(content string, bots []types.JID) string {
	for _, b := range bots {
		if !b.IsEmpty() {
			re := regexp.MustCompile(`(?m)(^|[ \t])@` + regexp.QuoteMeta(b.User) + `\b[ \t]?`)
			content = re.ReplaceAllString(content, "$1")
		}
	}
	return strings.TrimSpace(content)
}

// prependQuoted adds the replied-to message as context, in the same format the Telegram channel uses.
// note is the quoted attachment's download note ("" when it was stored).
func prependQuoted(content string, ci *waE2E.ContextInfo, bots []types.JID, note string) string {
	q := ci.GetQuotedMessage()
	quoted := strings.TrimSpace(messageText(q))
	if im := inboundMediaOf(q); im != nil {
		quoted = strings.TrimSpace(im.annotation(note) + " " + quoted)
	}
	if quoted == "" {
		return content
	}
	role, author := "user", ci.GetParticipant()
	if jid, err := types.ParseJID(author); err == nil {
		author = jid.User
	}
	if isBotJID(ci.GetParticipant(), bots) {
		role = "assistant"
	}
	header := fmt.Sprintf("[quoted %s message from %s]: %s", role, author, quoted)
	if strings.TrimSpace(content) == "" {
		return header
	}
	return header + "\n\n" + content
}
