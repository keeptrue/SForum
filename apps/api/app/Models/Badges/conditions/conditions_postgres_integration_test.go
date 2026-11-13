package conditions_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 条件解析器的真库集成测试。
//
// 用**独立的 schema + 手写 DDL**（与 `Badges` 包的集成测试同一套路），而不是复用
// 真实迁移链：这里要验的是「条件语义」——窗口 vs 累计、软删是否计入、只认 active
// 的绑定——而不是迁移能否重放（那由 migrations 包守）。手写 DDL 的代价是列名漂移
// 抓不到，所以每个条件至少有一条 allowed/denied 两态断言，任何列名写错都会直接报错。
//
// 解析器 SQL 用的是**不带 schema 前缀**的表名（与 `postgres_store.go` 一致），
// 靠 `search_path` 落到本 fixture 的 schema。
const conditionsSchemaSQL = `
CREATE TABLE users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  email_verified_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_activity (
  user_id BIGINT PRIMARY KEY,
  topic_count BIGINT NOT NULL DEFAULT 0,
  comment_count BIGINT NOT NULL DEFAULT 0,
  like_received_count BIGINT NOT NULL DEFAULT 0,
  like_given_count BIGINT NOT NULL DEFAULT 0,
  bookmark_count BIGINT NOT NULL DEFAULT 0,
  days_visited INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE user_visits (
  user_id BIGINT NOT NULL,
  visited_on DATE NOT NULL,
  PRIMARY KEY (user_id, visited_on)
);

CREATE TABLE categories (id BIGSERIAL PRIMARY KEY, slug TEXT NOT NULL UNIQUE);
CREATE TABLE tags (id BIGSERIAL PRIMARY KEY, slug TEXT NOT NULL UNIQUE);

CREATE TABLE topics (
  id BIGSERIAL PRIMARY KEY,
  category_id BIGINT NOT NULL,
  author_user_id BIGINT,
  status TEXT NOT NULL DEFAULT 'active',
  is_pinned BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE topic_tags (topic_id BIGINT NOT NULL, tag_id BIGINT NOT NULL, PRIMARY KEY (topic_id, tag_id));

CREATE TABLE comments (
  id BIGSERIAL PRIMARY KEY,
  topic_id BIGINT NOT NULL,
  author_user_id BIGINT,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE content_reactions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  reaction TEXT NOT NULL DEFAULT 'like',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 与生产同形的 roster 索引：P-01 要证明窗口「获赞」能用上它，不需要新增索引。
CREATE INDEX content_reactions_roster_idx
  ON content_reactions (target_type, target_id, reaction, created_at DESC, id DESC);

CREATE TABLE content_bookmarks (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  visibility TEXT NOT NULL DEFAULT 'private',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE identity_external_links (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL,
  provider_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active'
);

CREATE TABLE roles (id BIGSERIAL PRIMARY KEY, key TEXT NOT NULL UNIQUE, is_enabled BOOLEAN NOT NULL DEFAULT TRUE);
CREATE TABLE user_roles (user_id BIGINT NOT NULL, role_id BIGINT NOT NULL, PRIMARY KEY (user_id, role_id));
CREATE TABLE user_profiles (user_id BIGINT PRIMARY KEY, avatar_attachment_id BIGINT);

CREATE TABLE badges (id BIGSERIAL PRIMARY KEY, slug TEXT NOT NULL UNIQUE);
CREATE TABLE user_badges (user_id BIGINT NOT NULL, badge_id BIGINT NOT NULL, PRIMARY KEY (user_id, badge_id));

CREATE TABLE user_fact_counters (
  user_id BIGINT NOT NULL, fact_key TEXT NOT NULL, variant TEXT NOT NULL DEFAULT '',
  total BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (user_id, fact_key, variant)
);

CREATE TABLE badge_facts (
  id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL, fact_key TEXT NOT NULL,
  variant TEXT NOT NULL DEFAULT '', delta BIGINT NOT NULL,
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

type fixture struct {
	ctx  context.Context
	pool *pgxpool.Pool
	// now 是本次 fixture 的「今天」来源，在创建时取一次并同时喂给两侧：
	//   - 写侧：日历日参数（created_at / visited_on 的基准日）；
	//   - 读侧：`Resolver.WithClock`（resolver 的 today()）。
	// 两侧同源之后，断言不再依赖「测试运行到哪一刻」，也不会在跨零点时半路换日。
	//
	// 它**不改变**日界口径：仍然是 API 进程本地日（生产路径 `time.Now()` 的同一口径），
	// 只是把「同一次测试里的今天」钉住。
	now time.Time
}

// todayDate 返回与 `Resolver.today()` 同源的日历日（进程本地日，YYYY-MM-DD）。
//
// fixture 里凡是表达「N 个日历日前」的地方都必须用它，不能用
// `now() - make_interval(days => N)` 或 `CURRENT_DATE`：前者是 24 小时算术
// （跨零点会落到前一个日历日），后者取的是 PG 会话时区的日界，
// 两者都与 resolver「Go 传进程本地日」的口径不同源。
func (f *fixture) todayDate() string {
	return f.now.Format(time.DateOnly)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWithSessionTimeZone(t, "")
}

// newFixtureWithSessionTimeZone 与 newFixture 相同，但把 PG 会话时区钉死。
//
// 只给「日界来源」的回归测试用：`identity.account_age` 曾经把 created_at 在**会话
// 时区**里折成日期、再与 Go 的进程本地日相减，于是会话时区 ≠ 进程时区时多算一天。
// 要连续证伪那个实现，必须能显式设定会话时区，而不是依赖测试集群的默认值。
func newFixtureWithSessionTimeZone(t *testing.T, sessionTimeZone string) *fixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("conditions_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	// `search_path` 带 public 是全仓既有模式（计划 §13 残留 14）：漏建的表会静默落到
	// 真实 public 表，所以新增被查询的表必须同批加进上面的 DDL。
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	if sessionTimeZone != "" {
		// 会话级 GUC：连接池里每条连接都带这个启动参数，而不是 SET LOCAL 那种
		// 「只在当前连接上生效」的写法。
		config.ConnConfig.RuntimeParams["timezone"] = sessionTimeZone
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, conditionsSchemaSQL); err != nil {
		pool.Close()
		admin.Close()
		t.Fatalf("apply conditions fixture DDL: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
	})
	return &fixture{ctx: ctx, pool: pool, now: time.Now()}
}

func (f *fixture) user(t *testing.T, name string, createdAgoDays int) int64 {
	t.Helper()
	var id int64
	// created_at 表达的是「N 个日历日前的那个本地日起点」，不是「N×24 小时前的一个
	// 瞬间」：后者在本地 00:00–08:00（PG 会话仍是 UTC 时）会落到前一个日历日，于是
	// `identity.account_age` 多算一天——测试红的真正原因是这行 fixture。
	//
	// 时区处理的方式：日期在 **Go 侧**按 `f.now` 的 location 构造（本地午夜），再以
	// timestamptz 原样传参。不能写成 SQL 的 `$2::date - N`：那样会先在会话时区里
	// 把字符串折成日期、再按会话时区落到午夜，进程时区与会话时区偏移方向相反时
	// （例如进程 America/New_York、会话 UTC）又会位移一天。
	createdAt := time.Date(
		f.now.Year(), f.now.Month(), f.now.Day()-createdAgoDays,
		0, 0, 0, 0, f.now.Location(),
	)
	if err := f.pool.QueryRow(f.ctx,
		`INSERT INTO users (username, created_at) VALUES ($1, $2) RETURNING id`,
		name, createdAt).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func (f *fixture) resolver(t *testing.T, userID int64, factKeys ...string) *conditions.Resolver {
	t.Helper()
	resolver, err := conditions.NewResolver(f.pool, userID, factKeys...)
	if err != nil {
		t.Fatal(err)
	}
	return resolver.WithClock(func() time.Time { return f.now })
}

// eval 走完整链路：Prepare（按 type 分组一次查询）→ ruleengine.Eval（三值）。
func (f *fixture) eval(t *testing.T, userID int64, rule string, factKeys ...string) (bool, ruleengine.Result) {
	t.Helper()
	node, err := ruleengine.Decode([]byte(rule))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := ruleengine.Validate(node, conditions.Core()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	resolver := f.resolver(t, userID, factKeys...)
	if err := resolver.Prepare(f.ctx, node); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	result, err := ruleengine.Eval(f.ctx, node, resolver)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	return result.Matched, result
}

// TestRegistryCatalogIsCompleteAndValidatable 固定「目录里每条条件都能被引擎校验」。
func TestRegistryCatalogIsCompleteAndValidatable(t *testing.T) {
	registry := conditions.Core()
	if len(registry.Definitions()) != 13 {
		t.Fatalf("内置条件应为 13 条，实际 %d", len(registry.Definitions()))
	}
	for _, def := range registry.Definitions() {
		if def.LabelZh == "" || def.LabelEn == "" {
			t.Fatalf("%s 缺少双语 label", def.Key)
		}
		if def.ValueKind == ruleengine.ValueCount && len(def.Compare) == 0 {
			t.Fatalf("%s 是计数条件但没有允许的 compare", def.Key)
		}
		if def.ValueKind == ruleengine.ValueBoolean && len(def.Compare) != 0 {
			t.Fatalf("%s 是布尔条件但声明了 compare", def.Key)
		}
		for _, spec := range def.Params.Specs {
			if spec.Kind == ruleengine.ParamObjectRef && spec.RefKind == "" {
				t.Fatalf("%s 的 %s 是 objectRef 但没有 refKind（编辑器无法渲染候选）", def.Key, spec.Name)
			}
		}
	}
	// 发布面过滤：没有扩展声明 factKey 时 fact.count 不出现，但校验/求值仍认它
	// （否则 M3 存下的规则会通不过校验）。
	published := registry.PublishableDefinitions(nil)
	for _, def := range published {
		if def.Key == conditions.KeyFactCount {
			t.Fatal("fact.count 没有候选时不应出现在后台条件目录里")
		}
	}
	if len(published) != len(registry.Definitions())-1 {
		t.Fatalf("发布面应比全量少 1 条，实际 %d/%d", len(published), len(registry.Definitions()))
	}
	if _, ok := registry.ConditionMeta(conditions.KeyFactCount); !ok {
		t.Fatal("fact.count 必须仍在 Catalog 白名单里（M3 规则要能校验通过）")
	}
	withFacts := registry.PublishableDefinitions([]conditions.FactCandidate{{Key: "wiki.edit"}})
	if len(withFacts) != len(registry.Definitions()) {
		t.Fatalf("有候选时 fact.count 应出现，实际 %d", len(withFacts))
	}
	// 数量对、内容空是最容易写出来的假绿：必须断言候选真的填进去了。
	var factOptions []string
	for _, def := range withFacts {
		if def.Key != conditions.KeyFactCount {
			continue
		}
		for _, spec := range def.Params.Specs {
			if spec.Name == "factKey" {
				factOptions = spec.Options
			}
		}
	}
	if len(factOptions) != 1 || factOptions[0] != "wiki.edit" {
		t.Fatalf("factKey 的 Options 应恰含 wiki.edit，得到 %v", factOptions)
	}
	// 且不能污染只读单例：全集里的 fact.count 仍然没有候选。
	for _, def := range registry.Definitions() {
		if def.Key != conditions.KeyFactCount {
			continue
		}
		for _, spec := range def.Params.Specs {
			if spec.Name == "factKey" && len(spec.Options) != 0 {
				t.Fatalf("PublishableDefinitions 污染了单例：%v", spec.Options)
			}
		}
	}

	// 目录必须实现 ruleengine.Catalog：随机一个 key 能查到。
	if _, ok := registry.ConditionMeta(conditions.KeyActivityCount); !ok {
		t.Fatal("Catalog 查不到 activity.count")
	}
}

func TestActivityCountCumulativeAndWindow(t *testing.T) {
	f := newFixture(t)
	userID := f.user(t, "alice", 30)
	f.exec(t, `INSERT INTO user_activity (user_id, topic_count, comment_count) VALUES ($1, 5, 2)`, userID)

	cumulative := `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":5}}}`
	matched, result := f.eval(t, userID, cumulative)
	if !matched || result.Incomplete {
		t.Fatalf("累计 5 应满足 gte 5: %+v", result)
	}
	denied := `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":6}}}`
	if matched, result := f.eval(t, userID, denied); matched || result.Incomplete {
		t.Fatalf("累计 5 不应满足 gte 6: %+v", result)
	}

	// 窗口：两条 40 天前的主题 + 一条今天的；user_activity 故意写 5（累计口径）。
	f.exec(t, `INSERT INTO categories (slug) VALUES ('general')`)
	f.exec(t, `INSERT INTO topics (category_id, author_user_id, created_at)
	          SELECT (SELECT id FROM categories WHERE slug='general'), $1, now() - make_interval(days => d)
	          FROM unnest(ARRAY[40, 40, 0]) AS d`, userID)
	windowed := `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":2}}}`
	if matched, result := f.eval(t, userID, windowed); matched || result.Incomplete {
		t.Fatalf("30 天窗口内只有 1 条主题，不应满足 gte 2: %+v", result)
	}
	windowedOne := `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":1}}}`
	if matched, result := f.eval(t, userID, windowedOne); !matched || result.Incomplete {
		t.Fatalf("30 天窗口内 1 条主题应满足 gte 1: %+v", result)
	}
	// 窗口与累计必须给出不同的数（证明没有偷偷用累计值）。
	wide := `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"recent_days","days":90}},"cmp":{"op":"gte","value":3}}}`
	if matched, result := f.eval(t, userID, wide); !matched || result.Incomplete {
		t.Fatalf("90 天窗口内 3 条主题应满足 gte 3: %+v", result)
	}
}

