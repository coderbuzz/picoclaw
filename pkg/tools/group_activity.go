package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/groupactivity"
)

const (
	groupActivityDefaultLimit = 30
	groupActivityNameBatch    = 100
)

var groupActivityJSONLine = regexp.MustCompile(`(?m)^\s*(\[.*\])\s*$`)

// MCPCaller is the part of the MCP manager the tool needs to resolve names itself (the model never sees this call).
type MCPCaller interface {
	CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*sdkmcp.CallToolResult, error)
}

// GroupActivityTool reports who wrote how much in the current WhatsApp group. The group comes from the turn's chat,
// never from arguments, and phone numbers never appear in the result.
type GroupActivityTool struct {
	cfg     *config.GroupActivityConfig
	dbPath  string // group_activity.db; the whatsmeow store.db sits next to it
	once    sync.Once
	store   *groupactivity.Store
	openErr error
	mcp     atomic.Pointer[MCPCaller]
	now     func() time.Time
}

func NewGroupActivityTool(cfg *config.GroupActivityConfig, dbPath string) *GroupActivityTool {
	return &GroupActivityTool{cfg: cfg, dbPath: dbPath, now: time.Now}
}

// SetMCP gives the tool the MCP manager once it is connected; without it names fall back to push names.
func (t *GroupActivityTool) SetMCP(c MCPCaller) { t.mcp.Store(&c) }

func (t *GroupActivityTool) Name() string { return "group_activity" }

func (t *GroupActivityTool) Description() string {
	return "Count messages per member in THIS group chat (most active first). period: today, week (last 7 days), " +
		"month (last 30 days) or since (needs date YYYY-MM-DD). Counting started when the feature was switched on; " +
		"the result says since when. Phone numbers are never shown."
}

func (t *GroupActivityTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"period": map[string]any{"type": "string", "enum": []string{"today", "week", "month", "since"}},
			"date":   map[string]any{"type": "string", "description": "YYYY-MM-DD, only with period=since."},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Members to list, default 30."},
		},
		"required": []string{"period"},
	}
}

func (t *GroupActivityTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	chat := ToolChatID(ctx)
	if !groupactivity.Counts(t.cfg, chat) {
		return ErrorResult("Penghitung belum dinyalakan di grup ini")
	}
	now := t.now()
	period, _ := args["period"].(string)
	var from string
	switch period {
	case "today":
		from = groupactivity.Day(now)
	case "week":
		from = groupactivity.Day(now.AddDate(0, 0, -6))
	case "month":
		from = groupactivity.Day(now.AddDate(0, 0, -29))
	case "since":
		d, _ := args["date"].(string)
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return ErrorResult("group_activity: period=since needs date as YYYY-MM-DD")
		}
		from = d
	default:
		return ErrorResult("group_activity: period must be today, week, month or since")
	}
	limit := groupActivityDefaultLimit
	if f, ok := args["limit"].(float64); ok {
		limit = int(f)
	}
	limit = min(max(limit, 1), 100)

	t.once.Do(func() {
		t.store, t.openErr = groupactivity.Open(t.dbPath, groupactivity.Retention(t.cfg))
	})
	if t.openErr != nil {
		return ErrorResult("group_activity: store unavailable")
	}
	sum, err := t.store.Top(chat, from, limit)
	if err != nil {
		return ErrorResult("group_activity: " + err.Error())
	}
	if sum.Total == 0 {
		return NewToolResult(fmt.Sprintf("Belum ada pesan terhitung pada periode ini (dihitung sejak %s).", sinceOrNow(sum.Since, now)))
	}

	names := t.registeredNames(ctx, sum.Members)
	var b strings.Builder
	fmt.Fprintf(&b, "Jumlah pesan per orang, mulai %s (dihitung sejak %s):\n", from, sinceOrNow(sum.Since, now))
	for i, m := range sum.Members {
		fmt.Fprintf(&b, "%d. %s: %d\n", i+1, displayName(m, names), m.N)
	}
	fmt.Fprintf(&b, "Total %d pesan dari %d orang.", sum.Total, sum.People)
	return NewToolResult(b.String())
}

func sinceOrNow(since string, now time.Time) string {
	if since == "" {
		return groupactivity.Day(now)
	}
	return since
}

// displayName: the registered name; else the push name, marked "(belum terdaftar)" when names are looked up at all.
func displayName(m groupactivity.Member, names map[string]*string) string {
	reg, looked := names[m.Sender]
	if reg != nil && *reg != "" {
		return *reg
	}
	n := strings.TrimSpace(m.PushName)
	if n == "" {
		n = "(tanpa nama)"
	}
	if looked {
		return n + " (belum terdaftar)"
	}
	return n
}

// registeredNames asks the configured MCP tool for the members' names. Any failure returns nil (push names only).
// The map holds an entry (possibly nil = not registered) for each sender whose number was looked up.
func (t *GroupActivityTool) registeredNames(ctx context.Context, members []groupactivity.Member) map[string]*string {
	nm := t.cfg.Names
	cp := t.mcp.Load()
	if nm == nil || nm.Server == "" || nm.Tool == "" || cp == nil {
		return nil
	}
	senders := make([]string, len(members))
	for i, m := range members {
		senders[i] = m.Sender
	}
	numbers := groupactivity.PhoneNumbers(filepath.Join(filepath.Dir(t.dbPath), "store.db"), senders)
	var order []string // senders with a known number, in call order
	var nums []string
	for _, s := range senders {
		if n, ok := numbers[s]; ok {
			order = append(order, s)
			nums = append(nums, n)
		}
	}
	out := map[string]*string{}
	for lo := 0; lo < len(nums); lo += groupActivityNameBatch {
		hi := min(lo+groupActivityNameBatch, len(nums))
		res, err := (*cp).CallTool(WithCallerMeta(ctx), nm.Server, nm.Tool, map[string]any{"nomor": nums[lo:hi]})
		if err != nil || res == nil || res.IsError {
			return nil
		}
		parsed, ok := parseNameRows(res.Content, hi-lo)
		if !ok {
			return nil
		}
		for i := lo; i < hi; i++ {
			out[order[i]] = parsed[i-lo]
		}
	}
	return out
}

// parseNameRows reads the last JSON-array line of the tool text: [{"i":0,"nama":"..","peran":".."}], i = index in the
// numbers sent. A null or missing name means "not registered" (nil entry).
func parseNameRows(content []sdkmcp.Content, n int) ([]*string, bool) {
	var text strings.Builder
	for _, c := range content {
		if tc, ok := c.(*sdkmcp.TextContent); ok {
			text.WriteString(tc.Text + "\n")
		}
	}
	lines := groupActivityJSONLine.FindAllStringSubmatch(text.String(), -1)
	if len(lines) == 0 {
		return nil, false
	}
	var rows []struct {
		I    int     `json:"i"`
		Nama *string `json:"nama"`
	}
	if json.Unmarshal([]byte(lines[len(lines)-1][1]), &rows) != nil {
		return nil, false
	}
	out := make([]*string, n)
	for _, r := range rows {
		if r.I >= 0 && r.I < n {
			out[r.I] = r.Nama
		}
	}
	return out, true
}
