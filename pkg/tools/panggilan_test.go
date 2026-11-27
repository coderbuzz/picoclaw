package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	toolshared "github.com/sipeed/picoclaw/pkg/tools/shared"
)

func ctxWithSender(senderID string) context.Context {
	ctx := toolshared.WithToolContext(context.Background(), "whatsapp", "6285745674994-1483054751@g.us")
	return toolshared.WithToolSender(ctx, "group", senderID)
}

func TestPanggilanTool_MemberSelfOnly(t *testing.T) {
	ws := t.TempDir()
	tool := NewPanggilanTool(&config.AgentPanggilanConfig{CanSetAny: false}, ws)

	// Member sets their own callname.
	res := tool.Execute(ctxWithSender("15788383711443@lid"), map[string]any{
		"action":    "set",
		"panggilan": "Mbak Rahayu",
		"gender":    "P",
		"nama":      "Rahayu Wahyu B",
		"catatan":   "koreksi",
	})
	if res.IsError || !strings.Contains(res.ForLLM, "Mbak Rahayu") {
		t.Fatalf("set self failed: %+v", res)
	}

	// File must exist with 0600 permissions.
	info, err := os.Stat(filepath.Join(ws, "panggilan.md"))
	if err != nil {
		t.Fatalf("stat panggilan.md: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 perm, got %v", info.Mode().Perm())
	}

	// Member cannot set another member's target_id.
	res = tool.Execute(ctxWithSender("15788383711443@lid"), map[string]any{
		"action":    "set",
		"target_id": "999999@lid",
		"panggilan": "Pak X",
	})
	if !res.IsError || !strings.Contains(res.ForLLM, "Hanya admin/staf") {
		t.Fatalf("expected refusal when setting other ID: %+v", res)
	}

	// Member cannot delete another member's target_id.
	res = tool.Execute(ctxWithSender("15788383711443@lid"), map[string]any{
		"action":    "delete",
		"target_id": "999999@lid",
	})
	if !res.IsError {
		t.Fatalf("expected refusal when deleting other ID: %+v", res)
	}

	// Get returns saved entry.
	res = tool.Execute(ctxWithSender("15788383711443@lid"), map[string]any{"action": "get"})
	if res.IsError || !strings.Contains(res.ForLLM, "Mbak Rahayu") || !strings.Contains(res.ForLLM, "Gender: P") {
		t.Fatalf("get failed: %+v", res)
	}

	// Updating own callname without nama preserves existing nama and does not duplicate rows.
	res = tool.Execute(ctxWithSender("15788383711443@lid"), map[string]any{
		"action":    "set",
		"panggilan": "Bu Rahayu",
	})
	if res.IsError {
		t.Fatalf("update failed: %+v", res)
	}
	raw, err := os.ReadFile(filepath.Join(ws, "panggilan.md"))
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(raw), "15788383711443@lid"); count != 1 {
		t.Fatalf("expected 1 row for ID, got %d:\n%s", count, string(raw))
	}
	if !strings.Contains(string(raw), "Rahayu Wahyu B") || !strings.Contains(string(raw), "Bu Rahayu") {
		t.Fatalf("expected preserved nama and updated panggilan:\n%s", string(raw))
	}
}

func TestPanggilanTool_AdminCanSetAnyAndSanitize(t *testing.T) {
	ws := t.TempDir()
	sharedPath := filepath.Join(ws, "shared", "panggilan.md")
	tool := NewPanggilanTool(&config.AgentPanggilanConfig{
		File:      sharedPath,
		CanSetAny: true,
	}, ws)

	res := tool.Execute(ctxWithSender("admin@lid"), map[string]any{
		"action":    "set",
		"target_id": "222222@lid",
		"panggilan": "Pak\nBudi | A",
		"gender":    "laki",
		"nama":      "Budi Santoso",
	})
	if res.IsError {
		t.Fatalf("admin set failed: %+v", res)
	}

	raw, err := os.ReadFile(sharedPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Pak\nBudi") || !strings.Contains(string(raw), "Pak Budi / A") {
		t.Fatalf("sanitization failed:\n%s", string(raw))
	}
	if !strings.Contains(string(raw), "| L |") {
		t.Fatalf("expected normalized gender L:\n%s", string(raw))
	}

	// Delete target_id.
	res = tool.Execute(ctxWithSender("admin@lid"), map[string]any{
		"action":    "delete",
		"target_id": "222222@lid",
	})
	if res.IsError {
		t.Fatalf("admin delete failed: %+v", res)
	}
	res = tool.Execute(ctxWithSender("admin@lid"), map[string]any{
		"action":    "get",
		"target_id": "222222@lid",
	})
	if res.IsError || !strings.Contains(res.ForLLM, "Belum tercatat") {
		t.Fatalf("expected Belum tercatat after delete: %+v", res)
	}
}
