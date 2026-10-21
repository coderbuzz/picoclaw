package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

const testGoogleToken = "ya29.fake-secret-token"

type googleReq struct{ method, uri, auth, body string }

// newTestGoogle serves metadata + every API from one server; reqs records API calls.
func newTestGoogle(t *testing.T, cfg *config.AgentGoogleConfig, handler func(r *http.Request) (int, string)) (*GoogleTool, *[]googleReq, *int32) {
	t.Helper()
	var reqs []googleReq
	var tokenCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if r.Header.Get("Metadata-Flavor") != "Google" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			atomic.AddInt32(&tokenCalls, 1)
			io.WriteString(w, `{"access_token":"`+testGoogleToken+`","expires_in":3600}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		reqs = append(reqs, googleReq{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), string(b)})
		code, body := handler(r)
		w.WriteHeader(code)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	g := NewGoogleTool(cfg)
	g.metadataURL = srv.URL + "/token"
	g.sheetsURL, g.docsURL, g.slidesURL, g.driveURL = srv.URL+"/sheets", srv.URL+"/docs", srv.URL+"/slides", srv.URL+"/drive"
	return g, &reqs, &tokenCalls
}

func okJSON(body string) func(*http.Request) (int, string) {
	return func(*http.Request) (int, string) { return 200, body }
}

func run(g *GoogleTool, args map[string]any) *ToolResult {
	return g.Execute(context.Background(), args)
}

func TestGoogleTokenCachedAndRefreshed(t *testing.T) {
	g, reqs, calls := newTestGoogle(t, &config.AgentGoogleConfig{Files: []string{"*"}}, okJSON(`{"properties":{"title":"T"},"sheets":[]}`))
	for i := 0; i < 3; i++ {
		if r := run(g, map[string]any{"action": "sheets_info", "file": "abc"}); r.IsError {
			t.Fatal(r.ContentForLLM())
		}
	}
	if *calls != 1 {
		t.Fatalf("token fetched %d times, want 1", *calls)
	}
	if (*reqs)[0].auth != "Bearer "+testGoogleToken {
		t.Fatalf("auth header = %q", (*reqs)[0].auth)
	}
	g.expiry = g.expiry.Add(-2 * 3600e9) // near/after expiry -> refresh
	run(g, map[string]any{"action": "sheets_info", "file": "abc"})
	if *calls != 2 {
		t.Fatalf("token fetched %d times after expiry, want 2", *calls)
	}
}

func TestGoogleTokenNeverLeaks(t *testing.T) {
	g, _, _ := newTestGoogle(t, &config.AgentGoogleConfig{Files: []string{"*"}},
		func(*http.Request) (int, string) { return 403, `denied for ` + testGoogleToken })
	r := run(g, map[string]any{"action": "docs_read", "file": "abc"})
	if !r.IsError || strings.Contains(r.ContentForLLM(), testGoogleToken) || !strings.Contains(r.ContentForLLM(), "403") {
		t.Fatalf("unexpected result: %q", r.ContentForLLM())
	}
	g.metadataURL = "http://127.0.0.1:1/token"
	g.token = ""
	if r := run(g, map[string]any{"action": "docs_read", "file": "abc"}); !r.IsError {
		t.Fatal("metadata failure should be an error")
	}
}

func TestGooglePermissions(t *testing.T) {
	g, reqs, _ := newTestGoogle(t, &config.AgentGoogleConfig{Files: []string{"allowed1"}}, okJSON(`{}`))
	r := run(g, map[string]any{"action": "docs_read", "file": "other"})
	if !r.IsError || !strings.Contains(r.ContentForLLM(), "not in this agent's allowed") {
		t.Fatalf("file outside allowlist: %q", r.ContentForLLM())
	}
	for _, a := range []string{"sheets_write", "sheets_append", "docs_append", "docs_replace", "slides_replace"} {
		r := run(g, map[string]any{"action": a, "file": "allowed1", "range": "A1", "values": []any{[]any{"x"}}, "text": "x", "find": "a"})
		if !r.IsError || !strings.Contains(r.ContentForLLM(), "read-only") {
			t.Fatalf("%s with write=false: %q", a, r.ContentForLLM())
		}
	}
	if len(*reqs) != 0 {
		t.Fatalf("denied calls reached the API: %v", *reqs)
	}
	if r := run(g, map[string]any{"action": "docs_read", "file": "https://docs.google.com/document/d/allowed1/edit"}); r.IsError {
		t.Fatal(r.ContentForLLM())
	}
	if r := run(g, map[string]any{"action": "docs_read", "file": "../evil/x"}); !r.IsError {
		t.Fatal("path-like file must be rejected")
	}
}

func TestGoogleFileIDFromURL(t *testing.T) {
	g := NewGoogleTool(&config.AgentGoogleConfig{Files: []string{"*"}})
	for in, want := range map[string]string{
		"https://docs.google.com/spreadsheets/d/1AbC_-d/edit#gid=0": "1AbC_-d",
		"https://docs.google.com/presentation/d/XyZ9/edit":          "XyZ9",
		"  1AbC_-d ": "1AbC_-d",
	} {
		if got, err := g.fileID(in); err != nil || got != want {
			t.Errorf("fileID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := g.fileID(""); err == nil {
		t.Error("empty file must fail")
	}
}

func TestGoogleActionsBuildRequests(t *testing.T) {
	g, reqs, _ := newTestGoogle(t, &config.AgentGoogleConfig{Files: []string{"*"}, Write: true},
		func(r *http.Request) (int, string) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/sheets/f1/values/"):
				return 200, `{"range":"S!A1:B1","values":[["a",1]]}`
			case r.URL.Path == "/sheets/f1":
				return 200, `{"properties":{"title":"Budget"},"sheets":[{"properties":{"title":"Tab1","gridProperties":{"rowCount":10,"columnCount":4}}}]}`
			case r.URL.Path == "/docs/f1":
				return 200, `{"title":"Doc","body":{"content":[{"paragraph":{"elements":[{"textRun":{"content":"Hello\n"}}]}},{"table":{"tableRows":[{"tableCells":[{"content":[{"paragraph":{"elements":[{"textRun":{"content":"cell\n"}}]}}]}]}]}}]}}`
			case r.URL.Path == "/slides/f1":
				return 200, `{"title":"Deck","slides":[{"pageElements":[{"shape":{"text":{"textElements":[{"textRun":{"content":"Slide one\n"}}]}}}]}]}`
			case strings.HasSuffix(r.URL.Path, ":batchUpdate"):
				return 200, `{"replies":[{"replaceAllText":{"occurrencesChanged":3}}]}`
			case r.URL.Path == "/drive":
				return 200, `{"files":[{"id":"f1","name":"Budget","mimeType":"application/vnd.google-apps.spreadsheet"}]}`
			}
			return 200, `{}`
		})
	vals := []any{[]any{"a", "=1+1"}}
	cases := []struct {
		args         map[string]any
		method, path string
		body, out    string
	}{
		{map[string]any{"action": "sheets_info", "file": "f1"}, "GET", "/sheets/f1?", "", "Budget"},
		{map[string]any{"action": "sheets_read", "file": "f1", "range": "S!A1:B1"}, "GET", "/sheets/f1/values/S%21A1:B1", "", `[["a",1]]`},
		{map[string]any{"action": "sheets_write", "file": "f1", "range": "S!A1", "values": vals}, "PUT", "/sheets/f1/values/S%21A1?valueInputOption=USER_ENTERED", `{"values":[["a","=1+1"]]}`, "1 rows"},
		{map[string]any{"action": "sheets_append", "file": "f1", "range": "S", "values": vals}, "POST", "/sheets/f1/values/S:append?valueInputOption=USER_ENTERED", `{"values":[["a","=1+1"]]}`, "1 rows"},
		{map[string]any{"action": "docs_read", "file": "f1"}, "GET", "/docs/f1", "", "Hello\ncell"},
		{map[string]any{"action": "docs_append", "file": "f1", "text": "tail"}, "POST", "/docs/f1:batchUpdate", `{"requests":[{"insertText":{"endOfSegmentLocation":{},"text":"tail"}}]}`, "appended"},
		{map[string]any{"action": "docs_replace", "file": "f1", "find": "a", "replace": "b"}, "POST", "/docs/f1:batchUpdate", `{"requests":[{"replaceAllText":{"containsText":{"matchCase":true,"text":"a"},"replaceText":"b"}}]}`, "3 occurrences"},
		{map[string]any{"action": "slides_read", "file": "f1"}, "GET", "/slides/f1", "", "--- Slide 1 ---\nSlide one"},
		{map[string]any{"action": "slides_replace", "file": "f1", "find": "a", "replace": "b"}, "POST", "/slides/f1:batchUpdate", `{"requests":[{"replaceAllText":{"containsText":{"matchCase":true,"text":"a"},"replaceText":"b"}}]}`, "3 occurrences"},
		{map[string]any{"action": "drive_list"}, "GET", "/drive?pageSize=200", "", "f1\tBudget"},
	}
	for _, c := range cases {
		*reqs = nil
		r := run(g, c.args)
		if r.IsError || !strings.Contains(r.ContentForLLM(), c.out) {
			t.Errorf("%v: result %q, want substring %q", c.args["action"], r.ContentForLLM(), c.out)
			continue
		}
		got := (*reqs)[0]
		if got.method != c.method || !strings.HasPrefix(got.uri, c.path) {
			t.Errorf("%v: %s %s, want %s %s", c.args["action"], got.method, got.uri, c.method, c.path)
		}
		if c.body != "" {
			var a, b any
			json.Unmarshal([]byte(got.body), &a)
			json.Unmarshal([]byte(c.body), &b)
			if ab, _ := json.Marshal(a); string(ab) != func() string { bb, _ := json.Marshal(b); return string(bb) }() {
				t.Errorf("%v: body %s, want %s", c.args["action"], got.body, c.body)
			}
		}
	}
}

func TestGoogleDriveListFiltered(t *testing.T) {
	g, _, _ := newTestGoogle(t, &config.AgentGoogleConfig{Files: []string{"keep"}},
		okJSON(`{"files":[{"id":"keep","name":"A","mimeType":"m"},{"id":"drop","name":"B","mimeType":"m"}]}`))
	out := run(g, map[string]any{"action": "drive_list"}).ContentForLLM()
	if !strings.Contains(out, "keep") || strings.Contains(out, "drop") {
		t.Fatalf("drive_list not filtered: %q", out)
	}
}

func TestGoogleOutputTruncated(t *testing.T) {
	g, _, _ := newTestGoogle(t, &config.AgentGoogleConfig{Files: []string{"*"}},
		okJSON(`{"title":"D","body":{"content":[{"paragraph":{"elements":[{"textRun":{"content":"`+strings.Repeat("é", googleMaxOutput)+`"}}]}}]}}`))
	out := run(g, map[string]any{"action": "docs_read", "file": "f1"}).ContentForLLM()
	if !strings.Contains(out, "[output truncated") || len(out) > googleMaxOutput+200 {
		t.Fatalf("not truncated: len=%d", len(out))
	}
}

func TestGoogleToolNotRegisteredWithoutConfig(t *testing.T) {
	// registration is gated on agents.list[].google being set (see pkg/agent/instance.go)
	var a config.AgentConfig
	if err := json.Unmarshal([]byte(`{"id":"x"}`), &a); err != nil || a.Google != nil {
		t.Fatalf("google must default to nil: %+v %v", a.Google, err)
	}
	if err := json.Unmarshal([]byte(`{"id":"x","google":{"files":["*"],"write":true}}`), &a); err != nil || a.Google == nil || !a.Google.Write {
		t.Fatalf("google config not parsed: %+v %v", a.Google, err)
	}
}
