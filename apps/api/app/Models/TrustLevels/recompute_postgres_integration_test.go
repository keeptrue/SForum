package trustlevels_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	conditions "github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 本文件是 M4b-2 的真库断言：`trust_level.changed` 的三条语义 + `Incomplete` 闸。
//
// 为什么必须落在真库：闸本身是一行 if，但「台账有没有被覆盖」「同级时 computed_at
// 有没有被顺手刷新」这两件事只有真表能回答。用 fake ledger 测这两条等于自己出题
// 自己答——而脏账本恰恰是这条链路唯一难查的故障（下一次对比失去基准）。
//
// 夹具复用 loader_path_postgres_fixture_test.go 的建库助手：那条路径已经验证过
// 「一次性库 + 完整迁移链」，这里不需要第二套。

// recordingPublisher 记录 observe 事件（fail-open，永不出错）。
type recordingPublisher struct {
	envelopes []appevents.Envelope
}

func (p *recordingPublisher) Emit(_ context.Context, envelope appevents.Envelope) appevents.Result {
	p.envelopes = append(p.envelopes, envelope)
	return appevents.Result{OK: true}
}

type recomputeFixture struct {
	ctx        context.Context
	pool       *pgxpool.Pool
	options    *options.Service
	userID     int64
	recomputer *trustlevels.Recomputer
	ledger     trustlevels.LedgerStore
	publisher  *recordingPublisher
	resolver   *trustlevels.Resolver
	policyA    string
}

func newRecomputeFixture(t *testing.T, policy string) *recomputeFixture {
	t.Helper()
	ctx := context.Background()
	baseURL := testdb.IsolatedURL(t)
	databaseName := fmt.Sprintf("trust_recompute_%d", time.Now().UnixNano())
	t.Cleanup(createLoaderDatabase(t, ctx, baseURL, databaseName))

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

	fixture := &recomputeFixture{ctx: ctx, pool: pool, policyA: policy, publisher: &recordingPublisher{}}
	// users 夹具：username / username_lower / email / email_lower 都是 NOT NULL。
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name,
		                   status, email_verified_at, created_at, kind)
		VALUES ('recompute_user', 'recompute_user', 'recompute@example.test',
		        'recompute@example.test', 'Recompute', 'active', now(),
		        now() - interval '400 days', 'human')
		RETURNING id`).Scan(&fixture.userID); err != nil {
		t.Fatal(err)
	}
	// 行为统计取 loaderActivity：TL3 达标、TL4 六项全不过 → 无规则策略下稳定 TL3。
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_activity (user_id, topic_count, comment_count, like_received_count,
		                           like_given_count, bookmark_count, days_visited)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		fixture.userID, loaderActivity.TopicCount, loaderActivity.CommentCount,
		loaderActivity.LikeReceivedCount, loaderActivity.LikeGivenCount,
		loaderActivity.BookmarkCount, loaderActivity.DaysVisited); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO web_options (name, value) VALUES ($1, 'enabled')
		ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value`,
		options.NameFeatureTrustLevels); err != nil {
		t.Fatal(err)
	}

	fixture.options = options.NewService(options.NewPostgresStore(pool))
	fixture.setPolicy(t, policy)
	fixture.resolver = trustlevels.NewResolver(
		loaderActivityReader{store: badges.NewPostgresActivityStore(pool)},
		loaderPolicySource{options: fixture.options},
	).WithRuleEvaluators(func(ctx context.Context, userID int64) (trustlevels.RuleEvaluator, error) {
		conditionResolver, err := conditions.NewResolver(pool, userID)
		if err != nil {
			return nil, err
		}
		return trustlevels.NewRuleEvaluator(conditionResolver), nil
	})
	fixture.ledger = trustlevels.NewPostgresLedgerStore(pool)
	fixture.recomputer = trustlevels.NewRecomputer(
		fixture.resolver,
		fixture.ledger,
		trustlevels.NewPostgresUserTrustInputReader(pool),
		fixture.publisher,
	)
	return fixture
}

