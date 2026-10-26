// Package config reads the stateless BFF configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fin-assist/bank-bff/internal/rest"
)

// Config holds listener, logging, shutdown and public API settings.
type Config struct {
	Service, Env, PublicAddr, HTTPAddr, LogFormat string
	LogLevel                                      slog.Level
	ShutdownDelay, ShutdownTimeout                time.Duration
	// API configures the public REST/SSE API; it is served only in stage and prod.
	API API
}

// API holds the settings of the public business API (FIN-268).
type API struct {
	// Enabled is true in stage and prod. ENV=dev serves no business route: the
	// identity assembly refuses dev, and there is no development bypass.
	Enabled bool
	// UserAddr, AssistantAddr and StatementsAddr are the gRPC targets of the peers.
	UserAddr, AssistantAddr, StatementsAddr string
	// UserIssuer and UserAudience pin user access JWTs.
	UserIssuer, UserAudience string
	// Kubernetes is the prod service identity; Stage the stage one.
	Kubernetes Kubernetes
	Stage      Stage
	// MinVersions is the minimum supported client version per platform.
	MinVersions map[string]rest.Version
	// Maintenance turns every /v1 request into 503 MAINTENANCE.
	Maintenance rest.Maintenance
	// BodyLimit is the JSON body budget in bytes.
	BodyLimit int64
	// Streams bound concurrent SSE streams.
	Streams rest.StreamLimits
	// Uploads bounds long POST requests independently from SSE.
	Uploads rest.StreamLimits
	// TrustedProxies are the networks whose X-Forwarded-For names the client address.
	TrustedProxies []netip.Prefix
}

// Kubernetes are the prod identity files and endpoints; empty values take the
// go-identity defaults.
type Kubernetes struct {
	TokenFile, Issuer, JWKSURL, CAFile, APITokenFile, Namespace string
}

// Stage are the stage identity files.
type Stage struct {
	Issuer, SigningKeyFile, PublicKeysFile string
}

// Var declares one supported setting and its documented default.
type Var struct{ Name, Default, Description string }

