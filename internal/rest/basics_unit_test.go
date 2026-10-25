package rest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fin-assist/go-grpc/reqctx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/fin-assist/bank-bff/internal/catalog"
)

func TestUnitRequestIDEchoesUUIDAndReplacesOthers(t *testing.T) {
	var seen string
	var hints Hints
	h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, hints = reqctx.RequestID(r.Context()), HintsFrom(r.Context())
	}))
	const id = "0190c3a0-1b2c-7d3e-8f40-123456789abc"
	for _, tc := range []struct{ header, remote string }{{id, "203.0.113.7:4321"}, {"not a uuid\n", "bad"}, {"", "[2001:db8::1]:80"}} {
		req := httptest.NewRequest("GET", "/v1/profile", nil)
		req.RemoteAddr = tc.remote
		req.Header.Set("X-Request-Id", tc.header)
		req.Header.Set("X-Read-Primary", "1")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		echo := res.Header().Get("X-Request-Id")
		if echo != seen || !IsUUID(echo) || (tc.header == id) != (echo == id) {
			t.Fatalf("%q: echo %q seen %q", tc.header, echo, seen)
		}
		if !hints.ReadPrimary || hints.ClientVersion != "" || hints.IdempotencyKey != "" {
			t.Fatalf("hints %+v", hints)
		}
		if want := map[string]string{"203.0.113.7:4321": "203.0.113.7", "bad": "", "[2001:db8::1]:80": "2001:db8::1"}[tc.remote]; hints.ClientIP != want {
			t.Fatalf("client ip %q, want %q", hints.ClientIP, want)
		}
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Read-Primary", "true")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if hints.ReadPrimary {
		t.Fatal("only X-Read-Primary: 1 selects the primary")
	}
	if HintsFrom(context.Background()) != (Hints{}) {
		t.Fatal("hints without a request")
	}
}

func decodeError(t *testing.T, body string) errorObject {
	t.Helper()
	var b errorBody
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Fatal(err)
	}
	return b.Error
}

func TestUnitErrorWriteUsesCatalogAndFreshWait(t *testing.T) {
	res := httptest.NewRecorder()
	Error{Status: 429, Code: "RATE_LIMITED", RetryAt: "2026-10-25T20:00:00+03:00", RetryAfter: 1500 * time.Millisecond}.Write(res)
	got := decodeError(t, res.Body.String())
	entry, _ := catalog.REST("RATE_LIMITED")
	if res.Code != 429 || res.Header().Get("Retry-After") != "2" || res.Header().Get("Content-Type") != "application/json" ||
		got.UserMessage != entry.Text || got.Severity != entry.Severity || got.Message != entry.Diagnostic || got.RetryAt == "" {
		t.Fatalf("%d %v %+v", res.Code, res.Header(), got)
	}
	if strings.Contains(res.Body.String(), "retryable") {
		t.Fatal("REST retryable needs RequestRetryInfo (FIN-225, new gen/go tag): it stays absent")
	}
	res = httptest.NewRecorder()
	Error{Status: 418, Code: "SOMETHING_NEW"}.Write(res)
	got = decodeError(t, res.Body.String())
	if res.Header().Get("Retry-After") != "" || got.UserMessage == "" || got.Severity != catalog.Error || got.Details != nil {
		t.Fatalf("unknown code: %v %+v", res.Header(), got)
	}
	for in, want := range map[time.Duration]int64{0: 0, -time.Second: 0, time.Nanosecond: 1, time.Second: 1, 2*time.Second + 1: 3} {
		if got := RetryAfterSeconds(in); got != want {
			t.Fatalf("%v: %d", in, got)
		}
	}
	for _, e := range []Error{Internal(), Validation(), NotFound(), InvalidToken(), TooLarge()} {
		if _, known := catalog.REST(e.Code); !known || e.Status < 400 {
			t.Fatalf("%+v", e)
		}
	}
	res = httptest.NewRecorder()
	WriteNoContent(res)
	if res.Code != 204 || res.Body.Len() != 0 {
		t.Fatal(res.Code)
	}
}

