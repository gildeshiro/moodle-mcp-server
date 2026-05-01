package oauth

import (
	"errors"
	"testing"
	"time"
)

const (
	// RFC 7636 Appendix B vector reused for tests so we don't compute live.
	testVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	testChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	return NewProvider("http://localhost:8772")
}

func TestRegisterClient_Validation(t *testing.T) {
	p := newTestProvider(t)
	if _, err := p.RegisterClient(nil, "x"); err == nil {
		t.Errorf("nil redirect_uris should fail")
	}
	if _, err := p.RegisterClient([]string{}, "x"); err == nil {
		t.Errorf("empty redirect_uris should fail")
	}
	if _, err := p.RegisterClient([]string{"http://evil.example/cb"}, "x"); err == nil {
		t.Errorf("non-https non-loopback redirect_uri should fail")
	}
	if _, err := p.RegisterClient([]string{"https://example.com/cb"}, "x"); err != nil {
		t.Errorf("https redirect_uri should be accepted, got %v", err)
	}
	if _, err := p.RegisterClient([]string{"http://localhost:9999/cb"}, "x"); err != nil {
		t.Errorf("loopback redirect_uri should be accepted, got %v", err)
	}
}

func TestHappyPath(t *testing.T) {
	p := newTestProvider(t)
	c, err := p.RegisterClient([]string{"http://localhost:9999/cb"}, "smoke")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || len(c.ID) != 32 {
		t.Errorf("client_id should be 32 hex chars, got %q (len %d)", c.ID, len(c.ID))
	}

	code, err := p.IssueCode(c.ID, "http://localhost:9999/cb", testChallenge, "S256", "")
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	tok, err := p.ExchangeCode(code, testVerifier, c.ID, "http://localhost:9999/cb")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if tok.Token == "" || len(tok.Token) != 32 {
		t.Errorf("access_token should be 32 hex chars, got %q (len %d)", tok.Token, len(tok.Token))
	}
	if got, ok := p.ValidateToken(tok.Token); !ok || got.Token != tok.Token {
		t.Errorf("ValidateToken should accept fresh token")
	}
}

func TestReusedCode(t *testing.T) {
	p := newTestProvider(t)
	c, _ := p.RegisterClient([]string{"http://localhost:9999/cb"}, "")
	code, _ := p.IssueCode(c.ID, "http://localhost:9999/cb", testChallenge, "S256", "")
	if _, err := p.ExchangeCode(code, testVerifier, c.ID, "http://localhost:9999/cb"); err != nil {
		t.Fatalf("first exchange should succeed: %v", err)
	}
	if _, err := p.ExchangeCode(code, testVerifier, c.ID, "http://localhost:9999/cb"); !errors.Is(err, ErrUnknownCode) {
		t.Errorf("second exchange of same code should fail with ErrUnknownCode, got %v", err)
	}
}

func TestExpiredCode(t *testing.T) {
	p := newTestProvider(t)
	c, _ := p.RegisterClient([]string{"http://localhost:9999/cb"}, "")
	code, _ := p.IssueCode(c.ID, "http://localhost:9999/cb", testChallenge, "S256", "")
	// Reach into provider state to backdate the code's expiry.
	p.mu.Lock()
	p.codes[code].ExpiresAt = time.Now().Add(-time.Minute)
	p.mu.Unlock()
	if _, err := p.ExchangeCode(code, testVerifier, c.ID, "http://localhost:9999/cb"); !errors.Is(err, ErrCodeExpired) {
		t.Errorf("expected ErrCodeExpired, got %v", err)
	}
}

func TestWrongPKCE(t *testing.T) {
	p := newTestProvider(t)
	c, _ := p.RegisterClient([]string{"http://localhost:9999/cb"}, "")
	code, _ := p.IssueCode(c.ID, "http://localhost:9999/cb", testChallenge, "S256", "")
	if _, err := p.ExchangeCode(code, "wrong-verifier", c.ID, "http://localhost:9999/cb"); !errors.Is(err, ErrPKCEMismatch) {
		t.Errorf("expected ErrPKCEMismatch, got %v", err)
	}
}

func TestRedirectURIMismatch(t *testing.T) {
	p := newTestProvider(t)
	c, _ := p.RegisterClient([]string{"http://localhost:9999/cb"}, "")
	if _, err := p.IssueCode(c.ID, "http://localhost:9999/other", testChallenge, "S256", ""); !errors.Is(err, ErrInvalidRedirectURI) {
		t.Errorf("authorize with unregistered redirect should fail with ErrInvalidRedirectURI, got %v", err)
	}
}

func TestExchangeRedirectMismatch(t *testing.T) {
	p := newTestProvider(t)
	c, _ := p.RegisterClient([]string{"http://localhost:9999/cb", "http://localhost:9999/other"}, "")
	code, _ := p.IssueCode(c.ID, "http://localhost:9999/cb", testChallenge, "S256", "")
	if _, err := p.ExchangeCode(code, testVerifier, c.ID, "http://localhost:9999/other"); !errors.Is(err, ErrCodeRedirectMismatch) {
		t.Errorf("token redirect_uri mismatch should fail, got %v", err)
	}
}

func TestUnsupportedChallengeMethod(t *testing.T) {
	p := newTestProvider(t)
	c, _ := p.RegisterClient([]string{"http://localhost:9999/cb"}, "")
	if _, err := p.IssueCode(c.ID, "http://localhost:9999/cb", testChallenge, "plain", ""); !errors.Is(err, ErrUnsupportedChallenge) {
		t.Errorf("plain challenge method should be rejected, got %v", err)
	}
}

func TestExpiredToken(t *testing.T) {
	p := newTestProvider(t)
	c, _ := p.RegisterClient([]string{"http://localhost:9999/cb"}, "")
	code, _ := p.IssueCode(c.ID, "http://localhost:9999/cb", testChallenge, "S256", "")
	tok, _ := p.ExchangeCode(code, testVerifier, c.ID, "http://localhost:9999/cb")
	p.mu.Lock()
	p.accessTokens[tok.Token].ExpiresAt = time.Now().Add(-time.Minute)
	p.mu.Unlock()
	if _, ok := p.ValidateToken(tok.Token); ok {
		t.Errorf("expired token should not validate")
	}
}

func TestUnknownClient(t *testing.T) {
	p := newTestProvider(t)
	if _, err := p.IssueCode("nonexistent", "http://localhost:9999/cb", testChallenge, "S256", ""); !errors.Is(err, ErrUnknownClient) {
		t.Errorf("unknown client should fail, got %v", err)
	}
}
