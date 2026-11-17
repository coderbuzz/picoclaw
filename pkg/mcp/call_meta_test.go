package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
)

// An app server sees the bot token read from a file:// header and the caller facts in _meta.
func TestCallTool_SendsCallerMetaAndFileHeader(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.token"), []byte("Bearer s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.SetConfigDir(dir)
	t.Cleanup(func() { config.SetConfigDir("") })

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "app", Version: "1"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "whoami", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(_ context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			b, _ := json.Marshal(req.Params.Meta[CallerMetaKey])
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(b)}}}, nil
		})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	conn, err := connectServerFunc(context.Background(), "app", config.MCPServerConfig{
		Enabled: true, Type: "http", URL: srv.URL,
		Headers: map[string]string{"Authorization": "file://app.token"},
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	mgr := NewManager()
	mgr.servers["app"] = conn
	defer mgr.Close()

	ctx := WithCallerMeta(context.Background(), map[string]string{"chat_id": "g1@g.us", "agent_id": "club"})
	res, err := mgr.CallTool(ctx, "app", "whoami", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	got := res.Content[0].(*sdkmcp.TextContent).Text
	if got != `{"agent_id":"club","chat_id":"g1@g.us"}` {
		t.Fatalf("caller meta = %s", got)
	}

	res, err = mgr.CallTool(context.Background(), "app", "whoami", nil)
	if err != nil {
		t.Fatalf("CallTool without caller: %v", err)
	}
	if got := res.Content[0].(*sdkmcp.TextContent).Text; got != "null" {
		t.Fatalf("meta without caller = %s, want null", got)
	}
}
