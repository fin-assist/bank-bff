package endpoint

import (
	"context"
	"reflect"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/rest"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

// Compose only the real neighbor transport and source mapper. Public HTTP/SSE
// endpoint behavior is separately exercised by TestAPIAssistantSourceRevisions.
func TestIntegrationSourceRevisionsTransportAndMapper(t *testing.T) {
	for _, tc := range []struct {
		codes   []assistantv1.ChangedSince
		want    []string
		text    string
		unknown int
	}{
		{[]assistantv1.ChangedSince{3}, []string{"unverified"}, "Актуальность данных этого ответа не подтверждена", 0},
		{[]assistantv1.ChangedSince{4}, []string{"time_zone"}, "Обновились настройки часового пояса", 0},
		{[]assistantv1.ChangedSince{99}, []string{"unverified"}, "Актуальность данных этого ответа не подтверждена", 1},
		{[]assistantv1.ChangedSince{1, 99}, []string{"categories", "unverified"}, "с тех пор категории менялись. Актуальность данных этого ответа не подтверждена", 1},
		{[]assistantv1.ChangedSince{3, 99, 99}, []string{"unverified"}, "Актуальность данных этого ответа не подтверждена", 2},
	} {
		reg := prometheus.NewRegistry()
		metrics, err := rest.NewMetrics(reg)
		if err != nil {
			t.Fatal(err)
		}
		peers := testpeer.Start(t, testpeer.NewKeys(t), testpeer.NewStageFiles(t))
		src := &assistantv1.AnswerSource{CalculatedAt: timestamppb.New(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)), ChangedSince: tc.codes}
		peers.Assistant.Set(func(a *testpeer.Assistant) {
			a.ListFunc = func(context.Context, *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
				return &assistantv1.ListMessagesResponse{Messages: []*assistantv1.Message{{Source: src}}}, nil
			}
			a.WatchFunc = func(_ *assistantv1.WatchAnswerRequest, stream grpc.ServerStreamingServer[assistantv1.WatchAnswerResponse]) error {
				for _, event := range []*assistantv1.WatchAnswerResponse{{Event: &assistantv1.WatchAnswerResponse_Snapshot{Snapshot: &assistantv1.AnswerSnapshot{Source: src}}}, {Event: &assistantv1.WatchAnswerResponse_Source{Source: src}}} {
					if err := stream.Send(event); err != nil {
						return err
					}
				}
				return nil
			}
		})
		conn, err := grpc.NewClient(testpeer.Target, peers.DialOptions()...)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		api := assistantv1.NewAssistantServiceClient(conn)
		page, err := api.ListMessages(ctx, &assistantv1.ListMessagesRequest{})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		convert := converter{loc: time.UTC, metrics: metrics}
		check := func(wire *assistantv1.AnswerSource) {
			t.Helper()
			got := convert.source(wire)
			if !reflect.DeepEqual(got.ChangedSince, tc.want) || got.ChangedSinceText == nil || *got.ChangedSinceText != tc.text {
				t.Fatalf("source: %+v", got)
			}
		}
		check(page.Messages[0].Source)
		stream, err := api.WatchAnswer(ctx, &assistantv1.WatchAnswerRequest{})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		first, err := stream.Recv()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		check(first.GetSnapshot().Source)
		next, err := stream.Recv()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		check(next.GetSource())
		cancel()
		families, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		var count float64
		for _, family := range families {
			if family.GetName() == "finassist_untranslated_code_total" {
				for _, metric := range family.Metric {
					for _, label := range metric.Label {
						if label.GetName() == "field" && label.GetValue() == "AnswerSource.changed_since" {
							count += metric.Counter.GetValue()
						}
					}
				}
			}
		}
		if count != float64(3*tc.unknown) {
			t.Fatalf("unknown translation metric %v want%d", count, 3*tc.unknown)
		}
	}
}
