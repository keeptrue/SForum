package badges

import (
	"context"
	"log/slog"
)

// GrantedBadge 是一条**刚刚写入** user_badges 的授予，供授予通知层渲染。
//
// 只有 id/slug/name：通知内容不需要阈值、规则或运营字段，把完整 `Badge`
// 传进通知层会让「通知能读什么」这条边界随时间膨胀。
type GrantedBadge struct {
	ID   int64
	Slug string
	Name string
}

// GrantNotifier 把新授予投影成站内通知（实现由 bootstrap 装配到 Notifications）。
//
// 端口放在领域侧而不是直接 import Notifications：`Badges` 不依赖通知域
// （见模块笔记的 Dependency Direction），否则一条「发通知」的需求就会把
// 邮件模板、收件箱策略、渠道解析全部拖进徽章包。
type GrantNotifier interface {
	NotifyBadgeGranted(ctx context.Context, userID int64, granted []GrantedBadge) error
}

// SetGrantNotifier 注入授予通知出口（bootstrap 装配；测试可注入 fake）。
//
// 与 `SetBackfillEnqueuer` / `SetBackfillRuns` 同款：不放进构造函数，因为
// `NewAdminService(store)` 在外面的装配与测试里已经存在，加参数会让每个
// 只用一条能力的测试都被迫提供一个它不关心的出口。
//
// 未注入（nil）= 不发通知，且**不是错误**：徽章授予本身与通知是否装配无关，
// 缺装配时的正确行为是「授予成功、没有通知」，而不是让运营的授予请求失败。
func (a *AdminService) SetGrantNotifier(notifier GrantNotifier) {
	if a == nil {
		return
	}
	a.notifier = notifier
}

// notifyGranted 尽力而为地发授予通知。
//
// 失败只记日志，不返回错误、不回滚授予：GRANT 已经落库（且每条 grant 都是
// 自动提交的独立语句，没有可回滚的事务边界），把错误上抛会让运营看到「授予失败」
// 而库里明明有授予行——这正是本模块反复出现的假状态之一。
func (a *AdminService) notifyGranted(ctx context.Context, userID int64, granted []GrantedBadge) {
	if a == nil || a.notifier == nil || userID <= 0 || len(granted) == 0 {
		return
	}
	if err := a.notifier.NotifyBadgeGranted(ctx, userID, granted); err != nil {
		slog.Warn("notify badge granted failed", "user_id", userID, "badges", len(granted), "error", err)
	}
}