func TestUnitVersionsCompareNumerically(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		less bool
	}{{"0.2.4", "0.2.5", true}, {"0.10.0", "0.9.9", false}, {"0.2.5", "0.2.5", false}, {"1.0.0", "0.99.99", false}, {"9.0.0", "10.0.0", true}, {"123456789012345678901.0.0", "123456789012345678902.0.0", true}} {
		a, errA := ParseVersion(tc.a)
		b, errB := ParseVersion(tc.b)
		if errA != nil || errB != nil || a.Less(b) != tc.less {
			t.Fatalf("%s < %s: %v %v %v", tc.a, tc.b, a.Less(b), errA, errB)
		}
	}
	for _, bad := range []string{"", "1.2", "1.2.3.4", "01.2.3", "1.x.3", "1..3", "-1.2.3"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	for _, header := range []string{"android/0.2.5", "Android/0.2.5+1", "android/0.02.5+1", "android/0.2.5+x", "1os/0.1.0+1", "android/0.2.5+1 ", strings.Repeat("a", 54) + "/0.2.5+1234"} {
		if _, ok := ParseClient(header); ok {
			t.Fatalf("%q parsed", header)
		}
	}
	c, ok := ParseClient("ios_beta/10.20.30+999")
	if !ok || c.Platform != "ios_beta" || c.Label() != "ios_beta/10.20" {
		t.Fatalf("%+v %v", c, ok)
	}
	if !ValidPlatform("web") || ValidPlatform("Web") || ValidPlatform("") {
		t.Fatal("platform shape")
	}
}

func TestUnitGatesOrderMaintenanceThenVersion(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	g := Gates{MinVersions: map[string]Version{"android": {"0", "2", "5"}}, Clock: func() time.Time { return now }}
	check := func(g Gates, values ...string) (*Error, Client) {
		req := httptest.NewRequest("GET", "/v1/profile", nil)
		for _, v := range values {
			req.Header.Add("X-Client-Version", v)
		}
		c, e := g.Check(req)
		return e, c
	}
	if e, c := check(g, "android/0.2.5+40"); e != nil || c.Label() != "android/0.2" {
		t.Fatalf("equal minimum passes: %+v", e)
	}
	if e, _ := check(g, "android/0.10.0+1"); e != nil {
		t.Fatal("numeric comparison")
	}
	for _, values := range [][]string{nil, {"android/0.2.4+99"}, {"ios/9.9.9+1"}, {"garbage"}, {"android/0.2.5+1", "android/0.2.5+1"}} {
		if e, _ := check(g, values...); e == nil || e.Status != 400 || e.Code != "CLIENT_VERSION_UNSUPPORTED" {
			t.Fatalf("%v: %+v", values, e)
		}
	}
	m := g
	m.Maintenance = Maintenance{Enabled: true, RetryAfter: time.Minute}
	if e, _ := check(m); e == nil || e.Code != "MAINTENANCE" || e.Status != 503 || e.RetryAfter != time.Minute || e.Until != "" {
		t.Fatalf("maintenance precedes the version check: %+v", e)
	}
	m.Maintenance.Until = now.Add(90 * time.Second)
	if e, _ := check(m, "android/0.2.5+1"); e.RetryAfter != 90*time.Second || e.Until != "2026-10-25T12:01:30Z" {
		t.Fatalf("until: %+v", e)
	}
	m.Maintenance.Until = now.Add(-time.Hour)
	if e, _ := check(m); e.RetryAfter != time.Minute || e.Until == "" {
		t.Fatalf("passed until keeps the configured wait: %+v", e)
	}
}

func TestUnitOutgoingHintsAreAllowlisted(t *testing.T) {
	var got metadata.MD
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		got, _ = metadata.FromOutgoingContext(ctx)
		return nil
	}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-service-authorization", "Bearer svc"))
	if err := UnaryHints()(ctx, "/m", nil, nil, nil, invoker); err != nil || got.Len() != 1 {
		t.Fatalf("no hints add nothing: %v %v", got, err)
	}
	ctx = WithHints(ctx, Hints{ClientIP: "203.0.113.7", ClientVersion: "android/0.2.5+41", ReadPrimary: true, IdempotencyKey: "k"})
	if err := UnaryHints()(ctx, "/m", nil, nil, nil, invoker); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"x-client-ip": "203.0.113.7", "x-client-version": "android/0.2.5+41", "x-read-primary": "1", "idempotency-key": "k", "x-service-authorization": "Bearer svc"} {
		if values := got.Get(k); len(values) != 1 || values[0] != v {
			t.Fatalf("%s: %v", k, values)
		}
	}
	streamer := func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
		got, _ = metadata.FromOutgoingContext(ctx)
		return nil, nil
	}
	if _, err := StreamHints()(WithHints(context.Background(), Hints{ClientIP: "198.51.100.1"}), nil, nil, "/m", streamer); err != nil || got.Get("x-client-ip")[0] != "198.51.100.1" || len(got.Get("x-read-primary")) != 0 {
		t.Fatalf("stream hints: %v", got)
	}
}

