package trustlevels_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	conditions "github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 本文件不含 Test*，只提供「装载路径」A/B 的共享夹具与别名 SQL 断言。
// 两个消费方：
//
//   - loader_queries_postgres_integration_test.go  常开段：只断 SQL 条数与形状，进 CI；
//   - loader_latency_postgres_integration_test.go  可选段：10k 夹具，只做分位与 5ms 预算。
//
// 分区索引（按出现顺序，改这块时先看这里）：
//  1. 常量与「不能消失的 SQL」点名表       loaderBaselineSQL / loaderRequiredSQL
//  2. SQL tracer                          loaderQueryTracer
//  3. 夹具数据与 Reader（真读/stub 变异）   loaderActivity / loaderActivityReader
//  4. 策略来源（bootstrap 同形副本）        loaderPolicySource
//  5. 夹具本体（建库/迁移/连接池）           loaderFixture / newLoaderFixture / createLoaderDatabase
//  6. seed 与策略写入                      seed / setFeatureEnabled / setPolicy
//  7. 观察面（游标取样/单次装载/条数探针）     sampleIDs / load* / traceOneLoad / countQueries*
//  8. 策略形状构造                         loaderTierJSON / loaderPolicies
//  9. 条数断言                             assertLoaderQueries
//
// 口径边界（M5-P 的骨头，别混）：
//   - `TestRuleEvaluationLatency`（rule_eval_postgres_integration_test.go）两侧把
//     activity / policy 换成内存 stub，量到的是**规则单因子成本**，baseline 等于
//     什么都没做；**那里的数字不得被当成装载时延**。
//   - 这里两侧都走真库真读：主查询（3 LEFT JOIN）+ ListUserRoleKeys +
//     ListUserPermissions + `SELECT created_at, kind FROM users` + `user_activity`，
//     policy 走 options 的真读（features.trust_levels + trust.levels + trust.new_user_days）。
//     A 与 B 只差「档位上有没有规则树」，同一二进制、同一夹具、同一连接池。

const (
	// loaderBaselineSQL 是侧 A（所有档位无规则）单次装载实测的 SQL 条数。
	// 5 = 主查询 + ListUserRoleKeys + ListUserPermissions + users(created_at,kind) + user_activity。
	loaderBaselineSQL = 5
	// loaderRuleSQL 是侧 B 相对 A 多出的条数：Prepare 按**条件类型**分组，本夹具
	// 全部档位共用 `identity.account_age` 一个类型，所以只多 1 条。
	loaderRuleSQL = 1
	// loaderWithRulesSQL 是侧 B 单次装载的条数下界。
	loaderWithRulesSQL = loaderBaselineSQL + loaderRuleSQL
	// loaderMutateActivityStubEnv 是变异开关：把 activity 读换成常量（一条 SQL 都不发）。
	// 常开段与可选段共用它——前者的条数断言必须因此变红。
	loaderMutateActivityStubEnv = "SFORUM_TRUST_PERF_MUTATE_ACTIVITY_STUB"
	// loaderAccountAgeSQL 是 `identity.account_age` 的条件读 SQL（Conditions 包内常量）。
	loaderAccountAgeSQL = "SELECT users.created_at FROM users WHERE users.id = $1"
	// loaderSQLCountUsers 是常开段的夹具规模：条数与形状是常数，20 行足够。
	loaderSQLCountUsers = 20
)

// loaderRequiredSQL 逐条点名装载链上不能消失的 SQL。
// 少任何一条都说明某一段被 stub 掉了（或装配层换了实现），这是反证面。
var loaderRequiredSQL = []string{
	"LEFT JOIN user_appearance_preferences", // 主查询（含 3 个 LEFT JOIN）
	"FROM user_roles",                       // ListUserRoleKeys（真 RBAC 读）
	"role_permissions",                      // ListUserPermissions（真 policy 读）
	"SELECT created_at, kind FROM users",    // applyTrustLevel 的 users 行（R7：全请求唯一一次）
	"FROM user_activity",                    // 真 activity 读（stub 掉就整条消失）
}

