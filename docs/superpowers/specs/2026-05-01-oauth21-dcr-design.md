# OAuth 2.1 + Dynamic Client Registration

**Date:** 2026-05-01
**Status:** Approved (user pre-authorized autonomous execution)
**Working fork:** `gildeshiro/moodle-mcp-server`
**Branch:** `feat/streamable-http-transport`

## Goal

Replace the URL-as-secret deploy model with proper MCP-spec-compliant OAuth 2.1 authorization, so the claude.ai web custom-connector UI's OAuth fields actually do something meaningful and the server is no longer compromised by a leaked Railway URL. Implement single-tenant auto-approve OAuth: one server = one Moodle account, every authorize request is auto-approved (the deploy operator IS the user), tokens are issued and validated server-side.

When complete, the user will:
1. Set `MCP_USE_OAUTH=1` + `MCP_OAUTH_ISSUER=https://...railway.app` in Railway, remove `MCP_DISABLE_AUTH=1`
2. Re-add the connector in claude.ai with the same URL
3. claude.ai discovers the OAuth endpoints, performs DCR, runs the auth flow (auto-approved), receives an access token, and proceeds normally
4. The Railway URL is no longer the secret — only valid access tokens grant access

## Non-goals

- User-consent UI (auto-approve is correct for single-tenant deploys)
- Multi-tenant OAuth (one server = one Moodle account; no per-user account selection)
- Refresh tokens (claude.ai will re-DCR transparently when access tokens expire)
- JWT-format tokens (opaque random strings are simpler and revocable)
- Token persistence across restarts (in-memory only; restart forces claude.ai to redo DCR — invisible to the user)
- Full RFC 7591 client metadata fields (only the subset claude.ai actually uses)
- OAuth scopes enforcement (claude.ai requests broad scopes; we accept all)

## Context

The MCP 2025-03-26 specification's authorization section ([modelcontextprotocol.io/specification/2025-03-26/basic/authorization](https://modelcontextprotocol.io/specification/2025-03-26/basic/authorization)) defines a discovery-driven OAuth 2.1 flow built on:
- RFC 8414 (Authorization Server Metadata) — `/.well-known/oauth-authorization-server`
- RFC 9728 (Protected Resource Metadata) — `/.well-known/oauth-protected-resource`
- RFC 7591 (Dynamic Client Registration) — `POST /register` (or any registration endpoint declared in the AS metadata)
- RFC 7636 (PKCE) — required, S256 only

The current server has three auth modes: bearer-static (`MCP_AUTH_TOKEN`), no-auth (`MCP_DISABLE_AUTH=1`), and an implicit "boot fails without one of the two." This adds a fourth mode: full OAuth 2.1 + DCR.

## Architecture

### Auth-mode resolution

Server boot determines the auth mode from env:

| Env vars set | Mode | Notes |
|---|---|---|
| `MCP_USE_OAUTH=1` (and `MCP_OAUTH_ISSUER`) | OAuth 2.1 + DCR | New |
| `MCP_AUTH_TOKEN=<secret>` | Bearer-static | Existing |
| `MCP_DISABLE_AUTH=1` | No auth | Existing |
| (none of the above) | Boot fails | Existing |
| Multiple of the above | Boot fails with clear error | New guardrail |

When `MCP_USE_OAUTH=1`, `MCP_OAUTH_ISSUER` is required (the publicly reachable base URL the server is deployed at, e.g. `https://moodle-mcp-server-production-0473.up.railway.app`). The issuer URL is used in:
- `iss` field of discovery metadata
- `WWW-Authenticate` `resource_metadata=` URL
- `authorization_endpoint` and `token_endpoint` URLs in discovery

### File layout

```
internal/oauth/
├── provider.go      Provider struct: in-memory client/code/token stores + helpers
├── discovery.go     /.well-known/oauth-authorization-server handler
├── resource.go      /.well-known/oauth-protected-resource handler
├── register.go      DCR endpoint (POST /oauth/register)
├── authorize.go     Authorization endpoint (GET /oauth/authorize)
├── token.go         Token endpoint (POST /oauth/token)
├── middleware.go    BearerOAuth middleware that validates tokens against the provider
└── pkce.go          PKCE S256 verifier (sha256(verifier) == base64url-decode(code_challenge))

internal/server/
├── streamable.go    EXTEND: when AuthMode == "oauth", wire OAuth endpoints + use BearerOAuth middleware

cmd/moodle-mcp/
└── main.go          EXTEND: parse MCP_USE_OAUTH + MCP_OAUTH_ISSUER, pass into StreamableOpts
```

### `internal/oauth/provider.go`

