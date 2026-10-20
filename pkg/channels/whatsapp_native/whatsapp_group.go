//go:build whatsapp_native

package whatsapp

import (
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

// stripBotMentions removes "@<user>" tokens of the bot's own identities (number
// and LID) from content; mentions of other people are kept.
func stripBotMentions(content string, bots []types.JID) string {
	for _, b := range bots {
		if !b.IsEmpty() {
			content = strings.ReplaceAll(content, "@"+b.User, "")
		}
	}
	return strings.Join(strings.Fields(content), " ")
}
