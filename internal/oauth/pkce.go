// Package oauth implements a single-tenant OAuth 2.1 + DCR provider as
// specified by the MCP authorization spec (modelcontextprotocol.io,
// 2025-03-26). Auto-approve consent, in-memory storage, opaque tokens.
package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// VerifyPKCE returns true when sha256(verifier) base64url-no-padding equals
// the stored code_challenge. RFC 7636 § 4.6 (S256 only). Constant-time compare.
func VerifyPKCE(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	h := sha256.Sum256([]byte(verifier))
	encoded := base64.RawURLEncoding.EncodeToString(h[:])
	return subtle.ConstantTimeCompare([]byte(encoded), []byte(challenge)) == 1
}
