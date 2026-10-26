package endpoint

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/rest"
)

// Upload budgets are independent: the multipart envelope is not file content.
// MaxImportFileBytes is the fixed 10 MiB import limit of REST 3.2.
const (
	MaxImportFileBytes     int64 = 10 << 20
	maxImportEnvelopeBytes       = 64 << 10
	maxImportWireBytes           = MaxImportFileBytes + maxImportEnvelopeBytes
	importChunkBytes             = 256 << 10
)

func (d Deps) importConfig(c *rest.Call) {
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Statements.GetImportConfig(ctx, &statementdatav1.GetImportConfigRequest{})
	if err != nil {
		c.Fail(err)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, d.converter(c, resp.GetTimeZone()).importConfig(resp))
}
func (d Deps) listUploads(c *rest.Call) {
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Statements.ListUploads(ctx, &statementdatav1.ListUploadsRequest{})
	if err != nil {
		c.Fail(err)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, d.converter(c, resp.GetTimeZone()).uploads(resp))
}
func (d Deps) deleteUpload(c *rest.Call) {
	ctx, cancel := unary(c)
	defer cancel()
	_, err := d.Statements.DeleteUpload(ctx, &statementdatav1.DeleteUploadRequest{UploadId: c.R.PathValue("upload_id")})
	if err != nil {
		c.Fail(err)
		return
	}
	c.W.WriteHeader(http.StatusNoContent)
}
func (d Deps) unreadLines(c *rest.Call) {
	q := c.R.URL.Query()
	req := &statementdatav1.ListUnreadLinesRequest{}
	if q.Has("upload_id") && !q.Has("from") && !q.Has("to") {
		req.Scope = &statementdatav1.ListUnreadLinesRequest_UploadId{UploadId: q.Get("upload_id")}
	} else if !q.Has("upload_id") && q.Has("from") && q.Has("to") {
		from, e1 := time.Parse(time.RFC3339Nano, q.Get("from"))
		to, e2 := time.Parse(time.RFC3339Nano, q.Get("to"))
		if e1 != nil || e2 != nil || !from.Before(to) {
			field := "to"
			if e1 != nil {
				field = "from"
			}
			rest.Validation(rest.Detail{Field: field, Code: rest.DetailInvalidFormat, Message: "period must have from before to"}).Write(c.W)
			return
		}
		req.Scope = &statementdatav1.ListUnreadLinesRequest_Period{Period: &commonv1.TimeRange{Start: timestamppb.New(from), End: timestamppb.New(to)}}
	} else {
		rest.Validation(rest.Detail{Code: rest.DetailInvalidFormat, Message: "provide upload_id or from and to"}).Write(c.W)
		return
	}
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Statements.ListUnreadLines(ctx, req)
	if err != nil {
		c.Fail(err)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, d.converter(c, resp.GetTimeZone()).unread(resp))
}

func invalidImport(c *rest.Call, message string) {
	rest.Validation(rest.Detail{Field: "file", Code: rest.DetailInvalidFormat, Message: message}).Write(c.W)
}
func importTooLarge(c *rest.Call) {
	c.Refuse(rest.Error{Status: http.StatusRequestEntityTooLarge, Code: "FILE_TOO_LARGE"}, false)
}

