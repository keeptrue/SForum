// Package audit 提供宿主统一审计写入入口（F1.4 最小集）。
//
// 权限/角色变更已由 identity.PostgresStore 在事务内写入 audit_events；
// 本包供 options、extensions 等模块在业务成功后追加同表记录，避免各处手写 SQL。
// 保留期清理由 schedule registry 的 audit.cleanup_events 周期任务执行。
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 稳定 action 常量。新增敏感操作时优先复用或扩展此列表。
const (
	ActionSettingsUpdate                 = "settings.update"
	ActionSiteNavigationApply            = "site_navigation.apply"
	ActionSiteNavigationDefaultsRestore  = "site_navigation.defaults_restore"
	ActionSiteNavigationSnapshotRestore  = "site_navigation.snapshot_restore"
	ActionSiteNavigationImport           = "site_navigation.import"
	ActionNotificationPreferencesUpdate  = "notification.preferences.update"
	ActionNotificationPreferencesRestore = "notification.preferences.restore"
	ActionNotificationPolicyUpdate       = "notification.policy.update"
	ActionNotificationPolicyRestore      = "notification.policy.restore"
	ActionNotificationChannelTest        = "notification.channel.test"
	ActionNotificationChannelSelect      = "notification.channel.select"
	ActionNotificationChannelReset       = "notification.channel.reset"
	ActionNotificationSubscriptionCreate = "notification.subscription.create"
	ActionNotificationSubscriptionRevoke = "notification.subscription.revoke"
	ActionPushDeviceRegister             = "notification.push_device.register"
	ActionPushDeviceRevoke               = "notification.push_device.revoke"
	ActionExtensionSettingsAction        = "extension.settings.action"
	ActionExtensionEnable                = "extension.enable"
	ActionExtensionDisable               = "extension.disable"
	ActionExtensionRestart               = "extension.restart"
	ActionExtensionActivate              = "extension.theme_activate"
	ActionExtensionInstalled             = "extension.install"
	ActionExtensionUpgraded              = "extension.upgrade"
	ActionExtensionRollback              = "extension.rollback"
	ActionExtensionUninstalled           = "extension.uninstall"
	ActionExtensionLifecycleRecovery     = "extension.lifecycle_recovery"
	// ActionExtensionLifecycleOrphanReclaim 宿主回收孤儿 lifecycle operation
	// （所有 step lease 均已过期、无活进程推进）时写入。
	ActionExtensionLifecycleOrphanReclaim = "extension.lifecycle_orphan_reclaim"
	// ActionExtensionLifecycleActivationRecovered 启动修复把一条「state 事务已
	// 提交 target、publication 未提交」的中断 activation 恢复到 source 时写入。
	ActionExtensionLifecycleActivationRecovered = "extension.lifecycle_activation_recovered"
	// ActionExtensionVersionGC 扩展版本 GC：一条审计对应一个被回收的版本行。
	ActionExtensionVersionGC         = "extension.version_gc"
	ActionExtensionFrontendGrant     = "extension.frontend_trust_grant"
	ActionExtensionFrontendRevoke    = "extension.frontend_trust_revoke"
	ActionExtensionTrustChallenge    = "extension.trust_challenge"
	ActionExtensionTrustGrant        = "extension.trust_grant"
	ActionExtensionTrustRevoke       = "extension.trust_revoke"
	ActionExtensionTrustDenied       = "extension.trust_denied"
	ActionExtensionSafeModeBoot      = "extension.safe_mode_boot"
	ActionExtensionCLIRecovery       = "extension.cli_recovery"
	ActionExtensionPluginCommand     = "extension.plugin_command"
	ActionExtensionAdminSurface      = "extension.admin_surface"
	ActionExtensionActivationFailed  = "extension.activation_failed"
	ActionExtensionActivationSkipped = "extension.activation_skipped"
	// ActionExtensionDatabaseGrant 记录一次由操作者发起的扩展数据库精确授权
	// 供给（own_schema 等直接运行时权力的授权行）。它是 extension_database_grants
	// 行的审计锚点：没有这条事件，运行时租约就不会自建授权。
	ActionExtensionDatabaseGrant = "extension.database_grant"
	// ActionExtensionBackendDenied 非 super_admin 试图引入/执行非内置后端插件。
	ActionExtensionBackendDenied = "extension.backend_execution_denied"

	// Page Registry：核心页 replace 批准 / 恢复 / 冲突选择。
	ActionPageReplaceApprove = "pages.replace_approve"
	ActionPageRestoreCore    = "pages.restore_core"
	ActionPageConflictSelect = "pages.conflict_select"

	ActionRouteProviderSelect         = "routes.provider_select"
	ActionRouteProviderReset          = "routes.provider_reset"
	ActionRouteCommittedAfterFailure  = "routes.committed_after_failure"
	ActionRouteRuntimeIncident        = "routes.runtime_incident"
	ActionProviderSlotSelect          = "providers.slot_select"
	ActionProviderSlotReset           = "providers.slot_reset"
	ActionProviderSlotProbe           = "providers.slot_probe"
	ActionForumTopicEditAny           = "forum.topic.edit_any"
	ActionForumCommentEditAny         = "forum.comment.edit_any"
	ActionForumTopicRevisionRestore   = "forum.topic.revision_restore"
	ActionForumCommentRevisionRestore = "forum.comment.revision_restore"
	ActionForumTopicRevisionRedact    = "forum.topic.revision_redact"
	ActionForumCommentRevisionRedact  = "forum.comment.revision_redact"

	// ActionIdentityEmailConsentRead 插件经「能力 + actor 委派」门控的同意式
	// 查询读取了调用者本人的 email。metadata 只记 extension/query/request 标识与
	// 行数，绝不记 email 值。
	ActionIdentityEmailConsentRead = "identity.email.consent.read"

	// RecommendedRetentionDays 审计日志推荐保留天数（清理 job 默认）。
	RecommendedRetentionDays = 90
)

