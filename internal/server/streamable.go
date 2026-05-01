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

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// StreamableOpts configures the remote HTTP MCP server.
type StreamableOpts struct {
	Port        int
	AuthToken   string // required (non-empty); enforced by RunStreamable unless AllowNoAuth
	AllowNoAuth bool   // skip the bearer auth middleware entirely (URL-as-secret model)
	CORSOrigins string // comma-separated; empty = no CORS
	Path        string // default "/mcp"
	Version     string // surfaced in /healthz
}

const shutdownTimeout = 10 * time.Second

// RunStreamable starts an http.Server exposing the given MCPServer over the
// MCP Streamable HTTP transport, gated by a bearer-token middleware.
//
// Blocks until SIGINT/SIGTERM, the parent context cancels, or the listener
// fails. On shutdown signal, drains in-flight requests for up to 10 seconds
// before returning.
func RunStreamable(ctx context.Context, mcp *mcpserver.MCPServer, opts StreamableOpts) error {
	if opts.AuthToken == "" && !opts.AllowNoAuth {
		return errors.New("MCP_AUTH_TOKEN required for http mode (security guardrail; set MCP_DISABLE_AUTH=1 to opt out — URL becomes the only secret)")
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

	// Auth chain. When AllowNoAuth is set (typically because the client doesn't
	// support custom headers — e.g. claude.ai web BETA only offers OAuth), the
	// bearer middleware is replaced with a no-op pass-through. The deployment
	// URL becomes the only secret. Loud warning at startup so operators notice.
	auth := func(h http.Handler) http.Handler { return h }
	if !opts.AllowNoAuth {
		auth = BearerAuth(opts.AuthToken)
	} else {
		log.Println("WARNING: MCP_DISABLE_AUTH=1 — bearer auth is OFF. The deployment URL is the only protection. Rotate the URL if leaked.")
	}
	cors := CORS(origins)
	logger := RequestLogger()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler(opts.Version))
	mux.Handle(opts.Path, logger(cors(auth(streamable))))

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
