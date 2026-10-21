package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	googleMetadataURL = "http://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/token"
	googleMaxOutput   = 20000
	googleMaxResponse = 5 << 20
)

var (
	googleURLID = regexp.MustCompile(`/d/([A-Za-z0-9_-]+)`)
	googleID    = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

	googleWriteActions = map[string]bool{
		"sheets_write": true, "sheets_append": true,
		"docs_append": true, "docs_replace": true, "slides_replace": true,
	}
)

// GoogleTool reads and edits Sheets, Docs, Slides and lists Drive files with the VM's service account
// (token from the GCE metadata server, no key file). Per-agent permission: file allowlist + write flag.
type GoogleTool struct {
	files  map[string]bool // nil = any file
	write  bool
	client *http.Client

	// base URLs; overridable for tests
	metadataURL, sheetsURL, docsURL, slidesURL, driveURL string

	mu     sync.Mutex
	token  string
	expiry time.Time
}

func NewGoogleTool(cfg *config.AgentGoogleConfig) *GoogleTool {
	t := &GoogleTool{
		write:       cfg.Write,
		client:      &http.Client{Timeout: 30 * time.Second},
		metadataURL: googleMetadataURL,
		sheetsURL:   "https://sheets.googleapis.com/v4/spreadsheets",
		docsURL:     "https://docs.googleapis.com/v1/documents",
		slidesURL:   "https://slides.googleapis.com/v1/presentations",
		driveURL:    "https://www.googleapis.com/drive/v3/files",
	}
	t.files = map[string]bool{}
	for _, f := range cfg.Files {
		if f = strings.TrimSpace(f); f == "*" {
			t.files = nil
			break
		} else if f != "" {
			t.files[f] = true
		}
	}
	return t
}

func (t *GoogleTool) Name() string { return "google" }

func (t *GoogleTool) Description() string {
	return "Read and edit Google Sheets, Docs and Slides and list Drive files shared with the bot's service account. " +
		"`file` is a file ID or a docs.google.com URL. Cannot create new files (ask the owner to create one and share it with the bot). " +
		"Actions: sheets_info, sheets_read (range), sheets_write (range, values), sheets_append (range or tab name, values), " +
		"docs_read, docs_append (text), docs_replace (find, replace), slides_read, slides_replace (find, replace), drive_list. " +
		"Write actions may be disabled for this agent. Long output is truncated."
}

func (t *GoogleTool) Parameters() map[string]any {
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{
					"sheets_info", "sheets_read", "sheets_write", "sheets_append",
					"docs_read", "docs_append", "docs_replace",
					"slides_read", "slides_replace", "drive_list",
				},
			},
			"file":  str("File ID or docs.google.com URL (not needed for drive_list)."),
			"range": str("A1 range such as Sheet1!A1:C10, or a tab name for sheets_append."),
			"values": map[string]any{
				"type":        "array",
				"description": "Rows to write: array of rows, each an array of cell strings (formulas allowed, e.g. \"=SUM(A1:A3)\").",
				"items":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"text":    str("Text to append at the end of the document."),
			"find":    str("Text to find (case-sensitive) for docs_replace/slides_replace."),
			"replace": str("Replacement text (may be empty)."),
		},
		"required": []string{"action"},
	}
}

func (t *GoogleTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	action, _ := args["action"].(string)
	if googleWriteActions[action] && !t.write {
		return ErrorResult("google: this agent has read-only access; " + action + " is not allowed")
	}
	str := func(k string) string { s, _ := args[k].(string); return s }

	var id string
	if action != "drive_list" {
		var err error
		if id, err = t.fileID(str("file")); err != nil {
			return ErrorResult(err.Error())
		}
	}

	var out string
	var err error
	switch action {
	case "sheets_info":
		out, err = t.sheetsInfo(ctx, id)
	case "sheets_read":
		out, err = t.sheetsRead(ctx, id, str("range"))
	case "sheets_write", "sheets_append":
		out, err = t.sheetsWrite(ctx, action, id, str("range"), args["values"])
	case "docs_read":
		out, err = t.docsRead(ctx, id)
	case "docs_append":
		out, err = t.batchUpdate(ctx, t.docsURL, id, map[string]any{"insertText": map[string]any{
			"text": str("text"), "endOfSegmentLocation": map[string]any{},
		}}, "text appended")
	case "docs_replace":
		out, err = t.replaceAll(ctx, t.docsURL, id, str("find"), str("replace"))
	case "slides_read":
		out, err = t.slidesRead(ctx, id)
	case "slides_replace":
		out, err = t.replaceAll(ctx, t.slidesURL, id, str("find"), str("replace"))
	case "drive_list":
		out, err = t.driveList(ctx)
	default:
		return ErrorResult("google: unknown action " + action)
	}
	if err != nil {
		return ErrorResult("google: " + err.Error())
	}
	return NewToolResult(truncateGoogle(out))
}

