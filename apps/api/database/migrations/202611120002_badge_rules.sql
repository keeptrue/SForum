-- +goose Up
-- +sforum OnlineSafe
-- 徽章规则引擎的数据域（计划 2026-11-12-badge-rule-engine §4）：
--   * badges.rule            —— 规则 AST（JSON），成为自动徽章的**唯一**判定来源；
--   * badges.requirement_*   —— 运营填的双语自然语言要求（空串表示由规则自动生成）；
--   * badge_facts            —— 插件上报的「事件行」（append-only，可幂等去重）；
--   * user_fact_counters     —— 事实的累计计数（写路径同事务维护）；
--   * badge_rules_rollback   —— 迁移前的 metric/threshold 快照，只在 Down 段读。
--
-- 为什么 metric/threshold 保留而不是直接删：它们是 Down 段还原旧 CHECK 的依据，
-- 也是线上回滚时运维能看懂的那两列。**写路径从本迁移起不再维护它们**（见下面的
-- COMMENT），下一次迁移再删。留着不等于还在用，注释写清楚比删得早更重要。
--
-- OnlineSafe 的依据：ADD COLUMN 全部可空或带非易失默认值；三张新表是纯新增；
-- 唯一的表重写是 badges 的 CHECK 替换（内置徽章表是个位数量级的小表），
-- 且与既有 badges 迁移同批执行。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

-- 规则 AST：形状由 Support/RuleEngine 校验（深度/叶子数/条件白名单/参数 schema），
-- 库里只保证「自动徽章必须有规则、手动徽章必须没有」这条二元约束。
ALTER TABLE badges ADD COLUMN IF NOT EXISTS rule JSONB;
ALTER TABLE badges ADD COLUMN IF NOT EXISTS requirement_summary_zh TEXT NOT NULL DEFAULT '';
ALTER TABLE badges ADD COLUMN IF NOT EXISTS requirement_summary_en TEXT NOT NULL DEFAULT '';

-- 12 枚内置自动徽章转成单条件规则：activity.count + 窗口 all + gte 阈值。
-- 形态与 Support/RuleEngine 的 AST 逐字一致（`{"v":1,"root":{...}}`），
-- 这样「快路径」（单 activity.count + window.all 叶子）能直接命中，12 枚徽章的
-- 求值 SQL 与改造前完全一致（计划 §5.3）。
UPDATE badges
SET rule = jsonb_build_object(
  'v', 1,
  'root', jsonb_build_object(
    'type', 'activity.count',
    'params', jsonb_build_object(
      'metric', metric,
      'window', jsonb_build_object('kind', 'all')
    ),
    'cmp', jsonb_build_object('op', 'gte', 'value', threshold)
  )
)
WHERE grant_mode = 'automatic'
  AND metric IS NOT NULL
  AND threshold IS NOT NULL
  AND rule IS NULL;

-- 旧 CHECK 只认 metric/threshold；新 CHECK 认 rule。两者不能同时存在：
-- 同时存在会让「自动徽章改了 rule 但没改 metric」的写路径被旧约束误伤。
ALTER TABLE badges DROP CONSTRAINT IF EXISTS badges_rule_check;
ALTER TABLE badges ADD CONSTRAINT badges_rule_check CHECK (
  (grant_mode = 'automatic' AND rule IS NOT NULL)
  OR (grant_mode = 'manual' AND rule IS NULL)
);

COMMENT ON COLUMN badges.rule IS
  'Badge rule AST ({"v":1,"root":...}); the only decision source for automatic badges since 202611120002. Validated by Support/RuleEngine before write.';
COMMENT ON COLUMN badges.requirement_summary_zh IS
  'Operator-authored Chinese requirement text; empty string means "derive from rule".';
COMMENT ON COLUMN badges.requirement_summary_en IS
  'Operator-authored English requirement text; empty string means "derive from rule".';
COMMENT ON COLUMN badges.metric IS
  'LEGACY: not maintained by the write path since 202611120002 (rule is authoritative). Kept read-only as the Down-migration restore source; removed in a later migration.';
