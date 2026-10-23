package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// DMWebhook routes WhatsApp direct messages whose whole text matches Pattern to an HTTPS endpoint
// instead of the agent. The endpoint's JSON reply ({"reply": "..."}) is sent back to the sender.
type DMWebhook struct {
	Pattern string `json:"pattern"`
	URL     string `json:"url"`
	// SecretFile is the HMAC key file, resolved like file:// credentials (relative to the config directory).
	SecretFile string `json:"secret_file"`
	// ErrorReply is sent when the endpoint fails; empty means a built-in default.
	ErrorReply string `json:"error_reply"`
}

// Secret returns the trimmed content of SecretFile.
func (w DMWebhook) Secret() (string, error) {
	return resolveKey("file://" + w.SecretFile)
}

// ValidateDMWebhookFields checks one webhook's pattern, url and secret_file (shared with the dm_apps.d registry).
func ValidateDMWebhookFields(pattern, rawURL, secretFile string) error {
	if _, err := regexp.Compile("^(?:" + pattern + ")$"); err != nil || pattern == "" {
		return fmt.Errorf("invalid pattern")
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid url")
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return fmt.Errorf("url must be https (http only for loopback)")
	}
	if strings.TrimSpace(secretFile) == "" {
		return fmt.Errorf("secret_file is required")
	}
	return nil
}

// ValidateDMWebhooks checks patterns and URLs; it does not read the secret files.
func (s *WhatsAppSettings) ValidateDMWebhooks() error {
	for i, w := range s.DMWebhooks {
		if err := ValidateDMWebhookFields(w.Pattern, w.URL, w.SecretFile); err != nil {
			return fmt.Errorf("dm_webhooks[%d]: %w", i, err)
		}
	}
	return nil
}

// validateChannelDMWebhooks surfaces invalid dm_webhooks at config load time.
func (c *Config) validateChannelDMWebhooks() error {
	for name, bc := range c.Channels {
		decoded, err := bc.GetDecoded()
		if err != nil {
			continue
		}
		if s, ok := decoded.(*WhatsAppSettings); ok {
			if err := s.ValidateDMWebhooks(); err != nil {
				return fmt.Errorf("channels.%s: %w", name, err)
			}
		}
	}
	return nil
}
