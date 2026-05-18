// Single binary entrypoint. Listens on $PORT (default 4004 — same as
// Huly's rekoni used, so dropping this in place needs no front-end
// rewiring) and serves the /v1/* surface.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hanzoai/extract-svc/pkg/auth"
	"github.com/hanzoai/extract-svc/pkg/server"
)

// Version is overridden via -ldflags="-X main.Version=v0.1.0" at build time.
var Version = "v0.1.0-dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println(Version)
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	addr := envOr("LISTEN_ADDR", ":4004")
	jwks := envOr("IAM_JWKS_URL", auth.DefaultJWKSURL)
	logger.Info("starting extract",
		"version", Version, "addr", addr, "jwks", jwks)

	s := server.New(server.Config{
		Version: Version,
		JWKSURL: jwks,
		Logger:  logger,
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: extraction of a 25 MB PDF can take a while.
		// We bound work inside the handlers themselves (LimitReader,
		// FetchTimeout).
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown error", "err", err)
		}
	}
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