// fileID extracts the ID from a docs.google.com/drive URL (or accepts a bare ID) and checks the agent's allowlist.
func (t *GoogleTool) fileID(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	id := ref
	if m := googleURLID.FindStringSubmatch(ref); m != nil {
		id = m[1]
	}
	if !googleID.MatchString(id) {
		return "", fmt.Errorf("file must be a file ID or a docs.google.com URL")
	}
	if t.files != nil && !t.files[id] {
		return "", fmt.Errorf("file %s is not in this agent's allowed Google files", id)
	}
	return id, nil
}

func truncateGoogle(s string) string {
	if len(s) <= googleMaxOutput {
		return s
	}
	cut := googleMaxOutput
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s\n[output truncated: showing %d of %d bytes]", s[:cut], cut, len(s))
}

func (t *GoogleTool) accessToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Until(t.expiry) > time.Minute {
		return t.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.metadataURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach the metadata server (is this a GCE VM with a service account?)")
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK ||
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&tok) != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("metadata server returned no access token (status %d)", resp.StatusCode)
	}
	t.token, t.expiry = tok.AccessToken, time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second)
	return t.token, nil
}

// call sends a JSON request to the Google API and returns the raw response body.
func (t *GoogleTool) call(ctx context.Context, method, u string, body any) ([]byte, error) {
	tok, err := t.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %s", strings.ReplaceAll(err.Error(), tok, "[redacted]"))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, googleMaxResponse))
	if resp.StatusCode/100 != 2 {
		msg := strings.ReplaceAll(string(data), tok, "[redacted]")
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return nil, fmt.Errorf("API status %d: %s", resp.StatusCode, msg)
	}
	return data, nil
}

