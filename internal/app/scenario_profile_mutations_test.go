package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/fin-assist/bank-bff/internal/endpoint"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

func profileDeadline(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > endpoint.UnaryDeadline {
		t.Error("profile RPC must have its unary deadline")
	}
}

// These scenarios exercise HTTP, verified identities, serialized optional fields
// and gRPC results. Lifecycle persistence and outbox belong to the user service.
func scenarioProfileMutations(t *testing.T, start starter) {
	b := start(t, nil)
	access := b.keys.Access("u1")
	auth := base(map[string]string{"Authorization": "Bearer " + access, "X-Read-Primary": "1", "Idempotency-Key": "not-a-key", "X-User-Id": "u2", "Cookie": "session=foreign"})
	var mu sync.Mutex
	var got *userv1.UpdateProfileRequest
	b.peers.Profile.Set(func(p *testpeer.Profile) {
		p.UpdateFunc = func(ctx context.Context, in *userv1.UpdateProfileRequest) (*userv1.UpdateProfileResponse, error) {
			profileDeadline(t, ctx)
			mu.Lock()
			got = proto.Clone(in).(*userv1.UpdateProfileRequest)
			mu.Unlock()
			if in.Timezone != nil && in.GetTimezone() == "Bad/Timezone" {
				return nil, finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "timezone", Reason: "INVALID_FORMAT", Description: "internal path /etc/localtime"})
			}
			out := profileMessage(false)
			if in.Theme != nil {
				out.Theme = in.GetTheme()
			}
			if in.Timezone != nil {
				out.Timezone = in.GetTimezone()
			}
			return &userv1.UpdateProfileResponse{Profile: out}, nil
		}
	})
	for _, tc := range []struct {
		body                             string
		theme                            *userv1.Theme
		tz                               *string
		wantTheme, wantZone, wantCreated string
	}{
		{`{}`, nil, nil, "system", "Europe/Moscow", "2026-10-04T22:10:00+03:00"},
		{`{"theme":"system"}`, userv1.Theme_THEME_SYSTEM.Enum(), nil, "system", "Europe/Moscow", "2026-10-04T22:10:00+03:00"},
		{`{"theme":"light"}`, userv1.Theme_THEME_LIGHT.Enum(), nil, "light", "Europe/Moscow", "2026-10-04T22:10:00+03:00"},
		{`{"theme":"dark"}`, userv1.Theme_THEME_DARK.Enum(), nil, "dark", "Europe/Moscow", "2026-10-04T22:10:00+03:00"},
		{`{"timezone":"Asia/Vladivostok"}`, nil, proto.String("Asia/Vladivostok"), "system", "Asia/Vladivostok", "2026-10-05T05:10:00+10:00"},
		{`{"theme":"dark","timezone":"UTC"}`, userv1.Theme_THEME_DARK.Enum(), proto.String("UTC"), "dark", "UTC", "2026-10-04T19:10:00Z"},
		{`{"theme":"dark","Theme":"light","Timezone":"UTC","user_id":"u2"}`, userv1.Theme_THEME_DARK.Enum(), nil, "dark", "Europe/Moscow", "2026-10-04T22:10:00+03:00"},
	} {
		r := b.do(t, request{method: "PATCH", path: "/v1/profile", route: "/v1/profile", body: tc.body, header: auth})
		mu.Lock()
		in := got
		mu.Unlock()
		if r.StatusCode != 200 || !proto.Equal(in, &userv1.UpdateProfileRequest{Theme: tc.theme, Timezone: tc.tz}) || r.json["theme"] != tc.wantTheme || r.json["timezone"] != tc.wantZone || r.json["created_at"] != tc.wantCreated || r.json["user_id"] != "u1" {
			t.Fatalf("%s: request %v, response %d %s", tc.body, in, r.StatusCode, r.body)
		}
		call := lastCall(t, b, userv1.ProfileService_UpdateProfile_FullMethodName, access, "")
		if len(call.MD.Get("x-user-id")) != 0 || len(call.MD.Get("x-read-primary")) != 1 || call.MD.Get("x-read-primary")[0] != "1" {
			t.Fatal("untrusted metadata or missing read-primary", call.MD)
		}
	}
	// IANA validity is a service rule; its structured violation is safely mapped.
	r := b.do(t, request{method: "PATCH", path: "/v1/profile", route: "/v1/profile", body: `{"timezone":"Bad/Timezone"}`, header: auth})
	if r.StatusCode != 400 || errorCode(r) != "VALIDATION_ERROR" || strings.Contains(string(r.body), "/etc/localtime") {
		t.Fatal(r.StatusCode, string(r.body))
	}
	details := r.json["error"].(map[string]any)["details"].([]any)
	if len(details) != 1 || details[0].(map[string]any)["field"] != "timezone" {
		t.Fatal(details)
	}
	before := len(b.peers.Calls(userv1.ProfileService_UpdateProfile_FullMethodName))
	for _, body := range []string{"", `{"theme":"future"}`, `{"theme":null}`, `{"timezone":null}`, `{"timezone":42}`, `[]`, `{`} {
		r := b.do(t, request{method: "PATCH", path: "/v1/profile", route: "/v1/profile", body: body, header: auth})
		if r.StatusCode != 400 || errorCode(r) != "VALIDATION_ERROR" {
			t.Fatalf("%s: %d %s", body, r.StatusCode, r.body)
		}
	}
	if len(b.peers.Calls(userv1.ProfileService_UpdateProfile_FullMethodName)) != before {
		t.Fatal("invalid body reached user")
	}
	if r := b.do(t, request{method: "PATCH", path: "/v1/profile", route: "/v1/profile", body: `{}`, header: base(map[string]string{"Authorization": "Bearer " + access, "Content-Type": "text/plain"})}); r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
}