func TestActivityCountWindowedLikeReceived(t *testing.T) {
	f := newFixture(t)
	authorID := f.user(t, "author", 30)
	likerID := f.user(t, "liker", 30)
	f.exec(t, `INSERT INTO categories (slug) VALUES ('general')`)
	f.exec(t, `INSERT INTO topics (id, category_id, author_user_id, created_at)
	          VALUES (1, (SELECT id FROM categories WHERE slug='general'), $1, now() - interval '40 days')`, authorID)
	f.exec(t, `INSERT INTO comments (id, topic_id, author_user_id, created_at)
	          VALUES (1, 1, $1, now())`, authorID)
	// 一条 40 天前的赞（落在 30 天窗口外）+ 一条今天的赞。
	f.exec(t, `INSERT INTO content_reactions (user_id, target_type, target_id, reaction, created_at)
	          VALUES ($1,'topic',1,'like', now() - interval '40 days'), ($1,'comment',1,'like', now())`, likerID)

	windowed := `{"v":1,"root":{"type":"activity.count","params":{"metric":"like_received_count","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":2}}}`
	if matched, result := f.eval(t, authorID, windowed); matched || result.Incomplete {
		t.Fatalf("30 天窗口内只有 1 个赞，不应满足 gte 2: %+v", result)
	}
	windowedOne := `{"v":1,"root":{"type":"activity.count","params":{"metric":"like_received_count","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":1}}}`
	if matched, result := f.eval(t, authorID, windowedOne); !matched || result.Incomplete {
		t.Fatalf("30 天窗口内 1 个赞应满足 gte 1: %+v", result)
	}
}

