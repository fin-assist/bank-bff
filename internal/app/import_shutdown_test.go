package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"google.golang.org/grpc"

	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

// streamingRejected observes the assembly's actual registry at integration level
// and the actual private HTTP metrics listener at API level.
func (b *bff) streamingRejected(t *testing.T, route string) float64 {
	t.Helper()
	if b.registry != nil {
		families, err := b.registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range families {
			if f.GetName() != "finassist_sse_rejected_total" {
				continue
			}
			for _, metric := range f.Metric {
				for _, label := range metric.Label {
					if label.GetName() == "route" && label.GetValue() == route {
						return metric.GetCounter().GetValue()
					}
				}
			}
		}
		return 0
	}
	req, err := http.NewRequestWithContext(t.Context(), "GET", "http://"+b.operational+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("finassist_sse_rejected_total{route=%s} ", strconv.Quote(route))
	for _, line := range strings.Split(string(body), "\n") {
		if value, ok := strings.CutPrefix(line, prefix); ok {
			n, err := strconv.ParseFloat(value, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	return 0
}

func scenarioImportShutdown(t *testing.T, start starter) {
	for _, forced := range []bool{false, true} {
		t.Run(fmt.Sprintf("forced=%t", forced), func(t *testing.T) {
			timeout := 2 * time.Second
			if forced {
				timeout = 200 * time.Millisecond
			}
			b := start(t, func(c *config.Config, _ *testpeer.Peers) { c.ShutdownTimeout = timeout })
			b.expectShutdownTimeout = forced
			headerSeen := make(chan struct{})
			peerEnded := make(chan bool, 1)
			b.peers.Statements.Set(func(s *testpeer.Statements) {
				s.UploadFunc = func(stream grpc.ClientStreamingServer[statementdatav1.UploadStatementRequest, statementdatav1.UploadStatementResponse]) error {
					defer func() { peerEnded <- stream.Context().Err() != nil }()
					first, err := stream.Recv()
					if err != nil {
						return err
					}
					close(headerSeen)
					for {
						_, err = stream.Recv()
						if errors.Is(err, io.EOF) {
							break
						}
						if err != nil {
							return err
						}
					}
					return stream.SendAndClose(&statementdatav1.UploadStatementResponse{UploadId: "u1", FileName: first.GetHeader().GetFileName()})
				}
			})
			reader, writer := io.Pipe()
			defer func() { _ = reader.Close(); _ = writer.Close() }()
			form := multipart.NewWriter(writer)
			ct := form.FormDataContentType()
			finishBody := make(chan struct{})
			finish := sync.OnceFunc(func() { close(finishBody) })
			defer finish()
			written := make(chan error, 1)
			go func() {
				part, err := form.CreateFormFile("file", "draining.ofx")
				if err == nil {
					<-finishBody
					_, err = part.Write([]byte("OFX bytes"))
				}
				if err == nil {
					err = form.Close()
				}
				_ = writer.CloseWithError(err)
				written <- err
			}()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, "POST", "http://"+b.public+"/v1/statements", reader)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", ct)
			req.Header.Set("X-Client-Version", clientVersion)
			req.Header.Set("Authorization", "Bearer "+b.keys.Access("u1"))
			req.Header.Set("Idempotency-Key", idemKey)
			type result struct {
				response response
				err      error
			}
			answer := make(chan result, 1)
			go func() {
				r, e := http.DefaultClient.Do(req)
				if e != nil {
					answer <- result{err: e}
					return
				}
				defer func() { _ = r.Body.Close() }()
				data, e := io.ReadAll(r.Body)
				answer <- result{response: response{Response: r, body: data}, err: e}
			}()
			await(t, headerSeen, "upload header before shutdown")
			stopped := make(chan struct{})
			started := time.Now()
			go func() { b.stop(); close(stopped) }()
			// Observe the closed listener, not an arbitrary delay before shutdown starts.
			waitCtx, stopWaiting := context.WithTimeout(t.Context(), time.Second)
			defer stopWaiting()
			for {
				conn, e := (&net.Dialer{}).DialContext(waitCtx, "tcp", b.public)
				if e != nil {
					if waitCtx.Err() != nil {
						t.Fatal("public listener did not close", e)
					}
					break
				}
				_ = conn.Close()
				select {
				case <-waitCtx.Done():
					t.Fatal("public listener did not stop accepting")
				case <-time.After(time.Millisecond):
				}
			}
			if !forced {
				select {
				case ended := <-peerEnded:
					t.Fatalf("upload cancelled at shutdown before body completion: %v", ended)
				case <-time.After(50 * time.Millisecond):
				}
				finish()
				if err := await(t, written, "completed upload body"); err != nil {
					t.Fatal(err)
				}
				out := await(t, answer, "drained upload response")
				if out.err != nil || out.response.StatusCode != 202 {
					t.Fatalf("ordinary upload did not drain: status=%v err=%v", out.response.Response, out.err)
				}
				_ = b.contract.Response(t, "POST", "/v1/statements", req, out.response.Response, out.response.body)
				await(t, stopped, "graceful upload shutdown")
				if b.stopErr != nil {
					t.Fatal(b.stopErr)
				}
			} else {
				await(t, stopped, "bounded forced close")
				if !errors.Is(b.stopErr, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
					t.Fatal("shutdown not bounded", b.stopErr, time.Since(started))
				}
				if !await(t, peerEnded, "cancelled upload after forced close") {
					t.Fatal("RPC not cancelled after forced connection close")
				}
				_ = reader.Close()
				finish()
				out := await(t, answer, "forced upload response")
				if out.err == nil {
					t.Fatalf("incomplete upload unexpectedly completed: %+v", out.response)
				}
				finish()
				_ = await(t, written, "forced body cleanup")
			}
		})
	}
}
