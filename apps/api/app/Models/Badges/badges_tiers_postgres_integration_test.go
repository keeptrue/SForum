package badges

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 等级表（badge_tiers）的真库集成测试。
//
// 单测的 fakeStore 复刻了排序与删除规则，但它证明不了 SQL 本身：外键是否真的
// 拦住未知档位、ORDER BY 是否真的按 t.position 走、停用档是否真的被 JOIN 过滤掉。
// 这四件事只有真 Postgres 能回答。
//
// fixture 用**升级后**的 DDL（badge_tiers + 外键 + 无 CHECK），与迁移 202611120001
// 的形状一致；迁移本身的可逆性由 database/migrations 的测试与
// scripts/verify-migration-reversible.sh 守，这里只验语义。

const badgeTiersIntegrationSchemaSQL = `
CREATE TABLE users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT NOT NULL,
  username_lower TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  top_badge_id BIGINT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE badge_tiers (
  key        TEXT PRIMARY KEY,
  name_zh    TEXT NOT NULL,
  name_en    TEXT NOT NULL,
  icon       TEXT NOT NULL DEFAULT 'i-lucide-medal',
  color      TEXT NOT NULL DEFAULT '',
  position   INTEGER NOT NULL DEFAULT 0,
  enabled    BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT badge_tiers_key_check CHECK (key ~ '^[a-z][a-z0-9_]{1,31}$'),
  CONSTRAINT badge_tiers_name_zh_check CHECK (btrim(name_zh) <> ''),
  CONSTRAINT badge_tiers_name_en_check CHECK (btrim(name_en) <> '')
);

CREATE TABLE badges (
  id BIGSERIAL PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  group_id BIGINT,
  tier TEXT NOT NULL,
  icon TEXT NOT NULL DEFAULT 'i-lucide-award',
  grant_mode TEXT NOT NULL CHECK (grant_mode IN ('automatic','manual')),
  metric TEXT,
  threshold BIGINT,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  position INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT badges_tier_fkey FOREIGN KEY (tier) REFERENCES badge_tiers(key)
    ON UPDATE CASCADE ON DELETE RESTRICT
);

CREATE INDEX badges_tier_idx ON badges (tier);

CREATE TABLE user_badges (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  badge_id BIGINT NOT NULL REFERENCES badges(id) ON DELETE CASCADE,
  granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  granted_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
  grant_reason TEXT NOT NULL DEFAULT '',
  CONSTRAINT user_badges_unique UNIQUE (user_id, badge_id)
);
`

type badgeTiersPGFixture struct {
	ctx  context.Context
	pool *pgxpool.Pool
}

func newBadgeTiersPGFixture(t *testing.T) *badgeTiersPGFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("badge_tiers_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, badgeTiersIntegrationSchemaSQL); err != nil {
		pool.Close()
		admin.Close()
		t.Fatalf("apply badge tier fixture DDL: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
	})
	fixture := &badgeTiersPGFixture{ctx: ctx, pool: pool}
	if err := fixture.seedTiers(); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// seedTiers 与迁移种子逐字一致（position gold 0 / silver 1 / bronze 2）。
func (f *badgeTiersPGFixture) seedTiers() error {
	_, err := f.pool.Exec(f.ctx, `
		INSERT INTO badge_tiers (key, name_zh, name_en, icon, color, position, enabled) VALUES
		  ('gold',   '金牌', 'Gold',   'i-lucide-crown', 'amber',   0, TRUE),
		  ('silver', '银牌', 'Silver', 'i-lucide-medal', 'slate',   1, TRUE),
		  ('bronze', '铜牌', 'Bronze', 'i-lucide-award', '#b45309', 2, TRUE)`)
	return err
}

func (f *badgeTiersPGFixture) user(t *testing.T, username string) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO users (username, username_lower, display_name, email)
		VALUES ($1, lower($1), $1, $2) RETURNING id`, username, username+"@example.test").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *badgeTiersPGFixture) manualBadge(t *testing.T, slug, tier string, position int) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, enabled, position)
		VALUES ($1, $1, $2, 'manual', TRUE, $3) RETURNING id`, slug, tier, position).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestBadgeTierForeignKeyRejectsUnknownTier 断言外键是等级合法性的最后防线。
