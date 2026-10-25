package app

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"google.golang.org/grpc"

	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/rest"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

func scenarioGates(t *testing.T, start starter) {
	b := start(t, nil)
	access := b.keys.Access("u1")
	routes := []request{
		{method: "POST", path: "/v1/auth/phone", route: "/v1/auth/phone", body: `{"phone":"+79001234567"}`},
		{method: "GET", path: "/v1/auth/phone/events", route: "/v1/auth/phone/events"},
		{method: "GET", path: "/v1/profile", route: "/v1/profile"},
		{method: "GET", path: "/v1/assistant/messages/a1/events", route: "/v1/assistant/messages/{answer_id}/events"},
		{method: "GET", path: "/v1/consents/personal_data", route: "/v1/consents/{type}"},
	}
	// The client version is checked before the token and the schema.
	for _, version := range []string{"", "android/0.2.4+99", "ios/1.0.0+1", "android/latest"} {
		for _, r := range routes {
			r.header = base(map[string]string{"X-Client-Version": version, "Authorization": "Bearer expired"})
			if got := b.do(t, r); got.StatusCode != 400 || errorCode(got) != "CLIENT_VERSION_UNSUPPORTED" {
				t.Fatalf("%q %s: %d %s", version, r.path, got.StatusCode, got.body)
			}
		}
	}
	for _, c := range b.peers.Calls("") {
		if c.Method != userv1.UserInternalService_GetSigningKeys_FullMethodName {
			t.Fatalf("an unsupported client reached %s", c.Method)
		}
	}
	// X-Request-Id: a UUID is echoed and forwarded; anything else is replaced.
	b.peers.Profile.Set(func(p *testpeer.Profile) {
		p.GetFunc = func(context.Context, *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error) {
			return &userv1.GetProfileResponse{Profile: profileMessage(false)}, nil
		}
	})
	for _, id := range []string{requestID, "not-a-uuid", ""} {
		r := b.do(t, request{method: "GET", path: "/v1/profile", route: "/v1/profile", header: map[string]string{"X-Request-Id": id, "Authorization": "Bearer " + access}})
		echo := r.Header.Get("X-Request-Id")
		calls := b.peers.Calls(userv1.ProfileService_GetProfile_FullMethodName)
		forwarded := calls[len(calls)-1].MD.Get("x-request-id")
		if r.StatusCode != 200 || !rest.IsUUID(echo) || (id == requestID) != (echo == requestID) || len(forwarded) != 1 || forwarded[0] != echo {
			t.Fatalf("%q: echo %q forwarded %v", id, echo, forwarded)
		}
	}
	// Other paths: JSON 404 inside /v1, plain 404 outside, no operational endpoint.
	for _, path := range []string{"/v1/unknown", "/v1/profile/extra", "/v1/statements"} {
		got := b.raw(t, request{method: "GET", path: path, header: base(nil)})
		resp, body := got.Response, got.body
		if resp.StatusCode != 404 || !strings.Contains(string(body), `"code":"NOT_FOUND"`) || resp.Header.Get("X-Request-Id") == "" {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
	got := b.raw(t, request{method: "PATCH", path: "/v1/profile", body: `{"theme":"dark"}`, header: base(map[string]string{"Authorization": "Bearer " + access})})
	resp := got.Response
	if resp.StatusCode != 404 {
		t.Fatalf("an operation outside FIN-268 is not served: %d", resp.StatusCode)
	}
	for _, path := range []string{"/", "/healthz", "/readyz", "/metrics"} {
		if got := b.raw(t, request{method: "GET", path: path}); got.StatusCode != 404 {
			t.Fatalf("public %s: %d", path, got.StatusCode)
		}
	}
}

func scenarioMaintenance(t *testing.T, start starter) {
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	b := start(t, func(c *config.Config, _ *testpeer.Peers) {
		c.API.Maintenance = rest.Maintenance{Enabled: true, Until: until, RetryAfter: time.Minute}
	})
	for _, r := range []request{
		{method: "POST", path: "/v1/auth/phone", route: "/v1/auth/phone", body: `{"phone":"+79001234567"}`},
		{method: "GET", path: "/v1/profile", route: "/v1/profile"},
		{method: "GET", path: "/v1/assistant/messages/a1/events", route: "/v1/assistant/messages/{answer_id}/events"},
	} {
		// Maintenance precedes the client version check.
		r.header = base(map[string]string{"X-Client-Version": "android/0.0.1+1"})
		got := b.do(t, r)
		if got.StatusCode != 503 || errorCode(got) != "MAINTENANCE" || got.json["error"].(map[string]any)["until"] != until.Format(time.RFC3339) {
			t.Fatalf("%s: %d %s", r.path, got.StatusCode, got.body)
		}
		if ra := got.Header.Get("Retry-After"); ra == "" || ra == "60" {
			t.Fatalf("Retry-After counts down to until: %q", ra)
		}
	}
	if len(b.peers.Calls(userv1.AuthService_StartPhoneVerification_FullMethodName)) != 0 {
		t.Fatal("maintenance reached a service")
	}
}

func scenarioStreamLimit(t *testing.T, start starter) {
	b := start(t, func(c *config.Config, _ *testpeer.Peers) {
		c.API.Streams = rest.StreamLimits{Max: 1, PerIP: 1, RetryAfter: 7 * time.Second}
	}, WithStreamTimings(time.Hour, 5*time.Second, 5*time.Second))
	opened, release := make(chan struct{}), make(chan struct{})
	b.peers.Auth.Set(func(a *testpeer.Auth) {
		a.WatchFunc = func(_ *userv1.WatchPhoneVerificationRequest, s grpc.ServerStreamingServer[userv1.WatchPhoneVerificationResponse]) error {
			_ = s.Send(&userv1.WatchPhoneVerificationResponse{Status: userv1.VerificationStatus_VERIFICATION_STATUS_PENDING})
			close(opened)
			<-release
			return s.Send(&userv1.WatchPhoneVerificationResponse{Status: userv1.VerificationStatus_VERIFICATION_STATUS_EXPIRED})
		}
	})
	first := make(chan []byte, 1)
	go func() {
		got, err := b.fetch(t.Context(), request{method: "GET", path: "/v1/auth/phone/events", header: base(map[string]string{"X-Verification-Token": "vt"})})
		if err != nil {
			t.Error(err)
		}
		first <- got.body
	}()
	select {
	case <-opened:
	case body := <-first:
		t.Fatalf("the first stream was refused: %s", body)
	case <-time.After(5 * time.Second):
		t.Fatal("the first stream was not opened")
	}
	r := b.do(t, request{method: "GET", path: "/v1/assistant/messages/a1/events", route: "/v1/assistant/messages/{answer_id}/events",
		header: base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})})
	if r.StatusCode != 429 || errorCode(r) != "RATE_LIMITED" || r.Header.Get("Retry-After") != "7" || r.json["error"].(map[string]any)["retry_at"] == nil {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if len(b.peers.Calls(assistantv1.AssistantService_WatchAnswer_FullMethodName)) != 0 {
		t.Fatal("a refused stream reached the service")
	}
	close(release)
	if events, _ := b.contract.Events(t, "GET", "/v1/auth/phone/events", strings.NewReader(string(await(t, first, "first stream")))); len(events) != 2 || events[1].Data["status"] != "expired" {
		t.Fatalf("%v", events)
	}
}

// scenarioClientDisconnect: closing the HTTP stream cancels the peer stream; the
// BFF holds nothing after the client left (the generation itself is the service's).
func scenarioClientDisconnect(t *testing.T, start starter) {
	b := start(t, nil, WithStreamTimings(10*time.Millisecond, 10*time.Second, 10*time.Second))
	cancelled := make(chan struct{})
	b.peers.Assistant.Set(func(a *testpeer.Assistant) {
		a.WatchFunc = func(_ *assistantv1.WatchAnswerRequest, s grpc.ServerStreamingServer[assistantv1.WatchAnswerResponse]) error {
			_ = s.Send(&assistantv1.WatchAnswerResponse{Event: &assistantv1.WatchAnswerResponse_Snapshot{Snapshot: &assistantv1.AnswerSnapshot{Status: assistantv1.AnswerStatus_ANSWER_STATUS_GENERATING, Currency: "RUB"}}})
			<-s.Context().Done()
			close(cancelled)
			return s.Context().Err()
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+b.public+"/v1/assistant/messages/a1/events", nil)
	req.Header.Set("X-Client-Version", clientVersion)
	req.Header.Set("Authorization", "Bearer "+b.keys.Access("u1"))
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "event: snapshot\n" {
		t.Fatalf("%q %v", line, err)
	}
	cancel()
	_ = resp.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the peer stream outlived the client")
	}
}

// scenarioStreamOutlivesUnaryTimeouts: the listener read/write timeouts bound unary
// answers; an SSE stream extends its own deadlines and is not cut or cancelled by
// them. Its own deadline then ends it without a terminal event.
func scenarioStreamOutlivesUnaryTimeouts(t *testing.T, start starter) {
	b := start(t, nil, WithServerTimeouts(150*time.Millisecond, 200*time.Millisecond), WithStreamTimings(30*time.Millisecond, 700*time.Millisecond, time.Second))
	b.peers.Assistant.Set(func(a *testpeer.Assistant) {
		a.WatchFunc = func(in *assistantv1.WatchAnswerRequest, s grpc.ServerStreamingServer[assistantv1.WatchAnswerResponse]) error {
			if in.GetAnswerId() == "late" {
				<-time.After(400 * time.Millisecond) // a refusal after both listener timeouts, before any message
				return lateRefusal()
			}
			_ = s.Send(&assistantv1.WatchAnswerResponse{Event: &assistantv1.WatchAnswerResponse_Snapshot{Snapshot: &assistantv1.AnswerSnapshot{Status: assistantv1.AnswerStatus_ANSWER_STATUS_GENERATING, Currency: "RUB"}}})
			if in.GetAnswerId() == "slow" {
				<-s.Context().Done() // the BFF stream deadline ends it
				return s.Context().Err()
			}
			<-time.After(400 * time.Millisecond) // longer than both listener timeouts
			return s.Send(&assistantv1.WatchAnswerResponse{Event: &assistantv1.WatchAnswerResponse_Done{Done: &assistantv1.AnswerDone{Charged: true, RemainingLimit: 4}}})
		}
	})
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	route := "/v1/assistant/messages/{answer_id}/events"
	got := b.raw(t, request{method: "GET", path: "/v1/assistant/messages/a1/events", header: auth})
	resp, body := got.Response, got.body
	events, pings := b.contract.Events(t, "GET", route, strings.NewReader(string(body)))
	if resp.StatusCode != 200 || len(events) != 2 || events[1].Name != "done" || pings < 3 {
		t.Fatalf("%d %v %d", resp.StatusCode, events, pings)
	}
	startAt := time.Now()
	got = b.raw(t, request{method: "GET", path: "/v1/assistant/messages/slow/events", header: auth})
	resp, body = got.Response, got.body
	events, _ = b.contract.Events(t, "GET", route, strings.NewReader(string(body)))
	if resp.StatusCode != 200 || len(events) != 1 || time.Since(startAt) < 600*time.Millisecond {
		t.Fatalf("stream deadline: %v after %v", events, time.Since(startAt))
	}
	// A peer refusing a stream after the unary timeouts, before its first message, is
	// still a complete JSON answer: the deadlines are extended before the first wait.
	b.peers.Auth.Set(func(a *testpeer.Auth) {
		a.WatchFunc = func(*userv1.WatchPhoneVerificationRequest, grpc.ServerStreamingServer[userv1.WatchPhoneVerificationResponse]) error {
			<-time.After(400 * time.Millisecond)
			return lateRefusal()
		}
	})
	for _, r := range []request{
		{method: "GET", path: "/v1/assistant/messages/late/events", route: route, header: auth},
		{method: "GET", path: "/v1/auth/phone/events", route: "/v1/auth/phone/events", header: base(map[string]string{"X-Verification-Token": "vt"})},
	} {
		if got := b.do(t, r); got.StatusCode != 500 || errorCode(got) != "INTERNAL_ERROR" {
			t.Fatalf("late refusal of %s: %d %s", r.path, got.StatusCode, got.body)
		}
	}
}

// lateRefusal is a peer failure without a registry reason (gRPC Unknown): the BFF
// answers it with a generic 500.
func lateRefusal() error { return errors.New("peer is down") }

// scenarioShutdownEndsStreams: a graceful shutdown ends open SSE streams at once,
// without a terminal event, instead of waiting for them until its timeout.
func scenarioShutdownEndsStreams(t *testing.T, start starter) {
	b := start(t, func(c *config.Config, _ *testpeer.Peers) { c.ShutdownTimeout = 10 * time.Second },
		WithStreamTimings(time.Hour, time.Minute, time.Minute))
	opened, peerDone := make(chan struct{}), make(chan struct{})
	b.peers.Auth.Set(func(a *testpeer.Auth) {
		a.WatchFunc = func(_ *userv1.WatchPhoneVerificationRequest, s grpc.ServerStreamingServer[userv1.WatchPhoneVerificationResponse]) error {
			_ = s.Send(&userv1.WatchPhoneVerificationResponse{Status: userv1.VerificationStatus_VERIFICATION_STATUS_PENDING})
			close(opened)
			<-s.Context().Done()
			close(peerDone)
			return s.Context().Err()
		}
	})
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "http://"+b.public+"/v1/auth/phone/events", nil)
	req.Header.Set("X-Client-Version", clientVersion)
	req.Header.Set("X-Verification-Token", "vt")
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := bufio.NewReader(resp.Body)
	// The client holds the current state before the shutdown starts.
	var first strings.Builder
	for !strings.HasSuffix(first.String(), "\n\n") {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("%q %v", first.String(), err)
		}
		first.WriteString(line)
	}
	<-opened
	startAt := time.Now()
	b.stop()
	if elapsed := time.Since(startAt); elapsed > 5*time.Second {
		t.Fatalf("shutdown waited %v for an open stream", elapsed)
	}
	tail, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("the stream did not end cleanly: %v", err)
	}
	events, _ := b.contract.Events(t, "GET", "/v1/auth/phone/events", strings.NewReader(first.String()+string(tail)))
	if len(events) != 1 || events[0].Data["status"] != "pending" {
		t.Fatalf("a stream ended by shutdown has no terminal event: %v", events)
	}
	select {
	case <-peerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the peer stream outlived the shutdown")
	}
}

