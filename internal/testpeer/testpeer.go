// Package testpeer is test infrastructure: in-process gRPC fakes of the user and
// assistant and statement services on bufconn, user JWT keys and stage service identity files.
// It is never wired into the serve command.
package testpeer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	identity "github.com/fin-assist/go-identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// Identity settings shared by the fakes and the BFF under test.
const (
	UserIssuer   = "https://user.test"
	UserAudience = "bank-bff.test"
	StageIssuer  = "https://stage.test"
	Target       = "passthrough:///peers"
	userKID      = "user-key-1"
)

// Keys signs user access tokens with an Ed25519 key published by GetSigningKeys.
type Keys struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}

// NewKeys generates a user signing key.
func NewKeys(t testing.TB) *Keys {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &Keys{private: priv, public: pub}
}

// JWKS is the public key set of GetSigningKeys.
func (k *Keys) JWKS() string {
	b, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "OKP", "crv": "Ed25519", "kid": userKID, "use": "sig", "alg": "EdDSA",
		"x": base64.RawURLEncoding.EncodeToString(k.public),
	}}})
	return string(b)
}

// Token signs an access JWT of sub valid until exp, with the given issuer and audience.
func (k *Keys) Token(sub string, now, exp time.Time, issuer, audience string) string {
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": userKID})
	claims, _ := json.Marshal(map[string]any{"iss": issuer, "aud": audience, "sub": sub, "iat": now.Unix(), "exp": exp.Unix()})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(k.private, []byte(unsigned)))
}

// Access signs a valid access token of sub for an hour.
func (k *Keys) Access(sub string) string {
	now := time.Now()
	return k.Token(sub, now, now.Add(time.Hour), UserIssuer, UserAudience)
}

// StageFiles are the stage identity files of bank-bff and the key verifying it.
type StageFiles struct {
	SigningKeyFile, PublicKeysFile string
	public                         ed25519.PublicKey
}

// NewStageFiles writes a bank-bff stage key and the stage JWKS into a temporary dir.
func NewStageFiles(t testing.TB) StageFiles {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := StageFiles{SigningKeyFile: filepath.Join(dir, "key.pem"), PublicKeysFile: filepath.Join(dir, "jwks.json"), public: pub}
	if err := os.WriteFile(files.SigningKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	set, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "OKP", "crv": "Ed25519", "kid": "bank-bff", "use": "sig", "alg": "EdDSA",
		"x": base64.RawURLEncoding.EncodeToString(pub),
	}}})
	if err := os.WriteFile(files.PublicKeysFile, set, 0o600); err != nil {
		t.Fatal(err)
	}
	return files
}

// Call is one received RPC with its verified caller and metadata.
type Call struct {
	Method string
	// Caller is the verified stage service name, empty when the token was rejected.
	Caller string
	MD     metadata.MD
}

// Peers is one in-process gRPC server playing user, assistant and statements. Handlers are the
// fake behaviour; unset methods answer UNIMPLEMENTED. Peers is safe for concurrent use.
type Peers struct {
	Auth       *Auth
	Consents   *Consents
	Profile    *Profile
	Assistant  *Assistant
	Statements *Statements
	Keys       *Keys

	listener *bufconn.Listener
	server   *grpc.Server
	verifier *identity.StageVerifier

	mu    sync.Mutex
	calls []Call
	// KeysUnavailable makes GetSigningKeys fail: user keys cannot be refreshed.
	KeysUnavailable atomic.Bool
}

// Start serves the fakes on bufconn until the test ends.
func Start(t testing.TB, keys *Keys, stage StageFiles) *Peers {
	t.Helper()
	verifier, err := identity.NewStageVerifier("stage", identity.VerifyOptions{Issuer: StageIssuer, Audience: "finassist-internal", Clock: time.Now},
		map[string]ed25519.PublicKey{"bank-bff": stage.public})
	if err != nil {
		t.Fatal(err)
	}
	p := &Peers{Auth: &Auth{}, Consents: &Consents{}, Profile: &Profile{}, Assistant: &Assistant{}, Statements: &Statements{}, Keys: keys,
		listener: bufconn.Listen(1 << 20), verifier: verifier}
	p.server = grpc.NewServer(grpc.ChainUnaryInterceptor(p.unary), grpc.ChainStreamInterceptor(p.stream))
	userv1.RegisterUserInternalServiceServer(p.server, signingKeys{keys: keys, peers: p})
	userv1.RegisterAuthServiceServer(p.server, p.Auth)
	userv1.RegisterConsentDocumentServiceServer(p.server, p.Consents)
	userv1.RegisterProfileServiceServer(p.server, p.Profile)
	assistantv1.RegisterAssistantServiceServer(p.server, p.Assistant)
	statementdatav1.RegisterStatementsServiceServer(p.server, p.Statements)
	go func() { _ = p.server.Serve(p.listener) }()
	t.Cleanup(p.server.Stop)
	return p
}

// ServeTCP also serves the fakes on a loopback TCP port and returns its gRPC target,
// for tests of the serve command, which dials real addresses.
func (p *Peers) ServeTCP(t testing.TB) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.server.Serve(l) }()
	return "dns:///" + l.Addr().String()
}

// DialOptions connect a BFF client to the fakes.
func (p *Peers) DialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return p.listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
}

// Calls returns the received calls of method (all calls for "").
func (p *Peers) Calls(method string) []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Call
	for _, c := range p.calls {
		if method == "" || c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (p *Peers) record(ctx context.Context, method string) {
	md, _ := metadata.FromIncomingContext(ctx)
	caller := ""
	if values := md.Get("x-service-authorization"); len(values) == 1 && len(values[0]) > len("Bearer ") {
		if name, err := p.verifier.VerifyService(ctx, values[0][len("Bearer "):]); err == nil {
			caller = name
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, Call{Method: method, Caller: caller, MD: md.Copy()})
}

func (p *Peers) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	p.record(ctx, info.FullMethod)
	return handler(ctx, req)
}

func (p *Peers) stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	p.record(ss.Context(), info.FullMethod)
	return handler(srv, ss)
}

type signingKeys struct {
	userv1.UnimplementedUserInternalServiceServer
	keys  *Keys
	peers *Peers
}

func (s signingKeys) GetSigningKeys(context.Context, *userv1.GetSigningKeysRequest) (*userv1.GetSigningKeysResponse, error) {
	if s.peers != nil && s.peers.KeysUnavailable.Load() {
		return nil, errors.New("user is down") // no registry reason: a failed key fetch
	}
	return &userv1.GetSigningKeysResponse{JwksJson: s.keys.JWKS()}, nil
}
