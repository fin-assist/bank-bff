// Command bank-bff runs the stateless HTTP backend for frontend.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/fin-assist/bank-bff/internal/app"
	"github.com/fin-assist/bank-bff/internal/config"
)

// version is set by the image build.
var version = "dev"

type usageError string

func (e usageError) Error() string { return string(e) }

const usage = "usage: bank-bff serve"

func main() { os.Exit(mainExit(os.Args[1:], os.Stderr)) }

func mainExit(args []string, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return execute(ctx, args, stderr)
}

func execute(ctx context.Context, args []string, stderr io.Writer) int {
	if err := run(ctx, args, stderr); err != nil {
		var usage usageError
		if errors.As(err, &usage) {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
		slog.New(slog.NewJSONHandler(stderr, nil)).ErrorContext(ctx, "startup or shutdown failed", "error", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
	if len(args) != 1 || args[0] != "serve" {
		return usageError(usage)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var handler slog.Handler = slog.NewJSONHandler(stderr, opts)
	if cfg.LogFormat == "text" {
		handler = slog.NewTextHandler(stderr, opts)
	}
	logger := slog.New(handler).With("service", cfg.Service, "version", version)
	return app.Run(ctx, cfg, logger)
}
