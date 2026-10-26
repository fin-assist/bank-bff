package endpoint

import (
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/protobuf/proto"

	"github.com/fin-assist/bank-bff/internal/rest"
)

func TestUnitProfileAndAssistantConsent(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	profile := &userv1.Profile{UserId: "u1", Phone: "+79161234567", Theme: 42, Timezone: "Asia/Vladivostok", CreatedAt: ts,
		AssistantConsent: &userv1.AssistantConsent{Granted: true, Version: proto.String("v1"), CurrentVersion: "v2"}}
	u.f.profile = func() (*userv1.GetProfileResponse, error) { return &userv1.GetProfileResponse{Profile: profile}, nil }
	r := u.serve(t, "GET", "/v1/profile", "", nil)
	consents := r.json["consents"].([]any)
	c := consents[0].(map[string]any)
	if r.Code != 200 || r.json["theme"] != "system" || r.json["created_at"] != "2026-10-05T05:10:00+10:00" || len(consents) != 1 ||
		c["type"] != "assistant" || c["granted"] != true || c["version"] != "v1" || c["current_version"] != "v2" {
		t.Fatalf("%d %v", r.Code, r.json)
	}
	profile.AssistantConsent, profile.Theme = nil, userv1.Theme_THEME_LIGHT
	if r := u.serve(t, "GET", "/v1/profile", "", nil); len(r.json["consents"].([]any)) != 0 || r.json["theme"] != "light" {
		t.Fatalf("no consent element: %v", r.json)
	}
	if r := u.serve(t, "GET", "/v1/profile", "", map[string]string{"Authorization": ""}); r.Code != 401 {
		t.Fatal(r.Code)
	}
	u.f.profile = func() (*userv1.GetProfileResponse, error) { return nil, finerr.TokenRevoked() }
	if r := u.serve(t, "GET", "/v1/profile", "", nil); r.Code != 401 || r.code() != "TOKEN_REVOKED" {
		t.Fatalf("deleted user: %d", r.Code)
	}
	var grant *userv1.GrantAssistantConsentRequest
	u.f.grant = func(in *userv1.GrantAssistantConsentRequest) (*userv1.GrantAssistantConsentResponse, error) {
		grant = in
		return &userv1.GrantAssistantConsentResponse{Profile: &userv1.Profile{UserId: "u1", AssistantConsent: &userv1.AssistantConsent{Granted: true, CurrentVersion: "v2", Version: proto.String("v2")}}}, nil
	}
	if r := u.serve(t, "PUT", "/v1/profile/assistant-consent", `{"version":"v2"}`, nil); r.Code != 200 || grant.GetVersion() != "v2" || r.json["consents"].([]any)[0].(map[string]any)["granted"] != true {
		t.Fatalf("%d %v", r.Code, r.json)
	}
	if r := u.serve(t, "PUT", "/v1/profile/assistant-consent", `{}`, nil); r.Code != 400 {
		t.Fatalf("missing version: %d", r.Code)
	}
	u.f.grant = func(*userv1.GrantAssistantConsentRequest) (*userv1.GrantAssistantConsentResponse, error) {
		return nil, finerr.ConsentOutdated()
	}
	if r := u.serve(t, "PUT", "/v1/profile/assistant-consent", `{"version":"v1"}`, nil); r.Code != 422 || r.code() != "CONSENT_OUTDATED" {
		t.Fatal(r.Code)
	}
	revoked := false
	u.f.revoke = func() (*userv1.RevokeAssistantConsentResponse, error) {
		revoked = true
		return &userv1.RevokeAssistantConsentResponse{}, nil
	}
	if r := u.serve(t, "DELETE", "/v1/profile/assistant-consent", "", nil); r.Code != 204 || !revoked {
		t.Fatal(r.Code)
	}
	u.f.revoke = func() (*userv1.RevokeAssistantConsentResponse, error) {
		return nil, finerr.RateLimited(time.Now().Add(time.Minute))
	}
	if r := u.serve(t, "DELETE", "/v1/profile/assistant-consent", "", nil); r.Code != 429 {
		t.Fatal(r.Code)
	}
}

func TestUnitEnumsAndZones(t *testing.T) {
	if name, ok := enumName(assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_OFF_TOPIC); !ok || name != "OFF_TOPIC" {
		t.Fatal(name)
	}
	if code, ok := enumCode(commonv1.TransferMode_TRANSFER_MODE_WITHOUT); !ok || code != "without" {
		t.Fatal(code)
	}
	for _, e := range []commonv1.TransferMode{0, 77} {
		if _, ok := enumCode(e); ok {
			t.Fatalf("%v named", e)
		}
	}
	if transferMode(77) != "with" || transferMode(commonv1.TransferMode_TRANSFER_MODE_WITHOUT) != "without" {
		t.Fatal("transfer_mode default")
	}
	if code, ok := enumCode(userv1.VerificationStatus_VERIFICATION_STATUS_VERIFIED); !ok || code != "verified" {
		t.Fatal(code)
	}
	if upperSnake("AnswerErrorCode") != "ANSWER_ERROR_CODE" {
		t.Fatal(upperSnake("AnswerErrorCode"))
	}
	for name, offset := range map[string]int{"": 3 * 3600, "Nowhere/City": 3 * 3600, "Asia/Kolkata": 5*3600 + 1800, "UTC": 0} {
		if _, got := t0.In(zone(name)).Zone(); got != offset {
			t.Fatalf("%q: %d", name, got)
		}
	}
	if s := moment(ts, zone("UTC")); s != "2026-10-04T19:10:00Z" {
		t.Fatal(s)
	}
	if optionalMoment(nil, zone("")) != nil || timeRange(nil, zone("")) != nil || operationsFilter(nil, zone("")) != nil {
		t.Fatal("absent values stay absent")
	}
	if f := operationsFilter(&commonv1.OperationsFilter{}, zone("")); f.Kind != nil || f.TransferMode != nil {
		t.Fatal("unspecified filter values stay absent")
	}
}
