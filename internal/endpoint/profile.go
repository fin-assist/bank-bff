package endpoint

import (
	"net/http"

	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"

	"github.com/fin-assist/bank-bff/internal/rest"
)

// Profile is the REST profile (docs/api.md 2.1).
type Profile struct {
	UserID    string    `json:"user_id"`
	Phone     string    `json:"phone"`
	Theme     string    `json:"theme"`
	Timezone  string    `json:"timezone"`
	Consents  []Consent `json:"consents"`
	CreatedAt string    `json:"created_at"`
}

// Consent is one element of profile.consents[].
type Consent struct {
	Type           string  `json:"type"`
	Granted        bool    `json:"granted"`
	Version        *string `json:"version,omitempty"`
	CurrentVersion string  `json:"current_version"`
}

// profileJSON builds the profile; bank-bff assembles consents[] from the consent
// fields of user.Profile (grpc-api.md 5.3).
func profileJSON(p *userv1.Profile) Profile {
	theme, ok := enumCode(p.GetTheme())
	if !ok {
		// An unknown theme is shown as system (docs/api.md 2.1).
		theme = "system"
	}
	out := Profile{UserID: p.GetUserId(), Phone: p.GetPhone(), Theme: theme, Timezone: p.GetTimezone(),
		Consents: []Consent{}, CreatedAt: moment(p.GetCreatedAt(), zone(p.GetTimezone()))}
	if a := p.GetAssistantConsent(); a != nil {
		out.Consents = append(out.Consents, Consent{Type: "assistant", Granted: a.GetGranted(), Version: a.Version, CurrentVersion: a.GetCurrentVersion()})
	}
	return out
}

func (d Deps) profile(c *rest.Call) {
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Profile.GetProfile(ctx, &userv1.GetProfileRequest{})
	if err != nil {
		c.Fail(err)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, profileJSON(resp.GetProfile()))
}

func (d Deps) grantConsent(c *rest.Call) {
	var in struct {
		Version string `json:"version"`
	}
	if !decode(c, &in) {
		return
	}
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Profile.GrantAssistantConsent(ctx, &userv1.GrantAssistantConsentRequest{Version: in.Version})
	if err != nil {
		c.Fail(err)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, profileJSON(resp.GetProfile()))
}

func (d Deps) revokeConsent(c *rest.Call) {
	ctx, cancel := unary(c)
	defer cancel()
	if _, err := d.Profile.RevokeAssistantConsent(ctx, &userv1.RevokeAssistantConsentRequest{}); err != nil {
		c.Fail(err)
		return
	}
	rest.WriteNoContent(c.W)
}

func (d Deps) updateProfile(c *rest.Call) {
	var in struct {
		Theme    *string `json:"theme"`
		Timezone *string `json:"timezone"`
	}
	if !decode(c, &in) {
		return
	}
	request := &userv1.UpdateProfileRequest{Timezone: in.Timezone}
	if in.Theme != nil {
		// The OpenAPI closed request enum was validated before this conversion.
		request.Theme = map[string]userv1.Theme{
			"system": userv1.Theme_THEME_SYSTEM, "light": userv1.Theme_THEME_LIGHT, "dark": userv1.Theme_THEME_DARK,
		}[*in.Theme].Enum()
	}
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Profile.UpdateProfile(ctx, request)
	if err != nil {
		c.Fail(err)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, profileJSON(resp.GetProfile()))
}

func (d Deps) deleteAccount(c *rest.Call) {
	ctx, cancel := unary(c)
	defer cancel()
	if _, err := d.Profile.DeleteAccount(ctx, &userv1.DeleteAccountRequest{}); err != nil {
		c.Fail(err)
		return
	}
	rest.WriteNoContent(c.W)
}
