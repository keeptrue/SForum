package hostapi

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 本文件是 `public_user_trust_level.get` 的**现算**证据（真库 + 真 Resolver）。
//
// 构造：行为统计只够 TL1，而变更账本写着 2。
// 断言：读面返回 1。谁把实现改成读账本（user_trust_levels），这条立刻红——
// 账本是快照、NOT authoritative，读它就是「双真相」。

type pgsqlTrustActivityReader struct{ pool *pgxpool.Pool }

func (r pgsqlTrustActivityReader) ActivityFor(ctx context.Context, userID int64) (trustlevels.Activity, error) {
	var activity trustlevels.Activity
	err := r.pool.QueryRow(ctx, `
		SELECT days_visited, topic_count, comment_count, like_received_count,
		       like_given_count, bookmark_count
		FROM user_activity WHERE user_id = $1`, userID).Scan(
		&activity.DaysVisited, &activity.TopicCount, &activity.CommentCount,
		&activity.LikeReceivedCount, &activity.LikeGivenCount, &activity.BookmarkCount,
	)
	if err != nil {
		return trustlevels.Activity{}, err
	}
	return activity, nil
}

type pgsqlTrustPolicySource struct{ service *options.Service }

func (s pgsqlTrustPolicySource) Policy(ctx context.Context) (trustlevels.Policy, error) {
	// 与 bootstrap 的 trustLevelPolicySource 同形：产品开关在**读阈值之前**短路，
	// 所以关掉开关时 resolver 返 ErrDisabled，读面才可能进 disabled 态。
	flags, err := s.service.FeatureFlags(ctx)
	if err != nil {
		return trustlevels.Policy{}, err
	}
	if !flags.TrustLevels {
		return trustlevels.Policy{}, trustlevels.ErrDisabled
	}
	raw, err := s.service.WebOption(ctx, options.NameTrustLevels)
	if err != nil {
		return trustlevels.Policy{}, err
	}
	return trustlevels.DecodeConfig(raw)
}

// trustLevelQueryUserFixture 造一个 active、注册 400 天前、统计只够 TL1 的用户。
func trustLevelQueryUserFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, username string) int64 {
	t.Helper()
	var userID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name,
		                   status, email_verified_at, created_at, kind)
		VALUES ($1, $1, $2, $2, $1, 'active', now(), now() - interval '400 days', 'human')
		RETURNING id`, username, username+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_activity (user_id, days_visited, comment_count) VALUES ($1, 4, 6)`, userID); err != nil {
		t.Fatal(err)
	}
	return userID
}

// newHostAPIQueryDatabase 建一次性库 + 完整迁移链，返回 ctx 与连接池。
func newHostAPIQueryDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	baseURL := testdb.IsolatedURL(t)
	adminConfig, err := pgx.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	databaseName := fmt.Sprintf("hostapi_trust_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})

	migrationConfig, err := pgx.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	migrationConfig.Database = databaseName
	sqlDB := stdlib.OpenDB(*migrationConfig)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files(), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_ = sqlDB.Close()

	poolConfig, err := pgxpool.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Database = databaseName
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	return ctx, pool
}

