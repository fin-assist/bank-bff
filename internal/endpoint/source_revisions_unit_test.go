package endpoint

import (
	"reflect"
	"testing"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"

	"github.com/fin-assist/bank-bff/internal/rest"
)

func TestUnitSourceRevisionStatuses(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	for _, tc := range []struct {
		codes []assistantv1.ChangedSince
		want  []string
		text  string
	}{
		{[]assistantv1.ChangedSince{assistantv1.ChangedSince_CHANGED_SINCE_UNVERIFIED}, []string{"unverified"}, "Актуальность данных этого ответа не подтверждена"},
		{[]assistantv1.ChangedSince{assistantv1.ChangedSince_CHANGED_SINCE_TIME_ZONE}, []string{"time_zone"}, "Обновились настройки часового пояса"},
		{[]assistantv1.ChangedSince{assistantv1.ChangedSince_CHANGED_SINCE_CATEGORIES, assistantv1.ChangedSince_CHANGED_SINCE_UNVERIFIED, assistantv1.ChangedSince_CHANGED_SINCE_TIME_ZONE}, []string{"categories", "unverified", "time_zone"}, "с тех пор категории менялись. Актуальность данных этого ответа не подтверждена. Обновились настройки часового пояса"},
	} {
		u.f.list = func(*assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
			return &assistantv1.ListMessagesResponse{TimeZone: "Europe/Moscow", Messages: []*assistantv1.Message{{Id: "a1", Role: assistantv1.Role_ROLE_ASSISTANT, Status: assistantv1.AnswerStatus_ANSWER_STATUS_COMPLETE, CreatedAt: ts, Currency: "RUB", Source: &assistantv1.AnswerSource{CalculatedAt: ts, ChangedSince: tc.codes}}}}, nil
		}
		r := u.serve(t, "GET", "/v1/assistant/messages", "", nil)
		if r.Code != 200 {
			t.Fatal(r.Code)
		}
		got := r.json["messages"].([]any)[0].(map[string]any)["source"].(map[string]any)
		want := make([]any, len(tc.want))
		for i, s := range tc.want {
			want[i] = s
		}
		if !reflect.DeepEqual(got["changed_since"], want) || got["changed_since_text"] != tc.text {
			t.Fatalf("%#v", got)
		}
	}
}
