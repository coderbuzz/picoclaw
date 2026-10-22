package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	integrationtools "github.com/sipeed/picoclaw/pkg/tools/integration"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const browserMaxOutput = 20000

// One Chromium at a time: the VM has under 1 GB of RAM.
var browserMu sync.Mutex

// BrowserTool renders a page with headless Chromium on the host (outside the agent's network-less sandbox).
// All Chromium traffic goes through a loopback proxy whose dialer refuses private/local targets, so
// redirects and subresources cannot reach internal services.
type BrowserTool struct {
	command   string
	timeout   time.Duration
	workspace string

	// overridable for tests
	run  func(ctx context.Context, command string, args []string) ([]byte, error)
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func NewBrowserTool(cfg *config.AgentBrowserConfig, workspace string) *BrowserTool {
	t := &BrowserTool{command: cfg.Command, timeout: time.Duration(cfg.TimeoutSeconds) * time.Second, workspace: workspace,
		run:  runBrowserCommand,
		dial: utils.NewSafeDialContext(&net.Dialer{Timeout: 15 * time.Second}, nil, nil)}
	if t.command == "" {
		t.command = "chromium"
	}
	if t.timeout <= 0 {
		t.timeout = 45 * time.Second
	}
	return t
}

func (t *BrowserTool) Name() string { return "browser" }

func (t *BrowserTool) Description() string {
	return "Open a public http/https page in a real headless browser (runs JavaScript) and return its text, or save a screenshot " +
		"into the workspace (out/browser-*.png, then use send_file or load_image). Private and local addresses are blocked."
}

func (t *BrowserTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":    map[string]any{"type": "string", "description": "http or https URL."},
			"action": map[string]any{"type": "string", "enum": []string{"text", "screenshot"}, "description": "text (default) or screenshot."},
		},
		"required": []string{"url"},
	}
}

func (t *BrowserTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	raw, _ := args["url"].(string)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ErrorResult("browser: url must be a valid http/https URL")
	}
	if utils.IsObviousPrivateHost(u.Hostname(), nil, nil) {
		return ErrorResult("browser: private or local network hosts are not allowed")
	}
	action, _ := args["action"].(string)
	if action == "" {
		action = "text"
	}
	if action != "text" && action != "screenshot" {
		return ErrorResult("browser: unknown action " + action)
	}

	browserMu.Lock()
	defer browserMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return ErrorResult("browser: " + err.Error())
	}
	srv := &http.Server{Handler: &safeProxy{dial: t.dial}}
	go srv.Serve(ln)
	defer srv.Close()

	profile, err := os.MkdirTemp("", "picoclaw-browser-")
	if err != nil {
		return ErrorResult("browser: " + err.Error())
	}
	defer os.RemoveAll(profile)

	flags := []string{
		"--headless", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
		"--user-data-dir=" + profile,
		"--proxy-server=http://" + ln.Addr().String(),
		"--proxy-bypass-list=<-loopback>",
		`--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1`,
	}
	rel := ""
	if action == "screenshot" {
		rel = filepath.Join("out", fmt.Sprintf("browser-%d.png", time.Now().Unix()))
		if err := os.MkdirAll(filepath.Join(t.workspace, "out"), 0o755); err != nil {
			return ErrorResult("browser: " + err.Error())
		}
		flags = append(flags, "--screenshot="+filepath.Join(t.workspace, rel), "--window-size=1280,2000")
	} else {
		flags = append(flags, "--dump-dom")
	}
	out, err := t.run(ctx, t.command, append(flags, u.String()))
	if err != nil {
		return ErrorResult(fmt.Sprintf("browser: %v", err))
	}
	if action == "screenshot" {
		return NewToolResult("screenshot saved: " + filepath.ToSlash(rel))
	}
	text := integrationtools.ExtractText(string(out))
	if len(text) > browserMaxOutput {
		text = text[:browserMaxOutput]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		text += "\n[truncated]"
	}
	return NewToolResult(text)
}

func runBrowserCommand(ctx context.Context, command string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	prepareCommandForTermination(cmd)
	cmd.Cancel = func() error { return terminateProcessTree(cmd) }
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("timed out: %w", ctx.Err())
		}
		msg := stderr.String()
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("%s failed: %v %s", command, err, strings.TrimSpace(msg))
	}
	return stdout.Bytes(), nil
}

// safeProxy is a minimal forward proxy (CONNECT and plain http) that dials only through dial.
type safeProxy struct {
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (p *safeProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		up, err := p.dial(r.Context(), "tcp", r.Host)
		if err != nil {
			http.Error(w, "blocked: "+err.Error(), http.StatusForbidden)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			up.Close()
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		c, _, err := hj.Hijack()
		if err != nil {
			up.Close()
			return
		}
		c.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() { io.Copy(up, c); up.Close() }()
		io.Copy(c, up)
		c.Close()
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "proxy only", http.StatusBadRequest)
		return
	}
	r.RequestURI = ""
	r.Header.Del("Proxy-Connection")
	rt := &http.Transport{DialContext: p.dial, DisableKeepAlives: true}
	resp, err := rt.RoundTrip(r)
	if err != nil {
		http.Error(w, "blocked: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
