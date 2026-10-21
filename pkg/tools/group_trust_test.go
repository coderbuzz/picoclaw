package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

// fakeGroupAgents writes a script that prints its argv and cwd (or runs body instead).
func fakeGroupAgents(t *testing.T, body string) (cmd, ws string) {
	t.Helper()
	dir := t.TempDir()
	ws = filepath.Join(dir, "ws")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd = filepath.Join(dir, "group-agents")
	if body == "" {
		body = `printf 'argc=%s\n' "$#"; for a in "$@"; do printf 'arg=[%s]\n' "$a"; done; pwd`
	}
	if err := os.WriteFile(cmd, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return cmd, ws
}

func TestGroupTrustArgsAndCwd(t *testing.T) {
	cmd, ws := fakeGroupAgents(t, "")
	tool := NewGroupTrustTool(&config.AgentGroupTrustConfig{Profile: "club", Command: cmd}, ws)
	for _, tc := range []struct {
		args map[string]any
		want []string
	}{
		{map[string]any{"action": "add", "member": "6281200000001"}, []string{"argc=4", "[trust]", "[club]", "[add]", "[6281200000001]"}},
		{map[string]any{"action": "add", "member": "900000000000001@lid", "name": "Budi S"}, []string{"argc=5", "[900000000000001]", "[Budi S]"}},
		{map[string]any{"action": "remove", "member": " +6281200000001@s.whatsapp.net "}, []string{"[remove]", "[6281200000001]"}},
		{map[string]any{"action": "list", "member": "ignored"}, []string{"argc=3", "[list]"}},
	} {
		res := tool.Execute(context.Background(), tc.args)
		if res.IsError {
			t.Fatalf("%v: %s", tc.args, res.ForLLM)
		}
		for _, w := range tc.want {
			if !strings.Contains(res.ForLLM, w) {
				t.Errorf("%v: output missing %q:\n%s", tc.args, w, res.ForLLM)
			}
		}
		if real, _ := filepath.EvalSymlinks(ws); !strings.Contains(res.ForLLM, real) {
			t.Errorf("cwd not the agent workspace:\n%s", res.ForLLM)
		}
	}
}

func TestGroupTrustRejectsBadInput(t *testing.T) {
	cmd, ws := fakeGroupAgents(t, "touch ran")
	tool := NewGroupTrustTool(&config.AgentGroupTrustConfig{Profile: "club", Command: cmd}, ws)
	for _, args := range []map[string]any{
		{"action": "add", "member": "1;rm"},
		{"action": "add", "member": "$(x)"},
		{"action": "add", "member": "628120000000a"},
		{"action": "add", "member": "12345"},
		{"action": "add", "member": "628120000000123456789"},
		{"action": "add", "member": ""},
		{"action": "remove"},
		{"action": "add", "member": "6281200000001", "name": "--profile"},
		{"action": "add", "member": "6281200000001", "name": "a\nb"},
		{"action": "nuke", "member": "6281200000001"},
		{},
	} {
		if res := tool.Execute(context.Background(), args); !res.IsError {
			t.Errorf("%v accepted", args)
		}
	}
	if _, err := os.Stat(filepath.Join(ws, "ran")); err == nil {
		t.Fatal("command ran for rejected input")
	}
}

func TestGroupTrustExitCodeAndTimeout(t *testing.T) {
	cmd, ws := fakeGroupAgents(t, "echo ditolak >&2; exit 3")
	tool := NewGroupTrustTool(&config.AgentGroupTrustConfig{Profile: "club", Command: cmd}, ws)
	res := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if !res.IsError || !strings.Contains(res.ForLLM, "exit code 3") || !strings.Contains(res.ForLLM, "ditolak") {
		t.Fatalf("got %+v", res)
	}

	cmd, ws = fakeGroupAgents(t, "sleep 5")
	tool = NewGroupTrustTool(&config.AgentGroupTrustConfig{Profile: "club", Command: cmd}, ws)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if res := tool.Execute(ctx, map[string]any{"action": "list"}); !res.IsError {
		t.Fatalf("slow command not stopped: %+v", res)
	}
}
