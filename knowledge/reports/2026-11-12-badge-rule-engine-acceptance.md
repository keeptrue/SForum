# 徽章规则引擎 M1 独立验收矩阵

Status: **executed**（M1 已收口，见计划 §15.1；本矩阵原为「先写断言、再等实现」的验收侧交付，M1 落地后按同一判据实测执行）
计划：`knowledge/plans/2026-11-12-badge-rule-engine.md`（approved 2026-11-12）
基线 HEAD：`d502f19a3`（M1 未开始的干净树）
模块背景：`knowledge/modules/badges.md`（Known Residuals 1–23 与四条横切面注脚）
基线说明：`d502f19a3` 是本矩阵的历史基线（M1 代码未落地时的干净树，断言写作时点），`4457106e2`（apps）/ `4e1876331`（contracts 与生成物）是 M1 代码的提交锚点，**两个时点不要互相替换**；判据与 pend 数字一律照原文，未因状态行改动作任何调整。

本矩阵**不写生产代码**，只定义：断言 ID、验证方式、通过判据、失败归属。可执行的测试文件由 M1 实现方按此矩阵落地；本文件是它们的存在性证明与判据来源。

执行载体：`scripts/verify-badge-rules-acceptance.sh`（ok / FAIL / pend 三态，**不接门禁**）。
执行记录见 §9：M1.2 七条已全部执行并 PASS；M1.1 结构类断言已在已迁移的 dev 库上初验 PASS。

---

## 0. 判定规则、环境与命令

### 0.1 三种结果

| 结果 | 含义 |
| --- | --- |
| PASS | 按「验证方式」实际执行过，且满足「通过判据」 |
| FAIL | 已落地切片的行为不符判据 |
| pend | 前置不满足（切片未落地、dev 429 限流、夹具未备），**不算通过**，必须在报告里写明缺什么 |

**任何一条不得以「读代码看起来对」记为 PASS。** 模块笔记四条横切面注脚的教训就是：断言集不完备时「全绿」推不出「可用」。

### 0.2 环境

```bash
./scripts/dev.sh                      # PG / Redis / Mailpit
./scripts/api-dev.sh                  # API（dev 内嵌 worker）
cd apps/web && bun run dev            # 用户自起的 3000，勿杀
```

### 0.3 命令清单（矩阵里按 ID 引用）

| 代号 | 命令 |
| --- | --- |
| `CMD-MIG` | `cd apps/api && go run ./cmd/migrate` |
| `CMD-ISO` | `./scripts/test-api-isolated.sh -run '<TestName>' ./app/Models/Badges/...`（显式包路径不分片，脚本自己加 `-count=1`） |
| `CMD-AST` | `cd apps/api && go test ./app/Support/RuleEngine/...` |
| `CMD-RTE` | `cd apps/api && go test ./app/Support/Routes/` |
| `CMD-L10N` | `cd apps/api && go test ./app/Support/Localization/` |
| `CMD-HOST` | `cd apps/api && go test ./app/Support/HostAPI/ ./bootstrap/ ./app/Http/Controllers/Extensions/` |
| `CMD-REV` | `SFORUM_MIGRATION_TARGET_VERSION=202611120002 ./scripts/verify-migration-reversible.sh` |
| `CMD-OAS` | `ruby scripts/validate-openapi-refs.rb` |
| `CMD-ARCH` | `node tests/validate-architecture-boundaries.mjs` |
| `CMD-WEB` | `cd apps/web && bun test && bun run typecheck` |
| `CMD-GATE` | `./scripts/test.sh --fast` |

### 0.4 切片 ↔ 断言前缀对照

| 切片（计划 §15） | 前缀 | 责任面 |
| --- | --- | --- |
| M1.1 迁移 + 视图 + HostQuery | `M1.1-*` | 迁移文件、`sforum_core_v1` 视图、HostAPI 注册、**10 枚自动徽章回归 + 2 枚手动不变** |
| M1.2 `Support/RuleEngine` 纯 AST | `M1.2-*` | `ast.go` / `validate.go` / `eval.go` |
| M1.3 条件注册表 + 五组解析器 | `M1.3-*` | `Models/Badges/conditions/*`、窗口 SQL（含自赞排除）、性能 |
| M1.4 存储/求值/回扫/HTTP/契约 | `M1.4-*` | `rule_store.go` / `evaluate.go` / `backfill_rules.go` / `dto.go` / 公开面 |
| 横切面四条 | `X-*` | i18n / 路由计数 / 错误码本地化 / 迁移可逆 |
| 计划未覆盖但必须验 | `P-*` | 见 §6 |

---

## 1. 回归：迁移后 10 枚自动徽章逐人等价、2 枚手动不变（M1.1）

> **数量口径（2026-11-12 老后核实）**：内置 12 枚里 `community-contribution` /
> `quality-answer` 是 **manual**（`grant_mode='manual'`，`metric`/`threshold`/`rule` 全 NULL），
> 转换 SQL 按 `grant_mode='automatic'` 只命中 **10 枚**；`verify-migration-reversible.sh`
> 用的是 `EXPECTED_AUTO_BADGES=10`。**不要为了凑「12 枚」去改迁移**——那会把手动徽章
> 硬塞进规则，方向是错的。本节的「逐人一致」只对 10 枚自动徽章成立；2 枚手动徽章的判据是
> 「`rule IS NULL` 且授予行、`top_badge_id` 行为完全不变」。

这是计划 §1 验收第 1 条，也是本刀唯一的「零回归」硬承诺。**快照 SQL、比对 SQL、定位链见 §2**，本节只给断言。

| ID | 断言 | 验证方式 | 通过判据 | 失败归属 |
| --- | --- | --- | --- | --- |
| M1.1-01 | 迁移落地且是最新一条 | `CMD-MIG`；`SELECT max(version_id) FROM goose_db_version WHERE is_applied` | = `202611120002`，无报错 | `database/migrations/202611120002_badge_rules.sql` |
| M1.1-02 | `badges` 三新列 + 旧 CHECK 替换 | SQL §2.5-A | `rule` JSONB 可空；两个 `requirement_summary_*` NOT NULL DEFAULT `''`；`badges_rule_check` 变为 `automatic→rule IS NOT NULL / manual→rule IS NULL`；`badges_metric_check` 仍在；`COMMENT ON COLUMN badges.metric` 写明「写路径不再维护」 | 迁移 Up 段 |
| M1.1-03 | 10 枚自动徽章规则逐枚等价（内置基线 10，不含 2 枚手动） | SQL §2.5-B | 生效叶子（**root 直接是叶子，或 `all` 包 1 个叶子**，两种形状都接受）满足：`type='activity.count'`、`params.metric=legacy.metric`、`params.window.kind='all'`、`cmp.op='gte'`、`cmp.value=legacy.threshold`；组节点时 children 必须恰好 1 个；2 枚手动 `rule IS NULL` 且 metric/threshold 仍 NULL | 迁移 Up 段转换 SQL（非引擎） |
| M1.1-04 | 非内置的自动徽章也被转换 | 迁移前造一枚 `automatic/comment_count/3` 的徽章，迁移后跑 §2.5-B | 该徽章同样满足 B 的判据（不是只转 12 个 slug） | 迁移 Up 段（写死 slug 白名单即 FAIL） |
| M1.1-05 | `badge_rules_rollback` 在 Up 段写入 | `SELECT count(*), count(*) FILTER (WHERE legacy_metric IS NULL) FROM badge_rules_rollback` | 行数 = 迁移时 automatic 徽章数（基线 10 + 自定义）；legacy 值与迁移前一致 | 迁移 Up 段（时机歧义见 P-02） |
| M1.1-06 | `forum_badges` 视图扩展 | SQL §2.5-C | 含 `rule` / `requirement_summary_zh` / `requirement_summary_en`；`metric`/`threshold` 保留一版；仍 `security_barrier` + 只输出 `enabled`；不输出 `granted_by` / 邮箱 | 迁移视图段 |
| M1.1-07 | 新视图 `forum_user_badge_facts` 白名单 | SQL §2.5-D + 行为断言 | 列集合**不含** `delta` / `dedupe_key` / `source_extension_id` / `id`；disabled 用户的 fact 查不到（`WHERE users.status='active'`） | 迁移视图段 |
| M1.1-08 | 新 HostQuery 五面一致 | `CMD-HOST` | `sforum.core.public_user_badge_facts.list` 在 5 处同时出现：`HostAPI/v2_query_runtime.go` 常量、`query_registry_core.go` 定义、`query_registry_core_badges_test.go` 映射表、`bootstrap/sealed_query_catalog_test.go` 的 `sealedCoreQueryCatalogIDs`、`Extensions/query_catalog_test.go` 的 `sealedQueryCatalogIDs`；且 allowed/denied 测试存在 | M1.1 HostQuery 切片（漏一面即 FAIL，见 P-05） |
| M1.1-13 | `rule` **只进视图**，不进 HostQuery `Fields`、不进公开面 | SQL §2.5-E + `grep -n 'stable\.rule' apps/api/app/Support/HostAPI/v2_query_postgres.go` + 脚本 M1.4-13 | `sforum_core_v1.forum_badges` **有** `rule`；`sforum.core.public_badges.list` 的 `Fields` **不含** `rule`；`/api/v1/badges` 响应键集合不含 `rule`。**三者同时成立才算 PASS** | `HostAPI/v2_query_postgres.go`（JSONB 传输形态未定，见 §7 残留 R-1） |
| M1.1-09 | 迁移后全库重算零变更 | `POST /admin/badges/recompute`，读 `{granted, refreshed}` | `granted == 0 && refreshed == 0` | 迁移转换 / 快路径（先看 M1.1-03 是否已红） |
| M1.1-10 | 逐人逐规则真值等价（双向） | SQL §2.3，在**有活动数据**的夹具上跑 | `should_have_but_missing` = 0 行；`has_but_should_not` 限定 `granted_by IS NULL AND grant_reason IN ('','recompute')` 且与迁移前快照一致 | 迁移转换 / 引擎求值 |
| M1.1-11 | `top_badge_id` 逐人一致 | SQL §2.2 第二、三段 | 差集 0 行；且不存在「迁移后新出现 top_badge_id」的用户 | 排序 SQL（`topBadgeOrderSQL`）或授予集合 |
| M1.1-12 | 迁移不触碰 `user_activity` | SQL §2.2 第四段（`counter_before` 全表逐行比对） | 0 行差异 | 迁移 Up 段（见 P-06） |

> **M1.1-09 与 M1.1-10 的关系**：09 只证明「不会多授予」，10 才证明「不会少授予」。只有 09 时，一条把规则写成 `value: 999999` 的转换也能绿。

---

## 2. 回归快照：可执行方法

### 2.1 迁移前快照（在 `202611120001` 的库上执行）

```sql
BEGIN;
CREATE SCHEMA IF NOT EXISTS badge_regress;
CREATE TABLE badge_regress.ub_before AS
SELECT ub.user_id, b.slug, ub.granted_at, ub.granted_by, ub.grant_reason
FROM user_badges ub JOIN badges b ON b.id = ub.badge_id;
CREATE TABLE badge_regress.top_before AS
SELECT id AS user_id, top_badge_id FROM users WHERE top_badge_id IS NOT NULL;
CREATE TABLE badge_regress.counter_before AS SELECT * FROM user_activity;
CREATE TABLE badge_regress.badge_before AS
SELECT id, slug, grant_mode, metric, threshold, enabled FROM badges;
COMMIT;
```

`user_activity` 必须一起快照：它是规则输入。若迁移顺手回填计数，回归比对会把「规则转换错」伪装成「统计修正」——这是最容易被放过的一类假绿。

### 2.2 迁移后比对

```sql
-- (1) 授予集合：双向差集，整行比较（granted_at 也要一致，迁移不该重写授予行）
SELECT 'lost' AS dir, x.* FROM (
  SELECT user_id, slug, granted_at, granted_by, grant_reason FROM badge_regress.ub_before
  EXCEPT
  SELECT ub.user_id, b.slug, ub.granted_at, ub.granted_by, ub.grant_reason
  FROM user_badges ub JOIN badges b ON b.id = ub.badge_id
) x
UNION ALL
SELECT 'added', y.* FROM (
  SELECT ub.user_id, b.slug, ub.granted_at, ub.granted_by, ub.grant_reason
  FROM user_badges ub JOIN badges b ON b.id = ub.badge_id
  EXCEPT
  SELECT user_id, slug, granted_at, granted_by, grant_reason FROM badge_regress.ub_before
) y;

-- (2) 最高级徽章：原有非空者逐人比对
SELECT b.user_id, b.top_badge_id AS before, u.top_badge_id AS after
FROM badge_regress.top_before b JOIN users u ON u.id = b.user_id
WHERE u.top_badge_id IS DISTINCT FROM b.top_badge_id;

-- (3) 反向：迁移后新出现 top_badge_id 的人（快照只存非空行，必须单独查）
SELECT u.id, u.top_badge_id FROM users u
WHERE u.top_badge_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM badge_regress.top_before t WHERE t.user_id = u.id);

-- (4) 计数未被迁移改动
SELECT 'ua' AS t, * FROM (
  SELECT user_id, topic_count, comment_count, like_received_count,
         like_given_count, bookmark_count, days_visited
  FROM badge_regress.counter_before
  EXCEPT
  SELECT user_id, topic_count, comment_count, like_received_count,
         like_given_count, bookmark_count, days_visited
  FROM user_activity
) d;
```

