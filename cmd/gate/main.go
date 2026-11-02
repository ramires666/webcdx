// Command gate exposes a public multi-agent MCP endpoint and relays calls to connected WebCodex agents.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"webcodex/internal/buildinfo"
)

func main() {
	log.Printf("webcodex-gate version=%s", buildinfo.Version)
	srv, err := newServer()
	if err != nil {
		log.Fatalf("initialize server: %v", err)
	}
	defer srv.store.Close()

	addr := env("WEBCODEX_ADDR", ":8080")
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("webcodex-gate listening on %s (public URL: %s)", addr, srv.publicURL)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutting down webcodex-gate gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown error: %v", err)
	}
	log.Printf("webcodex-gate stopped")
}
