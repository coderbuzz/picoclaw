//go:build linux

package isolation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestBuildLinuxBwrapArgs_IncludesNamespaceFlagsAndExec(t *testing.T) {
	root := t.TempDir()
	binaryDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binaryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(binaryDir, "tool")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := BuildLinuxMountPlan(root, []config.ExposePath{{Source: binaryDir, Target: binaryDir, Mode: "ro"}})
	args, err := buildLinuxBwrapArgs(binaryPath, binaryPath, []string{binaryPath, "--flag"}, root, plan)
	if err != nil {
		t.Fatalf("buildLinuxBwrapArgs() error = %v", err)
	}
	hasNet := false
	hasIPC := false
	hasExec := false
	for i := range args {
		switch args[i] {
		case "--unshare-net":
			hasNet = true
		case "--unshare-ipc":
			hasIPC = true
		case "--":
			if i+1 < len(args) && args[i+1] == binaryPath {
				hasExec = true
			}
		}
	}
	if hasNet {
		t.Fatalf("bwrap args should not unshare net by default: %v", args)
	}
	if !hasIPC || !hasExec {
		t.Fatalf("bwrap args missing required items: %v", args)
	}
}

func TestResolveLinuxWorkingDir_ResolvesRelativeDir(t *testing.T) {
	cwd := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if chdirErr := os.Chdir(previous); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()
	if chdirErr := os.Chdir(cwd); chdirErr != nil {
		t.Fatal(chdirErr)
	}

	resolvedDir, execDir, err := resolveLinuxWorkingDir("./hooks", "./hook.sh")
	if err != nil {
		t.Fatalf("resolveLinuxWorkingDir() error = %v", err)
	}
	want := filepath.Join(cwd, "hooks")
	if resolvedDir != want || execDir != want {
		t.Fatalf("resolveLinuxWorkingDir() = (%q, %q), want (%q, %q)", resolvedDir, execDir, want, want)
	}
}

func TestResolveLinuxCommandPath_UsesExecDirForRelativeCommand(t *testing.T) {
	execDir := filepath.Join(t.TempDir(), "hooks")
	got, err := resolveLinuxCommandPath("./hook.sh", execDir)
	if err != nil {
		t.Fatalf("resolveLinuxCommandPath() error = %v", err)
	}
	want := filepath.Join(execDir, "hook.sh")
	if got != want {
		t.Fatalf("resolveLinuxCommandPath() = %q, want %q", got, want)
	}
}

