// Package app assembles the stateless HTTP BFF and owns its listener lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/ops"
)

// Run binds both listeners before advertising readiness. The public mux has no
// business routes until they are implemented against fin-assist/contracts.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	public, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.PublicAddr)
	if err != nil {
		return fmt.Errorf("public listener: %w", err)
	}
	defer func() { _ = public.Close() }()
	operational, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("operational listener: %w", err)
	}
	defer func() { _ = operational.Close() }()
	cfg.PublicAddr, cfg.HTTPAddr = public.Addr().String(), operational.Addr().String()
	readiness := &ops.Readiness{}
	return serve(ctx, cfg, log, readiness,
		boundServer{server(http.NewServeMux(), log), public},
		boundServer{server(ops.Handler(readiness), log), operational})
}

func server(handler http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{Handler: handler, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelError),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
}

type lifecycle interface {
	Serve() error
	Shutdown(context.Context) error
	Close() error
}

type boundServer struct {
	*http.Server
	listener net.Listener
}

func (s boundServer) Serve() error { return s.Server.Serve(s.listener) }

func serve(ctx context.Context, cfg config.Config, log *slog.Logger, readiness *ops.Readiness, public, operational lifecycle) error {
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, srv := range []lifecycle{public, operational} {
		wg.Go(func() {
			if err := srv.Serve(); !errors.Is(err, http.ErrServerClosed) {
				failures <- fmt.Errorf("HTTP listener stopped: %w", err)
			}
		})
	}
	readiness.Set(true)
	log.InfoContext(ctx, "serving", "public", cfg.PublicAddr, "operational", cfg.HTTPAddr, "env", cfg.Env)
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-failures:
	}
	readiness.Set(false)
	// Drain public requests while the operational listener still reports not ready.
	if runErr == nil && cfg.ShutdownDelay > 0 {
		<-time.After(cfg.ShutdownDelay)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	var shutdownErr error
	for _, srv := range []lifecycle{public, operational} {
		shutdownErr = errors.Join(shutdownErr, srv.Shutdown(shutdownCtx))
		// Shutdown does not close active connections when its deadline expires.
		shutdownErr = errors.Join(shutdownErr, srv.Close())
	}
	wg.Wait()
	log.InfoContext(ctx, "stopped")
	return errors.Join(runErr, shutdownErr)
}
