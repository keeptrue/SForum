package http

import (
	"github.com/gofiber/fiber/v3"

	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// ResolveUserID 解析当前请求用户：Bearer PAT 优先，否则 cookie session。
func ResolveUserID(c fiber.Ctx, sessions *authsession.Manager) (int64, bool, error) {
	if userID, ok := apitokens.UserIDFromContext(c.Context()); ok {
		return userID, true, nil
	}
	if sessions == nil {
		return 0, false, nil
	}
	return sessions.CurrentUserID(c)
}

// ResolveUserIDWithoutRenewal authenticates the current credential without
// rotating a cookie. Host-owned logout/revocation routes use this path so a
// third-party renew policy cannot veto the recovery action.
func ResolveUserIDWithoutRenewal(c fiber.Ctx, sessions *authsession.Manager) (int64, bool, error) {
	if userID, ok := apitokens.UserIDFromContext(c.Context()); ok {
		return userID, true, nil
	}
	if sessions == nil {
		return 0, false, nil
	}
	return sessions.CurrentUserIDWithoutRenewal(c)
}

// LoadActor 加载 Actor，并在 PAT 请求上按「当前权限 ∩ scopes」收窄。
func LoadActor(c fiber.Ctx, sessions *authsession.Manager, users identity.ActorStore) (identity.Actor, error) {
	userID, ok, err := ResolveUserID(c, sessions)
	if err != nil {
		return identity.Actor{}, err
	}
	if !ok {
		return identity.Actor{}, fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	if users == nil {
		return identity.Actor{}, fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	actor, err := users.LoadActor(c.Context(), userID)
	if err != nil {
		return identity.Actor{}, err
	}
	if scopes := apitokens.ScopesFromContext(c.Context()); len(scopes) > 0 {
		actor = apitokens.RestrictActor(actor, scopes)
	}
	return actor, nil
}

// OptionalActor 解析可选登录主体：匿名返回零值 Actor；PAT 同样按 scopes 收窄。
//
// 它保留原样返回 error 的语义，供「拿不到 viewer 就按 guest 渲染」的调用方使用
// （Page Registry 的渲染 fallback）。公开读端点与写路径不要用它去猜 error 的含义：
// error 里混着「凭据指向的主体没了」与「Redis/PG 现在答不上来」两件事，前者该 401、
// 后者该 503。用 ReadActor / RequireActor（auth_read.go）分类，否则就是本轮修掉的
// B1 缺陷本身。
//
// 依赖故障在这里留一条限频日志：它的现存调用方都会把 error 吞成 guest 渲染，
// 若不在这一层记录，一次 Redis 抖动会让全站个性化静默消失且没有任何证据。
func OptionalActor(c fiber.Ctx, sessions *authsession.Manager, users identity.ActorStore) (identity.Actor, error) {
	actor, err := optionalActor(c, sessions, users, false)
	if err != nil {
		LogActorDependency(c, err)
	}
	return actor, err
}

// OptionalActorWithoutRenewal is restricted to Host-owned security recovery
// routes. Ordinary requests must keep normal renewal behavior.
func OptionalActorWithoutRenewal(c fiber.Ctx, sessions *authsession.Manager, users identity.ActorStore) (identity.Actor, error) {
	return optionalActor(c, sessions, users, true)
}

func optionalActor(
	c fiber.Ctx,
	sessions *authsession.Manager,
	users identity.ActorStore,
	withoutRenewal bool,
) (identity.Actor, error) {
	resolve := ResolveUserID
	if withoutRenewal {
		resolve = ResolveUserIDWithoutRenewal
	}
	userID, ok, err := resolve(c, sessions)
	if err != nil {
		return identity.Actor{}, err
	}
	if !ok || userID <= 0 || users == nil {
		return identity.Actor{}, nil
	}
	actor, err := users.LoadActor(c.Context(), userID)
	if err != nil {
		return identity.Actor{}, err
	}
	if scopes := apitokens.ScopesFromContext(c.Context()); len(scopes) > 0 {
		actor = apitokens.RestrictActor(actor, scopes)
	}
	return actor, nil
}
