package providers

import (
	"context"

	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// SettingsSurfaceFieldPolicy 把「渲染投影」当成插件字段的读写策略。
//
// 判据只有一条：这个字段键是否出现在该访客当前的 settingsSections 投影里。
// 投影已经合并了 manifest 声明、站点治理覆盖、tab/字段权限与 visibility=admin
// 的排除，因此读写两侧不需要第二套规则；出现分叉的那天就是它们开始漂移的那天。
//
// 不注入（nil）= 不过滤，既有行为与测试保持不变。
type SettingsSurfaceFieldPolicy struct {
	surfaces ExtensionSettingsSurfaceProvider
}

func NewSettingsSurfaceFieldPolicy(surfaces ExtensionSettingsSurfaceProvider) SettingsSurfaceFieldPolicy {
	return SettingsSurfaceFieldPolicy{surfaces: surfaces}
}

// AllowRead 判定「这条插件字段当前是否可读」。
//
// 与渲染投影**解耦**：投影是按 viewer 过滤出来的表单（匿名访客拿到的是一份
// 稀疏视图），拿它当读权限会把公开资料页读空——匿名 viewer 的表单投影非空
// 与否，本来就不该决定一条 public 字段能不能被匿名读。
//
// 读裁决只回答两件事：字段是否仍被声明（含站点治理未关闭），以及字段/所在 tab
// 的 permission 是否对 viewer 成立。visibility（public/owner/admin）由 entity-meta
// 自己的 canReadField 裁决，这里不重复实现。
func (p SettingsSurfaceFieldPolicy) AllowRead(ctx context.Context, actor identity.Actor, definition entitymeta.FieldDefinition) bool {
	declared, ok := p.declaration(ctx, definition.FieldKey)
	if !ok {
		return false
	}
	return declared.allowsPermission(actor)
}

// AllowWrite 保持严格：写路径要求「这条字段出现在该 actor 的表单投影里」。
// 治理关闭、visibility=admin、tab/字段 permission 不足都会让它不在投影里。
func (p SettingsSurfaceFieldPolicy) AllowWrite(ctx context.Context, actor identity.Actor, definition entitymeta.FieldDefinition) bool {
	key := definition.FieldKey
	if key == "" || !actor.IsActive() {
		// 写路径额外要求活跃 actor：投影本身对匿名也非空（公开字段照常渲染），
		// 不能靠它表达「谁能写」。
		return false
	}
	sections, err := p.surfaces.ProfileSections(ctx, actor)
	if err != nil {
		// fail closed：策略读不出来时宁可不写，也不放行。
		return false
	}
	for _, section := range sections {
		for _, field := range section.Fields {
			if field.FieldKey == key {
				return true
			}
		}
	}
	return false
}

func (d DeclaredFieldPolicy) allowsPermission(actor identity.Actor) bool {
	if d.Permission != "" && !actor.Can(d.Permission) {
		return false
	}
	if d.TabPermission != "" && !actor.Can(d.TabPermission) {
		return false
	}
	return true
}

// declaration 返回某字段的声明事实（不看 viewer）。字段被治理关闭或声明被删掉
// 时返回 false：这时它既不可读也不可写。
func (p SettingsSurfaceFieldPolicy) declaration(ctx context.Context, fieldKey string) (DeclaredFieldPolicy, bool) {
	if fieldKey == "" {
		return DeclaredFieldPolicy{}, false
	}
	declarations, err := p.surfaces.DeclaredFields(ctx)
	if err != nil {
		return DeclaredFieldPolicy{}, false
	}
	declaration, ok := declarations[fieldKey]
	return declaration, ok
}
