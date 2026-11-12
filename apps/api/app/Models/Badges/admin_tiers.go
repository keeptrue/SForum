package badges

import (
	"context"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 本文件是等级表的 admin 面：授权 + 校验 + 「改完排序要重算」的编排。
//
// 与 admin_service.go 分文件的理由很直接：那个文件已 349 行且全是徽章目录 CRUD，
// 等级表是另一张表的另一套写规则（删除要先迁移引用、排序要全量重算）。
// 授权入口复用同一个 requireBadgePermission，避免出现第二套权限判断。

// WithTierReader 覆盖等级校验端口（测试注入 / 未来拆库时用）。
func (a *AdminService) WithTierReader(tiers TierReader) *AdminService {
	if a != nil {
		a.tiers = tiers
	}
	return a
}

// tierReader 解析等级校验端口；两者都缺时返回 nil（校验会明确报 tier invalid，
// 而不是静默放行）。
func (a *AdminService) tierReader() TierReader {
	if a == nil {
		return nil
	}
	if a.tiers != nil {
		return a.tiers
	}
	if reader, ok := a.store.(TierReader); ok {
		return reader
	}
	return nil
}

// ListTiers 返回全部等级（含停用）与引用计数。
//
// 读权限是 badge.view：后台徽章编辑弹窗要用它填等级下拉，只给 manage 会让
// 「能看列表但不能编辑」的运营角色连编辑弹窗都打不开。
func (a *AdminService) ListTiers(ctx context.Context, actor identity.Actor) ([]TierDef, error) {
	if err := requireBadgePermission(actor, PermissionBadgeView); err != nil {
		return nil, err
	}
	if a == nil || a.store == nil {
		return nil, nil
	}
	return a.store.ListTiers(ctx)
}

// GetTier 读单个等级（后台编辑弹窗要用旧值做 PATCH 合并）。
func (a *AdminService) GetTier(ctx context.Context, actor identity.Actor, key string) (TierDef, error) {
	if err := requireBadgePermission(actor, PermissionBadgeView); err != nil {
		return TierDef{}, err
	}
	if a == nil || a.store == nil {
		return TierDef{}, ErrBadgesDisabled
	}
	return a.store.GetTier(ctx, key)
}

// CreateTier 创建等级。
func (a *AdminService) CreateTier(ctx context.Context, actor identity.Actor, input TierInput) (TierDef, error) {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return TierDef{}, err
	}
	if a == nil || a.store == nil {
		return TierDef{}, ErrBadgesDisabled
	}
	normalized, err := validateTierInput(input, true)
	if err != nil {
		return TierDef{}, err
	}
	return a.store.CreateTier(ctx, normalized)
}

// UpdateTier 更新等级的展示字段（key 不可变）。
//
// 停用一档会让引用它的徽章退出「最高级徽章」竞争（topBadgeOrderSQL 里 t.enabled），
// 所以启用状态变化后必须全量重算——否则帖子旁会继续展示一个已停用档位的徽章。
func (a *AdminService) UpdateTier(ctx context.Context, actor identity.Actor, key string, input TierInput) (TierDef, error) {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return TierDef{}, err
	}
	if a == nil || a.store == nil {
		return TierDef{}, ErrBadgesDisabled
	}
	normalized, err := validateTierInput(input, false)
	if err != nil {
		return TierDef{}, err
	}
	previous, err := a.store.GetTier(ctx, key)
	if err != nil {
		return TierDef{}, err
	}
	updated, err := a.store.UpdateTier(ctx, key, normalized)
	if err != nil {
		return TierDef{}, err
	}
	if previous.Enabled != updated.Enabled {
		if _, err := a.store.RecomputeAll(ctx); err != nil {
			return TierDef{}, err
		}
	}
	return updated, nil
}

// DeleteTier 把引用该等级的徽章迁到 migrateTo，再删除等级行。
//
// 迁移改变了那些徽章的档位，必然影响「谁是他最高级徽章」，因此删除成功后全量重算。
func (a *AdminService) DeleteTier(ctx context.Context, actor identity.Actor, key string, migrateTo string) error {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return err
	}
	if a == nil || a.store == nil {
		return ErrBadgesDisabled
	}
	if err := a.store.DeleteTier(ctx, key, migrateTo); err != nil {
		return err
	}
	_, err := a.store.RecomputeAll(ctx)
	return err
}

// ReorderTiers 重排等级位次。
//
// position 是「最高级徽章」排序的第一关键字，改它等于改全站每个人的 chip，
// 所以重排后必须重算——这一步不能留给运营点「重算徽章」按钮，他们不会知道要按。
func (a *AdminService) ReorderTiers(ctx context.Context, actor identity.Actor, keys []string) error {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return err
	}
	if a == nil || a.store == nil {
		return ErrBadgesDisabled
	}
	if err := a.store.ReorderTiers(ctx, keys); err != nil {
		return err
	}
	_, err := a.store.RecomputeAll(ctx)
	return err
}