func TestBuildLinuxBwrapArgs_UsesResolvedPathForRelativeCommand(t *testing.T) {
	root := t.TempDir()
	execDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(execDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedPath := filepath.Join(execDir, "hook.sh")
	if err := os.WriteFile(resolvedPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := []MountRule{
		{Source: execDir, Target: execDir, Mode: "rw"},
		{Source: resolvedPath, Target: resolvedPath, Mode: "ro"},
	}
	args, err := buildLinuxBwrapArgs("./hook.sh", resolvedPath, []string{"./hook.sh"}, execDir, plan)
	if err != nil {
		t.Fatalf("buildLinuxBwrapArgs() error = %v", err)
	}
	hasExecDir := false
	for _, arg := range args {
		if arg == execDir {
			hasExecDir = true
			break
		}
	}
	if !hasExecDir {
		t.Fatalf("buildLinuxBwrapArgs() missing resolved chdir: %v", args)
	}
	for i := range args {
		if args[i] == "--" {
			if i+1 >= len(args) || args[i+1] != resolvedPath {
				t.Fatalf("buildLinuxBwrapArgs() exec path = %v, want %q after --", args, resolvedPath)
			}
			return
		}
	}
	t.Fatalf("buildLinuxBwrapArgs() missing exec delimiter: %v", args)
}

func TestAppendLinuxArgumentMounts_AddsAbsoluteArgumentPaths(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input.txt")
	if err := os.WriteFile(input, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "out", "result.txt")
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		t.Fatal(err)
	}

	plan := appendLinuxArgumentMounts(nil, []string{input, "--output=" + output})
	if len(plan) != 2 {
		t.Fatalf("appendLinuxArgumentMounts() len = %d, want 2", len(plan))
	}
	if plan[0].Source != input || plan[0].Mode != "ro" {
		t.Fatalf("appendLinuxArgumentMounts()[0] = %+v, want source=%q mode=ro", plan[0], input)
	}
	if plan[1].Source != filepath.Dir(output) || plan[1].Mode != "rw" {
		t.Fatalf("appendLinuxArgumentMounts()[1] = %+v, want source=%q mode=rw", plan[1], filepath.Dir(output))
	}
}

func fakeBwrapOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bwrap"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestPrepareAgentCommandMountPlan(t *testing.T) {
	fakeBwrapOnPath(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PICOCLAW_HOME", filepath.Join(home, ".picoclaw"))
	ws := filepath.Join(t.TempDir(), "ws-demo")
	extraRO, extraRW := t.TempDir(), t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", "-c", "cat "+outside)
	cmd.Dir = ws
	agent := &config.AgentIsolationConfig{
		Enabled: true,
		ExposePaths: []config.ExposePath{
			{Source: extraRO, Mode: "ro"},
			{Source: extraRW, Mode: "rw"},
		},
	}
	if err := prepareAgentCommand(cmd, agent, ws); err != nil {
		t.Fatalf("prepareAgentCommand() error = %v", err)
	}
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{
		"--bind " + ws + " " + ws,
		"--ro-bind " + extraRO + " " + extraRO,
		"--bind " + extraRW + " " + extraRW,
		"--unshare-net", "--unshare-pid",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %s", want, joined)
		}
	}
	for _, bad := range []string{"bind " + outside, filepath.Join(home, ".picoclaw"), "--bind " + home + " "} {
		if strings.Contains(joined, bad) {
			t.Errorf("args must not contain %q: %s", bad, joined)
		}
	}
	env := strings.Join(cmd.Env, "\n")
	if !strings.Contains(env, "HOME="+filepath.Join(ws, ".sandbox", "home")) {
		t.Errorf("HOME not redirected into workspace: %s", env)
	}
	if _, err := os.Stat(filepath.Join(ws, ".sandbox", "tmp")); err != nil {
		t.Errorf("sandbox tmp not created: %v", err)
	}

	cmd = exec.Command("/bin/sh", "-c", "true")
	cmd.Dir = ws
	agent.Network = true
	if err := prepareAgentCommand(cmd, agent, ws); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "--unshare-net") {
		t.Error("network=true must not unshare net")
	}
}

func TestPrepareAgentCommandFailsClosed(t *testing.T) {
	ws := t.TempDir()
	agent := &config.AgentIsolationConfig{Enabled: true}

	t.Setenv("PATH", t.TempDir())
	if err := prepareAgentCommand(exec.Command("/bin/sh", "-c", "true"), agent, ws); err == nil {
		t.Error("missing bwrap must be an error")
	}

	fakeBwrapOnPath(t)
	cmd := exec.Command("/bin/sh", "-c", "true")
	cmd.Dir = t.TempDir() // outside the plan
	if err := prepareAgentCommand(cmd, agent, ws); err == nil {
		t.Error("cwd outside the plan must be an error")
	}
}

func TestAgentIsolationIntegration(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	ws := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(script string) error {
		cmd := exec.Command("sh", "-c", script)
		cmd.Dir = ws
		if err := StartForAgent(cmd, &config.AgentIsolationConfig{Enabled: true}, ws); err != nil {
			return err
		}
		return cmd.Wait()
	}
	if err := run("echo ok > " + filepath.Join(ws, "out")); err != nil {
		t.Skipf("bwrap unusable here (user namespaces?): %v", err)
	}
	if err := run("cat " + secret); err == nil {
		t.Error("reading a file outside the plan must fail")
	}
	// Exit 0 only if a non-loopback interface is visible.
	if err := run("grep -v '^ *lo:' /proc/net/dev | grep -q :"); err == nil {
		t.Error("network must be unshared")
	}
}
