package endpoint

import (
	"context"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/catalog"
	"github.com/fin-assist/bank-bff/internal/rest"
)

var (
	t0 = time.Date(2026, 10, 4, 19, 10, 0, 0, time.UTC)
	ts = timestamppb.New(t0)
)

func chartBlock() *assistantv1.Block {
	return &assistantv1.Block{AltText: proto.String("chart"), Content: &assistantv1.Block_Chart{Chart: &assistantv1.ChartBlock{
		Kind: assistantv1.ChartKind_CHART_KIND_BAR, Title: "Самокат по месяцам", ValueKind: assistantv1.ValueKind_VALUE_KIND_AMOUNT, HighlightIndex: proto.Int32(1),
		Points: []*assistantv1.ChartPoint{
			{Range: &commonv1.TimeRange{Start: ts, End: ts}, Value: proto.Float64(288000), Bounds: &commonv1.DataBounds{Coverage: commonv1.Coverage_COVERAGE_COMPLETE}},
			{Range: &commonv1.TimeRange{Start: ts, End: ts}, Bounds: &commonv1.DataBounds{Coverage: commonv1.Coverage_COVERAGE_PARTIAL, DataFrom: ts, DataTo: ts}},
			{Range: &commonv1.TimeRange{Start: ts, End: ts}, Value: proto.Float64(1), Bounds: &commonv1.DataBounds{Coverage: 42}},
		}}}}
}

func rowsBlock() *assistantv1.Block {
	return &assistantv1.Block{AltText: proto.String("rows"), Content: &assistantv1.Block_Rows{Rows: &assistantv1.RowsBlock{Rows: []*assistantv1.BlockRow{
		{Label: "Операций", Value: &assistantv1.BlockRow_Count{Count: 9}},
		{Label: "Средний чек", Sublabel: proto.String("в месяц"), Value: &assistantv1.BlockRow_AmountMinor{AmountMinor: 80000}},
		{Label: "Доля", Value: &assistantv1.BlockRow_Percent{Percent: 0.25}},
		{Label: "Итог", Value: &assistantv1.BlockRow_Text{Text: "мало"}},
		{Label: "Пусто"},
	}}}}
}

func textBlock(s string) *assistantv1.Block {
	return &assistantv1.Block{Content: &assistantv1.Block_Text{Text: &assistantv1.TextBlock{Text: s}}}
}

func source() *assistantv1.AnswerSource {
	return &assistantv1.AnswerSource{CalculatedAt: ts, DataRange: &commonv1.TimeRange{Start: ts, End: ts}, TransferMode: commonv1.TransferMode_TRANSFER_MODE_WITHOUT,
		ChangedSince: []assistantv1.ChangedSince{assistantv1.ChangedSince_CHANGED_SINCE_CATEGORIES, assistantv1.ChangedSince_CHANGED_SINCE_UPLOADS}}
}

func chips() []*assistantv1.Chip {
	return []*assistantv1.Chip{
		{Label: "Самокат", Screen: assistantv1.ChipScreen_CHIP_SCREEN_OPERATIONS, Filters: &commonv1.OperationsFilter{From: ts, To: ts, Q: proto.String("q"), CategoryId: proto.String("cat"),
			Kind: commonv1.KindFilter_KIND_FILTER_EXPENSE, AmountFromMinor: proto.Int64(1), AmountToMinor: proto.Int64(2), TransferMode: commonv1.TransferMode_TRANSFER_MODE_WITH,
			Selection: proto.String("s_merchant_7f3a"), SelectionName: proto.String("Самокат")}},
		{Label: "Сентябрь", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_MONTH, PeriodKey: "2026-09"}},
		{Label: "unknown screen", Screen: 9},
		{Label: "operations without filters", Screen: assistantv1.ChipScreen_CHIP_SCREEN_OPERATIONS},
		{Label: "analytics without period", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{PeriodKey: "2026"}},
	}
}