// Event 是一条 append-only 审计记录。
type Event struct {
	ActorUserID  int64
	TargetUserID int64 // 0 表示无目标用户
	Action       string
	Metadata     map[string]any
}

// Writer 抽象，便于测试注入。
type Writer interface {
	Append(ctx context.Context, event Event) error
}

// TxWriter lets a domain ledger commit its audit record in the same database
// transaction. It is deliberately narrow: callers cannot access audit tables
// or retention internals directly.
type TxWriter interface {
	AppendTx(ctx context.Context, tx pgx.Tx, event Event) error
}

// IDWriter is the durable audit boundary used when another ledger must retain
// an immutable correlation to the audit row. Existing best-effort callers can
// continue to depend on Writer only.
type IDWriter interface {
	Writer
	AppendReturningID(ctx context.Context, event Event) (int64, error)
}

// Cleaner 清理过期审计行。
type Cleaner interface {
	DeleteOlderThan(ctx context.Context, keepDays int) (int64, error)
}

// CleanupResult keeps authority-retention exceptions observable without
// breaking the existing Cleaner interface used by the River worker.
type CleanupResult struct {
	Deleted  int64
	Retained int64
}

// PostgresWriter 直接写入 identity 的 audit_events 表。
type PostgresWriter struct {
	Pool *pgxpool.Pool
}

func NewPostgresWriter(pool *pgxpool.Pool) *PostgresWriter {
	return &PostgresWriter{Pool: pool}
}

func (w *PostgresWriter) Append(ctx context.Context, event Event) error {
	_, err := w.AppendReturningID(ctx, event)
	return err
}

func (w *PostgresWriter) AppendReturningID(ctx context.Context, event Event) (int64, error) {
	if w == nil || w.Pool == nil {
		return 0, fmt.Errorf("audit writer is not configured")
	}
	return appendReturningID(ctx, w.Pool, event)
}

func (w *PostgresWriter) AppendTx(ctx context.Context, tx pgx.Tx, event Event) error {
	if w == nil || tx == nil {
		return fmt.Errorf("audit writer is not configured")
	}
	_, err := appendReturningID(ctx, tx, event)
	return err
}

type auditExecutor interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func appendReturningID(ctx context.Context, q auditExecutor, event Event) (int64, error) {
	if event.Action == "" {
		return 0, fmt.Errorf("audit action is required")
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		metadata = []byte(`{}`)
	}
	if len(metadata) == 0 {
		metadata = []byte(`{}`)
	}

	var actor any
	if event.ActorUserID > 0 {
		actor = event.ActorUserID
	}
	var target any
	if event.TargetUserID > 0 {
		target = event.TargetUserID
	}

	var id int64
	err = q.QueryRow(ctx, `
		INSERT INTO audit_events (actor_user_id, target_user_id, action, metadata)
		VALUES ($1, $2, $3, $4::jsonb)
		RETURNING id
	`, actor, target, event.Action, string(metadata)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert audit event: %w", err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("insert audit event returned an invalid id")
	}
	return id, nil
}

