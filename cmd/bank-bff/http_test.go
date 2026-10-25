package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type startup struct{ public, operational string }
type logCapture struct {
	mu sync.Mutex
	bytes.Buffer
	started chan startup
}

func (w *logCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.Buffer.Write(p)
	var event struct{ Msg, Public, Operational string }
	if json.Unmarshal(p, &event) == nil && event.Msg == "serving" {
		select {
		case w.started <- startup{event.Public, event.Operational}:
		default:
		}
	} else if strings.Contains(string(p), "msg=serving") {
		parts := regexp.MustCompile(`public=(\S+) operational=(\S+)`).FindSubmatch(p)
		if len(parts) == 3 {
			select {
			case w.started <- startup{string(parts[1]), string(parts[2])}:
			default:
			}
		}
	}
	return n, err
}
func (w *logCapture) text() string { w.mu.Lock(); defer w.mu.Unlock(); return w.String() }

func cleanEnvironment(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{"SERVICE_NAME": "bank-bff", "ENV": "dev", "PUBLIC_ADDR": "127.0.0.1:0", "HTTP_ADDR": "127.0.0.1:0", "LOG_LEVEL": "info", "LOG_FORMAT": "json", "SHUTDOWN_DELAY": "0s", "SHUTDOWN_TIMEOUT": "1s"} {
		t.Setenv(key, value)
	}
}

func startBFF(t *testing.T) (startup, func(), *logCapture) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	output := &logCapture{started: make(chan startup, 1)}
	done := make(chan int, 1)
	go func() { done <- execute(ctx, []string{"serve"}, output) }()
	var once sync.Once
	stop := func() {
		t.Helper()
		once.Do(func() {
			cancel()
			select {
			case code := <-done:
				if code != 0 {
					t.Errorf("serve exited %d: %s", code, output.text())
				}
			case <-time.After(5 * time.Second):
				t.Error("serve failed to stop")
			}
		})
	}
	t.Cleanup(stop)
	select {
	case addr := <-output.started:
		return addr, stop, output
	case code := <-done:
		t.Fatalf("startup exited %d: %s", code, output.text())
	case <-time.After(10 * time.Second):
		t.Fatal("startup did not complete")
	}
	return startup{}, stop, output
}

