package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"

	"github.com/fin-assist/bank-bff/internal/testpeer"
)

// The serve command reads the stage environment, builds identity and peer clients
// and serves the public API: a real request through the whole command.
func TestAPICommandServesThePublicAPIInStage(t *testing.T) {
	cleanEnvironment(t)
	keys := testpeer.NewKeys(t)
	stage := testpeer.NewStageFiles(t)
	peers := testpeer.Start(t, keys, stage)
	target := peers.ServeTCP(t)
	peers.Profile.Set(func(p *testpeer.Profile) {
		p.GetFunc = func(context.Context, *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error) {
			return &userv1.GetProfileResponse{Profile: &userv1.Profile{UserId: "u1", Phone: "+79001234567", Theme: userv1.Theme_THEME_DARK, Timezone: "Europe/Moscow"}}, nil
		}
	})
	for k, v := range map[string]string{"ENV": "stage", "USER_GRPC_ADDR": target, "ASSISTANT_GRPC_ADDR": target, "AUTH_USER_ISSUER": testpeer.UserIssuer,
		"AUTH_USER_AUDIENCE": testpeer.UserAudience, "AUTH_STAGE_ISSUER": testpeer.StageIssuer, "AUTH_STAGE_SIGNING_KEY_FILE": stage.SigningKeyFile,
		"AUTH_STAGE_PUBLIC_KEYS_FILE": stage.PublicKeysFile, "CLIENT_MIN_VERSIONS": "android=0.2.5"} {
		t.Setenv(k, v)
	}
	addr, stop, _ := startBFF(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+addr.public+"/v1/profile", nil)
	req.Header.Set("X-Client-Version", "android/0.2.5+41")
	req.Header.Set("Authorization", "Bearer "+keys.Access("u1"))
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp2 := &http.Response{StatusCode: resp.StatusCode, Header: resp.Header}
	testpeer.LoadContract(t).Response(t, "GET", "/v1/profile", req, resp2, body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"theme":"dark"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	calls := peers.Calls(userv1.ProfileService_GetProfile_FullMethodName)
	if len(calls) != 1 || calls[0].Caller != "bank-bff" {
		t.Fatalf("%+v", calls)
	}
	code, metrics := get(t, addr.operational, "/metrics")
	for _, name := range []string{"finassist_http_requests_total", "grpc_client_handled_total", "finassist_identity_jwks_skipped_keys", "finassist_empty_required_field_total", "go_goroutines"} {
		if code != 200 || !strings.Contains(metrics, name) {
			t.Fatalf("metric %s missing", name)
		}
	}
	if strings.Contains(metrics, "u1") || strings.Contains(metrics, "+7900") {
		t.Fatal("user data in metric labels")
	}
	stop()
}
