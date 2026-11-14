package jobs

import (
	"time"

	"github.com/riverqueue/river"
)

// 核心 schedule 稳定 ID。新增维护任务时先加常量与 CoreScheduleDefinitions，再在 bootstrap 注入 Constructor。
const (
	ScheduleIdentityCleanupSessions         = "identity.cleanup_sessions"
	ScheduleAttachmentsCleanupOrphans       = "attachments.cleanup_orphans"
	ScheduleAttachmentsReconcileCompression = "attachments.reconcile_compression"
	// ScheduleAuditCleanupEvents 清理过期 audit_events（F1.4 保留期 job）。
	ScheduleAuditCleanupEvents = "audit.cleanup_events"
	// ScheduleForumAutoLockIdle 按站点 autoLockIdleDays 锁定闲置主题。
	ScheduleForumAutoLockIdle = "forum.auto_lock_idle"
	// ScheduleForumBookmarkReminders 扫描到期收藏提醒并发出通知（每分钟）。
	ScheduleForumBookmarkReminders = "forum.bookmark_reminders"
	// ScheduleForumFlushViewCounts 将 Redis 浏览增量刷入 topics.view_count / hot_score。
	ScheduleForumFlushViewCounts = "forum.flush_view_counts"
	// ScheduleSearchReconcile 增量修复当前搜索 provider 的漏建、过期和幽灵文档。
	ScheduleSearchReconcile = "search.reconcile"
	// ScheduleExtensionsFinalizeUninstallCleanup 周期收敛「台账已 succeeded 但
	// tombstone 仍 pending」的卸载物理清理；仅 embed worker 注册执行器。
	ScheduleExtensionsFinalizeUninstallCleanup = "extensions.finalize_uninstall_cleanup"
	// ScheduleTrustLevelsSweep 每日按账本陈旧度重算信任等级（M4b-2）。
	//
	// 它兜住的是**没有活动事件**的那些等级变化：角色变更（identity.role 规则）、
	// OAuth 解绑（identity.oauth_link 规则）、徽章收回（badge.owned 规则）——
	// 这些都不经过 6 个活动触发点，只有按账本 computed_at 兜底才看得见。
	ScheduleTrustLevelsSweep = "trustlevels.sweep"
)

// CoreScheduleDefinitions 返回宿主内置 schedule 目录模板（无 Constructor）。
// bootstrap 负责填入 Constructor 后 Register；admin 列表可直接用这些元数据。
func CoreScheduleDefinitions() []ScheduleDefinition {
	return []ScheduleDefinition{
		{
			ID:          ScheduleIdentityCleanupSessions,
			JobKind:     ScheduleIdentityCleanupSessions,
			Queue:       QueueDefault,
			Interval:    24 * time.Hour,
			Owner:       "identity",
			Enabled:     true,
			Description: "清理已下线超过保留期的历史会话行",
			RunOnStart:  false,
		},
		{
			ID:          ScheduleAttachmentsCleanupOrphans,
			JobKind:     ScheduleAttachmentsCleanupOrphans,
			Queue:       QueueMaintenance,
			Interval:    24 * time.Hour,
			Owner:       "attachments",
			Enabled:     true,
			Description: "清理超过保留期且无引用的孤儿附件",
			RunOnStart:  false,
		},
		{
			ID:          ScheduleAttachmentsReconcileCompression,
			JobKind:     ScheduleAttachmentsReconcileCompression,
			Queue:       QueueMaintenance,
			Interval:    time.Minute,
			Owner:       "attachments",
			Enabled:     true,
			Description: "补偿附件图片压缩任务入队失败并继续待处理任务",
			RunOnStart:  true,
		},
		{
			ID:          ScheduleAuditCleanupEvents,
			JobKind:     ScheduleAuditCleanupEvents,
			Queue:       QueueMaintenance,
			Interval:    24 * time.Hour,
			Owner:       "audit",
			Enabled:     true,
			Description: "清理超过保留期的审计日志（默认 90 天）",
			RunOnStart:  false,
		},
		{
			ID:          ScheduleForumAutoLockIdle,
			JobKind:     ScheduleForumAutoLockIdle,
			Queue:       QueueMaintenance,
			Interval:    24 * time.Hour,
			Owner:       "forum",
			Enabled:     true,
			Description: "按站点 autoLockIdleDays 锁定闲置主题（0 关闭时 job 空跑）",
			RunOnStart:  false,
		},
		{
			ID:          ScheduleForumBookmarkReminders,
			JobKind:     ScheduleForumBookmarkReminders,
			Queue:       QueueMaintenance,
			Interval:    time.Minute,
			Owner:       "forum",
			Enabled:     true,
			Description: "扫描到期的收藏提醒并写入站内/邮件通知（每轮最多 200 条）",
			RunOnStart:  false,
		},
		{
			ID:          ScheduleForumFlushViewCounts,
			JobKind:     ScheduleForumFlushViewCounts,
			Queue:       QueueMaintenance,
			Interval:    45 * time.Second,
			Owner:       "forum",
			Enabled:     true,
			Description: "将 Redis 主题浏览增量刷入 PG view_count/hot_score（D3）",
			RunOnStart:  false,
		},
		{
			ID:          ScheduleSearchReconcile,
			JobKind:     ScheduleSearchReconcile,
			Queue:       QueueMaintenance,
			Interval:    15 * time.Minute,
			Owner:       "search",
			Enabled:     true,
			Description: "核对并修复当前搜索 provider 的缺失、过期和幽灵文档",
			RunOnStart:  true,
		},
		{
			ID:          ScheduleTrustLevelsSweep,
			JobKind:     ScheduleTrustLevelsSweep,
			Queue:       QueueMaintenance,
			Interval:    24 * time.Hour,
			Owner:       "trustlevels",
			Enabled:     true,
			Description: "按变更账本陈旧度重算信任等级（mode=stale；覆盖角色变更/OAuth 解绑/徽章收回这类无活动事件的变化）",
			RunOnStart:  false,
		},
		{
			ID:          ScheduleExtensionsFinalizeUninstallCleanup,
			JobKind:     ScheduleExtensionsFinalizeUninstallCleanup,
			Queue:       QueueMaintenance,
			Interval:    5 * time.Minute,
			Owner:       "extensions",
			Enabled:     true,
			Description: "收敛卸载后仍未完成的宿主物理清理（pending tombstone 自动重放）",
			RunOnStart:  true,
		},
	}
}

// NewCoreScheduleRegistry 用内置模板构建 registry。
// constructors 按 schedule ID 注入；缺失时条目仍进入 catalog，但不生成 River PeriodicJob。
func NewCoreScheduleRegistry(constructors map[string]river.PeriodicJobConstructor) (*ScheduleRegistry, error) {
	reg := NewScheduleRegistry()
	for _, def := range CoreScheduleDefinitions() {
		if constructors != nil {
			def.Constructor = constructors[def.ID]
		}
		if err := reg.Register(def); err != nil {
			return nil, err
		}
	}
	return reg, nil
}
