package oauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TTLs. Authorization codes are short-lived per RFC 6749 § 4.1.2 ("a maximum
// authorization code lifetime of 10 minutes is RECOMMENDED"). Access tokens
// are 24h to keep the demo flow simple — refresh_token is not implemented.
const (
	authCodeTTL    = 10 * time.Minute
	accessTokenTTL = 24 * time.Hour

	gcCodeInterval  = 60 * time.Second
	gcTokenInterval = 5 * time.Minute
)

// Sentinel errors returned by the Provider methods. HTTP handlers map these to
// the OAuth-spec error codes (invalid_grant, invalid_client, etc.).
var (
	ErrUnknownClient        = errors.New("unknown client_id")
	ErrInvalidRedirectURI   = errors.New("redirect_uri not registered for client")
	ErrUnsupportedChallenge = errors.New("only code_challenge_method=S256 is supported")
	ErrMissingChallenge     = errors.New("code_challenge is required")
	ErrUnknownCode          = errors.New("authorization code not found or already used")
	ErrCodeExpired          = errors.New("authorization code expired")
	ErrCodeClientMismatch   = errors.New("authorization code was issued to a different client")
	ErrCodeRedirectMismatch = errors.New("redirect_uri does not match the value used at /authorize")
	ErrPKCEMismatch         = errors.New("code_verifier does not satisfy code_challenge")
)

// Client is a dynamically-registered OAuth client. token_endpoint_auth_method
// is implicitly "none" — public clients only, since DCR with no operator
// review can't issue real client secrets safely.
type Client struct {
	ID           string   `json:"id"`
	RedirectURIs []string `json:"redirect_uris"`
	Name         string   `json:"name,omitempty"`
	IssuedAtUnix int64    `json:"issued_at_unix"`
}

// AuthCode is a single-use authorization-code grant tied to a client+redirect
// pair, with PKCE state captured at /authorize time.
type AuthCode struct {
	Code          string    `json:"code"`
	ClientID      string    `json:"client_id"`
	RedirectURI   string    `json:"redirect_uri"`
	CodeChallenge string    `json:"code_challenge"` // S256 only (method enforced at issue time)
	Scope         string    `json:"scope,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// AccessToken is the bearer credential used at /mcp. Opaque (no JWT claims).
type AccessToken struct {
	Token     string    `json:"token"`
	ClientID  string    `json:"client_id"`
	Scope     string    `json:"scope,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

// persistedState is the on-disk JSON shape used when MCP_OAUTH_STATE_FILE is
// set. It bundles all state-bearing maps so a single atomic write covers them.
// Expired codes / tokens are pruned during load so the in-memory maps never
// contain dead entries.
type persistedState struct {
	Clients map[string]*Client      `json:"clients"`
	Codes   map[string]*AuthCode    `json:"codes"`
	Tokens  map[string]*AccessToken `json:"tokens"`
}

// Provider holds the OAuth state for the single-tenant server. By default
// storage is map+mutex only and restart wipes everything; with statePath set,
// state is mirrored to a JSON file (atomic write via tempfile + rename) so
// dynamically-registered clients survive process restarts.
type Provider struct {
	issuer    string
	statePath string

	mu           sync.RWMutex
	clients      map[string]*Client
	codes        map[string]*AuthCode
	accessTokens map[string]*AccessToken
}

// NewProvider returns a Provider whose discovery endpoints will advertise the
// given public base URL as the issuer. issuer should NOT end with a slash.
//
// When statePath is non-empty the provider mirrors clients/codes/tokens to a
// JSON file at that path: on construction the file (if any) is loaded and
// expired entries are dropped; after every state-changing operation
// (RegisterClient, IssueCode, ExchangeCode, GC sweep) the file is rewritten
// atomically (tempfile in the same directory + os.Rename). When statePath is
// empty the persistence layer is a no-op and behavior is unchanged from the
// pre-persistence implementation.
func NewProvider(issuer, statePath string) *Provider {
	p := &Provider{
		issuer:       strings.TrimRight(issuer, "/"),
		statePath:    statePath,
		clients:      make(map[string]*Client),
		codes:        make(map[string]*AuthCode),
		accessTokens: make(map[string]*AccessToken),
	}
	p.loadState()
	return p
}

// Issuer returns the public base URL used for discovery and as the iss claim.
func (p *Provider) Issuer() string { return p.issuer }

// randHex returns 2*n hex characters from crypto/rand. Panics on rand failure
// because the OS RNG being broken is not a recoverable condition here.
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("oauth: crypto/rand.Read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// validRedirectURI checks the redirect URI against the MCP spec rules:
// HTTPS or http://localhost (loopback). RFC 8252 § 7.3.
func validRedirectURI(uri string) bool {
	if uri == "" {
		return false
	}
	if strings.HasPrefix(uri, "https://") {
		return true
	}
	if strings.HasPrefix(uri, "http://localhost") || strings.HasPrefix(uri, "http://127.0.0.1") {
		return true
	}
	return false
}

// RegisterClient creates a new public client with the given redirect URIs and
// optional display name. Each redirect URI must be HTTPS or loopback HTTP.
// Returns the persisted client (caller serializes to JSON).
func (p *Provider) RegisterClient(redirectURIs []string, name string) (*Client, error) {
	if len(redirectURIs) == 0 {
		return nil, errors.New("redirect_uris must be a non-empty array")
	}
	for _, u := range redirectURIs {
		if !validRedirectURI(u) {
			return nil, errors.New("redirect_uri must use https:// or http://localhost: " + u)
		}
	}

	c := &Client{
		ID:           randHex(16),
		RedirectURIs: append([]string(nil), redirectURIs...),
		Name:         name,
		IssuedAtUnix: time.Now().Unix(),
	}
	p.mu.Lock()
	p.clients[c.ID] = c
	p.mu.Unlock()
	p.persist()
	return c, nil
}

// GetClient looks up a registered client by ID.
func (p *Provider) GetClient(id string) (*Client, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c, ok := p.clients[id]
	return c, ok
}

// clientHasRedirect uses a constant-time compare per registered URI to avoid
// length-based timing distinguishers between registered URIs.
func clientHasRedirect(c *Client, uri string) bool {
	got := []byte(uri)
	match := false
	for _, r := range c.RedirectURIs {
		if subtle.ConstantTimeCompare([]byte(r), got) == 1 {
			match = true
		}
	}
	return match
}

// IssueCode validates the authorize-time params and persists a single-use code.
// Caller is responsible for redirecting the user-agent back to redirectURI.
func (p *Provider) IssueCode(clientID, redirectURI, codeChallenge, method, scope string) (string, error) {
	c, ok := p.GetClient(clientID)
	if !ok {
		return "", ErrUnknownClient
	}
	if !clientHasRedirect(c, redirectURI) {
		return "", ErrInvalidRedirectURI
	}
	if method != "S256" {
		return "", ErrUnsupportedChallenge
	}
	if codeChallenge == "" {
		return "", ErrMissingChallenge
	}

	code := randHex(16)
	ac := &AuthCode{
		Code:          code,
		ClientID:      clientID,
		RedirectURI:   redirectURI,
		CodeChallenge: codeChallenge,
		Scope:         scope,
		ExpiresAt:     time.Now().Add(authCodeTTL),
	}
	p.mu.Lock()
	p.codes[code] = ac
	p.mu.Unlock()
	p.persist()
	return code, nil
}

// ExchangeCode redeems a single-use authorization code for an access token,
// verifying client_id, redirect_uri, and PKCE. The code is deleted before the
// access token is returned so a replay sees ErrUnknownCode.
func (p *Provider) ExchangeCode(code, codeVerifier, clientID, redirectURI string) (*AccessToken, error) {
	p.mu.Lock()
	ac, ok := p.codes[code]
	if !ok {
		p.mu.Unlock()
		return nil, ErrUnknownCode
	}
	// Delete-first: even if validation below fails, prevent replay attempts.
	delete(p.codes, code)
	p.mu.Unlock()

	if time.Now().After(ac.ExpiresAt) {
		return nil, ErrCodeExpired
	}
	if subtle.ConstantTimeCompare([]byte(ac.ClientID), []byte(clientID)) != 1 {
		return nil, ErrCodeClientMismatch
	}
	if subtle.ConstantTimeCompare([]byte(ac.RedirectURI), []byte(redirectURI)) != 1 {
		return nil, ErrCodeRedirectMismatch
	}
	if !VerifyPKCE(codeVerifier, ac.CodeChallenge) {
		return nil, ErrPKCEMismatch
	}

	tok := &AccessToken{
		Token:     randHex(16),
		ClientID:  ac.ClientID,
		Scope:     ac.Scope,
		ExpiresAt: time.Now().Add(accessTokenTTL),
	}
	p.mu.Lock()
	p.accessTokens[tok.Token] = tok
	p.mu.Unlock()
	p.persist()
	return tok, nil
}

// ValidateToken returns (token, true) if the bearer is known and unexpired.
// Caller treats !ok as 401.
func (p *Provider) ValidateToken(tok string) (*AccessToken, bool) {
	if tok == "" {
		return nil, false
	}
	p.mu.RLock()
	t, ok := p.accessTokens[tok]
	p.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(t.ExpiresAt) {
		return nil, false
	}
	return t, true
}

// AccessTokenTTL exposes the access-token TTL so the /token handler can echo
// the canonical expires_in to clients.
func AccessTokenTTL() time.Duration { return accessTokenTTL }

// StartGC sweeps expired authorization codes (every 60s) and access tokens
// (every 5min) until ctx is done. Designed to be launched in a goroutine; the
// caller's context cancels the GC at server shutdown.
func (p *Provider) StartGC(ctx context.Context) {
	go func() {
		codeTicker := time.NewTicker(gcCodeInterval)
		tokenTicker := time.NewTicker(gcTokenInterval)
		defer codeTicker.Stop()
		defer tokenTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-codeTicker.C:
				p.gcCodes()
			case <-tokenTicker.C:
				p.gcTokens()
			}
		}
	}()
}

func (p *Provider) gcCodes() {
	now := time.Now()
	p.mu.Lock()
	removed := 0
	for k, c := range p.codes {
		if now.After(c.ExpiresAt) {
			delete(p.codes, k)
			removed++
		}
	}
	p.mu.Unlock()
	if removed > 0 {
		log.Printf("oauth: gc swept %d expired authorization codes", removed)
		p.persist()
	}
}

func (p *Provider) gcTokens() {
	now := time.Now()
	p.mu.Lock()
	removed := 0
	for k, t := range p.accessTokens {
		if now.After(t.ExpiresAt) {
			delete(p.accessTokens, k)
			removed++
		}
	}
	p.mu.Unlock()
	if removed > 0 {
		log.Printf("oauth: gc swept %d expired access tokens", removed)
		p.persist()
	}
}

// loadState reads the on-disk JSON state into the in-memory maps. Called once
// from NewProvider when statePath is non-empty. Missing file is not an error
// (fresh deployment); malformed file is logged and treated as empty so the
// server still boots. Expired authorization codes and access tokens in the
// loaded snapshot are dropped here so they never enter the runtime maps.
func (p *Provider) loadState() {
	if p.statePath == "" {
		return
	}
	data, err := os.ReadFile(p.statePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("oauth: read state file %q: %v", p.statePath, err)
		}
		return
	}
	if len(data) == 0 {
		return
	}
	var s persistedState
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("oauth: parse state file %q (starting empty): %v", p.statePath, err)
		return
	}
	now := time.Now()
	if s.Clients != nil {
		p.clients = s.Clients
	}
	if s.Codes != nil {
		for k, c := range s.Codes {
			if c == nil || now.After(c.ExpiresAt) {
				continue
			}
			p.codes[k] = c
		}
	}
	if s.Tokens != nil {
		for k, t := range s.Tokens {
			if t == nil || now.After(t.ExpiresAt) {
				continue
			}
			p.accessTokens[k] = t
		}
	}
	log.Printf("oauth: loaded persisted state from %q (%d clients, %d codes, %d tokens)",
		p.statePath, len(p.clients), len(p.codes), len(p.accessTokens))
}

