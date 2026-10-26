package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"github.com/fin-assist/go-grpc/client"
	identity "github.com/fin-assist/go-identity"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"

	"github.com/fin-assist/bank-bff/internal/apispec"
	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/endpoint"
	"github.com/fin-assist/bank-bff/internal/rest"
)

// Reads and methods idempotent by construction that the client may retry on
// UNAVAILABLE (grpc-api.md 9). Methods with idempotency-key are retried by the app.
var (
	userRetry = []string{
		userv1.UserInternalService_GetSigningKeys_FullMethodName[1:],
		userv1.ConsentDocumentService_GetConsentDocument_FullMethodName[1:],
		userv1.ProfileService_GetProfile_FullMethodName[1:],
	}
	assistantRetry = []string{
		assistantv1.AssistantService_ListMessages_FullMethodName[1:],
		assistantv1.AssistantService_GetLimit_FullMethodName[1:],
	}
)

type publicAPI struct {
	handler    http.Handler
	identity   *identity.Assembly
	user       *grpc.ClientConn
	assistant  *grpc.ClientConn
	statements *grpc.ClientConn
	// streams is ended on shutdown, so draining does not wait for SSE until timeout.
	streams *rest.StreamLimiter
	uploads *rest.StreamLimiter
}

// newPublicAPI builds identity, peer connections and the routes. Nothing is fetched
// and no goroutine is started; a failure releases everything already built.
func newPublicAPI(cfg config.Config, log *slog.Logger, reg prometheus.Registerer, o options) (_ *publicAPI, err error) {
	api := &publicAPI{}
	defer func() {
		if err != nil {
			api.close(log)
		}
	}()
	spec, err := apispec.Load(context.Background())
	if err != nil {
		return nil, err
	}
	clientMetrics, err := client.NewMetrics(reg)
	if err != nil {
		return nil, err
	}
	a := cfg.API
	api.identity, err = identity.NewAssembly(identity.AssemblyOptions{
		Env: cfg.Env, Service: cfg.Service, UserIssuer: a.UserIssuer, UserAudience: a.UserAudience,
		DialUser: func(tokens identity.TokenSource) (*grpc.ClientConn, error) {
			return client.Dial(a.UserAddr, client.Options{Peer: "user", Tokens: tokens, Metrics: clientMetrics,
				Retry: userRetry[:1], DialOptions: o.dial})
		},
		Kubernetes: identity.KubernetesOptions{TokenFile: a.Kubernetes.TokenFile, Issuer: a.Kubernetes.Issuer,
			JWKSURL: a.Kubernetes.JWKSURL, CAFile: a.Kubernetes.CAFile, APITokenFile: a.Kubernetes.APITokenFile, Namespace: a.Kubernetes.Namespace},
		Stage:      identity.StageKeyFiles{Issuer: a.Stage.Issuer, SigningKeyFile: a.Stage.SigningKeyFile, PublicKeysFile: a.Stage.PublicKeysFile},
		Clock:      o.clock,
		Registerer: reg,
	})
	if err != nil {
		return nil, err
	}
	dial := func(target, peer string, retry []string) (*grpc.ClientConn, error) {
		// Interceptor order: go-grpc starts fresh metadata (service token, x-request-id),
		// identity adds only the verified user token, rest adds the allowlisted hints.
		return client.Dial(target, client.Options{Peer: peer, Tokens: api.identity, Metrics: clientMetrics, Retry: retry,
			DialOptions: append([]grpc.DialOption{
				grpc.WithChainUnaryInterceptor(identity.UnaryClient(api.identity), rest.UnaryHints()),
				grpc.WithChainStreamInterceptor(identity.StreamClient(api.identity), rest.StreamHints()),
			}, o.dial...)})
	}
	if api.user, err = dial(a.UserAddr, "user", userRetry[1:]); err != nil {
		return nil, err
	}
	if api.assistant, err = dial(a.AssistantAddr, "assistant", assistantRetry); err != nil {
		return nil, err
	}
	if api.statements, err = dial(a.StatementsAddr, "bank-statement-data", []string{
		statementdatav1.StatementsService_GetImportConfig_FullMethodName[1:],
		statementdatav1.StatementsService_ListUploads_FullMethodName[1:],
		statementdatav1.StatementsService_ListUnreadLines_FullMethodName[1:],
	}); err != nil {
		return nil, err
	}
	metrics, err := rest.NewMetrics(reg)
	if err != nil {
		return nil, err
	}
	auth, err := identity.NewHTTP(api.identity, func(w http.ResponseWriter, _ *http.Request, err error) {
		// INVALID_TOKEN -> 401; a token that could not be judged (no fresh keys) -> 500,
		// so a key outage never signs the user out.
		metrics.FromGRPC(err, map[int]bool{http.StatusUnauthorized: true}).Write(w)
	})
	if err != nil {
		return nil, err
	}
	api.streams = rest.NewStreamLimiter(a.Streams)
	api.uploads = rest.NewStreamLimiter(a.Uploads)
	deps := endpoint.Deps{
		Auth: userv1.NewAuthServiceClient(api.user), Consents: userv1.NewConsentDocumentServiceClient(api.user),
		Profile: userv1.NewProfileServiceClient(api.user), Assistant: assistantv1.NewAssistantServiceClient(api.assistant),
		Statements: statementdatav1.NewStatementsServiceClient(api.statements),
		Heartbeat:  o.heartbeat, AnswerStream: o.answerStream, PhoneStream: o.phoneStream,
	}
	api.handler, err = rest.Handler(rest.Config{
		Spec: spec, Gates: rest.Gates{Maintenance: a.Maintenance, MinVersions: a.MinVersions, Clock: o.clock},
		BodyLimit: a.BodyLimit, Authenticate: auth.Middleware, Metrics: metrics,
		Streams: api.streams, Uploads: api.uploads, Log: log, Clock: o.clock, TrustedProxies: a.TrustedProxies,
	}, deps.Routes())
	if err != nil {
		return nil, err
	}
	return api, nil
}

// refreshKeys runs the identity key refresh (user JWKS, Kubernetes keys in prod)
// for the lifetime of the listeners; before it, keys load lazily on first use.
func (a *publicAPI) refreshKeys(ctx context.Context) { _ = a.identity.Run(ctx) }

func (a *publicAPI) close(log *slog.Logger) {
	var err error
	for _, conn := range []*grpc.ClientConn{a.user, a.assistant, a.statements} {
		if conn != nil {
			err = errors.Join(err, conn.Close())
		}
	}
	if a.identity != nil {
		err = errors.Join(err, a.identity.Close())
	}
	if err != nil {
		log.Warn("peer connections closed with an error")
	}
}
