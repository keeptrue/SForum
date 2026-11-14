package providers

import (
	"github.com/gofiber/fiber/v3"

	trustlevelscontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/TrustLevels"
	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// TrustLevelsProvider 装配信任等级 HTTP 面（公开 1 条 + 后台 3 条）。
//
// 权限不在 provider 里判断：`admin.access` / `trust.manage` 由控制器用 actor.Can
// 真正执行（路由目录只是目录，不是运行时守卫）。
type TrustLevelsProvider struct {
	controller *trustlevelscontroller.Controller
}

func NewTrustLevelsProvider(
	optionsService *options.Service,
	resolver *trustlevels.Resolver,
	users identity.ActorStore,
	sessions *authsession.Manager,
) *TrustLevelsProvider {
	return &TrustLevelsProvider{
		controller: trustlevelscontroller.NewController(optionsService, resolver, users, sessions),
	}
}

// WithRuleEditing 注入规则编辑面的三个依赖（M1：条件目录与影响估算）。
//
// 用 setter 而不是加构造参数：装配顺序是 infra 造 controller、domains 之后才造出
// 徽章 store 与事实目录（D20 的同一个先例），构造期拿不到它们。路由注册发生在全部
// 装配完成之后，所以这里晚绑定是安全的。
//
// 三个依赖都是窄接口/闭包（D17）：本包与控制器都不 import `Models/Badges`，
// 真实查询留在徽章 store 里，装配层只做类型适配。
func (p *TrustLevelsProvider) WithRuleEditing(
	refCandidates trustlevels.RefCandidateSource,
	factCandidates func() []conditions.FactCandidate,
	impactEstimator trustlevels.RuleImpactEstimator,
) *TrustLevelsProvider {
	if p == nil || p.controller == nil {
		return p
	}
	p.controller.WithRuleEditingDeps(refCandidates, factCandidates, impactEstimator)
	return p
}

func (p *TrustLevelsProvider) RegisterRoutes(api fiber.Router) {
	if p == nil || p.controller == nil {
		return
	}
	p.controller.RegisterRoutes(api)
}
