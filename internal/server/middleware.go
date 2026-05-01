package server

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
	"time"
)

// BearerAuth wraps a handler with bearer-token authentication.
// Calls site MUST pass a non-empty token; this function panics on empty input
// to make accidental misconfiguration loud rather than insecure.
//
// The middleware uses constant-time comparison and lets CORS preflight
// (OPTIONS) requests through untouched.
func BearerAuth(token string) func(http.Handler) http.Handler {
	if token == "" {
		panic("server.BearerAuth: empty token (caller must enforce non-empty)")
	}
	expected := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			const prefix = "Bearer "
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, prefix) {
				unauthorized(w)
				return
			}
			got := []byte(strings.TrimPrefix(authHeader, prefix))
			if subtle.ConstantTimeCompare(got, expected) != 1 {
				unauthorized(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="moodle-mcp"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// CORS wraps a handler with permissive-but-explicit CORS headers for the given
// allowed origins. An empty origins slice short-circuits to a no-op pass-through
// (server-to-server callers, including claude.ai backends, do not need CORS).
//
// The wildcard "*" is accepted but logged with a warning — operators should
// pin specific origins in production.
//
// MCP-Session-Id is added to the allowed headers because the Streamable HTTP
// spec uses it for session continuity.
func CORS(origins []string) func(http.Handler) http.Handler {
	if len(origins) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	allowAll := false
	allowed := make(map[string]bool, len(origins))
	for _, o := range origins {
		o = strings.TrimSpace(o)
		if o == "*" {
			allowAll = true
			log.Println("WARNING: CORS allows all origins ('*'). Tighten in production.")
		}
		if o != "" {
			allowed[o] = true
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (allowAll || allowed[origin]) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Session-Id")
				w.Header().Set("Access-Control-Max-Age", "86400")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder captures the response status code for logging.
// We avoid pulling in a third-party logger — stdlib log.Printf with a
// grep-friendly key=value format is enough.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// RequestLogger logs one structured line per request to stdout.
// Format: "<RFC3339> method=<M> path=<P> status=<S> dur=<D> remote=<IP>".
// No request/response bodies are logged (may contain Moodle tokens or PII).
// X-Forwarded-For (first hop) is preferred over RemoteAddr when present.
func RequestLogger() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			log.Printf(
				"%s method=%s path=%s status=%d dur=%s remote=%s",
				start.UTC().Format(time.RFC3339),
				r.Method,
				r.URL.Path,
				rec.status,
				time.Since(start).Round(time.Millisecond),
				clientIP(r),
			)
		})
	}
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return xff
	}
	return r.RemoteAddr
}
