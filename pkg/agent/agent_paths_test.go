package agent

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// newPathTestAgent builds an agent whose workspace holds the given AGENT.md.
func newPathTestAgent(t *testing.T, agentMD string) *AgentInstance {
	t.Helper()
	workspace := t.TempDir()
	if agentMD != "" {
		if err := os.WriteFile(filepath.Join(workspace, "AGENT.md"), []byte(agentMD), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.RestrictToWorkspace = true
	cfg.Agents.Defaults.ModelName = "test-model"
	return NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
}

func TestAgentFrontmatterAllowPathsAreScopedToThatAgent(t *testing.T) {
	// Stands in for a directory like /srv/example outside every workspace.
	shared := t.TempDir()
	secret := filepath.Join(shared, "notes.txt")
	if err := os.WriteFile(secret, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	pattern := "^" + regexp.QuoteMeta(shared) + "(/|$)"

	withPaths := newPathTestAgent(t, "---\nname: a\nallow_read_paths: ['"+pattern+"']\nallow_write_paths: ['"+pattern+"']\n---\n# A\n")
	without := newPathTestAgent(t, "---\nname: b\n---\n# B\n")

	ctx := context.Background()

	if r := withPaths.Tools.Execute(ctx, "read_file", map[string]any{"path": secret}); r.IsError {
		t.Fatalf("agent with allow_read_paths: read_file failed: %s", r.ForLLM)
	}
	if r := without.Tools.Execute(ctx, "read_file", map[string]any{"path": secret}); !r.IsError {
		t.Fatalf("agent without allow_read_paths could read outside its workspace: %s", r.ForLLM)
	}

	out := filepath.Join(shared, "out.txt")
	if r := withPaths.Tools.Execute(ctx, "write_file", map[string]any{"path": out, "content": "x"}); r.IsError {
		t.Fatalf("agent with allow_write_paths: write_file failed: %s", r.ForLLM)
	}
	if r := without.Tools.Execute(ctx, "write_file", map[string]any{"path": filepath.Join(shared, "b.txt"), "content": "x"}); !r.IsError {
		t.Fatalf("agent without allow_write_paths could write outside its workspace: %s", r.ForLLM)
	}

	cmd := map[string]any{"action": "run", "command": "ls " + shared}
	if r := withPaths.Tools.Execute(ctx, "exec", cmd); r.IsError {
		t.Fatalf("agent with allow_read_paths: exec blocked: %s", r.ForLLM)
	}
	if r := without.Tools.Execute(ctx, "exec", cmd); !r.IsError {
		t.Fatalf("agent without allow_read_paths: exec was not blocked: %s", r.ForLLM)
	}
}

func TestParseAgentFrontmatterAllowPaths(t *testing.T) {
	fm, err := parseAgentFrontmatter("AGENT.md", "allow_read_paths: ['^/srv/example(/|$)']\nallow_write_paths: ['^/srv/example/out(/|$)']")
	if err != nil {
		t.Fatal(err)
	}
	if len(fm.AllowReadPaths) != 1 || fm.AllowReadPaths[0] != "^/srv/example(/|$)" {
		t.Fatalf("AllowReadPaths = %v", fm.AllowReadPaths)
	}
	if len(fm.AllowWritePaths) != 1 || fm.AllowWritePaths[0] != "^/srv/example/out(/|$)" {
		t.Fatalf("AllowWritePaths = %v", fm.AllowWritePaths)
	}
}
