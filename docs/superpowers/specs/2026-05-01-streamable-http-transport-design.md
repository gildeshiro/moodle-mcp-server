# MCP Streamable HTTP Transport for moodle-mcp-server

**Date:** 2026-05-01
**Status:** Approved (pending user review of this spec)
**Target upstream:** `Jawadh-Salih/moodle-mcp-server`
**Working fork:** `gildeshiro/moodle-mcp-server`
**Branch:** `feat/streamable-http-transport`

## Goal

Add MCP Streamable HTTP transport to `moodle-mcp-server` so it can be used as a remote MCP endpoint by clients that don't speak stdio — primarily Anthropic's claude.ai custom connectors. The existing `mcp` (stdio) and `rest` (custom REST API for ChatGPT/Gemini) modes remain untouched.

## Non-goals

- OAuth 2.1 / Dynamic Client Registration (overkill for this codebase; bearer secret is sufficient)
- Multi-tenant per-session Moodle auth (would require refactoring `api.Client` from singleton to per-session — out of scope for this PR)
- Replacing or merging the existing `rest` mode (it serves a different audience)
- Refactoring `cmd/moodle-mcp/main.go` size (separate PR if author wants it)
- Adding a test harness for the rest of the codebase (the repo currently has no tests; we add only a smoke test for the new transport)

## Context

`moodle-mcp-server` is a Go binary that exposes Moodle student-side capabilities (courses, grades, assignments, deadlines, notifications, file download, journal/assignment submission) as MCP tools. It currently pins `mark3labs/mcp-go` v0.26.0, which **predates** Streamable HTTP support in that library (added in v0.30.0). This PR bumps the dep to v0.50.0 (latest stable as of 2026-04-30) which ships `server.NewStreamableHTTPServer` natively. The bump is API-compatible with the existing tool registrations (`NewTool`, `ParseString`, `NewToolResultText`, `NewToolResultError`, `WithToolCapabilities` all unchanged). The upstream supports two run modes today:

- `-mode mcp` → stdio transport for Claude Desktop / Claude Code
- `-mode rest` → custom non-MCP REST API on port 8080 for ChatGPT custom GPTs and Gemini

There is no remote MCP path. Anthropic's claude.ai custom connectors require an HTTPS URL speaking MCP Streamable HTTP. This PR closes that gap.

## Architecture

### Mode flag and entrypoint

`cmd/moodle-mcp/main.go` adds `http` to the `-mode` switch and dispatches to a new function:

```
case "http":
    runStreamableHTTPServer(client, httpOpts{
        Port:        *port,
        AuthToken:   *authToken,
        CORSOrigins: *corsOrigins,
        Path:        *httpPath,
    })
```

New flags (env-var fallbacks in parens):

| Flag | Env | Default | Purpose |
|---|---|---|---|
| `-auth-token` | `MCP_AUTH_TOKEN` | empty (boot fails) | Bearer secret |
| `-cors-origins` | `MCP_CORS_ORIGINS` | empty | Comma-separated origins |
| `-http-path` | `MCP_HTTP_PATH` | `/mcp` | Endpoint path |

The existing `-port` flag is reused. We additionally honor the `PORT` env var if set (Railway, Render, Fly, Cloud Run inject it automatically).

### New files

```
internal/server/
├── rest.go             [unchanged]
├── streamable.go       [new — http.Server wiring + handler chain]
└── middleware.go       [new — bearerAuth, cors, requestLogger]
```

`internal/server/streamable.go` exports a single function:

```go
func RunStreamable(ctx context.Context, mcp *mcpserver.MCPServer, opts StreamableOpts) error
```

It builds an `http.ServeMux`, mounts `GET /healthz` (no auth), mounts the streamable MCP handler at `opts.Path` wrapped in `requestLogger → cors → bearerAuth`, and starts an `http.Server` with graceful shutdown on `SIGTERM` / `SIGINT` (10s drain).