// TestActivityCountExcludesSelfLikes 是 §5.2.2 第 1 条的证据：窗口「获赞」必须
// 排除自赞。写路径对自赞返 0（累计列本来就不含），窗口版若含，同一枚徽章会出现
// 「累计 0 但最近 30 天 5」的自相矛盾。
func TestActivityCountExcludesSelfLikes(t *testing.T) {
	f := newFixture(t)
	authorID := f.user(t, "selfliker", 30)
	otherID := f.user(t, "other", 30)
	f.exec(t, `INSERT INTO categories (slug) VALUES ('general')`)
	f.exec(t, `INSERT INTO topics (id, category_id, author_user_id) VALUES (1, (SELECT id FROM categories WHERE slug='general'), $1)`, authorID)
	// 作者给自己点赞 + 别人点一个。
	f.exec(t, `INSERT INTO content_reactions (user_id, target_type, target_id, reaction) VALUES
	  ($1,'topic',1,'like'), ($2,'topic',1,'like')`, authorID, otherID)

	windowed := `{"v":1,"root":{"type":"activity.count","params":{"metric":"like_received_count","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":2}}}`
	if matched, result := f.eval(t, authorID, windowed); matched || result.Incomplete {
		t.Fatalf("自赞不应计入窗口获赞（实际只有 1 个他人赞）: %+v", result)
	}
	windowedOne := `{"v":1,"root":{"type":"activity.count","params":{"metric":"like_received_count","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":1}}}`
	if matched, result := f.eval(t, authorID, windowedOne); !matched || result.Incomplete {
		t.Fatalf("他人 1 个赞应满足 gte 1: %+v", result)
	}
	// 累计口径由 user_activity 提供（写路径已排除自赞）；这里写 1 与窗口一致。
	f.exec(t, `INSERT INTO user_activity (user_id, like_received_count) VALUES ($1, 1)`, authorID)
	cumulative := `{"v":1,"root":{"type":"activity.count","params":{"metric":"like_received_count","window":{"kind":"all"}},"cmp":{"op":"lte","value":1}}}`
	if matched, result := f.eval(t, authorID, cumulative); !matched || result.Incomplete {
		t.Fatalf("累计应为 1（不含自赞）: %+v", result)
	}
}

