package endpoint

import (
	"context"
	"net/http"
	"time"

	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"

	"github.com/fin-assist/bank-bff/internal/rest"
)

// PhoneVerification is the 200 of POST /v1/auth/phone.
type PhoneVerification struct {
	VerificationToken string `json:"verification_token"`
	CallbackNumber    string `json:"callback_number"`
	ExpiresAt         string `json:"expires_at"`
	ExpiresIn         *int32 `json:"expires_in,omitempty"`
}

func (d Deps) startPhone(c *rest.Call) {
	var in struct {
		Phone string `json:"phone"`
	}
	if !decode(c, &in) {
		return
	}
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Auth.StartPhoneVerification(ctx, &userv1.StartPhoneVerificationRequest{Phone: in.Phone})
	if err != nil {
		c.Fail(err)
		return
	}
	// expires_in is recomputed by user at every send, replay included (grpc-api.md 6).
	rest.WriteJSON(c.W, http.StatusOK, PhoneVerification{
		VerificationToken: resp.GetVerificationToken(), CallbackNumber: resp.GetCallbackNumber(),
		ExpiresAt: moment(resp.GetExpiresAt(), zone("")), ExpiresIn: resp.ExpiresIn,
	})
}

// PhoneStatus is the status event of GET /v1/auth/phone/events.
type PhoneStatus struct {
	Status            string  `json:"status"`
	IsNewUser         *bool   `json:"is_new_user,omitempty"`
	AccessToken       *string `json:"access_token,omitempty"`
	RefreshToken      *string `json:"refresh_token,omitempty"`
	RegistrationToken *string `json:"registration_token,omitempty"`
}

// maxVerificationToken bounds the forwarded header; the real token is 43 characters.
const maxVerificationToken = 512

// phoneStatus maps one message; false for a status bank-bff cannot name, terminal for
// verified and expired.
func phoneStatus(m *userv1.WatchPhoneVerificationResponse) (event PhoneStatus, terminal, ok bool) {
	status, ok := enumCode(m.GetStatus())
	if !ok {
		return PhoneStatus{}, false, false
	}
	event = PhoneStatus{Status: status}
	if m.GetStatus() == userv1.VerificationStatus_VERIFICATION_STATUS_VERIFIED {
		event.IsNewUser = m.IsNewUser
		if m.Tokens != nil {
			access, refresh := m.Tokens.GetAccessToken(), m.Tokens.GetRefreshToken()
			event.AccessToken, event.RefreshToken = &access, &refresh
		}
		event.RegistrationToken = m.RegistrationToken
	}
	terminal = m.GetStatus() != userv1.VerificationStatus_VERIFICATION_STATUS_PENDING
	return event, terminal, true
}

func (d Deps) watchPhone(c *rest.Call) {
	token := ""
	// Only one header of bounded size is forwarded; anything else is checked by user
	// as an invalid token after its rate limit (docs/api.md, «Ошибки», порядок).
	if values := c.R.Header.Values("X-Verification-Token"); len(values) == 1 && len(values[0]) <= maxVerificationToken {
		token = values[0]
	}
	release, ok := c.Streams.Acquire(rest.HintsFrom(c.R.Context()).ClientIP)
	if !ok {
		c.Metrics.StreamRejected(c.Op.Route.Path)
		c.Streams.Busy(c.Clock()).Write(c.W)
		return
	}
	defer release()
	timeout := orDefault(d.PhoneStream, PhoneStreamDeadline)
	ctx, cancel := context.WithTimeout(c.R.Context(), timeout)
	defer cancel()
	stream, err := d.Auth.WatchPhoneVerification(ctx, &userv1.WatchPhoneVerificationRequest{VerificationToken: token})
	if err != nil {
		c.Fail(err)
		return
	}
	first, err := stream.Recv()
	if err != nil {
		c.Fail(err)
		return
	}
	event, terminal, ok := phoneStatus(first)
	if !ok {
		c.Metrics.Untranslated("WatchPhoneVerificationResponse.status", "unknown")
		rest.Internal().Write(c.W)
		return
	}
	sse, err := rest.StartStream(c.W, time.Now().Add(timeout+streamWriteMargin))
	if err != nil {
		c.Log.WarnContext(ctx, "stream start failed")
		return
	}
	defer c.Metrics.StreamOpened(c.Op.Route.Path)()
	if sse.Event("status", event) != nil || terminal {
		return
	}
	// A transport failure or an unknown status ends the stream without a terminal
	// event: the client reconnects and receives the current state first.
	_ = rest.Pump(ctx, stream.Recv, cancel, d.heartbeat(), sse.Ping, func(m *userv1.WatchPhoneVerificationResponse) (bool, error) {
		event, terminal, ok := phoneStatus(m)
		if !ok {
			c.Metrics.Untranslated("WatchPhoneVerificationResponse.status", "unknown")
			return true, nil
		}
		return terminal, sse.Event("status", event)
	})
}

