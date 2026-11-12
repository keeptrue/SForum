# 徽章规则 M2 剩余五项：证据可得性审计（只读）

日期：2026-11-12　作者：小研　状态：只读核实，未改任何生产代码 / locale / 测试 / 计划文件

审计对象（lead 定义的五项）：

1. `requirementText` 接卡片
2. 影响确认弹窗（`GET /admin/badges/:id/rule-impact`）
3. `queued` 重算 toast
4. 多条件 `requirementSummary` 字段级提示
5. 公开面 390×844 视觉 QA

结论摘要：**五项里只有第 1 项有可用证据链，而且其中「资料页」那一半是假绿**（脚本只判键存在、
现网值是空串）。第 2/3/4 项的前端在仓库里**一行都没有**，所以它们的缺证据不是「需要管理员会话」，
而是「没有被测物」；会话只卡住唯一一层——真实 HTTP 往返。

---

## 0. 复跑环境（本机实测）

```bash
cd /home/inkedus/Code/github/SForum
# 主机没有 psql，走容器；dev 库 pub 在 15432，容器名 sforum-postgres
docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum -Atc 'select 1'
# API 已在 8080（.env: HTTP_PORT=8080）
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/api/v1/badges   # → 200
```

下文所有 SQL 都带 `docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum` 前缀，
可直接复制。查库断言脚本需要 `DATABASE_URL`：

```bash
export DATABASE_URL='postgres://sforum:sforum@127.0.0.1:15432/sforum?sslmode=disable'
./scripts/verify-badge-rules-acceptance.sh
```

---

## 1. 五项 × 层：已有断言 / 缺 / pend

分层的意义：**同一项在「纯逻辑 / 组件渲染 / DTO 映射 / store / 真实 HTTP」上可得性完全不同**，
会话只卡最后一层。最后一列就是「验不到 ≠ 没问题」的落点。

| # | 项 | 纯逻辑 / 组件渲染（零会话） | DTO 映射（零会话） | store / 领域（隔离集群） | 真实 HTTP（需管理员会话） |
|---|---|---|---|---|---|
| 1 | requirementText 接卡片 | **有**：`apps/web/tests/forum/badgeRequirementText.test.ts:75`、`apps/web/tests/forum/profileBadgeTab.test.ts:372`、`apps/web/tests/badges/badgeCatalogData.test.ts:137` | **有**：`dto.go:88`（资料页）、`dto.go:175`（目录） | **有**：`TestPublicBadgeRequirementText`（`rule_store_postgres_integration_test.go:434`） | **有但假绿**：脚本 `M1.4-14 … 资料页` 只判 `.requirementText | type != "object"`（`verify-badge-rules-acceptance.sh:583`），空串照样 pass |
| 2 | 影响确认弹窗 | **无被测物**（`rg -ni "ruleimpact|rule-impact|estimate" apps/web/app` 只命中无关模块） | **缺**：impact 直接返回领域结构，没有独立映射函数 | **有**：`TestEstimateRuleImpactIsUpperBound`（`rule_store_postgres_integration_test.go:191`），脚本 `M1.4-10` | **全缺**：`controller_test.go` 无任何 impact 用例；`curl` 未登录 = 401（实测） |
| 3 | queued toast | **无被测物**：`useAdminBadges.ts:279-284` 把响应类型写成 `{granted, refreshed}`，`queued` 被丢弃；`badges.vue:161` 只用 `changed` 一条文案 | — | **有**：`M1.4-18`（`TestGrantFromRulesCoversEveryMetricBranch`）；wire 形状 `TestRecomputeResponseCarriesAllCounters` | **pend（设计如此）**：`verify-badge-rules-acceptance.sh:631` |
| 4 | 多条件 summary 字段提示 | **无被测物**：`rg -ni requirementSummary apps/web` = 0 命中；locale 无对应键（见 §1.2） | **有**：`admin_badge_wire_test.go:75-83` 断言 `requirementSummary` 逐字透出 | **有**：整体替换语义 `controller.go:290-311` | 缺（保存路径无 HTTP 级用例） |
| 5 | 390×844 视觉 QA | **无渲染证据**：无 Playwright/截图测试，`apps/web/tests` 只有 SFC mount 单测 | — | — | 需人工浏览器 |

### 1.1 「需要会话」的精确边界（脚本原文）