```go
type Provider struct {
    issuer string

    mu      sync.RWMutex
    clients map[string]*Client       // client_id -> client info
    codes   map[string]*AuthCode     // authorization codes (10-min TTL)
    tokens  map[string]*AccessToken  // access tokens (24-hour TTL)
}

type Client struct {
    ID           string
    RedirectURIs []string
    Name         string
    Created      time.Time
}

type AuthCode struct {
    Code                string
    ClientID            string
    RedirectURI         string
    CodeChallenge       string
    CodeChallengeMethod string
    Scope               string
    Expires             time.Time
}

type AccessToken struct {
    Token    string
    ClientID string
    Scope    string
    Expires  time.Time
}

func NewProvider(issuer string) *Provider
func (p *Provider) RegisterClient(redirectURIs []string, name string) *Client
func (p *Provider) IssueCode(clientID, redirectURI, codeChallenge, method, scope string) string
func (p *Provider) ExchangeCode(code, codeVerifier, clientID, redirectURI string) (*AccessToken, error)
func (p *Provider) ValidateToken(tok string) (*AccessToken, bool)
func (p *Provider) StartGC(ctx context.Context)  // periodic cleanup of expired entries
```

The provider runs a goroutine that sweeps expired auth codes (every 60s) and tokens (every 5min) under the same context as the HTTP server, so it shuts down cleanly.

### Auto-approve flow

`/oauth/authorize` is GET-only. It validates inputs (`client_id`, `redirect_uri`, `code_challenge`, `code_challenge_method=S256`, `state`, `scope`), issues an authorization code, and 302-redirects the browser back to `redirect_uri?code=<code>&state=<state>`.

No HTML consent page — for single-tenant deploys, the only person hitting this URL is the deploy owner authenticating their own client. If the deploy is exposed and an attacker tries to authorize, they'd need to know a registered `client_id` AND control its `redirect_uri`. The strongest defense remains the issuer URL itself (still secret-ish — at least not a guessable name) and the fact that any successful auth ends up redirecting to claude.ai's domain.

### Token validation middleware

`oauth.BearerOAuth(provider)` is the OAuth-mode replacement for `server.BearerAuth`. It:
- Extracts `Authorization: Bearer <token>`
- Calls `provider.ValidateToken(token)`
- On success: passes through
- On failure: returns `401` with `WWW-Authenticate: Bearer resource_metadata="<issuer>/.well-known/oauth-protected-resource"` so the spec-compliant client knows where to discover auth.

OPTIONS preflight passes through (existing CORS pattern).

### Streamable wiring

`internal/server/streamable.go`'s `StreamableOpts` gains:

```go
OAuthProvider *oauth.Provider // when non-nil, use OAuth mode (replaces AuthToken)
```

