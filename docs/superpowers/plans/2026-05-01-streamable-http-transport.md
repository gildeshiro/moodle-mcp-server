# Streamable HTTP Transport for moodle-mcp-server — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add MCP Streamable HTTP transport mode (`-mode http`) to `moodle-mcp-server` so it can serve claude.ai custom connectors and other remote MCP clients, gated by bearer-token auth.

**Architecture:** New entrypoint mode wires `mcp-go`'s built-in `NewStreamableHTTPServer` behind a stdlib middleware chain (`requestLogger → cors → bearerAuth`) on a custom `http.Server` with `/healthz` endpoint and graceful shutdown. The existing `mcp` (stdio) and `rest` (custom REST) modes are untouched.

**Tech Stack:** Go 1.23, `github.com/mark3labs/mcp-go` v0.50.0 (bumped from v0.26.0), stdlib `net/http`, `crypto/subtle`.

**Spec:** [`docs/superpowers/specs/2026-05-01-streamable-http-transport-design.md`](../specs/2026-05-01-streamable-http-transport-design.md)

**Working branch:** `feat/streamable-http-transport` on fork `gildeshiro/moodle-mcp-server`. Spec doc already committed (will be excluded from the upstream PR via cherry-pick — see Task 11).

---

## File Structure

| Path | Action | Responsibility |
|---|---|---|
| `go.mod`, `go.sum` | modify | Bump `mark3labs/mcp-go` v0.26.0 → v0.50.0 |
| `cmd/moodle-mcp/main.go` | modify | Add `-mode http` flags + dispatch; fix `Arguments` access for new lib API |
| `internal/server/middleware.go` | **create** | `BearerAuth`, `CORS`, `RequestLogger` middleware |
| `internal/server/streamable.go` | **create** | `RunStreamable` — wires mux, /healthz, middleware chain, graceful shutdown |
| `internal/server/streamable_test.go` | **create** | 4 smoke tests for the middleware chain + healthz |
| `Dockerfile` | modify | Bump Go 1.22→1.23, switch to `ENTRYPOINT` so mode is overridable |
| `examples/deploy/smithery.yaml` | **create** | Smithery deploy manifest |
| `examples/deploy/railway.json` | **create** | Railway deploy config |
| `README.md` | modify | New "Remote (HTTP) mode" section |
| `DEPLOYMENT_GUIDE.md` | modify | New "claude.ai Custom Connector" section |

**Final commit layout (4 commits for upstream PR):**

1. `chore: bump mark3labs/mcp-go from v0.26.0 to v0.50.0` — `go.mod`, `go.sum`, `cmd/moodle-mcp/main.go` (Arguments fix only)
2. `feat: add MCP Streamable HTTP transport mode` — middleware, streamable, test, main.go (flags+dispatch)
3. `docs: document remote HTTP mode and claude.ai custom connector` — `README.md`, `DEPLOYMENT_GUIDE.md`
4. `chore: add deploy examples + make Dockerfile mode-agnostic` — `Dockerfile`, `examples/deploy/*`

---

## Task 1: Bump mcp-go and fix Arguments access pattern

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `cmd/moodle-mcp/main.go:432-456` (the `intArg` helper and the `unread_only` access)

The library's `CallToolParams.Arguments` changed from `map[string]any` to `any` between v0.26 and v0.30+. New helper `req.GetArguments() map[string]any` returns the underlying map. Two call sites need updating.

- [ ] **Step 1.1: Update go.mod**

Edit `go.mod`. Change:

```
require github.com/mark3labs/mcp-go v0.26.0
```

to:

```
require github.com/mark3labs/mcp-go v0.50.0
```

- [ ] **Step 1.2: Run `go mod tidy`**

Run from repo root:

```
go mod tidy
```

Expected: `go.sum` updated with new hashes. No errors.

- [ ] **Step 1.3: Build to surface API breaks**

Run:

```
go build ./...
```

Expected output (failure — confirms the breaks we know about):

```
cmd/moodle-mcp/main.go:433:35: invalid operation: req.Params.Arguments["unread_only"] (type any does not support indexing)
cmd/moodle-mcp/main.go:451:23: invalid operation: req.Params.Arguments[key] (type any does not support indexing)
```

If you see DIFFERENT errors, stop and read the v0.50.0 changelog at https://github.com/mark3labs/mcp-go/releases — there may be other breaks not anticipated by this plan.

- [ ] **Step 1.4: Fix `intArg` helper**

In `cmd/moodle-mcp/main.go`, find:

```go
// intArg extracts an integer from request arguments (JSON numbers are float64).
func intArg(req mcp.CallToolRequest, key string) int {
	if v, ok := req.Params.Arguments[key]; ok {
		switch n := v.(type) {
```

Replace the `if v, ok := req.Params.Arguments[key]; ok {` line with:

```go
func intArg(req mcp.CallToolRequest, key string) int {
	if v, ok := req.GetArguments()[key]; ok {
		switch n := v.(type) {
```

- [ ] **Step 1.5: Fix `unread_only` access**

In the same file, find:

```go
unreadOnly := true
if v, ok := req.Params.Arguments["unread_only"]; ok {
	if b, ok := v.(bool); ok {
		unreadOnly = b
	}
}
```

Replace with:

```go
unreadOnly := true
if v, ok := req.GetArguments()["unread_only"]; ok {
	if b, ok := v.(bool); ok {
		unreadOnly = b
	}
}
```

