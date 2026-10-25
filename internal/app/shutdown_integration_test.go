package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/ops"
)

func TestIntegrationShutdownDeadlineClosesActiveRequest(t *testing.T) {
	public, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = public.Close() }()
	operational, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = operational.Close() }()
	started, closed := make(chan struct{}), make(chan struct{})
	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(closed) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	readiness := &ops.Readiness{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, config.Config{ShutdownTimeout: 20 * time.Millisecond}, log, readiness, boundServer{server(handler, log), public}, boundServer{server(ops.Handler(readiness), log), operational})
	}()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		requestCtx, stop := context.WithTimeout(t.Context(), 2*time.Second)
		defer stop()
		req, err := http.NewRequestWithContext(requestCtx, "GET", "http://"+public.Addr().String()+"/", nil)
		if err != nil {
			return
		}
		client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 2 * time.Second}
		if res, err := client.Do(req); err == nil {
			_ = res.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown deadline lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown was not bounded")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("active request context was not cancelled")
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("request goroutine survived shutdown")
	}
}
