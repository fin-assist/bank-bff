//go:build qase90

package app

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

const (
	q90Timeout       = 45 * time.Second
	q90PeriodQuery   = "date=2026-09&transfer_mode=with"
	q90Route         = "/v1/analytics/regular-payments/{id}/dismissal"
	q90Insight       = "regular_payments"
	q90Network       = "tcp"
	q90BFF           = "bank-bff"
	q90BSD           = "bank-statement-data"
	q90Analytics     = "analytics"
	q90Snapshot      = "snapshot"
	q90Calculation   = "calculation"
	q90Recalculating = "recalculating"
	q90CalcVersion   = "calc_version"
	q90SourceVersion = "Version"
	q90Queue         = "Queue"
	q90Jobs          = "Jobs"
	q90Revision      = "Revision"
	q90References    = "References"
	q90Dismissed     = "Dismissed"
	q90State         = "state"
	q90Value         = "value"
	q90KeyEnv        = "Q90_KEY="
	q90TargetPrefix  = "passthrough:///"
)

// Every child is a race-instrumented test binary, not a production control API.
// Anonymous pipes carry only a closed command vocabulary and state evidence.
type q90Fixture struct {
	input io.WriteCloser
	lines <-chan string
}

func q90Start(t *testing.T, binary, source string, vars ...string) *q90Fixture {
	t.Helper()
	if binary == "" || source == "" {
		t.Fatal("run scripts/qase90.py; fixture binaries/sources are mandatory")
	}
	cmd := exec.Command(binary, "-test.run=^TestAPIQase90Fixture$", "-test.timeout=3m")
	cmd.Dir, cmd.Env = source, append(os.Environ(), vars...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.CreateTemp("", "fin437-q90-fixture-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = log.Close()
		if !t.Failed() {
			_ = os.Remove(log.Name())
		}
	})
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines, done := make(chan string, 8), make(chan error, 1)
	go func() {
		defer close(lines)
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			line := scan.Text()
			if strings.HasPrefix(line, "QASE90 ") {
				lines <- strings.TrimPrefix(line, "QASE90 ")
			} else {
				switch line {
				case "Q90_STAGE bsd:seed", "Q90_STAGE bsd:verified-assembly", "Q90_STAGE bsd:initial-calculation", "Q90_STAGE analytics:verified-assembly":
					t.Log(line) // Closed allowlist: never print financial bodies or tokens.
				}
				_, _ = io.WriteString(log, line+"\n")
			}
		}
		if err := scan.Err(); err != nil {
			_, _ = io.WriteString(log, "fixture output read failed\n")
		}
		done <- cmd.Wait()
	}()
	f := &q90Fixture{stdin, lines}
	t.Cleanup(func() {
		_, _ = io.WriteString(stdin, "stop\n")
		_ = stdin.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Q90 fixture failed: %v (diagnostic: %s)", err, log.Name())
			}
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			t.Logf("Q90 fixture exit after kill: %v", <-done)
			t.Error("Q90 fixture shutdown timeout")
		}
	})
	return f
}

func (f *q90Fixture) read(t *testing.T, out any) {
	t.Helper()
	select {
	case line, ok := <-f.lines:
		if !ok {
			t.Fatal("Q90 fixture exited before evidence")
		}
		if err := json.Unmarshal([]byte(line), out); err != nil {
			t.Fatal("invalid Q90 evidence", err)
		}
	case <-time.After(q90Timeout):
		t.Fatal("Q90 evidence timeout")
	}
}

func (f *q90Fixture) command(t *testing.T, command string) map[string]any {
	t.Helper()
	if _, err := io.WriteString(f.input, command+"\n"); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	f.read(t, &out)
	return out
}

func q90Keys(t *testing.T) (map[string]string, string) {
	t.Helper()
	dir := t.TempDir()
	files := make(map[string]string)
	var keys []map[string]string
	for _, service := range []string{q90BFF, q90BSD, q90Analytics} {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			t.Fatal(err)
		}
		files[service] = filepath.Join(dir, service+".pem")
		if err = os.WriteFile(files[service], pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": service, "use": "sig", "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(public)})
	}
	raw, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	jwks := filepath.Join(dir, "jwks.json")
	if err = os.WriteFile(jwks, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return files, jwks
}

