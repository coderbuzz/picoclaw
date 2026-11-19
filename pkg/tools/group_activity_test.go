package tools

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/groupactivity"
	toolshared "github.com/sipeed/picoclaw/pkg/tools/shared"
)

type fakeNames struct {
	text    string
	err     error
	gotArgs map[string]any
	calls   int
}

func (f *fakeNames) CallTool(ctx context.Context, server, tool string, args map[string]any) (*sdkmcp.CallToolResult, error) {
	f.calls++
	f.gotArgs = args
	if server != "sirkel_grup" || tool != "nama_anggota" {
		return nil, errors.New("wrong tool " + server + "/" + tool)
	}
	if f.err != nil {
		return nil, f.err
	}
	return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: f.text}}}, nil
}

const (
	gaChat = "120363000000000000@g.us"
	gaAni  = "100000000000001@lid"   // maps to 628111
	gaBudi = "628222@s.whatsapp.net" // number is in the JID
	gaCici = "100000000000003@lid"   // unmapped
)

func gaTool(t *testing.T, names *config.GroupActivityNamesTool) (*GroupActivityTool, time.Time) {
	t.Helper()
	dir := t.TempDir()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, groupactivity.WIB)

	wa, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wa.Exec(`CREATE TABLE whatsmeow_lid_map(lid TEXT PRIMARY KEY, pn TEXT UNIQUE NOT NULL);
		INSERT INTO whatsmeow_lid_map VALUES('100000000000001','628111')`); err != nil {
		t.Fatal(err)
	}
	wa.Close()

	db := filepath.Join(dir, groupactivity.DBName)
	s, err := groupactivity.Open(db, 90)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_ = s.Record(gaChat, gaAni, "Ani", now)
	}
	for i := 0; i < 3; i++ {
		_ = s.Record(gaChat, gaBudi, "Budi", now.AddDate(0, 0, -2))
	}
	_ = s.Record(gaChat, gaCici, "Cici", now.AddDate(0, 0, -20))
	_ = s.Record("999@g.us", gaAni, "Ani", now)
	s.Close()

	tool := NewGroupActivityTool(&config.GroupActivityConfig{Groups: []string{gaChat}, Names: names}, db)
	tool.now = func() time.Time { return now }
	return tool, now
}

func gaRun(tool *GroupActivityTool, chat string, args map[string]any) *ToolResult {
	ctx := toolshared.WithToolContext(context.Background(), "whatsapp", chat)
	return tool.Execute(ctx, args)
}

func TestGroupActivityScopingPeriodsAndNoNumbers(t *testing.T) {
	tool, _ := gaTool(t, nil)

	if r := gaRun(tool, "999@g.us", map[string]any{"period": "week"}); !r.IsError || !strings.Contains(r.ForLLM, "belum dinyalakan") {
		t.Errorf("unlisted group: %+v", r)
	}
	if r := gaRun(tool, "", map[string]any{"period": "week"}); !r.IsError {
		t.Error("no chat in ctx must be refused")
	}

	r := gaRun(tool, gaChat, map[string]any{"period": "week"})
	if r.IsError {
		t.Fatal(r.ForLLM)
	}
	for _, w := range []string{"1. Ani: 5", "2. Budi: 3", "Total 8 pesan dari 2 orang", "dihitung sejak 2026-10-30"} {
		if !strings.Contains(r.ForLLM, w) {
			t.Errorf("week missing %q:\n%s", w, r.ForLLM)
		}
	}
	if strings.Contains(r.ForLLM, "Cici") {
		t.Errorf("20 days ago is outside week:\n%s", r.ForLLM)
	}
	if regexp.MustCompile(`\d{6,}`).MatchString(r.ForLLM) {
		t.Errorf("a number/JID leaked:\n%s", r.ForLLM)
	}

	if r := gaRun(tool, gaChat, map[string]any{"period": "today"}); !strings.Contains(r.ForLLM, "Total 5 pesan dari 1 orang") {
		t.Errorf("today:\n%s", r.ForLLM)
	}
	if r := gaRun(tool, gaChat, map[string]any{"period": "month"}); !strings.Contains(r.ForLLM, "Cici: 1") {
		t.Errorf("month:\n%s", r.ForLLM)
	}
	if r := gaRun(tool, gaChat, map[string]any{"period": "since", "date": "2026-11-18"}); !strings.Contains(r.ForLLM, "Total 5 pesan") {
		t.Errorf("since:\n%s", r.ForLLM)
	}
	if r := gaRun(tool, gaChat, map[string]any{"period": "month", "limit": float64(1)}); !strings.Contains(r.ForLLM, "Total 9 pesan dari 3 orang") || strings.Contains(r.ForLLM, "Budi") {
		t.Errorf("limit:\n%s", r.ForLLM)
	}
	for _, bad := range []map[string]any{{}, {"period": "year"}, {"period": "since"}, {"period": "since", "date": "kemarin"}} {
		if r := gaRun(tool, gaChat, bad); !r.IsError {
			t.Errorf("%v not rejected", bad)
		}
	}
}

func TestGroupActivityResolvesNamesThroughMCP(t *testing.T) {
	tool, _ := gaTool(t, &config.GroupActivityNamesTool{Server: "sirkel_grup", Tool: "nama_anggota"})
	fake := &fakeNames{text: "Hasil:\n[{\"i\":0,\"nama\":\"Dr. Ani Wijaya\",\"peran\":\"anggota\"},{\"i\":1,\"nama\":null,\"peran\":null}]"}
	tool.SetMCP(fake)

	r := gaRun(tool, gaChat, map[string]any{"period": "month"})
	if r.IsError {
		t.Fatal(r.ForLLM)
	}
	nums, _ := fake.gotArgs["nomor"].([]string)
	if len(nums) != 2 || nums[0] != "628111" || nums[1] != "628222" { // Cici has no number: not sent
		t.Errorf("nomor = %v (LID mapped via store, PN from JID, unmapped skipped)", fake.gotArgs["nomor"])
	}
	for _, w := range []string{"1. Dr. Ani Wijaya: 5", "2. Budi (belum terdaftar): 3", "3. Cici: 1"} {
		if !strings.Contains(r.ForLLM, w) {
			t.Errorf("missing %q:\n%s", w, r.ForLLM)
		}
	}
	if regexp.MustCompile(`\d{6,}`).MatchString(r.ForLLM) {
		t.Errorf("a number leaked:\n%s", r.ForLLM)
	}
}

func TestGroupActivityMCPFailureFallsBackToPushNames(t *testing.T) {
	for name, fake := range map[string]*fakeNames{
		"error":    {err: errors.New("down")},
		"no json":  {text: "ok tapi tanpa baris json"},
		"bad json": {text: "[{oops"},
	} {
		tool, _ := gaTool(t, &config.GroupActivityNamesTool{Server: "sirkel_grup", Tool: "nama_anggota"})
		tool.SetMCP(fake)
		r := gaRun(tool, gaChat, map[string]any{"period": "week"})
		if r.IsError || !strings.Contains(r.ForLLM, "1. Ani: 5") || strings.Contains(r.ForLLM, "belum terdaftar") {
			t.Errorf("%s: %+v", name, r)
		}
	}
}
