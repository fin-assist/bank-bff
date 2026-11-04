package endpoint

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/fin-assist/bank-bff/internal/testpeer"
)

// Exercise the protobuf transport and converter independently of the public BFF.
func TestIntegrationMalformedAssistantBlocksSurviveTransportAndStaySerializable(t *testing.T) {
	missingBounds := chartBlock()
	missingBounds.GetChart().Points[malformedChartPointIndex].Bounds = nil
	blocks := []*assistantv1.Block{missingBounds}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		chart, rows := chartBlock(), rowsBlock()
		chart.GetChart().Points[malformedChartPointIndex].Value = proto.Float64(value)
		rows.GetRows().Rows[malformedPercentRowIndex].Value = &assistantv1.BlockRow_Percent{Percent: value}
		blocks = append(blocks, chart, rows)
	}
	peers := testpeer.Start(t, testpeer.NewKeys(t), testpeer.NewStageFiles(t))
	peers.Assistant.Set(func(a *testpeer.Assistant) {
		a.ListFunc = func(context.Context, *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
			return &assistantv1.ListMessagesResponse{Messages: []*assistantv1.Message{{Blocks: blocks}}}, nil
		}
	})
	conn, err := grpc.NewClient(testpeer.Target, peers.DialOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	page, err := assistantv1.NewAssistantServiceClient(conn).ListMessages(ctx, &assistantv1.ListMessagesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	wire := page.GetMessages()[0].GetBlocks()
	if wire[0].GetChart().GetPoints()[malformedChartPointIndex].GetBounds() != nil || !math.IsNaN(*wire[1].GetChart().GetPoints()[malformedChartPointIndex].Value) {
		t.Fatal("transport removed the regression inputs")
	}
	out := (converter{loc: time.UTC}).blocks(wire)
	if len(out) != len(blocks) || out[0].Type != expectedAssistantChart || out[0].Data.(chartData).Points[malformedChartPointIndex].Coverage != expectedAssistantPartial {
		t.Fatalf("missing bounds broke conversion: %+v", out)
	}
	for i := 1; i < len(out); i++ {
		if out[i].Type != expectedAssistantUnsupported || out[i].Data != nil || out[i].AltText == nil || *out[i].AltText != *blocks[i].AltText {
			t.Fatalf("block %d lost its fallback: %+v", i, out[i])
		}
	}
	if _, err := json.Marshal(out); err != nil {
		t.Fatal("converted wire blocks cannot be encoded", err)
	}
}