- [ ] **Step 1.6: Verify build passes**

Run:

```
go build ./...
go vet ./...
```

Expected: no output (success). If there are still errors, they are unexpected — investigate the v0.50.0 release notes before continuing.

- [ ] **Step 1.7: Commit**

```
git add go.mod go.sum cmd/moodle-mcp/main.go
git commit -m "chore: bump mark3labs/mcp-go from v0.26.0 to v0.50.0

Required for Streamable HTTP transport support, which was added in
v0.30.0. The library's CallToolParams.Arguments type changed from
map[string]any to any in this range; switch the two access sites in
cmd/moodle-mcp/main.go to use req.GetArguments() instead.

No behavior change."
```

---

## Task 2: Create the `BearerAuth` middleware (TDD)

**Files:**
- Test: `internal/server/streamable_test.go` (new)
- Create: `internal/server/middleware.go`

We start with the test file because it drives the public API of `BearerAuth`. We write the tests for ALL middleware (bearer, cors, logger) at once because they share the test scaffolding (`newTestStreamable`), but we make them fail one by one as we implement each piece. For now the test file only contains the bearer-related tests; we'll add cors/logger tests in later tasks if needed (the smoke tests in Task 5 cover the chain end-to-end).

- [ ] **Step 2.1: Create the test scaffold + bearer tests**

Create `internal/server/streamable_test.go`:

```go
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
```

- [ ] **Step 2.2: Run tests to verify they fail with the right error**

Run:

```
go test ./internal/server/...
```

Expected: compile error referencing `BearerAuth`, `CORS`, `RequestLogger`, `healthHandler` — all unresolved. This confirms the tests reach for the API we're about to implement.

- [ ] **Step 2.3: Create `internal/server/middleware.go` with `BearerAuth`**

Create `internal/server/middleware.go`:

```go
package server

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
	"time"
)

// BearerAuth wraps a handler with bearer-token authentication.
// Calls site MUST pass a non-empty token; this function panics on empty input
// to make accidental misconfiguration loud rather than insecure.
//
// The middleware uses constant-time comparison and lets CORS preflight
// (OPTIONS) requests through untouched.
func BearerAuth(token string) func(http.Handler) http.Handler {
	if token == "" {
		panic("server.BearerAuth: empty token (caller must enforce non-empty)")
	}
	expected := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			const prefix = "Bearer "
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, prefix) {
				unauthorized(w)
				return
			}
			got := []byte(strings.TrimPrefix(authHeader, prefix))
			if subtle.ConstantTimeCompare(got, expected) != 1 {
				unauthorized(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="moodle-mcp"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}
```