The streamable handler is `mcpserver.NewStreamableHTTPServer(mcp, mcpserver.WithEndpointPath(opts.Path))`. The returned `*StreamableHTTPServer` implements `http.Handler` (has `ServeHTTP`), so it composes with the stdlib middleware chain. We do NOT use its built-in `Start(addr)` because we want our own `http.Server` with custom shutdown timeout and the `/healthz` endpoint mounted on the same listener.

### Handler chain

```
incoming request
  ↓
requestLogger      — logs method, path, status, duration, remote
  ↓
cors               — handles OPTIONS preflight; sets Allow-* headers if origin matches
  ↓
bearerAuth         — constant-time-compares Authorization header; 401 on miss
  ↓
streamableHandler  — mcp-go's NewStreamableHTTPServer wraps the MCPServer
```

`/healthz` skips the chain entirely and returns:

```json
{"status":"ok","version":"1.2.0","mode":"http"}
```

## Auth model

Single-tenant: one server process, one Moodle account, gated by a bearer secret.

- Server boots with `MOODLE_TOKEN` (or username/password) in env, OR a client calls the `login` tool
- Server boots with `MCP_AUTH_TOKEN` set, OR refuses to start in `http` mode
- Every request must carry `Authorization: Bearer <MCP_AUTH_TOKEN>`
- Constant-time comparison via `crypto/subtle.ConstantTimeCompare`
- Failure: `401` with `WWW-Authenticate: Bearer realm="moodle-mcp"`
- `/healthz` and CORS preflight `OPTIONS` skip auth

### `login` tool in remote mode

The existing `login` tool accepts Moodle credentials as arguments. In remote HTTP mode those credentials transit Claude → server. We keep the tool available because some users need it (MFA-issued mobile tokens), but the description gains a one-line warning steering users toward server-side `MOODLE_TOKEN` configuration. Operators can disable the tool by setting `MOODLE_TOKEN` and never invoking `login`.

## CORS