// Vars is the source of the configuration table in docs/config.md.
var Vars = []Var{
	{"SERVICE_NAME", "bank-bff", "Имя сервиса в логах и ServiceAccount для исходящих вызовов"},
	{"ENV", "prod", "dev, stage или prod; в dev публичный API не регистрируется"},
	{"PUBLIC_ADDR", ":8080", "Публичный HTTP listener REST/SSE"},
	{"HTTP_ADDR", ":9091", "Отдельный operational listener: /healthz, /readyz, /metrics"},
	{"LOG_LEVEL", "info", "debug, info, warn или error"},
	{"LOG_FORMAT", "json", "json или text"},
	{"SHUTDOWN_DELAY", "5s", "Пауза после снятия readiness для балансировщика"},
	{"SHUTDOWN_TIMEOUT", "20s", "Общий предел graceful shutdown обоих listeners"},
	{"USER_GRPC_ADDR", "", "gRPC-адрес сервиса user (`dns:///user.finassist.svc.cluster.local:9090`); обязателен в stage/prod"},
	{"ASSISTANT_GRPC_ADDR", "", "gRPC-адрес сервиса assistant; обязателен в stage/prod"},
	{"STATEMENTS_GRPC_ADDR", "", "gRPC-адрес bank-statement-data для шести RPC импорта; обязателен в stage/prod"},
	{"AUTH_USER_ISSUER", "", "`iss` access JWT пользователя, как у user; обязателен в stage/prod"},
	{"AUTH_USER_AUDIENCE", "", "`aud` access JWT пользователя, как у user; обязателен в stage/prod"},
	{"SERVICE_TOKEN_FILE", "", "prod: projected ServiceAccount token с audience finassist-internal"},
	{"AUTH_KUBERNETES_ISSUER", "", "prod: issuer ServiceAccount; пусто — значение go-identity"},
	{"AUTH_KUBERNETES_JWKS_URL", "", "prod: JWKS API-сервера; пусто — значение go-identity"},
	{"AUTH_KUBERNETES_CA_FILE", "", "prod: CA для JWKS API-сервера; пусто — значение go-identity"},
	{"AUTH_KUBERNETES_API_TOKEN_FILE", "", "prod: токен пода для запроса JWKS; пусто — значение go-identity"},
	{"AUTH_KUBERNETES_NAMESPACE", "", "prod: namespace вызывающих; пусто — значение go-identity"},
	{"AUTH_STAGE_ISSUER", "", "stage: issuer сервисных JWT стенда"},
	{"AUTH_STAGE_SIGNING_KEY_FILE", "", "stage: PKCS#8 Ed25519 ключ этого сервиса"},
	{"AUTH_STAGE_PUBLIC_KEYS_FILE", "", "stage: JWKS ключей сервисов стенда"},
	{"CLIENT_MIN_VERSIONS", "", "Минимальные версии клиента: `android=0.2.5,ios=0.1.0`; обязательна в stage/prod"},
	{"MAINTENANCE", "off", "on — все запросы /v1 получают 503 MAINTENANCE"},
	{"MAINTENANCE_UNTIL", "", "Ожидаемое окончание техработ, RFC 3339; пусто — неизвестно"},
	{"MAINTENANCE_RETRY_AFTER", "5m", "Retry-After техработ, пока окончание неизвестно или прошло; 1s–24h"},
	{"JSON_BODY_LIMIT", "65536", "Предел JSON-тела запроса в байтах; 65536–1048576"},
	{"UPLOAD_MAX_STREAMS", "16", "Предел одновременных загрузок процесса; отдельный от SSE"},
	{"UPLOAD_MAX_STREAMS_PER_IP", "2", "Предел загрузок с одного адреса клиента"},
	{"UPLOAD_BUSY_RETRY_AFTER", "1s", "Retry-After локального отказа загрузки; 1s–5m, original-request retryable=false"},
	{"SSE_MAX_STREAMS", "2000", "Предел одновременных SSE-потоков процесса"},
	{"SSE_MAX_STREAMS_PER_IP", "20", "Предел одновременных SSE-потоков с одного адреса клиента"},
	{"SSE_BUSY_RETRY_AFTER", "10s", "Retry-After отказа 429 по пределу SSE-потоков; 1s–5m"},
	{"TRUSTED_PROXIES", "", "CIDR доверенных прокси (nginx стенда, ingress) через запятую; только от них адрес клиента берётся из X-Forwarded-For; пусто — адрес TCP-пира"},
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
	api, apiErrs := loadAPI(cfg.Env, values)
	cfg.API = api
	errs = append(errs, apiErrs...)
	return cfg, errors.Join(errs...)
}