//
// Go 侧的 validateBadgeInput 会先拒绝未知档位，但插件/CLI/手工 SQL 都绕过它；
// 外键保证「库里不存在指向空气的 tier」这件事不依赖任何应用代码。
func TestBadgeTierForeignKeyRejectsUnknownTier(t *testing.T) {
	fixture := newBadgeTiersPGFixture(t)
	_, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode) VALUES ('x','x','platinum','manual')`)
	if err == nil {
		t.Fatal("unknown tier must be rejected by the foreign key")
	}
}

// TestTopBadgeFollowsTierPosition 断言「最高级徽章」按 badge_tiers.position 排序，
// 且**不**按 key 的字典序或写死的 gold/silver/bronze。
func TestTopBadgeFollowsTierPosition(t *testing.T) {
	fixture := newBadgeTiersPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	ctx := fixture.ctx

	// 把 bronze 提到最高档（position 0），gold 降到 2：如果 SQL 还写死权重，
	// gold 会继续胜出，测试就会红。
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE badge_tiers SET position = CASE key WHEN 'bronze' THEN 0 WHEN 'silver' THEN 1 ELSE 2 END`); err != nil {
		t.Fatal(err)
	}
	userID := fixture.user(t, "alice")
	bronzeBadge := fixture.manualBadge(t, "bronze-badge", "bronze", 5)
	goldBadge := fixture.manualBadge(t, "gold-badge", "gold", 0)
	for _, badgeID := range []int64{bronzeBadge, goldBadge} {
		if _, err := store.GrantManual(ctx, GrantInput{UserID: userID, BadgeID: badgeID, GrantedBy: userID}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecomputeTopBadge(ctx, userID); err != nil {
		t.Fatal(err)
	}
	var top int64
	if err := fixture.pool.QueryRow(ctx, `SELECT top_badge_id FROM users WHERE id = $1`, userID).Scan(&top); err != nil {
		t.Fatal(err)
	}
	if top != bronzeBadge {
		t.Fatalf("top badge = %d, want the bronze badge %d (position 0 after reorder)", top, bronzeBadge)
	}

	// 同档位内按 badges.position 升序。
	if _, err := fixture.pool.Exec(ctx, `UPDATE badges SET tier = 'silver' WHERE id = $1`, bronzeBadge); err != nil {
		t.Fatal(err)
	}
	first := fixture.manualBadge(t, "silver-first", "silver", 1)
	second := fixture.manualBadge(t, "silver-second", "silver", 9)
	for _, badgeID := range []int64{first, second} {
		if _, err := store.GrantManual(ctx, GrantInput{UserID: userID, BadgeID: badgeID, GrantedBy: userID}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecomputeTopBadge(ctx, userID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT top_badge_id FROM users WHERE id = $1`, userID).Scan(&top); err != nil {
		t.Fatal(err)
	}
	// silver 现在是最低档（position 1），bronze 已改成 silver；gold-badge 是 position 2 档，
	// 所以这里应选中 silver 档里 position 最小的那条。
	if top != first {
		t.Fatalf("top badge = %d, want %d (lowest badges.position inside the winning tier)", top, first)
	}
}

// TestDisabledTierIsExcludedFromTopBadge 断言停用档位的徽章不参与竞争。
func TestDisabledTierIsExcludedFromTopBadge(t *testing.T) {
	fixture := newBadgeTiersPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	ctx := fixture.ctx

	userID := fixture.user(t, "bob")
	goldBadge := fixture.manualBadge(t, "gold-badge", "gold", 0)
	bronzeBadge := fixture.manualBadge(t, "bronze-badge", "bronze", 0)
	for _, badgeID := range []int64{goldBadge, bronzeBadge} {
		if _, err := store.GrantManual(ctx, GrantInput{UserID: userID, BadgeID: badgeID, GrantedBy: userID}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecomputeTopBadge(ctx, userID); err != nil {
		t.Fatal(err)
	}
	var top int64
	if err := fixture.pool.QueryRow(ctx, `SELECT top_badge_id FROM users WHERE id = $1`, userID).Scan(&top); err != nil {
		t.Fatal(err)
	}
	if top != goldBadge {
		t.Fatalf("top badge = %d, want gold %d", top, goldBadge)
	}

	if _, err := fixture.pool.Exec(ctx, `UPDATE badge_tiers SET enabled = FALSE WHERE key = 'gold'`); err != nil {
		t.Fatal(err)
	}
	if err := store.RecomputeTopBadge(ctx, userID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT top_badge_id FROM users WHERE id = $1`, userID).Scan(&top); err != nil {
		t.Fatal(err)
	}
	if top != bronzeBadge {
		t.Fatalf("top badge = %d, want bronze %d after disabling the gold tier", top, bronzeBadge)
	}
}

// TestDeleteTierMigratesBadgesInOneTransaction 断言删除把引用徽章迁到目标档再删行。
func TestDeleteTierMigratesBadgesInOneTransaction(t *testing.T) {
	fixture := newBadgeTiersPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	ctx := fixture.ctx

	badgeID := fixture.manualBadge(t, "gold-badge", "gold", 0)
	if err := store.DeleteTier(ctx, "gold", "bronze"); err != nil {
		t.Fatal(err)
	}
	var tier string
	if err := fixture.pool.QueryRow(ctx, `SELECT tier FROM badges WHERE id = $1`, badgeID).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != "bronze" {
		t.Fatalf("badge tier = %q, want bronze", tier)
	}
	if _, err := store.GetTier(ctx, "gold"); !errors.Is(err, ErrBadgeTierNotFound) {
		t.Fatalf("gold tier still present: %v", err)
	}
}

// TestDeleteTierRollsBackWhenTargetInvalid 断言校验失败时不改动任何徽章。
func TestDeleteTierRollsBackWhenTargetInvalid(t *testing.T) {
	fixture := newBadgeTiersPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	ctx := fixture.ctx

	badgeID := fixture.manualBadge(t, "gold-badge", "gold", 0)
	if err := store.DeleteTier(ctx, "gold", ""); !errors.Is(err, ErrBadgeTierInvalid) {
		t.Fatalf("missing migrateTo: want ErrBadgeTierInvalid, got %v", err)
	}
	if err := store.DeleteTier(ctx, "gold", "platinum"); !errors.Is(err, ErrBadgeTierNotFound) {
		t.Fatalf("unknown migrateTo: want ErrBadgeTierNotFound, got %v", err)
	}
	var tier string
	if err := fixture.pool.QueryRow(ctx, `SELECT tier FROM badges WHERE id = $1`, badgeID).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != "gold" {
		t.Fatalf("badge tier changed to %q after a rejected delete", tier)
	}
}

// TestBatchGrantWritesRealRows 断言批量授予真的落库，且 top_badge_id 被重算。
func TestBatchGrantWritesRealRows(t *testing.T) {
	fixture := newBadgeTiersPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	admin := NewAdminService(store)
	ctx := fixture.ctx

	badgeID := fixture.manualBadge(t, "event-badge", "gold", 0)
	// granted_by 是 users(id) 外键：操作者必须是一行真实用户。
	operator := fixture.user(t, "operator")
	userA := fixture.user(t, "alice")
	userB := fixture.user(t, "bob")
	// 用户 B 已持有：批量里应算 skipped。
	if _, err := store.GrantManual(ctx, GrantInput{UserID: userB, BadgeID: badgeID, GrantedBy: userB}); err != nil {
		t.Fatal(err)
	}

	result, err := admin.BatchGrant(ctx, manageActor(), BatchGrantInput{
		BadgeID: badgeID, UserIDs: []int64{userA, userB, userA}, Reason: "活动补发", GrantedBy: operator,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Granted != 1 || result.Skipped != 1 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v, want granted=1 skipped=1 failed=0", result)
	}
	var rows int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM user_badges WHERE badge_id = $1`, badgeID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("user_badges rows = %d, want 2", rows)
	}
	var top *int64
	if err := fixture.pool.QueryRow(ctx, `SELECT top_badge_id FROM users WHERE id = $1`, userA).Scan(&top); err != nil {
		t.Fatal(err)
	}
	if top == nil || *top != badgeID {
		t.Fatalf("user A top_badge_id = %v, want %d", top, badgeID)
	}
	var reason string
	if err := fixture.pool.QueryRow(ctx, `SELECT grant_reason FROM user_badges WHERE user_id = $1 AND badge_id = $2`,
		userA, badgeID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "活动补发" {
		t.Fatalf("grant_reason = %q", reason)
	}
}

// TestSearchGrantCandidatesMatchesNameNotEmail 断言只匹配用户名/显示名，不匹配邮箱。
func TestSearchGrantCandidatesMatchesNameNotEmail(t *testing.T) {
	fixture := newBadgeTiersPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	ctx := fixture.ctx

	badgeID := fixture.manualBadge(t, "event-badge", "gold", 0)
	aliceID := fixture.user(t, "alice")
	_ = fixture.user(t, "bob")
	if _, err := store.GrantManual(ctx, GrantInput{UserID: aliceID, BadgeID: badgeID, GrantedBy: aliceID}); err != nil {
		t.Fatal(err)
	}

	items, err := store.SearchGrantCandidates(ctx, GrantCandidateInput{Query: "ali", BadgeID: badgeID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Username != "alice" {
		t.Fatalf("candidates = %+v, want just alice", items)
	}
	if !items[0].AlreadyGranted {
		t.Fatal("alice already holds the badge; alreadyGranted must be true")
	}

	// 邮箱片段不得命中：`example.test` 出现在每个人的邮箱里，若命中就会返回两行。
	items, err = store.SearchGrantCandidates(ctx, GrantCandidateInput{Query: "example.test", BadgeID: badgeID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("email must not be searchable, got %+v", items)
	}
}

// TestReorderTiersWritesPositions 断言重排把 position 写成数组下标。
func TestReorderTiersWritesPositions(t *testing.T) {
	fixture := newBadgeTiersPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	ctx := fixture.ctx

	if err := store.ReorderTiers(ctx, []string{"bronze", "gold"}); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListTiers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"bronze", "gold", "silver"}
	if len(items) != len(want) {
		t.Fatalf("tiers = %d, want %d", len(items), len(want))
	}
	for index, key := range want {
		if items[index].Key != key {
			t.Fatalf("tier[%d] = %q, want %q", index, items[index].Key, key)
		}
		if items[index].Position != index {
			t.Fatalf("tier[%d] position = %d, want %d", index, items[index].Position, index)
		}
	}
}

// TestUpdateTierRejectsDisablingLastEnabledTier 断言「停用最后一个启用档」被拒绝。
//
// 为什么这条必须有：topBadgeOrderSQL 只认 `t.enabled` 的档位，全档停用会让 JOIN
// 一行为空，AdminService 随后触发的 RecomputeAll 会把全站 top_badge_id 落成 NULL
// —— 帖子旁与资料页的徽章同时消失，而且没有任何错误提示。删除路径早有同口径保护
// （DeleteTier 的 remainingEnabled），停用路径曾经漏掉。
func TestUpdateTierRejectsDisablingLastEnabledTier(t *testing.T) {
	fixture := newBadgeTiersPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	ctx := fixture.ctx

	// 只留 gold 一档启用：此时再停用它等于清空全站最高级徽章。
	if _, err := fixture.pool.Exec(ctx, `UPDATE badge_tiers SET enabled = FALSE WHERE key <> 'gold'`); err != nil {
		t.Fatal(err)
	}
	gold, err := store.GetTier(ctx, "gold")
	if err != nil {
		t.Fatal(err)
	}
	disableGold := TierInput{
		Key: gold.Key, NameZh: gold.NameZh, NameEn: gold.NameEn,
		Icon: gold.Icon, Color: gold.Color, Position: gold.Position, Enabled: false,
	}
	if _, err := store.UpdateTier(ctx, "gold", disableGold); !errors.Is(err, ErrBadgeTierInUse) {
		t.Fatalf("disabling the last enabled tier: err = %v, want ErrBadgeTierInUse", err)
	}
	// 被拒绝的写不能落库：gold 必须还是启用状态。
	after, err := store.GetTier(ctx, "gold")
	if err != nil {
		t.Fatal(err)
	}
	if !after.Enabled {
		t.Fatal("rejected update must not persist: gold is still disabled in the database")
	}

	// 还有别的启用档时，停用是允许的。
	if _, err := fixture.pool.Exec(ctx, `UPDATE badge_tiers SET enabled = TRUE WHERE key = 'silver'`); err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateTier(ctx, "gold", disableGold)
	if err != nil {
		t.Fatalf("disabling a tier while another stays enabled: %v", err)
	}
	if updated.Enabled {
		t.Fatal("gold should be disabled after a successful update")
	}
}

// TestCreateTierAppendsToEnd 断言「不传 position」的新等级落在末尾。
//
// position 0 是**最高**位次：若把缺省当 0，新等级会与金牌并列，挂在它上面的徽章
// 会和金牌徽章争同一个名次（topBadgeOrderSQL 的首键是 t.position）。所以缺省必须
// 走 TierPositionAppend → max(position)+1，而显式传 0 仍然被尊重。
func TestCreateTierAppendsToEnd(t *testing.T) {
	fixture := newBadgeTiersPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	ctx := fixture.ctx

	appended, err := store.CreateTier(ctx, TierInput{
		Key: "event", NameZh: "活动", NameEn: "Event", Position: TierPositionAppend, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if appended.Position != 3 {
		t.Fatalf("appended tier position = %d, want 3 (max+1)", appended.Position)
	}

	explicit, err := store.CreateTier(ctx, TierInput{
		Key: "legend", NameZh: "传说", NameEn: "Legend", Position: 0, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Position != 0 {
		t.Fatalf("explicit position 0 must be honoured, got %d", explicit.Position)
	}
}
