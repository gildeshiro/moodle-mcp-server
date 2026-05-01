package oauth

import (
	"encoding/json"
	"net/http"
	"strings"
)

// DiscoveryHandler serves the RFC 8414 authorization-server metadata document
// at /.well-known/oauth-authorization-server. The MCP-spec subset advertises
// only authorization_code grant + S256 PKCE + public clients.
func DiscoveryHandler(p *Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		base := strings.TrimRight(p.Issuer(), "/")
		doc := map[string]interface{}{
			"issuer":                                base,
			"authorization_endpoint":                base + "/oauth/authorize",
			"token_endpoint":                        base + "/oauth/token",
			"registration_endpoint":                 base + "/oauth/register",
			"response_types_supported":              []string{"code"},
			"grant_types_supported":                 []string{"authorization_code"},
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(doc)
	}
}