func TestUnitHistoryMapsMessages(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	var req *assistantv1.ListMessagesRequest
	u.f.list = func(in *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
		req = in
		return &assistantv1.ListMessagesResponse{TimeZone: "Asia/Yekaterinburg", HasOlder: true, Messages: []*assistantv1.Message{
			{Id: "q1", Role: assistantv1.Role_ROLE_USER, CreatedAt: ts, Text: proto.String("Сколько?"), Currency: "RUB"},
			{Id: "a1", Role: assistantv1.Role_ROLE_ASSISTANT, CreatedAt: ts, Status: assistantv1.AnswerStatus_ANSWER_STATUS_COMPLETE, Charged: proto.Bool(true),
				Blocks: []*assistantv1.Block{textBlock("ответ"), chartBlock(), rowsBlock(), {AltText: proto.String("new")}}, Chips: chips(), Source: source(), Currency: "RUB"},
			{Id: "a2", Role: assistantv1.Role_ROLE_ASSISTANT, CreatedAt: ts, Status: assistantv1.AnswerStatus_ANSWER_STATUS_FAILED, ErrorCode: assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_MODEL_ERROR,
				ErrorText: proto.String("service text must not leak"), Retryable: proto.Bool(true), Blocks: []*assistantv1.Block{textBlock("stale")}},
			{Id: "a3", Role: assistantv1.Role_ROLE_ASSISTANT, CreatedAt: ts, Status: assistantv1.AnswerStatus_ANSWER_STATUS_FAILED, ErrorCode: 77, Currency: "RUB"},
			{Id: "a4", Role: assistantv1.Role_ROLE_ASSISTANT, CreatedAt: ts, Status: assistantv1.AnswerStatus_ANSWER_STATUS_GENERATING, Blocks: []*assistantv1.Block{textBlock("пока")}, Currency: "RUB"},
			{Id: "a5", Role: assistantv1.Role_ROLE_ASSISTANT, CreatedAt: ts, Status: 9, Currency: "RUB"},
			{Id: "x", Role: 9, CreatedAt: ts, Currency: "RUB"},
		}}, nil
	}
	r := u.serve(t, "GET", "/v1/assistant/messages?limit=5&before_id=a9", "", nil)
	if r.Code != 200 || req.GetLimit() != 5 || req.GetBeforeId() != "a9" || r.json["has_older"] != true {
		t.Fatalf("%d %v %v", r.Code, r.json, req)
	}
	msgs := r.json["messages"].([]any)
	if len(msgs) != 6 {
		t.Fatalf("unknown role is not shown: %d", len(msgs))
	}
	q, a, failed, unknownCode, generating, unknownStatus := msgs[0].(map[string]any), msgs[1].(map[string]any), msgs[2].(map[string]any), msgs[3].(map[string]any), msgs[4].(map[string]any), msgs[5].(map[string]any)
	if q["text"] != "Сколько?" || q["status"] != nil || q["blocks"] != nil || q["created_at"] != "2026-10-05T00:10:00+05:00" {
		t.Fatalf("question: %v", q)
	}
	blocks := a["blocks"].([]any)
	chart := blocks[1].(map[string]any)
	points := chart["data"].(map[string]any)["points"].([]any)
	if len(blocks) != 4 || blocks[0].(map[string]any)["type"] != "text" || chart["title"] != "Самокат по месяцам" || chart["data"].(map[string]any)["highlight_index"] != 1.0 ||
		points[0].(map[string]any)["value"] != 288000.0 || points[1].(map[string]any)["value"] != nil || points[1].(map[string]any)["data_to"] == nil ||
		points[2].(map[string]any)["coverage"] != "partial" || blocks[3].(map[string]any)["type"] != unsupportedBlock {
		t.Fatalf("blocks: %v", blocks)
	}
	rows := blocks[2].(map[string]any)["data"].(map[string]any)["rows"].([]any)
	if rows[0].(map[string]any)["value_kind"] != "count" || rows[1].(map[string]any)["value"] != 80000.0 || rows[1].(map[string]any)["sublabel"] != "в месяц" ||
		rows[2].(map[string]any)["value_kind"] != "percent" || rows[3].(map[string]any)["text"] != "мало" || rows[3].(map[string]any)["value"] != nil || rows[4].(map[string]any)["value_kind"] != nil {
		t.Fatalf("rows: %v", rows)
	}
	chipsOut := a["chips"].([]any)
	filters := chipsOut[0].(map[string]any)["filters"].(map[string]any)
	params := chipsOut[1].(map[string]any)["params"].(map[string]any)
	if len(chipsOut) != 2 || filters["kind"] != "expense" || filters["transfer_mode"] != "with" || filters["amount_from"] != 1.0 || filters["selection_name"] != "Самокат" ||
		params["period"] != "month" || params["date"] != "2026-09" || params["transfer_mode"] != "with" {
		t.Fatalf("chips: %v", chipsOut)
	}
	src := a["source"].(map[string]any)
	if src["transfer_mode"] != "without" || src["changed_since_text"] != "с тех пор категории менялись и загружались выписки" || len(src["changed_since"].([]any)) != 2 || a["charged"] != true {
		t.Fatalf("source: %v", src)
	}
	model, _ := catalog.Answer("MODEL_ERROR")
	if failed["error_code"] != "MODEL_ERROR" || failed["error_user_message"] != model.Text || failed["error_text"] != model.Text || failed["error_severity"] != model.Severity ||
		failed["retryable"] != true || len(failed["blocks"].([]any)) != 0 || failed["currency"] != "RUB" || failed["chips"] != nil {
		t.Fatalf("failed: %v", failed)
	}
	if unknownCode["error_code"] != "INTERNAL_ERROR" || unknownCode["retryable"] != nil {
		t.Fatalf("unknown legacy code: %v", unknownCode)
	}
	if generating["status"] != "generating" || len(generating["blocks"].([]any)) != 1 || generating["error_code"] != nil {
		t.Fatalf("generating: %v", generating)
	}
	if unknownStatus["status"] != "failed" || unknownStatus["error_code"] != nil || unknownStatus["error_user_message"] == nil {
		t.Fatalf("unknown status: %v", unknownStatus)
	}
	if n, _ := testutil.GatherAndCount(u.reg, "finassist_empty_required_field_total"); n != 5 {
		t.Fatalf("zero series: %d", n)
	}
	// An empty currency is replaced by RUB and counted (grpc-api.md 5.3).
	if !gathered(t, u, "finassist_empty_required_field_total", "Message.currency", 1) {
		t.Fatal("empty Message.currency not counted")
	}
	r = u.serve(t, "GET", "/v1/assistant/messages", "", nil)
	if req.GetLimit() != defaultHistoryLimit || req.BeforeId != nil {
		t.Fatalf("defaults: %v", req)
	}
	u.f.list = func(*assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
		return nil, finerr.InvalidToken()
	}
	if r = u.serve(t, "GET", "/v1/assistant/messages", "", nil); r.Code != 401 {
		t.Fatal(r.Code)
	}
}

