package http

import (
	"strings"

	"github.com/gofiber/fiber/v3"

	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

// BearerAuthenticator 校验 Authorization: Bearer sft_...
type BearerAuthenticator interface {
	AuthenticatePlaintext(ctx fiber.Ctx, plaintext string) (apitokens.Authenticated, error)
}

// TokenServiceAdapter 适配 apitokens.Service。
type TokenServiceAdapter struct {
	Service *apitokens.Service
}

func (a TokenServiceAdapter) AuthenticatePlaintext(c fiber.Ctx, plaintext string) (apitokens.Authenticated, error) {
	if a.Service == nil {
		return apitokens.Authenticated{}, apitokens.ErrTokenInvalid
	}
	return a.Service.AuthenticatePlaintext(c.Context(), plaintext)
}

// bearerMiddleware 解析 Bearer PAT 并写入 request context。
// 无 header 时放行（cookie 会话路径不变）。无效 token 返回 401。
func bearerMiddleware(auth BearerAuthenticator, auditor audit.Writer) fiber.Handler {
	return func(c fiber.Ctx) error {
		header := strings.TrimSpace(c.Get("Authorization"))
		if header == "" || auth == nil {
			return c.Next()
		}
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) {
			return c.Next()
		}
		plaintext := strings.TrimSpace(strings.TrimPrefix(header, prefix))
		if plaintext == "" || !strings.HasPrefix(plaintext, apitokens.TokenPrefix) {
			return c.Next()
		}
		authenticated, err := auth.AuthenticatePlaintext(c, plaintext)
		if err != nil {
			return ErrorResponse(c, fiber.StatusUnauthorized, "api_token.invalid")
		}
		c.SetContext(apitokens.WithAuth(c.Context(), authenticated))
		// 敏感写方法记一条轻量审计（不记 body）。
		switch c.Method() {
		case fiber.MethodPost, fiber.MethodPut, fiber.MethodPatch, fiber.MethodDelete:
			if auditor != nil {
				_ = auditor.Append(c.Context(), audit.Event{
					ActorUserID: authenticated.UserID,
					Action:      "api_token.use",
					Metadata: map[string]any{
						"tokenId": authenticated.TokenID,
						"method":  c.Method(),
						"path":    c.Path(),
					},
				})
			}
		}
		return c.Next()
	}
}
