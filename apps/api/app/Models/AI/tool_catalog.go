package ai

import (
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// 工具目录：控制台需要知道「站内有哪些 AI 工具」，运营者才能在用途白名单里
// 选名字，而不是凭记忆手打一个拼错的工具名。目录是登记表的只读枚举面。

// WithToolCatalog 注入站内工具目录。
//
// 它在装配期调用一次：工具登记表在 AI 服务之后才构造（工具依赖论坛、搜索等
// 服务，而这些服务又依赖 AI 网关），因此不能走构造函数。装配完成后不再改写，
// 与登记表「一次登记、只读枚举」的语义一致。
//
// 由此带来一个已知限制：插件启用/停用要等 API 重启才反映到目录，因为工具登记
// 本身也发生在装配期。运行期增量登记需要一份可变登记表 + 派发期校验，尚未做。
func (s *Service) WithToolCatalog(catalog supportai.ToolCatalog) *Service {
	if s == nil {
		return s
	}
	s.toolCatalog = catalog
	return s
}

// AvailableTools 返回站内已注册的 AI 工具清单（含来源）。
//
// 未注入目录时返回空清单而不是报错：控制台会退化成「手动输入工具名」，那是一个
// 可用但更笨的界面，不该让整个配置页打不开。
func (s *Service) AvailableTools(actor identity.Actor) ([]supportai.ToolAvailability, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return nil, identity.ErrPermissionDenied
	}
	if s == nil || s.toolCatalog == nil {
		return nil, nil
	}
	return s.toolCatalog.Available(), nil
}