### 2.3 逐人逐规则真值等价（比「重算 0 变更」更强）

```sql
WITH legacy AS (
  SELECT u.id AS user_id, b.id AS badge_id, b.slug,
         CASE b.metric
           WHEN 'topic_count'         THEN COALESCE(ua.topic_count, 0)
           WHEN 'comment_count'       THEN COALESCE(ua.comment_count, 0)
           WHEN 'like_received_count' THEN COALESCE(ua.like_received_count, 0)
           WHEN 'like_given_count'    THEN COALESCE(ua.like_given_count, 0)
           WHEN 'bookmark_count'      THEN COALESCE(ua.bookmark_count, 0)
           WHEN 'days_visited'        THEN COALESCE(ua.days_visited::bigint, 0)
         END >= b.threshold AS satisfied
  FROM users u
  JOIN badges b
    ON b.grant_mode = 'automatic' AND b.enabled
   AND b.metric IS NOT NULL AND b.threshold IS NOT NULL AND b.threshold > 0
  LEFT JOIN user_activity ua ON ua.user_id = u.id
)
SELECT 'should_have_but_missing' AS dir, l.user_id, l.slug
FROM legacy l
WHERE l.satisfied
  AND NOT EXISTS (SELECT 1 FROM user_badges ub
                  WHERE ub.user_id = l.user_id AND ub.badge_id = l.badge_id)
UNION ALL
SELECT 'has_but_should_not', l.user_id, l.slug
FROM legacy l
JOIN user_badges ub ON ub.user_id = l.user_id AND ub.badge_id = l.badge_id
WHERE NOT l.satisfied
  AND ub.granted_by IS NULL
  AND ub.grant_reason IN ('', 'recompute');
```

**为什么第二方向要收窄**：规则改严不自动收回（计划 §13.4）、手动授予、历史活动都会让「持有但当前不达标」成为合法状态。绝对真值不存在，只能断言「迁移前后新增的不一致」。第一方向（该有没有）没有这个豁免，必须严格 0 行。

**夹具要求**：dev 库若用户活动全是 0，两个方向都空，M1.1-10 就是空断言（假绿）。必须造边界夹具：每个指标在 `threshold-1 / threshold / threshold+1` 各一个用户，另加软删内容与 hidden 主题的用户。夹具 SQL 由 M1.1 切片提供，本矩阵只要求「`should_have_but_missing` 在夹具上非空过一次」——先用旧逻辑跑出非空，再在迁移后跑出 0 行，否则视为未验证。

### 2.4 不一致时的定位链

1. **按 slug 分组差集** → 定位到具体徽章：`... GROUP BY slug`。
2. 该徽章 `rule` 与 legacy 逐字段比对（§2.5-B）。多数 FAIL 停在这一步。
3. 若只有 `top_badge_id` 差异：手算期望赢家，判断是「授予集合变了」还是「排序变了」：

```sql
SELECT ub.user_id, b.slug, t.position AS tier_pos, b.position AS badge_pos, ub.granted_at
FROM user_badges ub
JOIN badges b ON b.id = ub.badge_id
JOIN badge_tiers t ON t.key = b.tier AND t.enabled
WHERE ub.user_id = $1
ORDER BY t.position, b.position, ub.granted_at DESC;
```

4. 若 §2.2 第 (4) 段非空 → 迁移动了计数，**直接 FAIL**，不进入徽章层排查。

### 2.5 结构与转换断言 SQL

> **形状口径（2026-11-12 实测）**：计划 §5.1 的示例把 `root` 画成组节点
> （`root.op = "all"` 包叶子），而迁移实际把 `root` 直接写成叶子
> （`{"v":1,"root":{"type":"activity.count",...}}`）。引擎两种都接受，所以**断言不能
> 钉死某一种**——本节的 SQL 统一用「生效叶子」求值（`CASE WHEN root.type IS NOT NULL
> THEN root ELSE root.children[0] END`）。第一版矩阵写死 `{root,children,0,...}`，
> 在已迁移的 dev 库上误报 10/10 不等价，根因是断言形状而非迁移。

```sql
-- A) 列与约束
SELECT column_name, data_type, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema='public' AND table_name='badges'
  AND column_name IN ('rule','requirement_summary_zh','requirement_summary_en');
SELECT conname, pg_get_constraintdef(oid)
FROM pg_constraint
WHERE conrelid='public.badges'::regclass AND contype='c';

-- B) 规则转换逐枚（形状无关：root 可直接是叶子，也可 all 包一个叶子）
WITH eff AS (
  SELECT b.slug, b.metric, b.threshold,
         CASE WHEN b.rule->'root'->>'type' IS NOT NULL THEN b.rule->'root'
              ELSE b.rule->'root'->'children'->0 END AS leaf,
         CASE WHEN b.rule->'root'->>'op' = 'all'
              THEN jsonb_array_length(COALESCE(b.rule->'root'->'children','[]'::jsonb))
              ELSE 1 END AS child_n
  FROM badges b WHERE b.grant_mode = 'automatic'
)
SELECT slug, metric, threshold, child_n,
       leaf->>'type'                     AS leaf_type,
       leaf->'params'->>'metric'         AS leaf_metric,
       leaf->'params'->'window'->>'kind' AS window_kind,
       leaf->'cmp'->>'op'                AS cmp_op,
       (leaf->'cmp'->>'value')::bigint   AS cmp_value
FROM eff ORDER BY slug;

-- 判据：上表 0 行 mismatch
SELECT count(*) AS mismatch FROM (
  SELECT 1 FROM eff
  WHERE leaf IS NULL OR child_n <> 1
     OR leaf->>'type' IS DISTINCT FROM 'activity.count'
     OR leaf->'params'->>'metric' IS DISTINCT FROM metric
     OR leaf->'params'->'window'->>'kind' IS DISTINCT FROM 'all'
     OR leaf->'cmp'->>'op' IS DISTINCT FROM 'gte'
     OR (leaf->'cmp'->>'value')::bigint IS DISTINCT FROM threshold
) x;

-- C) 视图列集
SELECT column_name, data_type
FROM information_schema.columns
WHERE table_schema='sforum_core_v1' AND table_name='forum_badges'
ORDER BY ordinal_position;

-- E) rule 的传播边界：视图有、HostQuery 无
SELECT EXISTS (
  SELECT 1 FROM information_schema.columns
  WHERE table_schema='sforum_core_v1' AND table_name='forum_badges' AND column_name='rule'
) AS view_has_rule;   -- 期望 t

-- HostQuery 侧的断言走源码（Fields 是 Go 字面量，不是视图列）：
--   grep -n 'stable\.rule' apps/api/app/Support/HostAPI/v2_query_postgres.go   → 期望 0 命中
--   public_badges.list 的 Fields 目前是 grant_mode / metric / threshold 三列，
--   不含 rule（见 §7 残留 R-1）。

-- D) 新视图列集 + 禁用用户过滤
SELECT column_name FROM information_schema.columns
WHERE table_schema='sforum_core_v1' AND table_name='forum_user_badge_facts'
ORDER BY ordinal_position;
SELECT count(*) FROM sforum_core_v1.forum_user_badge_facts WHERE user_id = $disabled_user;
```

---

## 3. 语义断言

### 3.1 M1.2 AST 纯函数（无 DB，最快，先跑）

| ID | 断言 | 验证方式 | 通过判据 | 失败归属 |
| --- | --- | --- | --- | --- |
| M1.2-01 | 未知字段解码报错，不静默 | `CMD-AST`：`{"v":1,"root":{"op":"all","children":[],"typo":1}}` | 返回 error | `RuleEngine/ast.go` |
| M1.2-02 | `all`/`any`/`not` 真值表 + 嵌套 | 表驱动：`all(any(A,B), not(C))` 三层，8 组真值；`not` 恰好 1 子节点 | 8 组全对；`not` 带 0 或 2 子节点被拒 | `eval.go` |
| M1.2-03 | 上限 | 深度 5/6、叶子 32/33、节点 64/65、`days` 1/3650/0/3651、`value` 1/1e9/0/1e9+1 | 边界内通过、越界返回 `forum.badge_rule_too_complex` 对应哨兵，不 panic | `validate.go` |
| M1.2-04 | 未知条件 key 在校验期拒绝 | `type:"activity.unknown"` | 校验期 error（不是求值期 false） | `validate.go` + registry |
| M1.2-05 | `cmp` 白名单按条件 | `identity.oauth_link` 带 `cmp`；`activity.count` 带 `eq` | 两者都被拒 | `validate.go` |
| M1.2-06 | **fail-closed 在 `not` 下不被反转**（**已验证 2026-11-12**） | `TestEvalNotOfUnknownStaysUnknown`：Resolver 对叶子返回 error，`not(leaf)` | 三值逻辑：`not(unknown)=unknown` → `Matched=false && Incomplete=true`。若 `not(errLeaf)` 授予，判 FAIL（P0） | `eval.go`（Kleene 实现：`triState` 零值为 unknown） |
| M1.2-07 | 纯函数包不依赖 domain | `go list -deps ./app/Support/RuleEngine \| grep -E 'Models/(Badges\|Forum\|Identity)'` | 输出为空 | `RuleEngine` 包 |

#### 3.1.1 M1.2 执行记录（2026-11-12，`apps/api/app/Support/RuleEngine/`）

命令：`cd apps/api && go test -v ./app/Support/RuleEngine/`（由脚本 `M1.2` 段按测试名逐条映射）。

| ID | 结果 | 证据（测试名 / 命令输出） |
| --- | --- | --- |
| M1.2-01 | **PASS** | `TestDecodeRejectsUnknownFields`（`dec.DisallowUnknownFields()` + 尾随内容拒绝 `TestDecodeRejectsTrailingContent`） |
| M1.2-02 | **PASS** | `TestEvalTruthTable`（含 `all`/`any`/`not` 与 `((a AND b) OR NOT c) AND d` 三层嵌套） |
| M1.2-03 | **PASS** | `TestValidateDepthBoundary`（5 过 / 6 拒）、`TestValidateLeafBoundary`（32/33）、`TestValidateNodeBoundary`（64/66）、`TestValidateDaysRangeIndependentOfSchema`（3650 过 / 3651 拒） |
| M1.2-04 | **PASS** | `TestValidateConditionAndCompare/未知条件` → `CodeUnknownCondition`（校验期拒绝，不是求值期 false） |
| M1.2-05 | **PASS** | 同测试：`布尔条件带 cmp` → `CodeCompareForbidden`；`不允许的比较运算` → `CodeCompareNotAllowed`。补充实测：`activity.count` 带 `eq` 同样 `compare_not_allowed`（allowed 集合仅 gte/lte） |
| M1.2-06 | **PASS** | `TestEvalNotOfUnknownStaysUnknown`；配套 `TestEvalFailClosed`、`TestEvalFalseDominatesUnknownInAll`、`TestEvalTrueDominatesUnknownInAny` |
| M1.2-07 | **PASS** | `go list -deps ./app/Support/RuleEngine \| grep -E 'Models/(Badges\|Forum\|Identity)'` 输出为空 |

补充观察（非断言）：`TestEvalShortCircuits` 证明 `any` 命中后不再求值后续叶子；`TestEvalUnvalidatedTreeDoesNotPanic` 证明未校验的树不会 panic。**后者同时暴露 P-12 的前提**：Eval 不强制先 Validate。

### 3.2 M1.3 条件解析器

