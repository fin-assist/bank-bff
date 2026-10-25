// Package config reads the stateless BFF configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"
)

// Config holds listener, logging and graceful shutdown settings.
type Config struct {
	Service, Env, PublicAddr, HTTPAddr, LogFormat string
	LogLevel                                      slog.Level
	ShutdownDelay, ShutdownTimeout                time.Duration
}

// Var declares one supported setting and its documented default.
type Var struct{ Name, Default, Description string }

// Vars is the source of the configuration table in docs/config.md.
var Vars = []Var{
	{"SERVICE_NAME", "bank-bff", "Имя сервиса в логах"},
	{"ENV", "prod", "dev, stage или prod"},
	{"PUBLIC_ADDR", ":8080", "Публичный HTTP listener; бизнес-маршруты пока не зарегистрированы"},
	{"HTTP_ADDR", ":9091", "Отдельный operational listener: /healthz, /readyz, /metrics"},
	{"LOG_LEVEL", "info", "debug, info, warn или error"},
	{"LOG_FORMAT", "json", "json или text"},
	{"SHUTDOWN_DELAY", "5s", "Пауза после снятия readiness для балансировщика"},
	{"SHUTDOWN_TIMEOUT", "20s", "Общий предел graceful shutdown обоих listeners"},
}

// Load reads and validates all settings. Invalid configuration prevents startup.
func Load() (Config, error) { return load(os.Getenv) }

func load(getenv func(string) string) (Config, error) {
	values := make(map[string]string, len(Vars))
	for _, v := range Vars {
		values[v.Name] = v.Default
		if value := getenv(v.Name); value != "" {
			values[v.Name] = value
		}
	}
	cfg := Config{Service: values["SERVICE_NAME"], Env: values["ENV"], PublicAddr: values["PUBLIC_ADDR"], HTTPAddr: values["HTTP_ADDR"], LogFormat: values["LOG_FORMAT"]}
	var errs []error
	if cfg.Env != "dev" && cfg.Env != "stage" && cfg.Env != "prod" {
		errs = append(errs, errors.New("ENV must be dev, stage or prod"))
	}
	if cfg.LogFormat != "json" && cfg.LogFormat != "text" {
		errs = append(errs, errors.New("LOG_FORMAT must be json or text"))
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(values["LOG_LEVEL"])); err != nil {
		errs = append(errs, errors.New("LOG_LEVEL must be debug, info, warn or error"))
	}
	for _, key := range []string{"PUBLIC_ADDR", "HTTP_ADDR"} {
		_, port, err := net.SplitHostPort(values[key])
		n, portErr := strconv.Atoi(port)
		if err != nil || portErr != nil || n < 0 || n > 65535 {
			errs = append(errs, fmt.Errorf("%s must be a host:port address with a numeric port", key))
		}
	}
	var err error
	cfg.ShutdownDelay, err = time.ParseDuration(values["SHUTDOWN_DELAY"])
	if err != nil || cfg.ShutdownDelay < 0 || cfg.ShutdownDelay > time.Minute {
		errs = append(errs, errors.New("SHUTDOWN_DELAY must be between 0s and 1m"))
	}
	cfg.ShutdownTimeout, err = time.ParseDuration(values["SHUTDOWN_TIMEOUT"])
	if err != nil || cfg.ShutdownTimeout <= 0 || cfg.ShutdownTimeout > 5*time.Minute {
		errs = append(errs, errors.New("SHUTDOWN_TIMEOUT must be positive and at most 5m"))
	}
	return cfg, errors.Join(errs...)
}