func TestAPIQase90CommittedDismissalSurvivesCompositionOutage(t *testing.T) {
	keys, jwks := q90Keys(t)
	var source *q90Fixture
	var composer *q90Fixture
	var ready struct{ Address, ID, Taxi, User string }
	b := startBFF(t, func(c *config.Config, peers *testpeer.Peers) {
		user := peers.ServeTCP(t)
		vars := []string{"Q90_USER=" + user, "Q90_JWKS=" + jwks, "Q90_USER_ISSUER=" + testpeer.UserIssuer, "Q90_USER_AUDIENCE=" + testpeer.UserAudience, "Q90_STAGE_ISSUER=" + testpeer.StageIssuer}
		composer = q90Start(t, os.Getenv("Q90_ANALYTICS_BINARY"), os.Getenv("Q90_ANALYTICS_SOURCE"), append(vars, q90KeyEnv+keys[q90Analytics])...)
		var analytics struct{ Address string }
		composer.read(t, &analytics)
		analyticsTarget := q90TargetPrefix + analytics.Address
		source = q90Start(t, os.Getenv("Q90_BSD_BINARY"), os.Getenv("Q90_BSD_SOURCE"), append(vars, "Q90_ANALYTICS="+analyticsTarget, q90KeyEnv+keys[q90BSD])...)
		source.read(t, &ready)
		c.API.UserAddr, c.API.AssistantAddr = user, user
		c.API.StatementsAddr, c.API.AnalyticsAddr = q90TargetPrefix+ready.Address, analyticsTarget
		c.API.Stage.SigningKeyFile, c.API.Stage.PublicKeysFile = keys[q90BFF], jwks
	}, WithDialOptions(grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, q90Network, addr)
	})))
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access(ready.User)})
	get := request{method: "GET", path: "/v1/analytics?period=month&" + q90PeriodQuery, route: "/v1/analytics", header: auth}
	baseline := b.do(t, get)
	if baseline.StatusCode != 200 {
		t.Fatal("baseline GET", baseline.StatusCode)
	}
	before := source.command(t, q90Snapshot)
	if before[q90Dismissed] != nil || before[q90Jobs] != float64(0) || before[q90Queue] != "" {
		t.Fatal("baseline is not settled")
	}
	baselineVersion := before[q90SourceVersion].(float64)
	baselineCalculation := baseline.json[q90Calculation].(map[string]any)
	if baselineCalculation[q90CalcVersion] != baselineVersion || baselineCalculation[q90Recalculating] != false {
		t.Fatal("baseline applied version is not settled")
	}
	q90Regular(t, baseline, ready.ID, ready.Taxi, true)
	composer.command(t, "fail")
	put := request{method: "PUT", path: "/v1/analytics/regular-payments/" + ready.ID + "/dismissal?" + q90PeriodQuery, route: q90Route, header: auth}
	failure := b.do(t, put)
	if failure.StatusCode != 500 || errorCode(failure) != "INTERNAL_ERROR" || failure.json["error"].(map[string]any)["retryable"] != true {
		t.Fatal("post-commit outage must be a retryable 500", failure.StatusCode)
	}
	source.command(t, "retry")
	committed := source.command(t, q90Snapshot)
	target := committed["Target"].(float64)
	if committed[q90Dismissed] == nil || target <= baselineVersion || committed[q90SourceVersion] != baselineVersion || committed[q90Queue] == "" || committed[q90Jobs] != float64(1) || committed[q90Revision] == before[q90Revision] || committed[q90References] != before[q90References] {
		t.Fatal("durable commit/queue/true-job evidence missing")
	}
	composer.command(t, "recover")
	source.command(t, "ack")
	acked := source.command(t, q90Snapshot)
	if acked[q90Jobs] != float64(0) || acked[q90Queue] != committed[q90Queue] || acked[q90SourceVersion] != baselineVersion {
		t.Fatal("delivery ACK waited for full calculation or lost queue")
	}
	marker := composer.command(t, "marker")
	if marker["Rows"] != float64(1) || marker["RequestedVersion"] != target {
		t.Fatal("ACK did not persist the original target in Analytics")
	}
	marked := b.do(t, get)
	if marked.StatusCode != 200 || marked.json[q90State].(map[string]any)[q90Recalculating] != true || marked.json[q90Calculation].(map[string]any)[q90Recalculating] != true || marked.json[q90Calculation].(map[string]any)[q90CalcVersion] != baselineVersion {
		t.Fatal("real Analytics marker missing after ACK")
	}
	updated := b.do(t, put)
	if updated.StatusCode != 200 || updated.json["required_calc_version"] != target {
		t.Fatal("explicit repeat did not return original target")
	}
	q90Regular(t, updated, ready.ID, ready.Taxi, false)
	if repeated := source.command(t, q90Snapshot); !reflect.DeepEqual(acked, repeated) {
		t.Fatal("explicit repeat added a revision, queue generation, job or business effect")
	}
	source.command(t, "calculate")
	final := b.do(t, get)
	if final.StatusCode != 200 {
		t.Fatal("ready GET failed", final.StatusCode)
	}
	state := final.json[q90State].(map[string]any)
	calculation := final.json[q90Calculation].(map[string]any)
	if state[q90Recalculating] != false || calculation[q90Recalculating] != false || calculation[q90CalcVersion].(float64) < target {
		t.Fatal("ready GET has not applied target")
	}
	q90Regular(t, final, ready.ID, ready.Taxi, false)
	t.Log("Q90: real PUT/commit -> retryable 500 -> durable retry -> ACK before calculation -> same-target complete repeat -> ready GET; PostgreSQL + signed gRPC + real worker")
}

