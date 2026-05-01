package oauth

import (
	"fmt"
	"net/http"
	"strings"
)

// BearerOAuth wraps a handler with OAuth bearer-token validation against the
// provider. Failures return 401 with a WWW-Authenticate header pointing the
// MCP-spec-compliant client at the protected resource metadata so it can
// discover and run the OAuth flow.
func BearerOAuth(p *Provider) func(http.Handler) http.Handler {
	resourceMetadata := strings.TrimRight(p.Issuer(), "/") + "/.well-known/oauth-protected-resource"
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			authHeader := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(authHeader, prefix) {
				oauthUnauthorized(w, resourceMetadata)
				return
			}
			tok := strings.TrimPrefix(authHeader, prefix)
			if _, ok := p.ValidateToken(tok); !ok {
				oauthUnauthorized(w, resourceMetadata)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func oauthUnauthorized(w http.ResponseWriter, resourceMetadataURL string) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="moodle-mcp", resource_metadata="%s"`, resourceMetadataURL))
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}