(`CORS`, `RequestLogger`, and `healthHandler` are stubs added in Tasks 3-5; the package won't compile yet.)

- [ ] **Step 2.4: Add minimal stubs so the test file compiles**

Append to `internal/server/middleware.go`:

```go
// CORS is implemented in Task 3.
func CORS(origins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}

// RequestLogger is implemented in Task 4.
func RequestLogger() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}
```

And create a temporary `internal/server/streamable.go` containing only the `healthHandler` (full file written in Task 5):

```go
package server

import (
	"encoding/json"
	"net/http"
)

func healthHandler(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"version": version,
			"mode":    "http",
		})
	}
}
```

- [ ] **Step 2.5: Run the four tests and verify they all pass**

Run:

```
go test -v -run "TestBearerAuth|TestHealthz" ./internal/server/...
```

Expected:

```
=== RUN   TestBearerAuthRejectsMissingHeader
--- PASS: TestBearerAuthRejectsMissingHeader
=== RUN   TestBearerAuthRejectsWrongToken
--- PASS: TestBearerAuthRejectsWrongToken
=== RUN   TestBearerAuthAllowsValidToken
--- PASS: TestBearerAuthAllowsValidToken
=== RUN   TestHealthzNoAuth
--- PASS: TestHealthzNoAuth
PASS
```

If `TestBearerAuthAllowsValidToken` fails with a non-401 error code we don't expect (e.g., the streamable server panics on a request without a `Mcp-Session-Id` header), debug — but the contract is "did the bearer middleware let it through?", not "does the MCP handshake succeed". A 200/400/406 all pass; only 401 fails.

- [ ] **Step 2.6: Do NOT commit yet** — we accumulate the entire feature into one commit at end of Task 6.

---

## Task 3: Implement `CORS` middleware

**Files:**
- Modify: `internal/server/middleware.go`

- [ ] **Step 3.1: Replace the CORS stub with the real implementation**

In `internal/server/middleware.go`, find the stub:

```go
// CORS is implemented in Task 3.
func CORS(origins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}
```

Replace with:

```go
// CORS wraps a handler with permissive-but-explicit CORS headers for the given
// allowed origins. An empty origins slice short-circuits to a no-op pass-through
// (server-to-server callers, including claude.ai backends, do not need CORS).
//
// The wildcard "*" is accepted but logged with a warning — operators should
// pin specific origins in production.
//
// MCP-Session-Id is added to the allowed headers because the Streamable HTTP
// spec uses it for session continuity.
func CORS(origins []string) func(http.Handler) http.Handler {
	if len(origins) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	allowAll := false
	allowed := make(map[string]bool, len(origins))
	for _, o := range origins {
		o = strings.TrimSpace(o)
		if o == "*" {
			allowAll = true
			log.Println("WARNING: CORS allows all origins ('*'). Tighten in production.")
		}
		if o != "" {
			allowed[o] = true
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (allowAll || allowed[origin]) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Session-Id")
				w.Header().Set("Access-Control-Max-Age", "86400")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

- [ ] **Step 3.2: Add a CORS preflight test**

Append to `internal/server/streamable_test.go`:

```go
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
```

- [ ] **Step 3.3: Run all tests**

Run:

```
go test -v ./internal/server/...
```

Expected: all 6 tests pass (4 from Task 2 + 2 new CORS tests).

---

## Task 4: Implement `RequestLogger` middleware

**Files:**
- Modify: `internal/server/middleware.go`

- [ ] **Step 4.1: Replace the RequestLogger stub**

In `internal/server/middleware.go`, find:

```go
// RequestLogger is implemented in Task 4.
func RequestLogger() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}
```

Replace with:

```go
// statusRecorder captures the response status code for logging.
// We avoid pulling in a third-party logger — stdlib log.Printf with a
// grep-friendly key=value format is enough.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// RequestLogger logs one structured line per request to stdout.
// Format: "<RFC3339> method=<M> path=<P> status=<S> dur=<D> remote=<IP>".
// No request/response bodies are logged (may contain Moodle tokens or PII).
// X-Forwarded-For (first hop) is preferred over RemoteAddr when present.
func RequestLogger() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			log.Printf(
				"%s method=%s path=%s status=%d dur=%s remote=%s",
				start.UTC().Format(time.RFC3339),
				r.Method,
				r.URL.Path,
				rec.status,
				time.Since(start).Round(time.Millisecond),
				clientIP(r),
			)
		})
	}
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return xff
	}
	return r.RemoteAddr
}
```

- [ ] **Step 4.2: Run all tests**

Run:

```
go test ./internal/server/...
```

Expected: all 6 tests pass. The logger doesn't have its own test — it's exercised by every other test, and asserting log output is brittle.

---

## Task 5: Implement `RunStreamable` (the server entrypoint)

**Files:**
- Modify: `internal/server/streamable.go` (replace the temporary `healthHandler`-only stub from Task 2)

- [ ] **Step 5.1: Replace `internal/server/streamable.go` with the full implementation**

Overwrite `internal/server/streamable.go` with:

```go
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// StreamableOpts configures the remote HTTP MCP server.
type StreamableOpts struct {
	Port        int
	AuthToken   string // required (non-empty); enforced by RunStreamable
	CORSOrigins string // comma-separated; empty = no CORS
	Path        string // default "/mcp"
	Version     string // surfaced in /healthz
}

const shutdownTimeout = 10 * time.Second

