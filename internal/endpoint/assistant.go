package endpoint

import (
	"context"
	"net/http"
	"strconv"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"

	"github.com/fin-assist/bank-bff/internal/rest"
)

// defaultHistoryLimit is the page size without ?limit (docs/api.md 7.3).
const defaultHistoryLimit = 20

func (d Deps) converter(c *rest.Call, timeZone string) converter {
	return converter{loc: zone(timeZone), metrics: c.Metrics}
}

// MessagesList is the 200 of GET /v1/assistant/messages.
type MessagesList struct {
	Messages []Message `json:"messages"`
	HasOlder bool      `json:"has_older"`
}

func (d Deps) listMessages(c *rest.Call) {
	q := c.R.URL.Query()
	req := &assistantv1.ListMessagesRequest{Limit: defaultHistoryLimit}
	if v := q.Get("limit"); v != "" {
		// The schema (1..50) is already checked.
		n, _ := strconv.ParseInt(v, 10, 32)
		req.Limit = int32(n)
	}
	if q.Has("before_id") {
		before := q.Get("before_id")
		req.BeforeId = &before
	}
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Assistant.ListMessages(ctx, req)
	if err != nil {
		c.Fail(err)
		return
	}
	conv := d.converter(c, resp.GetTimeZone())
	out := MessagesList{Messages: []Message{}, HasOlder: resp.GetHasOlder()}
	for _, m := range resp.GetMessages() {
		if msg, ok := conv.message(m); ok {
			out.Messages = append(out.Messages, msg)
		}
	}
	rest.WriteJSON(c.W, http.StatusOK, out)
}

// AskResponse is the 201 of POST /v1/assistant/messages.
type AskResponse struct {
	Question       Message `json:"question"`
	AnswerID       string  `json:"answer_id"`
	RemainingLimit int32   `json:"remaining_limit"`
}

var transferModes = map[string]commonv1.TransferMode{
	"with": commonv1.TransferMode_TRANSFER_MODE_WITH, "without": commonv1.TransferMode_TRANSFER_MODE_WITHOUT,
}

func (d Deps) ask(c *rest.Call) {
	var in struct {
		Text         string  `json:"text"`
		TransferMode *string `json:"transfer_mode"`
	}
	if !decode(c, &in) {
		return
	}
	mode := commonv1.TransferMode_TRANSFER_MODE_UNSPECIFIED // = with
	if in.TransferMode != nil {
		m, known := transferModes[*in.TransferMode]
		if !known {
			// An unknown value of the open code is refused in a request (docs/api.md 7.1).
			rest.Validation(rest.Detail{Field: "transfer_mode", Code: rest.DetailInvalidFormat, Message: "has invalid format"}).Write(c.W)
			return
		}
		mode = m
	}
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Assistant.AskQuestion(ctx, &assistantv1.AskQuestionRequest{Text: in.Text, TransferMode: mode})
	if err != nil {
		c.Fail(err)
		return
	}
	question, ok := d.converter(c, "").message(resp.GetQuestion())
	if !ok {
		rest.Internal().Write(c.W)
		return
	}
	rest.WriteJSON(c.W, http.StatusCreated, AskResponse{Question: question, AnswerID: resp.GetAnswerId(), RemainingLimit: resp.GetRemainingLimit()})
}

// RetryResponse is the 201 of POST /v1/assistant/messages/{answer_id}/retry.
type RetryResponse struct {
	AnswerID       string `json:"answer_id"`
	RemainingLimit int32  `json:"remaining_limit"`
}

func (d Deps) retry(c *rest.Call) {
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Assistant.RetryAnswer(ctx, &assistantv1.RetryAnswerRequest{AnswerId: c.R.PathValue("answer_id")})
	if err != nil {
		c.Fail(err)
		return
	}
	rest.WriteJSON(c.W, http.StatusCreated, RetryResponse{AnswerID: resp.GetAnswerId(), RemainingLimit: resp.GetRemainingLimit()})
}

// AssistantLimit is the 200 of GET /v1/assistant/limit.
type AssistantLimit struct {
	Remaining int32  `json:"remaining"`
	DailyMax  int32  `json:"daily_max"`
	ResetsAt  string `json:"resets_at"`
}

func (d Deps) limit(c *rest.Call) {
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Assistant.GetLimit(ctx, &assistantv1.GetLimitRequest{})
	if err != nil {
		c.Fail(err)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, AssistantLimit{Remaining: resp.GetRemaining(), DailyMax: resp.GetDailyMax(),
		ResetsAt: moment(resp.GetResetsAt(), zone(resp.GetTimeZone()))})
}

// Snapshot is the snapshot event (always first).
type Snapshot struct {
	Status   string  `json:"status"`
	Blocks   []Block `json:"blocks"`
	Chips    []Chip  `json:"chips"`
	Source   *Source `json:"source"`
	Currency string  `json:"currency"`
}

