package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/ops"
)

type fakeServer struct {
	started     chan struct{}
	stop        chan struct{}
	once        sync.Once
	failure     error
	shutdownErr error
	closeErr    error
	shutdown    func(context.Context)
}

func fake() *fakeServer { return &fakeServer{started: make(chan struct{}), stop: make(chan struct{})} }
func (s *fakeServer) Serve() error {
	close(s.started)
	if s.failure != nil {
		return s.failure
	}
	<-s.stop
	return http.ErrServerClosed
}
func (s *fakeServer) Shutdown(ctx context.Context) error {
	if s.shutdown != nil {
		s.shutdown(ctx)
	}
	s.once.Do(func() { close(s.stop) })
	return s.shutdownErr
}
func (s *fakeServer) Close() error { s.once.Do(func() { close(s.stop) }); return s.closeErr }

func TestUnitLifecycle(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancellation", true: "listener-failure"}[fail], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			a, b := fake(), fake()
			sentinel := errors.New("listener failed")
			if fail {
				a.failure = sentinel
			}
			r := &ops.Readiness{}
			a.shutdown = func(ctx context.Context) {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("shutdown context is unbounded")
				}
				res := httptest.NewRecorder()
				ops.Handler(r).ServeHTTP(res, httptest.NewRequest("GET", "/readyz", nil))
				if res.Code != http.StatusServiceUnavailable {
					t.Errorf("shutdown readiness: %d", res.Code)
				}
			}
			done := make(chan error, 1)
			go func() {
				done <- serve(ctx, config.Config{ShutdownDelay: time.Millisecond, ShutdownTimeout: time.Second}, slog.New(slog.DiscardHandler), r, a, b)
			}()
			<-a.started
			<-b.started
			if !fail {
				cancel()
			}
			select {
			case err := <-done:
				if fail && !errors.Is(err, sentinel) || !fail && err != nil {
					t.Fatalf("serve: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("shutdown hung")
			}
		})
	}
}

func TestUnitShutdownErrorsPreserved(t *testing.T) {
	a, b := fake(), fake()
	failure := errors.New("shutdown failure")
	closeFailure := errors.New("close failure")
	a.shutdownErr = failure
	b.closeErr = closeFailure
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := serve(ctx, config.Config{ShutdownTimeout: time.Second}, slog.New(slog.DiscardHandler), &ops.Readiness{}, a, b)
	if !errors.Is(err, failure) || !errors.Is(err, closeFailure) {
		t.Fatal(err)
	}
}

func TestUnitHTTPServerLimitsAndNoDefaultMux(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := http.NewServeMux()
	s := server(h, log)
	if s.Handler != h || s.ReadHeaderTimeout != 5*time.Second || s.ReadTimeout != 10*time.Second || s.WriteTimeout != 30*time.Second || s.IdleTimeout != 60*time.Second || s.MaxHeaderBytes != 1<<20 || s.ErrorLog == nil {
		t.Fatalf("server: %+v", s)
	}
}

func TestUnitMalformedPublicAddressFailsBeforeServing(t *testing.T) {
	// An address syntax error is resolved before any network listener can be created.
	err := Run(t.Context(), config.Config{PublicAddr: "missing-port"}, slog.New(slog.DiscardHandler))
	var address *net.AddrError
	if !errors.As(err, &address) || !strings.Contains(err.Error(), "public listener") {
		t.Fatalf("want contextual address error: %v", err)
	}
}