func gathered(t *testing.T, u *unitAPI, name, field string, want float64) bool {
	t.Helper()
	families, err := u.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetValue() == field && m.GetCounter().GetValue() == want {
					return true
				}
			}
		}
	}
	return false
}

func TestUnitAskRetryLimit(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	var ask *assistantv1.AskQuestionRequest
	u.f.ask = func(in *assistantv1.AskQuestionRequest) (*assistantv1.AskQuestionResponse, error) {
		ask = in
		return &assistantv1.AskQuestionResponse{AnswerId: "a1", RemainingLimit: 4, Question: &assistantv1.Message{Id: "q1", Role: assistantv1.Role_ROLE_USER, Text: proto.String(in.GetText()), CreatedAt: ts}}, nil
	}
	r := u.serve(t, "POST", "/v1/assistant/messages", `{"text":"Сколько?","transfer_mode":"without"}`, nil)
	q := r.json["question"].(map[string]any)
	if r.Code != 201 || ask.GetTransferMode() != commonv1.TransferMode_TRANSFER_MODE_WITHOUT || r.json["answer_id"] != "a1" || r.json["remaining_limit"] != 4.0 || q["currency"] != "RUB" || q["created_at"] != "2026-10-04T22:10:00+03:00" {
		t.Fatalf("%d %v", r.Code, r.json)
	}
	if r := u.serve(t, "POST", "/v1/assistant/messages", `{"text":"valid","Text":"","TEXT":"x","transfer_Mode":"without"}`, nil); r.Code != 201 || ask.GetText() != "valid" ||
		ask.GetTransferMode() != commonv1.TransferMode_TRANSFER_MODE_UNSPECIFIED {
		t.Fatalf("members are decoded by exact name: %d %q %v", r.Code, ask.GetText(), ask.GetTransferMode())
	}
	u.serve(t, "POST", "/v1/assistant/messages", `{"text":"Сколько?"}`, nil)
	if ask.GetTransferMode() != commonv1.TransferMode_TRANSFER_MODE_UNSPECIFIED {
		t.Fatal("absent transfer_mode is the default")
	}
	ask = nil
	if r := u.serve(t, "POST", "/v1/assistant/messages", `{"text":"Сколько?","transfer_mode":"sideways"}`, nil); r.Code != 400 || r.code() != "VALIDATION_ERROR" || ask != nil {
		t.Fatalf("unknown transfer_mode: %d", r.Code)
	}
	u.f.ask = func(*assistantv1.AskQuestionRequest) (*assistantv1.AskQuestionResponse, error) {
		return &assistantv1.AskQuestionResponse{Question: &assistantv1.Message{Role: 5}}, nil
	}
	if r := u.serve(t, "POST", "/v1/assistant/messages", `{"text":"x"}`, nil); r.Code != 500 {
		t.Fatalf("question of an unknown role: %d", r.Code)
	}
	resets := time.Date(2026, 10, 5, 0, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	for _, tc := range []struct {
		err    error
		status int
		field  string
	}{
		{finerr.ConsentRequired(), 403, ""}, {finerr.LimitExceeded(resets), 403, "resets_at"},
		{finerr.RateLimited(time.Now().Add(time.Hour)), 429, "retry_at"}, {finerr.RequestInProgress(time.Second), 409, ""}, {finerr.IdempotencyConflict(), 409, ""},
	} {
		u.f.ask = func(*assistantv1.AskQuestionRequest) (*assistantv1.AskQuestionResponse, error) { return nil, tc.err }
		r := u.serve(t, "POST", "/v1/assistant/messages", `{"text":"x"}`, nil)
		e := r.json["error"].(map[string]any)
		if r.Code != tc.status || tc.field != "" && e[tc.field] == nil {
			t.Fatalf("%v: %d %v", tc.err, r.Code, e)
		}
	}
	var retry *assistantv1.RetryAnswerRequest
	u.f.retry = func(in *assistantv1.RetryAnswerRequest) (*assistantv1.RetryAnswerResponse, error) {
		retry = in
		return &assistantv1.RetryAnswerResponse{AnswerId: "a2", RemainingLimit: 3}, nil
	}
	if r := u.serve(t, "POST", "/v1/assistant/messages/a1/retry", "", nil); r.Code != 201 || retry.GetAnswerId() != "a1" || r.json["answer_id"] != "a2" {
		t.Fatalf("%d %v", r.Code, r.json)
	}
	for _, err := range []error{finerr.NotFound(), finerr.RetryNotAllowed(), finerr.LimitExceeded(resets)} {
		u.f.retry = func(*assistantv1.RetryAnswerRequest) (*assistantv1.RetryAnswerResponse, error) { return nil, err }
		want, _ := finerr.ToREST(err)
		if r := u.serve(t, "POST", "/v1/assistant/messages/a1/retry", "", nil); r.Code != want.Spec.HTTPStatus || r.code() != want.Code {
			t.Fatalf("%v: %d", err, r.Code)
		}
	}
	u.f.limit = func() (*assistantv1.GetLimitResponse, error) {
		return &assistantv1.GetLimitResponse{Remaining: 4, DailyMax: 5, ResetsAt: timestamppb.New(resets), TimeZone: "Europe/Moscow"}, nil
	}
	if r := u.serve(t, "GET", "/v1/assistant/limit", "", nil); r.Code != 200 || r.json["resets_at"] != "2026-10-05T00:00:00+03:00" || r.json["daily_max"] != 5.0 {
		t.Fatalf("%d %v", r.Code, r.json)
	}
	u.f.limit = func() (*assistantv1.GetLimitResponse, error) { return nil, status.Error(codes.Unavailable, "x") }
	if r := u.serve(t, "GET", "/v1/assistant/limit", "", nil); r.Code != 500 {
		t.Fatal(r.Code)
	}
	if r := u.serve(t, "GET", "/v1/assistant/limit", "", map[string]string{"Authorization": ""}); r.Code != 401 {
		t.Fatal(r.Code)
	}
}

func answerStream(msgs []*assistantv1.WatchAnswerResponse, err error, block bool) func(context.Context, *assistantv1.WatchAnswerRequest) (grpc.ServerStreamingClient[assistantv1.WatchAnswerResponse], error) {
	return func(ctx context.Context, _ *assistantv1.WatchAnswerRequest) (grpc.ServerStreamingClient[assistantv1.WatchAnswerResponse], error) {
		return &fakeStream[assistantv1.WatchAnswerResponse]{ctx: ctx, msgs: msgs, err: err, block: block}, nil
	}
}

func TestUnitWatchAnswerEvents(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	snapshot := &assistantv1.WatchAnswerResponse{TimeZone: "Europe/Moscow", Event: &assistantv1.WatchAnswerResponse_Snapshot{Snapshot: &assistantv1.AnswerSnapshot{
		Status: assistantv1.AnswerStatus_ANSWER_STATUS_GENERATING, Blocks: []*assistantv1.Block{textBlock("За")}, Currency: "RUB"}}}
	msgs := []*assistantv1.WatchAnswerResponse{
		snapshot,
		{Event: &assistantv1.WatchAnswerResponse_Chunk{Chunk: &assistantv1.AnswerChunk{Index: 0, Text: " апрель"}}},
		{Event: &assistantv1.WatchAnswerResponse_Block{Block: &assistantv1.AnswerBlock{Index: 1, Block: chartBlock()}}},
		{Event: &assistantv1.WatchAnswerResponse_Source{Source: source()}},
		{Event: &assistantv1.WatchAnswerResponse_Source{}},
		{Event: &assistantv1.WatchAnswerResponse_Chip{Chip: chips()[0]}},
		{Event: &assistantv1.WatchAnswerResponse_Chip{Chip: chips()[2]}},
		{},
		{Event: &assistantv1.WatchAnswerResponse_Done{Done: &assistantv1.AnswerDone{Charged: true, RemainingLimit: 4}}},
		{Event: &assistantv1.WatchAnswerResponse_Chunk{Chunk: &assistantv1.AnswerChunk{Text: "after terminal"}}},
	}
	var asked string
	u.f.watchAnswer = func(ctx context.Context, in *assistantv1.WatchAnswerRequest) (grpc.ServerStreamingClient[assistantv1.WatchAnswerResponse], error) {
		asked = in.GetAnswerId()
		return answerStream(msgs, nil, false)(ctx, in)
	}
	r := u.serve(t, "GET", "/v1/assistant/messages/a1/events", "", nil)
	events, _ := r.events(t)
	names := ""
	for _, e := range events {
		names += e.name + " "
	}
	if r.Code != 200 || asked != "a1" || names != "snapshot chunk block source chip done " || events[0].data["source"] != nil || events[0].data["status"] != "generating" ||
		events[5].data["remaining_limit"] != 4.0 {
		t.Fatalf("%d %q %v", r.Code, names, events)
	}
	for _, tc := range []struct {
		code      assistantv1.AnswerErrorCode
		retryable bool
		want      string
	}{{assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_MODEL_ERROR, true, "MODEL_ERROR"}, {assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_OFF_TOPIC, false, "OFF_TOPIC"},
		{assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_CANNOT_ANSWER, false, "CANNOT_ANSWER"}, {0, true, "INTERNAL_ERROR"}} {
		failed := &assistantv1.WatchAnswerResponse{Event: &assistantv1.WatchAnswerResponse_Snapshot{Snapshot: &assistantv1.AnswerSnapshot{Status: assistantv1.AnswerStatus_ANSWER_STATUS_FAILED}}}
		terminal := &assistantv1.WatchAnswerResponse{Event: &assistantv1.WatchAnswerResponse_Error{Error: &assistantv1.AnswerError{Code: tc.code, Message: "raw diagnostic", Text: "service text", Retryable: tc.retryable, RemainingLimit: 5}}}
		u.f.watchAnswer = answerStream([]*assistantv1.WatchAnswerResponse{failed, terminal}, nil, false)
		events, _ := u.serve(t, "GET", "/v1/assistant/messages/a1/events", "", nil).events(t)
		entry, _ := catalog.Answer(tc.want)
		e := events[1].data
		if len(events) != 2 || events[0].data["status"] != "failed" || events[0].data["currency"] != "RUB" || e["code"] != tc.want || e["user_message"] != entry.Text || e["text"] != entry.Text ||
			e["severity"] != entry.Severity || e["retryable"] != tc.retryable || e["message"] == "raw diagnostic" || e["remaining_limit"] != 5.0 {
			t.Fatalf("%v: %v", tc.code, events)
		}
	}
	// Unknown snapshot status is failed; a peer failure after the snapshot just closes.
	unknown := &assistantv1.WatchAnswerResponse{Event: &assistantv1.WatchAnswerResponse_Snapshot{Snapshot: &assistantv1.AnswerSnapshot{Status: 7, Currency: "RUB"}}}
	u.f.watchAnswer = answerStream([]*assistantv1.WatchAnswerResponse{unknown}, status.Error(codes.Unavailable, "pod"), false)
	if events, _ := u.serve(t, "GET", "/v1/assistant/messages/a1/events", "", nil).events(t); len(events) != 1 || events[0].data["status"] != "failed" {
		t.Fatalf("%v", events)
	}
	u.f.watchAnswer = answerStream([]*assistantv1.WatchAnswerResponse{snapshot}, nil, true)
	if events, pings := u.serve(t, "GET", "/v1/assistant/messages/a1/events", "", nil).events(t); len(events) != 1 || pings == 0 {
		t.Fatalf("heartbeat until the stream deadline: %v %d", events, pings)
	}
	u.f.watchAnswer = answerStream(nil, finerr.NotFound(), false)
	if r := u.serve(t, "GET", "/v1/assistant/messages/other/events", "", nil); r.Code != 404 || r.code() != "NOT_FOUND" {
		t.Fatalf("another user's answer: %d", r.Code)
	}
	u.f.watchAnswer = func(context.Context, *assistantv1.WatchAnswerRequest) (grpc.ServerStreamingClient[assistantv1.WatchAnswerResponse], error) {
		return nil, status.Error(codes.Unavailable, "x")
	}
	if r := u.serve(t, "GET", "/v1/assistant/messages/a1/events", "", nil); r.Code != 500 {
		t.Fatal(r.Code)
	}
	if r := u.serve(t, "GET", "/v1/assistant/messages/a1/events", "", map[string]string{"Authorization": ""}); r.Code != 401 {
		t.Fatal(r.Code)
	}
}
