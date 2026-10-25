package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Heartbeat is the interval of the SSE comment `: ping` (docs/api.md, «SSE»).
const Heartbeat = 15 * time.Second

// StreamLimits bound concurrent SSE streams of this process: in total and per client
// address. A stream holds a connection and a peer stream for minutes, so the unary
// limits of the listener do not bound it.
type StreamLimits struct {
	Max, PerIP int
	// RetryAfter is the wait announced to a refused client.
	RetryAfter time.Duration
}

// StreamLimiter counts open streams and ends them on shutdown. It is safe for
// concurrent use.
type StreamLimiter struct {
	mu     sync.Mutex
	limits StreamLimits
	total  int
	perIP  map[string]int
	// closing ends with Shutdown: open streams stop so the server drains promptly.
	closing  context.Context
	shutdown context.CancelFunc
}

// NewStreamLimiter returns a limiter with the given limits.
func NewStreamLimiter(l StreamLimits) *StreamLimiter {
	closing, shutdown := context.WithCancel(context.Background())
	return &StreamLimiter{limits: l, perIP: make(map[string]int), closing: closing, shutdown: shutdown}
}

// Shutdown ends every open stream without a terminal event (the client reconnects
// and receives the current state first). It is registered with
// http.Server.RegisterOnShutdown, so a graceful shutdown does not wait for streams
// until its timeout.
func (l *StreamLimiter) Shutdown() { l.shutdown() }

// StopOnShutdown calls cancel when Shutdown is called; the returned stop detaches it.
func (l *StreamLimiter) StopOnShutdown(cancel context.CancelFunc) (stop func() bool) {
	return context.AfterFunc(l.closing, cancel)
}

// Acquire reserves a stream slot for ip; release must be called exactly once.
func (l *StreamLimiter) Acquire(ip string) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= l.limits.Max || l.perIP[ip] >= l.limits.PerIP {
		return nil, false
	}
	l.total++
	l.perIP[ip]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.total--
			if l.perIP[ip]--; l.perIP[ip] <= 0 {
				delete(l.perIP, ip)
			}
		})
	}, true
}

// Busy is the 429 RATE_LIMITED of a refused stream, with a fresh retry_at.
func (l *StreamLimiter) Busy(now time.Time) Error {
	wait := l.limits.RetryAfter
	return Error{Status: http.StatusTooManyRequests, Code: "RATE_LIMITED", RetryAfter: wait,
		RetryAt: now.Add(time.Duration(RetryAfterSeconds(wait)) * time.Second).Format(time.RFC3339)}
}

// Stream writes server-sent events. It is used by one handler goroutine.
type Stream struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// ExtendDeadlines moves the connection read and write deadlines to the stream's own
// deadline. The listener timeouts are for unary requests; a stream calls this before
// it waits for the first peer message, so neither the server write timeout nor the
// read timeout cuts it or a late JSON refusal. Nothing is written.
func ExtendDeadlines(w http.ResponseWriter, deadline time.Time) error {
	rc := http.NewResponseController(w)
	// A writer without connection deadlines (an in-process recorder) has no listener
	// timeouts to extend.
	if err := rc.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return fmt.Errorf("sse write deadline: %w", err)
	}
	if err := rc.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return fmt.Errorf("sse read deadline: %w", err)
	}
	return nil
}

// StartStream commits 200 text/event-stream; call ExtendDeadlines first.
func StartStream(w http.ResponseWriter) (*Stream, error) {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &Stream{w: w, rc: rc}
	return s, s.rc.Flush()
}

// Event writes one event with a JSON data line and flushes it.
func (s *Stream) Event(name string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("sse event %s: %w", name, err)
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return err
	}
	return s.rc.Flush()
}

// Ping writes the heartbeat comment.
func (s *Stream) Ping() error {
	if _, err := fmt.Fprint(s.w, ": ping\n\n"); err != nil {
		return err
	}
	return s.rc.Flush()
}

// Pump relays messages of recv to handle, writing a heartbeat every interval while
// it waits. It stops when handle reports a terminal message, recv or handle fails, or
// ctx ends. Before returning it calls stop, which must cancel the peer stream so that
// a blocked recv returns, and waits for its receiving goroutine.
func Pump[T any](ctx context.Context, recv func() (T, error), stop func(), interval time.Duration, ping func() error, handle func(T) (bool, error)) error {
	type item struct {
		msg T
		err error
	}
	quit := make(chan struct{})
	items := make(chan item)
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			msg, err := recv()
			select {
			case items <- item{msg, err}:
			case <-quit:
				return
			}
			if err != nil {
				return
			}
		}
	})
	defer func() {
		close(quit)
		stop()
		wg.Wait()
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := ping(); err != nil {
				return err
			}
		case it := <-items:
			if it.err != nil {
				return it.err
			}
			done, err := handle(it.msg)
			if err != nil || done {
				return err
			}
		}
	}
}
