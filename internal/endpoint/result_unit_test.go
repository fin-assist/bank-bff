package endpoint

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/fin-assist/bank-bff/internal/catalog"
	"github.com/fin-assist/bank-bff/internal/rest"
)

func answerStatus(t *testing.T, reason string, extras ...proto.Message) *statuspb.Status {
	t.Helper()
	wire, err := finerr.NewResultStatus(codes.Internal, reason, map[string]string{"debug_message": "SECRET metadata"})
	if err != nil {
		t.Fatal(err)
	}
	wire.Message = "SECRET status diagnostic"
	for _, extra := range extras {
		detail, err := anypb.New(extra)
		if err != nil {
			t.Fatal(err)
		}
		wire.Details = append(wire.Details, detail)
	}
	return wire
}

func TestUnitAnswerResultPrecedenceAndIndependentPermission(t *testing.T) {
	model := assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_MODEL_ERROR
	offTopic := assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_OFF_TOPIC
	duplicate := answerStatus(t, "MODEL_ERROR")
	duplicate.Details = append(duplicate.Details, proto.Clone(duplicate.Details[0]).(*anypb.Any))
	malformed := answerStatus(t, "MODEL_ERROR")
	malformed.Details[0].Value = []byte{0xff}
	okStatus := answerStatus(t, "MODEL_ERROR")
	okStatus.Code = int32(codes.OK)
	unknownExtension := answerStatus(t, "MODEL_ERROR")
	unknownExtension.Details = append(unknownExtension.Details, &anypb.Any{TypeUrl: "type.googleapis.com/future.ErrorExtension", Value: []byte("SECRET extension")})
	malformedAdvice := answerStatus(t, "MODEL_ERROR")
	malformedAdvice.Details = append(malformedAdvice.Details, &anypb.Any{TypeUrl: "type.googleapis.com/finassist.common.v1.RequestRetryInfo", Value: []byte{0xff}})
	cases := []struct {
		name   string
		wire   *statuspb.Status
		legacy assistantv1.AnswerErrorCode
		want   string
	}{
		{"legacy_model", nil, model, "MODEL_ERROR"},
		{"legacy_off_topic", nil, offTopic, "OFF_TOPIC"},
		{"legacy_unknown", nil, 99, "INTERNAL_ERROR"},
		{"canonical_wins", answerStatus(t, "OFF_TOPIC"), model, "OFF_TOPIC"},
		{"canonical_open_reason", answerStatus(t, "FUTURE_ANSWER_FAILURE"), model, "FUTURE_ANSWER_FAILURE"},
		{"canonical_rest_code_stays_result", answerStatus(t, "MAINTENANCE"), model, "MAINTENANCE"},
		{"canonical_cancelled_is_open_result", answerStatus(t, "CANCELLED"), model, "CANCELLED"},
		{"present_empty_never_resurrects_legacy", &statuspb.Status{}, offTopic, "INTERNAL_ERROR"},
		{"present_ok_invalid", okStatus, offTopic, "INTERNAL_ERROR"},
		{"missing_error_info", &statuspb.Status{Code: int32(codes.Internal)}, offTopic, "INTERNAL_ERROR"},
		{"malformed_error_info", malformed, offTopic, "INTERNAL_ERROR"},
		{"duplicate_error_info", duplicate, offTopic, "INTERNAL_ERROR"},
		{"request_permission_true_forbidden", answerStatus(t, "MODEL_ERROR", &commonv1.RequestRetryInfo{Retryable: true}), offTopic, "INTERNAL_ERROR"},
		{"request_permission_false_also_forbidden", answerStatus(t, "MODEL_ERROR", &commonv1.RequestRetryInfo{}), offTopic, "INTERNAL_ERROR"},
		{"malformed_request_permission", malformedAdvice, offTopic, "INTERNAL_ERROR"},
		{"relative_request_wait_forbidden", answerStatus(t, "MODEL_ERROR", &errdetails.RetryInfo{RetryDelay: durationpb.New(time.Second)}), offTopic, "INTERNAL_ERROR"},
		{"unknown_extension_ignored", unknownExtension, model, "MODEL_ERROR"},
		{"validation_diagnostics_not_presented", answerStatus(t, "MODEL_ERROR", &errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: "input", Reason: "INVALID_FORMAT", Description: "SECRET violation"}}}), model, "MODEL_ERROR"},
	}
	c := converter{loc: time.UTC}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry, _ := catalog.Answer(tc.want)
			for _, allowed := range []bool{false, true} {
				text := ""
				if allowed {
					text = "SECRET service UI text"
				}
				name, data, terminal, ok := c.answerEvent(&assistantv1.WatchAnswerResponse{Event: &assistantv1.WatchAnswerResponse_Error{Error: &assistantv1.AnswerError{
					Status: tc.wire, Code: tc.legacy, Message: "SECRET legacy diagnostic", Text: text, Retryable: allowed, RemainingLimit: 7,
				}}})
				event, typed := data.(AnswerError)
				if !typed || !ok || !terminal || name != "error" || event.Code != tc.want || event.UserMessage != entry.Text || event.Text != entry.Text || event.Severity != entry.Severity || event.Retryable != allowed || event.RemainingLimit != 7 {
					t.Fatalf("terminal result: %s %+v terminal=%t ok=%t", name, data, terminal, ok)
				}
				// Supported FIN-159 producers own even a false bool with no legacy text. Text cannot signal capability.
				message, ok := c.message(&assistantv1.Message{Role: assistantv1.Role_ROLE_ASSISTANT, Status: assistantv1.AnswerStatus_ANSWER_STATUS_FAILED,
					Error: tc.wire, ErrorCode: tc.legacy, ErrorText: proto.String("SECRET legacy history text"), Retryable: proto.Bool(allowed), Currency: "RUB", Blocks: []*assistantv1.Block{textBlock("stale")},
				})
				if !ok || message.ErrorCode == nil || *message.ErrorCode != tc.want || message.ErrorUserMessage == nil || *message.ErrorUserMessage != entry.Text || message.ErrorText == nil || *message.ErrorText != entry.Text || message.ErrorSeverity == nil || *message.ErrorSeverity != entry.Severity || message.Retryable == nil || *message.Retryable != allowed || message.Blocks == nil || len(*message.Blocks) != 0 {
					t.Fatalf("failed history: %+v", message)
				}
				for _, value := range []any{event, message} {
					wire, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(wire, []byte("SECRET")) || bytes.Contains(wire, []byte("debug_message")) || bytes.Contains(wire, []byte("retry_after")) {
						t.Fatalf("internal payload leaked: %s", wire)
					}
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(wire, &fields); err != nil {
						t.Fatal(err)
					}
					if _, present := fields["retryable"]; !present {
						t.Fatal("explicit outer false lost on wire")
					}
				}
			}
			history, _ := c.message(&assistantv1.Message{Role: assistantv1.Role_ROLE_ASSISTANT, Status: assistantv1.AnswerStatus_ANSWER_STATUS_FAILED, Error: tc.wire, ErrorCode: tc.legacy, Currency: "RUB"})
			if history.Retryable != nil {
				t.Fatal("missing optional history permission was invented")
			}
		})
	}
}

