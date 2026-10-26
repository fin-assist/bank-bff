package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/netip"
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/endpoint"
	"github.com/fin-assist/bank-bff/internal/rest"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

func importRange() *commonv1.TimeRange { return &commonv1.TimeRange{Start: created, End: created} }
func importConfigFixture() *statementdatav1.GetImportConfigResponse {
	return &statementdatav1.GetImportConfigResponse{DownloadUrl: "https://www.tbank.ru/export", LoginUrl: "https://www.tbank.ru/login/", MaxFileSizeBytes: 10 << 20, AcceptedFormats: []string{"OFX"}, SuggestedPeriod: importRange(), Loaded: &statementdatav1.LoadedSummary{FirstOperationAt: created, LastOperationAt: created, OperationCount: 3}, TimeZone: "Asia/Novosibirsk"}
}
func importResultFixture() *statementdatav1.ImportResult {
	features := []*statementdatav1.FeatureAvailability{}
	for i := int32(1); i <= 7; i++ {
		features = append(features, &statementdatav1.FeatureAvailability{Feature: statementdatav1.Feature(i), Open: i < 4, OpenedNow: i < 3, RequiredFullMonths: proto.Int32(12)})
	}
	features = append(features, &statementdatav1.FeatureAvailability{Feature: 99}, &statementdatav1.FeatureAvailability{Feature: statementdatav1.Feature_FEATURE_MONTHLY_CHART, RequiredMonthsWithData: proto.Int32(2)})
	return &statementdatav1.ImportResult{OperationCount: 3, NewCount: 2, DuplicateCount: 1, UnreadCount: 1, FirstOperationAt: created, LastOperationAt: created, IsFirstImport: true,
		Totals:   &statementdatav1.ImportTotals{Scope: statementdatav1.ImportTotalsScope_IMPORT_TOTALS_SCOPE_ALL, ExpenseMinor: -9007199254740993, IncomeMinor: 12},
		Accounts: []*statementdatav1.ImportAccount{{TypeName: "Текущий", Mask: proto.String("··1234")}, {TypeName: "Счёт другого банка", IsOtherBank: true}}, CategorizedCount: 1, OwnTransferCount: 1, UncategorizedCount: 1,
		UncategorizedFilters: &commonv1.OperationsFilter{Selection: proto.String("s_uncategorized"), SelectionName: proto.String("Без категории"), From: created, To: created},
		Coverage:             &statementdatav1.ImportCoverage{FullMonths: 5, FullMonthsBefore: 4, NewlyFullMonths: []string{"2026-09"}, IncompleteMonths: []*statementdatav1.IncompleteMonth{{Month: "2026-10", Range: importRange(), DataFrom: created, DataTo: created, Gaps: []*commonv1.TimeRange{importRange()}, InProgress: true}}, Features: features},
		Notices:              []statementdatav1.ImportNotice{1, 2, 99}}
}