| 断言 | 位置 | 为什么需要会话 |
|---|---|---|
| `M1.4-21 完整 HTTP 往返` | `verify-badge-rules-acceptance.sh:631` | 调 `POST /admin/badges/recompute` 要 `super_admin` |
| `M1.1-09 迁移后重算零变更` | `:373` | 同上 |

除这两条，脚本里**没有**任何一条断言因「缺会话」而 pend；其余 pend 全部是「缺夹具 / 缺一次性集群 /
缺 API / 缺 ruby·jq」（`:279/:331/:347/:361/:470/:544/:560/:580/:585`）。
换句话说：**用「没有管理员会话」解释第 2/3/4 项缺证据是不成立的**——缺的是被测代码和被测断言。

### 1.2 第 3、4 项缺证据的机器证据

```bash
# queued 在前端被丢弃：响应类型只有两个字段，注释也停在旧契约
sed -n 275,285p apps/web/app/composables/admin/badges/useAdminBadges.ts
# 后端 wire 三个计数器都在（M1.4-21 已修）
sed -n 476,500p apps/api/app/Http/Controllers/Badges/dto.go
# 现有 toast 文案把「本次影响 N 个用户」钉死成唯一形态：加 queued 必须同时改这条测试
sed -n 229,241p apps/web/tests/admin/adminBadgesI18n.test.ts
# admin.badges 下不存在 impact.* / queued / requirementSummary 文案
node -e "const z=require('./apps/web/i18n/locales/zh-CN.json');const w=(o,p='')=>Object.entries(o).flatMap(([k,v])=>typeof v==='object'?w(v,p+k+'.'):[p+k]);console.log(w(z.admin.badges).filter(k=>/impact|queued|summary/i.test(k)))"
# → 仅 rule.missingHint 一类，无 impact/queued/summary 键
```

### 1.3 零会话可用的三个层（第 2/3/4 项落地时该往这里加断言）

1. **纯函数 / composable**：`badges.vue:154-172` 的 toast 分支应当抽成「计数 → 文案键」的纯函数，
   像 `forumBadgeRequirementText`（`utils/forum/forumBadge.ts:121`）那样单测。`queued` 的三态
   （0 / >0 / 与 granted 混合）不需要任何会话。
2. **组件 mount**：`apps/web/tests/admin/adminBadgeEditDialogRule.test.ts` 已有完整 harness
   （`compileVueSfc` + `requestMock` + `useApiClientStub`，见该文件 `:1-50`）。
   影响弹窗的预览态、`≈` 文案、`estimated === null` 的「无法估算」分支、多条件 summary 字段级提示，
   都能在这里 mount 出来验，**不碰网络**。
3. **DTO 映射**：impact 目前没有独立映射函数（`rule_controller.go:64-82` 直接 `apphttp.OK(c, impact)`）。
   要零会话验 `estimated:null` 的 wire 形状，必须先抽一个 `toRuleImpactResponse`——这正是 M1.4-21
   对 `recomputeResponse` 做过的事（`dto.go:476-500`）。

---

## 2. 影响估算三状态：dev 库能造出什么样本

### 2.1 `method` 枚举 vs 实际可触发的形状（逐条核对）

常量定义：`apps/api/app/Models/Badges/rule_impact.go:32-41`；OpenAPI 枚举：
`contracts/openapi/schemas/badges.yaml:1205`（`required: [method, partial, unsupportedConditions]`，
`estimated` 可空但**不在 required 里**，`estimated` 为 nil 时仍序列化成 `null`）。

| method | 产生点 | 最小合法规则（可保存、可通过 `RuleEngine.Validate`） | 预期 |
|---|---|---|---|
| `single_leaf` | `rule_impact.go:81` | `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}}` | `estimated` = 计数 |
| `strictest_leaf` | `:106` | `all[ topic_count gte 1, content.pinned gte 1 ]` | AND 取最小 |
| `strictest_leaf_partial` + `partial:true` | `:104` | `all[ topic_count gte 1, like_given_count **lte** 9 ]` | 前者计数，后者进 `unsupportedConditions` |
| `sum_of_leaves` | `:122` | `any[ topic_count gte 1, comment_count gte 1 ]` | 相加（上界） |
| `complement_of_leaf` | `:135` | `not[ activity.count days_visited gte 30 ]` | 活跃总人数 − 该叶 |
| `unsupported_condition` + `estimated:null` | `:79/:101/:118/:125/:137` | 单叶 `activity.count … lte 9`；或 `any[…]` 里混一个 `lte`；或 `all` 全不可估 | null（不是 0） |

