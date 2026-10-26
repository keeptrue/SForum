package identity

import (
	"context"

	appearance "github.com/keeptrue/sforum/apps/api/app/Support/Appearance"
)

type CurrentUserAppearanceStore interface {
	UpdateCurrentUserAppearance(ctx context.Context, userID int64, preference AppearancePreference) (CurrentUser, error)
	ClearCurrentUserAppearance(ctx context.Context, userID int64) (CurrentUser, error)
}

// AppearancePreferenceService owns validation and persistence for the current
// user's private appearance override.
type AppearancePreferenceService struct {
	store CurrentUserAppearanceStore
}

func NewAppearancePreferenceService(store CurrentUserAppearanceStore) *AppearancePreferenceService {
	return &AppearancePreferenceService{store: store}
}

func (s *AppearancePreferenceService) Update(ctx context.Context, userID int64, preference AppearancePreference) (CurrentUser, error) {
	theme, themeOK := appearance.NormalizeTheme(preference.Theme)
	lightBackground, backgroundOK := appearance.NormalizeLightBackground(preference.LightBackground)
	if userID <= 0 || !themeOK || !backgroundOK {
		return CurrentUser{}, ErrInvalidAppearancePreference
	}
	if s == nil || s.store == nil {
		return CurrentUser{}, ErrAppearanceUpdateUnavailable
	}
	return s.store.UpdateCurrentUserAppearance(ctx, userID, AppearancePreference{
		Theme: theme, LightBackground: lightBackground,
	})
}

func (s *AppearancePreferenceService) Clear(ctx context.Context, userID int64) (CurrentUser, error) {
	if userID <= 0 {
		return CurrentUser{}, ErrInvalidAppearancePreference
	}
	if s == nil || s.store == nil {
		return CurrentUser{}, ErrAppearanceUpdateUnavailable
	}
	return s.store.ClearCurrentUserAppearance(ctx, userID)
}
