package oauth

import (
	"encoding/json"
	"net/http"
)

// tokenResponse is the RFC 6749 § 5.1 successful access-token response.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
}

// TokenHandler serves POST /oauth/token, exchanging an authorization code for
// a bearer access token. application/x-www-form-urlencoded body. PKCE +
// redirect_uri match required.
func TokenHandler(p *Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")

		if err := r.ParseForm(); err != nil {
			writeTokenError(w, "invalid_request", "could not parse form body: "+err.Error())
			return
		}
		grantType := r.PostForm.Get("grant_type")
		code := r.PostForm.Get("code")
		redirectURI := r.PostForm.Get("redirect_uri")
		clientID := r.PostForm.Get("client_id")
		verifier := r.PostForm.Get("code_verifier")

		if grantType != "authorization_code" {
			writeTokenError(w, "unsupported_grant_type", "only grant_type=authorization_code is supported")
			return
		}
		if code == "" || redirectURI == "" || clientID == "" || verifier == "" {
			writeTokenError(w, "invalid_request", "code, redirect_uri, client_id, and code_verifier are required")
			return
		}

		tok, err := p.ExchangeCode(code, verifier, clientID, redirectURI)
		if err != nil {
			writeTokenError(w, "invalid_grant", err.Error())
			return
		}

		resp := tokenResponse{
			AccessToken: tok.Token,
			TokenType:   "Bearer",
			ExpiresIn:   int(AccessTokenTTL().Seconds()),
			Scope:       tok.Scope,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func writeTokenError(w http.ResponseWriter, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": desc,
	})
}
