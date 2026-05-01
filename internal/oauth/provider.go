package oauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log"
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
	ID              string
	RedirectURIs    []string
	Name            string
	IssuedAtUnix    int64
}

// AuthCode is a single-use authorization-code grant tied to a client+redirect
// pair, with PKCE state captured at /authorize time.
type AuthCode struct {
	Code          string
	ClientID      string
	RedirectURI   string
	CodeChallenge string // S256 only (method enforced at issue time)
	Scope         string
	ExpiresAt     time.Time
}

// AccessToken is the bearer credential used at /mcp. Opaque (no JWT claims).
type AccessToken struct {
	Token     string
	ClientID  string
	Scope     string
	ExpiresAt time.Time
}

// Provider holds the in-memory state for the single-tenant OAuth server.
// Storage is map+mutex; restart wipes everything (fine for the deploy model:
// agents re-register dynamically on every fresh connection).
type Provider struct {
	issuer string

	mu           sync.RWMutex
	clients      map[string]*Client
	codes        map[string]*AuthCode
	accessTokens map[string]*AccessToken
}

// NewProvider returns a Provider whose discovery endpoints will advertise the
// given public base URL as the issuer. issuer should NOT end with a slash.
func NewProvider(issuer string) *Provider {
	return &Provider{
		issuer:       strings.TrimRight(issuer, "/"),
		clients:      make(map[string]*Client),
		codes:        make(map[string]*AuthCode),
		accessTokens: make(map[string]*AccessToken),
	}
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
	defer p.mu.Unlock()
	removed := 0
	for k, c := range p.codes {
		if now.After(c.ExpiresAt) {
			delete(p.codes, k)
			removed++
		}
	}
	if removed > 0 {
		log.Printf("oauth: gc swept %d expired authorization codes", removed)
	}
}

func (p *Provider) gcTokens() {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	removed := 0
	for k, t := range p.accessTokens {
		if now.After(t.ExpiresAt) {
			delete(p.accessTokens, k)
			removed++
		}
	}
	if removed > 0 {
		log.Printf("oauth: gc swept %d expired access tokens", removed)
	}
}