// TestActivityStreakEndsTodayOrYesterday 用「日历日」构造访问日期。
//
// 曾经的写法是 `CURRENT_DATE - d`：把 PG 会话时区的今天当成基准日。而 resolver 的
// 日界来自 Go 的进程本地日（`today()`，见 activityStreakSQL 的注释），两者在
// `TZ=Asia/Shanghai` + PG 会话 UTC 的本地 00:00–08:00 会差一天，于是「以昨天收尾的
// 连续 3 天」被判成不是连续段。基准日必须与 resolver 同源，这里直接用 fixture 的
// 进程本地日，日期运算本身仍是纯日历运算。
func TestActivityStreakEndsTodayOrYesterday(t *testing.T) {
	f := newFixture(t)
	today := f.todayDate()
	userID := f.user(t, "streaker", 30)
	// 今天 + 昨天 + 前天：连续 3 天。
	f.exec(t, `INSERT INTO user_visits (user_id, visited_on)
	          SELECT $1, $2::date - d FROM unnest(ARRAY[0,1,2]) AS d`, userID, today)
	rule := `{"v":1,"root":{"type":"activity.streak","params":{"minDays":3},"cmp":{"op":"gte","value":3}}}`
	if matched, result := f.eval(t, userID, rule); !matched || result.Incomplete {
		t.Fatalf("连续 3 天应满足 minDays 3: %+v", result)
	}
	// 以昨天收尾（今天没上线）仍然是连续 3 天。
	gapID := f.user(t, "gap", 30)
	f.exec(t, `INSERT INTO user_visits (user_id, visited_on)
	          SELECT $1, $2::date - d FROM unnest(ARRAY[1,2,3]) AS d`, gapID, today)
	if matched, result := f.eval(t, gapID, rule); !matched || result.Incomplete {
		t.Fatalf("以昨天收尾的连续 3 天应满足: %+v", result)
	}
	// 断档：今天 + 前天，只有 1 天。
	brokenID := f.user(t, "broken", 30)
	f.exec(t, `INSERT INTO user_visits (user_id, visited_on)
	          SELECT $1, $2::date - d FROM unnest(ARRAY[0,2]) AS d`, brokenID, today)
	if matched, result := f.eval(t, brokenID, rule); matched || result.Incomplete {
		t.Fatalf("断档后不应满足连续 3 天: %+v", result)
	}
	// 更早的连续段不算：今天没访问、最近一次是 5 天前。
	staleID := f.user(t, "stale", 30)
	f.exec(t, `INSERT INTO user_visits (user_id, visited_on)
	          SELECT $1, $2::date - d FROM unnest(ARRAY[5,6,7]) AS d`, staleID, today)
	if matched, result := f.eval(t, staleID, rule); matched || result.Incomplete {
		t.Fatalf("以 5 天前收尾的连续段不应算作当前连续: %+v", result)
	}
}

