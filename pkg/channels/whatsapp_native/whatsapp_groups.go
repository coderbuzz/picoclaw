//go:build whatsapp_native

package whatsapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/sipeed/picoclaw/pkg/logger"
)

const groupListFile = "groups.json"

type groupListEntry struct {
	JID          string `json:"jid"`
	Name         string `json:"name"`
	Participants int    `json:"participants"`
}

// groupListJSON renders the joined groups sorted by name, for operators who need a group JID.
func groupListJSON(groups []*types.GroupInfo) ([]byte, error) {
	out := make([]groupListEntry, 0, len(groups))
	for _, g := range groups {
		if g == nil {
			continue
		}
		out = append(out, groupListEntry{JID: g.JID.String(), Name: g.Name, Participants: len(g.Participants)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return json.MarshalIndent(out, "", "  ")
}

// writeGroupList saves the joined groups to <store>/groups.json (0600, atomic replace).
func (c *WhatsAppNativeChannel) writeGroupList() {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil || client.Store.ID == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.runCtx, 30*time.Second)
	defer cancel()
	groups, err := client.GetJoinedGroups(ctx)
	if err != nil {
		logger.WarnCF("whatsapp", "Group list fetch failed", map[string]any{"error": err.Error()})
		return
	}
	data, err := groupListJSON(groups)
	if err != nil {
		return
	}
	path := filepath.Join(c.storePath, groupListFile)
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, data, 0o600); err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		logger.WarnCF("whatsapp", "Group list write failed", map[string]any{"error": err.Error()})
	}
}