// DeleteOlderThan 删除 created_at 早于 keepDays 的审计行。keepDays<=0 时使用推荐值。
func (w *PostgresWriter) DeleteOlderThan(ctx context.Context, keepDays int) (int64, error) {
	result, err := w.CleanupOlderThan(ctx, keepDays)
	return result.Deleted, err
}

// CleanupOlderThan removes ordinary expired events while retaining audit rows
// that still authorize an Identity role decision, applied grant, or permission
// catalog entry.
// Candidate rows are locked with SKIP LOCKED: an in-flight authority binding
// retains its audit instead of making the whole cleanup batch fail.
func (w *PostgresWriter) CleanupOlderThan(ctx context.Context, keepDays int) (CleanupResult, error) {
	if w == nil || w.Pool == nil {
		return CleanupResult{}, fmt.Errorf("audit writer is not configured")
	}
	if keepDays <= 0 {
		keepDays = RecommendedRetentionDays
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -keepDays)
	var permissionCatalogExists, roleGrantsExists bool
	if err := w.Pool.QueryRow(ctx, `
		SELECT
		  to_regclass('extension_permission_catalog') IS NOT NULL,
		  to_regclass('extension_permission_role_grants') IS NOT NULL
	`).Scan(&permissionCatalogExists, &roleGrantsExists); err != nil {
		return CleanupResult{}, fmt.Errorf("inspect audit authority retention tables: %w", err)
	}

	// Catalog and grant evidence arrive in the additive Identity approval
	// migration. Independent probes keep this compatibility layer deployable
	// before that migration and safe during rollback/recovery.
	catalogRetention := ""
	if permissionCatalogExists {
		catalogRetention = `
			  AND NOT EXISTS (
				SELECT 1
				FROM extension_permission_catalog AS catalog
				WHERE catalog.registered_audit_event_id = event.id
			  )`
	}
	grantRetention := ""
	if roleGrantsExists {
		grantRetention = `
			  AND NOT EXISTS (
				SELECT 1
				FROM extension_permission_role_grants AS role_grant
				WHERE role_grant.applied_audit_event_id = event.id
			  )`
	}
	cleanupQuery := `
		WITH eligible AS MATERIALIZED (
			SELECT count(*)::bigint AS total
			FROM audit_events
			WHERE created_at < $1
		), candidates AS MATERIALIZED (
			SELECT id
			FROM audit_events
			WHERE created_at < $1
			ORDER BY id
			FOR UPDATE SKIP LOCKED
		), deleted AS (
			DELETE FROM audit_events AS event
			USING candidates
			WHERE event.id = candidates.id
			  AND NOT EXISTS (
				SELECT 1
				FROM extension_permission_role_suggestions AS suggestion
				WHERE suggestion.decision_audit_event_id = event.id
			  )
	` + catalogRetention + grantRetention + `
			RETURNING event.id
		)
		SELECT
		  (SELECT count(*)::bigint FROM deleted),
		  GREATEST(
			(SELECT total FROM eligible) - (SELECT count(*)::bigint FROM deleted),
			0
		  )
	`
	var result CleanupResult
	err := w.Pool.QueryRow(ctx, cleanupQuery, cutoff).Scan(&result.Deleted, &result.Retained)
	if err != nil {
		return CleanupResult{}, fmt.Errorf("delete old audit events: %w", err)
	}
	return result, nil
}

// NoopWriter 测试与未装配场景。
type NoopWriter struct{}

func (NoopWriter) Append(context.Context, Event) error { return nil }

func (NoopWriter) AppendTx(context.Context, pgx.Tx, Event) error { return nil }

func (NoopWriter) AppendReturningID(context.Context, Event) (int64, error) { return 0, nil }

// Ensure 返回可用 Writer。
func Ensure(w Writer) Writer {
	if w == nil {
		return NoopWriter{}
	}
	return w
}

var _ IDWriter = (*PostgresWriter)(nil)
var _ IDWriter = NoopWriter{}
