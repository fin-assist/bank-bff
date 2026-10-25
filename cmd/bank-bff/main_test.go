package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestUnitCommandUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"migrate", "up"}, {"serve", "extra"}} {
		var output bytes.Buffer
		if code := mainExit(args, &output); code != 2 || !strings.Contains(output.String(), usage) {
			t.Fatalf("%v: %d %s", args, code, output.String())
		}
	}
}

func TestUnitInvalidConfigPreventsStartup(t *testing.T) {
	t.Setenv("LOG_FORMAT", "bad")
	var output bytes.Buffer
	if code := execute(context.Background(), []string{"serve"}, &output); code != 1 || !strings.Contains(output.String(), "LOG_FORMAT") {
		t.Fatalf("%d %s", code, output.String())
	}
}
