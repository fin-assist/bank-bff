package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fin-assist/bank-bff/internal/rest"
)

// apiValues are the settings stage and prod require for the public API.
var apiValues = map[string]string{"USER_GRPC_ADDR": "dns:///user:9090", "ASSISTANT_GRPC_ADDR": "dns:///assistant:9090",
	"AUTH_USER_ISSUER": "https://user", "AUTH_USER_AUDIENCE": "bank-bff", "CLIENT_MIN_VERSIONS": "android=0.2.5"}

func TestUnitDefaultsAndOverrides(t *testing.T) {
	cfg, err := load(func(k string) string { return apiValues[k] })
	if err != nil || cfg.Service != "bank-bff" || cfg.Env != "prod" || cfg.PublicAddr != ":8080" || cfg.HTTPAddr != ":9091" || cfg.LogFormat != "json" || cfg.ShutdownDelay != 5*time.Second || cfg.ShutdownTimeout != 20*time.Second {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	a := cfg.API
	if !a.Enabled || a.BodyLimit != 65536 || a.Maintenance.Enabled || a.Maintenance.RetryAfter != 5*time.Minute || a.Streams.Max != 2000 || a.Streams.PerIP != 20 ||
		a.Streams.RetryAfter != 10*time.Second || a.MinVersions["android"] != (rest.Version{"0", "2", "5"}) || a.UserAddr != "dns:///user:9090" {
		t.Fatalf("API defaults: %+v", a)
	}
	for _, env := range []string{"dev", "stage", "prod"} {
		values := map[string]string{"ENV": env, "SERVICE_NAME": "bff-test", "PUBLIC_ADDR": "127.0.0.1:0", "HTTP_ADDR": "[::1]:65535", "LOG_LEVEL": "debug", "LOG_FORMAT": "text", "SHUTDOWN_DELAY": "0s", "SHUTDOWN_TIMEOUT": "1ms"}
		for k, v := range apiValues {
			values[k] = v
		}
		cfg, err := load(func(k string) string { return values[k] })
		if err != nil || cfg.Env != env || cfg.Service != "bff-test" || cfg.LogFormat != "text" || cfg.LogLevel.String() != "DEBUG" || cfg.ShutdownDelay != 0 || cfg.ShutdownTimeout != time.Millisecond {
			t.Fatalf("overrides: %+v %v", cfg, err)
		}
		if cfg.API.Enabled != (env != "dev") {
			t.Fatalf("%s: API enabled %v", env, cfg.API.Enabled)
		}
	}
	values := map[string]string{"ENV": "dev", "MAINTENANCE": "on", "MAINTENANCE_UNTIL": "2026-10-16T03:00:00+03:00", "MAINTENANCE_RETRY_AFTER": "30s",
		"JSON_BODY_LIMIT": "1048576", "SSE_MAX_STREAMS": "5", "SSE_MAX_STREAMS_PER_IP": "5", "SSE_BUSY_RETRY_AFTER": "5m",
		"CLIENT_MIN_VERSIONS": "android=0.10.0, ios=1.0.0", "SERVICE_TOKEN_FILE": "/t", "AUTH_KUBERNETES_ISSUER": "i", "AUTH_KUBERNETES_JWKS_URL": "u",
		"AUTH_KUBERNETES_CA_FILE": "c", "AUTH_KUBERNETES_API_TOKEN_FILE": "a", "AUTH_KUBERNETES_NAMESPACE": "n", "AUTH_STAGE_ISSUER": "si",
		"AUTH_STAGE_SIGNING_KEY_FILE": "sk", "AUTH_STAGE_PUBLIC_KEYS_FILE": "sp"}
	cfg, err = load(func(k string) string { return values[k] })
	a = cfg.API
	if err != nil || a.Enabled || !a.Maintenance.Enabled || a.Maintenance.Until.IsZero() || a.Maintenance.RetryAfter != 30*time.Second || a.BodyLimit != 1<<20 ||
		a.Streams.Max != 5 || a.MinVersions["ios"] != (rest.Version{"1", "0", "0"}) || a.Kubernetes != (Kubernetes{"/t", "i", "u", "c", "a", "n"}) || a.Stage != (Stage{"si", "sk", "sp"}) {
		t.Fatalf("API overrides: %+v %v", a, err)
	}
}

func TestUnitStageAndProdRequireTheAPISettings(t *testing.T) {
	for key := range apiValues {
		for _, env := range []string{"stage", "prod"} {
			_, err := load(func(k string) string {
				if k == "ENV" {
					return env
				}
				if k == key {
					return " "
				}
				return apiValues[k]
			})
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("%s without %s: %v", env, key, err)
			}
		}
	}
}

func TestUnitInvalidSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"ENV", "invalid"}, {"LOG_FORMAT", "xml"}, {"LOG_LEVEL", "invalid"},
		{"PUBLIC_ADDR", "no-port"}, {"PUBLIC_ADDR", ":http"}, {"PUBLIC_ADDR", ":-1"}, {"HTTP_ADDR", ":65536"},
		{"SHUTDOWN_DELAY", "bad"}, {"SHUTDOWN_DELAY", "-1s"}, {"SHUTDOWN_DELAY", "61s"},
		{"SHUTDOWN_TIMEOUT", "bad"}, {"SHUTDOWN_TIMEOUT", "0s"}, {"SHUTDOWN_TIMEOUT", "-1s"}, {"SHUTDOWN_TIMEOUT", "301s"},
		{"CLIENT_MIN_VERSIONS", "android"}, {"CLIENT_MIN_VERSIONS", "Android=0.1.0"}, {"CLIENT_MIN_VERSIONS", "android=0.1"}, {"CLIENT_MIN_VERSIONS", "android=0.1.0,android=0.2.0"},
		{"MAINTENANCE", "yes"}, {"MAINTENANCE_UNTIL", "tomorrow"}, {"MAINTENANCE_RETRY_AFTER", "0s"}, {"MAINTENANCE_RETRY_AFTER", "25h"}, {"MAINTENANCE_RETRY_AFTER", "x"},
		{"JSON_BODY_LIMIT", "16384"}, {"JSON_BODY_LIMIT", "2097152"}, {"JSON_BODY_LIMIT", "x"},
		{"SSE_MAX_STREAMS", "0"}, {"SSE_MAX_STREAMS", "x"}, {"SSE_MAX_STREAMS_PER_IP", "0"}, {"SSE_MAX_STREAMS_PER_IP", "2001"},
		{"SSE_BUSY_RETRY_AFTER", "500ms"}, {"SSE_BUSY_RETRY_AFTER", "6m"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			_, err := load(func(key string) string {
				if key == tc.key {
					return tc.value
				}
				return apiValues[key]
			})
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("want error for %s: %v", tc.key, err)
			}
		})
	}
}

func TestUnitDocIsCurrent(t *testing.T) {
	var table strings.Builder
	table.WriteString("| Переменная | По умолчанию | Что |\n|---|---|---|\n")
	names := map[string]bool{}
	for _, v := range Vars {
		if names[v.Name] {
			t.Fatalf("duplicate setting %s", v.Name)
		}
		names[v.Name] = true
		def := "—"
		if v.Default != "" {
			def = "`" + v.Default + "`"
		}
		fmt.Fprintf(&table, "| `%s` | %s | %s |\n", v.Name, def, v.Description)
	}
	path := filepath.Join("..", "..", "docs", "config.md")
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), table.String()) {
		t.Fatalf("docs/config.md table is stale; want:\n%s", table.String())
	}
	read := map[string]bool{}
	_, err = load(func(name string) string { read[name] = true; return apiValues[name] })
	if err != nil {
		t.Fatal(err)
	}
	for name := range names {
		if !read[name] {
			t.Errorf("declared but unread: %s", name)
		}
	}
	for name := range read {
		if !names[name] {
			t.Errorf("read but undeclared: %s", name)
		}
	}
}
