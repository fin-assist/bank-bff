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

	"google.golang.org/grpc"

	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/ops"
)

// Option adjusts the assembly. Options are trusted test seams: they change the
// transport to peers or timings, never identity, validation or authorization.
type Option func(*options)

type options struct {
	dial                      []grpc.DialOption
	heartbeat                 time.Duration
	answerStream, phoneStream time.Duration
	readTimeout, writeTimeout time.Duration
	clock                     func() time.Time
}

// WithDialOptions adds transport options to every peer connection (bufconn in tests).
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dial = append(o.dial, opts...) }
}

// WithStreamTimings overrides the SSE heartbeat and stream deadlines.
func WithStreamTimings(heartbeat, answer, phone time.Duration) Option {
	return func(o *options) { o.heartbeat, o.answerStream, o.phoneStream = heartbeat, answer, phone }
}

// WithServerTimeouts overrides the listener read and write timeouts.
func WithServerTimeouts(read, write time.Duration) Option {
	return func(o *options) { o.readTimeout, o.writeTimeout = read, write }
}

// WithClock overrides "now" of the identity verifiers and fresh retry moments.
func WithClock(clock func() time.Time) Option { return func(o *options) { o.clock = clock } }

func defaults() options {
	return options{readTimeout: 10 * time.Second, writeTimeout: 30 * time.Second, clock: time.Now}
}

// Run builds the public API, binds both listeners and only then advertises readiness.
// In ENV=dev the public mux has no business routes: identity cannot be built there
// and no development bypass exists.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger, opts ...Option) error {
	o := defaults()
	for _, opt := range opts {
		opt(&o)
	}
	registry := ops.NewRegistry()
	var public http.Handler = http.NewServeMux()
	var api *publicAPI
	if cfg.API.Enabled {
		var err error
		api, err = newPublicAPI(cfg, log, registry, o)
		if err != nil {
			return fmt.Errorf("public API: %w", err)
		}
		defer api.close(log)
		public = api.handler
	}
	publicListener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.PublicAddr)
	if err != nil {
		return fmt.Errorf("public listener: %w", err)
	}
	defer func() { _ = publicListener.Close() }()
	operational, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("operational listener: %w", err)
	}
	defer func() { _ = operational.Close() }()
	cfg.PublicAddr, cfg.HTTPAddr = publicListener.Addr().String(), operational.Addr().String()
	readiness := &ops.Readiness{}
	keys := func(context.Context) {}
	if api != nil {
		keys = api.refreshKeys
	}
	publicServer := server(public, log, o)
	if api != nil {
		publicServer.RegisterOnShutdown(api.streams.Shutdown)
	}
	return serve(ctx, cfg, log, readiness, keys,
		boundServer{publicServer, publicListener},
		boundServer{server(ops.Handler(readiness, registry), log, o), operational})
}

func server(handler http.Handler, log *slog.Logger, o options) *http.Server {
	// The write timeout bounds unary answers; an SSE stream extends its own read and
	// write deadlines (rest.StartStream), and unary peer calls end in 5 s (search in 10 s), before it.
	return &http.Server{Handler: handler, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelError),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: o.readTimeout,
		WriteTimeout: o.writeTimeout, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
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

// serve runs both listeners and the identity key refresh until ctx ends or a listener
// fails, then drains and stops everything within the shutdown bound.
func serve(ctx context.Context, cfg config.Config, log *slog.Logger, readiness *ops.Readiness, keys func(context.Context), public, operational lifecycle) error {
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, srv := range []lifecycle{public, operational} {
		wg.Go(func() {
			if err := srv.Serve(); !errors.Is(err, http.ErrServerClosed) {
				failures <- fmt.Errorf("HTTP listener stopped: %w", err)
			}
		})
	}
	keysCtx, stopKeys := context.WithCancel(context.Background())
	var keysDone sync.WaitGroup
	keysDone.Go(func() { keys(keysCtx) })
	readiness.Set(true)
	log.InfoContext(ctx, "serving", "public", cfg.PublicAddr, "operational", cfg.HTTPAddr, "env", cfg.Env, "api", cfg.API.Enabled)
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
	stopKeys()
	keysDone.Wait()
	log.InfoContext(ctx, "stopped")
	return errors.Join(runErr, shutdownErr)
}