func loadAPI(env string, v map[string]string) (API, []error) {
	api := API{
		Enabled: env == "stage" || env == "prod", UserAddr: v["USER_GRPC_ADDR"], AssistantAddr: v["ASSISTANT_GRPC_ADDR"], StatementsAddr: v["STATEMENTS_GRPC_ADDR"],
		UserIssuer: v["AUTH_USER_ISSUER"], UserAudience: v["AUTH_USER_AUDIENCE"],
		Kubernetes: Kubernetes{TokenFile: v["SERVICE_TOKEN_FILE"], Issuer: v["AUTH_KUBERNETES_ISSUER"], JWKSURL: v["AUTH_KUBERNETES_JWKS_URL"],
			CAFile: v["AUTH_KUBERNETES_CA_FILE"], APITokenFile: v["AUTH_KUBERNETES_API_TOKEN_FILE"], Namespace: v["AUTH_KUBERNETES_NAMESPACE"]},
		Stage: Stage{Issuer: v["AUTH_STAGE_ISSUER"], SigningKeyFile: v["AUTH_STAGE_SIGNING_KEY_FILE"], PublicKeysFile: v["AUTH_STAGE_PUBLIC_KEYS_FILE"]},
	}
	var errs []error
	minVersions, err := parseMinVersions(v["CLIENT_MIN_VERSIONS"])
	if err != nil {
		errs = append(errs, err)
	}
	api.MinVersions = minVersions
	switch v["MAINTENANCE"] {
	case "on":
		api.Maintenance.Enabled = true
	case "off":
	default:
		errs = append(errs, errors.New("MAINTENANCE must be on or off"))
	}
	if until := v["MAINTENANCE_UNTIL"]; until != "" {
		t, err := time.Parse(time.RFC3339, until)
		if err != nil {
			errs = append(errs, errors.New("MAINTENANCE_UNTIL must be an RFC 3339 moment"))
		}
		api.Maintenance.Until = t
	}
	api.Maintenance.RetryAfter, err = time.ParseDuration(v["MAINTENANCE_RETRY_AFTER"])
	if err != nil || api.Maintenance.RetryAfter < time.Second || api.Maintenance.RetryAfter > 24*time.Hour {
		errs = append(errs, errors.New("MAINTENANCE_RETRY_AFTER must be between 1s and 24h"))
	}
	api.BodyLimit, err = strconv.ParseInt(v["JSON_BODY_LIMIT"], 10, 64)
	if err != nil || api.BodyLimit < rest.MinBodyLimit || api.BodyLimit > 1<<20 {
		errs = append(errs, errors.New("JSON_BODY_LIMIT must be between 65536 and 1048576"))
	}
	api.Streams.Max, err = strconv.Atoi(v["SSE_MAX_STREAMS"])
	if err != nil || api.Streams.Max < 1 || api.Streams.Max > 100000 {
		errs = append(errs, errors.New("SSE_MAX_STREAMS must be between 1 and 100000"))
	}
	api.Streams.PerIP, err = strconv.Atoi(v["SSE_MAX_STREAMS_PER_IP"])
	if err != nil || api.Streams.PerIP < 1 || api.Streams.PerIP > api.Streams.Max {
		errs = append(errs, errors.New("SSE_MAX_STREAMS_PER_IP must be between 1 and SSE_MAX_STREAMS"))
	}
	api.Streams.RetryAfter, err = time.ParseDuration(v["SSE_BUSY_RETRY_AFTER"])
	if err != nil || api.Streams.RetryAfter < time.Second || api.Streams.RetryAfter > 5*time.Minute {
		errs = append(errs, errors.New("SSE_BUSY_RETRY_AFTER must be between 1s and 5m"))
	}
	api.Uploads.Max, err = strconv.Atoi(v["UPLOAD_MAX_STREAMS"])
	if err != nil || api.Uploads.Max < 1 || api.Uploads.Max > 100000 {
		errs = append(errs, errors.New("UPLOAD_MAX_STREAMS must be between 1 and 100000"))
	}
	api.Uploads.PerIP, err = strconv.Atoi(v["UPLOAD_MAX_STREAMS_PER_IP"])
	if err != nil || api.Uploads.PerIP < 1 || api.Uploads.PerIP > api.Uploads.Max {
		errs = append(errs, errors.New("UPLOAD_MAX_STREAMS_PER_IP must be between 1 and UPLOAD_MAX_STREAMS"))
	}
	api.Uploads.RetryAfter, err = time.ParseDuration(v["UPLOAD_BUSY_RETRY_AFTER"])
	if err != nil || api.Uploads.RetryAfter < time.Second || api.Uploads.RetryAfter > 5*time.Minute {
		errs = append(errs, errors.New("UPLOAD_BUSY_RETRY_AFTER must be between 1s and 5m"))
	}
	for _, part := range strings.Split(v["TRUSTED_PROXIES"], ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			errs = append(errs, errors.New("TRUSTED_PROXIES must be comma-separated CIDR networks"))
			break
		}
		api.TrustedProxies = append(api.TrustedProxies, prefix.Masked())
	}
	if !api.Enabled {
		return api, errs
	}
	for _, key := range []string{"USER_GRPC_ADDR", "ASSISTANT_GRPC_ADDR", "STATEMENTS_GRPC_ADDR", "AUTH_USER_ISSUER", "AUTH_USER_AUDIENCE", "CLIENT_MIN_VERSIONS"} {
		if strings.TrimSpace(v[key]) == "" {
			errs = append(errs, fmt.Errorf("%s is required in %s", key, env))
		}
	}
	return api, errs
}

// parseMinVersions reads "android=0.2.5,ios=0.1.0".
func parseMinVersions(s string) (map[string]rest.Version, error) {
	out := map[string]rest.Version{}
	if s == "" {
		return out, nil
	}
	invalid := errors.New("CLIENT_MIN_VERSIONS must be platform=MAJOR.MINOR.PATCH, separated by commas")
	for _, item := range strings.Split(s, ",") {
		platform, version, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok || !rest.ValidPlatform(platform) {
			return nil, invalid
		}
		if _, dup := out[platform]; dup {
			return nil, invalid
		}
		v, err := rest.ParseVersion(version)
		if err != nil {
			return nil, invalid
		}
		out[platform] = v
	}
	return out, nil
}