Default off (server-to-server calls from claude.ai backend don't need CORS). When `-cors-origins` is set:

- Allowed origins: exact match against the configured list
- Allowed methods: `GET, POST, DELETE, OPTIONS`
- Allowed headers: `Authorization, Content-Type, MCP-Session-Id`
- `Access-Control-Max-Age: 86400`
- Wildcard `*` accepted but explicitly logged with a warning at boot

`MCP-Session-Id` is required by the MCP Streamable HTTP spec for session continuity.

## Logging

One structured line per request, plain text to stdout:

```
2026-05-01T14:23:11Z method=POST path=/mcp status=200 dur=142ms remote=1.2.3.4
```

No request body (may contain Moodle tokens or PII). No response body. The auth header is never logged. Format chosen for grep-ability and compatibility with any aggregator (Cloud Run logs, Railway logs, Fly logs, Loki, Vector, etc.).

## Graceful shutdown

`SIGTERM` / `SIGINT` triggers `http.Server.Shutdown(ctx)` with a 10-second drain. This prevents 502s during cloud platform deploys (Cloud Run, Railway).

## Documentation changes

### `README.md`

Add a "Remote (HTTP) mode" section after the existing setup sections:

```
## Remote (HTTP) mode — for claude.ai custom connectors

The server can also run as a remote MCP endpoint over Streamable HTTP.

    export MOODLE_URL=https://your.moodle.example
    export MOODLE_TOKEN=$(your-moodle-token)
    export MCP_AUTH_TOKEN=$(openssl rand -hex 32)

    ./moodle-mcp -mode http -port 8080 -cors-origins "https://claude.ai,https://claude.com"

Then in claude.ai → Settings → Connectors → Add custom connector:

  URL:     https://your-deployment.example/mcp
  Header:  Authorization: Bearer <your MCP_AUTH_TOKEN>

See DEPLOYMENT_GUIDE.md for cloud platform recipes.
```

### `DEPLOYMENT_GUIDE.md`

New section "claude.ai Custom Connector" with quick-start recipes for:

- Railway (recommended — no cold start, free tier suitable for one student)
- Smithery (MCP-specific registry hosting)
- Google Cloud Run (scale-to-zero free tier)
- Fly.io (small-app free tier)

Each recipe is ~10 lines: env vars, deploy command, claude.ai config snippet.

### Existing `Dockerfile` — minor updates

The repo already has a Dockerfile that hardcodes `-mode rest`. We update it to:

- Bump base from `golang:1.22-alpine` to `golang:1.23-alpine` (match `go.mod`)
- Switch from `CMD ["./moodle-mcp", "-mode", "rest", "-port", "8080"]` to `ENTRYPOINT ["./moodle-mcp"]` + `CMD ["-mode", "rest", "-port", "8080"]`

This lets cloud platforms override the mode without rebuilding: `docker run image -mode http`. Default behavior (REST mode) is preserved for users who haven't read the changelog.

### `examples/deploy/` (new directory)

Two short manifests:

- `smithery.yaml` — Smithery deploy manifest (declares the http mode + required env vars)
- `railway.json` — Railway deploy config (build from Dockerfile, override CMD to use http mode)

Each is ~20 lines. Documented in `DEPLOYMENT_GUIDE.md`.

## Testing

The repo currently has zero tests. We do not introduce a full test harness. We add **one** smoke test:

`internal/server/streamable_test.go` — boots the streamable server in-process with a mock `*mcpserver.MCPServer`, makes three HTTP calls:

1. `GET /healthz` without auth → expect 200
2. `POST /mcp` without auth → expect 401
3. `POST /mcp` with valid bearer → expect 200 (or at least not 401)

Goal: catch regressions in the middleware chain. Not exhaustive.

## PR strategy

Four commits on `feat/streamable-http-transport`:

1. `chore: bump mark3labs/mcp-go from v0.26.0 to v0.50.0` — `go.mod`, `go.sum` (isolated dep bump for easy review)
2. `feat: add MCP Streamable HTTP transport mode` — `cmd/moodle-mcp/main.go`, `internal/server/streamable.go`, `internal/server/middleware.go`, `internal/server/streamable_test.go`
3. `docs: document remote HTTP mode and claude.ai custom connector setup` — `README.md`, `DEPLOYMENT_GUIDE.md`
4. `chore: add deploy examples + make Dockerfile mode-agnostic` — `Dockerfile`, `examples/deploy/smithery.yaml`, `examples/deploy/railway.json`

PR title: `feat: add MCP Streamable HTTP transport for remote clients (claude.ai connectors)`

PR body:
- What: adds a fourth run mode `-mode http` that exposes the MCP server over Streamable HTTP per the 2025-03-26 spec
- Why: lets users connect the server to claude.ai custom connectors and other remote MCP clients without a stdio bridge
- Backwards-compat: existing `mcp`, `rest`, `both` modes are unchanged; new mode is opt-in
- Security: requires `MCP_AUTH_TOKEN`; refuses to boot without it
- Tests: smoke test for the new middleware chain
- Docs: deploy recipes for Railway, Smithery, Cloud Run, Fly

## Verification before opening PR

```
go build ./...
go vet ./...
go test ./internal/server/...

# manual smoke against a real Moodle:
export MOODLE_URL=...; export MOODLE_TOKEN=...; export MCP_AUTH_TOKEN=test123
./moodle-mcp -mode http &
curl -s http://localhost:8080/healthz
curl -s -H "Authorization: Bearer test123" -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

## Open questions for the upstream author

These are left as PR discussion points, not blockers:

- Should `MCP_AUTH_TOKEN` requirement be enforceable via a flag (e.g., `-allow-no-auth` for trusted networks)? Default-secure says no.
- Should the `login` tool be auto-disabled when `MOODLE_TOKEN` is set in env? Probably yes, but added in a follow-up.
- Future PR could add OAuth 2.1 + DCR for full MCP spec compliance with public hosting.
