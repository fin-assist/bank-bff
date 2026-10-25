package rest

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// clientVersionShape is the X-Client-Version pattern of openapi.yaml.
var clientVersionShape = regexp.MustCompile(`^([a-z][a-z0-9_]*)/((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))\+[0-9]+$`)

// Version is MAJOR.MINOR.PATCH with numbers kept as canonical digit strings, so any
// length compares correctly without overflow.
type Version [3]string

// ParseVersion parses MAJOR.MINOR.PATCH without leading zeros.
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", s)
	}
	var v Version
	for i, p := range parts {
		if p == "" || len(p) > 1 && p[0] == '0' || strings.Trim(p, "0123456789") != "" {
			return Version{}, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", s)
		}
		v[i] = p
	}
	return v, nil
}

// Less compares numerically, part by part.
func (v Version) Less(o Version) bool {
	for i := range v {
		if len(v[i]) != len(o[i]) {
			return len(v[i]) < len(o[i])
		}
		if v[i] != o[i] {
			return v[i] < o[i]
		}
	}
	return false
}

// Client is a parsed X-Client-Version.
type Client struct {
	Platform string
	Version  Version
}

// ParseClient parses X-Client-Version: <platform>/<MAJOR.MINOR.PATCH>+<build>, at most 64 characters.
func ParseClient(header string) (Client, bool) {
	if len(header) > 64 {
		return Client{}, false
	}
	m := clientVersionShape.FindStringSubmatch(header)
	if m == nil {
		return Client{}, false
	}
	v, err := ParseVersion(m[2])
	if err != nil {
		return Client{}, false
	}
	return Client{Platform: m[1], Version: v}, true
}

// Label is the bounded metric label: platform/MAJOR.MINOR.
func (c Client) Label() string { return c.Platform + "/" + c.Version[0] + "." + c.Version[1] }

// Maintenance is the maintenance gate configuration.
type Maintenance struct {
	Enabled bool
	// Until is the expected end, zero when unknown.
	Until time.Time
	// RetryAfter is the wait announced while the end is unknown or passed.
	RetryAfter time.Duration
}

// Gates applies the first checks of every /v1 request in the contract order:
// 503 MAINTENANCE, then 400 CLIENT_VERSION_UNSUPPORTED (docs/api.md, «Ошибки»).
type Gates struct {
	Maintenance Maintenance
	// MinVersions is the minimum supported version per platform; an absent platform is unknown.
	MinVersions map[string]Version
	// Clock is "now" of the Retry-After of maintenance.
	Clock func() time.Time
}

// Check returns the gate refusal of r, if any, and the client it parsed.
func (g Gates) Check(r *http.Request) (Client, *Error) {
	if g.Maintenance.Enabled {
		e := Error{Status: http.StatusServiceUnavailable, Code: "MAINTENANCE", RetryAfter: g.Maintenance.RetryAfter}
		if !g.Maintenance.Until.IsZero() {
			e.Until = g.Maintenance.Until.Format(time.RFC3339)
			if left := g.Maintenance.Until.Sub(g.Clock()); left > 0 {
				e.RetryAfter = left
			}
		}
		return Client{}, &e
	}
	values := r.Header.Values("X-Client-Version")
	if len(values) != 1 {
		return Client{}, unsupported()
	}
	c, ok := ParseClient(values[0])
	if !ok {
		return Client{}, unsupported()
	}
	minimum, known := g.MinVersions[c.Platform]
	if !known || c.Version.Less(minimum) {
		return c, unsupported()
	}
	return c, nil
}

func unsupported() *Error {
	return &Error{Status: http.StatusBadRequest, Code: "CLIENT_VERSION_UNSUPPORTED"}
}

var platformShape = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidPlatform reports whether p is a platform code of X-Client-Version.
func ValidPlatform(p string) bool { return platformShape.MatchString(p) }