| ID | 断言 | 验证方式 | 通过判据 | 失败归属 |
| --- | --- | --- | --- | --- |
| M1.3-01 | 11 个条件 key 各一对 allowed/denied | `CMD-ISO` 表驱动 | 每个 key 至少 1 正 1 反；`badge.owned` 走真实 `user_badges`；`content.pinned` 的语义见 M1.3-13 | `conditions/resolve_*.go` |
| M1.3-02 | 窗口 vs 累计 | 同一用户：90 天内 4 条 + 90 天前 8 条主题 | `activity.count{all}` = 12；`{recent_days:30}` = 4；同一评估内两叶子互不污染（memo 键含 window）。获赞类另见 M1.3-11（自赞必须两个口径都为 0） | `resolve_activity.go` |
| M1.3-03 | 软删口径：窗口计数**不过滤** status（**task-6 补夹具中**） | 3 条主题同在本窗口内：`active` / `hidden` / `deleted` | 窗口 `topic_count` = 3（与 `user_activity.topic_count` 成就口径一致）。窗口 SQL 出现 `status <> 'deleted'` 即 FAIL | `resolve_activity.go` |
| M1.3-04 | `content.hidden` 只统计**主题**的 `status='hidden'` | 4 条主题：hidden / deleted / active / 各 1 + 评论：1 条 hidden 评论 | 主题计数 = 1；**评论被隐藏不增加该计数**；`deleted`（作者自删）、`pending`、`rejected` 均不计入；写成 `status <> 'active'` 或把评论表 JOIN 进来即 FAIL。文案要求见 M1.3-12 | `resolve_content.go`（见 P-07） |
| M1.3-05 | 连续天数以今天/昨天收尾 | 三组 `user_visits`：今天起连续 5 天；昨天起连续 5 天（今天缺）；今天+前天有、昨天缺 | 分别 = 5 / 5 / 1（断链只算最近一段） | `resolve_activity.go` |
| M1.3-06 | `identity.oauth_link` 只认 `status='active'` | 造 active 与 revoked 两条 `identity_external_links` | active → true；revoked → false | `resolve_identity.go` |
| M1.3-07 | `object.engagement` 三 action | like / bookmark / comment 各一对；comment 另加「自己主题下自评论」正例（P-11 已裁决：算） | like / bookmark 只在关系行存在时为 true；comment：A 在主题 T（含 T 是 A 自己发的）下评论过 → true，未评论 → false | `resolve_object.go` |
| M1.3-08 | 失效引用 | 规则指向不存在的 category/tag slug | 条件恒 false，**不报错不 panic**；`GET /admin/badges/rule-health` 列出该徽章 | `resolve_object.go` + `rule_store.go` |
| M1.3-09 | 每 type 一次查询（memo） | `pg_stat_statements` 前后差：一次 `EvaluateUser` | Δcalls ≤ 不同 type 数 + 3 | `conditions` 取数层 |
| M1.3-10 | 窗口「获赞」P95（1 万用户夹具） | §3.3 | 记录 p50/p95/p99 与实际 EXPLAIN 计划；预算见 §3.3 | `resolve_activity.go` |
| M1.3-11 | **自赞排除**：`r.user_id <> $1` | 用户 A 给自己的主题 T 点赞 | `activity.count{like_received_count, all}` = 0 **且** `{recent_days:30}` = 0；给 B 的主题点赞后 A 的「给出」+1、B 的「收到」+1 | `resolve_activity.go` |
| M1.3-12 | `content.hidden` 的**文案口径** | 读条件目录 `GET /admin/badges/conditions` 的 label/describe | 文案只能是「主题被管理员隐藏的次数」这类中性描述；**不得**出现「违规」「处罚」「封禁」等词——库里没有处罚记录，写强了就是误导运营 | `conditions/registry.go` |
| M1.3-13 | `content.pinned` = **当前仍被置顶**的主题数 | 2 条置顶主题 → 取消 1 条置顶 | 取消前 = 2、取消后 = **1**。断言里**不要**写「取消置顶后仍为 1（曾经置顶）」「保持 2」：库里无 `pinned_at`、无审计列，「曾经」没有数据源 | `resolve_content.go` |
| M1.3-14 | 连续天数的日期由 **Go 传入**，SQL 不用 `CURRENT_DATE` | 设 `TZ=Asia/Shanghai` 跑 API、PG 时区设 UTC，造「今天刚访问」的用户；再 grep 源码 | streak 计数正确（不因跨时区把今天算成昨天而误判断签）；`grep -rn 'CURRENT_DATE\|now()::date' app/Models/Badges/conditions/` 零命中 | `resolve_activity.go`（跨零点误判只在异时区下暴露） |
| M1.3-16 | `fact.count` 必须在 **`Core()`** 里（校验/求值全集 = 13 条） | `TestRegistryCatalogIsCompleteAndValidatable` | `len(Core().Definitions()) == 13` 且 `ConditionMeta("fact.count")` 命中。删掉它 → M3 存下的规则保存时 `unknown_condition` | `conditions/registry.go` |
| M1.3-17 | `fact.count` **不在** `PublishableDefinitions(nil)`，但在 `PublishableDefinitions([]string{...})` 里 | 同测试 | 空候选集时 `fact.count` 不出现且总数少 1；给候选后重新出现且总数相等。把校验接到发布面 → 插件卸载后所有已存规则保存不了 | `conditions/registry.go` |
| M1.3-18 | `fact.count` 的 `factKey` 参数 `Options` 内容 = 传入的 factKeys | 同测试（**当前未断言**，建议补一行） | `PublishableDefinitions([]string{"acme.x"})` 里 `factKey` 的 `Options` 恰含 `acme.x`，不是只有条数对 | `conditions/registry.go` |
| M1.3-19 | **bookmark 三个面口径一致**（累计 / 窗口 / `object.engagement`） | `TestObjectEngagement`（当前红）+ `resolve_activity.go` 窗口分支 | 三处对 private 收藏的处理必须一致。当前：`user_activity.bookmark_count` 计入 private（写路径 `relationDelta` 与可见性无关），`object.engagement{bookmark}` 也计入，**但窗口 SQL 过滤 `visibility='public'`** → 同一指标累计 5 / 最近 30 天 0。要么三处都不看可见性，要么三处都看 | `resolve_activity.go:53`（窗口）+ `conditions_object_identity_integration_test.go:21`（期望值） |
| M1.3-15 | **所有窗口日界不得依赖 PG 时区**（**task-6 修复中**） | `grep -rn 'now() - make_interval' app/Models/Badges/conditions/resolve_activity.go`，看是否对结果 `::date` 取日界 | 零命中 `(now() - interval)::date`。`user_visits.visited_on` 是按 **API 进程本地日**写入的，窗口下界若用 PG 的 `now()::date`，跨零点 8 小时会多算/少算一天——与 M1.3-14 同一失败类 | `resolve_activity.go`（`days_visited` 窗口） |

#### 3.2.1 M1.3 执行记录（2026-11-12，`Models/Badges/conditions/`）

命令：`./scripts/test-api-isolated.sh -v ./app/Models/Badges/...`（一次性集群）
结果：`ok  Badges 2.925s` / `ok  Badges/conditions 2.032s`，0 FAIL。

| ID | 结果 | 证据 / 判据核对 |
| --- | --- | --- |
| M1.3-01 | PASS | `TestRegistryCatalogIsCompleteAndValidatable` |
| M1.3-02 | PASS | `TestActivityCountCumulativeAndWindow`（累计 5 / 30 天 1 / 90 天 3 三档互不污染） |
| M1.3-03 | PASS（task-6 后） | `TestActivityCountWindowCountsSoftDeleted`（软删主题 + 软删评论都计入窗口） |
| M1.3-04 | PASS | `TestContentAndBadgeConditions`（hidden / deleted 主题各 1）；另外 `hiddenSQL` **只查 `topics` 表**，结构上不可能把评论隐藏计入 |
| M1.3-05 | PASS | `TestActivityStreakEndsTodayOrYesterday` |
| M1.3-06 | PASS | `TestIdentityConditions` |
| M1.3-07 | PASS（task-6 后） | `TestObjectEngagement`：task-6 把 private 收藏的期望值从 false 翻成 true（与写路径一致）。三面口径仍不一致，见 M1.3-19 |
| M1.3-08 | PASS | `TestObjectAuthoring`（不存在的 tag slug → 0 且不 Incomplete） |
| M1.3-09 | PASS | `TestPrepareServesFromMemo`；`loadActivityCount` 一条 SQL 覆盖全部窗口 × 6 指标 |
| M1.3-10 | pend | 需 `M1-EXIT-PERF`（默认 10k） |
| M1.3-11 | PASS | `TestActivityCountExcludesSelfLikes`；SQL 两处 `r.user_id <> $1`（主题赞 + 评论赞） |
| M1.3-12 | PASS | `registry.go` 文案为「主题被隐藏 / 被隐藏的主题数」，无「违规/处罚/封禁」 |
| M1.3-13 | PASS | `pinnedSQL` = `author_user_id = $1 AND is_pinned`（当前态，取消置顶即减 1） |
| M1.3-14 | PASS | `activityStreakSQL` 用 `$2::date`，`today()` 取 `time.Local`；非注释的 `CURRENT_DATE` 零命中 |
| M1.3-15 | PASS（task-6 后） | `TestDaysVisitedWindowUsesProcessLocalDate`；`days_visited` 窗口下界改为 `$3::date`（Go 传入），静态检查零命中 `(now()-interval)::date` |
| M1.3-16 | PASS | 同 `TestRegistryCatalogIsCompleteAndValidatable`：13 条 + `ConditionMeta("fact.count")` 命中 |
| M1.3-17 | PASS | 同测试：空候选少 1 条、给候选后相等 |
| M1.3-18 | PASS（task-7 后） | 测试断言 `Options` 恰含 `wiki.edit`，并断言 `PublishableDefinitions` **没有污染 `Core()` 单例**（全集里 `factKey.Options` 仍为空） |
| M1.3-19 | PASS（task-7 后） | 窗口 SQL 去掉 `visibility` 过滤；三面并排表见下 |

**M1.3-15 的历史（已修，保留为断言设计的证据）**：原 `activityCountSQL` 的 `days_visited`
窗口下界是 `(now() - make_interval(...))::date`（PG 会话时区），而 `user_visits.visited_on`
按 API 本地日写入（`visits.go` 用 `time.Now`）——跨零点 8 小时边界差一天。task-6 已改成
`$3::date` 并补了 `TestDaysVisitedWindowUsesProcessLocalDate`。**这条之所以曾经漏网**：
M1.3-14 只盯 `CURRENT_DATE` 字面量，而 `(now()-interval)::date` 是同一失败类的另一种写法
（见 §9.2 规则 2）。修法很轻，教训很贵。

#### M1.3-19 bookmark 三面口径（task-7 后已一致）

| 面 | private 收藏是否计入 | 证据 |
| --- | --- | --- |
| `user_activity.bookmark_count`（累计） | **计入** | `postgres_store_bookmarks.go` 新建行恒 `relationDelta: 1`，与 `visibility` 无关 |
| `activity.count{bookmark_count, recent_days}`（窗口） | **计入**（task-7 已去过滤） | `resolve_activity.go` 窗口分支不再带 `b.visibility`；`TestActivityCountWindowedLikeReceived` 等仍绿 |
| `object.engagement{action:"bookmark"}` | **计入** | `objectEngagementSQL` 不按 `visibility` 过滤；task-6 已把测试期望翻成 true |

三面现在同口径。**这张表是规则 3（§9.2）的模板**：同一个指标在多个面各算一套时，
单点验任何一个面都会绿，只有并排才看得见差异——task-6 当时只改了两面，就是靠这张表发现的。

> 连带文档已由 lead 同批修正：`knowledge/modules/badges.md` 的计数表与 Reconcile SQL
> 现在都是「数全部行」，与写路径一致。

### 3.3 性能：窗口「获赞」P95

**夹具**（10k 用户，目标量级 200k 主题 / 2M reactions）：

```sql
INSERT INTO users (username, email, status)
SELECT 'perf'||g, 'perf'||g||'@example.test', 'active' FROM generate_series(1,10000) g;
-- topics：每用户 20 条，author_user_id 轮转，created_at 在 90 天内均匀
-- content_reactions：每主题 10 条 reaction='like'，user_id <> 作者；6 条在 30 天内、4 条在 30-90 天
-- user_activity：必须由真实写入路径回填（或按模块笔记 Reconcile SQL 重算），禁止手工填
ANALYZE;
```

**测法**：

1. 单用户 P95：循环 200 个不同 `user_id` 调 `EvaluateWithCounters`（换 id 避免缓存），记录 p50/p95/p99。
2. 查询计数：`pg_stat_statements` 前后差，验证 M1.3-09。
3. 端到端回扫：10k 用户全量 job 的 wall time 与 users/s。
4. **必须附 `EXPLAIN (ANALYZE, BUFFERS)` 实际计划**，不是只看耗时。
5. 判据是「计划中出现 `content_reactions_roster_idx`」，**不要求 Index Only Scan**（见 P-01：
   自赞排除让 `user_id` 不在索引里，退成 Index Scan + heap 过滤是正确代价）。
6. **探针 SQL 一律抄 resolver 的最终 SQL**，不要抄参考报告里的探针。第一版 `PERF-01` 少了
   自赞排除（`r.user_id <> $1`），复现的是小研那份探针形状，会得出「Index Only Scan，很好」的
   假结论；补上之后才是生产形状（Index Scan + heap 过滤）。
