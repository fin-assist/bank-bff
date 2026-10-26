package endpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/rest"
)

type statementFakes struct {
	StatementsClient
	config *statementdatav1.GetImportConfigResponse
	list   *statementdatav1.ListUploadsResponse
	unread *statementdatav1.ListUnreadLinesResponse
	watch  []*statementdatav1.WatchUploadResponse
	err    error
	input  *statementdatav1.ListUnreadLinesRequest
	upload *uploadFake
}

func (f *statementFakes) GetImportConfig(context.Context, *statementdatav1.GetImportConfigRequest, ...grpc.CallOption) (*statementdatav1.GetImportConfigResponse, error) {
	return f.config, f.err
}
func (f *statementFakes) ListUploads(context.Context, *statementdatav1.ListUploadsRequest, ...grpc.CallOption) (*statementdatav1.ListUploadsResponse, error) {
	return f.list, f.err
}
func (f *statementFakes) ListUnreadLines(_ context.Context, r *statementdatav1.ListUnreadLinesRequest, _ ...grpc.CallOption) (*statementdatav1.ListUnreadLinesResponse, error) {
	f.input = r
	return f.unread, f.err
}
func (f *statementFakes) DeleteUpload(context.Context, *statementdatav1.DeleteUploadRequest, ...grpc.CallOption) (*statementdatav1.DeleteUploadResponse, error) {
	return &statementdatav1.DeleteUploadResponse{}, f.err
}
func (f *statementFakes) WatchUpload(ctx context.Context, _ *statementdatav1.WatchUploadRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[statementdatav1.WatchUploadResponse], error) {
	return &fakeStream[statementdatav1.WatchUploadResponse]{ctx: ctx, msgs: f.watch}, f.err
}
func (f *statementFakes) UploadStatement(context.Context, ...grpc.CallOption) (grpc.ClientStreamingClient[statementdatav1.UploadStatementRequest, statementdatav1.UploadStatementResponse], error) {
	return f.upload, f.err
}

type uploadFake struct {
	grpc.ClientStream
	sent            []*statementdatav1.UploadStatementRequest
	err, receiveErr error
	closed          bool
}

func (f *uploadFake) Send(r *statementdatav1.UploadStatementRequest) error {
	f.sent = append(f.sent, proto.Clone(r).(*statementdatav1.UploadStatementRequest))
	return f.err
}
func (f *uploadFake) CloseAndRecv() (*statementdatav1.UploadStatementResponse, error) {
	f.closed = true
	return &statementdatav1.UploadStatementResponse{UploadId: "u1", FileName: f.sent[0].GetHeader().GetFileName()}, f.receiveErr
}
func (f *uploadFake) RecvMsg(any) error { return f.receiveErr }

