package app

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"strings"
	"testing"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/fin-assist/bank-bff/internal/testpeer"
)

const (
	malformedHistoryPath = "/v1/assistant/messages"
	malformedStreamPath  = "/v1/assistant/messages/answer/events"
	malformedStreamRoute = "/v1/assistant/messages/{answer_id}/events"
	malformedChartAlt    = "Chart unavailable"
	malformedRowsAlt     = "Rows unavailable"
	malformedBeforeText  = "before"
	malformedAfterText   = "after"
	malformedBlockType   = "type"
	malformedBlockData   = "data"
	malformedBlocksField = "blocks"
	malformedCurrency    = "RUB"
	malformedTimeZone    = "Europe/Moscow"
	malformedLastIndex   = 8
)

func malformedAssistantBlocks() []*assistantv1.Block {
	chart := func(value float64, bounds *commonv1.DataBounds) *assistantv1.Block {
		return &assistantv1.Block{AltText: proto.String(malformedChartAlt), Content: &assistantv1.Block_Chart{Chart: &assistantv1.ChartBlock{
			Kind: assistantv1.ChartKind_CHART_KIND_BAR, ValueKind: assistantv1.ValueKind_VALUE_KIND_AMOUNT,
			Points: []*assistantv1.ChartPoint{{Range: &commonv1.TimeRange{Start: created, End: created}, Value: proto.Float64(value), Bounds: bounds}},
		}}}
	}
	text := func(value string) *assistantv1.Block {
		return &assistantv1.Block{Content: &assistantv1.Block_Text{Text: &assistantv1.TextBlock{Text: value}}}
	}
	blocks := []*assistantv1.Block{text(malformedBeforeText), chart(42, nil)}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		blocks = append(blocks, chart(value, &commonv1.DataBounds{Coverage: commonv1.Coverage_COVERAGE_COMPLETE}),
			&assistantv1.Block{AltText: proto.String(malformedRowsAlt), Content: &assistantv1.Block_Rows{Rows: &assistantv1.RowsBlock{
				Rows: []*assistantv1.BlockRow{{Label: "Share", Value: &assistantv1.BlockRow_Percent{Percent: value}}},
			}}})
	}
	return append(blocks, text(malformedAfterText))
}

func assertMalformedAssistantBlock(t *testing.T, index int, block map[string]any) {
	t.Helper()
	switch index {
	case 0, malformedLastIndex:
		want := malformedBeforeText
		if index == malformedLastIndex {
			want = malformedAfterText
		}
		if block[malformedBlockType] != "text" || block[malformedBlockData].(map[string]any)["text"] != want {
			t.Fatalf("adjacent text changed: %v", block)
		}
	case 1:
		if block[malformedBlockType] != "chart" {
			t.Fatalf("chart without bounds was lost: %v", block)
		}
		point := block[malformedBlockData].(map[string]any)["points"].([]any)[0].(map[string]any)
		if point["coverage"] != "partial" || point["value"] != float64(42) || point["data_from"] != nil || point["data_to"] != nil {
			t.Fatalf("missing bounds changed the point: %v", point)
		}
	default:
		alt := malformedChartAlt
		if index%2 == 1 {
			alt = malformedRowsAlt
		}
		if block[malformedBlockType] != "unsupported" || block["alt_text"] != alt || block[malformedBlockData] != nil {
			t.Fatalf("invalid number was not replaced by an accessible fallback: %v", block)
		}
	}
}

func TestAPIMalformedAssistantBlocksPreserveHistorySnapshotAndEvents(t *testing.T) {
	b := startBFF(t, nil)
	blocks := malformedAssistantBlocks()
	b.peers.Assistant.Set(func(a *testpeer.Assistant) {
		a.ListFunc = func(context.Context, *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
			return &assistantv1.ListMessagesResponse{TimeZone: malformedTimeZone, Messages: []*assistantv1.Message{{
				Id: "answer", Role: assistantv1.Role_ROLE_ASSISTANT, Status: assistantv1.AnswerStatus_ANSWER_STATUS_COMPLETE,
				Currency: malformedCurrency, CreatedAt: created, Blocks: blocks,
			}}}, nil
		}
		a.WatchFunc = func(_ *assistantv1.WatchAnswerRequest, stream grpc.ServerStreamingServer[assistantv1.WatchAnswerResponse]) error {
			if err := stream.Send(&assistantv1.WatchAnswerResponse{TimeZone: malformedTimeZone, Event: &assistantv1.WatchAnswerResponse_Snapshot{Snapshot: &assistantv1.AnswerSnapshot{
				Status: assistantv1.AnswerStatus_ANSWER_STATUS_COMPLETE, Currency: malformedCurrency, Blocks: blocks,
			}}}); err != nil {
				return err
			}
			for i, block := range blocks {
				if err := stream.Send(&assistantv1.WatchAnswerResponse{TimeZone: malformedTimeZone, Event: &assistantv1.WatchAnswerResponse_Block{Block: &assistantv1.AnswerBlock{
					Index: int32(i), Block: block,
				}}}); err != nil {
					return err
				}
			}
			return stream.Send(&assistantv1.WatchAnswerResponse{Event: &assistantv1.WatchAnswerResponse_Done{Done: &assistantv1.AnswerDone{}}})
		}
	})
	header := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	history := b.do(t, request{method: http.MethodGet, path: malformedHistoryPath, route: malformedHistoryPath, header: header})
	if history.StatusCode != http.StatusOK {
		t.Fatalf("history failed: %d %s", history.StatusCode, history.body)
	}
	items := history.json["messages"].([]any)[0].(map[string]any)[malformedBlocksField].([]any)
	if len(items) != len(blocks) {
		t.Fatal("history lost block positions")
	}
	for i, block := range items {
		assertMalformedAssistantBlock(t, i, block.(map[string]any))
	}
	stream := b.do(t, request{method: http.MethodGet, path: malformedStreamPath, route: malformedStreamRoute, header: header})
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("stream failed: %d %s", stream.StatusCode, stream.body)
	}
	events, _ := b.contract.Events(t, http.MethodGet, malformedStreamRoute, bytes.NewReader(stream.body))
	if len(events) != len(blocks)+2 || events[0].Name != "snapshot" || events[len(events)-1].Name != "done" {
		t.Fatalf("stream lost its snapshot or terminal event: %+v", events)
	}
	snapshot := events[0].Data[malformedBlocksField].([]any)
	if len(snapshot) != len(blocks) {
		t.Fatal("snapshot lost block positions")
	}
	for i, block := range snapshot {
		assertMalformedAssistantBlock(t, i, block.(map[string]any))
		if events[i+1].Name != "block" || events[i+1].Data["index"] != float64(i) {
			t.Fatalf("block event position changed: %+v", events[i+1])
		}
		assertMalformedAssistantBlock(t, i, events[i+1].Data["block"].(map[string]any))
	}
	b.stop()
	if strings.Contains(b.logs.text(), "panic serving") {
		t.Fatal("malformed service data caused an HTTP panic")
	}
}
