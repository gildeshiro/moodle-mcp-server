package config_test

import (
	"testing"

	"github.com/jawadh/moodle-mcp-server/internal/config"
)

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", ""},
		{"plain host", "moodle.example.com", "https://moodle.example.com"},
		{"trailing slash", "https://moodle.example.com/", "https://moodle.example.com"},
		{"multiple trailing slashes", "https://moodle.example.com///", "https://moodle.example.com"},
		{"https preserved", "https://moodle.example.com", "https://moodle.example.com"},
		{"http preserved", "http://moodle.example.com", "http://moodle.example.com"},
		{"whitespace trimmed", "  https://moodle.example.com  ", "https://moodle.example.com"},
		{"whitespace + no scheme", "  moodle.example.com  ", "https://moodle.example.com"},
		{"path preserved", "https://moodle.example.com/lms", "https://moodle.example.com/lms"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := config.NormalizeURL(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Config
		wantErr bool
	}{
		{"empty URL is valid (deferred login)", config.Config{}, false},
		{"valid https URL", config.Config{MoodleURL: "https://moodle.example.com"}, false},
		{"valid http URL", config.Config{MoodleURL: "http://moodle.example.com"}, false},
		{"invalid scheme", config.Config{MoodleURL: "ftp://moodle.example.com"}, true},
		{"no host", config.Config{MoodleURL: "https://"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfigHasAuth(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{"no URL", config.Config{Token: "tok"}, false},
		{"URL + token", config.Config{MoodleURL: "https://m.example.com", Token: "tok"}, true},
		{"URL + credentials", config.Config{MoodleURL: "https://m.example.com", Username: "u", Password: "p"}, true},
		{"URL only", config.Config{MoodleURL: "https://m.example.com"}, false},
		{"URL + username only", config.Config{MoodleURL: "https://m.example.com", Username: "u"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.HasAuth()
			if got != tt.want {
				t.Errorf("HasAuth() = %v, want %v", got, tt.want)
			}
		})
	}
}

// BenchmarkNormalizeURL measures the cost of URL normalization.
func BenchmarkNormalizeURL(b *testing.B) {
	for b.Loop() {
		config.NormalizeURL("  https://moodle.example.com/  ")
	}
}
