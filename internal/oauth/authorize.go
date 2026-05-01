package oauth

import (
	"net/http"
	"net/url"
)

// AuthorizeHandler serves GET /oauth/authorize. The authorization-code flow
// step 1: it auto-approves consent (single-tenant deploy model — there's no
// human owner to prompt; the bearer at /mcp is the gate) and 302-redirects
// the user-agent back to redirect_uri with code+state.
//
// On invalid request: if redirect_uri is registered we 302 with
// error/error_description/state per RFC 6749 § 4.1.2.1; otherwise 400 to
// avoid open redirector behavior to attacker-controlled URLs.
func AuthorizeHandler(p *Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")

		q := r.URL.Query()
		responseType := q.Get("response_type")
		clientID := q.Get("client_id")
		redirectURI := q.Get("redirect_uri")
		challenge := q.Get("code_challenge")
		challengeMethod := q.Get("code_challenge_method")
		state := q.Get("state")
		scope := q.Get("scope")

		if clientID == "" {
			http.Error(w, "missing client_id", http.StatusBadRequest)
			return
		}
		client, ok := p.GetClient(clientID)
		if !ok {
			http.Error(w, "unknown client_id", http.StatusBadRequest)
			return
		}
		if !clientHasRedirect(client, redirectURI) {
			// Don't redirect to an unregistered redirect_uri (open redirector).
			http.Error(w, "redirect_uri not registered for client", http.StatusBadRequest)
			return
		}

		// From here on, any error redirects back to the registered redirect_uri.
		if responseType != "code" {
			redirectError(w, r, redirectURI, "unsupported_response_type", "only response_type=code is supported", state)
			return
		}
		if challengeMethod != "S256" {
			redirectError(w, r, redirectURI, "invalid_request", "code_challenge_method must be S256", state)
			return
		}
		if challenge == "" {
			redirectError(w, r, redirectURI, "invalid_request", "code_challenge is required", state)
			return
		}

		code, err := p.IssueCode(clientID, redirectURI, challenge, challengeMethod, scope)
		if err != nil {
			redirectError(w, r, redirectURI, "server_error", err.Error(), state)
			return
		}

		// 302 to redirect_uri?code=<code>&state=<state>
		dst, err := url.Parse(redirectURI)
		if err != nil {
			http.Error(w, "redirect_uri unparseable", http.StatusBadRequest)
			return
		}
		params := dst.Query()
		params.Set("code", code)
		if state != "" {
			params.Set("state", state)
		}
		dst.RawQuery = params.Encode()
		http.Redirect(w, r, dst.String(), http.StatusFound)
	}
}

func redirectError(w http.ResponseWriter, r *http.Request, redirectURI, code, desc, state string) {
	dst, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "redirect_uri unparseable", http.StatusBadRequest)
		return
	}
	params := dst.Query()
	params.Set("error", code)
	if desc != "" {
		params.Set("error_description", desc)
	}
	if state != "" {
		params.Set("state", state)
	}
	dst.RawQuery = params.Encode()
	http.Redirect(w, r, dst.String(), http.StatusFound)
}