func (t *GoogleTool) getJSON(ctx context.Context, u string, v any) error {
	data, err := t.call(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (t *GoogleTool) sheetsInfo(ctx context.Context, id string) (string, error) {
	var r struct {
		Properties struct{ Title string }
		Sheets     []struct {
			Properties struct {
				Title          string
				GridProperties struct{ RowCount, ColumnCount int }
			}
		}
	}
	u := t.sheetsURL + "/" + id + "?fields=properties.title,sheets.properties(title,gridProperties)"
	if err := t.getJSON(ctx, u, &r); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\nTabs:\n", r.Properties.Title)
	for _, s := range r.Sheets {
		g := s.Properties.GridProperties
		fmt.Fprintf(&b, "- %s (%d rows x %d columns)\n", s.Properties.Title, g.RowCount, g.ColumnCount)
	}
	return b.String(), nil
}

func (t *GoogleTool) sheetsRead(ctx context.Context, id, rng string) (string, error) {
	if rng == "" {
		return "", fmt.Errorf("range is required")
	}
	var r struct {
		Range  string
		Values [][]any
	}
	if err := t.getJSON(ctx, t.sheetsURL+"/"+id+"/values/"+url.PathEscape(rng), &r); err != nil {
		return "", err
	}
	b, _ := json.Marshal(r.Values)
	return fmt.Sprintf("Range %s:\n%s", r.Range, b), nil
}

func (t *GoogleTool) sheetsWrite(ctx context.Context, action, id, rng string, values any) (string, error) {
	rows, ok := values.([]any)
	if rng == "" || !ok || len(rows) == 0 {
		return "", fmt.Errorf("range and non-empty values (array of rows) are required")
	}
	for _, r := range rows {
		if _, ok := r.([]any); !ok {
			return "", fmt.Errorf("values must be an array of rows, each an array of cells")
		}
	}
	u := t.sheetsURL + "/" + id + "/values/" + url.PathEscape(rng)
	method := http.MethodPut
	if action == "sheets_append" {
		u += ":append"
		method = http.MethodPost
	}
	u += "?valueInputOption=USER_ENTERED"
	if _, err := t.call(ctx, method, u, map[string]any{"values": rows}); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d rows written (%s)", len(rows), action), nil
}

func (t *GoogleTool) batchUpdate(ctx context.Context, base, id string, req map[string]any, done string) (string, error) {
	_, err := t.call(ctx, http.MethodPost, base+"/"+id+":batchUpdate", map[string]any{"requests": []any{req}})
	if err != nil {
		return "", err
	}
	return done, nil
}

func (t *GoogleTool) replaceAll(ctx context.Context, base, id, find, repl string) (string, error) {
	if find == "" {
		return "", fmt.Errorf("find is required")
	}
	data, err := t.call(ctx, http.MethodPost, base+"/"+id+":batchUpdate", map[string]any{"requests": []any{
		map[string]any{"replaceAllText": map[string]any{
			"containsText": map[string]any{"text": find, "matchCase": true}, "replaceText": repl,
		}},
	}})
	if err != nil {
		return "", err
	}
	var r struct {
		Replies []struct {
			ReplaceAllText struct{ OccurrencesChanged int }
		}
	}
	_ = json.Unmarshal(data, &r)
	n := 0
	if len(r.Replies) > 0 {
		n = r.Replies[0].ReplaceAllText.OccurrencesChanged
	}
	return fmt.Sprintf("%d occurrences replaced", n), nil
}

type googleTextRun struct{ Content string }

type googleParagraphEl struct{ TextRun *googleTextRun }

type googleStructEl struct {
	Paragraph *struct{ Elements []googleParagraphEl }
	Table     *struct {
		TableRows []struct {
			TableCells []struct{ Content []googleStructEl }
		}
	}
}

// text flattens paragraphs (and table cells) in order.
func (e googleStructEl) text(b *strings.Builder) {
	if e.Paragraph != nil {
		for _, pe := range e.Paragraph.Elements {
			if pe.TextRun != nil {
				b.WriteString(pe.TextRun.Content)
			}
		}
	}
	if e.Table != nil {
		for _, row := range e.Table.TableRows {
			for _, c := range row.TableCells {
				for _, ce := range c.Content {
					ce.text(b)
				}
			}
		}
	}
}

func (t *GoogleTool) docsRead(ctx context.Context, id string) (string, error) {
	var r struct {
		Title string
		Body  struct{ Content []googleStructEl }
	}
	if err := t.getJSON(ctx, t.docsURL+"/"+id, &r); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\n\n", r.Title)
	for _, e := range r.Body.Content {
		e.text(&b)
	}
	return b.String(), nil
}

func (t *GoogleTool) slidesRead(ctx context.Context, id string) (string, error) {
	type textEl struct{ TextRun *googleTextRun }
	type text struct{ TextElements []textEl }
	type pageEl struct {
		Shape *struct{ Text *text }
		Table *struct {
			TableRows []struct{ TableCells []struct{ Text *text } }
		}
	}
	var r struct {
		Title  string
		Slides []struct{ PageElements []pageEl }
	}
	if err := t.getJSON(ctx, t.slidesURL+"/"+id, &r); err != nil {
		return "", err
	}
	var b strings.Builder
	write := func(tx *text) {
		if tx == nil {
			return
		}
		for _, te := range tx.TextElements {
			if te.TextRun != nil {
				b.WriteString(te.TextRun.Content)
			}
		}
	}
	fmt.Fprintf(&b, "Title: %s\n", r.Title)
	for i, s := range r.Slides {
		fmt.Fprintf(&b, "\n--- Slide %d ---\n", i+1)
		for _, pe := range s.PageElements {
			if pe.Shape != nil {
				write(pe.Shape.Text)
			}
			if pe.Table != nil {
				for _, row := range pe.Table.TableRows {
					for _, c := range row.TableCells {
						write(c.Text)
						b.WriteString("\t")
					}
					b.WriteString("\n")
				}
			}
		}
	}
	return b.String(), nil
}

func (t *GoogleTool) driveList(ctx context.Context) (string, error) {
	var r struct {
		Files []struct{ ID, Name, MimeType string }
	}
	u := t.driveURL + "?pageSize=200&q=" + url.QueryEscape("trashed=false") +
		"&fields=" + url.QueryEscape("files(id,name,mimeType)")
	if err := t.getJSON(ctx, u, &r); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, f := range r.Files {
		if t.files == nil || t.files[f.ID] {
			fmt.Fprintf(&b, "%s\t%s\t%s\n", f.ID, f.Name, f.MimeType)
		}
	}
	if b.Len() == 0 {
		return "no files visible", nil
	}
	return "id\tname\tmimeType\n" + b.String(), nil
}