func scenarioImportRead(t *testing.T, start starter) {
	b := start(t, nil)
	access := b.keys.Access("u1")
	auth := base(map[string]string{"Authorization": "Bearer " + access, "X-Read-Primary": "1"})
	b.peers.Statements.Set(func(s *testpeer.Statements) {
		s.ConfigFunc = func(ctx context.Context, _ *statementdatav1.GetImportConfigRequest) (*statementdatav1.GetImportConfigResponse, error) {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 6*time.Second {
				t.Error("unbounded unary")
			}
			return importConfigFixture(), nil
		}
		s.ListFunc = func(context.Context, *statementdatav1.ListUploadsRequest) (*statementdatav1.ListUploadsResponse, error) {
			return &statementdatav1.ListUploadsResponse{TimeZone: "Asia/Novosibirsk", Summary: &statementdatav1.UploadsSummary{UploadCount: 1, OperationCount: 3, FirstOperationAt: created, LastOperationAt: created, Gaps: []*commonv1.TimeRange{importRange()}}, Uploads: []*statementdatav1.Upload{{UploadId: "u1", FileName: "../ RAW.ofx ", Status: statementdatav1.UploadStatus_UPLOAD_STATUS_PROCESSING, UploadedAt: created, SourceName: "Выписка Т-Банка"}, {UploadId: "u2", FileName: "a.ofx", Status: 99, UploadedAt: created, SourceName: "Выписка Т-Банка", ResolvedUnreadCount: 2, ResolvedByUploadedAt: created, PeriodFrom: proto.String("2026-09-01"), PeriodTo: proto.String("2026-09-30")}}}, nil
		}
		s.UnreadFunc = func(_ context.Context, r *statementdatav1.ListUnreadLinesRequest) (*statementdatav1.ListUnreadLinesResponse, error) {
			if r.GetScope() == nil {
				t.Error("missing scope")
			}
			return &statementdatav1.ListUnreadLinesResponse{TimeZone: "Asia/Novosibirsk", Lines: []*statementdatav1.UnreadLine{{UploadId: "u1", FileName: "../ RAW.ofx ", LineNumber: 1, Date: created, Reason: 1, ReasonName: "не распознали сумму"}}}, nil
		}
		s.DeleteFunc = func(_ context.Context, r *statementdatav1.DeleteUploadRequest) (*statementdatav1.DeleteUploadResponse, error) {
			if r.GetUploadId() != "u1" {
				return nil, finerr.NotFound()
			}
			return &statementdatav1.DeleteUploadResponse{}, nil
		}
	})
	r := b.do(t, request{method: "GET", path: "/v1/statements/config", route: "/v1/statements/config", header: auth})
	if r.StatusCode != 200 || r.json["loaded"].(map[string]any)["first_operation_at"] != "2026-10-05T02:10:00+07:00" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	c := lastCall(t, b, statementdatav1.StatementsService_GetImportConfig_FullMethodName, access, "")
	if c.MD.Get("x-read-primary")[0] != "1" {
		t.Fatal("primary hint missing")
	}
	r = b.do(t, request{method: "GET", path: "/v1/statements", route: "/v1/statements", header: auth})
	if r.StatusCode != 200 || r.json["uploads"].([]any)[1].(map[string]any)["status"] != "done" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	lastCall(t, b, statementdatav1.StatementsService_ListUploads_FullMethodName, access, "")
	for _, query := range []string{"upload_id=u1", "from=2026-09-01T00:00:00Z&to=2026-10-01T00:00:00Z"} {
		r = b.do(t, request{method: "GET", path: "/v1/statements/unread-lines?" + query, route: "/v1/statements/unread-lines", header: auth})
		if r.StatusCode != 200 || len(r.json["lines"].([]any)) != 1 {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
	}
	lastCall(t, b, statementdatav1.StatementsService_ListUnreadLines_FullMethodName, access, "")
	before := len(b.peers.Calls(statementdatav1.StatementsService_ListUnreadLines_FullMethodName))
	for _, query := range []string{"", "upload_id=u1&from=2026-09-01T00:00:00Z&to=2026-10-01T00:00:00Z", "from=2026-09-01T00:00:00Z", "from=2026-10-01T00:00:00Z&to=2026-09-01T00:00:00Z", "from=bad&to=bad"} {
		r = b.do(t, request{method: "GET", path: "/v1/statements/unread-lines?" + query, route: "/v1/statements/unread-lines", header: auth})
		if r.StatusCode != 400 {
			t.Fatalf("query %s: %d %s", query, r.StatusCode, r.body)
		}
	}
	if len(b.peers.Calls(statementdatav1.StatementsService_ListUnreadLines_FullMethodName)) != before {
		t.Fatal("invalid scope reached service")
	}
	for i := 0; i < 2; i++ {
		r = b.do(t, request{method: "DELETE", path: "/v1/statements/u1", route: "/v1/statements/{upload_id}", header: auth})
		if r.StatusCode != 204 {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
	}
	lastCall(t, b, statementdatav1.StatementsService_DeleteUpload_FullMethodName, access, "")
}

func multipartBody(t *testing.T, name, body string, extra bool) (string, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, strings.ReplaceAll(name, `"`, `\"`)))
	h.Set("Content-Type", "application/octet-stream")
	p, err := w.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if extra {
		if err = w.WriteField("other", "invalid"); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String(), w.FormDataContentType()
}

func scenarioImportUpload(t *testing.T, start starter) {
	b := start(t, nil)
	access := b.keys.Access("u1")
	var committed atomic.Int32
	var received atomic.Int64
	raw := "../ ВЫПИСКА é.ofx "
	b.peers.Statements.Set(func(s *testpeer.Statements) {
		s.UploadFunc = func(stream grpc.ClientStreamingServer[statementdatav1.UploadStatementRequest, statementdatav1.UploadStatementResponse]) error {
			deadline, ok := stream.Context().Deadline()
			if !ok || time.Until(deadline) > 61*time.Second || time.Until(deadline) < 50*time.Second {
				t.Error("upload deadline")
			}
			first, err := stream.Recv()
			if err != nil {
				return err
			}
			h := first.GetHeader()
			if h == nil || h.GetFileName() != raw || h.GetSizeBytes() != 0 {
				t.Errorf("raw header %+v", h)
			}
			var size int64
			previousChunk := 0
			for {
				chunk, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return err
				}
				if len(chunk.GetChunk()) > 256<<10 || chunk.GetHeader() != nil {
					t.Error("chunk contract")
				}
				if previousChunk != 0 && previousChunk != 256<<10 {
					t.Error("nonfinal chunk not coalesced")
				}
				previousChunk = len(chunk.GetChunk())
				size += int64(len(chunk.GetChunk()))
				received.Add(int64(len(chunk.GetChunk())))
			}
			committed.Add(1)
			return stream.SendAndClose(&statementdatav1.UploadStatementResponse{UploadId: "up1", FileName: h.GetFileName()})
		}
	})
	body, ct := multipartBody(t, raw, strings.Repeat("x", 300<<10), false)
	req := request{method: "POST", path: "/v1/statements", route: "/v1/statements", body: body, header: base(map[string]string{"Authorization": "Bearer " + access, "Idempotency-Key": idemKey, "Content-Type": ct})}
	for i := 0; i < 2; i++ {
		r := b.do(t, req)
		if r.StatusCode != 202 || r.json["file_name"] != raw {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
	}
	if received.Load() != 600<<10 {
		t.Fatal(received.Load())
	}
	lastCall(t, b, statementdatav1.StatementsService_UploadStatement_FullMethodName, access, idemKey)
	before := committed.Load()
	for _, tc := range []struct {
		name, body, ct string
		want           int
	}{
		{"extra", func() string { x, _ := multipartBody(t, raw, "x", true); return x }(), "", 400},
		{"oversize", strings.Repeat("x", int(endpoint.MaxImportFileBytes)+1), "file", 413},
		{"malformed", "broken", "multipart/form-data; boundary=x", 400},
		{"wrong_type", "{}", "application/json", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			test := req
			if tc.name == "extra" {
				test.body, test.header["Content-Type"] = multipartBody(t, raw, "x", true)
			} else if tc.ct == "file" {
				test.body, test.header["Content-Type"] = multipartBody(t, raw, tc.body, false)
			} else {
				test.body = tc.body
				test.header["Content-Type"] = tc.ct
			}
			r := b.do(t, test)
			if r.StatusCode != tc.want {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
			if tc.want == 413 && errorCode(r) != "FILE_TOO_LARGE" {
				t.Fatal(string(r.body))
			}
		})
	}
	if committed.Load() != before {
		t.Fatal("invalid multipart committed")
	}
	for _, key := range []string{"", "bad"} {
		body, ct := multipartBody(t, raw, "x", false)
		r := b.do(t, request{method: "POST", path: req.path, route: req.route, body: body, header: base(map[string]string{"Authorization": "Bearer " + access, "Idempotency-Key": key, "Content-Type": ct})})
		if r.StatusCode != 400 {
			t.Fatalf("key: %d %s", r.StatusCode, r.body)
		}
	}
	if strings.Contains(b.logs.text(), raw) {
		t.Fatal("filename leaked to logs")
	}
}

func scenarioImportWatch(t *testing.T, start starter) {
	b := start(t, nil)
	access := b.keys.Access("u1")
	auth := base(map[string]string{"Authorization": "Bearer " + access})
	route := "/v1/statements/{upload_id}/progress"
	b.peers.Statements.Set(func(s *testpeer.Statements) {
		s.WatchFunc = func(r *statementdatav1.WatchUploadRequest, stream grpc.ServerStreamingServer[statementdatav1.WatchUploadResponse]) error {
			deadline, ok := stream.Context().Deadline()
			if !ok || time.Until(deadline) > 241*time.Second || time.Until(deadline) < 230*time.Second {
				t.Error("unbounded import watch")
			}
			send := func(e *statementdatav1.WatchUploadResponse) error { return stream.Send(e) }
			switch r.GetUploadId() {
			case "missing":
				return finerr.NotFound()
			case "transport":
				_ = send(&statementdatav1.WatchUploadResponse{Event: &statementdatav1.WatchUploadResponse_Progress{Progress: &statementdatav1.ImportProgress{Stage: 1, Percent: 10}}})
				return status.Error(codes.Unavailable, "SECRET transport")
			case "complete":
				_ = send(&statementdatav1.WatchUploadResponse{})
				_ = send(&statementdatav1.WatchUploadResponse{Event: &statementdatav1.WatchUploadResponse_Progress{Progress: &statementdatav1.ImportProgress{Stage: 99, Percent: 15}}})
				return send(&statementdatav1.WatchUploadResponse{Event: &statementdatav1.WatchUploadResponse_Complete{Complete: importResultFixture()}, TimeZone: "Asia/Novosibirsk"})
			default:
				var canonical *statuspb.Status
				legacy := statementdatav1.ImportErrorCode_IMPORT_ERROR_CODE_CANCELLED
				switch r.GetUploadId() {
				case "invalid":
					canonical = &statuspb.Status{}
				case "unknown":
					canonical, _ = finerr.NewResultStatus(codes.Internal, "FUTURE_IMPORT_FAILURE", nil)
				case "canonical":
					canonical, _ = finerr.NewResultStatus(codes.FailedPrecondition, "PROCESSING_FAILED", nil)
				}
				return send(&statementdatav1.WatchUploadResponse{Event: &statementdatav1.WatchUploadResponse_Error{Error: &statementdatav1.ImportStreamError{Status: canonical, Code: legacy, Message: "SECRET service"}}})
			}
		}
	})
	for _, tc := range []struct {
		id, code string
		events   int
	}{{"cancelled", "CANCELLED", 1}, {"invalid", "PROCESSING_FAILED", 1}, {"unknown", "FUTURE_IMPORT_FAILURE", 1}, {"canonical", "PROCESSING_FAILED", 1}, {"transport", "", 1}, {"complete", "", 2}} {
		r := b.do(t, request{method: "GET", path: "/v1/statements/" + tc.id + "/progress", route: route, header: auth})
		events, _ := b.contract.Events(t, "GET", route, strings.NewReader(string(r.body)))
		if r.StatusCode != 200 || len(events) != tc.events {
			t.Fatalf("%s: %d %s", tc.id, r.StatusCode, r.body)
		}
		if tc.code != "" {
			e := events[0]
			if e.Name != "error" || e.Data["code"] != tc.code || e.Data["user_message"] == "" || e.Data["retryable"] != nil {
				t.Fatalf("%+v", e)
			}
		}
		if tc.id == "transport" && events[0].Name != "progress" {
			t.Fatal("fabricated terminal")
		}
		if tc.id == "complete" {
			e := events[1]
			if e.Name != "complete" || e.Data["currency"] != "RUB" || len(e.Data["notices"].([]any)) != 2 || len(e.Data["coverage"].(map[string]any)["features"].([]any)) != 8 || e.Data["first_operation_at"] != "2026-10-05T02:10:00+07:00" {
				t.Fatalf("%+v", e)
			}
		}
		if strings.Contains(string(r.body), "SECRET") {
			t.Fatal("diagnostic leaked")
		}
	}
	lastCall(t, b, statementdatav1.StatementsService_WatchUpload_FullMethodName, access, "")
	r := b.do(t, request{method: "GET", path: "/v1/statements/missing/progress", route: route, header: auth})
	if r.StatusCode != 404 || errorCode(r) != "NOT_FOUND" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func scenarioImportErrors(t *testing.T, start starter) {
	b := start(t, func(c *config.Config, _ *testpeer.Peers) { c.API.Streams.Max = 1; c.API.Streams.PerIP = 1 })
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	body, ct := multipartBody(t, "a.ofx", "x", false)
	ops := []request{{method: "GET", path: "/v1/statements/config", route: "/v1/statements/config"}, {method: "GET", path: "/v1/statements", route: "/v1/statements"}, {method: "GET", path: "/v1/statements/unread-lines?upload_id=u1", route: "/v1/statements/unread-lines"}, {method: "DELETE", path: "/v1/statements/u1", route: "/v1/statements/{upload_id}"}, {method: "GET", path: "/v1/statements/u1/progress", route: "/v1/statements/{upload_id}/progress"}, {method: "POST", path: "/v1/statements", route: "/v1/statements", body: body}}
	for _, pe := range registry(finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "file", Reason: "INVALID_FORMAT", Description: "SECRET service validation"}), finerr.InternalError(), finerr.InvalidToken(), finerr.TokenRevoked(), finerr.NotFound(), finerr.FileTooLarge(), finerr.WrongFormat(), finerr.CsvNotAccepted(), finerr.WrongBank(), finerr.IdempotencyConflict(), finerr.RequestInProgress(time.Second), finerr.RateLimited(retryAt())) {
		advice := finerr.WithRequestRetryInfo(pe.err, true)
		b.peers.Statements.Set(func(s *testpeer.Statements) {
			s.ConfigFunc = func(context.Context, *statementdatav1.GetImportConfigRequest) (*statementdatav1.GetImportConfigResponse, error) {
				return nil, advice
			}
			s.ListFunc = func(context.Context, *statementdatav1.ListUploadsRequest) (*statementdatav1.ListUploadsResponse, error) {
				return nil, advice
			}
			s.UnreadFunc = func(context.Context, *statementdatav1.ListUnreadLinesRequest) (*statementdatav1.ListUnreadLinesResponse, error) {
				return nil, advice
			}
			s.DeleteFunc = func(context.Context, *statementdatav1.DeleteUploadRequest) (*statementdatav1.DeleteUploadResponse, error) {
				return nil, advice
			}
			s.WatchFunc = func(*statementdatav1.WatchUploadRequest, grpc.ServerStreamingServer[statementdatav1.WatchUploadResponse]) error {
				return advice
			}
			s.UploadFunc = func(stream grpc.ClientStreamingServer[statementdatav1.UploadStatementRequest, statementdatav1.UploadStatementResponse]) error {
				for {
					_, err := stream.Recv()
					if err != nil {
						break
					}
				}
				return advice
			}
		})
		for _, op := range ops {
			op.header = base(map[string]string{"Authorization": auth["Authorization"], "Idempotency-Key": idemKey, "Content-Type": ct})
			r := b.do(t, op)
			// The route's declared statuses determine translation; the schema validator
			// above also rejects any status not declared by this exact operation.
			if strings.Contains(string(r.body), "SECRET") {
				t.Fatal("service diagnostic leaked")
			}
			want := pe.status
			if !importDeclared(op.method, op.route, want) {
				want = 500
			}
			if r.StatusCode != want {
				t.Fatalf("%s %s %s: %d %s", op.method, op.path, pe.code, r.StatusCode, r.body)
			}
			if want == 429 && r.json["error"].(map[string]any)["retryable"] != (op.method != "POST") {
				t.Fatalf("retry projection: %s", r.body)
			}
		}
	}
	for _, op := range ops {
		op.header = base(map[string]string{"Content-Type": ct, "Idempotency-Key": idemKey})
		before := len(b.peers.Calls(""))
		r := b.do(t, op)
		if r.StatusCode != 401 || len(b.peers.Calls("")) != before {
			t.Fatalf("unauthorized route: %d %s", r.StatusCode, r.body)
		}
	}
}
func importDeclared(method, route string, status int) bool {
	if status == 400 || status == 401 || status == 429 || status == 500 || status == 503 {
		return true
	}
	if method == "POST" {
		return status == 409 || status == 413 || status == 422
	}
	return status == 404 && (method == "DELETE" || strings.Contains(route, "progress") || strings.Contains(route, "unread-lines"))
}

