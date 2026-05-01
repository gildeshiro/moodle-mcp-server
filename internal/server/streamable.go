package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jawadh/moodle-mcp-server/internal/oauth"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// StreamableOpts configures the remote HTTP MCP server.
type StreamableOpts struct {
	Port          int
	AuthToken     string          // bearer-static mode: required (non-empty) when chosen
	AllowNoAuth   bool            // no-auth mode: skip the bearer auth middleware (URL-as-secret model)
	OAuthProvider *oauth.Provider // OAuth 2.1 + DCR mode: when non-nil, mount discovery/register/authorize/token + bearer validation against this provider
	CORSOrigins   string          // comma-separated; empty = no CORS
	Path          string          // default "/mcp"
	Version       string          // surfaced in /healthz
}

const shutdownTimeout = 10 * time.Second

// RunStreamable starts an http.Server exposing the given MCPServer over the
// MCP Streamable HTTP transport, gated by a bearer-token middleware.
//
// Blocks until SIGINT/SIGTERM, the parent context cancels, or the listener
// fails. On shutdown signal, drains in-flight requests for up to 10 seconds
// before returning.
func RunStreamable(ctx context.Context, mcp *mcpserver.MCPServer, opts StreamableOpts) error {
	// Boot guard: exactly one of {bearer-static, no-auth, oauth} must be set.
	// Mixing modes silently would let a deployment forget to gate /mcp.
	modes := 0
	if opts.AuthToken != "" {
		modes++
	}
	if opts.AllowNoAuth {
		modes++
	}
	if opts.OAuthProvider != nil {
		modes++
	}
	if modes == 0 {
		return errors.New("auth mode required for http mode: set MCP_AUTH_TOKEN (bearer-static), MCP_DISABLE_AUTH=1 (URL-as-secret), or MCP_USE_OAUTH=1 + MCP_OAUTH_ISSUER (OAuth 2.1 + DCR) — exactly one")
	}
	if modes > 1 {
		return errors.New("auth mode conflict: MCP_AUTH_TOKEN, MCP_DISABLE_AUTH, and MCP_USE_OAUTH are mutually exclusive — set exactly one")
	}
	if opts.Path == "" {
		opts.Path = "/mcp"
	}
	if !strings.HasPrefix(opts.Path, "/") {
		opts.Path = "/" + opts.Path
	}
	if opts.Port == 0 {
		opts.Port = 8080
	}

	streamable := mcpserver.NewStreamableHTTPServer(mcp,
		mcpserver.WithEndpointPath(opts.Path),
	)

	var origins []string
	if opts.CORSOrigins != "" {
		origins = strings.Split(opts.CORSOrigins, ",")
	}

	// Auth chain. The boot guard above guarantees exactly one mode is set.
	//   * AuthToken    → static bearer compared in constant time
	//   * AllowNoAuth  → no-op pass-through (URL-as-secret); loud startup warning
	//   * OAuthProvider→ bearer validated against the OAuth provider's tokens;
	//                    discovery/register/authorize/token endpoints mounted unprotected
	auth := func(h http.Handler) http.Handler { return h }
	switch {
	case opts.OAuthProvider != nil:
		auth = oauth.BearerOAuth(opts.OAuthProvider)
	case opts.AllowNoAuth:
		log.Println("WARNING: MCP_DISABLE_AUTH=1 — bearer auth is OFF. The deployment URL is the only protection. Rotate the URL if leaked.")
	default:
		auth = BearerAuth(opts.AuthToken)
	}
	cors := CORS(origins)
	logger := RequestLogger()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler(opts.Version))
	mux.Handle(opts.Path, logger(cors(auth(streamable))))

	// OAuth bootstrap endpoints. These MUST be reachable without a bearer
	// (they ARE the bearer-issuance flow), so they wrap only logger+cors.
	if opts.OAuthProvider != nil {
		p := opts.OAuthProvider
		mux.Handle("/.well-known/oauth-authorization-server", logger(cors(oauth.DiscoveryHandler(p))))
		mux.Handle("/.well-known/oauth-protected-resource", logger(cors(oauth.ResourceHandler(p))))
		mux.Handle("/oauth/register", logger(cors(oauth.RegisterHandler(p))))
		mux.Handle("/oauth/authorize", logger(cors(oauth.AuthorizeHandler(p))))
		mux.Handle("/oauth/token", logger(cors(oauth.TokenHandler(p))))
		// Background sweep of expired codes/tokens; ctx cancellation stops it.
		p.StartGC(ctx)
		log.Printf("OAuth 2.1 + DCR enabled (issuer=%s)", p.Issuer())
	}

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", opts.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("Moodle MCP Streamable HTTP listening on :%d (path=%s)", opts.Port, opts.Path)
		errCh <- srv.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case sig := <-sigCh:
		log.Printf("shutdown signal %s received; draining...", sig)
	case <-ctx.Done():
		log.Println("context cancelled; draining...")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func healthHandler(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"version": version,
			"mode":    "http",
		})
	}
}
