package endpoint

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	"google.golang.org/protobuf/proto"
)

// Contract expectations belong to the test layer, independently of production.
const (
	expectedAssistantChart       = "chart"
	expectedAssistantRows        = "rows"
	expectedAssistantUnsupported = "unsupported"
	expectedAssistantPartial     = "partial"
	expectedChartAltText         = "chart"
	assistantBeforeText          = "before"
	assistantAfterText           = "after"
	malformedChartPointIndex     = 0
	malformedPercentRowIndex     = 2
)

func TestUnitAssistantChartWithoutBoundsPreservesPoint(t *testing.T) {
	input := chartBlock()
	input.GetChart().Points[malformedChartPointIndex].Bounds = nil
	conv := converter{loc: time.UTC}
	out := conv.block(input)
	if out.Type != expectedAssistantChart || out.AltText == nil || *out.AltText != expectedChartAltText {
		t.Fatalf("chart was lost: %+v", out)
	}
	points := out.Data.(chartData).Points
	if len(points) != 3 {
		t.Fatalf("unexpected points: %+v", points)
	}
	point := points[malformedChartPointIndex]
	if point.Coverage != expectedAssistantPartial || point.DataFrom != nil || point.DataTo != nil || point.Value == nil || *point.Value != 288000 {
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
			chart.GetChart().Points[malformedChartPointIndex].Value = proto.Float64(value)
			rows := rowsBlock()
			rows.GetRows().Rows[malformedPercentRowIndex].Value = &assistantv1.BlockRow_Percent{Percent: value}
			for _, input := range []*assistantv1.Block{chart, rows} {
				out := (converter{loc: time.UTC}).blocks([]*assistantv1.Block{textBlock(assistantBeforeText), input, textBlock(assistantAfterText)})
				if len(out) != 3 || out[1].Type != expectedAssistantUnsupported || out[1].Data != nil || out[1].AltText == nil || *out[1].AltText != *input.AltText {
					t.Fatalf("invalid numeric block lost its fallback or position: %+v", out)
				}
				if out[0].Data.(textData).Text != assistantBeforeText || out[2].Data.(textData).Text != assistantAfterText {
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
		chart.GetChart().Points[malformedChartPointIndex].Value = proto.Float64(value)
		rows := rowsBlock()
		rows.GetRows().Rows[malformedPercentRowIndex].Value = &assistantv1.BlockRow_Percent{Percent: value}
		conv := converter{loc: time.UTC}
		chartOut, rowsOut := conv.block(chart), conv.block(rows)
		if chartOut.Type != expectedAssistantChart || rowsOut.Type != expectedAssistantRows || *chartOut.Data.(chartData).Points[malformedChartPointIndex].Value != value || rowsOut.Data.(rowsData).Rows[malformedPercentRowIndex].Value != value {
			t.Fatalf("finite value %v was changed", value)
		}
		if _, err := json.Marshal([]Block{chartOut, rowsOut}); err != nil {
			t.Fatal(err)
		}
	}
}