// Exact file boundary, chunked transport and raw transfer bytes are exercised at
// both levels. The service fixture can commit only after the client's final EOF.
func scenarioImportMultipartEdges(t *testing.T, start starter) {
	var attempts atomic.Int32
	// Server-side dispatch may lag a preceding cancelled upload. Observe the actual
	// client invocation synchronously, before grpc can queue/cancel that request.
	observer := grpc.WithChainStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		if method == statementdatav1.StatementsService_UploadStatement_FullMethodName {
			attempts.Add(1)
		}
		return streamer(ctx, desc, cc, method, opts...)
	})
	b := start(t, nil, WithDialOptions(observer))
	access := b.keys.Access("u1")
	var commits atomic.Int32
	var gotName string
	var gotBody bytes.Buffer
	b.peers.Statements.Set(func(s *testpeer.Statements) {
		s.UploadFunc = func(stream grpc.ClientStreamingServer[statementdatav1.UploadStatementRequest, statementdatav1.UploadStatementResponse]) error {
			first, err := stream.Recv()
			if err != nil {
				return err
			}
			name := first.GetHeader().GetFileName()
			var content bytes.Buffer
			for {
				p, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return err
				}
				if len(p.GetChunk()) > 256<<10 {
					t.Error("oversizedchunk")
				}
				_, _ = content.Write(p.GetChunk())
			}
			gotName = name
			gotBody = content
			commits.Add(1)
			return stream.SendAndClose(&statementdatav1.UploadStatementResponse{UploadId: "up1", FileName: name})
		}
	})
	send := func(body, ct string, chunked bool) response {
		req, err := http.NewRequestWithContext(t.Context(), "POST", "http://"+b.public+"/v1/statements", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if chunked {
			req.ContentLength = -1
			req.TransferEncoding = []string{"chunked"}
		}
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("X-Client-Version", clientVersion)
		req.Header.Set("Idempotency-Key", idemKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return response{Response: resp, body: data, json: b.contract.Response(t, "POST", "/v1/statements", req, resp, data)}
	}
	body, ct := multipartBody(t, "full.ofx", strings.Repeat("x", int(endpoint.MaxImportFileBytes)), false)
	if r := send(body, ct, true); r.StatusCode != 202 {
		t.Fatalf("exactfilelimitwithenvelope %d %s", r.StatusCode, r.body)
	}
	// MIME NextPart would decode this; the original uploaded bytes must be sent.
	raw := "A=3DB\r\n"
	body, ct = multipartBody(t, strings.Repeat("д", 160)+".ofx", raw, false)
	body = strings.Replace(body, "Content-Type: application/octet-stream\r\n", "Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: quoted-printable\r\n", 1)
	if r := send(body, ct, true); r.StatusCode != 202 || gotBody.String() != raw || len(gotName) <= 255 {
		t.Fatalf("raw transfer/name changed: %d %s", r.StatusCode, r.body)
	}
	// FIN-297 defines an empty filename as an empty second hash component. The
	// BFF forwards opaque content and the exact present value; hashing stays remote.
	emptyContent := "opaque\x00content\r\n"
	emptyBody, emptyCT := multipartBody(t, "", emptyContent, false)
	beforeEmpty := commits.Load()
	if r := send(emptyBody, emptyCT, true); r.StatusCode != 202 || r.json["file_name"] != "" || gotName != "" || gotBody.String() != emptyContent || commits.Load() != beforeEmpty+1 {
		t.Fatalf("present empty filename was changed or failed to reach EOF: %d %s", r.StatusCode, r.body)
	}
	before := commits.Load()
	for index, bad := range []string{
		strings.TrimSuffix(body, body[len(body)-8:]),
		strings.Replace(body, `name="file"`, `name="other"`, 1),
		strings.Replace(body, fmt.Sprintf(`; filename="%s"`, strings.Repeat("д", 160)+".ofx"), "", 1),
		strings.Replace(body, `filename="`, "filename=\"\x00", 1),
		strings.Replace(body, `filename="`, "filename=\"\xff", 1),
		strings.Replace(body, `filename="`, `filename="`+strings.Repeat("x", 65<<10), 1),
		strings.Replace(body, "application/octet-stream", strings.Repeat("x", 65<<10), 1),
		strings.Replace(body, "Content-Type: application/octet-stream", "X-Envelope: "+strings.Repeat("x", 65<<10)+"\r\nContent-Type: application/octet-stream", 1),
	} {
		beforeCalls := attempts.Load()
		if r := send(bad, ct, true); r.StatusCode != 400 {
			t.Fatalf("invalid envelope: %d %s", r.StatusCode, r.body)
		}
		if index == 0 && attempts.Load() != beforeCalls+1 {
			t.Fatal("truncated form did not exercise cancellation after RPC initiation")
		}
		if index != 0 && attempts.Load() != beforeCalls {
			t.Fatalf("invalid local form %d initiated RPC: before=%d after=%d", index, beforeCalls, attempts.Load())
		}
	}
	if commits.Load() != before {
		t.Fatal("invalid envelope reached final EOF")
	}
	// A peer can refuse while the client is still sending, which may yield EOF
	// from Send. Its FILE_TOO_LARGE status must still become 413.
	b.peers.Statements.Set(func(s *testpeer.Statements) {
		s.UploadFunc = func(stream grpc.ClientStreamingServer[statementdatav1.UploadStatementRequest, statementdatav1.UploadStatementResponse]) error {
			_, _ = stream.Recv()
			return finerr.FileTooLarge()
		}
	})
	body, ct = multipartBody(t, "x", strings.Repeat("x", 2<<20), false)
	if r := send(body, ct, false); r.StatusCode != 413 || errorCode(r) != "FILE_TOO_LARGE" {
		t.Fatalf("early refusal: %d %s", r.StatusCode, r.body)
	}
}

func scenarioImportLifetime(t *testing.T, start starter) {
	for _, stop := range []bool{false, true} {
		b := start(t, nil, WithServerTimeouts(150*time.Millisecond, 200*time.Millisecond))
		cancelled := make(chan struct{})
		b.peers.Statements.Set(func(s *testpeer.Statements) {
			s.WatchFunc = func(_ *statementdatav1.WatchUploadRequest, stream grpc.ServerStreamingServer[statementdatav1.WatchUploadResponse]) error {
				if err := stream.Send(&statementdatav1.WatchUploadResponse{Event: &statementdatav1.WatchUploadResponse_Progress{Progress: &statementdatav1.ImportProgress{Stage: 1, Percent: 10}}}); err != nil {
					return err
				}
				<-stream.Context().Done()
				close(cancelled)
				return stream.Context().Err()
			}
		})
		ctx, cancel := context.WithCancel(t.Context())
		req, err := http.NewRequestWithContext(ctx, "GET", "http://"+b.public+"/v1/statements/up1/progress", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+b.keys.Access("u1"))
		req.Header.Set("X-Client-Version", clientVersion)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(resp.Body)
		line, err := reader.ReadString('\n')
		if err != nil || line != "event: progress\n" {
			t.Fatal(line, err)
		}
		// Beyond the ordinary listener write deadline: a still-active stream is valid.
		time.Sleep(250 * time.Millisecond)
		select {
		case <-cancelled:
			t.Fatal("unary timeout cancelled import watch")
		default:
		}
		if stop {
			b.stop()
		} else {
			cancel()
		}
		_ = resp.Body.Close()
		await(t, cancelled, "importstreamcancellation")
		cancel()
	}
}

func scenarioImportUnknownReason(t *testing.T, start starter) {
	b := start(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	route := "/v1/statements/unread-lines"
	known := &statementdatav1.UnreadLine{UploadId: "u1", FileName: "raw.ofx", LineNumber: 1, Reason: statementdatav1.UnreadReason_UNREAD_REASON_BAD_AMOUNT, ReasonName: "не распознали сумму"}
	for _, unknown := range []statementdatav1.UnreadReason{0, 999} {
		wire := &statementdatav1.ListUnreadLinesResponse{Lines: []*statementdatav1.UnreadLine{known, {UploadId: "u1", FileName: "raw.ofx", LineNumber: 2, Reason: unknown, ReasonName: "подпись новой причины"}}}
		b.peers.Statements.Set(func(s *testpeer.Statements) {
			s.UnreadFunc = func(context.Context, *statementdatav1.ListUnreadLinesRequest) (*statementdatav1.ListUnreadLinesResponse, error) {
				return wire, nil
			}
		})
		r := b.do(t, request{method: "GET", path: route + "?upload_id=u1", route: route, header: auth})
		if r.StatusCode != 200 || len(r.json["lines"].([]any)) != 2 || r.json["lines"].([]any)[1].(map[string]any)["reason"] != "unknown" || r.json["lines"].([]any)[1].(map[string]any)["reason_name"] != "подпись новой причины" {
			t.Fatalf("unknown reason record was lost: %d %s", r.StatusCode, r.body)
		}
	}
	b.peers.Statements.Set(func(s *testpeer.Statements) {
		s.UnreadFunc = func(context.Context, *statementdatav1.ListUnreadLinesRequest) (*statementdatav1.ListUnreadLinesResponse, error) {
			return &statementdatav1.ListUnreadLinesResponse{Lines: []*statementdatav1.UnreadLine{known}}, nil
		}
	})
	r := b.do(t, request{method: "GET", path: route + "?upload_id=u1", route: route, header: auth})
	if r.StatusCode != 200 || len(r.json["lines"].([]any)) != 1 || r.json["lines"].([]any)[0].(map[string]any)["reason_name"] != known.GetReasonName() {
		t.Fatalf("known row changed: %d %s", r.StatusCode, r.body)
	}
}

func scenarioImportEarlyLength(t *testing.T, start starter) {
	b := start(t, nil)
	access := b.keys.Access("u1")
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", b.public)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "POST /v1/statements HTTP/1.1\r\nHost: %s\r\nX-Client-Version: %s\r\nAuthorization: Bearer %s\r\nIdempotency-Key: %s\r\nContent-Type: multipart/form-data; boundary=x\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", b.public, clientVersion, access, idemKey, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), "POST", "http://"+b.public+"/v1/statements", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("%v uploadCalls=%d logs=%s", err, len(b.peers.Calls(statementdatav1.StatementsService_UploadStatement_FullMethodName)), b.logs.text())
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	body := b.contract.Response(t, "POST", "/v1/statements", req, resp, data)
	if resp.StatusCode != 413 || body["error"].(map[string]any)["code"] != "FILE_TOO_LARGE" || len(b.peers.Calls(statementdatav1.StatementsService_UploadStatement_FullMethodName)) != 0 {
		t.Fatalf("declaredoversize: %d %s", resp.StatusCode, data)
	}
}

func scenarioImportCapacity(t *testing.T, start starter) {
	b := start(t, func(c *config.Config, _ *testpeer.Peers) {
		c.API.Uploads = rest.StreamLimits{Max: 2, PerIP: 1, RetryAfter: time.Second}
		c.API.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	})
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	b.peers.Statements.Set(func(s *testpeer.Statements) {
		s.UploadFunc = func(stream grpc.ClientStreamingServer[statementdatav1.UploadStatementRequest, statementdatav1.UploadStatementResponse]) error {
			first, err := stream.Recv()
			if err != nil {
				return err
			}
			for {
				_, err = stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return err
				}
			}
			entered <- struct{}{}
			select {
			case <-release:
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
			return stream.SendAndClose(&statementdatav1.UploadStatementResponse{UploadId: "u1", FileName: first.GetHeader().GetFileName()})
		}
		s.WatchFunc = func(_ *statementdatav1.WatchUploadRequest, stream grpc.ServerStreamingServer[statementdatav1.WatchUploadResponse]) error {
			return stream.Send(&statementdatav1.WatchUploadResponse{Event: &statementdatav1.WatchUploadResponse_Error{Error: &statementdatav1.ImportStreamError{Code: 2}}})
		}
	})
	access := b.keys.Access("u1")
	body, ct := multipartBody(t, "x.ofx", "x", false)
	req := func(ip string) request {
		return request{method: "POST", path: "/v1/statements", route: "/v1/statements", body: body, header: base(map[string]string{"Authorization": "Bearer " + access, "Idempotency-Key": idemKey, "Content-Type": ct, "X-Forwarded-For": ip})}
	}
	type outcome struct {
		response response
		err      error
	}
	first, second := make(chan outcome, 1), make(chan outcome, 1)
	go func() { r, e := b.fetch(t.Context(), req("192.0.2.1")); first <- outcome{r, e} }()
	await(t, entered, "firstupload")
	checkBusy := func(ip string) {
		before := len(b.peers.Calls(statementdatav1.StatementsService_UploadStatement_FullMethodName))
		r := b.do(t, req(ip))
		if r.StatusCode != 429 || errorCode(r) != "RATE_LIMITED" || r.json["error"].(map[string]any)["retryable"] != false || r.Header.Get("Retry-After") != "1" || len(b.peers.Calls(statementdatav1.StatementsService_UploadStatement_FullMethodName)) != before {
			t.Fatalf("uploadbudget: %d %s", r.StatusCode, r.body)
		}
	}
	checkBusy("192.0.2.1")
	go func() { r, e := b.fetch(t.Context(), req("192.0.2.2")); second <- outcome{r, e} }()
	await(t, entered, "secondupload")
	checkBusy("192.0.2.3")
	if got := b.streamingRejected(t, "/v1/statements"); got != 2 {
		t.Fatalf("upload rejection metric=%v", got)
	}
	// Upload saturation cannot consume the independently configured SSE budget.
	r := b.do(t, request{method: "GET", path: "/v1/statements/u1/progress", route: "/v1/statements/{upload_id}/progress", header: base(map[string]string{"Authorization": "Bearer " + access})})
	if r.StatusCode != 200 {
		t.Fatal("upload slots blocked SSE", r.StatusCode)
	}
	close(release)
	for _, done := range []<-chan outcome{first, second} {
		out := await(t, done, "completedupload")
		if out.err != nil || out.response.StatusCode != 202 {
			t.Fatal(out.response.StatusCode, out.err)
		}
	}
	if r := b.do(t, req("192.0.2.1")); r.StatusCode != 202 {
		t.Fatal("upload slot leaked", r.StatusCode, string(r.body))
	}
}

func scenarioImportStreamingBeforeEOF(t *testing.T, start starter) {
	b := start(t, nil)
	headerSeen := make(chan struct{})
	finishBody := make(chan struct{})
	b.peers.Statements.Set(func(s *testpeer.Statements) {
		s.UploadFunc = func(stream grpc.ClientStreamingServer[statementdatav1.UploadStatementRequest, statementdatav1.UploadStatementResponse]) error {
			first, err := stream.Recv()
			if err != nil {
				return err
			}
			if first.GetHeader() == nil {
				return finerr.ValidationError()
			}
			close(headerSeen)
			for {
				_, err = stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return err
				}
			}
			return stream.SendAndClose(&statementdatav1.UploadStatementResponse{UploadId: "u1", FileName: first.GetHeader().GetFileName()})
		}
	})
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	form := multipart.NewWriter(writer)
	ct := form.FormDataContentType()
	written := make(chan error, 1)
	go func() {
		part, err := form.CreateFormFile("file", "stream.ofx")
		if err == nil {
			select {
			case <-finishBody:
			case <-t.Context().Done():
				written <- t.Context().Err()
				return
			}
			_, err = part.Write([]byte("raw OFX bytes"))
		}
		if err == nil {
			err = form.Close()
		}
		_ = writer.CloseWithError(err)
		written <- err
	}()
	req, err := http.NewRequestWithContext(t.Context(), "POST", "http://"+b.public+"/v1/statements", reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Client-Version", clientVersion)
	req.Header.Set("Authorization", "Bearer "+b.keys.Access("u1"))
	req.Header.Set("Idempotency-Key", idemKey)
	type answer struct {
		response response
		err      error
	}
	done := make(chan answer, 1)
	go func() {
		r, e := http.DefaultClient.Do(req)
		if e != nil {
			done <- answer{err: e}
			return
		}
		defer func() { _ = r.Body.Close() }()
		data, e := io.ReadAll(r.Body)
		done <- answer{response: response{Response: r, body: data}, err: e}
	}()
	await(t, headerSeen, "gRPC header before HTTP body EOF")
	close(finishBody)
	if err := await(t, written, "finished multipart"); err != nil {
		t.Fatal(err)
	}
	out := await(t, done, "streaming upload response")
	if out.err != nil {
		t.Fatal(out.err)
	}
	_ = b.contract.Response(t, "POST", "/v1/statements", req, out.response.Response, out.response.body)
	if out.response.StatusCode != 202 {
		t.Fatalf("%d %s", out.response.StatusCode, out.response.body)
	}
}