// RegisterResponse is the 201 of POST /v1/auth/register.
type RegisterResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id"`
}

func (d Deps) register(c *rest.Call) {
	var in struct {
		RegistrationToken string  `json:"registration_token"`
		Accepted          bool    `json:"pd_consent_accepted"`
		Version           string  `json:"pd_consent_version"`
		Timezone          *string `json:"timezone"`
	}
	if !decode(c, &in) {
		return
	}
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Auth.Register(ctx, &userv1.RegisterRequest{RegistrationToken: in.RegistrationToken,
		PdConsentAccepted: in.Accepted, PdConsentVersion: in.Version, Timezone: in.Timezone})
	if err != nil {
		c.Fail(err)
		return
	}
	rest.WriteJSON(c.W, http.StatusCreated, RegisterResponse{AccessToken: resp.GetTokens().GetAccessToken(),
		RefreshToken: resp.GetTokens().GetRefreshToken(), UserID: resp.GetUserId()})
}

// TokenPair is the 200 of POST /v1/auth/token.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type refreshBody struct {
	RefreshToken string `json:"refresh_token"`
}

func (d Deps) refresh(c *rest.Call) {
	var in refreshBody
	if !decode(c, &in) {
		return
	}
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Auth.RefreshTokens(ctx, &userv1.RefreshTokensRequest{RefreshToken: in.RefreshToken})
	if err != nil {
		c.Fail(err)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, TokenPair{AccessToken: resp.GetTokens().GetAccessToken(), RefreshToken: resp.GetTokens().GetRefreshToken()})
}

func (d Deps) logout(c *rest.Call) {
	var in refreshBody
	if !decode(c, &in) {
		return
	}
	ctx, cancel := unary(c)
	defer cancel()
	if _, err := d.Auth.Logout(ctx, &userv1.LogoutRequest{RefreshToken: in.RefreshToken}); err != nil {
		c.Fail(err)
		return
	}
	rest.WriteNoContent(c.W)
}

// ConsentDocument is the 200 of GET /v1/consents/{type}.
type ConsentDocument struct {
	Type    string `json:"type"`
	Version string `json:"version"`
	Title   string `json:"title"`
	URL     string `json:"url"`
}

var consentTypes = map[string]userv1.ConsentType{
	"personal_data": userv1.ConsentType_CONSENT_TYPE_PERSONAL_DATA,
	"assistant":     userv1.ConsentType_CONSENT_TYPE_ASSISTANT,
}

// assistantDocument: the assistant document needs the token, personal_data does not
// (docs/api.md 1.6). An Authorization header of personal_data is neither checked nor forwarded.
func assistantDocument(r *http.Request) bool { return r.PathValue("type") == "assistant" }

func (d Deps) consentDocument(c *rest.Call) {
	requested := c.R.PathValue("type")
	kind, known := consentTypes[requested]
	if !known {
		rest.NotFound().Write(c.W)
		return
	}
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Consents.GetConsentDocument(ctx, &userv1.GetConsentDocumentRequest{Type: kind})
	if err != nil {
		c.Fail(err)
		return
	}
	doc := resp.GetDocument()
	rest.WriteJSON(c.W, http.StatusOK, ConsentDocument{Type: requested, Version: doc.GetVersion(), Title: doc.GetTitle(), URL: doc.GetUrl()})
}
