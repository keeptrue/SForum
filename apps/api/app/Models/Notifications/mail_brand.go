package notifications

import (
	"context"

	mail "github.com/keeptrue/sforum/apps/api/app/Support/Mail"
)

type MailBrand = mail.Brand
type MailBrandResolver = mail.BrandResolver

func resolveMailBrand(ctx context.Context, resolver MailBrandResolver) MailBrand {
	return mail.ResolveBrand(ctx, resolver)
}

func DefaultMailBrand(siteName, siteURL string) MailBrand {
	return mail.DefaultBrand(siteName, siteURL)
}

func MailBrandFromSettings(siteName, siteURL, logoURL, iconURL, appearanceTheme string) MailBrand {
	return mail.BrandFromSettings(siteName, siteURL, logoURL, iconURL, appearanceTheme)
}