**没有「文档有、代码永不产生」的 method**：6 个常量每个都在 `estimateNode` 里有可达分支，
且 6 个都是合法规则形状（`Validate` 不限制根节点类型，`not` 可作为根，见
`apps/api/app/Support/RuleEngine/validate.go:79-150`；`activity.count` 允许 `gte`/`lte`，
见 `conditions/registry.go:196-200`；`countLeafMatches` 只支持 `gte`，见 `rule_impact.go:160-170`）。

反向的坑（不在本刀修，但写清）：`unsupportedConditions` 只回 `node.Type`
（`rule_impact.go:79`），所以「`activity.count` 用了 `lte`」和「`activity.count` 的 metric 不认识」
在响应里长得一模一样；弹窗文案不能把 `unsupportedConditions` 直接拼给运营看。

### 2.2 dev 库现状（实测，2026-11-12 12:5x）

```bash
docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum -Atc \
"select count(*) filter (where enabled), count(*), count(*) filter (where rule is not null),
        count(*) filter (where requirement_summary_zh <> '' or requirement_summary_en <> '')
 from badges;"
# → 12|12|10|0
```

- 10 枚自动徽章全是**单条件** `activity.count + window.all + gte` → 现成可用的 `single_leaf` 样本；
- 2 枚手工徽章（`community-contribution` / `quality-answer`）`rule IS NULL`；
- 库内 `requirement_summary_zh/en` **全空**，所以公开面文案全部来自服务端生成；
- 现网**没有任何多条件徽章**，也没有 `not`/`any` 样本 → `partial` / `null` / `sum` / `complement`
  四种状态在 dev 库上**目前不可观测**。

### 2.3 造样本（SQL，只写不跑；可复跑、可回滚）

```bash
# 一次性样例：三枚停用徽章，slug 前缀固定，重复执行安全
docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO badges (slug, name, description, icon, tier, grant_mode, enabled, position, rule)
VALUES
  ('m2-evidence-value',   '样本·有值', '', 'i-lucide-flask-conical', 'bronze', 'automatic', false, 900,
   '{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}}'),
  ('m2-evidence-partial', '样本·部分', '', 'i-lucide-flask-conical', 'bronze', 'automatic', false, 901,
   '{"v":1,"root":{"op":"all","children":[
      {"v":1,"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}},
      {"v":1,"type":"activity.count","params":{"metric":"like_given_count","window":{"kind":"all"}},"cmp":{"op":"lte","value":9}}]}}'),
  ('m2-evidence-null',    '样本·不可估', '', 'i-lucide-flask-conical', 'bronze', 'automatic', false, 902,
   '{"v":1,"root":{"type":"activity.count","params":{"metric":"like_given_count","window":{"kind":"all"}},"cmp":{"op":"lte","value":9}}}')
ON CONFLICT (slug) DO NOTHING;
SQL
# 回收：delete from badges where slug like 'm2-evidence-%';
```

列集已按 dev 库 `information_schema.columns` 核对（无默认值的 NOT NULL 列只有
`slug`/`name`/`tier`/`grant_mode`；`description`/`icon`/`enabled`/`position` 均有默认值）。
本轮**只写不跑**，故 dev 库未落任何样本行；要跑就直接执行上面的 heredoc，回收用注释里的 delete。

### 2.4 读这三个状态（curl，需要管理员会话）

实测未登录：

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/api/v1/admin/badges/1/rule-impact
# → 401
curl -s http://127.0.0.1:8080/api/v1/admin/badges/1/rule-impact
# → {"code":401,"message":"请先登录。","data":{"reason":"auth.required"}}
```

拿会话（登录体字段 `email`/`password`，见 `Identity/auth_handlers.go:33-34`）：

```bash
# 需要一个 super_admin 账号（dev 库存在 zhuchunshu999@gmail.com 等，密码只有运营知道）
curl -s -c /tmp/sf.jar -H 'content-type: application/json' \
  -d '{"email":"ADMIN_EMAIL","password":"ADMIN_PASSWORD"}' \
  http://127.0.0.1:8080/api/v1/auth/login | head -c 200
