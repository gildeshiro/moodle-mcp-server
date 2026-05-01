package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Config holds the Moodle MCP server configuration.
type Config struct {
	MoodleURL string
	Token     string
	Username  string
	Password  string
}

// LoadFromEnv loads configuration from environment variables.
// All fields are optional at load time — the login tool can provide them at runtime.
func LoadFromEnv() *Config {
	return &Config{
		MoodleURL: NormalizeURL(os.Getenv("MOODLE_URL")),
		Token:     os.Getenv("MOODLE_TOKEN"),
		Username:  os.Getenv("MOODLE_USERNAME"),
		Password:  os.Getenv("MOODLE_PASSWORD"),
	}
}

// HasAuth returns true if the config has enough info to authenticate.
func (c *Config) HasAuth() bool {
	if c.MoodleURL == "" {
		return false
	}
	return c.Token != "" || (c.Username != "" && c.Password != "")
}

// Validate checks that the config has a valid Moodle URL if one is set.
func (c *Config) Validate() error {
	if c.MoodleURL == "" {
		return nil // URL can be provided later via login tool
	}
	u, err := url.Parse(c.MoodleURL)
	if err != nil {
		return fmt.Errorf("invalid MOODLE_URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("MOODLE_URL must use http or https scheme, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("MOODLE_URL must have a host")
	}
	return nil
}

func NormalizeURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	rawURL = strings.TrimRight(rawURL, "/")
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}
	return rawURL
}