// scenarioTrustedProxies: behind a trusted proxy the client address is the rightmost
// untrusted X-Forwarded-For element; it is forwarded as x-client-ip and keys the
// per-address stream limit. A forged header of an untrusted peer is ignored.
func scenarioTrustedProxies(t *testing.T, start starter) {
	untrusted := start(t, nil)
	untrusted.peers.Profile.Set(func(p *testpeer.Profile) {
		p.GetFunc = func(context.Context, *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error) {
			return &userv1.GetProfileResponse{Profile: profileMessage(false)}, nil
		}
	})
	profile := request{method: "GET", path: "/v1/profile", route: "/v1/profile",
		header: base(map[string]string{"Authorization": "Bearer " + untrusted.keys.Access("u1"), "X-Forwarded-For": "198.51.100.1"})}
	if r := untrusted.do(t, profile); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	calls := untrusted.peers.Calls(userv1.ProfileService_GetProfile_FullMethodName)
	if ip := calls[len(calls)-1].MD.Get("x-client-ip"); len(ip) != 1 || ip[0] != "127.0.0.1" {
		t.Fatalf("an untrusted peer chose its address: %v", ip)
	}

	b := start(t, func(c *config.Config, _ *testpeer.Peers) {
		c.API.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8")}
		c.API.Streams = rest.StreamLimits{Max: 10, PerIP: 1, RetryAfter: time.Second}
	})
	b.peers.Profile.Set(func(p *testpeer.Profile) {
		p.GetFunc = func(context.Context, *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error) {
			return &userv1.GetProfileResponse{Profile: profileMessage(false)}, nil
		}
	})
	profile.header = base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1"), "X-Forwarded-For": "192.0.2.66, 198.51.100.1, 10.1.2.3"})
	if r := b.do(t, profile); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	calls = b.peers.Calls(userv1.ProfileService_GetProfile_FullMethodName)
	if ip := calls[len(calls)-1].MD.Get("x-client-ip"); len(ip) != 1 || ip[0] != "198.51.100.1" {
		t.Fatalf("client behind a proxy chain: %v", ip)
	}
	// A malformed or long prefix written by the client does not move it into the
	// proxy's bucket: only the elements appended by the trusted proxies are examined.
	for _, xff := range []string{"unknown, 198.51.100.7", strings.Repeat("junk, ", 16) + "198.51.100.7"} {
		profile.header["X-Forwarded-For"] = xff
		if r := b.do(t, profile); r.StatusCode != 200 {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
		calls = b.peers.Calls(userv1.ProfileService_GetProfile_FullMethodName)
		if ip := calls[len(calls)-1].MD.Get("x-client-ip"); len(ip) != 1 || ip[0] != "198.51.100.7" {
			t.Fatalf("%q: %v", xff, ip)
		}
	}
	// Two clients behind the same proxy hold one stream each; a second stream of the
	// first client is over its per-address limit.
	opened, release := make(chan struct{}, 2), make(chan struct{})
	b.peers.Auth.Set(func(a *testpeer.Auth) {
		a.WatchFunc = func(_ *userv1.WatchPhoneVerificationRequest, s grpc.ServerStreamingServer[userv1.WatchPhoneVerificationResponse]) error {
			_ = s.Send(&userv1.WatchPhoneVerificationResponse{Status: userv1.VerificationStatus_VERIFICATION_STATUS_PENDING})
			opened <- struct{}{}
			<-release
			return s.Send(&userv1.WatchPhoneVerificationResponse{Status: userv1.VerificationStatus_VERIFICATION_STATUS_EXPIRED})
		}
	})
	watch := func(client string) request {
		return request{method: "GET", path: "/v1/auth/phone/events", route: "/v1/auth/phone/events",
			header: base(map[string]string{"X-Verification-Token": "vt", "X-Forwarded-For": client})}
	}
	done := make(chan struct{}, 2)
	for _, client := range []string{"198.51.100.1", "198.51.100.2"} {
		go func() {
			if got, err := b.fetch(t.Context(), watch(client)); err != nil || got.StatusCode != 200 {
				t.Errorf("stream of %s: %v %d %s", client, err, got.StatusCode, got.body)
			}
			done <- struct{}{}
		}()
		select {
		case <-opened:
		case <-done:
			t.Fatalf("stream of %s was refused", client)
		case <-time.After(5 * time.Second):
			t.Fatalf("stream of %s was not opened", client)
		}
	}
	if r := b.do(t, watch("198.51.100.1")); r.StatusCode != 429 || errorCode(r) != "RATE_LIMITED" {
		t.Fatalf("second stream of one client: %d %s", r.StatusCode, r.body)
	}
	close(release)
	await(t, done, "first stream")
	await(t, done, "second stream")
}
