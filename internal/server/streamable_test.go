package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// newTestStreamable builds a minimal handler chain identical to RunStreamable's
// production setup, exposed as an httptest.Server for assertions.
func newTestStreamable(t *testing.T, token string) *httptest.Server {
	t.Helper()
	mcp := mcpserver.NewMCPServer("test", "0.0.0",
		mcpserver.WithToolCapabilities(true),
	)
	streamable := mcpserver.NewStreamableHTTPServer(mcp,
		mcpserver.WithEndpointPath("/mcp"),
	)

	auth := BearerAuth(token)
	cors := CORS(nil)
	logger := RequestLogger()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler("test"))
	mux.Handle("/mcp", logger(cors(auth(streamable))))

	return httptest.NewServer(mux)
}

func TestBearerAuthRejectsMissingHeader(t *testing.T) {
	ts := newTestStreamable(t, "secret")
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/mcp", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf(`want WWW-Authenticate header containing "Bearer"; got %q`, got)
	}
}

func TestBearerAuthRejectsWrongToken(t *testing.T) {
	ts := newTestStreamable(t, "secret")
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401 with wrong token; got %d", resp.StatusCode)
	}
}

func TestBearerAuthAllowsValidToken(t *testing.T) {
	ts := newTestStreamable(t, "secret")
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// We don't assert 200 here — initialize handshake state may differ —
	// only that the bearer middleware did not reject us with 401.
	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("got 401 with valid bearer; middleware chain broken")
	}
}

func TestHealthzNoAuth(t *testing.T) {
	ts := newTestStreamable(t, "secret")
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("want 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestCORSPreflightAllowedOrigin(t *testing.T) {
	t.Helper()
	mcp := mcpserver.NewMCPServer("test", "0.0.0", mcpserver.WithToolCapabilities(true))
	streamable := mcpserver.NewStreamableHTTPServer(mcp, mcpserver.WithEndpointPath("/mcp"))
	chain := RequestLogger()(CORS([]string{"https://claude.ai"})(BearerAuth("secret")(streamable)))

	mux := http.NewServeMux()
	mux.Handle("/mcp", chain)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("OPTIONS", ts.URL+"/mcp", nil)
	req.Header.Set("Origin", "https://claude.ai")
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("want 204 for preflight, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://claude.ai" {
		t.Errorf("want Allow-Origin=https://claude.ai, got %q", got)
	}
	if !strings.Contains(resp.Header.Get("Access-Control-Allow-Headers"), "MCP-Session-Id") {
		t.Errorf("want MCP-Session-Id in Allow-Headers, got %q",
			resp.Header.Get("Access-Control-Allow-Headers"))
	}
}

func TestCORSPreflightDisallowedOrigin(t *testing.T) {
	t.Helper()
	mcp := mcpserver.NewMCPServer("test", "0.0.0", mcpserver.WithToolCapabilities(true))
	streamable := mcpserver.NewStreamableHTTPServer(mcp, mcpserver.WithEndpointPath("/mcp"))
	chain := RequestLogger()(CORS([]string{"https://claude.ai"})(BearerAuth("secret")(streamable)))

	mux := http.NewServeMux()
	mux.Handle("/mcp", chain)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("OPTIONS", ts.URL+"/mcp", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("want empty Allow-Origin for disallowed origin, got %q", got)
	}
}
