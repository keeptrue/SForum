package profilecontroller

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
)

type nicknameTestEditor struct {
	state        identity.NicknameState
	previous     []string
	availability identity.NicknameAvailability
	changeErr    error
}

func (e *nicknameTestEditor) State(context.Context, int64) (identity.NicknameState, error) {
	return e.state, nil
}

func (e *nicknameTestEditor) Change(context.Context, identity.Actor, string) (identity.NicknameState, error) {
	if e.changeErr != nil {
		return identity.NicknameState{}, e.changeErr
	}
	return e.state, nil
}

func (e *nicknameTestEditor) Availability(context.Context, identity.Actor, string) (identity.NicknameAvailability, error) {
	return e.availability, nil
}

func (e *nicknameTestEditor) PublicPreviousNames(context.Context, int64) ([]string, error) {
	return e.previous, nil
}

type nicknameErrorEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Reason            string              `json:"reason"`
		Fields            map[string][]string `json:"fields"`
		RetryAt           *time.Time          `json:"retryAt"`
		RetryAfterSeconds *int                `json:"retryAfterSeconds"`
	} `json:"data"`
}

func TestControllerUpdateProfileNicknameCooldownReturnsRetryAt(t *testing.T) {
	retryAt := time.Date(2026, 10, 27, 12, 30, 0, 0, time.UTC)
	editor := &nicknameTestEditor{changeErr: &identity.NicknameCooldownError{RetryAt: retryAt}}
	app, _, _ := newProfileTestAppWithEditor(profileForumReadPolicy{guestRead: "public", ok: true}, editor)
	cookie := loginProfileUser(t, app, 7)

	resp := performProfileRequest(t, app, http.MethodPut, "/api/v1/profile", []byte(`{"displayName":"Alicia"}`), cookie)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 cooldown, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body nicknameErrorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Reason != profile.CodeNicknameCooldown {
		t.Fatalf("reason = %q, want %q", body.Data.Reason, profile.CodeNicknameCooldown)
	}
	if body.Data.RetryAt == nil || !body.Data.RetryAt.Equal(retryAt) {
		t.Fatalf("retryAt = %v, want %v", body.Data.RetryAt, retryAt)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("cooldown response must carry a Retry-After header")
	}
}

func TestControllerUpdateProfileNicknameFieldErrors(t *testing.T) {
	editor := &nicknameTestEditor{changeErr: &identity.NicknameInvalidError{
		Fields: identity.FieldMessages{identity.FieldDisplayName: {identity.MessageNicknameTooShort}},
	}}
	app, _, _ := newProfileTestAppWithEditor(profileForumReadPolicy{guestRead: "public", ok: true}, editor)
	cookie := loginProfileUser(t, app, 7)

	resp := performProfileRequest(t, app, http.MethodPut, "/api/v1/profile", []byte(`{"displayName":"a"}`), cookie)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 invalid nickname, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body nicknameErrorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Reason != profile.CodeNicknameInvalid {
		t.Fatalf("reason = %q", body.Data.Reason)
	}
	// 字段消息经过本地化，不再是 i18n key。
	messages := body.Data.Fields["displayName"]
	if len(messages) != 1 || messages[0] == identity.MessageNicknameTooShort {
		t.Fatalf("displayName field messages = %v", messages)
	}
}

func TestControllerUpdateProfileNicknameDisabled(t *testing.T) {
	editor := &nicknameTestEditor{changeErr: identity.ErrNicknameDisabled}
	app, _, _ := newProfileTestAppWithEditor(profileForumReadPolicy{guestRead: "public", ok: true}, editor)
	cookie := loginProfileUser(t, app, 7)

	resp := performProfileRequest(t, app, http.MethodPut, "/api/v1/profile", []byte(`{"displayName":"Alicia"}`), cookie)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 disabled, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body nicknameErrorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Reason != profile.CodeNicknameDisabled {
		t.Fatalf("reason = %q", body.Data.Reason)
	}
}

func TestControllerNicknameAvailabilityRequiresLogin(t *testing.T) {
	app, _, _ := newProfileTestApp()
	resp := performProfileRequest(t, app, http.MethodGet, "/api/v1/profile/nickname/availability?value=Alicia", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d", resp.StatusCode)
	}
}

func TestControllerNicknameAvailabilityReturnsPreview(t *testing.T) {
	editor := &nicknameTestEditor{availability: identity.NicknameAvailability{
		Available:  true,
		Duplicates: 2,
		Normalized: "Alicia",
	}}
	app, _, _ := newProfileTestAppWithEditor(profileForumReadPolicy{guestRead: "public", ok: true}, editor)
	cookie := loginProfileUser(t, app, 7)

	resp := performProfileRequest(t, app, http.MethodGet, "/api/v1/profile/nickname/availability?value=Alicia", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 availability, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body profileEnvelope[identity.NicknameAvailability]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Data.Available || body.Data.Duplicates != 2 || body.Data.Normalized != "Alicia" {
		t.Fatalf("availability = %+v", body.Data)
	}
}
