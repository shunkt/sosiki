// Package serve is the shared HTTP server bootstrap for every kaigi
// process (moderator/discovery/persona): listen, wait for SIGINT/SIGTERM
// or a listen error, then shut down with a bounded drain timeout. Before
// this package existed, cmd/discovery, cmd/moderator, and cmd/persona each
// hand-wrote an identical ~30 lines of this — a future change to shutdown
// behavior had to be applied in three places, and it was easy to patch two
// and silently leave the third inconsistent. Caught by code review.
package serve

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 10 * time.Second
)

// Run serves handler on addr until ctx is canceled or a SIGINT/SIGTERM is
// received, then drains in-flight requests for up to shutdownTimeout before
// returning. ReadTimeout/WriteTimeout are deliberately left unset on the
// underlying http.Server: both are absolute deadlines on the entire
// request/response, so either would cut an SSE stream off mid-message —
// every SSE handler in this codebase manages its own per-frame write
// deadline with http.ResponseController instead.
func Run(ctx context.Context, log *slog.Logger, addr string, handler http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stopCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return err
	case <-stopCtx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