func (d Deps) uploadStatement(c *rest.Call) {
	// The total wire upper bound is safe even though Content-Length includes the
	// envelope. Reject it without opening an internal stream or reading the file.
	if c.R.ContentLength > maxImportWireBytes {
		importTooLarge(c)
		return
	}
	release, ok := c.Uploads.Acquire(rest.HintsFrom(c.R.Context()).ClientIP)
	if !ok {
		c.Metrics.StreamRejected(c.Op.Route.Path)
		c.Refuse(c.Uploads.Busy(c.Clock()), false)
		return
	}
	defer release()
	// A unary listener's read/write limits must not truncate a valid streaming upload.
	if err := rest.ExtendDeadlines(c.W, time.Now().Add(UploadDeadline+streamWriteMargin)); err != nil {
		rest.Internal().Write(c.W)
		return
	}
	media, params, err := mime.ParseMediaType(c.R.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" || params["boundary"] == "" {
		invalidImport(c, "expected multipart/form-data")
		return
	}
	wire := &importWireReader{ReadCloser: http.MaxBytesReader(c.W, c.R.Body, maxImportWireBytes), headerRemaining: maxImportEnvelopeBytes + 1}
	c.R.Body = wire
	reader := multipart.NewReader(c.R.Body, params["boundary"])
	part, err := reader.NextRawPart()
	if err != nil {
		invalidImport(c, "expected one file part")
		return
	}
	wire.headerRemaining = -1 // Header parsing is complete; permit streamed file reads.
	disposition, p, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	// FileName() calls filepath.Base and corrupts the hash identity; use the raw
	// decoded MIME parameter instead, preserving Unicode, path, spaces and case.
	filename, present := p["filename"]
	if err != nil || disposition != "form-data" || p["name"] != "file" || !present || !utf8.ValidString(filename) || strings.ContainsRune(filename, 0) {
		invalidImport(c, "expected one named file part")
		return
	}
	contentType := part.Header.Get("Content-Type")
	// The decoded metadata alone cannot exceed the entire envelope budget. Reject
	// it before gRPC so a transport message limit cannot turn this form error into
	// a peer outage; the exact full envelope is checked again before EOF.
	if len(filename)+len(contentType) > maxImportEnvelopeBytes {
		invalidImport(c, "multipart metadata exceeds its budget")
		return
	}
	ctx, cancel := context.WithTimeout(c.R.Context(), UploadDeadline)
	defer cancel()
	stream, err := d.Statements.UploadStatement(ctx)
	if err != nil {
		c.Fail(err)
		return
	}
	if err = stream.Send(&statementdatav1.UploadStatementRequest{Part: &statementdatav1.UploadStatementRequest_Header{Header: &statementdatav1.UploadHeader{FileName: filename, ContentType: contentType}}}); err != nil {
		uploadSendError(c, stream, err)
		return
	}
	buf := make([]byte, importChunkBytes)
	var total int64
	for {
		n, readErr := readImportChunk(part, buf)
		total += int64(n)
		if total > MaxImportFileBytes {
			importTooLarge(c)
			return
		}
		if n > 0 {
			if err = stream.Send(&statementdatav1.UploadStatementRequest{Part: &statementdatav1.UploadStatementRequest_Chunk{Chunk: buf[:n]}}); err != nil {
				uploadSendError(c, stream, err)
				return
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			invalidImport(c, "file could not be read")
			return
		}
	}
	// Do not send gRPC EOF until the ENTIRE form has been checked. Cancelling an
	// invalid extra/truncated part prevents the service's final hash lookup/effect.
	if _, err = reader.NextRawPart(); !errors.Is(err, io.EOF) {
		invalidImport(c, "expected exactly one file part")
		return
	}
	if _, err = io.Copy(io.Discard, c.R.Body); err != nil {
		invalidImport(c, "multipart envelope exceeds its budget or is incomplete")
		return
	}
	if wire.bytes-total > maxImportEnvelopeBytes {
		invalidImport(c, "multipart envelope exceeds its budget")
		return
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		c.Fail(err)
		return
	}
	rest.WriteJSON(c.W, http.StatusAccepted, ImportAccepted{resp.GetUploadId(), resp.GetFileName()})
}

// An early server refusal may appear as io.EOF from Send. Recv exposes its actual
// typed status (not a fabricated INTERNAL_ERROR); no final client EOF is sent.
func uploadSendError(c *rest.Call, stream grpc.ClientStreamingClient[statementdatav1.UploadStatementRequest, statementdatav1.UploadStatementResponse], err error) {
	if errors.Is(err, io.EOF) {
		var ignored statementdatav1.UploadStatementResponse
		if receiveErr := stream.RecvMsg(&ignored); receiveErr != nil {
			err = receiveErr
		}
	}
	c.Fail(err)
}

func (d Deps) watchUpload(c *rest.Call) {
	ctx, done, ok := openStream(c, ImportStreamDeadline)
	if !ok {
		return
	}
	defer done()
	stream, err := d.Statements.WatchUpload(ctx, &statementdatav1.WatchUploadRequest{UploadId: c.R.PathValue("upload_id")})
	if err != nil {
		c.Fail(err)
		return
	}
	first, err := stream.Recv()
	if err != nil {
		c.Fail(err)
		return
	}
	sse, err := rest.StartStream(c.W)
	if err != nil {
		c.Log.WarnContext(ctx, "stream start failed")
		return
	}
	defer c.Metrics.StreamOpened(c.Op.Route.Path)()
	send := func(m *statementdatav1.WatchUploadResponse) (bool, error) {
		name, data, terminal, known := d.converter(c, m.GetTimeZone()).importEvent(m)
		if !known {
			return false, nil
		}
		return terminal, sse.Event(name, data)
	}
	if terminal, err := send(first); terminal || err != nil {
		return
	}
	// Transport interruption never becomes a terminal business failure.
	_ = rest.Pump(ctx, stream.Recv, done, d.heartbeat(), sse.Ping, send)
}

// importWireReader counts the whole MIME envelope separately from the file.
type importWireReader struct {
	io.ReadCloser
	bytes           int64
	headerRemaining int64
}

func (r *importWireReader) Read(p []byte) (int, error) {
	if r.headerRemaining == 0 {
		return 0, errors.New("multipart initial header exceeds budget")
	}
	if r.headerRemaining > 0 && int64(len(p)) > r.headerRemaining {
		p = p[:int(r.headerRemaining)]
	}
	n, err := r.ReadCloser.Read(p)
	if r.headerRemaining > 0 {
		r.headerRemaining -= int64(n)
	}
	r.bytes += int64(n)
	return n, err
}

// readImportChunk coalesces MIME reads while preserving the actual EOF/error.
// ReadFull conflates a normal short tail with a truncated multipart boundary.
func readImportChunk(reader io.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		count, err := reader.Read(buf[n:])
		n += count
		if err != nil {
			return n, err
		}
		if count == 0 {
			return n, io.ErrNoProgress
		}
	}
	return n, nil
}