// loaderQueryTracer 按需统计 SQL：计时阶段关闭（只留一次原子读），
// 条数断言阶段打开。这样 tracer 的 mutex/append 不污染延迟样本。
type loaderQueryTracer struct {
	enabled atomic.Bool
	mu      sync.Mutex
	queries []string
}

func (q *loaderQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !q.enabled.Load() {
		return ctx
	}
	q.mu.Lock()
	q.queries = append(q.queries, data.SQL)
	q.mu.Unlock()
	return ctx
}

func (q *loaderQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (q *loaderQueryTracer) setEnabled(enabled bool) {
	q.enabled.Store(enabled)
}

func (q *loaderQueryTracer) reset() {
	q.mu.Lock()
	q.queries = nil
	q.mu.Unlock()
}

func (q *loaderQueryTracer) snapshot() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, len(q.queries))
	copy(out, q.queries)
	return out
}

// loaderActivity 是夹具里每个用户的行为统计（全表同值）。
//
// 取值让 TL1..TL3 达标、TL4 六项门槛**全部**不过（days_visited 60<100、topic_count
// 10<25、comment_count 100<250、like_received 40<100、like_given 25<80、bookmark 8<20）。
// 所以 B 侧数值预筛排除 TL4 与「某一个维度」无关，是整档都不过；合成根里仍然只有
// `identity.account_age` 一个条件类型 → 规则成本 = 1 条 SQL，与档数无关。
var loaderActivity = trustlevels.Activity{
	DaysVisited:       60,
	TopicCount:        10,
	CommentCount:      100,
	LikeReceivedCount: 40,
	LikeGivenCount:    25,
	BookmarkCount:     8,
}

const loaderExpectedLevel = 3

// loaderActivityReader 与 bootstrap 的 trustLevelActivityReader 同形：
// 复用 Badges 的 user_activity 读入口，不在这里写第二份 SQL。
//
// **反例（常开段存在的理由）**：把生产这份适配器换成缓存读，是性能优化时最自然的
// 动作；那时 SQL 条数会从 5 掉到 4、`FROM user_activity` 消失。如果条数断言躲在
// 人工环境变量后面，整条 CI 都不会红，线上全员等级静默掉 TL0（读不到统计 = 未解析）。
type loaderActivityReader struct {
	store *badges.PostgresActivityStore
}

func (r loaderActivityReader) ActivityFor(ctx context.Context, userID int64) (trustlevels.Activity, error) {
	counters, err := r.store.GetCounters(ctx, userID)
	if err != nil {
		return trustlevels.Activity{}, err
	}
	return trustlevels.Activity{
		TopicCount:        counters.TopicCount,
		CommentCount:      counters.CommentCount,
		LikeReceivedCount: counters.LikeReceivedCount,
		LikeGivenCount:    counters.LikeGivenCount,
		BookmarkCount:     counters.BookmarkCount,
		DaysVisited:       counters.DaysVisited,
	}, nil
}

// stubLoaderActivity 是变异用的替身：返回常量、**一条 SQL 都不发**。
type stubLoaderActivity struct{}

func (stubLoaderActivity) ActivityFor(context.Context, int64) (trustlevels.Activity, error) {
	return loaderActivity, nil
}

// loaderPolicySource 是 bootstrap 里 trustLevelPolicySource 的测试侧同形实现。
//
// 为什么在这里复制一份：生产那个类型未导出，而本测试的写作用域只允许动 TrustLevels
// 包。三次读键（features.trust_levels → trust.levels → trust.new_user_days）与解码
// 顺序逐行对应 `bootstrap/trust_levels_wiring.go`；生产侧若改了读键顺序，这里必须同步。
type loaderPolicySource struct {
	options *options.Service
}