# 取三枚样本的 id 后逐个读
for slug in m2-evidence-value m2-evidence-partial m2-evidence-null; do
  id=$(docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum -Atc \
       "select id from badges where slug='$slug'")
  echo "== $slug ($id)"
  curl -s -b /tmp/sf.jar "http://127.0.0.1:8080/api/v1/admin/badges/$id/rule-impact"; echo
done
```

预期（与 §2.1 表逐行对齐）：

```
== m2-evidence-value (…)
{"code":0,"data":{"estimated":<count topics>=1 的人数>,"method":"single_leaf","partial":false,"unsupportedConditions":[]}}
== m2-evidence-partial (…)
{"code":0,"data":{"estimated":<count topics>=1 的人数>,"method":"strictest_leaf_partial","partial":true,"unsupportedConditions":["activity.count"]}}
== m2-evidence-null (…)
{"code":0,"data":{"estimated":null,"method":"unsupported_condition","partial":false,"unsupportedConditions":["activity.count"]}}
```

**零会话等价路径**（不想造数据时）：直接对同一条 SQL 跑计数，验证估算口径本身——

```bash
docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum -Atc \
"select (select count(*) from users where status='active') as total,
        (select count(*) from user_activity where topic_count >= 1) as topics_ge_1;"
```

`rule_impact.go:163`（累计）与 `:225-249`（窗口）的口径都可这样逐条对表；`partial`/`null`
的**分支存在性**则由 `TestEstimateRuleImpactIsUpperBound`（`rule_store_postgres_integration_test.go:191`）
在隔离集群上覆盖，同样不需要会话。

---

## 3. `requirementText`：公开面两处是否真有值

### 3.1 目录页：**有值级内容**（现场实测）

```bash
curl -s http://127.0.0.1:8080/api/v1/badges \
 | jq -r '.data.groups[].badges[] | [.slug, .requirementText.zh, .requirementText.en] | @tsv'
```

```
regular-visitor	访问天数 ≥ 7	days visited ≥ 7
resident	访问天数 ≥ 30	days visited ≥ 30
veteran	访问天数 ≥ 180	days visited ≥ 180
first-topic	发布主题 ≥ 1	topics published ≥ 1
topic-author	发布主题 ≥ 10	topics published ≥ 10
discussant	发表评论 ≥ 25	comments posted ≥ 25
rising-star	收到赞 ≥ 10	likes received ≥ 10
well-received	收到赞 ≥ 100	likes received ≥ 100
widely-acclaimed	收到赞 ≥ 500	likes received ≥ 500
collector	收藏内容 ≥ 10	bookmarks ≥ 10
community-contribution		
quality-answer		
```

10 条自动徽章双语非空且**不含 AST 词**；最后 2 条是手工徽章（`rule IS NULL`），空串是正确的
（`BuildRequirementText` 先取运营文本，规则不成立就返回空，`requirement_text.go:36-44`）。

### 3.2 资料页：**空串 —— 这是本刀的真实缺陷**

```bash
U=$(docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum -Atc \
    "select u.username from user_badges ub join users u on u.id=ub.user_id group by u.username order by count(*) desc limit 1")
