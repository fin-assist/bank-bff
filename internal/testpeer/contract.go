package testpeer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"

	"github.com/fin-assist/bank-bff/internal/apispec"
)

// Contract validates BFF responses and SSE events against the embedded openapi.yaml
// with kin-openapi, including undeclared statuses.
type Contract struct{ doc *openapi3.T }

// LoadContract parses the embedded contract, independently of the BFF's own copy,
// once per test process.
func LoadContract(t testing.TB) *Contract {
	t.Helper()
	doc, err := contractOnce()
	if err != nil {
		t.Fatal(err)
	}
	return &Contract{doc: doc}
}

var contractOnce = sync.OnceValues(func() (*openapi3.T, error) {
	doc, err := openapi3.NewLoader().LoadFromData(apispec.Document())
	if err != nil {
		return nil, err
	}
	return doc, doc.Validate(context.Background(), openapi3.DisableExamplesValidation())
})

func (c *Contract) route(t testing.TB, method, path string) *routers.Route {
	t.Helper()
	item := c.doc.Paths.Value(path)
	if item == nil || item.GetOperation(method) == nil {
		t.Fatalf("%s %s is not in the contract", method, path)
	}
	return &routers.Route{Spec: c.doc, Path: path, PathItem: item, Method: method, Operation: item.GetOperation(method)}
}

// Check validates a non-SSE response of the operation and returns the violation.
func (c *Contract) Check(t testing.TB, method, path string, req *http.Request, resp *http.Response, body []byte) error {
	t.Helper()
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, Route: c.route(t, method, path),
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}},
		Status: resp.StatusCode, Header: resp.Header,
		Options: &openapi3filter.Options{IncludeResponseStatus: true, MultiError: true},
	}
	input.SetBodyBytes(body)
	return openapi3filter.ValidateResponse(context.Background(), input)
}

// Response validates a non-SSE response of the operation (method, path template)
// and returns the decoded JSON body (nil for an empty body).
func (c *Contract) Response(t testing.TB, method, path string, req *http.Request, resp *http.Response, body []byte) map[string]any {
	t.Helper()
	if err := c.Check(t, method, path, req, resp, body); err != nil {
		t.Fatalf("%s %s %d violates openapi.yaml: %v\n%s", method, path, resp.StatusCode, err, body)
	}
	if resp.Header.Get("Date") == "" {
		t.Fatalf("%s %s: no Date header", method, path)
	}
	if len(body) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%s %s: body is not a JSON object: %v", method, path, err)
	}
	return out
}

// Event is one parsed server-sent event.
type Event struct {
	Name string
	Data map[string]any
}

// Events reads an SSE body to its end, validating every event's data against the
// operation's x-sse-events schema. Comments (heartbeats) are counted.
func (c *Contract) Events(t testing.TB, method, path string, body io.Reader) (events []Event, pings int) {
	t.Helper()
	resp := c.route(t, method, path).Operation.Responses.Value("200")
	ext, ok := resp.Value.Extensions["x-sse-events"].(map[string]any)
	if !ok {
		t.Fatalf("%s %s has no x-sse-events", method, path)
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	var name string
	var data []byte
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, ":"):
			pings++
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = append(data, strings.TrimPrefix(line, "data: ")...)
		case line == "" && name != "":
			events = append(events, Event{Name: name, Data: c.event(t, ext, name, data)})
			name, data = "", nil
		}
	}
	return events, pings
}

func (c *Contract) event(t testing.TB, ext map[string]any, name string, data []byte) map[string]any {
	t.Helper()
	ref, ok := ext[name].(map[string]any)["$ref"].(string)
	if !ok {
		t.Fatalf("event %q is not declared", name)
	}
	schema := c.doc.Components.Schemas[strings.TrimPrefix(ref, "#/components/schemas/")]
	if schema == nil {
		t.Fatalf("event %q: unknown schema %s", name, ref)
	}
	var value any
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&value); err != nil {
		t.Fatalf("event %q: %v", name, err)
	}
	if err := schema.Value.VisitJSON(value, openapi3.MultiErrors()); err != nil {
		t.Fatalf("event %q violates %s: %v\n%s", name, ref, err, data)
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