COMMENT ON COLUMN badges.threshold IS
  'LEGACY: not maintained by the write path since 202611120002 (rule is authoritative). Kept read-only as the Down-migration restore source; removed in a later migration.';

-- 插件事实：一行一个「发生了什么」，append-only。delta <> 0 挡住无意义的零增量行；
-- dedupe_key 非空时由唯一索引保证同一事件不会重复计数（幂等的真值位置）。
CREATE TABLE IF NOT EXISTS badge_facts (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  fact_key TEXT NOT NULL CHECK (fact_key ~ '^[a-z0-9][a-z0-9._-]{1,120}$'),
  variant TEXT NOT NULL DEFAULT '',
  delta BIGINT NOT NULL CHECK (delta <> 0),
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  source_extension_id TEXT NOT NULL DEFAULT '',
  dedupe_key TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 部分唯一索引：dedupe_key 为空的行不参与去重（允许同一事实在同一秒内多次累加）。
CREATE UNIQUE INDEX IF NOT EXISTS badge_facts_dedupe_idx
  ON badge_facts (user_id, fact_key, variant, dedupe_key) WHERE dedupe_key <> '';

-- 窗口类条件的扫描路径：按用户 + 事实键 + 变体取最近窗口。
CREATE INDEX IF NOT EXISTS badge_facts_window_idx
  ON badge_facts (user_id, fact_key, variant, occurred_at DESC);

-- 累计计数：total 允许为负（事实可以是扣减），读侧统一 GREATEST(total, 0)，
-- 所以负数不会漏到公开面，但也别在写路径上加 CHECK 把扣减挡住。
CREATE TABLE IF NOT EXISTS user_fact_counters (
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  fact_key TEXT NOT NULL,
  variant TEXT NOT NULL DEFAULT '',
  total BIGINT NOT NULL DEFAULT 0,
  last_occurred_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, fact_key, variant)
);

