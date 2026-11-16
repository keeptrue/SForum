package identity

import (
	"context"
	"testing"
	"time"
)

// TestDeniedChildPermissionSurvivesParentExpansion 是 P1-1 的 Postgres 侧证据。
//
// 场景（真实事故形状）：角色授予 settings.manage，管理员再对该账号 deny
// settings.mail.manage。覆盖写入成功、后台显示已保存，但装载期的父→子展开在 deny
// 过滤之后又把 settings.mail.manage 加回集合，Actor.Can() 于是仍然放行 —— 邮件设置
// 路由从未被真正收回。
//
// 两个装载点都必须被这条测试覆盖：
//   - 池路径 LoadActor（HTTP 请求裁决读的就是它）；
//   - 事务路径 LoadEffectiveActorTx（注册/改密等身份写流程）。
//
// 变异要求：删掉 postgres_store.go 的 applyDenied 调用，第一条断言必须红；
// 删掉 actor_postgres_tx.go 的 applyDenied 调用，事务段必须红。
func TestDeniedChildPermissionSurvivesParentExpansion(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	t.Cleanup(cancel)

	preparePermissionDenySchema(t, ctx, fixture)

	// 专用账号：member 角色 + settings.manage，再单独 deny settings.mail.manage。
	var userID int64
	if err := fixture.pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, locale, status)
		VALUES ('deny_expand', 'deny_expand', 'deny_expand@example.test', 'deny_expand@example.test',
		        'Deny Expand', 'zh-CN', 'active')
		RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatalf("insert deny fixture user: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key = 'member'`,
		`INSERT INTO user_permission_overrides (user_id, permission_key, effect)
		 VALUES ($1, 'settings.mail.manage', 'deny')`,
	} {
		if _, err := fixture.pool.Exec(ctx, statement, userID); err != nil {
			t.Fatalf("seed deny fixture state (%s): %v", statement, err)
		}
	}
	// 角色授予 settings.manage：这就是「父权限在手」的前置条件。
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, 'settings.manage' FROM roles WHERE key = 'member'
	`); err != nil {
		t.Fatalf("grant settings.manage to member role: %v", err)
	}

	store := NewPostgresStore(fixture.pool)

	// 装载期集合：被 deny 的键不得出现在 CurrentUser.Permissions 列表里
	// （会话下发给前端的形状），也不得被父权限展开复活。
	current, err := store.GetCurrentUser(ctx, userID)
	if err != nil {
		t.Fatalf("GetCurrentUser: %v", err)
	}
	if !containsPermissionKey(current.Permissions, PermissionSettingsManage) {
		t.Fatalf("前置条件缺失：角色没有授予 settings.manage，得到 %v", current.Permissions)
	}
	if containsPermissionKey(current.Permissions, PermissionSettingsMailManage) {
		t.Fatalf("deny 被父权限展开复活：Permissions 列表含 settings.mail.manage，得到 %v", current.Permissions)
	}
	if !containsPermissionKey(current.Permissions, PermissionSettingsSiteManage) {
		t.Fatalf("兄弟子权限应当照常展开，得到 %v", current.Permissions)
	}

	// 池路径裁决：Can() 必须为 false，且 deny 以显式 false 条目存在（只写不删）。
	actor, err := store.LoadActor(ctx, userID)
	if err != nil {
		t.Fatalf("LoadActor: %v", err)
	}
	if actor.Can(PermissionSettingsMailManage) {
		t.Fatalf("LoadActor: Can(settings.mail.manage) 仍为 true，Permissions=%v", actor.Permissions)
	}
	if allowed, present := actor.Permissions[PermissionSettingsMailManage]; !present || allowed {
		t.Fatalf("deny 没有落成显式 false 条目：present=%t allowed=%t", present, allowed)
	}
	if !actor.Can(PermissionSettingsSiteManage) {
		t.Fatal("LoadActor: deny 不应影响同一父权限下的兄弟子权限")
	}

	// 事务路径裁决：注册/改密等写流程不能绕过 deny。
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txActor, err := LoadEffectiveActorTx(ctx, tx, userID, nil)
	if err != nil {
		t.Fatalf("LoadEffectiveActorTx: %v", err)
	}
	if txActor.Can(PermissionSettingsMailManage) {
		t.Fatalf("LoadEffectiveActorTx: Can(settings.mail.manage) 仍为 true，Permissions=%v", txActor.Permissions)
	}
	if !txActor.Can(PermissionSettingsManage) {
		t.Fatal("LoadEffectiveActorTx: 父权限本身必须仍然生效")
	}

	// 后台详情与生效结论必须一致：loadAdminUserAccess 走同一个展开入口。
	detail := &AdminUserDetail{AdminUserSummary: AdminUserSummary{ID: userID}}
	if err := store.loadAdminUserAccess(ctx, detail); err != nil {
		t.Fatalf("loadAdminUserAccess: %v", err)
	}
	if containsPermissionKey(detail.Permissions, PermissionSettingsMailManage) {
		t.Fatalf("后台详情仍显示被 deny 的 settings.mail.manage，得到 %v", detail.Permissions)
	}
	if !containsPermissionKey(detail.Permissions, PermissionSettingsManage) {
		t.Fatalf("后台详情缺少父权限 settings.manage，得到 %v", detail.Permissions)
	}
}

// preparePermissionDenySchema 补齐 fixture 基础表里缺失的列/表：LoadActor 走的是
// 生产 GetCurrentUser 查询（users 的外观偏好、资料与附件 JOIN），fixture 的极简
// users 表没有这些列。补在这里而不是改共享 fixture，避免影响其它集成测试的形状。
func preparePermissionDenySchema(t *testing.T, ctx context.Context, fixture *identityPersistencePGFixture) {
	t.Helper()
	for _, ddl := range []string{
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS is_initial_super_admin BOOLEAN NOT NULL DEFAULT FALSE`,
		`CREATE TABLE IF NOT EXISTS user_appearance_preferences (
			user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			theme TEXT,
			light_background TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS user_profiles (
			user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			avatar_attachment_id BIGINT
		)`,
		`CREATE TABLE IF NOT EXISTS attachments (
			id BIGSERIAL PRIMARY KEY,
			public_id TEXT,
			owner_user_id BIGINT,
			content_type TEXT,
			status TEXT
		)`,
		`INSERT INTO permissions (key, module, description) VALUES
			('settings.manage', 'settings', 'Legacy settings bundle'),
			('settings.mail.manage', 'settings', 'Mail settings'),
			('settings.site.manage', 'settings', 'Site settings')
		 ON CONFLICT (key) DO NOTHING`,
	} {
		if _, err := fixture.pool.Exec(ctx, ddl); err != nil {
			t.Fatalf("prepare permission deny schema: %v", err)
		}
	}
}

func containsPermissionKey(keys []string, want string) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}
