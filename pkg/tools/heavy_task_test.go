package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// fakeHeavyTask writes a script that prints argv, cwd and the remote-only env, saves a file into HEAVY_TASK_COPY_TO,
// and exits with $RC.
func fakeHeavyTask(t *testing.T) (cmd, ws string) {
	t.Helper()
	dir := t.TempDir()
	ws = filepath.Join(dir, "ws")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd = filepath.Join(dir, "heavy-task")
	body := `for a in "$@"; do printf 'arg=[%s]\n' "$a"; done; pwd; echo "remote=$HEAVY_TASK_REMOTE_ONLY"
mkdir -p "$HEAVY_TASK_COPY_TO" && echo hi > "$HEAVY_TASK_COPY_TO/hasil.txt"; exit ${RC:-0}`
	if err := os.WriteFile(cmd, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return cmd, ws
}

func TestHeavyTaskArgsEnvAndFiles(t *testing.T) {
	cmd, ws := fakeHeavyTask(t)
	tool := NewHeavyTaskTool(&config.AgentHeavyTaskConfig{Command: cmd, Models: []string{"p/haiku", "p/sonnet"}, TimeoutMinutes: 7}, ws)

	r := tool.Execute(context.Background(), map[string]any{"instruction": "--rm everything"})
	if r.IsError {
		t.Fatal(r.ForLLM)
	}
	real, _ := filepath.EvalSymlinks(ws)
	for _, w := range []string{"arg=[--model]\narg=[p/haiku]\narg=[--timeout]\narg=[7]\narg=[--]\narg=[--rm everything]", real, "remote=1", "files: out/heavy-"} {
		if !strings.Contains(r.ForLLM, w) {
			t.Errorf("missing %q:\n%s", w, r.ForLLM)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(ws, "out", "heavy-*", "hasil.txt")); len(m) != 1 {
		t.Errorf("job files not in workspace: %v", m)
	}

	if r := tool.Execute(context.Background(), map[string]any{"instruction": "x", "model": "p/sonnet"}); r.IsError || !strings.Contains(r.ForLLM, "arg=[p/sonnet]") {
		t.Errorf("allowed model: %s", r.ForLLM)
	}
	for _, bad := range []map[string]any{{"instruction": "x", "model": "p/opus"}, {"instruction": "  "}, {}} {
		if r := tool.Execute(context.Background(), bad); !r.IsError {
			t.Errorf("%v not rejected", bad)
		}
	}

	t.Setenv("RC", "69")
	if r := tool.Execute(context.Background(), map[string]any{"instruction": "x"}); !r.IsError || !strings.Contains(r.ForLLM, "exit code 69") {
		t.Errorf("failure not reported: %+v", r)
	}
}
