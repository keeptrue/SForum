package http

import (
	"github.com/gofiber/fiber/v3"

	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	"github.com/keeptrue/sforum/apps/api/app/Support/Localization"
	"github.com/keeptrue/sforum/apps/api/config"
)

const requestLocaleKey = "sforum.locale"

func localeMiddleware(cfg config.Config, service *options.Service) fiber.Handler {
	return func(c fiber.Ctx) error {
		supported := cfg.SupportedLocales
		fallback := cfg.AppLocale
		if service != nil {
			if settings, err := service.RuntimeSettings(c.Context()); err == nil {
				supported = settings.SupportedLocales
				fallback = settings.DefaultLocale
			}
		}
		locale := localization.NegotiateAcceptLanguage(
			c.Get("Accept-Language"),
			supported,
			fallback,
		)
		c.Locals(requestLocaleKey, locale)
		return c.Next()
	}
}

func Locale(c fiber.Ctx) string {
	if locale, ok := c.Locals(requestLocaleKey).(string); ok && locale != "" {
		return locale
	}
	return localization.DefaultLocale
}
