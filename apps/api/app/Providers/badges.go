package providers

import (
	"context"

	"github.com/gofiber/fiber/v3"

	badgescontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Badges"
	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// BadgesProvider 装配徽章 HTTP 面（公开资料页 1 条 + 后台 12 条）。
//
// 权限不在 provider 里判断：读 badge.view / 写 badge.manage 由生成的
// core route catalog 守卫声明并执行（见 app/Support/Routes）。
type BadgesProvider struct {
	controller *badgescontroller.Controller
}

// badgesFeatureGate 把 features.badges 适配成 controller 需要的窄接口。
//
// 刻意不在 Options.Service 上加方法：那个类型已经顶到接收者方法上限（48），
// 加一个就破门禁。适配器属于装配层，放在 provider 里是正确的归属。
type badgesFeatureGate struct {
	options *options.Service
}

func (g badgesFeatureGate) BadgesFeatureEnabled(ctx context.Context) (bool, error) {
	if g.options == nil {
		return true, nil
	}
	flags, err := g.options.FeatureFlags(ctx)
	if err != nil {
		return false, err
	}
	return flags.Badges, nil
}

func NewBadgesProvider(
	service *badges.Service,
	admin *badges.AdminService,
	optionsService *options.Service,
	users identity.ActorStore,
	sessions *authsession.Manager,
	profiles *profile.Service,
) *BadgesProvider {
	gate := badgesFeatureGate{options: optionsService}
	controller := badgescontroller.NewController(service, admin, gate, users, sessions)
	if profiles != nil {
		controller = controller.WithUserLookup(profileUserLookup{profiles: profiles})
	}
	return &BadgesProvider{controller: controller}
}

func (p *BadgesProvider) RegisterRoutes(api fiber.Router) {
	if p == nil || p.controller == nil {
		return
	}
	p.controller.RegisterRoutes(api)
}

// profileUserLookup 把 Profile 域的公开资料查询适配成徽章墙需要的窄接口。
//
// 走 GetPublicProfile 而不是自己查 users 表：公开资料页已经有一整套可见性
// 与状态过滤规则（封禁/私密资料），徽章墙必须复用同一套，不能另开一条。
type profileUserLookup struct {
	profiles *profile.Service
}

func (l profileUserLookup) PublicProfileUserID(ctx context.Context, username string) (int64, error) {
	publicProfile, err := l.profiles.GetPublicProfile(ctx, username)
	if err != nil {
		return 0, err
	}
	return publicProfile.UserID, nil
}
