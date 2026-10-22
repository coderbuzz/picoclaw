//go:build whatsapp_native

package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	dmWebhookTimeout      = 10 * time.Second
	dmWebhookMaxReply     = 64 << 10
	dmWebhookDefaultError = "Layanan sedang sibuk, coba lagi sebentar lagi."
)

type dmWebhook struct {
	re         *regexp.Regexp
	pattern    string
	url        string
	secret     []byte
	errorReply string
}

// dmWebhookPayload is the JSON body POSTed to the endpoint.
type dmWebhookPayload struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
	SenderPN  string `json:"sender_pn"`
	SenderLID string `json:"sender_lid"`
	ChatID    string `json:"chat_id"`
	Timestamp int64  `json:"timestamp"`
}

// compileDMWebhooks validates the settings, compiles each pattern (whole-text match) and reads the secrets.
func compileDMWebhooks(s *config.WhatsAppSettings) ([]dmWebhook, error) {
	if err := s.ValidateDMWebhooks(); err != nil {
		return nil, err
	}
	var out []dmWebhook
	for i, w := range s.DMWebhooks {
		secret, err := w.Secret()
		if err != nil {
			return nil, fmt.Errorf("dm_webhooks[%d]: secret_file: %w", i, err)
		}
		reply := w.ErrorReply
		if reply == "" {
			reply = dmWebhookDefaultError
		}
		out = append(out, dmWebhook{
			re:         regexp.MustCompile("^(?:" + w.Pattern + ")$"),
			pattern:    w.Pattern,
			url:        w.URL,
			secret:     []byte(secret),
			errorReply: reply,
		})
	}
	return out, nil
}

// matchDMWebhook returns the first webhook whose pattern matches the whole text.
func matchDMWebhook(hooks []dmWebhook, text string) *dmWebhook {
	for i := range hooks {
		if hooks[i].re.MatchString(text) {
			return &hooks[i]
		}
	}
	return nil
}

// dmSenderPNLID returns the sender's phone number and LID (user parts, "" when unknown).
func dmSenderPNLID(sender, alt types.JID, counterpart func(types.JID) types.JID) (pn, lid string) {
	jids := []types.JID{sender.ToNonAD(), alt.ToNonAD()}
	if alt.IsEmpty() && counterpart != nil {
		jids[1] = counterpart(sender.ToNonAD()).ToNonAD()
	}
	for _, j := range jids {
		switch {
		case j.Server == types.DefaultUserServer && pn == "":
			pn = j.User
		case j.Server == types.HiddenUserServer && lid == "":
			lid = j.User
		}
	}
	return pn, lid
}

// signDMWebhook returns "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + body)).
func signDMWebhook(secret []byte, timestamp int64, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(strconv.FormatInt(timestamp, 10) + "."))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// call POSTs the payload and returns the reply text and HTTP status (0 when the request never completed).
func (h *dmWebhook) call(ctx context.Context, client *http.Client, p dmWebhookPayload) (string, int, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, dmWebhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	ts := time.Now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Picoclaw-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Picoclaw-Signature", signDMWebhook(h.secret, ts, body))
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct {
		Reply string `json:"reply"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, dmWebhookMaxReply)).Decode(&out); err != nil {
		return "", resp.StatusCode, fmt.Errorf("invalid reply body")
	}
	return strings.TrimSpace(out.Reply), resp.StatusCode, nil
}

// newDMWebhookClient never follows redirects: the signed body must only reach the configured URL.
func newDMWebhookClient() *http.Client {
	return &http.Client{
		Timeout:       dmWebhookTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
