package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnitDefaultsAndOverrides(t *testing.T) {
	cfg, err := load(func(string) string { return "" })
	if err != nil || cfg.Service != "bank-bff" || cfg.Env != "prod" || cfg.PublicAddr != ":8080" || cfg.HTTPAddr != ":9091" || cfg.LogFormat != "json" || cfg.ShutdownDelay != 5*time.Second || cfg.ShutdownTimeout != 20*time.Second {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	for _, env := range []string{"dev", "stage", "prod"} {
		values := map[string]string{"ENV": env, "SERVICE_NAME": "bff-test", "PUBLIC_ADDR": "127.0.0.1:0", "HTTP_ADDR": "[::1]:65535", "LOG_LEVEL": "debug", "LOG_FORMAT": "text", "SHUTDOWN_DELAY": "0s", "SHUTDOWN_TIMEOUT": "1ms"}
		cfg, err := load(func(k string) string { return values[k] })
		if err != nil || cfg.Env != env || cfg.Service != "bff-test" || cfg.LogFormat != "text" || cfg.LogLevel.String() != "DEBUG" || cfg.ShutdownDelay != 0 || cfg.ShutdownTimeout != time.Millisecond {
			t.Fatalf("overrides: %+v %v", cfg, err)
		}
	}
}

func TestUnitInvalidSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"ENV", "invalid"}, {"LOG_FORMAT", "xml"}, {"LOG_LEVEL", "invalid"},
		{"PUBLIC_ADDR", "no-port"}, {"PUBLIC_ADDR", ":http"}, {"PUBLIC_ADDR", ":-1"}, {"HTTP_ADDR", ":65536"},
		{"SHUTDOWN_DELAY", "bad"}, {"SHUTDOWN_DELAY", "-1s"}, {"SHUTDOWN_DELAY", "61s"},
		{"SHUTDOWN_TIMEOUT", "bad"}, {"SHUTDOWN_TIMEOUT", "0s"}, {"SHUTDOWN_TIMEOUT", "-1s"}, {"SHUTDOWN_TIMEOUT", "301s"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			_, err := load(func(key string) string {
				if key == tc.key {
					return tc.value
				}
				return ""
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
		fmt.Fprintf(&table, "| `%s` | `%s` | %s |\n", v.Name, v.Default, v.Description)
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
	_, err = load(func(name string) string { read[name] = true; return "" })
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