func (s loaderPolicySource) Policy(ctx context.Context) (trustlevels.Policy, error) {
	if s.options == nil {
		return trustlevels.Policy{}, trustlevels.ErrDisabled
	}
	flags, err := s.options.FeatureFlags(ctx)
	if err != nil {
		return trustlevels.Policy{}, err
	}
	if !flags.TrustLevels {
		return trustlevels.Policy{}, trustlevels.ErrDisabled
	}
	raw, err := s.options.WebOption(ctx, options.NameTrustLevels)
	if err != nil {
		return trustlevels.Policy{}, err
	}
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		return trustlevels.Policy{}, err
	}
	trust, err := s.options.TrustPolicy(ctx)
	if err != nil {
		return trustlevels.Policy{}, err
	}
	if trust.NewUserDays >= 0 {
		policy.NewUserDays = trust.NewUserDays
	}
	return policy, nil
}

// loaderFixture 是「一次性库 + 完整迁移 + 夹具 + 带 tracer 的池」。
type loaderFixture struct {
	ctx     context.Context
	pool    *pgxpool.Pool
	tracer  *loaderQueryTracer
	store   *identity.PostgresStore
	options *options.Service
}

// newLoaderFixture 造夹具。users 只影响表的行数：SQL 条数与形状是常数，
// 10k 与 20 逐字相同（10k 只对分位有意义）。
func newLoaderFixture(t *testing.T, users int) *loaderFixture {
	t.Helper()
	ctx := context.Background()
	baseURL := testdb.IsolatedURL(t)
	databaseName := fmt.Sprintf("trust_loader_%d", time.Now().UnixNano())
	cleanupDatabase := createLoaderDatabase(t, ctx, baseURL, databaseName)
	t.Cleanup(cleanupDatabase)

	tracer := &loaderQueryTracer{}
	poolConfig, err := pgxpool.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Database = databaseName
	poolConfig.ConnConfig.Tracer = tracer
	poolConfig.MaxConns = 4
	poolConfig.MinConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// options.Service 必须在 seed 之前就位：seed 会写产品开关并让缓存失效。
	optionService := options.NewService(options.NewPostgresStore(pool))
	fixture := &loaderFixture{ctx: ctx, pool: pool, tracer: tracer, options: optionService}
	if err := fixture.seed(t, users); err != nil {
		t.Fatal(err)
	}

	var activity trustlevels.ActivityReader = loaderActivityReader{store: badges.NewPostgresActivityStore(pool)}
	if os.Getenv(loaderMutateActivityStubEnv) == "1" {
		activity = stubLoaderActivity{}
	}
	resolver := trustlevels.NewResolver(activity, loaderPolicySource{options: optionService}).
		WithRuleEvaluators(func(ctx context.Context, userID int64) (trustlevels.RuleEvaluator, error) {
			conditionResolver, err := conditions.NewResolver(pool, userID)
			if err != nil {
				return nil, err
			}
			return trustlevels.NewRuleEvaluator(conditionResolver), nil
		})
	fixture.store = identity.NewPostgresStore(pool).WithTrustLevelSource(resolver)
	return fixture
}

// createLoaderDatabase 建一个独立的库并保证它是「生产形状」。
//
// 快路径：`scripts/test-api-isolated.sh` 已经把共享测试库迁移完，直接拿它当模板复制
// （毫秒级）——常开段每次 CI 都跑，不能再付一遍 171 条迁移的代价。慢路径：模板被别的
// 会话占用（55006）或环境里根本没有模板库时，自己建库跑完整迁移链，结果等价。
func createLoaderDatabase(t *testing.T, ctx context.Context, baseURL, databaseName string) func() {
	t.Helper()
	baseConfig, err := pgx.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	templateDatabase := baseConfig.Database
	adminConfig := baseConfig.Copy()
	if adminConfig.Database != "postgres" {
		adminConfig.Database = "postgres"
	}
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	quoted := pgx.Identifier{databaseName}.Sanitize()
	drop := func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	}

	fastPath := false
	if templateDatabase != "" && templateDatabase != "postgres" {
		// 模板路径**继承模板库的行**：今天的断言只看 SQL 条数与形状，所以免疫；
		// 将来若在这里加行数/内容断言，模板路径会变成 flaky 源（别人留下的行会进来）。
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted+" TEMPLATE "+pgx.Identifier{templateDatabase}.Sanitize()); err == nil {
			fastPath = true
		}
	}
	if !fastPath {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
			drop()
			t.Fatalf("create loader fixture database: %v", err)
		}
	}
	// 不论哪条路径，都在副本上补一次 Up 并断言 schema 版本：
	//   - 模板落后（上一次跑用的是旧 checkout）时，Up 会把副本补齐，而不是让失败信息
	//     指到「SQL 条数不对」上（那会把下一个人带偏半天）；
	//   - 版本号进日志，出问题第一眼就能看出「库是旧的」还是「断言错了」。
	t.Logf("fastPath=%v template=%q", fastPath, templateDatabase)
	ensureLoaderSchemaCurrent(t, ctx, baseURL, databaseName, drop)
	return drop
}