7. 若 planner 连 `roster_idx` 都没选，**先 `SET LOCAL enable_hashjoin = off`（脚本还额外关 mergejoin）
   复核一遍再下结论**：这一步验的是「没退化成全站顺序扫」，不是「能拿到 Index Only Scan」。
   关掉后仍不见 `roster_idx` 才判 FAIL。两次计划都要贴进证据。

**预算建议**（超预算不判 FAIL，判「需记录 + 需决策」，因为计划 §13.1 已声明这是待实测项）：

| 指标 | 建议预算 | 依据 |
| --- | --- | --- |
| 单用户评估 P95 | ≤ 50ms（20 条本人主题，warm） | 一次评估的查询数应 ≤ type 数 + 3；50ms 下 10k 回扫 ≈ 500s |
| 单用户 P99 | ≤ 150ms | 长尾只应来自 planner 误判 |
| 10k 全量回扫 | ≤ 10 min（≥ 17 users/s，单 worker） | 与 `badges.backfill_rules` 的运营可接受等待 |

**索引前提见 P-01：先 EXPLAIN，再谈加索引。**

### 3.4 `M1-EXIT-PERF`（M1 收口门，不是日常循环）

| 项 | 内容 |
| --- | --- |
| 断言 ID | `M1-EXIT-PERF` |
| 内容 | 在**默认 10k 规模**（10000 用户 × 20 主题 × 10 点赞 ≈ 2M reactions）上真实跑一次 `scripts/verify-badge-rules-perf.sh`，留下 `PERF-01` / `PERF-02` 的 `EXPLAIN (ANALYZE, BUFFERS)` 原文 |
| 时机 | **只在 M1 结项前跑一次**，不进每次提交的循环（2M reactions 要几分钟） |
| 当前状态 | **PASS**（2026-11-12 12:0x，10k 规模真跑；证据见 §9.4） |
| 为什么是门 | 它是 P-01「**不加** `content_reactions` 索引」这个结论的唯一证据。没有这次数据，那条结论只是小研在一张 62 行 reactions 的小表上做的推断 |
| 判据 | ① `PERF-01` 计划中出现 `content_reactions_roster_idx`（**Index Scan 亦可**，不要求 Index Only Scan）；② `PERF-03` 的 P95 在预算内（≤ 50ms）。两条同时成立 |
| 反判据（结论作废） | **关掉 hashjoin 后仍不走 `roster_idx`（含退化成 `content_reactions` 全站顺序扫），或 P95 超预算 → P-01 结论作废，M1.3 必须加索引**，并把新索引定义写回计划 §13.1 与本矩阵 P-01 |

证据落点：把 `EXPLAIN` 原文贴进本报告 §9.4，并在 `knowledge/plans/2026-11-12-badge-rule-engine.md` §13.1 标注「已实测」。

---

## 4. M1.4 规则存储 / 求值 / 回扫 / HTTP / 契约

| ID | 断言 | 验证方式 | 通过判据 | 失败归属 |
| --- | --- | --- | --- | --- |
| M1.4-01 | **快路径等价性**：单 `activity.count`+`window.all` 走旧 SQL | 见 §4.1 | 快路径用户评估只出现 `ListEnabledAutomaticBadges` + `GetCounters` 两条 SQL，**零**窗口 SQL；把同一规则改成 `any(count,count)` 后必须出现引擎 SQL（否则测的是死代码） | `evaluate.go` 双分支 |
| M1.4-02 | **纯 rule 徽章（metric NULL）能被授予** | 建 `automatic` + `rule` + `metric NULL` 的徽章，达标用户评估 | `user_badges` 出现新行。否则是 `ruleSatisfied` 的 nil metric 短路 | `evaluate.go`（见 P-04，最容易漏的 P0） |
| M1.4-03 | 幂等 | 同用户连续评估 3 次 | `user_badges` 行数不变、`top_badge_id` 不变、`badge_granted` 通知只 1 条 | `evaluate.go` + `GrantAutomatic` |
| M1.4-04 | 三来源判别式仍成立 | SQL 读 `granted_by` / `grant_reason` | 规则授予行 `granted_by IS NULL AND grant_reason=''`；回扫行**不得**写成 `'recompute'` | `backfill_rules.go`（见 P-08） |
| M1.4-05 | `rule` 三态：缺省 = 不改 | `PATCH {"name":"x"}` 后 SQL 读回 | `rule` 与两个 summary 字节级不变 | `dto.go` `mergeInto` |
| M1.4-06 | `rule` 三态：`null` = 清空 | (a) `PATCH {"rule":null}`；(b) `PATCH {"rule":null,"grantMode":"manual"}` | (a) 422 `forum.badge_rule_invalid`（automatic 必须 rule 非空）；(b) 200 且 `rule` 落 NULL、metric/threshold 清空 | `dto.go` + 校验 |
| M1.4-07 | `rule` 三态：对象 = 替换；只改 summary 不清空 rule | `PATCH {"requirementSummary":{"zh":"…"}}`；再 `PATCH {"rule":{…}}` | 前者 `rule` 不变；后者 `badges.rule` 等于新对象（`jsonb` 相等） | `dto.go` `mergeInto` |
| M1.4-08 | 422 错误码映射 | `POST/PATCH` 触发五种非法规则 | 超上限→`forum.badge_rule_too_complex`；未知 key→`forum.badge_rule_unknown_condition`；环→`forum.badge_rule_cycle`；失效引用→`forum.badge_rule_reference_invalid`；插件未声明 key→**unknown_condition**（不是 reference_invalid，也不是 fact_write_denied） | `errors.go` + controller |
| M1.4-09a | 环引用：**2 枚互指** | A 依赖 `owned{B}`、B 依赖 `owned{A}`，先存 A 再存 B | 第二个请求 `422 forum.badge_rule_cycle` | `rule_store.go` |
| M1.4-09b | 环引用：**3 枚互指** | A→B→C→A | 第三个请求 `422` | `rule_store.go` |
| M1.4-09c | 环引用：**自引用** | A 依赖 `owned{A}`（保存 A 时引用自己） | `422`。**DFS 最常漏这一组**：很多实现只在「遇到已访问节点」时判环，而自引用在第一步就撞上自己，容易被当成「已访问 = 正常回溯」。三组必须是三个独立子断言，不能合并成一句「环检测通过」 | `rule_store.go` |
| M1.4-09e | **反向断言：菱形依赖必须保存成功** | A 依赖 B 和 C，B、C 都依赖 D（无环） | 保存成功，**不得** 422。只验「该拒的拒了」会把「拒得太多」当成通过：用「已访问集合」判环的实现会把合法菱形误判成环。老后的实现用「当前路径集合」是对的——这条断言钉住它 | `rule_store.go` |
| M1.4-09d | 环检测只在**保存期** | 评估一条已存在的环规则（若有） | 评估不死循环；判环由保存路径负责。**不要**为了「能跑」把环检测挪到评估期 | `rule_store.go` |
| M1.4-10 | 影响估算 | `GET /admin/badges/:id/rule-impact` | 返回 `{estimated:int, method:非空}`；AND 取最小、OR 取和；复杂规则不 500 | `rule_impact.go` |
| M1.4-11 | 健康清单 | `GET /admin/badges/rule-health` | 列出失效条件/失效引用的徽章 slug；空库返回 `[]` 而非 `null`；provider 存在性见 M1.4-16 | `rule_store.go` |
| M1.4-12 | 回扫 job 可续 + 幂等 + 不阻塞 HTTP | 保存规则后看响应时间；kill 后重入；连续两次保存看 `river_job` | HTTP < 500ms 且 job 已入队；重入不重复授予；同 `{badgeId,cursorUserId,batch}` 的 `ByArgs` 去重生效 | `Jobs/Badges/backfill_rules.go` + `bootstrap` 注册 |
| M1.4-13 | **公开面不泄露 AST** | 见 §4.2 | 响应键集合无 `rule`/`cmp`/`params`/`children`/`op`/`type`；源码级：公开 DTO 结构体不含 rule 字段；`Cache-Control: private, no-store` 不变 | `dto.go` 公开 DTO |
| M1.4-14 | `requirementText` 回落矩阵（**领域层 + wire 层两层**） | 4 组合：都填 / 只填 zh / 只填 en / 都没填（单条件 vs 多条件）；另 `curl /api/v1/badges` 断言每条都有该键 | 领域层：每种组合两种语言非空且非裸键、运营值优先；**wire 层：OpenAPI 的 `PublicBadgeCatalogEntry` 把 `requirementText` 列为 required，响应里每条都必须有该对象** | `Models/Badges/requirement_text.go`（领域）/ `Controllers/Badges/dto.go`（wire） |
| M1.4-15 | 契约一致 | `CMD-OAS` + `go test ./app/Http/Controllers/Badges/` | 0 失败；三个新端点、`rule` 三态、5 个新错误码进 `contracts/openapi/paths/badges.yaml` 与 `schemas/badges.yaml` | `contracts/` |
| M1.4-16 | `rule_health` 判 provider 存在性要认「已声明但零绑定」 | 造一条引用 provider P 的规则：P 只在 identity registry 里声明，`identity_external_links` 里零行 | `rule-health` 判该规则**有效**。只查 `identity_external_links` 就会把新装插件上线瞬间的所有规则误报失效（插件刚装、还没人绑定） | `rule_health.go` |
| M1.4-17 | `rule_health` 不得把「零授予」当失效 | 一条合法规则、零用户持有 | 不出现在健康清单里；健康清单只回答「引用/条件是否还有效」，不回答「有没有人达标」 | `rule_health.go` |
| M1.4-18 | `recompute` 的 `queued` 语义：**徽章数**，不是用户数 | 同时存在一条复杂规则（需引擎）与一条快路径规则，跑 `POST /admin/badges/recompute` | 响应 `{granted, refreshed, queued}` 中 `queued = 1`（只有那一条复杂规则转入异步），`granted` 只反映快路径那条的授予数。`queued` 若按用户数计会随库大小漂移，运维无法据此判断「排了几条规则」 | `admin_service.go` / `rule_controller.go` |
| M1.4-19 | **快路径必须校验 `cmp.op = 'gte'`**（**store 层**：`GrantFromRules`） | 造一条 `activity.count` + `window.all` + `cmp:{op:"lte"}` 的规则，跑批量授予 | `lte` 规则**不进** SQL 选中集合（方向一：SQL 不许选 Go 认为非快路径的徽章），且计入 `queued`——即「不授予 + 进队列」，不是静默丢弃。不校验 `op` 的后果是**语义反转 + 大批量误授** | `evaluate.go` 快路径分支 |
| M1.4-21 | `recompute` 的 `queued` 三层：**store 语义 / 映射+契约键 / 完整往返** | ① store：`queued = len(complexBadgeIDs)`；② 映射层：控制器响应键集合 vs OpenAPI `BadgeRecomputeResult.required`；③ 完整往返：需管理员会话 | ① M1.4-18/19 已覆盖；② 响应键必须覆盖 `granted/refreshed/queued`；③ 未执行。**三层分开判，不写成笼统 pend** | `admin_service.go`（语义）/ `controller.go:699`（映射）/ 无会话（往返） |
| M1.4-22 | **契约级：OpenAPI `required` 列表 vs 真实响应键集合** | `ruby -ryaml -rjson` 读 `contracts/openapi/schemas/badges.yaml` 的 `required`，与 `curl /api/v1/badges` 的每条 entry 键集合求差 | 差集为空。这条抓的是「领域层有值、wire 少一个键」——`validate-openapi-refs.rb` 只验 `$ref` 可解析，抓不到这类漏法（M1.4-14 就是这么漏的） | `Controllers/Badges/dto.go` + `contracts/` |
| M1.4-20 | `rule_health` 要区分**两种失效** | ①引用不存在的对象（topic/category/tag/role/provider/badge slug）；②条件本身失效（`factKey` 未被任何扩展声明） | 两种都列进健康清单；provider 已声明零绑定仍有效（M1.4-16）；零授予不算失效（M1.4-17） | `rule_health.go` |

#### 4.3 M1.4 执行记录（2026-11-12，`rule_store.go` 等落地后）

命令：`./scripts/verify-badge-rules-acceptance.sh`（带 dev 库）
结果：**19 ok / 1 FAIL / 4 pend**（M1.4 共 24 条）。