// TestPrepareGroupsOneQueryPerType 断言「Prepare 之后 Eval 不再打查询」：
// 把 pool 关掉再 Eval，若还有查询就会失败。
func TestPrepareServesFromMemo(t *testing.T) {
	f := newFixture(t)
	userID := f.user(t, "memo", 30)
	f.exec(t, `INSERT INTO user_activity (user_id, topic_count, comment_count) VALUES ($1, 5, 5)`, userID)
	rule := `{"v":1,"root":{"op":"all","children":[
	  {"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":5}},
	  {"type":"activity.count","params":{"metric":"comment_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":5}},
	  {"type":"identity.account_age","cmp":{"op":"gte","value":1}}
	]}}`
	node, err := ruleengine.Decode([]byte(rule))
	if err != nil {
		t.Fatal(err)
	}
	if err := ruleengine.Validate(node, conditions.Core()); err != nil {
		t.Fatal(err)
	}
	resolver := f.resolver(t, userID)
	if err := resolver.Prepare(f.ctx, node); err != nil {
		t.Fatal(err)
	}
	// 关闭 pool：memo 命中就不该再发查询。
	f.pool.Close()
	result, err := ruleengine.Eval(f.ctx, node, resolver)
	if err != nil {
		t.Fatalf("Eval 不应打查询: %v", err)
	}
	if !result.Matched || result.Incomplete {
		t.Fatalf("期望确定成立: %+v", result)
	}
}