func TestUserTrustLevelSourceComputesInsteadOfReadingLedger(t *testing.T) {
	ctx, pool := newHostAPIQueryDatabase(t)

	// 夹具：active 用户，注册 400 天前；统计只够 TL1（days 4 / comments 6；TL2 要 days 15）。
	var userID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name,
		                   status, email_verified_at, created_at, kind)
		VALUES ('q', 'q', 'q@example.test', 'q@example.test', 'Q', 'active', now(),
		        now() - interval '400 days', 'human') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_activity (user_id, days_visited, comment_count) VALUES ($1, 4, 6)`, userID); err != nil {
		t.Fatal(err)
	}
	// 账本故意写 2：它是快照，绝不能成为读面的答案。
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_trust_levels (user_id, level, tier_id) VALUES ($1, 2, 'resident')`, userID); err != nil {
		t.Fatal(err)
	}
	policyJSON, err := trustlevels.EncodeConfig(trustlevels.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO web_options (name, value) VALUES ($1, $2), ($3, 'enabled')
		ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value`,
		options.NameTrustLevels, policyJSON, options.NameFeatureTrustLevels); err != nil {
		t.Fatal(err)
	}

	optionService := options.NewService(options.NewPostgresStore(pool))
	resolver := trustlevels.NewResolver(
		pgsqlTrustActivityReader{pool: pool},
		pgsqlTrustPolicySource{service: optionService},
	)
	source := NewTrustLevelQuerySource(resolver, optionService, pool)

	rows, err := source.UserTrustLevel(ctx, userID)
	if err != nil {
		t.Fatalf("user trust level: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if got := rows[0]["level"]; got != int64(1) {
		t.Fatalf("level = %#v, want 1（账本写着 2，但真实条件只够 TL1；读账本会返回 2）", got)
	}
	if got := rows[0]["state"]; got != "resolved" {
		t.Fatalf("state = %#v", got)
	}

	// denied 侧：停用用户不在公开投影里（返回空行，而不是 403 或伪等级）。
	if _, err := pool.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if rows, err := source.UserTrustLevel(ctx, userID); err != nil || len(rows) != 0 {
		t.Fatalf("停用用户必须不在公开面：rows=%v err=%v", rows, err)
	}
	// denied 侧：不存在的用户同样返回空行。
	if rows, err := source.UserTrustLevel(ctx, userID+1_000_000); err != nil || len(rows) != 0 {
		t.Fatalf("不存在的用户必须返回空行：rows=%v err=%v", rows, err)
	}
}

// TestUserTrustLevelSourceKeepsIncompleteAndDisabledNullable 是 task-34 的水源判据。
//
// 为什么必须打在**真实现 + 真库**上：`TestPublicUserTrustLevelQueryKeepsIncompleteNullable`
// 用 fake source 只证明了「执行器把 nil 原样透传」——锁的是管道，不是水源。把生产实现
// 的 incomplete 分支改成 `level: 0` 时，那条 fake 用例照样绿。
//
// 构造：
//   - 策略挂一条引用**未声明 factKey** 的规则到 TL3 ⇒ Evaluate() 判不了 ⇒ state=incomplete；
//   - 同一用户换成无规则策略 ⇒ state=resolved；
//   - features.trust_levels=disabled ⇒ state=disabled。
//
// 三态都要求 level / tier_id 为 null：0 是一个真实等级，不能拿来冒充「不知道」。
func TestUserTrustLevelSourceKeepsIncompleteAndDisabledNullable(t *testing.T) {
	ctx, pool := newHostAPIQueryDatabase(t)
	userID := trustLevelQueryUserFixture(t, ctx, pool, "incomplete_user")

	rule := `,"rule":` + `{"v":1,"root":{"op":"all","children":[{"type":"fact.count","params":{"factKey":"acme.undeclared.metric","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}]}}`
	tier := func(id, requirements, extra string) string {
		base := `{"id":"` + id + `","name":{"zh":"档","en":"Tier"},"icon":"leaf","requirements":` +
			requirements + `,"limits":{},"earnable":[]`
		return base + extra + `}`
	}
	withRule := `{"schemaVersion":2,"tiers":[` +
		tier("member", `{"days_visited":3,"comment_count":5}`, "") + `,` +
		tier("resident", `{"days_visited":15}`, "") + `,` +
		tier("veteran", `{"days_visited":4,"comment_count":6}`, rule) + `,` +
		tier("leader", `{"days_visited":100}`, "") + `]}`
	plain, err := trustlevels.EncodeConfig(trustlevels.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	optionService := options.NewService(options.NewPostgresStore(pool))
	resolver := trustlevels.NewResolver(
		pgsqlTrustActivityReader{pool: pool},
		pgsqlTrustPolicySource{service: optionService},
	)
	source := NewTrustLevelQuerySource(resolver, optionService, pool)
	writeTrustPolicy := func(policyJSON string, enabled string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO web_options (name, value) VALUES ($1, $2), ($3, $4)
			ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value`,
			options.NameTrustLevels, policyJSON, options.NameFeatureTrustLevels, enabled); err != nil {
			t.Fatal(err)
		}
		optionService.Invalidate()
	}

	// incomplete：规则引用了未声明的 factKey。
	writeTrustPolicy(withRule, "enabled")
	rows, err := source.UserTrustLevel(ctx, userID)
	if err != nil {
		t.Fatalf("incomplete 查询失败：%v", err)
	}
	if len(rows) != 1 || rows[0]["state"] != "incomplete" {
		t.Fatalf("rows=%v，期望 state=incomplete", rows)
	}
	if rows[0]["level"] != nil || rows[0]["tier_id"] != nil {
		t.Fatalf("incomplete 时 level/tier_id 必须为 null，得到 level=%#v tier=%#v",
			rows[0]["level"], rows[0]["tier_id"])
	}

	// 对照：同一用户、换成无规则策略 ⇒ resolved 且有具体等级。
	writeTrustPolicy(plain, "enabled")
	rows, err = source.UserTrustLevel(ctx, userID)
	if err != nil {
		t.Fatalf("resolved 查询失败：%v", err)
	}
	if len(rows) != 1 || rows[0]["state"] != "resolved" || rows[0]["level"] == nil {
		t.Fatalf("rows=%v，期望 state=resolved 且有 level", rows)
	}

	// disabled：产品面关闭同样是「读不到」，level/tier_id 仍为 null。
	writeTrustPolicy(plain, "disabled")
	rows, err = source.UserTrustLevel(ctx, userID)
	if err != nil {
		t.Fatalf("disabled 查询失败：%v", err)
	}
	if len(rows) != 1 || rows[0]["state"] != "disabled" {
		t.Fatalf("rows=%v，期望 state=disabled", rows)
	}
	if rows[0]["level"] != nil || rows[0]["tier_id"] != nil {
		t.Fatalf("disabled 时 level/tier_id 必须为 null，得到 level=%#v tier=%#v",
			rows[0]["level"], rows[0]["tier_id"])
	}
}