func get(t *testing.T, addr, path string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestAPIHTTPScaffoldExposesOnlyOperationalEndpoints(t *testing.T) {
	cleanEnvironment(t)
	addr, stop, output := startBFF(t)
	for path, body := range map[string]string{"/healthz": "ok\n", "/readyz": "ok\n", "/metrics": "go_goroutines"} {
		code, response := get(t, addr.operational, path)
		if code != 200 || !strings.Contains(response, body) {
			t.Fatalf("%s: %d %q", path, code, response)
		}
	}
	// Readiness is operational only. No business route or authentication bypass exists.
	for _, path := range []string{"/", "/healthz", "/readyz", "/metrics", "/v1/profile"} {
		code, _ := get(t, addr.public, path)
		if code != 404 {
			t.Fatalf("public %s: %d", path, code)
		}
	}
	if code, _ := get(t, addr.operational, "/v1/profile"); code != 404 {
		t.Fatal(code)
	}
	stop()
	for _, a := range []string{addr.public, addr.operational} {
		conn, err := net.DialTimeout("tcp", a, time.Second)
		if err == nil {
			_ = conn.Close()
			t.Fatalf("listener %s survived shutdown", a)
		}
	}
	if !strings.Contains(output.text(), `"service":"bank-bff"`) || !strings.Contains(output.text(), `"version":"dev"`) {
		t.Fatal(output.text())
	}
}

func TestIntegrationHTTPProtocolAndIndependentListeners(t *testing.T) {
	cleanEnvironment(t)
	t.Setenv("ENV", "stage")
	t.Setenv("LOG_FORMAT", "text")
	t.Setenv("LOG_LEVEL", "debug")
	addr, _, _ := startBFF(t)
	conn, err := net.DialTimeout("tcp", addr.public, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(conn, "INVALID HTTP\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(response, []byte("400 Bad Request")) {
		t.Fatalf("malformed HTTP: %q", response)
	}
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		if code, _ := get(t, addr.operational, path); code != 200 {
			t.Fatalf("ops %s: %d", path, code)
		}
	}
	if code, _ := get(t, addr.public, "/v1/profile"); code != 404 {
		t.Fatal(code)
	}
}

func TestAPIStartupValidationAndListenerConflicts(t *testing.T) {
	cleanEnvironment(t)
	startupFailures(t)
}

func TestIntegrationStartupValidationAndListenerConflicts(t *testing.T) {
	cleanEnvironment(t)
	startupFailures(t)
}

func startupFailures(t *testing.T) {
	t.Helper()
	for _, tc := range []struct{ key, value string }{
		{"ENV", "bad"}, {"LOG_FORMAT", "bad"}, {"LOG_LEVEL", "bad"}, {"PUBLIC_ADDR", ":-1"}, {"HTTP_ADDR", ":65536"},
		{"SHUTDOWN_DELAY", "-1s"}, {"SHUTDOWN_TIMEOUT", "0s"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			var output bytes.Buffer
			if code := execute(t.Context(), []string{"serve"}, &output); code != 1 || !strings.Contains(output.String(), tc.key) {
				t.Fatalf("bad %s accepted: %d %s", tc.key, code, output.String())
			}
		})
	}
	for _, key := range []string{"PUBLIC_ADDR", "HTTP_ADDR"} {
		t.Run(key+"-occupied", func(t *testing.T) {
			occupied, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = occupied.Close() }()
			t.Setenv(key, occupied.Addr().String())
			var output bytes.Buffer
			if code := execute(t.Context(), []string{"serve"}, &output); code != 1 {
				t.Fatalf("occupied listener accepted: %d %s", code, output.String())
			}
			// When the second bind fails, the first listener must be released.
			if key == "HTTP_ADDR" {
				address, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				public := address.Addr().String()
				_ = address.Close()
				t.Setenv("PUBLIC_ADDR", public)
				if code := execute(t.Context(), []string{"serve"}, &output); code != 1 {
					t.Fatal(code)
				}
				rebound, err := net.Listen("tcp", public)
				if err != nil {
					t.Fatalf("public port leaked: %v", err)
				}
				_ = rebound.Close()
			}
		})
	}
	for _, args := range [][]string{nil, {"help"}, {"migrate", "up"}, {"serve", "extra"}} {
		var output bytes.Buffer
		if code := mainExit(args, &output); code != 2 || !strings.Contains(output.String(), usage) {
			t.Fatalf("%v: %d %s", args, code, output.String())
		}
	}
	// Configure both log formats through the real command and listener assembly.
	t.Run("text-format", func(t *testing.T) {
		t.Setenv("LOG_FORMAT", "text")
		t.Setenv("ENV", "prod")
		_, stop, _ := startBFF(t)
		stop()
	})
}

func TestAPIReadinessDropsBeforeListenersClose(t *testing.T) {
	cleanEnvironment(t)
	t.Setenv("SHUTDOWN_DELAY", "200ms")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output := &logCapture{started: make(chan startup, 1)}
	done := make(chan int, 1)
	go func() { done <- execute(ctx, []string{"serve"}, output) }()
	var addr startup
	select {
	case addr = <-output.started:
	case <-time.After(5 * time.Second):
		t.Fatal("startup timeout")
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		code, _ := get(t, addr.operational, "/readyz")
		if code == 503 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("readiness never dropped")
		}
		time.Sleep(time.Millisecond)
	}
	if code, _ := get(t, addr.operational, "/healthz"); code != 200 {
		t.Fatal(code)
	}
	if code, _ := get(t, addr.public, "/v1/profile"); code != 404 {
		t.Fatal(code)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, output.text())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

// Ensure declarations are not accidentally expanded with database credentials or
// development authentication in this stateless scaffold.
func TestAPIStatelessScaffoldNeedsNoDatabaseOrAuthBypass(t *testing.T) {
	cleanEnvironment(t)
	for _, key := range []string{"DB_PRIMARY_DSN", "DB_PRIMARY_DSN_FILE", "AUTH_MODE", "GRPC_ADDR"} {
		t.Setenv(key, "intentionally-invalid")
	}
	_, stop, _ := startBFF(t)
	stop()
}