// RunStreamable starts an http.Server exposing the given MCPServer over the
// MCP Streamable HTTP transport, gated by a bearer-token middleware.
//
// Blocks until SIGINT/SIGTERM, the parent context cancels, or the listener
// fails. On shutdown signal, drains in-flight requests for up to 10 seconds
// before returning.
func RunStreamable(ctx context.Context, mcp *mcpserver.MCPServer, opts StreamableOpts) error {
	if opts.AuthToken == "" {
		return errors.New("MCP_AUTH_TOKEN required for http mode (security guardrail)")
	}
	if opts.Path == "" {
		opts.Path = "/mcp"
	}
	if !strings.HasPrefix(opts.Path, "/") {
		opts.Path = "/" + opts.Path
	}
	if opts.Port == 0 {
		opts.Port = 8080
	}

	streamable := mcpserver.NewStreamableHTTPServer(mcp,
		mcpserver.WithEndpointPath(opts.Path),
	)

	var origins []string
	if opts.CORSOrigins != "" {
		origins = strings.Split(opts.CORSOrigins, ",")
	}

	auth := BearerAuth(opts.AuthToken)
	cors := CORS(origins)
	logger := RequestLogger()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler(opts.Version))
	mux.Handle(opts.Path, logger(cors(auth(streamable))))

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", opts.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("Moodle MCP Streamable HTTP listening on :%d (path=%s)", opts.Port, opts.Path)
		errCh <- srv.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case sig := <-sigCh:
		log.Printf("shutdown signal %s received; draining...", sig)
	case <-ctx.Done():
		log.Println("context cancelled; draining...")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func healthHandler(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"version": version,
			"mode":    "http",
		})
	}
}
```

- [ ] **Step 5.2: Build to verify the package compiles**

Run:

```
go build ./...
```

Expected: no output (success).

- [ ] **Step 5.3: Run all tests**

Run:

```
go test ./internal/server/...
```

Expected: all tests still pass.

---

## Task 6: Wire `-mode http` into `cmd/moodle-mcp/main.go`

**Files:**
- Modify: `cmd/moodle-mcp/main.go`

- [ ] **Step 6.0: Update the `login` tool description with a remote-mode warning**

The `login` tool accepts Moodle credentials as arguments, which is fine for stdio (local-only) but flows credentials over the network in http mode. We add a one-line steering note to the description so it's visible to clients regardless of mode.

In `cmd/moodle-mcp/main.go`, find the `login` tool registration:

```go
mcp.NewTool("login",
    mcp.WithDescription("Log in to your Moodle account. This is the first step — provide your Moodle site URL, username, and password."),
```

Replace the description with:

```go
mcp.NewTool("login",
    mcp.WithDescription("Log in to your Moodle account. This is the first step — provide your Moodle site URL, username, and password. (In remote HTTP mode, prefer pre-configuring MOODLE_TOKEN server-side so credentials don't transit the network.)"),
```

- [ ] **Step 6.1: Update the `-mode` flag's help text**

In `cmd/moodle-mcp/main.go`, find:

```go
mode := flag.String("mode", "mcp", "Server mode: 'mcp' for Claude, 'rest' for HTTP API, 'both' for both")
```

Replace with:

```go
mode := flag.String("mode", "mcp", "Server mode: 'mcp' (stdio), 'rest' (custom HTTP API), 'http' (MCP Streamable HTTP), 'both' (mcp+rest)")
```

- [ ] **Step 6.2: Add the new flags right after `-mode`**

Below the `mode` line and before `flag.Parse()`, add:

```go
authToken := flag.String("auth-token", "", "Bearer token for http mode (env: MCP_AUTH_TOKEN)")
corsOrigins := flag.String("cors-origins", "", "Comma-separated CORS origins for http mode (env: MCP_CORS_ORIGINS)")
httpPath := flag.String("http-path", "/mcp", "MCP endpoint path for http mode (env: MCP_HTTP_PATH)")
```

- [ ] **Step 6.3: Add env-var fallbacks for the new flags**

After `flag.Parse()` and the existing `REST_API_PORT` block, add:

```go
if *authToken == "" {
	*authToken = os.Getenv("MCP_AUTH_TOKEN")
}
if *corsOrigins == "" {
	*corsOrigins = os.Getenv("MCP_CORS_ORIGINS")
}
if v := os.Getenv("MCP_HTTP_PATH"); v != "" {
	*httpPath = v
}
// Honor PORT env from cloud platforms (Railway, Render, Fly, Cloud Run)
if envPort := os.Getenv("PORT"); envPort != "" {
	if p, err := strconv.Atoi(envPort); err == nil {
		*restPort = p
	}
}
```

(Note: `REST_API_PORT` from the existing code keeps working; `PORT` is checked AFTER it so cloud platforms can override per-deploy.)

- [ ] **Step 6.4: Add the http case to the mode switch**

Find:

```go
switch *mode {
case "mcp":
	runMCPServer(client)
case "rest":
	runRESTServer(client, *restPort)
case "both":
	log.Println("Running both MCP and REST servers (experimental)")
	go runRESTServer(client, *restPort)
	runMCPServer(client)
default:
```

Add a `case "http":` branch:

```go
switch *mode {
case "mcp":
	runMCPServer(client)
case "rest":
	runRESTServer(client, *restPort)
case "http":
	runStreamableHTTPServer(client, *restPort, *authToken, *corsOrigins, *httpPath)
case "both":
	log.Println("Running both MCP and REST servers (experimental)")
	go runRESTServer(client, *restPort)
	runMCPServer(client)
default:
```

- [ ] **Step 6.5: Add the `runStreamableHTTPServer` function**

Add this function below `runRESTServer` (right before `registerTools`):

```go
func runStreamableHTTPServer(client *api.Client, port int, authToken, corsOrigins, path string) {
	s := mcpserver.NewMCPServer(
		"moodle-mcp-server",
		"1.2.0",
		mcpserver.WithToolCapabilities(true),
	)
	registerTools(s, client)

	if err := server.RunStreamable(context.Background(), s, server.StreamableOpts{
		Port:        port,
		AuthToken:   authToken,
		CORSOrigins: corsOrigins,
		Path:        path,
		Version:     "1.2.0",
	}); err != nil {
		fmt.Fprintf(os.Stderr, "streamable server error: %v\n", err)
		os.Exit(1)
	}
}
```

(The `server` import — `github.com/jawadh/moodle-mcp-server/internal/server` — already exists in `main.go` for the REST mode; no new import needed.)

- [ ] **Step 6.6: Verify build + vet**

Run:

```
go build ./...
go vet ./...
```

Expected: no output.

- [ ] **Step 6.7: Manual smoke test with a fake bearer**

Build the binary and run it briefly:

```
go build -o moodle-mcp.exe ./cmd/moodle-mcp/
$env:MCP_AUTH_TOKEN = "smoke-test-token"
$env:MOODLE_URL = "https://example.com"
$env:MOODLE_TOKEN = "fake"
.\moodle-mcp.exe -mode http -port 8090
```

(The user is on Windows; PowerShell syntax. On bash hosts use `export VAR=value`.)

In a separate terminal:

```
curl -i http://localhost:8090/healthz
curl -i -X POST http://localhost:8090/mcp -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
curl -i -X POST http://localhost:8090/mcp -H "Authorization: Bearer smoke-test-token" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

Expected:
1. healthz → `200 OK` with JSON body
2. /mcp without auth → `401 Unauthorized`, `WWW-Authenticate: Bearer realm="moodle-mcp"`
3. /mcp with auth → some 2xx/4xx response that is NOT 401 (the MCP layer may complain about the request but auth passed)

Stop the server (`Ctrl+C`) and verify the log shows "shutdown signal SIGINT received; draining..." then exits cleanly.

- [ ] **Step 6.8: Verify negative case — empty auth token**

Run:

```
$env:MCP_AUTH_TOKEN = ""
.\moodle-mcp.exe -mode http -port 8090
```

Expected output:

```
streamable server error: MCP_AUTH_TOKEN required for http mode (security guardrail)
```

And exit code 1. This proves the security guardrail works.

- [ ] **Step 6.9: Commit feature commit (Tasks 2-6)**

```
git add internal/server/middleware.go internal/server/streamable.go internal/server/streamable_test.go cmd/moodle-mcp/main.go
git commit -m "feat: add MCP Streamable HTTP transport mode

Adds a fourth run mode (-mode http) that exposes the MCP server over
the Streamable HTTP transport (per the 2025-03-26 MCP spec) on a
configurable port and path, gated by a bearer-token middleware.

Components:
- internal/server/middleware.go — BearerAuth, CORS, RequestLogger
- internal/server/streamable.go — RunStreamable wires NewStreamableHTTPServer
  behind the middleware chain on a custom http.Server with /healthz and
  graceful shutdown (10s drain on SIGINT/SIGTERM)
- internal/server/streamable_test.go — smoke tests for healthz, bearer
  rejection, and CORS preflight

New flags / env vars (http mode only):
- -auth-token / MCP_AUTH_TOKEN (required; server refuses to boot without it)
- -cors-origins / MCP_CORS_ORIGINS (default empty = no CORS)
- -http-path / MCP_HTTP_PATH (default /mcp)
- PORT env var honored for cloud platforms (Railway, Render, Fly, Cloud Run)

The existing mcp (stdio), rest (custom HTTP API), and both modes are
unchanged."
```

- [ ] **Step 6.10: Verify the commit only touches the four files**

Run:

```
git show --stat HEAD
```

Expected: 4 files changed (3 new in `internal/server/`, 1 modified `cmd/moodle-mcp/main.go`). No `go.mod`/`go.sum` (those went in Task 1's commit). No README/Dockerfile changes.

---

## Task 7: Update README.md with the new mode

**Files:**
- Modify: `README.md`

- [ ] **Step 7.1: Add a new section after "Installation (Easiest)"**

Find the line `## Installation (Easiest)` in `README.md`. Find the END of the Installation section (next `##` heading). Insert a new section RIGHT BEFORE it:

```markdown
## Remote (HTTP) mode — for claude.ai custom connectors

The server can also run as a remote MCP endpoint over Streamable HTTP, suitable for [claude.ai custom connectors](https://support.claude.com/en/articles/11503834) and any other client that speaks the MCP Streamable HTTP transport.

### Quick start (local)

```bash
# Generate a strong shared secret
export MCP_AUTH_TOKEN=$(openssl rand -hex 32)

# Configure your Moodle session (one of the two options below)
export MOODLE_URL=https://your.moodle.example
export MOODLE_TOKEN=$(your-moodle-mobile-token)
# or use username/password (server fetches a token at boot):
# export MOODLE_USERNAME=you; export MOODLE_PASSWORD=...

./moodle-mcp -mode http -port 8080
```

Then in **claude.ai → Settings → Connectors → Add custom connector**:

- **URL:** `https://<your-deployment>/mcp`
- **Custom header:** `Authorization: Bearer <your MCP_AUTH_TOKEN>`

### Available knobs

| Flag | Env var | Default | Purpose |
|---|---|---|---|
| `-auth-token` | `MCP_AUTH_TOKEN` | — (required) | Shared bearer secret |
| `-port` | `PORT`, `REST_API_PORT` | `8080` | TCP port to listen on |
| `-cors-origins` | `MCP_CORS_ORIGINS` | (empty) | Comma-separated origins (e.g. `https://claude.ai,https://claude.com`) |
| `-http-path` | `MCP_HTTP_PATH` | `/mcp` | Endpoint path |

The server refuses to boot in `http` mode without an auth token (security guardrail). The `/healthz` endpoint is unauthenticated for cloud load balancers. See [DEPLOYMENT_GUIDE.md](DEPLOYMENT_GUIDE.md#claudeai-custom-connector) for hosted deployment recipes.
```

- [ ] **Step 7.2: Verify the file renders**

Open `README.md` in a markdown preview (or use `gh markdown-preview` if installed). Check:
- The new section appears between Installation and the next major section.
- The flag table is properly formatted (no broken pipes).
- The link to `DEPLOYMENT_GUIDE.md` is valid (will resolve once Task 8 lands).

---

## Task 8: Update DEPLOYMENT_GUIDE.md

**Files:**
- Modify: `DEPLOYMENT_GUIDE.md`

- [ ] **Step 8.1: Add a new section at the end of the file**

Append to `DEPLOYMENT_GUIDE.md`:

```markdown
---

## claude.ai Custom Connector

Use `-mode http` to serve [claude.ai custom connectors](https://support.claude.com/en/articles/11503834) and other Streamable HTTP clients. Below: four free-tier-friendly hosting recipes.

### Common prerequisites

```bash
# Generate a shared secret you'll paste into claude.ai
export MCP_AUTH_TOKEN=$(openssl rand -hex 32)

# Pick one auth path:
# (a) Pre-issued Moodle token (recommended for production):
export MOODLE_URL=https://your.moodle.example
export MOODLE_TOKEN=...
# (b) Username + password (server fetches a token at boot):
# export MOODLE_URL=...; export MOODLE_USERNAME=...; export MOODLE_PASSWORD=...
```

In claude.ai → Settings → Connectors → Add custom connector:
- **URL:** `https://<your-host>/mcp`
- **Custom header:** `Authorization: Bearer <MCP_AUTH_TOKEN>`

---

### Option 1: Railway (recommended — no cold start, free tier)

```bash
# 1. Install Railway CLI
npm i -g @railway/cli
railway login

# 2. From the repo root:
railway init
railway up

# 3. Set env vars in the Railway dashboard:
#    MCP_AUTH_TOKEN, MOODLE_URL, MOODLE_TOKEN
# 4. Set the "Start Command" override to:
#    ./moodle-mcp -mode http
#    (or use examples/deploy/railway.json — Railway picks it up automatically)
```

Railway's $5/month free credit comfortably covers a single-user MCP endpoint.

### Option 2: Smithery (MCP-specific registry)

```bash
# 1. Install Smithery CLI
npm i -g @smithery/cli

# 2. From repo root (smithery.yaml is in examples/deploy/):
cp examples/deploy/smithery.yaml .

# 3. Deploy
smithery deploy

# 4. Configure secrets in the Smithery dashboard:
#    MCP_AUTH_TOKEN, MOODLE_URL, MOODLE_TOKEN
```

### Option 3: Google Cloud Run (scale-to-zero, generous free tier)

```bash
gcloud run deploy moodle-mcp \
  --source . \
  --region us-central1 \
  --allow-unauthenticated \
  --port 8080 \
  --command "./moodle-mcp" \
  --args "-mode,http" \
  --set-env-vars "MCP_AUTH_TOKEN=...,MOODLE_URL=...,MOODLE_TOKEN=..."
```

`--allow-unauthenticated` exposes the URL publicly — the bearer middleware enforces access. Cloud Run scales to zero between requests; the first call after idle takes ~1-2 s to cold-start.

### Option 4: Fly.io

```bash
flyctl launch --no-deploy
# In the generated fly.toml, set:
#   [processes]
#   app = "./moodle-mcp -mode http"
#   [env]
#   PORT = "8080"
flyctl secrets set MCP_AUTH_TOKEN=... MOODLE_URL=... MOODLE_TOKEN=...
flyctl deploy
```

---

### Verifying the deployment

```bash
# Health check (no auth required):
curl https://<your-host>/healthz
# → {"status":"ok","version":"1.2.0","mode":"http"}

# Auth gate:
curl -X POST https://<your-host>/mcp -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
# → 401 Unauthorized

curl -X POST https://<your-host>/mcp \
  -H "Authorization: Bearer $MCP_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
# → 200 with the tool list
```

If `/healthz` returns 200 and `/mcp` returns 401 without auth + 200 with auth, paste the URL and bearer token into claude.ai's connector setup.
```

- [ ] **Step 8.2: Commit docs**

```
git add README.md DEPLOYMENT_GUIDE.md
git commit -m "docs: document remote HTTP mode and claude.ai custom connector

- README.md: new \"Remote (HTTP) mode\" section with quick start and a
  flag/env-var reference table.
- DEPLOYMENT_GUIDE.md: new \"claude.ai Custom Connector\" section with
  four deploy recipes (Railway, Smithery, Google Cloud Run, Fly.io)
  plus end-to-end verification curl commands."
```

---

## Task 9: Update Dockerfile + add deploy examples

**Files:**
- Modify: `Dockerfile`
- Create: `examples/deploy/smithery.yaml`
- Create: `examples/deploy/railway.json`

- [ ] **Step 9.1: Update the existing Dockerfile**

Read the current `Dockerfile` (it ends with `CMD ["./moodle-mcp", "-mode", "rest", "-port", "8080"]`).

Replace the entire file with:

```dockerfile
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o moodle-mcp ./cmd/moodle-mcp/

# Final stage
FROM alpine:latest

# Install ca-certificates for HTTPS
RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/moodle-mcp .

# Default port (overridable via PORT env on most cloud platforms)
ENV PORT=8080
EXPOSE 8080

# ENTRYPOINT pins the binary; CMD is the default args (rest mode for backward
# compatibility with existing users). Override CMD at runtime to switch modes:
#   docker run image -mode http
ENTRYPOINT ["./moodle-mcp"]
CMD ["-mode", "rest", "-port", "8080"]
```

Two changes:
1. `golang:1.22-alpine` → `golang:1.23-alpine` (matches `go.mod`'s `go 1.23` directive)
2. `CMD ["./moodle-mcp", ...]` → `ENTRYPOINT ["./moodle-mcp"]` + `CMD ["-mode", "rest", ...]` (lets cloud platforms override mode without rebuilding)

- [ ] **Step 9.2: Build the docker image to verify**

Run (Docker must be running):

```
docker build -t moodle-mcp:test .
```

Expected: build succeeds. (If Docker isn't running on this host, skip this step and verify on the deploy host instead.)

- [ ] **Step 9.3: Create `examples/deploy/smithery.yaml`**

```bash
mkdir -p examples/deploy
```

Create `examples/deploy/smithery.yaml`:

```yaml
# Smithery deploy manifest for moodle-mcp-server in MCP Streamable HTTP mode.
# Deploy with: smithery deploy
#
# Required secrets (configure in Smithery dashboard):
#   - MCP_AUTH_TOKEN  (shared bearer secret for claude.ai connector)
#   - MOODLE_URL      (e.g. https://your.moodle.example)
#   - MOODLE_TOKEN    (Moodle Web Service token; or use MOODLE_USERNAME + MOODLE_PASSWORD)

name: moodle-mcp
runtime: docker
build:
  dockerfile: Dockerfile
start:
  command: ["./moodle-mcp", "-mode", "http"]
env:
  required:
    - MCP_AUTH_TOKEN
    - MOODLE_URL
  optional:
    - MOODLE_TOKEN
    - MOODLE_USERNAME
    - MOODLE_PASSWORD
    - MCP_CORS_ORIGINS
healthcheck:
  path: /healthz
  port: 8080
```

- [ ] **Step 9.4: Create `examples/deploy/railway.json`**

Create `examples/deploy/railway.json`:

```json
{
  "$schema": "https://railway.com/railway.schema.json",
  "build": {
    "builder": "DOCKERFILE",
    "dockerfilePath": "Dockerfile"
  },
  "deploy": {
    "startCommand": "./moodle-mcp -mode http",
    "healthcheckPath": "/healthz",
    "healthcheckTimeout": 30,
    "restartPolicyType": "ON_FAILURE",
    "restartPolicyMaxRetries": 3
  }
}
```

To use: copy this file to the repo root before `railway up`, OR configure these settings via the Railway dashboard.

- [ ] **Step 9.5: Commit**

```
git add Dockerfile examples/deploy/smithery.yaml examples/deploy/railway.json
git commit -m "chore: deploy examples for Smithery + Railway, mode-agnostic Dockerfile

- Dockerfile: bump base to golang:1.23-alpine (match go.mod) and switch
  to ENTRYPOINT/CMD split so the run mode can be overridden at deploy
  time (e.g. 'docker run image -mode http') without rebuilding. Default
  CMD remains -mode rest for backward compatibility.
- examples/deploy/smithery.yaml: Smithery manifest for http mode.
- examples/deploy/railway.json: Railway config for http mode."
```

---

## Task 10: Final verification (full `verification-before-completion`)

**Files:** none (read-only checks)

- [ ] **Step 10.1: Verify the full test suite still passes**

```
go test ./...
```

Expected: all tests pass; no skips, no failures.

- [ ] **Step 10.2: Verify the build succeeds for the production target**

```
go build ./...
go vet ./...
```

Expected: no output.

- [ ] **Step 10.3: Verify the commit graph matches the spec**

```
git log --oneline upstream/main..HEAD
```

Expected output (newest first), 5 commits total — the design-doc commit at the bottom plus 4 feature commits:

```
<sha> chore: deploy examples for Smithery + Railway, mode-agnostic Dockerfile
<sha> docs: document remote HTTP mode and claude.ai custom connector
<sha> feat: add MCP Streamable HTTP transport mode
<sha> chore: bump mark3labs/mcp-go from v0.26.0 to v0.50.0
<sha> docs(superpowers): design spec for Streamable HTTP transport
```

- [ ] **Step 10.4: Verify the spec commit is the only one mentioning superpowers**

```
git log --oneline upstream/main..HEAD -- docs/superpowers/
```

Expected: exactly ONE commit (the design-doc commit). No feature commits should touch `docs/superpowers/`.

---

## Task 11: Push, prepare PR branch (excluding spec), open PR

**Files:** none (git ops only)

The current branch contains 5 commits, but the upstream PR should only include 4 (the spec is internal). We use `git rebase -i` to drop the spec commit from a separate PR-only branch.

- [ ] **Step 11.1: Push the full branch (with spec) to the fork**

```
git push -u origin feat/streamable-http-transport
```

This preserves the spec on the fork as a record. The PR will be opened from a different branch.

- [ ] **Step 11.2: Create the PR branch (without the spec commit)**

```
git checkout -b pr/streamable-http-transport upstream/main
git cherry-pick <sha-of-mcp-go-bump>
git cherry-pick <sha-of-feat-commit>
git cherry-pick <sha-of-docs-commit>
git cherry-pick <sha-of-deploy-commit>
```

Get the SHAs from `git log --oneline feat/streamable-http-transport`. Skip the spec commit; cherry-pick the 4 implementation commits in chronological order.

Verify:

```
git log --oneline upstream/main..HEAD
```

Expected: exactly 4 commits, none mentioning the spec doc.

```
git diff --stat upstream/main..HEAD -- docs/superpowers/
```

Expected: empty (no files in `docs/superpowers/` touched on this branch).

- [ ] **Step 11.3: Push the PR branch to the fork**

```
git push -u origin pr/streamable-http-transport
```

- [ ] **Step 11.4: Open the PR**

```
gh pr create \
  --repo Jawadh-Salih/moodle-mcp-server \
  --base main \
  --head gildeshiro:pr/streamable-http-transport \
  --title "feat: add MCP Streamable HTTP transport for remote clients (claude.ai connectors)" \
  --body "$(cat <<'EOF'
## Summary

Adds a fourth run mode `-mode http` that exposes the MCP server over the Streamable HTTP transport (MCP spec 2025-03-26). This lets the server be used as a remote MCP endpoint by [claude.ai custom connectors](https://support.claude.com/en/articles/11503834), the Claude Apps SDK, and any other client that speaks Streamable HTTP — without a stdio bridge.

## Why

Local MCP modes (stdio) require Claude Desktop or a CLI client. claude.ai web custom connectors only accept HTTPS endpoints speaking MCP Streamable HTTP. This PR closes that gap so a student can host the server on Railway/Smithery/Cloud Run/Fly and connect it to claude.ai web in two clicks.

## What's in the diff

1. **`chore: bump mark3labs/mcp-go from v0.26.0 to v0.50.0`** — required because Streamable HTTP support landed in v0.30.0. The library's `CallToolParams.Arguments` type changed from `map[string]any` to `any` in this range; two access sites in `cmd/moodle-mcp/main.go` switch to `req.GetArguments()`.
2. **`feat: add MCP Streamable HTTP transport mode`** — `internal/server/{streamable,middleware}.go` plus a smoke-test file. Wires `mcp-go`'s `NewStreamableHTTPServer` behind a stdlib middleware chain (`requestLogger → cors → bearerAuth`) on a custom `http.Server` with `/healthz` and 10 s graceful shutdown. New flags: `-auth-token` / `-cors-origins` / `-http-path`. Honors `PORT` env from cloud platforms.
3. **`docs: document remote HTTP mode and claude.ai custom connector`** — README + DEPLOYMENT_GUIDE.
4. **`chore: deploy examples for Smithery + Railway, mode-agnostic Dockerfile`** — `examples/deploy/{smithery.yaml,railway.json}` plus an ENTRYPOINT/CMD split in `Dockerfile` so deploy platforms can override `-mode` without rebuilding. Existing default behavior (`-mode rest`) preserved.

## Backwards compatibility

The existing `mcp` (stdio), `rest` (custom HTTP API for ChatGPT/Gemini), and `both` modes are completely untouched. The new `http` mode is opt-in.

## Security

- `MCP_AUTH_TOKEN` is REQUIRED in `http` mode — server refuses to boot without it.
- Bearer comparison is constant-time (`crypto/subtle`).
- `/healthz` is unauthenticated (cloud LBs).
- No request bodies are logged (Moodle tokens / PII protection).

## Tests

- `internal/server/streamable_test.go` covers: healthz returns 200 without auth; bearer rejects missing header; bearer rejects wrong token; bearer accepts correct token; CORS preflight headers correct for allowed origin; CORS silent for disallowed origin.
- Manual smoke against a real Moodle instance: ✅

## Open questions

- Should `MCP_AUTH_TOKEN` be relaxable via a flag (`-allow-no-auth`) for trusted-network deploys? I left it default-secure.
- Should the `login` tool auto-disable when `MOODLE_TOKEN` is set in env? Probably yes, but I'd rather do that in a follow-up so this PR stays focused on the transport.
- A future PR could add OAuth 2.1 + DCR for full MCP spec compliance with public hosting.

## Test plan for reviewers

```bash
# 1. Build
go build ./...

# 2. Run tests
go test ./...

# 3. Manual smoke
export MCP_AUTH_TOKEN=test123
export MOODLE_URL=https://your.moodle.example
export MOODLE_TOKEN=...
./moodle-mcp -mode http -port 8080 &

curl http://localhost:8080/healthz                                # → 200
curl -X POST http://localhost:8080/mcp                            # → 401
curl -X POST http://localhost:8080/mcp -H "Authorization: Bearer test123" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'             # → 200 with tool list
```

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 11.5: Capture PR URL**

The previous command prints a URL. Copy it. Run:

```
gh pr view --repo Jawadh-Salih/moodle-mcp-server <pr-number>
```

Expected: PR is open, base `main`, head `gildeshiro:pr/streamable-http-transport`, 4 commits, files-changed list matches the spec.

---

## Self-review checklist (run after writing the plan, before kicking off execution)

**Spec coverage:**
- ✅ Goal (add `-mode http` for claude.ai connectors) — Tasks 5, 6
- ✅ Non-goals respected — no OAuth, no multi-tenant, no main.go refactor, no rest mode merge
- ✅ Bearer auth + boot guardrail — Task 2 (impl), Task 6.8 (negative test)
- ✅ CORS + MCP-Session-Id header — Task 3
- ✅ /healthz no auth — Task 2 + Task 5
- ✅ Graceful shutdown 10s — Task 5
- ✅ Request logging without bodies — Task 4
- ✅ Login tool warning in description — Task 6.0
- ✅ mcp-go v0.50.0 bump + Arguments fix — Task 1
- ✅ Dockerfile ENTRYPOINT — Task 9
- ✅ examples/deploy/{smithery.yaml,railway.json} — Task 9
- ✅ README + DEPLOYMENT_GUIDE — Tasks 7, 8
- ✅ 4-commit PR layout — Tasks 1, 6, 8, 9
- ✅ Spec excluded from PR — Task 11

**Placeholder scan:** No "TBD", "TODO", "fill in", "similar to" — every code block contains the actual code an engineer would paste.

**Type consistency:**
- `BearerAuth(token string) func(http.Handler) http.Handler` — same signature in Tasks 2, 5
- `CORS(origins []string)` — same in Tasks 3, 5
- `RequestLogger()` — same in Tasks 4, 5
- `RunStreamable(ctx, mcp, opts)` returns `error` — used consistently in Task 6.5
- `StreamableOpts` fields (`Port`, `AuthToken`, `CORSOrigins`, `Path`, `Version`) — defined in Task 5, used in Task 6.5

No drift detected.
