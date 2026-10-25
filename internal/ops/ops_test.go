package ops

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnitOperationalEndpoints(t *testing.T) {
	r := &Readiness{}
	h := Handler(r)
	for _, tc := range []struct {
		method, path string
		ready        bool
		code         int
		body         string
	}{
		{"GET", "/healthz", false, 200, "ok\n"}, {"GET", "/readyz", false, 503, "not ready\n"},
		{"GET", "/readyz", true, 200, "ok\n"}, {"GET", "/readyz", false, 503, "not ready\n"},
		{"GET", "/metrics", true, 200, "go_goroutines"}, {"POST", "/healthz", true, 405, "Method Not Allowed"},
		{"GET", "/unknown", true, 404, "404 page not found"},
	} {
		r.Set(tc.ready)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, httptest.NewRequest(tc.method, tc.path, nil))
		if res.Code != tc.code || !strings.Contains(res.Body.String(), tc.body) {
			t.Fatalf("%s %s: %d %q", tc.method, tc.path, res.Code, res.Body.String())
		}
	}
	// A HEAD probe has the same status as GET.
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodHead, "/healthz", nil))
	if res.Code != 200 {
		t.Fatal(res.Code)
	}
}
