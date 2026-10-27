package app

import (
	"context"
	"reflect"
	"strings"
	"testing"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"google.golang.org/grpc"

	"github.com/fin-assist/bank-bff/internal/testpeer"
)

// The same semantic source must survive history, the reconnect snapshot, and
// an incremental source event. All three cross the real HTTP/gRPC boundary.
func scenarioAssistantSourceRevisions(t *testing.T, start starter) {
	b := start(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	for _, tc := range []struct {
		name  string
		codes []assistantv1.ChangedSince
		want  []any
		text  string
	}{
		{"unverified", []assistantv1.ChangedSince{assistantv1.ChangedSince_CHANGED_SINCE_UNVERIFIED}, []any{"unverified"}, "Актуальность данных этого ответа не подтверждена"},
		{"timezone", []assistantv1.ChangedSince{assistantv1.ChangedSince_CHANGED_SINCE_TIME_ZONE}, []any{"time_zone"}, "Обновились настройки часового пояса"},
		{"mixed", []assistantv1.ChangedSince{assistantv1.ChangedSince_CHANGED_SINCE_UPLOADS, assistantv1.ChangedSince_CHANGED_SINCE_UNVERIFIED, assistantv1.ChangedSince_CHANGED_SINCE_TIME_ZONE}, []any{"uploads", "unverified", "time_zone"}, "с тех пор загружались выписки. Актуальность данных этого ответа не подтверждена. Обновились настройки часового пояса"},
		{"unknown", []assistantv1.ChangedSince{99}, []any{"unverified"}, "Актуальность данных этого ответа не подтверждена"},
		{"mixed-unknown", []assistantv1.ChangedSince{assistantv1.ChangedSince_CHANGED_SINCE_CATEGORIES, 99}, []any{"categories", "unverified"}, "с тех пор категории менялись. Актуальность данных этого ответа не подтверждена"},
		{"already-unverified", []assistantv1.ChangedSince{assistantv1.ChangedSince_CHANGED_SINCE_UNVERIFIED, 99}, []any{"unverified"}, "Актуальность данных этого ответа не подтверждена"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &assistantv1.AnswerSource{CalculatedAt: created, TransferMode: commonv1.TransferMode_TRANSFER_MODE_WITHOUT, ChangedSince: tc.codes}
			b.peers.Assistant.Set(func(a *testpeer.Assistant) {
				a.ListFunc = func(context.Context, *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
					return &assistantv1.ListMessagesResponse{TimeZone: "Europe/Moscow", Messages: []*assistantv1.Message{{Id: "a1", Role: assistantv1.Role_ROLE_ASSISTANT, Status: assistantv1.AnswerStatus_ANSWER_STATUS_COMPLETE, CreatedAt: created, Currency: "RUB", Source: src}}}, nil
				}
				a.WatchFunc = func(_ *assistantv1.WatchAnswerRequest, s grpc.ServerStreamingServer[assistantv1.WatchAnswerResponse]) error {
					for _, event := range []*assistantv1.WatchAnswerResponse{
						{TimeZone: "Europe/Moscow", Event: &assistantv1.WatchAnswerResponse_Snapshot{Snapshot: &assistantv1.AnswerSnapshot{Status: assistantv1.AnswerStatus_ANSWER_STATUS_GENERATING, Currency: "RUB", Source: src}}},
						{TimeZone: "Europe/Moscow", Event: &assistantv1.WatchAnswerResponse_Source{Source: src}},
						{Event: &assistantv1.WatchAnswerResponse_Done{Done: &assistantv1.AnswerDone{Charged: true, RemainingLimit: 4}}},
					} {
						if err := s.Send(event); err != nil {
							return err
						}
					}
					return nil
				}
			})
			check := func(value any) {
				t.Helper()
				s := value.(map[string]any)
				if !reflect.DeepEqual(s["changed_since"], tc.want) || s["changed_since_text"] != tc.text {
					t.Fatalf("source: %#v", s)
				}
			}
			list := b.do(t, request{method: "GET", path: "/v1/assistant/messages", route: "/v1/assistant/messages", header: auth})
			if list.StatusCode != 200 {
				t.Fatalf("history: %d %s", list.StatusCode, list.body)
			}
			check(list.json["messages"].([]any)[0].(map[string]any)["source"])
			const route = "/v1/assistant/messages/{answer_id}/events"
			got := b.raw(t, request{method: "GET", path: "/v1/assistant/messages/a1/events", route: route, header: auth})
			events, _ := b.contract.Events(t, "GET", route, strings.NewReader(string(got.body)))
			if got.StatusCode != 200 || len(events) != 3 {
				t.Fatalf("events: %d %s", got.StatusCode, got.body)
			}
			check(events[0].Data["source"])
			check(events[1].Data)
			for _, body := range []string{string(list.body), string(got.body)} {
				if strings.Contains(body, "data_revisions") || strings.Contains(body, "changed_at") {
					t.Fatalf("internal provenance leaked: %s", body)
				}
			}
		})
	}
}

func TestAPIAssistantSourceRevisions(t *testing.T) { scenarioAssistantSourceRevisions(t, startBFF) }
