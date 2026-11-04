package endpoint

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	"google.golang.org/protobuf/proto"
)

func TestUnitAssistantChartWithoutBoundsPreservesPoint(t *testing.T) {
	input := chartBlock()
	input.GetChart().Points[0].Bounds = nil
	conv := converter{loc: time.UTC}
	out := conv.block(input)
	if out.Type != "chart" || out.AltText == nil || *out.AltText != "chart" {
		t.Fatalf("chart was lost: %+v", out)
	}
	points := out.Data.(chartData).Points
	if len(points) != 3 || points[0].Coverage != "partial" || points[0].DataFrom != nil || points[0].DataTo != nil || points[0].Value == nil || *points[0].Value != 288000 {
		t.Fatalf("point without bounds: %+v", points)
	}
	if _, err := json.Marshal(out); err != nil {
		t.Fatal(err)
	}
}

func TestUnitAssistantNonFiniteNumbersKeepBlockPositionAndAltText(t *testing.T) {
	for name, value := range map[string]float64{"NaN": math.NaN(), "positive infinity": math.Inf(1), "negative infinity": math.Inf(-1)} {
		t.Run(name, func(t *testing.T) {
			chart := chartBlock()
			chart.GetChart().Points[0].Value = proto.Float64(value)
			rows := rowsBlock()
			rows.GetRows().Rows[2].Value = &assistantv1.BlockRow_Percent{Percent: value}
			for _, input := range []*assistantv1.Block{chart, rows} {
				out := (converter{loc: time.UTC}).blocks([]*assistantv1.Block{textBlock("before"), input, textBlock("after")})
				if len(out) != 3 || out[1].Type != "unsupported" || out[1].Data != nil || out[1].AltText == nil || *out[1].AltText != *input.AltText {
					t.Fatalf("invalid numeric block lost its fallback or position: %+v", out)
				}
				if out[0].Data.(textData).Text != "before" || out[2].Data.(textData).Text != "after" {
					t.Fatal("adjacent blocks changed")
				}
				if _, err := json.Marshal(out); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestUnitAssistantFiniteNumbersAreUnchanged(t *testing.T) {
	for _, value := range []float64{0, math.Copysign(0, -1), -1, 0.25, math.MaxFloat64, -math.MaxFloat64} {
		chart := chartBlock()
		chart.GetChart().Points[0].Value = proto.Float64(value)
		rows := rowsBlock()
		rows.GetRows().Rows[2].Value = &assistantv1.BlockRow_Percent{Percent: value}
		conv := converter{loc: time.UTC}
		chartOut, rowsOut := conv.block(chart), conv.block(rows)
		if chartOut.Type != "chart" || rowsOut.Type != "rows" || *chartOut.Data.(chartData).Points[0].Value != value || rowsOut.Data.(rowsData).Rows[2].Value != value {
			t.Fatalf("finite value %v was changed", value)
		}
		if _, err := json.Marshal([]Block{chartOut, rowsOut}); err != nil {
			t.Fatal(err)
		}
	}
}