func (f *recomputeFixture) setPolicy(t *testing.T, raw string) {
	t.Helper()
	mustPolicy(t, raw)
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO web_options (name, value) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value`,
		options.NameTrustLevels, raw); err != nil {
		t.Fatal(err)
	}
	f.options.Invalidate()
}

func (f *recomputeFixture) seedLedger(t *testing.T, level int, tierID string, computedAt time.Time) {
	t.Helper()
	if err := f.ledger.SaveLedger(f.ctx, f.userID, trustlevels.LedgerEntry{
		Level: level, TierID: tierID, ComputedAt: computedAt,
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *recomputeFixture) ledgerEntry(t *testing.T) trustlevels.LedgerEntry {
	t.Helper()
	entry, found, err := f.ledger.LoadLedger(f.ctx, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("账本行不存在")
	}
	return entry
}

// TestRecomputeEmitsExactlyOneEventOnUpgrade 是「升级 → 发恰好一次事件」。
func TestRecomputeEmitsExactlyOneEventOnUpgrade(t *testing.T) {
	policy := recomputePolicy(t, "")
	fixture := newRecomputeFixture(t, policy)
	previous := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	fixture.seedLedger(t, 2, "resident", previous)

	outcome, err := fixture.recomputer.RecomputeUser(fixture.ctx, fixture.userID)
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if !outcome.EventEmitted {
		t.Fatalf("2 档 → 3 档必须发一次事件，实际 skipped=%q", outcome.Skipped)
	}
	if len(fixture.publisher.envelopes) != 1 {
		t.Fatalf("事件数 = %d，必须恰好 1 条（重复发送会让订阅方重复处理）",
			len(fixture.publisher.envelopes))
	}
	envelope := fixture.publisher.envelopes[0]
	if envelope.Name != appevents.TrustLevelChanged {
		t.Fatalf("event name = %q", envelope.Name)
	}
	if got := envelope.Payload["fromLevel"]; got != 2 {
		t.Fatalf("fromLevel = %v, want 2", got)
	}
	if got := envelope.Payload["toLevel"]; got != 3 {
		t.Fatalf("toLevel = %v, want 3", got)
	}
	if got := envelope.Payload["fromTierId"]; got != "resident" {
		t.Fatalf("fromTierId = %v", got)
	}
	if got := envelope.Payload["toTierId"]; got != mustPolicy(t, policy).TierID(3) {
		t.Fatalf("toTierId = %v", got)
	}
	// 载荷键必须与目录 PayloadFields 逐键对齐：生产者手写 map，漏一个就是订阅方
	// 静默拿到空值。
	definition, ok := appevents.FindDefinition(appevents.TrustLevelChanged)
	if !ok {
		t.Fatal("trust_level.changed 没有进目录")
	}
	for _, field := range definition.PayloadFields {
		if _, exists := envelope.Payload[field]; !exists {
			t.Fatalf("载荷缺少目录声明的字段 %q：%v", field, envelope.Payload)
		}
	}
	if entry := fixture.ledgerEntry(t); entry.Level != 3 {
		t.Fatalf("账本未更新：level = %d, want 3", entry.Level)
	}
}

// TestRecomputeSkipsUnchangedLevel 是「同级 → 不发」。
//
// 同时钉住：同级时**不刷新 computed_at**。刷新了就会让「上次完整求值时刻」失真，
// 每日 sweep 永远看不到「太久没算过」的人。
func TestRecomputeSkipsUnchangedLevel(t *testing.T) {
	policy := recomputePolicy(t, "")
	fixture := newRecomputeFixture(t, policy)
	previous := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	fixture.seedLedger(t, 3, mustPolicy(t, policy).TierID(3), previous)

	outcome, err := fixture.recomputer.RecomputeUser(fixture.ctx, fixture.userID)
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if outcome.EventEmitted || len(fixture.publisher.envelopes) != 0 {
		t.Fatalf("同级必须不发事件，实际发了 %d 条", len(fixture.publisher.envelopes))
	}
	if outcome.Skipped != "unchanged" {
		t.Fatalf("skipped = %q, want unchanged", outcome.Skipped)
	}
	if entry := fixture.ledgerEntry(t); !entry.ComputedAt.Equal(previous) {
		t.Fatalf("同级时账本 computed_at 被改写：%s → %s", previous, entry.ComputedAt)
	}
}

// TestRecomputeIncompleteNeitherEmitsNorOverwritesLedger 是 `Incomplete` 闸的骨头。
//
// 构造：账本先写 level=3，再让求值 incomplete（规则引用未声明的插件事实 key →
// 条件求值返 error → 引擎转 unknown → 该档不成立 + incomplete）。
// 断言：不发事件，且账本**仍是 3**（既没被 fail-closed 算出来的低值覆盖，也没被
// 刷成 computed_at=now）。
//
// 为什么账本写 3 而不是 2（这一条是变异逼出来的）：incomplete 下 fail-closed 的
// 结果恰好也是 TL2 时，「不覆盖」与「覆盖成同一个值」在断言上无法区分——把闸删掉
// 测试照样绿。只有当「账本值」≠「fail-closed 值」时，这条断言才真的看着闸。
//
// 为什么这条闸是唯一的防洪水阀：一次 DB 抖动会让所有带规则档位判不了，若此时按
// 「fail-closed 算出来的低等级」写账本并发事件，全站会同时收到一条「等级掉了」，
// 而且账本被真降级覆盖之后，抖动恢复时又会再发一条「等级回来了」。
func TestRecomputeIncompleteNeitherEmitsNorOverwritesLedger(t *testing.T) {
	policy := recomputePolicy(t, "")
	fixture := newRecomputeFixture(t, policy)
	previous := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	fixture.seedLedger(t, 3, mustPolicy(t, policy).TierID(3), previous)

	// 换成「TL3 规则引用未声明 factKey」的策略：数值仍达标，规则判不了。
	incomplete := recomputePolicy(t, `,"rule":`+ruleDoc(
		`{"op":"all","children":[{"type":"fact.count","params":{"factKey":"acme.undeclared.metric","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}]}`,
	))
	fixture.setPolicy(t, incomplete)

	outcome, err := fixture.recomputer.RecomputeUser(fixture.ctx, fixture.userID)
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if !outcome.Evaluation.Incomplete {
		t.Fatalf("夹具没造出 incomplete（求值 = %+v）", outcome.Evaluation)
	}
	if outcome.EventEmitted || len(fixture.publisher.envelopes) != 0 {
		t.Fatalf("incomplete 时必须不发事件，实际发了 %d 条", len(fixture.publisher.envelopes))
	}
	if outcome.LedgerWritten {
		t.Fatal("incomplete 时不许写账本")
	}
	entry := fixture.ledgerEntry(t)
	if entry.Level != 3 {
		t.Fatalf("incomplete 时账本被覆盖：level = %d, want 3（账本是下次对比的基准）", entry.Level)
	}
	if !entry.ComputedAt.Equal(previous) {
		t.Fatalf("incomplete 时账本 computed_at 被刷新：%s → %s", previous, entry.ComputedAt)
	}
}

// recomputePolicy 造一份「四档默认数值 + 可选规则」的策略。
// extraRule 直接拼到 TL3 的档位对象里（空串 = 无规则）。
func recomputePolicy(t *testing.T, extraRule string) string {
	t.Helper()
	const (
		tl1 = `{"days_visited":3,"comment_count":5}`
		tl2 = `{"days_visited":15,"topic_count":2,"comment_count":20,"like_received_count":5,"like_given_count":3}`
		tl3 = `{"days_visited":45,"topic_count":8,"comment_count":80,"like_received_count":30,"like_given_count":20,"bookmark_count":5}`
		tl4 = `{"days_visited":100,"topic_count":25,"comment_count":250,"like_received_count":100,"like_given_count":80,"bookmark_count":20}`
	)
	tier := func(id, requirements string, extra string) string {
		base := `{"id":"` + id + `","name":{"zh":"档","en":"Tier"},"icon":"leaf","requirements":` +
			requirements + `,"limits":{},"earnable":[]`
		if extra == "" {
			return base + `}`
		}
		return base + extra + `}`
	}
	return tiersJSON(
		tier("member", tl1, ""),
		tier("resident", tl2, ""),
		tier("veteran", tl3, extraRule),
		tier("leader", tl4, ""),
	)
}
