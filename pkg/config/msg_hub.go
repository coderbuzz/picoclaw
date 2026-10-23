package config

import (
	"fmt"
	"net/url"
	"strings"
)

// MsgHubConfig enables the outbound-notification subscriber: the bot dials URL (a WebSocket hub) and sends
// the WhatsApp messages the hub pushes. nil (omitted) means the feature is off.
type MsgHubConfig struct {
	URL string `json:"url"` // wss://host[/path]; ws:// only for loopback
	// TokenFile holds the subscriber bearer token, resolved like file:// credentials (relative to the config directory).
	TokenFile string `json:"token_file"`
}

// Token returns the trimmed content of TokenFile.
func (m MsgHubConfig) Token() (string, error) {
	return resolveKey("file://" + m.TokenFile)
}

// Validate checks url and token_file; it does not read the token file.
func (m MsgHubConfig) Validate() error {
	u, err := url.Parse(m.URL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid url")
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "wss" && !(u.Scheme == "ws" && loopback) {
		return fmt.Errorf("url must be wss (ws only for loopback)")
	}
	if strings.TrimSpace(m.TokenFile) == "" {
		return fmt.Errorf("token_file is required")
	}
	return nil
}
