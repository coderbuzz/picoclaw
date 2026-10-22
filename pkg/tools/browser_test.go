package tools

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestBrowserProxyBlocksPrivateAndForwardsAllowed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) }))
	defer upstream.Close()

	tool := NewBrowserTool(&config.AgentBrowserConfig{}, t.TempDir())
	real := tool.dial
	allowed := upstream.Listener.Addr().String()
	// test hook: let the one httptest address through, everything else uses the real safe dialer
	dial := func(ctx context.Context, n, a string) (net.Conn, error) {
		if a == allowed {
			return (&net.Dialer{}).DialContext(ctx, n, a)
		}
		return real(ctx, n, a)
	}
	px := httptest.NewServer(&safeProxy{dial: dial})
	defer px.Close()
	pu, _ := neturl.Parse(px.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}

	resp, err := client.Get(upstream.URL)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("allowed forward failed: %v %v", resp, err)
	}
	resp.Body.Close()
	for _, target := range []string{"http://127.0.0.1:1/", "http://169.254.169.254/", "https://10.0.0.1/", "https://127.0.0.1:1/"} {
		resp, err := client.Get(target)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 400 {
				t.Fatalf("%s not blocked: %d", target, resp.StatusCode)
			}
		}
	}
}

func TestBrowserExecute(t *testing.T) {
	ws := t.TempDir()
	tool := NewBrowserTool(&config.AgentBrowserConfig{}, ws)
	var got []string
	tool.run = func(_ context.Context, _ string, args []string) ([]byte, error) {
		got = args
		for _, a := range args {
			if p, ok := strings.CutPrefix(a, "--screenshot="); ok {
				os.WriteFile(p, []byte("png"), 0o644)
			}
		}
		return []byte("<html><script>x()</script><body><h1>Hi</h1></body></html>"), nil
	}
	for _, bad := range []string{"file:///etc/passwd", "ftp://a.com", "http://localhost/", "http://169.254.169.254/", "http://10.1.2.3/", ""} {
		if r := tool.Execute(context.Background(), map[string]any{"url": bad}); !r.IsError {
			t.Errorf("%q not rejected", bad)
		}
	}
	r := tool.Execute(context.Background(), map[string]any{"url": "https://example.com"})
	if r.IsError || r.ForLLM != "Hi" {
		t.Fatalf("text: %+v", r)
	}
	if !strings.Contains(strings.Join(got, " "), "--proxy-bypass-list=<-loopback>") {
		t.Fatalf("flags: %v", got)
	}
	r = tool.Execute(context.Background(), map[string]any{"url": "https://example.com", "action": "screenshot"})
	rel := strings.TrimPrefix(r.ForLLM, "screenshot saved: ")
	if r.IsError || !strings.HasPrefix(rel, "out/browser-") {
		t.Fatalf("screenshot: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(ws, rel)); err != nil {
		t.Fatal(err)
	}
}