When `OAuthProvider != nil`:
- Mount `/.well-known/oauth-authorization-server`, `/.well-known/oauth-protected-resource`, `/oauth/register`, `/oauth/authorize`, `/oauth/token` at top level (NOT behind auth — these ARE the auth bootstrap)
- Use `oauth.BearerOAuth(provider)` instead of `server.BearerAuth(token)` on the `/mcp` path
- The OAuth endpoints DO get CORS treatment (claude.ai's browser client may hit `/.well-known/...` directly)
- The OAuth endpoints DO get request-logger treatment (visibility)

`RunStreamable` updates the boot-guard logic: requires exactly one of `AuthToken`, `AllowNoAuth`, or `OAuthProvider` to be set.

### Discovery metadata payload

`GET /.well-known/oauth-authorization-server` returns:

```json
{
  "issuer": "<issuer>",
  "authorization_endpoint": "<issuer>/oauth/authorize",
  "token_endpoint": "<issuer>/oauth/token",
  "registration_endpoint": "<issuer>/oauth/register",
  "response_types_supported": ["code"],
  "grant_types_supported": ["authorization_code"],
  "code_challenge_methods_supported": ["S256"],
  "token_endpoint_auth_methods_supported": ["none"]
}
```

`token_endpoint_auth_methods_supported: ["none"]` because we use PKCE-public-client model — clients aren't issued secrets, only an ID. PKCE itself is the proof-of-possession.

### Resource metadata payload

`GET /.well-known/oauth-protected-resource` returns:

```json
{
  "resource": "<issuer>/mcp",
  "authorization_servers": ["<issuer>"]
}
```

Per RFC 9728, this tells the client which authorization servers protect this resource.

### DCR endpoint

`POST /oauth/register` accepts `application/json`:

```json
{
  "client_name": "claude.ai connector",
  "redirect_uris": ["https://claude.ai/api/mcp/auth_callback"]
}
```

Returns `201 Created`:

```json
{
  "client_id": "<random opaque>",
  "client_id_issued_at": <unix>,
  "redirect_uris": [...],
  "client_name": "claude.ai connector",
  "token_endpoint_auth_method": "none"
}
```

Validation:
- `redirect_uris` array must be non-empty
- Each redirect URI must be `https://` or `http://localhost*`

We generate `client_id` as 32 hex chars from `crypto/rand`. No client_secret is issued (public client + PKCE).

### Authorization endpoint

`GET /oauth/authorize?client_id=<id>&redirect_uri=<uri>&response_type=code&code_challenge=<chall>&code_challenge_method=S256&state=<state>&scope=<scope>`

Server validates:
- `response_type == "code"` (else error)
- `client_id` exists in registry (else error)
- `redirect_uri` is in the client's registered redirect_uris (else error — security critical)
- `code_challenge_method == "S256"` (else error)
- `code_challenge` is non-empty
- `state` is recommended but not required (we pass it through verbatim)

On success:
- Generate a 32-hex-char `code` value
- Store `AuthCode{code, client_id, redirect_uri, code_challenge, ...}` with 10-min expiration
- 302 redirect to `<redirect_uri>?code=<code>&state=<state>`

On failure: redirect to `<redirect_uri>?error=invalid_request&error_description=<msg>&state=<state>` per OAuth spec (assumes redirect_uri itself is valid; otherwise return 400 directly).

### Token endpoint

`POST /oauth/token` accepts `application/x-www-form-urlencoded`:

```
grant_type=authorization_code
code=<code>
redirect_uri=<uri>
client_id=<id>
code_verifier=<verifier>
```

Validation:
- `grant_type == "authorization_code"`
- `code` exists, not expired, matches `client_id` and `redirect_uri`
- `sha256(code_verifier)` (base64-url-encoded, no padding) equals stored `code_challenge`

On success: delete the code (single-use), issue a 32-hex-char access token, store with 24-hour expiration, return:

```json
{
  "access_token": "<token>",
  "token_type": "Bearer",
  "expires_in": 86400,
  "scope": "<scope>"
}
```

On failure: HTTP 400 with `{"error": "invalid_grant", "error_description": "..."}`.

### PKCE verification

```go
func VerifyChallenge(verifier, challenge string) bool {
    h := sha256.Sum256([]byte(verifier))
    encoded := base64.RawURLEncoding.EncodeToString(h[:])
    return subtle.ConstantTimeCompare([]byte(encoded), []byte(challenge)) == 1
}
```

`base64.RawURLEncoding` is the no-padding URL-safe encoding RFC 7636 mandates.

## Error handling

- Storage operations are lock-protected (sync.RWMutex on the Provider)
- Expired codes/tokens are visible to lookups until the GC sweeps them; lookups always re-check expiration
- Network/parse errors on incoming requests return `400` with a JSON OAuth error
- All endpoints set `Cache-Control: no-store` on responses (security best practice for token-related endpoints)

## Tool description / claude.ai connector setup

Once deployed, the `read_resource` tool description doesn't change. What changes is the connector setup flow:

1. claude.ai → Settings → Connectors → Custom connectors → delete the existing "Moodle CECIERJ" entry
2. Add custom connector again with the same URL `https://moodle-mcp-server-production-0473.up.railway.app/mcp`
3. **Leave OAuth Client ID and OAuth Client Secret empty** — claude.ai will discover and DCR automatically
4. Click "Add"
5. claude.ai walks the discovery + DCR + authorize + token flow; on the auto-approve, the user sees a brief "Approve access?" UI from claude.ai's side, clicks approve, and the connector lights up

The user's URL is no longer "the secret" — leaks of the URL alone are no longer compromising. An attacker would need to also complete the OAuth flow which only succeeds via legitimate claude.ai client redirect.

## Testing

- **Unit-test the PKCE verifier** with known-good test vectors (verifier → S256 challenge per RFC 7636 Appendix B).
- **Unit-test the Provider** for: client registration, code issuance, code exchange (happy path + expired code + wrong PKCE + reused code), token validation (happy path + expired token).
- **Integration-test the HTTP endpoints** by mounting them on an `httptest.Server` and walking the full discovery → register → authorize (302) → token flow with a synthetic PKCE pair.
- **Manual end-to-end** in production: change Railway env from `MCP_DISABLE_AUTH=1` to `MCP_USE_OAUTH=1` + `MCP_OAUTH_ISSUER=https://moodle-mcp-server-production-0473.up.railway.app`. Re-add connector in claude.ai. Confirm the flow completes.

## Deployment

Same as previous rounds:

1. Commit on `feat/streamable-http-transport`
2. Push to fork
3. Force-push to `main` → Railway redeploys
4. **Manual env-var swap on Railway**: remove `MCP_DISABLE_AUTH=1`, add `MCP_USE_OAUTH=1` and `MCP_OAUTH_ISSUER=https://moodle-mcp-server-production-0473.up.railway.app`. Railway auto-redeploys.
5. User re-adds connector in claude.ai (deletes old, re-adds; OAuth fields stay empty).

## Open follow-ups (not in this round)

- Persistent token storage (e.g., a small SQLite or even just disk-backed JSON) so Railway restarts don't force claude.ai to redo DCR. Low priority — DCR is invisible to the user.
- Multi-tenant Moodle accounts — would require per-token `Moodle credentials` rather than the single env-var pinned client. Significant refactor.
- Refresh tokens — minor improvement; current 24h access token life with transparent re-DCR is sufficient.
