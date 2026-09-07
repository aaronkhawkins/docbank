package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxRequestBytes = 1 << 20

// ValidateListenAddress permits only an explicit loopback IP and port.
func ValidateListenAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host == "" || portText == "" {
		return errors.New("MCP listen address must be an explicit loopback IP and port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || !ip.IsLoopback() {
		return errors.New("MCP listen address must use a loopback IP")
	}
	if _, err := strconv.ParseUint(portText, 10, 16); err != nil {
		return errors.New("MCP listen address has an invalid port")
	}
	return nil
}

// ServeHTTP exposes a stateless, JSON-response MCP transport until ctx ends.
func ServeHTTP(ctx context.Context, address string, auth *Authenticator, server *sdkmcp.Server) error {
	if err := ValidateListenAddress(address); err != nil {
		return err
	}
	if auth == nil || server == nil {
		return errors.New("MCP HTTP server requires authentication and protocol handlers")
	}
	transport := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server },
		&sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true,
			// The listener is loopback-only and Authenticator independently requires
			// the canonical public resource Host. This permits normal proxy Host
			// preservation without a fragile rewrite.
			DisableLocalhostProtection: true})
	limited := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.ContentLength > maxRequestBytes {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		transport.ServeHTTP(w, r)
	})
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listening for MCP HTTP: %w", err)
	}
	httpServer := &http.Server{
		Handler: auth.Handler(limited), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 2 * time.Minute, WriteTimeout: 2 * time.Minute, IdleTimeout: 30 * time.Second,
	}
	stopped := make(chan error, 1)
	go func() { stopped <- httpServer.Serve(listener) }()
	select {
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := httpServer.Shutdown(shutdownCtx)
		serveErr := <-stopped
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}
