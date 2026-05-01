package oauth

import (
	"encoding/json"
	"net/http"
)

// dcrRequest is the RFC 7591 client registration request body. We only honor
// redirect_uris and client_name; everything else is ignored (token_endpoint
// auth method is always "none" — public client only).
type dcrRequest struct {
	RedirectURIs []string `json:"redirect_uris"`
	ClientName   string   `json:"client_name"`
}

// dcrResponse is the RFC 7591 client information response (subset). Returned
// with HTTP 201.
type dcrResponse struct {
	ClientID                string   `json:"client_id"`
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

// RegisterHandler serves POST /oauth/register, the RFC 7591 DCR endpoint.
// Auto-approves any well-formed body — single-tenant deploy model trusts the
// caller; the access-control gate is the user's eventual approval at /authorize
// (currently auto-approved too) and the bearer token at /mcp.
func RegisterHandler(p *Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req dcrRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeRegisterError(w, "invalid_client_metadata", "request body must be valid JSON: "+err.Error())
			return
		}
		client, err := p.RegisterClient(req.RedirectURIs, req.ClientName)
		if err != nil {
			writeRegisterError(w, "invalid_client_metadata", err.Error())
			return
		}
		resp := dcrResponse{
			ClientID:                client.ID,
			RedirectURIs:            client.RedirectURIs,
			ClientName:              client.Name,
			TokenEndpointAuthMethod: "none",
			ClientIDIssuedAt:        client.IssuedAtUnix,
			GrantTypes:              []string{"authorization_code"},
			ResponseTypes:           []string{"code"},
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func writeRegisterError(w http.ResponseWriter, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": desc,
	})
}
