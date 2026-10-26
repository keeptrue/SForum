package options

import (
	"context"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	mail "github.com/keeptrue/sforum/apps/api/app/Support/Mail"
)

func init() {
	optionDefinitions = append(optionDefinitions, optionDefinition{
		name: NameMailWelcomeEnabled, managePermission: identity.PermissionSettingsMailManage,
	})
}

// MailSettings is the narrow runtime view needed by transactional mail.
// Keeping it separate prevents mail policy reads from growing Options Service.
type MailSettings struct{ service *Service }

func NewMailSettings(service *Service) *MailSettings { return &MailSettings{service: service} }

func (s *MailSettings) WelcomeMailEnabled(ctx context.Context) (bool, error) {
	if s == nil || s.service == nil {
		return false, nil
	}
	values, err := s.service.loadMap(ctx)
	if err != nil {
		return false, err
	}
	return isEnabledOption(values[NameMailWelcomeEnabled]), nil
}

func (s *MailSettings) DefaultMailLocale(ctx context.Context) (string, error) {
	if s == nil || s.service == nil {
		return "", nil
	}
	values, err := s.service.loadMap(ctx)
	if err != nil {
		return "", err
	}
	return values[NameSiteDefaultLocale], nil
}

func (s *MailSettings) MailBrand(ctx context.Context) (mail.Brand, error) {
	if s == nil || s.service == nil {
		return mail.DefaultBrand("SForum", ""), nil
	}
	values, err := s.service.loadMap(ctx)
	if err != nil {
		return mail.Brand{}, err
	}
	return mail.BrandFromSettings(
		values[NameSiteName], values[NameSiteURL], values[NameSiteLogoURL], values[NameSiteFaviconURL], values[NameAppearanceTheme],
	), nil
}
