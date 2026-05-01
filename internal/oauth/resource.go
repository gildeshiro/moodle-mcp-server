package oauth

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ResourceHandler serves the RFC 9728-style protected-resource metadata at
// /.well-known/oauth-protected-resource so MCP clients can discover the
// authorization server tied to /mcp.
func ResourceHandler(p *Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		base := strings.TrimRight(p.Issuer(), "/")
		doc := map[string]interface{}{
			"resource":              base + "/mcp",
			"authorization_servers": []string{base},
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(doc)
	}
}