-- 回滚支持：记录改造前的 metric/threshold。Down 段优先用它还原旧列，
-- 迁移之后新建的徽章没有快照行，才回落到「从当前单条件规则反推」。
-- 理由：多条件规则无法反推 metric/threshold，所以 Down 必须先拒绝而不是猜。
CREATE TABLE IF NOT EXISTS badge_rules_rollback (
  badge_id BIGINT PRIMARY KEY,
  rule JSONB NOT NULL,
  legacy_metric TEXT,
  legacy_threshold BIGINT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO badge_rules_rollback (badge_id, rule, legacy_metric, legacy_threshold)
SELECT badges.id, badges.rule, badges.metric, badges.threshold
FROM badges
WHERE badges.rule IS NOT NULL
ON CONFLICT (badge_id) DO NOTHING;

-- 稳定视图：追加规则三列（metric/threshold 保留一版，见列注释）。
-- 用 OR REPLACE 且**只在末尾追加**列：CREATE OR REPLACE VIEW 不允许改列顺序或删列，
-- 而 HostQuery 的字段表达式是按列名取的，所以追加是唯一安全形态。
CREATE OR REPLACE VIEW sforum_core_v1.forum_badges
WITH (security_barrier = true, security_invoker = false) AS
SELECT badges.id, badges.slug, badges.name, badges.description,
       badge_groups.key AS group_key, badges.tier, badges.icon, badges.position,
       badges.grant_mode, badges.metric, badges.threshold,
       badges.rule, badges.requirement_summary_zh, badges.requirement_summary_en
FROM public.badges
LEFT JOIN public.badge_groups ON badge_groups.id = badges.group_id
WHERE badges.enabled
OFFSET 0;

COMMENT ON VIEW sforum_core_v1.forum_badges IS
  'SForum core view v1: enabled badge catalog with group key and grant rule (grant_mode + rule AST; metric/threshold are legacy read-only columns) plus bilingual requirement text, so plugins can rebuild catalog and earn conditions; no audit fields.';

-- 插件事实的公开读面：只输出 active 用户的累计值。
-- **不输出** delta 行（那是写入流水）、dedupe_key（幂等键是写入方私有约定）、
-- source_extension_id（谁上报的事实属于扩展间信息，不进公开面）。
CREATE OR REPLACE VIEW sforum_core_v1.forum_user_badge_facts
WITH (security_barrier = true, security_invoker = false) AS
SELECT user_fact_counters.user_id, users.username, users.display_name,
       user_fact_counters.fact_key, user_fact_counters.variant,
       GREATEST(user_fact_counters.total, 0) AS total
FROM public.user_fact_counters
JOIN public.users ON users.id = user_fact_counters.user_id AND users.status = 'active'
OFFSET 0;

COMMENT ON VIEW sforum_core_v1.forum_user_badge_facts IS
  'SForum core view v1: per-user accumulated fact totals for active users, clamped at 0; no delta rows, no dedupe keys, no source extension identity.';

-- 新对象与既有 Core 对象同源：复制 `badges` 的 owner。
--
-- 为什么需要：扩展平台的 kernel 校验要求 `public` / `sforum_core_v1` 里**每个**对象
-- 的 owner 都等于 Core owner role，而它的收敛步骤只搬「owner 恰好等于运行时角色」的
-- 对象（`loadExtensionDatabaseKernelRelations` 的 `relowner = roleName` 过滤）。
-- 迁移新建的表/视图如果 owner 与既有表不同，就永远不会被收敛，运行期租约签发时
-- 报 `extension database physical resource conflict`。
--
-- 从 `badges` 抄而不是写死角色名：它同域、同生命周期、且一定已被收敛过，迁移也
-- 不需要知道 Core owner role 是谁。`forum_badges` 也要重设——它在本迁移里被
-- DROP + CREATE 重建过，owner 会重置成执行者。
-- +goose StatementBegin
DO $$
DECLARE target_owner text;
BEGIN
  SELECT pg_get_userbyid(classes.relowner) INTO target_owner
  FROM pg_class AS classes
  JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
  WHERE namespaces.nspname = 'public' AND classes.relname = 'badges';

  IF target_owner IS NOT NULL THEN
    EXECUTE format('ALTER TABLE public.badge_facts OWNER TO %I', target_owner);
    EXECUTE format('ALTER TABLE public.user_fact_counters OWNER TO %I', target_owner);
    EXECUTE format('ALTER TABLE public.badge_rules_rollback OWNER TO %I', target_owner);
    EXECUTE format('ALTER VIEW sforum_core_v1.forum_user_badge_facts OWNER TO %I', target_owner);
    EXECUTE format('ALTER VIEW sforum_core_v1.forum_badges OWNER TO %I', target_owner);
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- 严格逆序拆除。先做**数据可逆性检查**：多条件（或非 activity.count + window.all）
-- 规则无法反推 metric/threshold，此时宁可拒绝回滚也不猜——猜错等于悄悄改掉
-- 运营配置的徽章条件，而且没有任何地方会报错。
-- +goose StatementBegin
DO $$
DECLARE
  unrestorable INT;
BEGIN
  SELECT COUNT(*) INTO unrestorable
  FROM public.badges
  WHERE grant_mode = 'automatic'
    AND (
      rule IS NULL
      OR rule->>'v' IS DISTINCT FROM '1'
      OR rule->'root'->>'op' IS NOT NULL
      OR rule->'root'->>'type' IS DISTINCT FROM 'activity.count'
      OR rule->'root'->'params'->'window'->>'kind' IS DISTINCT FROM 'all'
      OR rule->'root'->'params'->>'metric' IS NULL
      OR rule->'root'->'params'->>'metric' NOT IN (
        'topic_count','comment_count','like_received_count',
        'like_given_count','bookmark_count','days_visited'
      )
      OR rule->'root'->'cmp'->>'op' IS DISTINCT FROM 'gte'
      OR (rule->'root'->'cmp'->>'value') IS NULL
      OR (rule->'root'->'cmp'->>'value') !~ '^[0-9]+$'
    );

  IF unrestorable > 0 THEN
    RAISE EXCEPTION 'Refusing to roll back badge rules: % automatic badge(s) have a rule that cannot be reduced to metric/threshold', unrestorable
      USING HINT = 'This migration can only be rolled back while every automatic badge has a single activity.count leaf with window.kind=all and cmp.op=gte. Convert those badges back to a single-condition rule (or make them manual) before rolling back.';
  END IF;
END $$;
-- +goose StatementEnd

-- 还原旧列要区分三种情况（计划 §4.3 的「回滚支持表」就是为这个而存在）：
--   1. 规则与快照相同      → 运营没动过，还原迁移前的 metric/threshold（真正的回滚语义）；
--   2. 规则与快照不同但可逆 → 运营改过单条件规则，按**当前**规则反推，
--                             否则会把运营的新条件悄悄换回旧值；
--   3. 规则不同且不可逆    → 上面已经 RAISE EXCEPTION 拒绝，不会走到这里。
-- jsonb 的 `=` 是值相等（键序无关），所以这条比较不需要额外规范化。
UPDATE public.badges AS badges
SET metric = CASE
      WHEN rollback.rule = badges.rule THEN rollback.legacy_metric
      ELSE badges.rule->'root'->'params'->>'metric'
    END,
    threshold = CASE
      WHEN rollback.rule = badges.rule THEN rollback.legacy_threshold
      ELSE (badges.rule->'root'->'cmp'->>'value')::BIGINT
    END
FROM public.badge_rules_rollback AS rollback
WHERE rollback.badge_id = badges.id
  AND badges.grant_mode = 'automatic';

-- 迁移之后新建的自动徽章没有快照行：只能从它自己的单条件规则反推
-- （多条件的那些已经被上面的检查拒绝）。
UPDATE public.badges AS badges
SET metric = badges.rule->'root'->'params'->>'metric',
    threshold = (badges.rule->'root'->'cmp'->>'value')::BIGINT
WHERE badges.grant_mode = 'automatic'
  AND NOT EXISTS (SELECT 1 FROM public.badge_rules_rollback AS rollback WHERE rollback.badge_id = badges.id);

-- 手动徽章在新模型里 rule 为 NULL，但旧模型要求 metric/threshold 也为 NULL；
-- 写路径不再维护这两列，所以这里必须显式清空，否则旧 CHECK 加不回去。
UPDATE public.badges
SET metric = NULL, threshold = NULL
WHERE badges.grant_mode = 'manual';

-- 视图必须先拆再建：CREATE OR REPLACE 只能追加列，不能删掉 rule 三列。
DROP VIEW IF EXISTS sforum_core_v1.forum_user_badge_facts;
DROP VIEW IF EXISTS sforum_core_v1.forum_badges;

CREATE VIEW sforum_core_v1.forum_badges
WITH (security_barrier = true, security_invoker = false) AS
SELECT badges.id, badges.slug, badges.name, badges.description,
       badge_groups.key AS group_key, badges.tier, badges.icon, badges.position,
       badges.grant_mode, badges.metric, badges.threshold
FROM public.badges
LEFT JOIN public.badge_groups ON badge_groups.id = badges.group_id
WHERE badges.enabled
OFFSET 0;

COMMENT ON VIEW sforum_core_v1.forum_badges IS
  'SForum core view v1: enabled badge catalog with group key and grant rule (grant_mode/metric/threshold) so plugins can rebuild catalog and earn conditions; no audit fields.';

ALTER TABLE badges DROP CONSTRAINT IF EXISTS badges_rule_check;
ALTER TABLE badges ADD CONSTRAINT badges_rule_check CHECK (
  (grant_mode = 'automatic' AND metric IS NOT NULL AND threshold IS NOT NULL AND threshold > 0)
  OR (grant_mode = 'manual' AND metric IS NULL AND threshold IS NULL)
);

COMMENT ON COLUMN badges.metric IS NULL;
COMMENT ON COLUMN badges.threshold IS NULL;

ALTER TABLE badges DROP COLUMN IF EXISTS requirement_summary_en;
ALTER TABLE badges DROP COLUMN IF EXISTS requirement_summary_zh;
ALTER TABLE badges DROP COLUMN IF EXISTS rule;

DROP TABLE IF EXISTS badge_rules_rollback;
DROP TABLE IF EXISTS user_fact_counters;
DROP INDEX IF EXISTS badge_facts_window_idx;
DROP INDEX IF EXISTS badge_facts_dedupe_idx;
DROP TABLE IF EXISTS badge_facts;