// TestDaysVisitedWindowUsesProcessLocalDate 是 M1.3-15 的回归：窗口日界必须来自
// Go 的进程本地日，不能由 PG 会话时区折算。
//
// 用固定时钟（2026-11-12 07:00 +08:00）+ PG 会话 UTC 构造「日界差一天」的场景：
// 若 SQL 用 `(now() - interval)::date` 取界，下界会落到 11-10，把 11-10 的访问也算进来；
// 正确实现的下界是 11-11（Go 的今天减 1 天）。
func TestDaysVisitedWindowUsesProcessLocalDate(t *testing.T) {
	f := newFixture(t)
	userID := f.user(t, "daybound", 30)
	f.exec(t, `INSERT INTO user_visits (user_id, visited_on) VALUES
	  ($1, DATE '2026-11-11'), ($1, DATE '2026-11-10')`, userID)

	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	fixed := time.Date(2026, 11, 12, 7, 0, 0, 0, loc)
	resolver := f.resolver(t, userID).WithClock(func() time.Time { return fixed })

	value, err := resolver.Resolve(f.ctx, ruleengine.Leaf{
		Type:   conditions.KeyActivityCount,
		Params: map[string]any{"metric": "days_visited", "window": map[string]any{"kind": "recent_days", "days": float64(1)}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if value.Count != 1 {
		t.Fatalf("recent_days:1 只应算 2026-11-11 一天，得到 %d（日界被 PG 时区折算？）", value.Count)
	}
}

// TestActivityCountWindowCountsSoftDeleted 是 M1.3-03 的回归：软删内容仍计入窗口
// 计数（成就口径），与 `topic_count` 的累计口径一致。
func TestActivityCountWindowCountsSoftDeleted(t *testing.T) {
	f := newFixture(t)
	userID := f.user(t, "softdel", 30)
	f.exec(t, `INSERT INTO categories (slug) VALUES ('general')`)
	f.exec(t, `INSERT INTO topics (id, category_id, author_user_id, status) VALUES
	  (1, (SELECT id FROM categories WHERE slug='general'), $1, 'deleted'),
	  (2, (SELECT id FROM categories WHERE slug='general'), $1, 'active')`, userID)
	f.exec(t, `INSERT INTO comments (topic_id, author_user_id, status) VALUES (2, $1, 'deleted'), (2, $1, 'active')`, userID)

	topics := `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":2}}}`
	if matched, result := f.eval(t, userID, topics); !matched || result.Incomplete {
		t.Fatalf("软删主题应计入窗口计数（成就口径）: %+v", result)
	}
	comments := `{"v":1,"root":{"type":"activity.count","params":{"metric":"comment_count","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":2}}}`
	if matched, result := f.eval(t, userID, comments); !matched || result.Incomplete {
		t.Fatalf("软删评论应计入窗口计数（成就口径）: %+v", result)
	}
}

// TestBookmarkCountWindowIgnoresVisibility 是 task-7 ① 的回归：窗口 bookmark_count
// 与累计、与 object.engagement 三面必须同口径（都不看 visibility）。
// 旧实现只算 public，会出现「累计 3 / 最近 30 天 1」。
func TestBookmarkCountWindowIgnoresVisibility(t *testing.T) {
	f := newFixture(t)
	userID := f.user(t, "bookmark-window", 30)
	// 累计值由写路径维护；这里直接写 3 模拟「三条收藏都计过数」。
	f.exec(t, `INSERT INTO user_activity (user_id, bookmark_count) VALUES ($1, 3)`, userID)
	f.exec(t, `INSERT INTO content_bookmarks (user_id, target_type, target_id, visibility) VALUES
	  ($1,'topic',11,'private'), ($1,'topic',12,'private'), ($1,'topic',13,'public')`, userID)

	windowed := `{"v":1,"root":{"type":"activity.count","params":{"metric":"bookmark_count","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":3}}}`
	if matched, result := f.eval(t, userID, windowed); !matched || result.Incomplete {
		t.Fatalf("窗口 bookmark_count 必须数全部 3 条（含 2 条 private）: %+v", result)
	}
	// 与累计相等：同一用户两个面给出同一个数。
	value, err := f.resolver(t, userID).Resolve(f.ctx, ruleengine.Leaf{
		Type:   conditions.KeyActivityCount,
		Params: map[string]any{"metric": "bookmark_count", "window": map[string]any{"kind": "recent_days", "days": float64(30)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cumulative, err := f.resolver(t, userID).Resolve(f.ctx, ruleengine.Leaf{
		Type:   conditions.KeyActivityCount,
		Params: map[string]any{"metric": "bookmark_count", "window": map[string]any{"kind": "all"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if value.Count != cumulative.Count {
		t.Fatalf("窗口 %d 与累计 %d 必须一致（同口径）", value.Count, cumulative.Count)
	}
}
