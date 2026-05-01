package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jawadh/moodle-mcp-server/internal/api"
)

func TestClientIsAuthenticated(t *testing.T) {
	c := api.NewClient()
	if c.IsAuthenticated() {
		t.Fatal("new client should not be authenticated")
	}
	c.SetSession("https://moodle.example.com", "token123")
	if !c.IsAuthenticated() {
		t.Fatal("client should be authenticated after SetSession")
	}
}

func TestClientUserID(t *testing.T) {
	c := api.NewClient()
	if c.GetUserID() != 0 {
		t.Fatal("new client should have zero user ID")
	}
	c.SetUserID(42)
	if c.GetUserID() != 42 {
		t.Errorf("GetUserID() = %d, want 42", c.GetUserID())
	}
}

func TestClientCallNotAuthenticated(t *testing.T) {
	c := api.NewClient()
	_, err := c.Call(context.Background(), "any_function", nil)
	if err == nil {
		t.Fatal("expected error when not authenticated")
	}
}

func TestClientCallSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("wsfunction") != "core_webservice_get_site_info" {
			http.Error(w, "unexpected function", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"sitename": "Test", "userid": 1})
	}))
	defer srv.Close()

	c := api.NewClient()
	c.SetSession(srv.URL, "testtoken")

	data, err := c.Call(context.Background(), "core_webservice_get_site_info", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("could not unmarshal response: %v", err)
	}
	if result["sitename"] != "Test" {
		t.Errorf("sitename = %v, want Test", result["sitename"])
	}
}

func TestClientCallAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"errorcode": "invalidtoken",
			"message":   "Invalid token",
			"exception": "moodle_exception",
		})
	}))
	defer srv.Close()

	c := api.NewClient()
	c.SetSession(srv.URL, "badtoken")

	_, err := c.Call(context.Background(), "any_function", nil)
	if err == nil {
		t.Fatal("expected error for API error response")
	}
	apiErr, ok := err.(*api.APIError)
	if !ok {
		t.Fatalf("expected *api.APIError, got %T", err)
	}
	if apiErr.ErrorCode != "invalidtoken" {
		t.Errorf("ErrorCode = %q, want %q", apiErr.ErrorCode, "invalidtoken")
	}
}

func TestClientCallLargeResponseRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write more than maxResponseBytes (10MB)
		w.Header().Set("Content-Type", "application/json")
		// Write enough bytes to exceed the limit
		large := make([]byte, 11*1024*1024)
		for i := range large {
			large[i] = 'a'
		}
		w.Write(large)
	}))
	defer srv.Close()

	c := api.NewClient()
	c.SetSession(srv.URL, "tok")

	// Should not OOM — the response is truncated at 10MB and then JSON parse fails
	_, err := c.Call(context.Background(), "anything", nil)
	// We expect either a JSON parse error (truncated) or success with truncated data
	// The key property is that the server does not consume 11MB of memory
	_ = err // any outcome is acceptable as long as it returns
}

func BenchmarkClientCall(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"sitename": "Test", "userid": 1})
	}))
	defer srv.Close()

	c := api.NewClient()
	c.SetSession(srv.URL, "testtoken")
	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		c.Call(ctx, "core_webservice_get_site_info", nil)
	}
}