func TestUnitStreamLimiter(t *testing.T) {
	l := NewStreamLimiter(StreamLimits{Max: 3, PerIP: 2, RetryAfter: 1500 * time.Millisecond})
	r1, ok1 := l.Acquire("a")
	_, ok2 := l.Acquire("a")
	if _, ok := l.Acquire("a"); !ok1 || !ok2 || ok {
		t.Fatal("per-address limit")
	}
	r3, ok3 := l.Acquire("b")
	if _, ok := l.Acquire("c"); !ok3 || ok {
		t.Fatal("total limit")
	}
	r1()
	r1() // idempotent
	if _, ok := l.Acquire("c"); !ok {
		t.Fatal("released slot is reusable")
	}
	r3()
	if l.perIP["b"] != 0 || l.total != 2 {
		t.Fatalf("%d %v", l.total, l.perIP)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := l.Busy(now)
	if e.Status != 429 || e.Code != "RATE_LIMITED" || e.RetryAt != "2026-01-01T00:00:02Z" || RetryAfterSeconds(e.RetryAfter) != 2 {
		t.Fatalf("%+v", e)
	}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	failWrite                 bool
	readDeadline, writeDeadln time.Time
	failDeadline              string
}

func (f *flushRecorder) Write(b []byte) (int, error) {
	if f.failWrite {
		return 0, errors.New("broken pipe")
	}
	return f.ResponseRecorder.Write(b)
}
func (f *flushRecorder) SetReadDeadline(t time.Time) error {
	if f.failDeadline == "read" {
		return errors.New("no deadline")
	}
	f.readDeadline = t
	return nil
}
func (f *flushRecorder) SetWriteDeadline(t time.Time) error {
	if f.failDeadline == "write" {
		return errors.New("no deadline")
	}
	f.writeDeadln = t
	return nil
}

func TestUnitStreamWritesEventsAndExtendsDeadlines(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	s, err := StartStream(w, deadline)
	if err != nil || !w.writeDeadln.Equal(deadline) || !w.readDeadline.Equal(deadline) || w.Header().Get("Content-Type") != "text/event-stream" || w.Code != 200 || !w.Flushed {
		t.Fatalf("%v %+v", err, w)
	}
	if err := s.Event("status", map[string]string{"status": "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ping(); err != nil {
		t.Fatal(err)
	}
	if got := w.Body.String(); got != "event: status\ndata: {\"status\":\"pending\"}\n\n: ping\n\n" {
		t.Fatalf("%q", got)
	}
	if err := s.Event("bad", func() {}); err == nil {
		t.Fatal("unencodable data")
	}
	w.failWrite = true
	if s.Event("x", 1) == nil || s.Ping() == nil {
		t.Fatal("write errors are returned")
	}
	for _, which := range []string{"read", "write"} {
		if _, err := StartStream(&flushRecorder{ResponseRecorder: httptest.NewRecorder(), failDeadline: which}, deadline); err == nil {
			t.Fatalf("%s deadline error ignored", which)
		}
	}
}

func TestUnitPump(t *testing.T) {
	type msg = int
	feed := func(items ...any) (func() (msg, error), func()) {
		ch := make(chan any, len(items))
		for _, it := range items {
			ch <- it
		}
		stopped := make(chan struct{})
		recv := func() (msg, error) {
			select {
			case it := <-ch:
				if err, ok := it.(error); ok {
					return 0, err
				}
				return it.(int), nil
			case <-stopped:
				return 0, context.Canceled
			}
		}
		var once bool
		return recv, func() {
			if !once {
				once = true
				close(stopped)
			}
		}
	}
	var got []int
	handle := func(m msg) (bool, error) { got = append(got, m); return m == 3, nil }
	noPing := func() error { return nil }
	recv, stop := feed(1, 2, 3, 4)
	if err := Pump(context.Background(), recv, stop, time.Hour, noPing, handle); err != nil || len(got) != 3 {
		t.Fatalf("terminal message ends the pump: %v %v", err, got)
	}
	failure := errors.New("transport")
	recv, stop = feed(failure)
	if err := Pump(context.Background(), recv, stop, time.Hour, noPing, handle); !errors.Is(err, failure) {
		t.Fatalf("recv error: %v", err)
	}
	recv, stop = feed(7)
	if err := Pump(context.Background(), recv, stop, time.Hour, noPing, func(int) (bool, error) { return false, io.ErrClosedPipe }); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("handle error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	recv, stop = feed()
	pings := 0
	ping := func() error {
		if pings++; pings == 2 {
			cancel()
		}
		return nil
	}
	if err := Pump(ctx, recv, stop, time.Millisecond, ping, handle); !errors.Is(err, context.Canceled) || pings < 2 {
		t.Fatalf("heartbeat then cancellation: %v %d", err, pings)
	}
	recv, stop = feed()
	if err := Pump(context.Background(), recv, stop, time.Millisecond, func() error { return io.ErrClosedPipe }, handle); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("ping error: %v", err)
	}
}