| ID | 结果 | 证据 |
| --- | --- | --- |
| M1.4-01 | PASS | `TestFastPathSQLSubsetOfGoFastPath`（双向：SQL 选中 ⊆ `fastPathRule`，且内置形状不漏） |
| M1.4-02 | **pend** | **无专门测试**：现有夹具都带 legacy `metric`，`metric IS NULL` 的纯 rule 徽章授予路径没有被断言覆盖。**这是 P-04（静默永不授予）的回归缺口**，建议补一条 |
| M1.4-03 | PASS | `TestConcurrentEvaluationGrantsExactlyOnce` |
| M1.4-04 | **pend** | 无专门断言：回扫行 `grant_reason` 不得为 `recompute` 需人工核对 |
| M1.4-05 | PASS | `TestUpdateBadgeRuleTriState` A 段：只改 name → `rule::text` 与基线**字节级相同** |
| M1.4-06 | PASS（store 层） | 同测试 B 段：`RuleSet + nil` 清空。**HTTP 层「单独 `{"rule":null}` → 422」未被测试覆盖** |
| M1.4-07 | PASS | 同测试 C 段：对象替换，读回 `Cmp.Value == 3` |
| M1.4-08 | **pend** | 控制器层 422 映射无专门测试 |
| M1.4-09a…e | PASS | `TestBadgeRuleCycleDetection`（2 枚 / 3 枚 / 自引用 / 只在保存期 / 菱形必须成功） |
| M1.4-10 | PASS | `TestEstimateRuleImpactIsUpperBound` |
| M1.4-11 | PASS | `TestRuleHealthReportsOnlyBrokenReferences` |
| M1.4-12 | **pend** | 回扫 job 无测试（游标续跑 / `ByArgs` 去重 / 不阻塞 HTTP） |
| M1.4-13 | PASS | `curl /api/v1/badges` 键集合无 `rule`/`cmp`/`params`/`children`/`op` |
| M1.4-14 | **FAIL（wire 层）** | 领域层 `TestPublicBadgeRequirementText` 过；但 `curl /api/v1/badges` **12/12 条缺 `requirementText`**，而 OpenAPI 把它列为 `required`。见下 |
| M1.4-15 | PASS（refs） | `validate-openapi-refs.rb` 0 失败。**注意它只验 `$ref` 可解析，不验「required 字段真的输出」**——M1.4-14 的 wire 缺口就是它抓不到的 |
| M1.4-16/17/20 | PASS | `TestRuleHealthReportsOnlyBrokenReferences`（provider 零绑定有效 / 零授予不算失效 / 两种失效都列） |
| M1.4-18 | PASS | `TestGrantFromRulesCoversEveryMetricBranch`：`len(queuedIDs) == len(nonFast)`（徽章数） |
| M1.4-19 | PASS | `TestFastPathSQLSubsetOfGoFastPath`：`lte` 规则不进 SQL 选中集合，且计入 `queued`——即「不授予 + 进队列」 |

**唯一 FAIL：M1.4-14 wire 层——`requirementText` 只做到领域层**

`PublicBadge.RequirementText` 有值（`requirement_text.go` + `Public()`），`TestPublicBadgeRequirementText`
也过；但 `Controllers/Badges/dto.go` 的 `publicBadgeCatalogItem` **没有这个字段**，
OpenAPI 的 `PublicBadgeCatalogEntry.required` 里却有它。这是「过滤后重建结构」的典型漏法：
领域结构对、wire 结构少一个键。归属 `dto.go`（目录与资料页两个公开 DTO 都要检查）。

### 4.1 快路径等价性怎么证明（M1.4-01 展开）

计划 §12 只写「快路径回归测试」，没写**怎么证明走了哪条分支**。三种方法任选其一，必须在报告里留原始输出：

```sql
-- 方法 a：pg_stat_statements 前后差（推荐，最贴近生产）
SELECT pg_stat_statements_reset();
-- 调一次只含快路径徽章的用户评估
SELECT query, calls FROM pg_stat_statements
WHERE query ILIKE '%badges%' OR query ILIKE '%user_activity%' OR query ILIKE '%content_reactions%'
ORDER BY calls DESC;
```

- 判据：只出现 `ListEnabledAutomaticBadges` 与 `GetCounters` 形状的语句各 1 次；`content_reactions` / `topics` / `comments` 的窗口查询**0 次**。
- 反向对照（必须有，否则测的是死代码）：把同一用户的规则改成 `any(activity.count, activity.count)`，重跑，窗口查询必须出现。

方法 b：`log_statement=all` 抓一次评估的语句集。方法 c：测试内注入计数 store，断言 `conditions` 解析器零调用。

### 4.2 公开面不泄露 AST（M1.4-13 展开）

```bash
# 1) 键集合
curl -s "$SFORUM_API_URL/badges" -H 'Accept: application/json' \
  | jq -r '.. | objects | keys[]' | sort -u
# 期望：不含 rule / cmp / params / children / op / type

# 2) 值级兜底（键改名也能抓到）
curl -s "$SFORUM_API_URL/badges" | jq '[.. | .cmp? // empty, .. | .params? // empty] | length'
# 期望：0

# 3) 缓存头不变
curl -sI "$SFORUM_API_URL/badges" | grep -i cache-control
# 期望：private, no-store
```

源码级（防止「今天没泄露、下次加字段又泄」）：

```bash
grep -n "Rule\|rule" apps/api/app/Http/Controllers/Badges/dto.go
# 断言 publicBadgeCatalogItem / profileBadgeItem 两个结构体都不含 rule 字段
```

公开面是白名单结构，不是「admin DTO 减字段」——这是 `dto.go` 现有注释的原文口径，M1 必须延续。

---

## 5. 横切面四条（X）

| ID | 断言 | 验证方式 | 通过判据 | 失败归属 |
| --- | --- | --- | --- | --- |
| X-01 | 新 `t()` 键在两份 locale 存在（双向） | `cd apps/web && bun test tests/admin/adminBadgesI18n.test.ts` | `missing` 与 `extra` 同时为空；zh/en 键集合相等 | 见下方风险 |
| X-02 | 路由计数 tripwire | `CMD-RTE` | `418 → 421`（+3：`conditions` / `:id/rule-impact` / `rule-health`），且 `registry_test.go` 注释补一行「421 = 418 + 三条规则引擎路由」 | `Support/Routes/registry_test.go` |
| X-02b | 静态段先于 `/:badgeID` 注册（**只验注册顺序与 401，不验响应体**） | `TestRuleRoutesAreRegisteredBeforeParamRoutes` + `curl` 未认证 | 未认证返回 401 `auth.required`（证明没被 `:badgeID` 吃掉，不是 id 解析失败）；注册顺序测试绿。**200 响应体未验**（本会话无管理员会话），见 §11.2 #6 | `controller.go` `RegisterRoutes` 顺序 |
| X-03 | 新错误码 zh/en 文案（M1 四个已落地；`forum.badge_fact_write_denied` **属 M3 能力码**） | `CMD-L10N` | `TestAPIErrorCodesHaveLocalizedMessages` + `TestAPIErrorCodesAreLocalizedInEveryLocale` 全绿；M1 的四个码在两段 `messages.go` 各一条且文案 ≠ key。**M3 码缺失不算 M1 缺口**，不要当收口条件 | `Support/Localization/messages.go`（M3 码归 M3） |
| X-04 | 迁移 up/down 可逆 | `CMD-REV` | exit 0。**前置**：脚本必须新增 `202611120002` 分支（否则落进 `else` 去断言旧对象消失 → 假红 exit 6） | 迁移 Down 段 + 脚本分支 |
| X-04b | 多条件规则下 down 拒绝并给 HINT | `SFORUM_VERIFY_KEEP_CLUSTER=1` 保留集群，手工造一条多条件规则后单步 down | 非 0 退出 + stderr 含 HINT；拒绝时**无半回滚残留**（同一事务） | 迁移 Down 段 |

**X-01 的真实风险（跨切片耦合）**：`adminBadgesI18n.test.ts` 是**双向**断言（locale 里的键必须都被源码引用）。M1 若往 `zh-CN.json` / `en-US.json` 加 `conditions.*` 而前端编辑器要到 M2 才引用，`extra` 立刻红。正确姿势二选一：M1 **不加任何 locale 键**（条件 label 由后端 `badgeConditions` 目录下发，不走 `t()`），或加键与引用方同批落地。看到这条红，先查是不是「M1 提前加了 M2 的键」，别去改测试。

**X-02b 的陷阱**：现有 `/admin/badges/tiers` 与 `/:badgeID` 同形，控制器注释已写明「静态段必须先注册，否则 `DELETE /admin/badges/tiers` 会被解析成 `badgeID="tiers"`」。M1 新增的 `conditions` / `rule-health` 是同一形状，必须同款处理并留下回归测试。

**X-04 的前置缺口**：`verify-migration-reversible.sh` 的 down 断言按 `TARGET_VERSION` 分支写（现有 `202611120001` / `202611100004` / `else` 三个分支）。`202611120002` 没有分支时会走 `else`，去断言 `user_activity` / `badges` 等表已删除——而这些表在 202611120002 的 down 后**仍然存在**，脚本会 exit 6。新分支至少要断言：

- down 后：`badges.rule` / `requirement_summary_zh` / `requirement_summary_en` 三列消失；`badge_facts` / `user_fact_counters` / `badge_rules_rollback` 三表消失；`sforum_core_v1.forum_user_badge_facts` 消失；`forum_badges` 列集恢复；`badges_rule_check` 恢复旧定义（automatic→metric+threshold）；goose 版本 = `202611120001`。
- up 后：上述对象全部重建；`badges.rule IS NOT NULL` 行数 = automatic 徽章数（内置基线 **10**）；`badge_rules_rollback` 行数 = 同数；内置徽章仍是 12 枚（10 自动 + 2 手动），手动两枚 `rule IS NULL`。

---

## 6. 计划未覆盖但必须验（P）

单列，不计入 M1 四片；每条都是「按计划字面实现会留下缺口」的地方。

### P-01 窗口「获赞」**不加索引**（已两次实测修正，判据不再要求 Index Only Scan）

**结论：`content_reactions` 不加索引。** 这个结论经过两次 EXPLAIN 修正，过程本身是验收依据：

| 阶段 | SQL 形状 | 计划 | 说明 |
| --- | --- | --- | --- |
| 第一次（小研探针） | 无自赞排除 | **Index Only Scan** using `content_reactions_roster_idx` | 探针形状的结论，**不适用于最终 SQL** |
| 第二次（含自赞排除） | `r.user_id <> $1` | **Index Scan + heap 过滤** | `user_id` 不在 `roster_idx` 里，必须回表；10k 实测默认 1.98ms / `enable_seqscan=off` 2.46ms |

**这是正确的代价，不是缺陷。** 因此：

- `PERF-01` 判据 = 「计划中出现 `content_reactions_roster_idx`」，**不要求 Index Only Scan**；
- 「关掉 hashjoin 复核」保留但降级为「没退化成全站顺序扫」的证据；
- 关掉后仍不见 `roster_idx`，或 P95 超预算 → 本条作废，M1.3 必须加索引（新索引定义要含 `reaction` 与 `user_id`，见下）。

**不要**预建计划原提议的 `content_reactions (target_type, target_id, created_at)`：它缺 `reaction`，
对本查询形状更差；即便要加，也必须是 `(target_type, target_id, reaction, created_at)` 并评估
`user_id` 的取舍。

### P-02 `badge_rules_rollback` 的写入时机计划未定

§4.3 只说「只在 down 迁移里读」。若 Up 不写，down 无法区分「迁移转换出的单条件规则」与「运营后来手改的单条件规则」，于是「只有多条件才拒绝」的判据不成立：运营把规则改回单条件后，down 会静默还原成错的 `metric`/`threshold`。

要求：Up 段为每个 automatic 徽章写一行（`rule` = 迁移后规则，`legacy_*` = 迁移前值）。Down 段比对 `badges.rule` 与 `badge_rules_rollback.rule`：相同 → 还原 legacy；不同且可逆 → 按当前规则反推；不同且不可逆 → 拒绝 + HINT。对应断言 M1.1-05。

### P-03 fail-closed 与 `not` 组合会把「失败」反转成「授予」

§0#12 fail-closed + §5.1 `not` 节点：若实现成「error → false」，则 `not(false) = true`，事实查询失败反而**授予**徽章——与 fail-closed 的意图完全相反。

要求：三值逻辑（true/false/unknown），`not(unknown)=unknown`，根节点 unknown 视为不授予，并回传错误清单供可观测事件使用。断言 M1.2-06，判据里明确写「`not(errLeaf)` 授予即 P0 FAIL」。

### P-04 `ListEnabledAutomaticBadges` 的 nil metric 短路

现有 `ruleSatisfied`（`evaluate.go:78`）对 `Metric == nil` 返回 false；而纯 rule 徽章的 `metric` 是 NULL。双分支若写错（例如仍先过 `ruleSatisfied`），新徽章永远不授予且**没有任何报错**——表现为「保存成功、用户达标、就是不发」。

要求：断言 M1.4-02 必须在，且失败信息指名「nil metric 短路」。

### P-05 新 HostQuery 的注册面是 5 处，不是 2 处

计划 §4.4 只写「新增 HostQuery + schema + allowed/denied 测试」。实际还要同步：

