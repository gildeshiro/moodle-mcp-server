package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jawadh/moodle-mcp-server/internal/api"
)

func TestGetTokenFromCredentials_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify method and content type
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("expected form content type, got %s", r.Header.Get("Content-Type"))
		}

		// Verify credentials are in the BODY, not the URL
		body, _ := io.ReadAll(r.Body)
		bodyStr := string(body)
		if r.URL.RawQuery != "" {
			t.Errorf("credentials must not appear in URL query string, got: %s", r.URL.RawQuery)
		}
		if bodyStr == "" {
			t.Error("request body must not be empty")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"token": "abc123"})
	}))
	defer srv.Close()

	token, err := api.GetTokenFromCredentials(context.Background(), srv.URL, "user", "pass")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "abc123" {
		t.Errorf("token = %q, want %q", token, "abc123")
	}
}

func TestGetTokenFromCredentials_InvalidCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid login, please try again"})
	}))
	defer srv.Close()

	_, err := api.GetTokenFromCredentials(context.Background(), srv.URL, "bad", "creds")
	if err == nil {
		t.Fatal("expected error for invalid credentials")
	}
}

func TestGetTokenFromCredentials_EmptyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"token": ""})
	}))
	defer srv.Close()

	_, err := api.GetTokenFromCredentials(context.Background(), srv.URL, "u", "p")
	if err == nil {
		t.Fatal("expected error when token is empty")
	}
}

func TestGetTokenFromCredentials_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	// A 500 response with non-JSON body should return a parse error
	_, err := api.GetTokenFromCredentials(context.Background(), srv.URL, "u", "p")
	if err == nil {
		t.Fatal("expected error on server error response")
	}
}
