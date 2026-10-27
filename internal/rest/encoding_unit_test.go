package rest

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

type encodingReadSpy struct {
	io.Reader
	reads, closes int
}

func (b *encodingReadSpy) Read(p []byte) (int, error) { b.reads++; return b.Reader.Read(p) }
func (b *encodingReadSpy) Close() error               { b.closes++; _, err := io.Copy(io.Discard, b); return err }

var encodingJSONRoutes = []struct{ method, path, body string }{
	{"POST", "/v1/auth/phone", `{"phone":"+79001234567"}`},
	{"POST", "/v1/auth/register", `{"registration_token":"rt","pd_consent_accepted":true,"pd_consent_version":"v"}`},
	{"POST", "/v1/auth/token", `{"refresh_token":"rt"}`},
	{"POST", "/v1/auth/logout", `{"refresh_token":"rt"}`},
	{"PUT", "/v1/profile/assistant-consent", `{"version":"v"}`},
	{"POST", "/v1/assistant/messages", `{"text":"x"}`},
}

func TestUnitJSONEncodingRefusesBeforeAnyBodyRead(t *testing.T) {
	for _, route := range encodingJSONRoutes {
		t.Run(route.path, func(t *testing.T) {
			calls := 0
			h, err := Handler(handlerConfig(t), []Route{{Method: route.method, Path: route.path, Protected: Always, Idempotent: true, Handle: func(c *Call) { calls++; WriteNoContent(c.W) }}})
			if err != nil {
				t.Fatal(err)
			}
			for _, values := range [][]string{{"gzip"}, {"br"}, {""}, {"identity,gzip"}, {"identity", "gzip"}, {"identity", "identity"}} {
				for _, length := range []int64{0, 12, MinBodyLimit + 1, -1} {
					body := &encodingReadSpy{Reader: strings.NewReader(strings.Repeat("x", int(MinBodyLimit+1)))}
					req := httptest.NewRequest(route.method, route.path, nil)
					req.Body = body
					req.ContentLength = length
					req.Header.Set("X-Client-Version", "android/0.2.5+41")
					req.Header.Set("Authorization", "Bearer good")
					req.Header.Set("Content-Type", "application/json")
					req.Header["Content-Encoding"] = values
					// Missing key and oversized/invalid JSON cannot outrank encoding refusal.
					w := httptest.NewRecorder()
					h.ServeHTTP(w, req)
					got := decodeError(t, w.Body.String())
					if w.Code != 400 || got.Code != "VALIDATION_ERROR" || len(got.Details) != 1 || got.Details[0].Field != "Content-Encoding" || got.Details[0].Code != DetailInvalidFormat || body.reads != 0 || body.closes != 0 || calls != 0 || w.Header().Get("Connection") != "close" {
						t.Fatalf("%v len%d: status%d body%+v reads%d closes%d calls%d", values, length, w.Code, got, body.reads, body.closes, calls)
					}
				}
			}
		})
	}
}
func TestUnitJSONEncodingAllowsAbsentAndOneIdentity(t *testing.T) {
	for _, route := range encodingJSONRoutes {
		t.Run(route.path, func(t *testing.T) {
			calls := 0
			h, err := Handler(handlerConfig(t), []Route{{Method: route.method, Path: route.path, Protected: Always, Idempotent: true, Handle: func(c *Call) {
				calls++
				if string(c.Body) != route.body {
					t.Fatal(string(c.Body))
				}
				WriteNoContent(c.W)
			}}})
			if err != nil {
				t.Fatal(err)
			}
			for _, values := range [][]string{nil, {"identity"}, {"IDENTITY"}, {" \tIdEnTiTy\t "}} {
				before := calls
				req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
				req.Header.Set("X-Client-Version", "android/0.2.5+41")
				req.Header.Set("Authorization", "Bearer good")
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", "0190c3a0-1b2c-7d3e-8f40-123456789abc")
				if values != nil {
					req.Header["Content-Encoding"] = values
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != 204 || calls != before+1 || w.Header().Get("Connection") != "" {
					t.Fatalf("%v: %d %s calls%d", values, w.Code, w.Body.String(), calls)
				}
			}
		})
	}
}
func TestUnitJSONEncodingKeepsGatesAndAuthenticationFirst(t *testing.T) {
	for _, tc := range []struct {
		name, version, auth string
		maintenance         bool
		want                int
		code                string
	}{
		{"maintenance", "android/0.1.0+1", "bad", true, 503, "MAINTENANCE"},
		{"version", "android/0.1.0+1", "bad", false, 400, "CLIENT_VERSION_UNSUPPORTED"},
		{"auth", "android/0.2.5+41", "bad", false, 401, "INVALID_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := handlerConfig(t)
			cfg.Gates.Maintenance.Enabled = tc.maintenance
			h, err := Handler(cfg, []Route{{Method: "POST", Path: "/v1/assistant/messages", Protected: Always, Handle: func(*Call) { t.Fatal("rejected request reached endpoint") }}})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/v1/assistant/messages", strings.NewReader(`{"text":"x"}`))
			req.Header.Set("X-Client-Version", tc.version)
			req.Header.Set("Authorization", tc.auth)
			req.Header.Set("Content-Encoding", "gzip")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.want || decodeError(t, w.Body.String()).Code != tc.code {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestUnitJSONEncodingLeavesOtherOperationPolicies(t *testing.T) {
	for _, route := range [][2]string{{"PATCH", "/v1/profile"}, {"POST", "/v1/statements"}, {"POST", "/v1/assistant/messages/{answer_id}/retry"}, {"GET", "/v1/assistant/messages"}} {
		req := httptest.NewRequest(route[0], "/", nil)
		req.Header.Set("Content-Encoding", "gzip")
		req.Header.Add("Content-Encoding", "identity")
		if refusal := jsonEncodingRefusal(req, operation(t, route[0], route[1])); refusal != nil {
			t.Fatalf("%v: %+v", route, refusal)
		}
	}
}