// ensureLoaderSchemaCurrent 把副本迁到本 checkout 的最新版本并断言没有残留 pending。
//
// 它就是 R7 的版本断言：**先把库补齐，再证明补齐了**。失败信息带版本号，
// 因为「模板库落后」与「SQL 条数变了」在旧实现里长得一模一样。
func ensureLoaderSchemaCurrent(t *testing.T, ctx context.Context, baseURL, databaseName string, drop func()) {
	t.Helper()
	migrationConfig, err := pgx.ParseConfig(baseURL)
	if err != nil {
		drop()
		t.Fatal(err)
	}
	migrationConfig.Database = databaseName
	sqlDB := stdlib.OpenDB(*migrationConfig)
	defer func() { _ = sqlDB.Close() }()
	provider, err := goose.NewProvider(
		goose.DialectPostgres, sqlDB, migrations.Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		drop()
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		drop()
		t.Fatalf("migrate loader fixture database: %v", err)
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		drop()
		t.Fatalf("read loader fixture schema version: %v", err)
	}
	t.Logf("schema version=%d target=%d", current, target)
	if pending, err := provider.HasPending(ctx); err != nil {
		drop()
		t.Fatalf("check loader fixture schema pending: %v", err)
	} else if pending {
		drop()
		t.Fatalf("夹具库 schema 仍是旧版（current=%d target=%d）：模板库落后于本 checkout。"+
			"先重跑 ./scripts/test-api-isolated.sh 让共享库迁移到最新，不要先去改 SQL 条数断言", current, target)
	}
}

// seed 造夹具：users + user_activity + 每人一个内置 member 角色 + 产品开关落库。
func (f *loaderFixture) seed(t *testing.T, users int) error {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name,
		                   status, email_verified_at, created_at, kind)
		SELECT 'perf_' || i, 'perf_' || i, 'perf_' || i || '@example.test',
		       'perf_' || i || '@example.test', 'Perf ' || i,
		       'active', now(), now() - interval '400 days', 'human'
		FROM generate_series(1, $1) AS i`, users); err != nil {
		return fmt.Errorf("seed users: %w", err)
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO user_activity (user_id, topic_count, comment_count, like_received_count,
		                           like_given_count, bookmark_count, days_visited, last_visited_on)
		SELECT id, $1, $2, $3, $4, $5, $6, current_date FROM users`,
		loaderActivity.TopicCount, loaderActivity.CommentCount, loaderActivity.LikeReceivedCount,
		loaderActivity.LikeGivenCount, loaderActivity.BookmarkCount, loaderActivity.DaysVisited,
	); err != nil {
		return fmt.Errorf("seed user_activity: %w", err)
	}
	// 真 RBAC 读：每人挂一个内置 member 角色（迁移链已播种该角色及其权限）。
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT users.id, roles.id FROM users CROSS JOIN roles WHERE roles.key = 'member'`); err != nil {
		return fmt.Errorf("seed user_roles: %w", err)
	}
	// 产品开关显式落库：A/B 两侧都必须「功能开着」，否则量的就是关闭态。
	return f.setFeatureEnabled(true)
}

// setFeatureEnabled 写 features.trust_levels 并让 options 缓存失效。
func (f *loaderFixture) setFeatureEnabled(enabled bool) error {
	value := "disabled"
	if enabled {
		value = "enabled"
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO web_options (name, value) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value`, options.NameFeatureTrustLevels, value); err != nil {
		return fmt.Errorf("write feature flag: %w", err)
	}
	f.options.Invalidate()
	return nil
}

