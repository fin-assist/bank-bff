package endpoint

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	"google.golang.org/grpc"

	"github.com/fin-assist/bank-bff/internal/rest"
)

func TestUnitDecodeExactKeepsOnlyExactMembers(t *testing.T) {
	var v struct {
		Text string  `json:"text"`
		Mode *string `json:"transfer_mode,omitempty"`
		Skip string  `json:"-"`
		Bare string
	}
	if err := decodeExact([]byte(`{"text":"a","Text":"b","transfer_mode":"with","Bare":"x","bare":"y","-":"z"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.Text != "a" || v.Mode == nil || *v.Mode != "with" || v.Skip != "" || v.Bare != "" {
		t.Fatalf("%+v", v)
	}
	for _, body := range []string{`[1]`, `{"text":1}`, `{`} {
		if err := decodeExact([]byte(body), &v); err == nil {
			t.Fatalf("%s decoded", body)
		}
	}
}

// noDeadlines is a connection whose deadlines cannot be moved.
type noDeadlines struct{ *httptest.ResponseRecorder }

func (noDeadlines) SetWriteDeadline(time.Time) error { return errors.New("closed") }

func TestUnitStreamRefusedWhenDeadlinesCannotBeExtended(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{Max: 1, PerIP: 1, RetryAfter: time.Second})
	called := 0
	u.f.watchAnswer = func(ctx context.Context, in *assistantv1.WatchAnswerRequest) (grpc.ServerStreamingClient[assistantv1.WatchAnswerResponse], error) {
		called++
		return answerStream([]*assistantv1.WatchAnswerResponse{{Event: &assistantv1.WatchAnswerResponse_Done{Done: &assistantv1.AnswerDone{}}}}, nil, false)(ctx, in)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/assistant/messages/a1/events", nil)
	req.Header.Set("X-Client-Version", "android/0.2.5+41")
	req.Header.Set("Authorization", goodToken)
	w := noDeadlines{httptest.NewRecorder()}
	u.h.ServeHTTP(w, req)
	if w.Code != 500 || called != 0 {
		t.Fatalf("%d %s, peer called %d times", w.Code, w.Body.String(), called)
	}
	// The slot was released: the only stream slot is free again.
	if r := u.serve(t, http.MethodGet, "/v1/assistant/messages/a1/events", "", nil); r.Code != 200 || called != 1 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
}