| # | 位置 | 断言形式 |
| --- | --- | --- |
| 1 | `app/Support/HostAPI/v2_query_runtime.go` | 常量 `QueryPublicUserBadgeFactsList` |
| 2 | `app/Support/HostAPI/query_registry_core.go` | QueryID/ContractVersion/Entity/PlanVersion |
| 3 | `app/Support/HostAPI/query_registry_core_badges_test.go` | 映射表一行 |
| 4 | `bootstrap/sealed_query_catalog_test.go` | `sealedCoreQueryCatalogIDs` 列表 |
| 5 | `app/Http/Controllers/Extensions/query_catalog_test.go` | `sealedQueryCatalogIDs` 列表 |

第 4、5 处都是**集合相等**断言（不是计数），所以漏加会直接报「集合不一致」并列出差异 ID —— 比路由那条的纯计数友好，但仍必须同步。断言 M1.1-08。

### P-06 迁移不得触碰 `user_activity`

计划只写「加列 + 转规则」。若迁移顺手回填计数（看起来无害），会改变规则输入，把「规则转换错误」伪装成「统计修正」。断言 M1.1-12（`counter_before` 全表逐行比对）。

### P-07 `content.hidden` 与「软删」的边界必须在同一张表里写清

§5.2 说「软删内容仍计入做过什么；`content.hidden` 是唯一看 status 的条件」。但 `topics.status` 里 `deleted` 与 `hidden` 是**同一列的两个值**：`content.hidden` 若写成 `status <> 'active'`，就把 deleted 也算进去了，与「唯一看 status」的语义不符（它该只看 hidden 这一个值）。

断言 M1.3-04 的 3 行夹具（hidden / deleted / active 各 1）→ 期望计数 1。

### P-08 回扫与重算是两个语义，别合并

保存规则 → 回扫（补齐应得）；`POST /recompute` → 全量重算 + `top_badge_id` 刷新（有 `refreshed`）。若回扫复用 recompute 路径，会把 `grant_reason` 写成 `'recompute'`，**三来源判别式失效**（模块笔记 §Grant Sources 是运维审计依据）。

要求：回扫授予行 `granted_by IS NULL AND grant_reason=''`。断言 M1.4-04。

### P-09 公开面 `progress` 新形态的向后兼容

§9 只说「`Progress` 扩展为 `{mode,current,threshold,satisfied,total}`」。现有公开 DTO 用指针 + `omitempty` 表达「没有这个键」，前端 `SFProfileBadgeTab` / `SFBadgeChip` 已按旧形状实现（`dto.go` 顶部的指针字段注释就是这条契约）。

要求：旧字段保留一版且语义不变，或明确 bump 契约版本并同批改前端；不允许出现「后端改了形状、M2 还没做」的中间态发布。断言 M1.4-14 + `CMD-OAS`。

### P-10 `requirementText` 的多语言回落顺序未定

§13.10 说「只填一种时另一种回落到规则自动生成句」，但没定「运营填的 en 为空、自动生成的 zh 存在时，zh 站点显示哪句」。

要求定为「逐语言独立回落」：`zh = 运营 zh || 生成 zh`、`en = 运营 en || 生成 en`；不能「任一语言为空就两种都用生成句」（会让已填的语言被覆盖）。断言 M1.4-14 的 4 组合矩阵。

### P-11 `object.engagement{action:comment}` 含「自己评论自己的主题」（**已裁决：算**，**仅限 comment**）

`comments(topic_id)` 的真值来源不区分作者。用户裁决（2026-11-12）：**包含**自己主题下的评论，不加自排除——与 `comment_count` 含自评论一致，且规则层无法表达「主题作者 ≠ 我」，隐藏例外只会造成「规则看着满足却不发」。

> **适用范围收窄（2026-11-12 小研核实）**：这条裁决**只适用于 `object.engagement{action:"comment"}`**，
> **不适用于获赞**。获赞必须排除自赞（M1.3-11）：`activityDeltaForReaction` 对自赞返回 `0,0`，
> 累计列本就不含自赞，窗口版若把自赞算进去，两个口径就会分叉。把 P-11 写成「自己产生的互动都算」
> 会直接和 M1.3-11 打架。

判据（M1.3-07 拆成正反两条）：

| 断言 | 夹具 | 期望 |
| --- | --- | --- |
| M1.3-07a | A 在自己主题 T 下评论过 | `object.engagement{action:comment,target:{topic:T}}` = true |
| M1.3-07b | A 未在 T 下评论（只有他人评论） | false |
| M1.3-07c | T 被软删（`status='deleted'`）但 A 的评论行仍在 | 见 P-07：本条按「软删内容仍计入做过什么」= true，除非 M1.3-03 口径被推翻 |

M2 附带要求：后台条件说明文案必须写明「含自己主题下的评论」，否则运营会以为它是互动指标。

### P-12 畸形树必须 fail-closed（空组 / `not` 元数 / 错形组）

**状态：已裁决（Lead 2026-11-12，选项 ①）+ 已落地（task-5）+ 已验证。**

原始缺陷（2026-11-12 实测）：`Validate` 拒空 `all`，但 `Eval` 对空 `all` 返回
`matched=true, incomplete=false, errors=0`；而 `Eval` 文档明确「不要求先 Validate」。
一条绕过保存校验写进 `badges.rule` 的空 `all`（手工改库 / 导入工具 / 未来迁移）会在下次回扫时
**给全库用户授予该徽章**，零错误零日志。

裁决口径（`Eval` 自己兜住畸形树，不依赖调用方先跑 `Validate`）：

| 形状 | 期望 |
| --- | --- |
| 空 `all` / 空 `any`（`len(children)==0`） | 记 `CodeShapeInvalid`，节点 = **unknown** |
| `not` 子节点数 ≠ 1 | 记 `CodeShapeInvalid`，节点 = unknown |
| 组节点带 `type`/`params`/`cmp` | 记 `CodeShapeInvalid`，节点 = unknown（不得静默当真值） |
| 叶子带 `children` | 同上 |
| 根节点 unknown | `Matched=false` + `Incomplete=true`（既有语义不动） |

**通过判据**：脚本用**独立探针**（不是实现方自己的测试）直接调 `Eval`，六个畸形形状全部
`matched=false incomplete=true codes=shape_invalid`，且 `normal_all_true` 仍
`matched=true incomplete=false errors=0`（兜底不得误伤正常路径）。行为正确后，还要求三条回归
测试在位：`TestEvalEmptyGroupStaysUnknown` / `TestEvalNotArityStaysUnknown` /
`TestEvalMalformedGroupStaysUnknown`——防止下次被改回去。

**失败归属**：`Support/RuleEngine/eval.go`。**FAIL 判据**：任一畸形形状出现
`matched=true`，或正常 `all` 被降级为 unknown，或三条测试缺失（后者记 pend，属交付缺口）。

**2026-11-12 10:48 探针输出（`task-5` 落地后）**：

```
empty_all            matched=false incomplete=true  errors=1 codes=shape_invalid,
empty_any            matched=false incomplete=true  errors=1 codes=shape_invalid,
not_zero             matched=false incomplete=true  errors=1 codes=shape_invalid,
not_two              matched=false incomplete=true  errors=1 codes=shape_invalid,
group_with_type      matched=false incomplete=true  errors=1 codes=shape_invalid,
leaf_with_children   matched=false incomplete=true  errors=1 codes=shape_invalid,
normal_all_true      matched=true  incomplete=false errors=0 codes=
```

> 探针刻意不复用实现方的测试：若测试本身写错（例如断言空 `all` 应为 true），
> 「测试全绿」推不出「行为正确」。探针编译在 `apps/api/.reviewer-p12-probe-$$`（点开头，
> go 工具链忽略），跑完立即删除；`SFORUM_ACCEPT_NO_PROBE=1` 可跳过并记 pend。

---

## 7. 已知残留（不判 FAIL，计划 §13 已声明）

| 计划条目 | 本矩阵的处理 |
| --- | --- |
| §13.1 窗口获赞成本 / 索引 | M1.3-10 只要求**记录** P95 与 EXPLAIN，超预算不算 FAIL；索引决策见 P-01 |
| §13.2 影响人数是估算 | M1.4-10 只断言 `{estimated, method}` 存在且不 500，不比对精确值 |
| §13.3 `RecomputeAll` 仍同步 | 不验；只要求规则保存触发的回扫走 job（M1.4-12） |
| §13.4 规则改严不自动收回 | 视为设计；§2.3 第二方向据此收窄 |
| §13.5 插件判定器（A 路径）未做 | 不验 |
| §13.6 查看类条件未做 | 不验 |
| §13.7 采纳/精华/热榜未做 | 不验 |
| §13.8 举报类不做 | 不验 |
| §13.9 trust levels 未共用引擎 | 不验（`LevelOf` 不得被改动：`git diff` 断言） |
| §13.10 公开面双列文案 | 见 P-10（回落顺序仍需定，但「双列」本身不判 FAIL） |

**本刀新增残留（计划 §13 未列，2026-11-12 老后核实）**：

| ID | 残留 | 验收处理 |
| --- | --- | --- |
| R-1 | `badges.rule`（JSONB）**只进** `sforum_core_v1.forum_badges` 视图，**不进** `sforum.core.public_badges.list` 的 `Fields` | **不判 FAIL**。JSONB 进 HostQuery 要先在 `normalizeProtocolV2QueryValue` 定传输形态（现只认 `[]byte`/`string`/`int`/`time`），留 M3 与 `badgeConditions` 同批。断言 M1.1-13 验的是「视图有、HostQuery 无、公开 web 面也无」，**不要**写成「视图有 ⇒ HostQuery 也有」 |

模块笔记残留的验收口径同样适用：dev admin 写端点 429 → 退避重试后仍 429 记 **pend**，不判 FAIL（残留 #6）；`revoked:false` 不是失败（残留 #2）；缓存 20–30s 延迟不判失败（残留 #5）。

---

## 8. 执行顺序与收口

1. **M1.2**（无 DB，最快）→ 先钉死 AST 与三值逻辑，避免 M1.3/M1.4 建在错语义上。
2. **M1.1**（一次性集群）→ 先跑 §2.1 快照，再迁移，再 §2.2/§2.3 比对。**顺序不能反**：迁移做完再补快照就没基线了。
3. **M1.3**（PG 集成 + 性能夹具，最慢）→ 夹具一次建好，M1.3-01…10 共用。
4. **M1.4**（需 `api-dev` 与管理员会话）。
5. **X**（横切面四条）→ 最后跑，因为它们最容易因别人的改动而红。

收口门禁（计划 §1 第 7 条）：

```bash
./scripts/test.sh --fast
node tests/validate-architecture-boundaries.mjs
ruby scripts/validate-openapi-refs.rb
```

判定汇总模板（交付给 lead 时填）：

| 切片 | 断言数 | PASS | FAIL | pend |
| --- | --- | --- | --- | --- |
| M1.1 | 13 | | | |
| M1.2 | 7 | 7 | 0 | 0 |
| M1.3 | 19 | 18 | 0 | 1 |
| M1.4 | 28 | 27 | 0 | 1 |
| X | 6 | | | |
| P | 12 | 2 | 0 | 10（P-01 已由 §9.4 的 10k 证据覆盖；其余为文档级、未脚本化） |
| M1-EXIT-PERF | 1 | 1 | 0 | 0 |

**FAIL 必须带：断言 ID、原始命令/输出、复现步骤、归属文件。** 「看起来应该没问题」不是 PASS，也不是 pend。

---

## 9. 执行记录

### 9.1 2026-11-12 10:44 初验（dev 库已迁移到 202611120002）

```
DATABASE_URL='postgres://sforum:sforum@127.0.0.1:15432/sforum?sslmode=disable' \
  ./scripts/verify-badge-rules-acceptance.sh
→ ok 19 / FAIL 0 / pend 32，exit 0
```

| 断言 | 结果 | 备注 |
| --- | --- | --- |
| M1.2-01…07 | 7× ok | 见 §3.1.1 |
| M1.1-01/02/03/05/06/07/08/10 | 8× ok | 迁移结构、规则转换（10 枚 automatic）、回滚表 10 行、两个视图、HostQuery 五面、真值等价（17 个达标组合） |
| M1.1-11/12 | pend | **dev 库已迁移，基线快照无法补拍**：必须在新的一次性集群上重放（checkout 迁移前 → up 到 202611120001 → §2.1 快照 → 迁移 → 比对） |
| M1.1-04/09 | pend | 需一次性集群造自定义 automatic 徽章 / 管理员会话调 recompute |
| M1.3-*、M1.4-01…12/14/15 | pend | 切片未落地 |
| M1.4-13 | ok | 公开目录响应键集合无 `rule`/`cmp`/`params`/`children`/`op` |
| X-01 | ok | `adminBadgesI18n.test.ts` missing/extra 均为空（M1 未加 locale 键，符合约定） |
| X-02 | ok | 路由计数 418（M1.4 路由未落地，期望值随 `rule_store.go` 落地变 421） |
| X-02b / X-03 / X-04 | pend | 路由未注册 / 5 个新错误码未落地 / 可逆脚本缺 `202611120002` 分支（task-1 交付缺口） |
| P-01 | pend | 解析器未落地 |
| P-12 | FAIL（当时） | 空 `all` 在 Eval 层为 true；（task-5 落地前为 FAIL，落地后转 ok）（见 §9.2） |

