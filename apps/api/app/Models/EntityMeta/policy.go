package entitymeta

import (
	"context"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// FieldPolicy 是插件字段的读写策略钩子。
//
// 为什么需要它：entity-meta 只知道「字段存在 + 可见性」，不知道「这条字段现在被
// 站点治理关掉了」或「这个 tab 对本访客不可见」。没有这层，治理关掉的字段仍然
// 能被 PUT 写入、被公开面读出——治理就只是视觉开关。
//
// 约定：
//   - nil 策略 = 不过滤（既有调用方与测试行为不变）
//   - 策略只作用于 owner_extension_id 非空的字段；宿主/运营手建字段不受影响
//   - 实现必须 fail closed：策略读不出来就拒绝，而不是放行
type FieldPolicy interface {
	AllowRead(ctx context.Context, actor identity.Actor, definition FieldDefinition) bool
	AllowWrite(ctx context.Context, actor identity.Actor, definition FieldDefinition) bool
}

// WithFieldPolicy 注入插件字段策略（链式可选）。
func (s *Service) WithFieldPolicy(policy FieldPolicy) *Service {
	if s != nil {
		s.fieldPolicy = policy
	}
	return s
}

func (s *Service) allowsRead(ctx context.Context, actor identity.Actor, definition FieldDefinition) bool {
	if s.fieldPolicy == nil || definition.OwnerExtensionID == "" {
		return true
	}
	return s.fieldPolicy.AllowRead(ctx, actor, definition)
}

func (s *Service) allowsWrite(ctx context.Context, actor identity.Actor, definition FieldDefinition) bool {
	if s.fieldPolicy == nil || definition.OwnerExtensionID == "" {
		return true
	}
	return s.fieldPolicy.AllowWrite(ctx, actor, definition)
}