type chunkEvent struct {
	Index int32  `json:"index"`
	Text  string `json:"text"`
}

type blockEvent struct {
	Index int32 `json:"index"`
	Block Block `json:"block"`
}

type doneEvent struct {
	Charged        bool  `json:"charged"`
	RemainingLimit int32 `json:"remaining_limit"`
}

// AnswerError is the terminal error event of an answer. Text, user_message and
// severity come from the BFF catalog; retryable is the assistant's permission of a
// new attempt (RetryAnswer), not REST retry of the original question.
type AnswerError struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	UserMessage    string `json:"user_message"`
	Severity       string `json:"severity"`
	Text           string `json:"text"`
	Retryable      bool   `json:"retryable"`
	RemainingLimit int32  `json:"remaining_limit"`
}

// answerEvent maps one stream message to an SSE event name and data; false skips a
// message bank-bff cannot translate.
func (c converter) answerEvent(m *assistantv1.WatchAnswerResponse) (name string, data any, terminal, ok bool) {
	switch e := m.GetEvent().(type) {
	case *assistantv1.WatchAnswerResponse_Snapshot:
		s := e.Snapshot
		status, known := enumCode(s.GetStatus())
		if !known {
			// An unknown status is read as failed (docs/api.md 7.2).
			status = "failed"
		}
		return "snapshot", Snapshot{Status: status, Blocks: c.blocks(s.GetBlocks()), Chips: c.chips(s.GetChips()),
			Source: c.source(s.Source), Currency: c.currency(s.GetCurrency(), "AnswerSnapshot.currency")}, false, true
	case *assistantv1.WatchAnswerResponse_Chunk:
		return "chunk", chunkEvent{Index: e.Chunk.GetIndex(), Text: e.Chunk.GetText()}, false, true
	case *assistantv1.WatchAnswerResponse_Block:
		return "block", blockEvent{Index: e.Block.GetIndex(), Block: c.block(e.Block.GetBlock())}, false, true
	case *assistantv1.WatchAnswerResponse_Source:
		if e.Source != nil {
			return "source", c.source(e.Source), false, true
		}
	case *assistantv1.WatchAnswerResponse_Chip:
		chip, ok := c.chip(e.Chip)
		return "chip", chip, false, ok
	case *assistantv1.WatchAnswerResponse_Done:
		return "done", doneEvent{Charged: e.Done.GetCharged(), RemainingLimit: e.Done.GetRemainingLimit()}, true, true
	case *assistantv1.WatchAnswerResponse_Error:
		code := c.answerCode(e.Error.GetCode())
		entry := answerPresentation(code)
		return "error", AnswerError{Code: code, Message: entry.Diagnostic, UserMessage: entry.Text, Severity: entry.Severity,
			Text: entry.Text, Retryable: e.Error.GetRetryable(), RemainingLimit: e.Error.GetRemainingLimit()}, true, true
	}
	c.metrics.Untranslated("WatchAnswerResponse.event", "unknown")
	return "", nil, false, false
}

func (d Deps) watchAnswer(c *rest.Call) {
	release, ok := c.Streams.Acquire(rest.HintsFrom(c.R.Context()).ClientIP)
	if !ok {
		c.Metrics.StreamRejected(c.Op.Route.Path)
		c.Streams.Busy(c.Clock()).Write(c.W)
		return
	}
	defer release()
	timeout := orDefault(d.AnswerStream, AnswerStreamDeadline)
	ctx, cancel := context.WithTimeout(c.R.Context(), timeout)
	defer cancel()
	stream, err := d.Assistant.WatchAnswer(ctx, &assistantv1.WatchAnswerRequest{AnswerId: c.R.PathValue("answer_id")})
	if err != nil {
		c.Fail(err)
		return
	}
	// Errors before the first message (another user's or replaced answer -> NOT_FOUND)
	// are ordinary HTTP answers.
	first, err := stream.Recv()
	if err != nil {
		c.Fail(err)
		return
	}
	sse, err := rest.StartStream(c.W, time.Now().Add(timeout+streamWriteMargin))
	if err != nil {
		c.Log.WarnContext(ctx, "stream start failed")
		return
	}
	defer c.Metrics.StreamOpened(c.Op.Route.Path)()
	send := func(m *assistantv1.WatchAnswerResponse) (bool, error) {
		name, data, terminal, ok := d.converter(c, m.GetTimeZone()).answerEvent(m)
		if !ok {
			return false, nil
		}
		return terminal, sse.Event(name, data)
	}
	if done, err := send(first); done || err != nil {
		return
	}
	// The generation does not depend on this stream: a client disconnect or a peer
	// failure only closes it, without a terminal event (grpc-api.md 5.2).
	_ = rest.Pump(ctx, stream.Recv, cancel, d.heartbeat(), sse.Ping, send)
}