**这次初验抓到的问题不是迁移的**：第一版 M1.1-03 写死 `{root,children,0,...}`，在真库上误报 10/10 不等价。根因是计划 §5.1 示例（root=组）与迁移实现（root=叶子）形状不同，而断言只认了其中一种。已按「生效叶子」重写 SQL（§2.5-B）并复验 0 mismatch——**这正说明回归断言必须在真库上跑过，不能只审 SQL**。

### 9.2 2026-11-12 10:48 P-12 复验（task-5 落地后）

```
DATABASE_URL=... ./scripts/verify-badge-rules-acceptance.sh
→ ok 19 / FAIL 0 / pend 32，exit 0
  ok  P-12 畸形树 fail-closed（空组/not 元数/错形组 → unknown+shape_invalid，正常路径未误伤）
```

`eval.go` 由 5394B → 6870B（10:47），`eval_test.go` 新增三条测试（264/281/306 行）。
独立探针输出见 P-12 段。

**顺带发现一处测试改名**：`TestValidateDaysRangeIndependentOfSchema` 在 task-2 收尾时被改名为
`TestValidateWindowDaysRangeIndependentOfSchema`（同时新增「window kind=all 带 days」「recent_days
缺 days」两条用例，覆盖更严）。我的脚本第一版写死旧名，导致 M1.2-03 从 ok 退成 pend——
已改成按名字族匹配（`TestValidate.*DaysRangeIndependentOfSchema`）。

#### 两条通用规则（并排，同一类毛病：断言比被测代码更脆）

**规则 1 —— 不写死测试全名。** 断言按「测试名前缀族 + 行为」匹配。写死全名的后果是把
「实现方改名」误报成「未落地」（本例 M1.2-03 从 ok 退成 pend）。族匹配 + 行为探针
（如 P-12 的独立探针）是更稳的组合：名字族负责「有没有」，探针负责「对不对」。

**规则 2 —— 按失败类写，不按具体写法写。** M1.3-14 原本只验 `CURRENT_DATE` 零命中，
实现换成 `(now() - make_interval(...))::date` 就绕过去了——两者是同一个失败类
（用 PG 会话时区取日界）。按字面量写，等于给实现留后门。**判据里要写「这类错误都不许」，
而不是「这个字面量不许」。**

**规则 3 —— 同一个指标出现在多个面时，必须用表格并排验。** 本次的现场：`bookmark_count`
在「累计（`user_activity`）/ 窗口（`activity.count`）/ `object.engagement`」三个面各算一套，
单点验任何一个面都会绿，只有把三面并排成表才看得见窗口那处没改（M1.3-19）。
**判据模板**：列 = 各个面，行 = 「private 是否计入」这类语义维度，每格必须给出代码位置或测试名。

> 三条说的是同一件事：规则 1 产生**假红**，规则 2 产生**假绿**，规则 3 产生**局部绿**——
> 每个面单独看都对，合起来才错。写断言时依次问：实现改名还成立吗？换等价写法还成立吗？
> 同一个语义在别的面是否也成立？

### 9.3 2026-11-12 口径同步（小研 12 条参考 SQL 落地后）

#### 共同点：这五条都是「同环境测不出来」的失败模式

本地跑绿、线上出事——自赞、评论隐藏、曾经置顶、跨时区零点，四个都满足这一点。
所以夹具必须**显式构造**，不能靠 dev 库现有数据；「我这边跑过了」对这四类不构成证据。

五处口径变化已写进断言，**均来自实现侧取证，不是我的推断**：

| # | 口径 | 影响断言 |
| --- | --- | --- |
| 1 | 窗口「获赞」必须排除自赞（`r.user_id <> $1`）——`activityDeltaForReaction` 对自赞返 `0,0`，累计列本就不含自赞 | 新增 **M1.3-11**：自赞后累计与窗口两个口径都必须是 0（只验累计会漏掉窗口版的错） |
| 2 | P-11「自评论算」**不适用于获赞** | P-11 加适用范围收窄注记；否则与 M1.3-11 打架 |
| 3 | `content.hidden` 只统计主题 `status='hidden'`；隐藏评论不计；`deleted`/`pending`/`rejected` 不计 | 改写 **M1.3-04** 夹具与判据；新增 **M1.3-12** 文案口径（不得出现「违规」「处罚」「封禁」——库里没有处罚记录） |
| 4 | `content.pinned` = **当前仍被置顶**的主题数，不是「曾经」 | 新增 **M1.3-13**：取消 1 条置顶后计数必须从 2 变 1；断言里不许出现「取消后仍为 1」 |
| 5 | 连续天数的日期由 Go 传入，SQL 不许出现 `CURRENT_DATE` | 新增 **M1.3-14**：API `TZ=Asia/Shanghai` + PG UTC，造「今天刚访问」的用户；同 TZ 环境测不出跨零点误判 |

M1.3 断言数由 10 增至 14。这五条的共同点：**都是「同环境测不出来」的失败模式**（自赞、评论隐藏、
曾经置顶、跨时区零点），所以夹具必须显式构造，不能靠 dev 库现有数据。

### 9.4 `M1-EXIT-PERF` 证据（10k 规模，2026-11-12 12:00 首次真跑）

夹具：10000 用户 / 200000 主题 / 1,999,800 点赞（窗口内 60%），写入耗时 **286s**，
一次性集群 + 完整迁移链，不读 dev 库。

**PERF-01 窗口「获赞」（单主题形状，含自赞排除，与 resolver 最终 SQL 同形）**

```
Aggregate  (cost=17.36..17.37 rows=1 width=8) (actual time=0.131..0.132 rows=1 loops=1)
  ->  Index Scan using content_reactions_roster_idx on content_reactions
        (cost=0.43..17.35 rows=6 width=0) (actual time=0.094..0.128 rows=6 loops=1)
        Index Cond: ((target_type = 'topic') AND (target_id = 80000) AND (reaction = 'like')
                     AND (created_at >= (now() - '30 days'::interval)))
        Filter: (user_id <> 1)
Execution Time: 0.189 ms
```

判据成立：计划中出现 `content_reactions_roster_idx`（**Index Scan**，非 Index Only Scan——
`user_id` 不在索引里，回表过滤自赞是预期代价）。默认计划**没有**退化，无需关 hashjoin 复核。

**PERF-02 本人主题列表**

```
Bitmap Heap Scan on topics
  Recheck Cond: ((author_user_id = 1) AND (created_at >= ...))
  ->  Bitmap Index Scan on topics_author_created_idx
Execution Time: 0.165 ms
```

**PERF-03 单用户 P50 / P95**（200 个不同 user_id，窗口获赞规则，走引擎路径）：

```
PERF03 users=200 failed=0 p50=2.19ms p95=3.47ms p99=3.97ms
→ ok  P95 3.47ms ≤ 预算 50ms
```

**PERF-04 全量回扫吞吐**：仍 pend（回扫 job 未接进脚本，属未覆盖项，见 §11.2 #7）。

**`M1-EXIT-PERF` 判定：PASS。** 判据两条同时成立——计划中出现 `content_reactions_roster_idx`
（Index Scan），且 P95 3.47ms 远低于 50ms 预算。P-01「不加索引」在 10k / 2M 行上成立。

> 结论：P-01「不加 `content_reactions` 索引」在 10k / 2M 行上成立。`Index Scan + Filter`
> 是自赞排除的正确代价；执行 0.19ms 量级，与老后 10k 实测（默认 1.98ms、关 seqscan 2.46ms）
> 同一量级（差异来自夹具分布与缓存状态，不影响结论）。

---

## 11. M1 收口状态与未覆盖项（2026-11-12）

### 11.1 全量计数

```
./scripts/verify-badge-rules-acceptance.sh（带 dev 库，queued 修复 + 映射抽取后）
→ ok 74 / FAIL 0 / pend 9
```

**已无 FAIL。** M1.4-21 的 `queued` 缺陷已修复并升级成单测（§11.7）；剩余 pend 见 §11.4/§11.6。

> 计数口径：`ok/FAIL/pend` 是脚本的**输出行数**（同一条断言可能产生两行，例如 M1.4-14 的领域层与 wire 层、
> M1.3-15 的测试与静态检查）；下表按**断言 ID** 计。

| 切片 | 断言数 | ok | FAIL | pend |
| --- | --- | --- | --- | --- |
| M1.1 | 13 | 9 | 0 | 4（快照/一次性集群/管理员会话） |
| M1.2 | 7 | 7 | 0 | 0 |
| M1.3 | 19 | 18 | 0 | 1（性能，归 M1-EXIT-PERF） |
| M1.4 | 28 | 27 | 0 | 1 |
| X | 6 | 4 | 0 | 2（X-03 缺 `forum.badge_fact_write_denied`——**属 M3 能力码**；X-04b 多条件 down 拒绝未脚本化） |
| P | 12 | 2 | 0 | 10（P-01 已由 §9.4 的 10k 证据覆盖；其余为文档级、未脚本化） |
| M1-EXIT-PERF | 1 | 1 | 0 | 0 |

**task-8 落地后曾全绿（72/0/9）；把 M1.4-21 从「缺会话无法验」改成验映射层后，暴露 1 个真 FAIL（`queued` 被控制器丢弃）。**
其余 9 条 pend 全部是「需要额外环境/会话/一次性集群」的项，见 §11.4。

### 11.2 未覆盖但应该验（按风险排序）

| # | 项 | 为什么必须补 |
| --- | --- | --- |
| 1 | **M1.4-02 纯 rule 徽章（`metric IS NULL`）的授予路径无测试** | P-04 是本刀最贵的失败模式（保存成功、达标、永不发、零报错）。现有夹具全部带 legacy `metric`，双分支写错也全绿 |
| 2 | **M1.4-12 回扫 job**（游标续跑 / `ByArgs` 去重 / 不阻塞 HTTP） | 计划 §7 的核心承诺，目前只有代码没有证据；「HTTP 内同步跑」是既有残留 #17，最容易被悄悄扩大 |
| 3 | **M1.4-04 三来源判别式** | 回扫行若写成 `grant_reason='recompute'`，运维审计的三来源判别式失效（模块笔记 §Grant Sources） |
| 4 | **M1.4-08 控制器 422 映射** | 五个错误码的 HTTP 边界（`rule_too_complex` / `unknown_condition` / `cycle` / `reference_invalid`）没有端到端断言 |
| 5 | **M1.4-06 HTTP 分支**：单独 `{"rule":null}` → 422 | store 层三态已覆盖，但「automatic 必须 rule 非空」的 HTTP 拒绝分支没测 |
| 6 | **三条新路由的 200 响应** | 本次只有 401（`auth.required`，证明没被 `/:badgeID` 吃掉）+ 注册顺序测试；没有管理员会话，拿不到 200 与真实响应体 |
| 7 | **PERF-04 全量回扫吞吐** | `M1-EXIT-PERF` 只要求 P95；吞吐（10k ≤ 10min）仍无证据 |
| 8 | **资料页墙的 `requirementText`** | OpenAPI 的 `ProfileBadgeItem` 未列为 required，但计划 §9 说公开面要有；M2 视觉落地前需定口径（目录有、资料页没有，用户会困惑） |

### 11.3 已确认可用但需要注意的边界

- `GET /api/v1/badges` 与 `/profiles/:username/badges` 的键集合都不含 `rule`/`cmp`/`params`/`children`/`op`，
  值级 `jq '[.. | .cmp? // empty]'` 也为 0（M1.4-13 PASS）。
- `Cache-Control: private, no-store` 保持（查看者 `earned` 相关）。
- 三条规则路由的注册顺序正确（`conditions` / `rule-health` 在 `/:badgeID` 之前，`controller.go:103-114`）。

---

## 12. 检查器变异验证（2026-11-12）

检查器没被验证过 = 「全绿」可能只是它不会红。本节记录三次真实变异。

### 12.1 `dto.go` 目录赋值注释掉 → **暴露了检查器本身的洞**

变异：注释 `toPublicCatalog` 里的 `RequirementText: toRequirementText(entry.RequirementText)`。

结果：**`TestPublicCatalogWireKeysAreWhitelisted` 仍然通过**。原因：DTO 字段是
`RequirementText requirementTextDTO \`json:"requirementText"\``——**没有 `omitempty`**，
漏赋值会输出 `{"zh":"","en":""}`，键仍在。于是：