func scenarioProfileDeletion(t *testing.T, start starter) {
	b := start(t, nil)
	access, other := b.keys.Access("u1"), b.keys.Access("u2")
	auth := base(map[string]string{"Authorization": "Bearer " + access})
	var mu sync.Mutex
	removed := map[string]bool{}
	b.peers.Profile.Set(func(p *testpeer.Profile) {
		p.DeleteFunc = func(ctx context.Context, _ *userv1.DeleteAccountRequest) (*userv1.DeleteAccountResponse, error) {
			profileDeadline(t, ctx)
			md, _ := metadata.FromIncomingContext(ctx)
			mu.Lock()
			removed[strings.Join(md.Get("authorization"), "")] = true
			mu.Unlock()
			return &userv1.DeleteAccountResponse{}, nil
		}
		p.GetFunc = func(ctx context.Context, _ *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error) {
			md, _ := metadata.FromIncomingContext(ctx)
			mu.Lock()
			deleted := removed[strings.Join(md.Get("authorization"), "")]
			mu.Unlock()
			if deleted {
				return nil, finerr.TokenRevoked()
			}
			out := profileMessage(false)
			out.UserId = "u2"
			return &userv1.GetProfileResponse{Profile: out}, nil
		}
		p.UpdateFunc = func(context.Context, *userv1.UpdateProfileRequest) (*userv1.UpdateProfileResponse, error) {
			return nil, finerr.TokenRevoked()
		}
	})
	req := request{method: "DELETE", path: "/v1/profile", route: "/v1/profile", header: auth}
	for range 2 {
		if r := b.do(t, req); r.StatusCode != 204 || len(r.body) != 0 {
			t.Fatal(r.StatusCode, string(r.body))
		}
		lastCall(t, b, userv1.ProfileService_DeleteAccount_FullMethodName, access, "")
	}
	for _, req := range []request{
		{method: "GET", path: "/v1/profile", route: "/v1/profile", header: auth},
		{method: "PATCH", path: "/v1/profile", route: "/v1/profile", header: auth, body: `{}`},
	} {
		if r := b.do(t, req); r.StatusCode != 401 || errorCode(r) != "TOKEN_REVOKED" {
			t.Fatal(r.StatusCode, string(r.body))
		}
	}
	if r := b.do(t, request{method: "GET", path: "/v1/profile", route: "/v1/profile", header: base(map[string]string{"Authorization": "Bearer " + other})}); r.StatusCode != 200 || r.json["user_id"] != "u2" {
		t.Fatal(r.StatusCode, string(r.body))
	}
	lastCall(t, b, userv1.ProfileService_GetProfile_FullMethodName, other, "")
	calls := len(b.peers.Calls(userv1.ProfileService_DeleteAccount_FullMethodName))
	req.body = `{"user_id":"u2"}`
	if r := b.do(t, req); r.StatusCode != 400 || len(b.peers.Calls(userv1.ProfileService_DeleteAccount_FullMethodName)) != calls {
		t.Fatal(r.StatusCode, string(r.body))
	}
	// Authentication still precedes body validation on both new routes.
	for _, method := range []string{"PATCH", "DELETE"} {
		for _, token := range []string{"", "Bearer invalid", "Bearer " + b.keys.Token("u1", time.Now().Add(-time.Hour), time.Now().Add(-time.Minute), testpeer.UserIssuer, testpeer.UserAudience), "Bearer " + testpeer.NewKeys(t).Access("u1")} {
			if r := b.do(t, request{method: method, path: "/v1/profile", route: "/v1/profile", body: `{`, header: base(map[string]string{"Authorization": token})}); r.StatusCode != 401 || errorCode(r) != "INVALID_TOKEN" {
				t.Fatal(method, r.StatusCode, string(r.body))
			}
		}
	}
	if len(b.peers.Calls(userv1.ProfileService_DeleteAccount_FullMethodName)) != calls {
		t.Fatal("rejected token reached deletion")
	}
}