func TestUnitCanonicalResultMetricsAreBoundedAndIgnoreUnspecifiedAlias(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := rest.NewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	c := converter{loc: time.UTC, metrics: metrics}
	c.answerResult(answerStatus(t, "FUTURE_ANSWER_FAILURE"), assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_UNSPECIFIED)
	c.answerResult(answerStatus(t, "MODEL_ERROR"), assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_OFF_TOPIC)
	c.answerResult(&statuspb.Status{}, assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_OFF_TOPIC)
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "finassist_untranslated_code_total" {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			seen[labels["field"]+":"+labels["code"]] = metric.GetCounter().GetValue()
		}
	}
	expected := []string{"AnswerError.reason:unknown", "AnswerError.status:legacy_mismatch", "AnswerError.status:invalid"}
	if len(seen) != len(expected) {
		t.Fatalf("unexpected/unbounded metric categories: %v", seen)
	}
	for _, key := range expected {
		if seen[key] != 1 {
			t.Fatalf("metric %s: %v", key, seen)
		}
	}
}

func TestUnitRouteReplaySafetyDeclarations(t *testing.T) {
	safe := map[string]bool{
		"GET /v1/auth/phone/events": true, "GET /v1/consents/{type}": true, "GET /v1/profile": true,
		"PUT /v1/profile/assistant-consent": true, "DELETE /v1/profile/assistant-consent": true,
		"GET /v1/assistant/messages": true, "GET /v1/assistant/messages/{answer_id}/events": true, "GET /v1/assistant/limit": true,
	}
	routes := (Deps{}).Routes()
	if len(routes) != 14 {
		t.Fatalf("unexpected endpoint scope: %d", len(routes))
	}
	found := 0
	for _, route := range routes {
		key := route.Method + " " + route.Path
		if route.ReplaySafe != safe[key] {
			t.Errorf("%s replay safety=%t, want %t", key, route.ReplaySafe, safe[key])
		}
		if route.ReplaySafe {
			found++
		}
		if route.Method == http.MethodPost && route.ReplaySafe {
			t.Errorf("POST has no whole-request replay proof: %s", key)
		}
		if route.Idempotent && route.ReplaySafe {
			t.Errorf("a key alone does not establish its replay window: %s", key)
		}
	}
	if found != len(safe) {
		t.Fatalf("safe route declaration count %d, want %d", found, len(safe))
	}
}
