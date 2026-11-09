package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Tegardf/reksa/internal/config"
	"github.com/Tegardf/reksa/internal/db"
	"github.com/Tegardf/reksa/internal/order"
)

// main is the program entry point. It sets up a JSON logger that writes to
// stdout, then calls run. If run returns an error, main logs it and exits
// with status 1. Keeping the logic in run lets deferred cleanup (closing the
// DB pool, etc.) finish before the process exits.
func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// run wires the application together and blocks until the server stops.
//
// Steps:
//  1. Create a context that is cancelled on Ctrl+C (SIGINT) or SIGTERM.
//  2. Load configuration from environment variables.
//  3. Open the Postgres connection pool, failing early if the DB is unreachable.
//  4. Register the order routes and the GET /healthz endpoint.
//  5. Start the HTTP server in a goroutine.
//  6. Wait until the server fails or a shutdown signal arrives, then shut
//     down gracefully, giving in-flight requests up to 10 seconds to finish.
func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	mux := http.NewServeMux()
	order.NewHandler(order.NewService(pool), log).Register(mux)

	// Health check: reports "ok" only if the database answers a ping
	// within 2 seconds; otherwise returns 503.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