func unitMultipart(t *testing.T, name, body string, extra bool) (string, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte(body))
	if extra {
		_ = w.WriteField("extra", "x")
	}
	_ = w.Close()
	return b.String(), w.FormDataContentType()
}
func TestUnitImportRoutesAndStreamingValidation(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	f := u.statements
	at := timestamppb.New(time.Now())
	r := &commonv1.TimeRange{Start: at, End: at}
	f.config = &statementdatav1.GetImportConfigResponse{DownloadUrl: "https://bank.test", LoginUrl: "https://bank.test", MaxFileSizeBytes: MaxImportFileBytes, SuggestedPeriod: r, AcceptedFormats: []string{"OFX"}, Loaded: &statementdatav1.LoadedSummary{FirstOperationAt: at, LastOperationAt: at, OperationCount: 1}}
	f.list = &statementdatav1.ListUploadsResponse{Summary: &statementdatav1.UploadsSummary{FirstOperationAt: at, LastOperationAt: at, Gaps: []*commonv1.TimeRange{r}}, Uploads: []*statementdatav1.Upload{{UploadId: "u1", UploadedAt: at, Status: 99, PeriodFrom: proto.String("2026-01-01"), PeriodTo: proto.String("2026-01-02"), ResolvedUnreadCount: 2, ResolvedByUploadedAt: at}, {UploadedAt: at, Status: 1, PeriodFrom: proto.String("2026-01-01")}}}
	f.unread = &statementdatav1.ListUnreadLinesResponse{Lines: []*statementdatav1.UnreadLine{{Reason: 1, LineNumber: 1}}}
	for _, path := range []string{"/v1/statements/config", "/v1/statements", "/v1/statements/unread-lines?upload_id=u1", "/v1/statements/unread-lines?from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:00Z"} {
		if got := u.serve(t, "GET", path, "", nil); got.Code != 200 {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	if f.input.GetPeriod() == nil {
		t.Fatal("period lost")
	}
	for _, q := range []string{"", "upload_id=u1&from=2026-01-01T00:00:00Z", "from=2026-02-01T00:00:00Z&to=2026-01-01T00:00:00Z"} {
		if got := u.serve(t, "GET", "/v1/statements/unread-lines?"+q, "", nil); got.Code != 400 {
			t.Fatal(got.Code)
		}
	}
	if got := u.serve(t, "DELETE", "/v1/statements/u1", "", nil); got.Code != 204 {
		t.Fatal(got.Code)
	}
	f.err = finerr.NotFound()
	for _, path := range []string{"/v1/statements/config", "/v1/statements", "/v1/statements/unread-lines?upload_id=u1", "/v1/statements/u1/progress"} {
		if got := u.serve(t, "GET", path, "", nil); got.Code < 400 {
			t.Fatal(got.Code)
		}
	}
	if got := u.serve(t, "DELETE", "/v1/statements/u1", "", nil); got.Code != 404 {
		t.Fatal(got.Code)
	}
	f.err = nil
	for _, tc := range []struct {
		name, body string
		extra      bool
		want       int
	}{{"../ X.ofx ", strings.Repeat("x", 300<<10), false, 202}, {"a", "x", true, 400}, {"a", strings.Repeat("x", int(MaxImportFileBytes)+1), false, 413}} {
		f.upload = &uploadFake{}
		body, ct := unitMultipart(t, tc.name, tc.body, tc.extra)
		got := u.serve(t, "POST", "/v1/statements", body, map[string]string{"Content-Type": ct})
		if got.Code != tc.want {
			t.Fatalf("%d %s", got.Code, got.Body.String())
		}
		if tc.want == 202 {
			if !f.upload.closed || f.upload.sent[0].GetHeader().GetFileName() != tc.name {
				t.Fatal("raw filename lost")
			}
			for index, p := range f.upload.sent[1:] {
				if index < len(f.upload.sent)-2 && len(p.GetChunk()) != importChunkBytes {
					t.Fatal("nonfinal chunk not coalesced")
				}
				if len(p.GetChunk()) > importChunkBytes {
					t.Fatal("oversized chunk")
				}
			}
		} else if f.upload.closed {
			t.Fatal("invalid multipart finalized")
		}
	}
	for _, tc := range []struct{ body, ct string }{{"x", "application/json"}, {"x", "multipart/form-data"}, {"x", "multipart/form-data; boundary=x"}} {
		if got := u.serve(t, "POST", "/v1/statements", tc.body, map[string]string{"Content-Type": tc.ct}); got.Code != 400 {
			t.Fatal(got.Code)
		}
	}
	body, ct := unitMultipart(t, "a", "x", false)
	for _, failure := range []error{finerr.WrongBank(), io.EOF} {
		f.upload = &uploadFake{err: failure, receiveErr: finerr.FileTooLarge()}
		got := u.serve(t, "POST", "/v1/statements", body, map[string]string{"Content-Type": ct})
		want := 422
		if errors.Is(failure, io.EOF) {
			want = 413
		}
		if got.Code != want {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	f.upload = &uploadFake{receiveErr: finerr.WrongFormat()}
	if got := u.serve(t, "POST", "/v1/statements", body, map[string]string{"Content-Type": ct}); got.Code != 422 {
		t.Fatal(got.Code)
	}
	f.err = finerr.FileTooLarge()
	if got := u.serve(t, "POST", "/v1/statements", body, map[string]string{"Content-Type": ct}); got.Code != 413 {
		t.Fatal(got.Code)
	}
}

func TestUnitImportProjectionAndCanonicalPrecedence(t *testing.T) {
	c := converter{loc: time.UTC}
	at := timestamppb.Now()
	r := &commonv1.TimeRange{Start: at, End: at}
	features := []*statementdatav1.FeatureAvailability{}
	for i := int32(1); i <= 7; i++ {
		features = append(features, &statementdatav1.FeatureAvailability{Feature: statementdatav1.Feature(i), Open: i < 3, RequiredFullMonths: proto.Int32(12)})
	}
	features = append(features, &statementdatav1.FeatureAvailability{Feature: 2, RequiredMonthsWithData: proto.Int32(2)}, &statementdatav1.FeatureAvailability{Feature: 99})
	m := &statementdatav1.ImportResult{Accounts: []*statementdatav1.ImportAccount{{TypeName: "Текущий", Mask: proto.String("··12")}}, Totals: &statementdatav1.ImportTotals{Scope: 1, ExpenseMinor: -11}, Coverage: &statementdatav1.ImportCoverage{Features: features, IncompleteMonths: []*statementdatav1.IncompleteMonth{{Month: "2026-01", Range: r, DataFrom: at, DataTo: at, Gaps: []*commonv1.TimeRange{r}}}}, Notices: []statementdatav1.ImportNotice{1, 2, 99}}
	out := c.importResult(m)
	if out.Currency != "RUB" || out.Totals.Expense != -11 || len(out.Coverage.Features) != 8 || len(out.Notices) != 2 {
		t.Fatalf("%+v", out)
	}
	m.Totals.Scope = 99
	if c.importResult(m).Totals.Scope != "unknown" {
		t.Fatal("invented known scope")
	}
	for _, tc := range []struct {
		wire   *statuspb.Status
		legacy statementdatav1.ImportErrorCode
		want   string
	}{{nil, 2, "CANCELLED"}, {nil, 99, "PROCESSING_FAILED"}, {&statuspb.Status{}, 2, "PROCESSING_FAILED"}, {answerStatus(t, "FUTURE_IMPORT"), 2, "FUTURE_IMPORT"}, {answerStatus(t, "PROCESSING_FAILED"), 2, "PROCESSING_FAILED"}} {
		e := c.importError(&statementdatav1.ImportStreamError{Status: tc.wire, Code: tc.legacy, Message: "SECRET"})
		if e.Code != tc.want || strings.Contains(e.Message, "SECRET") {
			t.Fatal(e)
		}
	}
	for _, event := range []*statementdatav1.WatchUploadResponse{{}, {Event: &statementdatav1.WatchUploadResponse_Progress{Progress: &statementdatav1.ImportProgress{Stage: 99, Percent: 1}}}, {Event: &statementdatav1.WatchUploadResponse_Complete{Complete: m}}, {Event: &statementdatav1.WatchUploadResponse_Error{Error: &statementdatav1.ImportStreamError{Code: 2}}}} {
		_, _, _, _ = c.importEvent(event)
	}
	u := newUnitAPI(t, rest.StreamLimits{})
	u.statements.watch = []*statementdatav1.WatchUploadResponse{{Event: &statementdatav1.WatchUploadResponse_Complete{Complete: m}}}
	if got := u.serve(t, http.MethodGet, "/v1/statements/u1/progress", "", nil); got.Code != 200 {
		t.Fatal(got.Code)
	}
}

func TestUnitUnknownUnreadReasonPreservesRecord(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	known := &statementdatav1.UnreadLine{UploadId: "u1", FileName: "x.ofx", LineNumber: 1, Reason: statementdatav1.UnreadReason_UNREAD_REASON_BAD_AMOUNT, ReasonName: "не распознали сумму"}
	for _, unknown := range []statementdatav1.UnreadReason{0, 999} {
		wire := &statementdatav1.ListUnreadLinesResponse{Lines: []*statementdatav1.UnreadLine{known, {UploadId: "u1", LineNumber: 2, Reason: unknown, ReasonName: "сохранённая подпись новой причины"}}}
		before := proto.Clone(wire)
		out := (converter{loc: time.UTC}).unread(wire)
		if len(out.Lines) != 2 || out.Lines[1].Reason != "unknown" || out.Lines[1].ReasonName != wire.Lines[1].ReasonName || !proto.Equal(wire, before) {
			t.Fatal("unknown reason record changed", out)
		}
		u.statements.unread = wire
		r := u.serve(t, "GET", "/v1/statements/unread-lines?upload_id=u1", "", nil)
		if r.Code != 200 || len(r.json["lines"].([]any)) != 2 || r.json["lines"].([]any)[1].(map[string]any)["reason"] != "unknown" {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
	}
	families, err := u.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, family := range families {
		if family.GetName() != "finassist_untranslated_code_total" {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, l := range metric.Label {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["field"] != "UnreadLine.reason" || labels["code"] != "unknown" || metric.GetCounter().GetValue() != 2 {
				t.Fatal("unexpected metric", metric)
			}
			seen++
		}
	}
	if seen != 1 {
		t.Fatal("missing bounded projection metric", seen)
	}
	u.statements.unread = &statementdatav1.ListUnreadLinesResponse{Lines: []*statementdatav1.UnreadLine{known}}
	r := u.serve(t, "GET", "/v1/statements/unread-lines?upload_id=u1", "", nil)
	if r.Code != 200 || r.json["lines"].([]any)[0].(map[string]any)["reason_name"] != known.GetReasonName() {
		t.Fatal("known row changed", r.Body.String())
	}
}

func TestUnitImportPeriodValidationDetailFields(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	for _, tc := range []struct{ query, field string }{{"from=bad&to=2026-02-01T00:00:00Z", "from"}, {"from=2026-01-01T00:00:00Z&to=bad", "to"}, {"from=2026-02-01T00:00:00Z&to=2026-01-01T00:00:00Z", "to"}} {
		r := u.serve(t, "GET", "/v1/statements/unread-lines?"+tc.query, "", nil)
		if r.Code != 400 {
			t.Fatal(r.Code)
		}
		details := r.json["error"].(map[string]any)["details"].([]any)
		if details[0].(map[string]any)["field"] != tc.field {
			t.Fatal(tc, details)
		}
	}
}

type importReadCounter struct {
	io.ReadCloser
	bytes int
}

func (r *importReadCounter) Read(p []byte) (int, error) {
	n, e := r.ReadCloser.Read(p)
	r.bytes += n
	return n, e
}
func TestUnitImportInitialHeaderReadBudget(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	u.statements.upload = &uploadFake{}
	body, ct := unitMultipart(t, "x.ofx", "x", false)
	body = strings.Replace(body, "Content-Disposition:", "X-Large: "+strings.Repeat("x", 1<<20)+"\r\nContent-Disposition:", 1)
	req := httptest.NewRequest("POST", "/v1/statements", strings.NewReader(body))
	counter := &importReadCounter{ReadCloser: req.Body}
	req.Body = counter
	req.Header.Set("X-Client-Version", "android/0.2.5+41")
	req.Header.Set("Authorization", goodToken)
	req.Header.Set("Idempotency-Key", testKey)
	req.Header.Set("Content-Type", ct)
	res := httptest.NewRecorder()
	u.h.ServeHTTP(res, req)
	if res.Code != 400 || counter.bytes > maxImportEnvelopeBytes+1 || len(u.statements.upload.sent) != 0 {
		t.Fatalf("unbounded initial MIME header: status=%d bytes=%d sends=%d", res.Code, counter.bytes, len(u.statements.upload.sent))
	}
}

// The cumulative envelope cap must apply before parsing every potential header,
// including when the first header has already spent most of that allowance.
func TestUnitImportSecondHeaderUsesRemainingEnvelopeBudget(t *testing.T) {
	for _, firstHeaderBytes := range []int{0, 60 << 10} {
		t.Run(fmt.Sprintf("first_header_%d", firstHeaderBytes), func(t *testing.T) {
			u := newUnitAPI(t, rest.StreamLimits{})
			u.statements.upload = &uploadFake{}
			var body bytes.Buffer
			w := multipart.NewWriter(&body)
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", `form-data; name="file"; filename="x.ofx"`)
			if firstHeaderBytes > 0 {
				h.Set("X-First", strings.Repeat("x", firstHeaderBytes))
			}
			first, err := w.CreatePart(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.WriteString(first, "abc"); err != nil {
				t.Fatal(err)
			}
			h = textproto.MIMEHeader{}
			h.Set("Content-Disposition", `form-data; name="extra"`)
			h.Set("X-Large", strings.Repeat("x", 5<<20))
			if _, err = w.CreatePart(h); err != nil {
				t.Fatal(err)
			}
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/v1/statements", strings.NewReader(body.String()))
			counter := &importReadCounter{ReadCloser: req.Body}
			req.Body = counter
			req.Header.Set("X-Client-Version", "android/0.2.5+41")
			req.Header.Set("Authorization", goodToken)
			req.Header.Set("Idempotency-Key", testKey)
			req.Header.Set("Content-Type", w.FormDataContentType())
			res := httptest.NewRecorder()
			u.h.ServeHTTP(res, req)
			if res.Code != 400 || counter.bytes > maxImportEnvelopeBytes+1+len("abc") || u.statements.upload.closed || len(u.statements.upload.sent) != 2 {
				t.Fatalf("unbounded second MIME header: status=%d bytes=%d sends=%d closed=%v", res.Code, counter.bytes, len(u.statements.upload.sent), u.statements.upload.closed)
			}
		})
	}
}

// This alignment exhausts the envelope while multipart reads the file tail:
// prefetched second-header bytes make the remaining budget negative before parsing.
func TestUnitImportRejectsAlreadyExhaustedEnvelopeBeforeSecondHeader(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	u.statements.upload = &uploadFake{}
	const fileBytes = 300_000
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="x.ofx"`)
	h.Set("X-First", strings.Repeat("x", 65_000))
	first, err := w.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(first, strings.Repeat("f", fileBytes)); err != nil {
		t.Fatal(err)
	}
	h = textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="extra"`)
	h.Set("X-Large", strings.Repeat("x", 5<<20))
	if _, err = w.CreatePart(h); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/statements", strings.NewReader(body.String()))
	counter := &importReadCounter{ReadCloser: req.Body}
	req.Body = counter
	req.Header.Set("X-Client-Version", "android/0.2.5+41")
	req.Header.Set("Authorization", goodToken)
	req.Header.Set("Idempotency-Key", testKey)
	req.Header.Set("Content-Type", w.FormDataContentType())
	res := httptest.NewRecorder()
	u.h.ServeHTTP(res, req)
	forwarded := 0
	for _, part := range u.statements.upload.sent {
		forwarded += len(part.GetChunk())
	}
	if forwarded != fileBytes || counter.bytes-fileBytes <= maxImportEnvelopeBytes+1 {
		t.Fatalf("fixture did not exhaust the remaining budget after the file: forwarded=%d read=%d", forwarded, counter.bytes)
	}
	// At most one 4 KiB MIME read-ahead was needed to finish the file. Parsing the
	// second header must not continue with a negative (unlimited) reader allowance.
	if res.Code != 400 || counter.bytes > fileBytes+maxImportEnvelopeBytes+4096 || u.statements.upload.closed {
		t.Fatalf("exhausted envelope resumed unbounded MIME parsing: status=%d read=%d closed=%v", res.Code, counter.bytes, u.statements.upload.closed)
	}
}

type noProgressImportReader struct{}

func (noProgressImportReader) Read([]byte) (int, error) { return 0, nil }
func TestUnitImportChunkPreservesTailErrorAndBoundsZeroProgress(t *testing.T) {
	buf := make([]byte, 8)
	n, err := readImportChunk(strings.NewReader("abc"), buf)
	if n != 3 || !errors.Is(err, io.EOF) {
		t.Fatal(n, err)
	}
	n, err = readImportChunk(noProgressImportReader{}, buf)
	if n != 0 || !errors.Is(err, io.ErrNoProgress) {
		t.Fatal(n, err)
	}
}
