package app

import (
	"bufio"
	"context"
	"net/http"
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
		resp, body := b.raw(t, request{method: "GET", path: path, header: base(nil)}) //nolint:bodyclose // raw reads and closes the body.
		if resp.StatusCode != 404 || !strings.Contains(string(body), `"code":"NOT_FOUND"`) || resp.Header.Get("X-Request-Id") == "" {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
	resp, _ := b.raw(t, request{method: "PATCH", path: "/v1/profile", body: `{"theme":"dark"}`, header: base(map[string]string{"Authorization": "Bearer " + access})}) //nolint:bodyclose // raw reads and closes the body.
	if resp.StatusCode != 404 {
		t.Fatalf("an operation outside FIN-268 is not served: %d", resp.StatusCode)
	}
	for _, path := range []string{"/", "/healthz", "/readyz", "/metrics"} {
		if resp, _ := b.raw(t, request{method: "GET", path: path}); resp.StatusCode != 404 { //nolint:bodyclose // raw reads and closes the body.
			t.Fatalf("public %s: %d", path, resp.StatusCode)
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
	first := make(chan []byte)
	go func() {
		_, body := b.raw(t, request{method: "GET", path: "/v1/auth/phone/events", header: base(map[string]string{"X-Verification-Token": "vt"})}) //nolint:bodyclose // raw reads and closes the body.
		first <- body
	}()
	<-opened
	r := b.do(t, request{method: "GET", path: "/v1/assistant/messages/a1/events", route: "/v1/assistant/messages/{answer_id}/events",
		header: base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})})
	if r.StatusCode != 429 || errorCode(r) != "RATE_LIMITED" || r.Header.Get("Retry-After") != "7" || r.json["error"].(map[string]any)["retry_at"] == nil {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if len(b.peers.Calls(assistantv1.AssistantService_WatchAnswer_FullMethodName)) != 0 {
		t.Fatal("a refused stream reached the service")
	}
	close(release)
	if events, _ := b.contract.Events(t, "GET", "/v1/auth/phone/events", strings.NewReader(string(<-first))); len(events) != 2 || events[1].Data["status"] != "expired" {
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
	resp, body := b.raw(t, request{method: "GET", path: "/v1/assistant/messages/a1/events", header: auth}) //nolint:bodyclose // raw reads and closes the body.
	events, pings := b.contract.Events(t, "GET", route, strings.NewReader(string(body)))
	if resp.StatusCode != 200 || len(events) != 2 || events[1].Name != "done" || pings < 3 {
		t.Fatalf("%d %v %d", resp.StatusCode, events, pings)
	}
	startAt := time.Now()
	resp, body = b.raw(t, request{method: "GET", path: "/v1/assistant/messages/slow/events", header: auth}) //nolint:bodyclose // raw reads and closes the body.
	events, _ = b.contract.Events(t, "GET", route, strings.NewReader(string(body)))
	if resp.StatusCode != 200 || len(events) != 1 || time.Since(startAt) < 600*time.Millisecond {
		t.Fatalf("stream deadline: %v after %v", events, time.Since(startAt))
	}
}