curl -s "http://127.0.0.1:8080/api/v1/profiles/$U/badges" | jq '.data.groups[0].badges[0]'
```

实测输出：

```json
{
  "slug": "regular-visitor",
  "name": "常客",
  "tier": "bronze",
  "earned": false,
  "requirementText": { "zh": "", "en": "" },
  "current": 1,
  "threshold": 7
}
```

同一枚徽章在 `/api/v1/badges` 里是 `访问天数 ≥ 7`。根因两处，都在读路径上：

1. `apps/api/app/Models/Badges/postgres_store.go:176-221` 的 `ListUserBadges` **没有 select**
   `badges.rule` / `requirement_summary_zh` / `requirement_summary_en`（对比
   `postgres_store_public_catalog.go:32` 有）；
2. 该函数直接 `Scan` 进 `PublicBadge` 字段，**没有走 `Badge.Public()`**
   （文案的唯一计算点是 `types.go:136` → `BuildRequirementText`），所以 `RequirementText` 恒为零值；
   `dto.go:88` 把零值原样下发。

后果：`SFProfileBadgeTab.vue:120` 两个 variant 都传 `:description="forumBadgeRequirementText(badge, locale)"`，
资料页因此**整行说明不渲染**——第 1 项的「接卡片」只完成了目录页那一半。

**为什么脚本没抓到**：`verify-badge-rules-acceptance.sh:583` 的资料页断言是
`select((.requirementText | type) != "object")`，键在就 pass；值级检查只写在目录页
（`:560-573`，`empty_vals`）。这正是「验不到 ≠ 没问题」的落点：脚本绿 ≠ 值对。

### 3.3 多条件徽章**应当**是空串（设计而非缺陷）

`requirement_text.go:41-47`：只有「单 `activity.count` 叶子 + `window.all` + `gte` + 正阈值」才生成句子
（`fastPathRuleForText`，`:62-78`）；其余一律留空。固定它的测试：
`requirement_text_test.go:43-45`（多条件 → `zh`/`en` 都空）、
`rule_store_postgres_integration_test.go:450-456`（库内同断言）。计划原文：`knowledge/plans/2026-11-12-badge-rule-engine.md:357`
（「多条件规则不生成——宁可空，也不把内部结构拼成读不懂的话」）。
所以**不能**把「多条件徽章文案为空」当缺陷报；`M2.3`（第 4 项）要补的正是「保存时提醒运营自己填」。

### 3.4 零会话值级核验（推荐写进脚本的形态）

```bash
# 目录：单条件徽章双语非空（脚本已有）
# 资料页：把同一 slug 在两条接口上的文案做逐条比对，空串即 FAIL —— 不依赖会话
docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum -Atc \
"select slug, requirement_summary_zh, requirement_summary_en,
        (rule->'root'->>'type') as leaf_type,
        (rule->'root'->'params'->>'metric') as metric,
        (rule->'root'->'cmp'->>'op') as cmp_op,
        (rule->'root'->'cmp'->>'value') as cmp_value
 from badges where rule is not null order by id;"