// setPolicy 把 trust.levels 写成给定策略并让 options 缓存失效（下一次装载重建缓存）。
func (f *loaderFixture) setPolicy(t *testing.T, raw string) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO web_options (name, value) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value`, options.NameTrustLevels, raw); err != nil {
		t.Fatalf("write trust.levels: %v", err)
	}
	f.options.Invalidate()
}

// firstUserID 返回夹具里的一个用户（条数探针用，规模无关）。
func (f *loaderFixture) firstUserID() int64 {
	var id int64
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		panic(fmt.Sprintf("load fixture user: %v", err))
	}
	return id
}

// sampleIDs 沿主键均匀取样，避免「只取前 N 个」把索引页热情形测成同一页。
func (f *loaderFixture) sampleIDs(t *testing.T, limit int) []int64 {
	t.Helper()
	ids := make([]int64, 0, limit)
	rows, err := f.pool.Query(f.ctx, `SELECT id FROM users WHERE id % 10 = 0 ORDER BY id LIMIT $1`, limit)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		t.Fatal("夹具里没有可采样用户")
	}
	return ids
}

// load 走一次真装载并校验等级真的解析出来了（0 = 未解析，说明链断在前面）。
func (f *loaderFixture) load(t *testing.T, userID int64) time.Duration {
	t.Helper()
	return f.loadExpectLevel(t, userID, loaderExpectedLevel)
}

// loadExpectLevel 是 load 的可指定等级版本（关闭态对照要断言「未解析」= 0）。
func (f *loaderFixture) loadExpectLevel(t *testing.T, userID int64, expected int) time.Duration {
	t.Helper()
	start := time.Now()
	current, err := f.store.GetCurrentUser(f.ctx, userID)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetCurrentUser(%d): %v", userID, err)
	}
	if current.TrustLevel != expected {
		t.Fatalf("user %d 解析出 TL%d，期望 TL%d（0 表示装载未解析）",
			userID, current.TrustLevel, expected)
	}
	return elapsed
}

// countQueries 量「写入策略 + 预热」之后**单次装载**的 SQL 序列。
func (f *loaderFixture) countQueries(t *testing.T, raw string, userID int64) []string {
	t.Helper()
	f.setPolicy(t, raw)
	f.load(t, userID) // 预热：重建 options 缓存 + 保证连接已建好
	return f.traceOneLoad(t, userID)
}

// countQueriesFlagOff 量「产品开关关闭」时的 SQL 序列。
//
// 这是**功能关闭**基线，不是侧 A：关闭态里 policy 与 activity 两段都短路，所以它
// 恰好比侧 A 少一条（activity 读）。量它的目的是证明开关真的存在且真的省事——不是
// 把它当成本单的基线。
//
// 为什么常开段只断条数、不断「关闭态到侧 A 的时延差」：那比较的是「功能开 vs 关」，
// 不是本里程碑（加规则）的增量；而单条主键点查的时延量级要 10k 夹具 + 分位才有意义，
// 属于可选段的事。常开段的职责是形状，不是数字。
func (f *loaderFixture) countQueriesFlagOff(t *testing.T, policyA string, userID int64) []string {
	t.Helper()
	if err := f.setFeatureEnabled(false); err != nil {
		t.Fatal(err)
	}
	f.setPolicy(t, policyA)
	f.loadExpectLevel(t, userID, 0) // 关闭态：等级保持「未解析」（0）
	queries := f.traceOneLoad(t, userID)
	if err := f.setFeatureEnabled(true); err != nil {
		t.Fatal(err)
	}
	f.setPolicy(t, policyA)
	return queries
}

func (f *loaderFixture) traceOneLoad(t *testing.T, userID int64) []string {
	t.Helper()
	f.tracer.setEnabled(true)
	defer f.tracer.setEnabled(false)
	f.tracer.reset()
	if _, err := f.store.GetCurrentUser(f.ctx, userID); err != nil {
		t.Fatalf("probe GetCurrentUser(%d): %v", userID, err)
	}
	return f.tracer.snapshot()
}

// loaderTierJSON 造一条合法 v2 档位；rule 为空表示「本档无规则」（= 今天的形状）。
func loaderTierJSON(id, requirements, rule string) string {
	base := `{"id":"` + id + `","name":{"zh":"档","en":"Tier"},"icon":"leaf","requirements":` +
		requirements + `,"limits":{},"earnable":[]`
	if rule == "" {
		return base + `}`
	}
	return base + `,"rule":` + rule + `}`
}

// loaderPolicies 返回三种策略形状：A 无规则、B 全档带规则、B1 只 TL3 带规则。
//
// 数值门槛在三种形状里逐档相同——A/B 的差别只有规则树，符合「同一份夹具、只翻策略
// 形状」的要求。B 与 B1 的对比是「查询数随条件类型数、不随档位数」的判据：
// 档位从 3 档带规则收到 1 档，SQL 条数必须不变。
func loaderPolicies(t *testing.T) (a, b, bSingle string) {
	t.Helper()
	rule := ruleDoc(`{"op":"all","children":[{"type":"identity.account_age","cmp":{"op":"gte","value":1}}]}`)
	const (
		tl1 = `{"days_visited":3,"comment_count":5}`
		tl2 = `{"days_visited":15,"topic_count":2,"comment_count":20,"like_received_count":5,"like_given_count":3}`
		tl3 = `{"days_visited":45,"topic_count":8,"comment_count":80,"like_received_count":30,"like_given_count":20,"bookmark_count":5}`
		tl4 = `{"days_visited":100,"topic_count":25,"comment_count":250,"like_received_count":100,"like_given_count":80,"bookmark_count":20}`
	)
	a = tiersJSON(
		loaderTierJSON("member", tl1, ""),
		loaderTierJSON("resident", tl2, ""),
		loaderTierJSON("veteran", tl3, ""),
		loaderTierJSON("leader", tl4, ""),
	)
	b = tiersJSON(
		loaderTierJSON("member", tl1, rule),
		loaderTierJSON("resident", tl2, rule),
		loaderTierJSON("veteran", tl3, rule),
		loaderTierJSON("leader", tl4, rule),
	)
	bSingle = tiersJSON(
		loaderTierJSON("member", tl1, ""),
		loaderTierJSON("resident", tl2, ""),
		loaderTierJSON("veteran", tl3, rule),
		loaderTierJSON("leader", tl4, ""),
	)
	// 三种形状都必须能被严格解码（解码失败 = 测的就不是生产策略）。
	mustPolicy(t, a)
	mustPolicy(t, b)
	mustPolicy(t, bSingle)
	return a, b, bSingle
}

// assertLoaderQueries 是反证断言本体：条数下界 + 关键 SQL 点名。
func assertLoaderQueries(t *testing.T, side string, queries []string, minimum int, requiredExtra string) {
	t.Helper()
	if len(queries) < minimum {
		t.Fatalf("%s：单次装载只有 %d 条 SQL，低于基线 %d 条——某一段被 stub 掉了：\n%s",
			side, len(queries), minimum, strings.Join(queries, "\n---\n"))
	}
	joined := strings.Join(queries, "\n---\n")
	for _, required := range loaderRequiredSQL {
		if !strings.Contains(joined, required) {
			t.Fatalf("%s：装载链缺少关键 SQL %q（真读被替换成内存读）：\n%s",
				side, required, joined)
		}
	}
	if requiredExtra != "" && !strings.Contains(joined, requiredExtra) {
		t.Fatalf("%s：缺少规则求值 SQL %q（规则根本没被求值）：\n%s",
			side, requiredExtra, joined)
	}
}
