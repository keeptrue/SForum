package identity

import (
	"testing"
)

// TestIdentityPersistenceFixtureModelsActorLoaderColumns 是这份手写 schema 的**正向守卫**。
//
// 背景：`seedIdentityPersistenceTestBaseTables` 是真实 users 表的第二份模型。主 schema
// 加一列、这里不加，症状不是「夹具报错」，而是 31 条无关测试一起红，而且哨兵文案指向
// `ErrIdentitySessionPolicyStoreUnavailable`（错误指向 A、原因在 B）。2026-11-11 的
// `created_at` 就是这么红的。
//
// 这条守卫的做法是最省的一种：直接跑一次 `LoadEffectiveActorTx`，断言它读出来的
// `CreatedAt` 非零。以后谁再往这个 loader 的 SELECT 里加 users 列，这份夹具会在这里红，
// 而不是让一堆无关测试用一句误导性哨兵红掉。
func TestIdentityPersistenceFixtureModelsActorLoaderColumns(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	pool := fixture.newPool("actor_loader_columns_guard")

	var userID int64
	if err := pool.QueryRow(fixture.ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, locale, status)
		VALUES ('guard', 'guard', 'guard@example.test', 'guard@example.test', 'Guard', 'zh-CN', 'active')
		RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatalf("insert guard user: %v", err)
	}

	tx, err := pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(fixture.ctx) }()

	// trust 传 nil：本守卫只验列存在与扫描正确，不验等级解析。
	actor, err := LoadEffectiveActorTx(fixture.ctx, tx, userID, nil)
	if err != nil {
		t.Fatalf("LoadEffectiveActorTx 在这份夹具上失败（users 表缺列？）：%v", err)
	}
	if actor.CreatedAt.IsZero() {
		t.Fatal("LoadEffectiveActorTx 读出的 CreatedAt 是零值：夹具 users 表缺 created_at，或 loader 没扫它")
	}
}