- 只验「键存在」的检查（我最初的 M1.4-22）→ **假绿**；
- 只验「类型是 object」的检查（我最初的 M1.4-14 wire）→ **假绿**。

这与 lead 提醒的「required 写了但 DTO 漏了仍然绿」是**同一类**，但更隐蔽：漏的不是键，是值。
已补两条值级断言（脚本 M1.4-14）：

1. 按 DB 里的规则形状取「单条件」徽章 slug，断言这些 slug 的 `requirementText.zh` / `.en`
   **长度都 > 0**；
2. 断言文案里不出现 `{` / `cmp` / `children` / `params`（防止退化成结构文本）。

### 12.2 合成响应剥字段 → 检查器确实会红

把真实响应里第一条 entry 的 `requirementText` 删掉再喂给 M1.4-22 的比对器：

```
CHECKER=MISSING(1):regular-visitor:requirementText
```

检查器灵敏度成立（不是永远输出 OK）。

### 12.3 `mapBadgeError` 删掉 `cycle` 分支 → 映射测试确实会红

变异：删掉 `case errors.Is(err, badges.ErrBadgeRuleCycle)` 分支。

```
--- FAIL: TestMapBadgeErrorRuleCodes/cycle
    controller_test.go:194: 必须映射成 fiber.Error，得到 *fmt.wrapError
```

恢复后全绿。**这条同时证明测试覆盖的是真实映射分支，不是同义反复。**

### 12.4 变异测试自身的教训

第一次变异时我以为「API 返回 200 但字段消失」就说明重建生效了，实际是 **air 重建窗口返回 000**，
`grep` 不到字段被误读成「字段消失」。判据要写「http==200 且字段数==0」，不能只写「grep 不到」。
这与 §9.2 规则 2 同源：**断言按失败类写，别把「进程不在」当成「行为正确」。**

### 11.4 task-8 复验（2026-11-12 12:4x）

| 项 | 结果 | 证据 |
| --- | --- | --- |
| M1.4-02 纯 rule 徽章 | **ok** | `TestEvaluatePureRuleBadgeWithoutLegacyMetric`：先断言 legacy 两列**读回为 NULL**（防「以为在测纯 rule、其实还走旧列」），再断言达标授予、改严不授予 |
| M1.4-04 三来源 | **ok** | `TestGrantSourcesAreDistinguishable`：自动 `NULL/''`、重算 `NULL/'recompute'`、手动 `操作者 id/理由` |
| M1.4-06 三组合 | **ok** | `TestUpdateBadgeRuleNullCombinations`：单独 `rule:null` → 422；带 `manual` → 200；带 `automatic` → 422 |
| M1.4-08 五个码 | **ok** | `TestMapBadgeErrorRuleCodes`：五个码各喂一次 → 422 + 精确 message；用例包的是真正的 `*ruleengine.Error`（`CodeOf` 走 `errors.As`） |
| M1.4-12 回扫 | **ok** | `TestBackfillRulesWorkerChainsPages` / `...SurfacesFailures` / `TestBackfillRulesArgsDedupeKey`；「不阻塞 HTTP」按**结构**验（HTTP 只调 `EnqueueRuleBackfill`），不测时间 |
| M1.4-14 `requirementText` | **ok（四层）** | 领域层 `TestPublicBadgeRequirementText`；目录键 12 条；单条件 10 条双语**非空**（另 2 条是手动徽章，无规则）；未出现结构文本；资料页 12 条也下发 |
| M1.4-22 契约级 | **ok** | 逐 slug 差集清零 |

### 11.5 独立复跑：迁移基线与可逆性（2026-11-12 12:35）

| 脚本 | 退出码 | 结论 |
| --- | --- | --- |
| `./scripts/verify-migration-reversible.sh`（`SFORUM_MIGRATION_TARGET_VERSION=202611120002`） | **0** | **44 条 ok**，末行「全部通过：202611120002 可 down 回干净状态并重新 up 重建」。含 down 后 11 项对象消失、up 后 18 项重建、`X-04b` 多条件规则下 down 拒绝且无半回滚残留（goose 版本仍 202611120002）、`X-04c` 规则改过但可逆时按当前规则反推（`comment_count` / 5）。**④ 已独立复现，不是转述** |
| `./scripts/verify-badge-rules-baseline.sh` | **8** | **脚本自身编译失败**：`/tmp/.../main.go:63:19: assignment mismatch: 2 variables but store.GrantFromRules returns 3 values` → `[exit 8] 迁移到迁移前版本失败`。①②（M1.1-04/11/12）因此**无法复验** |

**根因**：task-4 把 `GrantFromRules` 的签名从 `(int64, error)` 改成 `(int64, []int64, error)`（`postgres_store.go:419`），
而 `verify-badge-rules-baseline.sh:177` 仍写 `granted, err := store.GrantFromRules(ctx)`。
老后此前报的「迁移前 57 条、旧逻辑 `granted=57 refreshed=29`」是在这次签名变更**之前**跑的。

**修复记录（2026-11-12，由 reviewer 代改，一行，仅修签名）**：

```diff
-		granted, err := store.GrantFromRules(ctx)
+		granted, _, err := store.GrantFromRules(ctx) // queuedIDs 只用于回执；基线脚本只关心授予数
```

**调用点扫描**：脚本里只有两处 store 调用——`GrantFromRules`（已漂移，已修）与 `RecomputeAll`
（仍返回 `(int64, error)`，未漂移）；无 `EvaluateUser` / `GrantAutomatic` 调用。SQL 侧断言不受 Go 签名影响。

**复跑结果：exit 5，仍然失败——而且失败点比签名更深。**

```
== 迁移前基线：旧逻辑的 should_have_but_missing 必须非空
  ok   迁移前缺授予 57 条（夹具有效）
== 迁移前跑旧逻辑 recompute（补授予 + 刷 top_badge）
grant from rules: ERROR: column rules.rule does not exist (SQLSTATE 42703)
[exit 5] 迁移前 recompute 失败。
```

根因：`GrantFromRules` 已被 task-4 重写成**规则驱动**（`postgres_store.go:419` 起的 SQL 读
`rules.rule->'root'...`，并用 `fastPathRulePredicateSQL`），而 `badges.rule` 列在
`202611120001` **还不存在**。也就是说：**脚本里的「旧逻辑」已经不是旧逻辑**——它拿当前代码去跑迁移前
schema，结构上必然失败。

这不是签名问题，需要的是「让基线由**旧语义**产生」：把迁移前那一步改成字面 legacy SQL
（`metric`/`threshold` 谓词 + 按 `topBadgeOrderSQL` 刷新），或 checkout 迁移前代码跑。两者都是设计决定，
属 task-1 写作用域，**reviewer 没有代改**。

### 11.6 基线脚本的旧语义修复与独立复跑（2026-11-12 12:5x）

**改法**（reviewer 代改，复用脚本里已有的 legacy 谓词，不引入第二份旧实现）：

| 段 | 改前 | 改后 |
| --- | --- | --- |
| 迁移前缺口计数 | 内联一段 `missing_sql_old` | 抽出 `legacy_gap_predicate` 变量，计数与补授予**共用同一段** |
| 迁移前补授予 | `helper recompute` → `store.GrantFromRules`（规则驱动，读 `badges.rule`，迁移前不存在） | `INSERT INTO user_badges ... SELECT ... ${legacy_gap_predicate} ON CONFLICT DO NOTHING`，用 `RETURNING` 计数 |
| 迁移前刷 `top_badge_id` | 同上（在 store 里） | `DISTINCT ON (user_id)` + `ORDER BY t.position ASC, badges.position ASC, granted_at DESC`，与 `postgres_store.go` 的 `topBadgeOrderSQL` 逐字一致（JOIN 而非 LEFT JOIN） |
| 迁移后 recompute | `helper recompute` | **不变**——迁移后正是要验当前代码 |

**复跑结果：exit 0，全部通过。**

| 判据 | 实测值 |
| --- | --- |
| 迁移前 `should_have_but_missing` | **57**（34 个夹具用户；非空，`die 4` 防呆通过） |
| 迁移前旧逻辑 recompute | **`granted=57 refreshed=29`**（与老后此前报的数字一致，独立复现） |
| 迁移前补授予后缺口 | **0** |
| 迁移后 recompute | **`{granted:0, refreshed:0}`** |
| 迁移后双向对账 | `should_have_but_missing = 0` / `has_but_should_not = 0` |
| `top_badge_id` 快照差集 | **0 行** |
| `user_activity` 全表 | **逐行一致** |
| 附加 | `user_badges` 授予集合一致；M1.1-04 非内置自动徽章也有规则；回滚快照行数 = 自动徽章数 |

末行输出：「全部通过：迁移前后 recompute 零变更、双向对账 0 行、快照逐行一致。」

> 这一步的价值不止于数字：它证明了**「旧语义」必须由脚本自带**。调用当前 store 的写法在代码重写后
> 会静默失去意义（这次是运行时 `SQLSTATE 42703`，下次可能是「跑通了但算的是新语义」——那更糟）。

**仍未收口的项**：

1. **M1.1-04/11/12**：等上面那一行修好并重跑（脚本逻辑本身没问题，是签名漂移）。
2. **M1.1-09 迁移后 `recompute {0,0}`**：同一脚本覆盖，同一条阻塞。
3. **M1.4-21 HTTP `queued`**：需管理员会话。

（`X-03` 缺的 `forum.badge_fact_write_denied` 属 M3 能力码，不算 M1 缺口；`M1-EXIT-PERF` 与 P-01 已由 §9.4 的 10k 证据覆盖，脚本里仍显示 pend 是因为它们是人工门。）

### 11.7 M1.4-21：映射层检查抓到真缺陷——`queued` 被控制器丢掉

不需要管理员会话也能验的那一层，一验就红：

```
response_keys   = granted,refreshed
openapi_required= granted,refreshed,queued
VERDICT=MISSING:queued
```

`admin_service.go:339` 返回 `RecomputeResult{Granted, Refreshed, Queued}`，
但 `controller.go:699` 的响应映射只写了两个键：

```go
return apphttp.OK(c, map[string]any{"granted": result.Granted, "refreshed": result.Refreshed})
```

而 OpenAPI `BadgeRecomputeResult.required` 明确包含 `queued`。**一行修复**：

**已修复（2026-11-12，reviewer 代改，共三处）**：

| 文件 | 改动 |
| --- | --- |
| `Controllers/Badges/dto.go` | 新增 `recomputeResponse` 结构体（三个 json tag）+ `toRecomputeResponse`；注释写明「键集合必须与 OpenAPI `required` 一致」以及为什么不用内联 map |
| `Controllers/Badges/controller.go` | handler 改为 `apphttp.OK(c, toRecomputeResponse(result))`，不再手写 map 字面量 |
| `Controllers/Badges/controller_test.go` | 新增 `TestRecomputeResponseCarriesAllCounters`：`{Granted:1, Refreshed:2, Queued:3}` → 断言三键都在、值对、**无多余键** |

验收脚本的 M1.4-21 也随之升级：从「grep handler 里的字面量」改成
「单测 + 读 DTO 的 json tag 与 OpenAPI `required` 求差」——前者抓拼错，后者抓漏加。

影响面：M2 的重算 toast 计划用 `queued` 提示「已转异步」，修复前会拿到 `undefined`；
更根本的是**契约与实现不一致**——与 M1.4-22 对 `requirementText` 抓到的是同一类问题，
只是这次发生在响应侧。

> 附注：这条本来被记成「缺管理员会话，无法验证」。改成验映射层后立刻暴露成真缺陷——
> 与 §12.1 同一个教训：**「验不到」不等于「没问题」，换个不需要会话的层去验，问题常常就在那里。**
> 映射已抽成函数，所以现在它从「grep 守」升级成「单测守」。

### 11.8 `queued` 修复的复跑证据（2026-11-12 12:5x）

| 命令 | exit | 结果 |
| --- | --- | --- |
| `./scripts/verify-badge-rules-acceptance.sh`（带 dev 库） | **0** | `ok 74 / FAIL 0 / pend 9`；M1.4-21 的两层转 ok（单测 + DTO 契约） |
| `cd apps/api && go build ./...` | **0** | — |
| `./scripts/test-api-isolated.sh ./app/Http/Controllers/Badges/...` | **0** | `ok .../Badges 0.033s` |
| `ruby scripts/validate-openapi-refs.rb` | **0** | `OpenAPI references OK: checked 3285 refs across 65 files.` |

新增测试：`TestRecomputeResponseCarriesAllCounters`（映射层单测，断言三键 + 值 + 无多余键）。
M1.4-21 现在只剩「完整 HTTP 往返未执行（无管理员会话）」一条 pend，且已按三层写清。