// Hand-computed independent oracle: the fixture has Cloud=29900 and Taxi=100000
// minor units. Excluding Cloud leaves one Taxi, including its actual opaque ID.
func q90Regular(t *testing.T, r response, excluded, taxi string, baseline bool) {
	t.Helper()
	if r.json["currency"] != "RUB" {
		t.Fatal("regular currency changed")
	}
	blocks, ok := r.json["blocks"].([]any)
	if !ok {
		t.Fatal("complete blocks missing")
	}
	for _, raw := range blocks {
		block := raw.(map[string]any)
		if block["id"] != q90Insight {
			continue
		}
		if block["status"] != "ready" {
			t.Fatal("regular status is not ready")
		}
		data, ok := block["data"].(map[string]any)
		if !ok {
			t.Fatal("ready regular block has no data")
		}
		want, count := float64(100000), 1
		description := "в месяц · 1 списание каждый месяц"
		type expectedRow struct {
			title string
			value float64
		}
		expected := map[string]expectedRow{taxi: {"Taxi", 100000}}
		if baseline {
			want, count = 129900, 2
			description = "в месяц · 2 списания каждый месяц"
			expected[excluded] = expectedRow{"Cloud", 29900}
		}
		items, ok := data["items"].([]any)
		if data[q90Value] != want || data["description"] != description || data["items_more"] != nil || !ok || len(items) != count {
			t.Fatal("regular amount/items differ from independent ledger")
		}
		for _, raw := range items {
			item := raw.(map[string]any)
			button, ok := item["button"].(map[string]any)
			if !ok {
				t.Fatal("regular row has no payment ID")
			}
			id, ok := button["payment_id"].(string)
			if !ok {
				t.Fatal("regular row has invalid payment ID")
			}
			row, ok := expected[id]
			if !ok || item["title"] != row.title || item[q90Value] != row.value || item["value_kind"] != "amount" {
				t.Fatal("regular row identity/amount differs from ledger")
			}
			delete(expected, id)
		}
		if len(expected) != 0 {
			t.Fatal("regular rows are incomplete")
		}
		return
	}
	t.Fatal("complete regular payments block missing")
}