// persist writes the current state to disk atomically (tempfile in the same
// directory, then rename). No-op when statePath is empty. Errors are logged
// but never returned: persistence is best-effort and a transient failure
// (e.g. disk full) should not break an in-flight OAuth flow — the in-memory
// maps remain authoritative for the running process.
func (p *Provider) persist() {
	if p.statePath == "" {
		return
	}
	p.mu.RLock()
	state := persistedState{
		Clients: p.clients,
		Codes:   p.codes,
		Tokens:  p.accessTokens,
	}
	data, err := json.Marshal(state)
	p.mu.RUnlock()
	if err != nil {
		log.Printf("oauth: marshal state: %v", err)
		return
	}

	dir := filepath.Dir(p.statePath)
	tmp, err := os.CreateTemp(dir, ".oauth-state-*.tmp")
	if err != nil {
		log.Printf("oauth: create state tempfile in %q: %v", dir, err)
		return
	}
	tmpPath := tmp.Name()
	// On any error past this point, attempt to clean up the tempfile.
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		log.Printf("oauth: write state tempfile: %v", err)
		return
	}
	if err := tmp.Chmod(0600); err != nil {
		// Non-fatal on Windows where Chmod is largely cosmetic.
		log.Printf("oauth: chmod state tempfile: %v", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		log.Printf("oauth: close state tempfile: %v", err)
		return
	}
	if err := os.Rename(tmpPath, p.statePath); err != nil {
		cleanup()
		log.Printf("oauth: rename state file %q -> %q: %v", tmpPath, p.statePath, err)
		return
	}
}