# 期望：leaf_type=activity.count 且 cmp_op=gte 且 metric ∈ 六指标 → 公开面两处都必须非空
```

上面这条 SQL 就是「不依赖会话」的判据来源：`leaf_type/cmp_op/metric` 三列决定**应不应当**有文案，
`requirement_summary_*` 两列决定有没有运营覆盖；两者一握手，资料页空串立刻是 FAIL。

---

## 4. 390×844 横向溢出的结构性风险点（只列可机器判定的）

| # | 位置 | 结构事实 | 390 下判定 | 本刀是否必动 |
|---|---|---|---|---|
| 1 | `SFProfileBadgeTab.vue:222-244` | `.sf-badge-grid` 默认 `minmax(0,1fr)`，768/1024/1280 才加列 | **安全**：390 单列，`minmax(0,…)` 已防撑破 | 否 |
| 2 | `SFBadgeCard.vue:212-219` | `__name` `white-space:nowrap` + `text-overflow:ellipsis`，父 `__body` 有 `min-width:0`（`:205-211`，移动端 `:154-161` 再给 `flex:1 1 auto`） | **安全**：省略号可控 | 否 |
| 3 | `SFBadgeCard.vue:236-243` | `__meta` `white-space:nowrap` + `overflow:hidden`；子 `__date` 有 ellipsis，`__tier` 无 `min-width` | **被裁切但不外溢**（父 overflow:hidden） | 否 |
| 4 | `SFBadgeCard.vue:224-234` | `__description` `line-clamp:2` + `overflow-wrap:anywhere` | **安全**：长英文/URL 可断行 | 否 |
| 5 | `SFBadgeCard.vue:257-273` | `__track` `flex:1 1 auto`、`__progress-text` `flex:0 0 auto` | **安全** | 否 |
| 6 | `SFProfileBadgeTab.vue:188-212` | `__title` flex，`__name` 有 `min-width:0`+ellipsis，`__count` `flex:0 0 auto` | **安全** | 否 |
| 7 | `pages/admin/badges.vue:189-197` | 四个统计 `<span class="whitespace-nowrap">` 在 `flex min-w-0 flex-wrap` 内 | **安全**：nowrap 元素可换行，容器有 `min-w-0` | 否 |
| 8 | `SFAdminBadgesBadgesTab.vue:292` | 筛选行 `flex flex-wrap items-center gap-2`；固定宽 `w-56/w-40/w-36/w-36/w-32`（`:298/:305/:312/:329/:336`） | **安全**：5 个筛选键 + 清除 + 新建 = **7 个控件**（lead 说的「七个筛选键」实为 7 个控件、5 个筛选键：`q/group/tier/grantMode/enabled`，见 `utils/admin/adminBadges.ts:170-178`）；最宽 224px < 358px 可用宽，flex-wrap 兜住 | 否（但要 QA 确认换行后 `ml-auto` 的「新建」不孤行） |
| 9 | `SFAdminBadgeTable.vue:58/69/74/79/85/95` | 列宽 `min-w-[180px]`（name，始终显示）+ `w-12`（icon）+ `w-[88px]`（tier）+ `w-[96px]`（actions）；**表格自身无滚动容器**（`Table.vue` 内无 `overflow`），页面滚动区 `layouts/admin.vue:722` 只有 `overflow-y-auto`，**没有 `overflow-x-hidden`** | **风险点**：180+48+88+96+padding ≈ 412px+ > 358px，`min-w-*` 会推开 `<table>`，页面级横向滚动 | **是**：给表格套 `overflow-x-auto`（或 390 档把 name 的 `min-w-[180px]` 收掉） |
| 10 | `SFAdminBadgesBadgesTab.vue:360-372` 等 | `UButton class="ml-auto"`（新建）与 `filtersActive` 的「清除」在 wrap 后各占一行 | 结构性可接受，但需要看 390 下是否出现「清除/新建」两行按钮 | 否（QA 目视） |
| 11 | 规则编辑器 `rule/SFAdminBadgeRuleLeaf.vue:284`（`white-space:nowrap`）+ `:289-290`（`width:100%;min-width:0`）；`SFAdminBadgeRuleGroup.vue:345` 已有 `@media (max-width:480px)` 分支 | 叶子/组的窄屏降级已写；控件统一 `width:100%` + `min-width:0` | **大概率安全**，但影响确认弹窗（第 2 项）落进 `sm:max-w-xl` 的 `UModal`（`SFAdminBadgeEditDialog.vue:286`）后要重新验 | 落地第 2 项时一并验 |

`white-space: nowrap` 全量清单（徽章相关，已逐条判定）：

```bash
rg -n "white-space: nowrap|whitespace-nowrap" \
  apps/web/app/components/forum/badges apps/web/app/components/profile/SFProfileBadgeTab.vue \
  apps/web/app/components/admin/badges apps/web/app/pages/admin/badges.vue
# SFBadgeCard.vue:219 (name)  219/243
# SFProfileBadgeTab.vue:205-211 (__name)
# SFAdminBadgesTiersTab.vue:165 (等级行尾部说明)
# SFAdminBadgeRuleLeaf.vue:284 (条件标签)
# pages/admin/badges.vue:191/193/195/197 (统计)
```

结论：**唯一必须动的结构风险是 #9（后台徽章表格没有横向滚动容器）**；公开面 390 档本身是安全的
（单列 + `minmax(0,…)` + 两处 ellipsis 已到位）。所以第 5 项 QA 的价值不在「找溢出」，
而在确认 **#9 修完后的 390 表现** 与 8/10/11 的换行效果。

---

## 5. 给下一棒的交接清单

1. **先修 §3.2**（资料页 `requirementText` 恒空）：`ListUserBadges` 补 `rule` + 两个 summary 列，
   并让 `UserBadge` 组装时调用 `BuildRequirementText`（或复刻目录页的 `Public()` 路径）。
   修完把脚本 `:583` 的资料页断言从「判键」升级成「判值」，与目录页 `:560` 同形。
2. **第 2/3/4 项的零会话断言先写，别等会话**：`requestMock` harness（
   `tests/admin/adminBadgeEditDialogRule.test.ts:1-50`）已具备；impact 需要先抽
   `toRuleImpactResponse` 才能零会话验 `estimated:null`。
3. **第 3 项的既有测试会挡你**：`adminBadgesI18n.test.ts:229-241` 断言源码里存在
   `t('admin.badges.toast.recomputeChanged', { count: changed })`，改文案必须同步改这条。
4. **第 2 项的状态样本可复跑**：§2.3 的 SQL + §2.4 的 curl；无会话时用 §2.4 末尾的计数 SQL 对口径。
5. **第 5 项只盯 #9**，其余公开面结构已安全。
