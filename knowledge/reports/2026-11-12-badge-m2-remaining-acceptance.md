# 徽章 M2 剩余项 · 独立复核 + 假绿修复

- 日期：2026-11-12 13:0x
- 复核人：reviewer（小审）
- 范围：A 验收脚本假绿修复；B「`requirementText` 接卡片 + 资料页修复」复验；C「`queued` toast」复验；D 风险排序
- 边界：只读生产代码（Go/TS/Vue/locale 一行未改），只动 `scripts/verify-badge-rules-acceptance.sh` 与本文
- 环境：dev API `http://127.0.0.1:8080/api/v1`（可达）；dev 库经 docker `postgres:17-alpine` 只读查询；`zhuchunshu` 为取样用户

## 结论摘要

| 项 | 结论 | 关键证据 |
| --- | --- | --- |
| A 脚本假绿 | **已修，且已用变异证明会红** | 旧判据 `:583` 恒真；新判据 `:576-673`；真实数据 ok=3/pend=1，两个变异各红一条 |
| B1 两墙值级文案 | 有 | 目录/资料页各 12 条，10 条单条件双语非空，2 条 manual 无规则两语言空 |
| B2 多条件两语言空 + 整行不渲染 | **组件级有证据，运行时无证据**（库里 0 枚多条件徽章） | `SFBadgeCard.vue:101`、`profileBadgeTab.test.ts:399` |
| B3 无 AST 词泄漏（词边界） | 有（真实载荷） | 真实 catalog/profile 文案词边界扫描 0 命中，样本含 `topics published` |
| B4 两行 clamp 不横向溢出 | 静态有，**渲染证据缺** | `SFBadgeCard.vue:224-236`（父容器 `min-width: 0` 在 `:209`）；无浏览器 QA |
| C queued 三数原样 + 文案 | 有 | `useAdminBadges.ts:295-303`、`adminBadges.ts:270-286` |
| C i18n 双向断言 | 断言在，**当前红** | `adminBadgesI18n.test.ts:144-167` 因 4 个未登记动态前缀失败 |
| D 风险 | 见 §D，第一条是产品级缺口 | 多条件徽章的文案在前端后台无处可填 |

---

## A. 修假绿：资料页 `requirementText` 判据

### A-1 旧判据（为什么恒绿）

`scripts/verify-badge-rules-acceptance.sh:583`（旧）：

```bash
prof_missing="$(curl -s --max-time 5 "$API_URL/profiles/${prof_user}/badges" \
  | jq '[.data.groups[].badges[] | select((.requirementText | type) != "object")] | length')"
```

`requirementTextDTO`（`apps/api/app/Http/Controllers/Badges/dto.go:145-149`）没有 `omitempty`，
漏赋值输出的是 `{"zh":"","en":""}`；`type` 永远是 `object`，选择器恒空 → `prof_missing` 恒 0 → 恒 `pass`。
它连「键在」都只算间接证据，值一律不验。

### A-2 新判据（值级 + 双向 + 按规则形状取样）

`scripts/verify-badge-rules-acceptance.sh:576-673`，三层：

1. **样本按形状从库里取**，不写死 slug：
   - 单条件（与服务端生成判据同源，`requirement_text.go:62-77` 的 `fastPathRuleForText`）：
     `root.type = activity.count` 且 `cmp.op = gte` 且 `value > 0` 且 `window.kind = all` 且无 `window.days`
     且 `params.metric ∈ {topic_count, comment_count, like_received_count, like_given_count, bookmark_count, days_visited}`。
   - 多条件：`jsonb_array_length(root.children) >= 2`，且运营没手填 `requirement_summary_*`
     （手填的多条件徽章有文案是**合法**的，不排除就会变成假 FAIL）。
2. **值级断言（双向）**：
   - 单条件徽章：`(.requirementText.zh | length) > 0` **且** `(.requirementText.en | length) > 0`；
   - 多条件徽章：两种语言长度都为 0；
   - 另加覆盖断言：库里样本数 > 资料页响应里的样本数 → FAIL（两墙不同源）。
3. **形状样本为 0 时记 pend**，不记 pass（没有样本的「满足」是空真）。
4. 附带两条：资料页文案的词边界 AST 扫描（`\b(cmp|params|children|op|rule|type)\b`，`topics published` 不会误判）；
   以及静态护栏「`conditions.Metrics()` 的每个指标都在 `metricLabels` 里有标签」——
   否则上面那张指标白名单会把新指标悄悄排除，覆盖无声丢失。

### A-3 三态取证（同一段代码，三份输入）

harness 用真实 DB + 真实 API；`FIXTURE` / `FAKE_SQL` 只替换资料页响应体和样本 SQL，不动被测逻辑：

```
### RUN A: 真实数据
  ok    M1.4-14 资料页单条件徽章两种语言都有值（zhuchunshu，10 枚）
  pend  M1.4-14 资料页多条件值为空 — 库里没有多条件规则徽章；该分支无法取证（空真不算通过）
  ok    M1.4-14 资料页文案无规则内部词汇（词边界 cmp/params/children/op/rule/type）
  ok    M1.4-14 指标文案标签齐全（6/6 个指标）

### RUN B: 把 regular-visitor 的 requirementText 改成 {"zh":"","en":""}
  FAIL  M1.4-14 资料页 requirementText 值 — 单条件徽章有整语言空串：regular-visitor（键在、值没填）

### RUN C: 给「多条件」样本塞一句生成文案
  FAIL  M1.4-14 资料页多条件值为空 — 多条件徽章不应有服务端文案，却有值：fake-multi
```

判据自证：**删掉实现（值空了）→ 红；反向（多条件有值）→ 红**。
`bash -n scripts/verify-badge-rules-acceptance.sh` 通过（SYNTAX_OK）。
未跑整脚本（它会拉起一次性 PG 集群跑完 M1.1–M1.4 全量），本次只做定点三态验证。

---

## B. 复验「`requirementText` 接卡片 + 资料页修复」

### B-1 两份公开响应在**值级**都有文案 —— 通过

`GET /api/v1/badges` 与 `GET /api/v1/profiles/zhuchunshu/badges`，逐条长度：

```
regular-visitor 8/16   resident 9/17   veteran 10/18   first-topic 8/20
topic-author 9/21      discussant 9/20 rising-star 8/19 well-received 9/20
widely-acclaimed 9/20  collector 9/14
community-contribution 0/0   quality-answer 0/0   ← 两枚 manual、rule=NULL、运营未填（合法空）
```

两份响应的 10 枚单条件徽章值完全一致；链路上四处都对：
`types.go:112-131 Badge.Public()` 填值 → `dto.go:31`（目录）/`dto.go:142`（资料页）两处 DTO 都带字段 →
`dto.go:151-153 toRequirementText` 原样搬运 → `forumBadge.ts:122-131` 只选语言不重算。
领域层回归 `TestPublicBadgeRequirementText`：`ok github.com/keeptrue/sforum/apps/api/app/Models/Badges 0.393s`。

### B-2 多条件徽章两语言空 + 前端整行不渲染 —— **只有组件级证据**

- 服务端：`requirement_text.go:41-44`，`fastPathRuleForText` 不认组节点 → 直接返回运营文本（空则空）。
- 前端：`SFBadgeCard.vue:101` `<span v-if="props.description" …>`；宿主线 `SFProfileBadgeTab.vue:120`
  `:description="forumBadgeRequirementText(badge, locale)"`；空串 → 整行不渲染（不占位）。
- 测试：`profileBadgeTab.test.ts:394-400`（两语言空 / 键缺失都 `undefined`）、`badgeRequirementText.test.ts:87-89`。
- **缺口**：dev 库里 `jsonb_array_length(root->'children') >= 2` 的徽章为 **0**（12 枚：10 单条件 + 2 无规则 manual），
  所以这条契约至今只在合成夹具上成立过；验收脚本对应分支现在是 pend，不是 pass。

### B-3 公开面文本无 `cmp`/`params`/`children`/`op`/`rule`（词边界） —— 通过

对两份真实响应做 `test("\\b(cmp|params|children|op|rule|type)\\b")`，命中 0 段。
样本里含 `topics published ≥ 1`，正好是「子串匹配会把 `topics` 判成 `op`」的那类词，词边界口径被真正执行到。
另外 `M1.4-13` 的**键集合**检查（`.. | objects | keys[]`）覆盖 AST 字段名不进 JSON，两者互补。

### B-4 两行 clamp 不造成横向溢出 —— 静态通过，渲染未验

`SFBadgeCard.vue:224-236`：`display:-webkit-box` + `-webkit-line-clamp:2` + `white-space:normal` +
`overflow-wrap:anywhere`，且**不在**该块里写 `text-overflow`；父容器 `.sf-profile-badge-card__body { min-width: 0 }`
（`:205-210`，窄屏分支 `:160-163`）给了收缩基准，移动端 `flex-direction: row` 时也不会把卡片撑宽。名字行 `:219` 仍是 `nowrap`（这是设计，不是回归）。
静态契约由 `badgeRequirementText.test.ts:118-140` 钉住。
**未覆盖**：没有浏览器渲染证据（桌面 + 390×844 的横向溢出实测），长英文句子（>2 行）实际裁切效果只在源码层面对。

### B 的「假绿七形态」逐条（`knowledge/modules/badges.md:198-212`）

| 形态 | 本项是否命中 | 说明 / 补法 |
| --- | --- | --- |
| 写死字面量 | 部分已避免 | 验收脚本按 DB 规则形状取样；前端测试里的 `BILINGUAL` 仍是字面量，但它测的是「选语言/排版」，不是服务端生成逻辑，可接受 |
| 写死实现细节 | 已覆盖 | 断言认「生效形状（叶子/组）」而不是某个具体 AST 字面量 |
| 只验单点不验多面 | 已覆盖 | 目录 + 资料页两条链路都验；单/多条件双向都验 |
| 验证对象为空 | **单条件已解决，多条件仍是空真** | 多条件分支现在 pend；补法 = 在测试库里造一枚 `op=all, children=2` 的徽章后再跑（依赖：需要一个可写夹具库，dev 库只读） |
| 断言集覆盖不到横切面 | 部分 | 已补：指标标签齐全度、资料页样本覆盖数、AST 词边界；未覆盖：非 zh 前缀 locale 的服务端文本落地（只有前端回落测试） |
| 测试借了环境 | 命中 | 资料页断言依赖「dev 库里有单条件徽章 + 有用户」；库空时是 pend（已按脚本口径处理，不算绿） |
| 只验「该拒的拒了」 | 已覆盖 | 多条件必须**空**（反向验），不是只验单条件有值 |

---

## C. 复验 `queued` toast

### C-1 三个数原样返回，没有相加 —— 通过

`apps/web/app/composables/admin/badges/useAdminBadges.ts:295-303`：`POST /admin/badges/recompute` 之后
逐字段 `Number(result?.x ?? 0)` 返回 `{granted, refreshed, queued}`，没有任何 `+`。
后端语义对得上：`admin_service.go:341` `queued := int64(len(complexBadgeIDs))`（**徽章数**，不是用户数），
`:356` 原样返回。前端断言 `adminBadgesRecomputeResult.test.ts:40-69` 覆盖「不相加（7/13 不得出现）」「queued-only」「缺字段归 0」。

### C-2 `queued > 0` 时文案确实说「N 枚徽章已转入后台评估」 —— 通过

`adminBadges.ts:270-286` 是唯一决策点；`zh-CN.json:901` = `{count} 枚徽章已转入后台评估，稍后自动完成。`，
`en-US.json:861` 对应英文。文案值与徽章数语义一致（`queued` 就是徽章数）。
全 0 → 返回 `undefined`（`:286`），页面只在有值时才带 `description`，不会编假成功。

### C-3 i18n 双向断言还在，但**当前是红的** —— 真问题

`adminBadgesI18n.test.ts:144-167`：`missing`（用到但没定义）与 `extra`（定义了但没人用）两侧都断言，
失败信息同时给出两侧。新键 `admin.badges.toast.recomputeQueued` 被 `adminBadges.ts:278` 真实引用，
`referencedBadgeKeys()` 扫 `app/**/*.{vue,ts}` 的 `admin.badges.*` 字面量能扫到它。

但整条测试现在是 **FAIL**（`bun test tests/admin/adminBadgesI18n.test.ts`，9 pass / 1 fail）：

```
error: expect(received).toBeDefined()
Received: undefined
  at adminBadgesI18n.test.ts:149   // DYNAMIC_ENUMERATIONS[prefix]
```

未登记的动态前缀（我用同款扫描逻辑复现）：

```
admin.badges.rule.errors            ← SFAdminBadgeRuleSection.vue:53（模板字符串 `…errors.engine.${ref}` / 回退 `…errors.${ref}`）
admin.badges.rule.errors.engine     ← 同上（真实动态键）
admin.badges.rule.health            ← SFAdminBadgeRuleSection.vue:61（`…health.${issue.kind}`）
admin.badges.rule.impact.method     ← SFAdminBadgeRuleImpactDialog.vue:11 —— **这行是注释**
```

即：`rule/` 这批在做的 UI 引入了 3 个真实动态键族 + 1 个「注释里写了模板」的扫描假阳性。
它不是 queued 那刀引入的，但**现在这条双向门禁是红的**，`./scripts/test.sh` 的 web 线会跟着红。
补法：在 `DYNAMIC_ENUMERATIONS` 里登记 `rule.health`（`IssueKind` 枚举）、`rule.errors`、`rule.errors.engine`
（引擎错误码枚举），并把扫描器改成**先剥注释**再收引用——否则注释既能让 `missing` 假红，也能让 `extra` 假绿（死键被注释「引用」）。

### C-4 变异矩阵：`queued > 0` 分支失效时哪几条断言会红

对 `adminBadges.ts` 的三个变异体（`/tmp` 副本，未动生产文件），跑 `adminBadgesI18n.test.ts` 里 4-3 段的**逐字**断言：

| 断言（4-3 段原文） | M1 丢掉 queued 句子 | M2 queued>0 直接 return undefined | M3 queued 字段被归零 |
| --- | --- | --- | --- |
| A `queuedOnly === '4 枚徽章…'` | RED | RED | RED |
| B `affectedOnly === '本次影响 7 个用户。'` | GREEN | GREEN | GREEN |
| C `both contains '9 枚徽章…'` | RED | RED | RED |
| D `both contains '本次影响 3 个用户。'` | GREEN | **RED** | GREEN |
| E `both === 两句按序拼接` | RED | RED | RED |
| F 全 0 → `undefined` | GREEN | GREEN | GREEN |
| G `bothEn contains '9 badge(s) queued…'` | RED | RED | RED |
| H `bothEn contains '3 user(s) affected.'` | GREEN | **RED** | GREEN |

**不是一条断言红，是 4 条（M2 下 6 条）**——但它们**在同一个 `test()` 里**（`adminBadgesI18n.test.ts:251-279`），
所以在测试报告上只会看到**一条红**：`queued, affected, both and neither map to distinct sentences`。
这不算假绿（红就是红），但定位粒度差：无法从报告看出是「queued 丢了」还是「affected 丢了」。

还缺的断言类型（按价值排序）：

1. **store → formatter 的接缝没有断言**。页面级只有 `.toContain('recomputeToastDescription(result, t)')`（`:248`），
   这是**字符串出现即通过**：把 `description: recomputeToastDescription(result, t)` 改成
   `const _d = recomputeToastDescription(result, t)` 而 `description` 不传，源文件里这句话仍在 → 测试照绿，toast 实际没有副标题。
   补法：断言 `source` 含 `description: recomputeToastDescription(result, t)`，或直接挂载页面断言 `toast.add` 收到的 `description`。
2. **没有独立的「queued > 0 ⇒ 副标题非空」不变量**。现在靠精确文案，将来改文案要同时改 3 处预期；
   补一条 `expect(recomputeToastDescription({queued:1}, t)).toBeTruthy()` 能把「语义」和「措辞」分开。
3. **英文侧缺 `neither` 与 `queuedOnly`**：`tEn` 只测了 both，en 的「全 0 不出副标题」「只有 queued」没有对照。
4. **场景粒度**：A–H 拆成 4 个 `test()`（queued-only / affected-only / both / neither），报告才能直接指到坏的那一支。

---

## D. 风险排序：最可能在真实环境出错、而测试拦不住的三件事

### D-1（高）多条件徽章的「获得条件」文案，运营在前端**无处可填** → 组合徽章在公开面必然没有说明

- 服务端刻意不为多条件规则生成句子（`requirement_text.go:41-44`），设计上是「宁可没有说明，也不把 AST 拼成句子」。
- 兜底本该是运营手填，但前端没有这个字段：
  - 后台写接口**支持** `requirementSummary`（`controller.go:290-299` 的 `badgeBody.RequirementSummary`，整体替换语义）；
  - 后台读接口也下发了它（`dto.go:218, 246`）；
  - 但 `useAdminBadges.ts:16-31` 的 `AdminBadgeInput` 没有这个字段，
    全仓 `apps/web/app` 里 `requirementSummary` 出现 **0 次**（`rg requirementSummary apps/web/app` 无命中），编辑弹窗也没有任何 `requirement` 输入。
- 后果：一旦运营建了一枚 `op=all` 的组合徽章（恰好是最需要解释的那种），公开卡片就永远是「名字 + 等级 + 未获得」，用户不知道怎么做才能拿到。
- 为什么测试拦不住：服务端测试只验「多条件 → 空」（符合设计），前端测试只验「空 → 不渲染」（也符合设计），**没有任何一条断言问「运营能不能填」**。
- 补法：编辑弹窗加 zh/en 两行「获得条件（可选，留空则按规则自动生成）」；若多条件 + 两语言都空，表单给一条非阻断提示。

### D-2（中）资料页响应是 `public, max-age=30`，但公开文案的「新鲜度」与目录不一致，且没人定义过这件事

实测响应头（匿名，`curl -D -`）：

```
GET /api/v1/profiles/zhuchunshu/badges
Cache-Control: public, max-age=30
Vary: Cookie, Accept-Encoding
Set-Cookie: csrf_=…; path=/; SameSite=Lax

GET /api/v1/badges
Cache-Control: private, no-store
```

- 同一个 `requirementText`，在目录页立刻更新，在资料页最长滞后 30s。
- 本地知识库还写着「资料页 ≤10s」（`knowledge/modules/badges.md:233` 第 18 条，同类表述还有 `:142`），与 `controller.go:794-796` 的 30 不一致——文档已经漂了。
- `public` + `Vary: Cookie` 组合也不健康：登录用户各带唯一 Cookie，共享缓存按 Cookie 建键 → 命中率≈0 且键无限增长；
  匿名侧才有真正的 30s 共享。测试只断言头字符串（`controller_test.go:158`），没有人验过 CDN 行为。
- 为什么测试拦不住：这是缓存语义问题，单测只钉字面量。补法：把「资料页徽章墙新鲜度上限」写进 module note 并与代码常量同源；或直接降到与其他公开配置面一致的策略。

### D-3（中）`requirementText` 对「规则形状不在白名单」是**静默降级**，后台没有任何提示

`fastPathRuleForText`（`requirement_text.go:62-77`）只认 `activity.count + gte + window.all + 已知指标`：

- `window.kind = last_days`（带 `days`）→ 不生成；
- `cmp.op = lt/gt/lte/eq` → 不生成；
- 指标不在 `metricLabels`（`requirement_text.go:22-29` 六个）→ 不生成；
- 组节点（多条件）→ 不生成。

每条都只是「公开面少一行字」，**保存、授予、后台列表全都不报错**。运营看到的是「我明明写了规则，用户却看不到条件」。
`TestPublicBadgeRequirementText` 只覆盖单条件/多条件两种，`M1.4-19`（lte 不进快路径）覆盖求值不覆盖文案。
为什么测试拦不住：这些形状都不是错，只是「没有文案」；测试断言的是「正确形状有文案」和「多条件没文案」，中间那一大片形状不在断言集里。
补法：后台规则编辑器在保存前计算「这段规则会不会产出公开文案」，不会产出时提示「请手填获得条件」——与 D-1 一起做，一次收口。

### 附注（不是风险，是「验收脚本自身」的同类假绿，供顺手修）

`scripts/verify-badge-rules-acceptance.sh:551`（目录那一段）的样本选择比生成判据**宽**：
`(rule->'root'->>'type') IS NOT NULL OR (op='all' AND children=1)`。
`type IS NOT NULL` 会把 `lt/eq`、`value<=0`、`window.last_days`、未知指标的单叶子也选进来，
这些形状按设计就没有文案 → 是一颗**假 FAIL**（不是假绿）。资料页那半边已按精确判据修好，
目录这半边建议同样换成 `:590` 那条形状 SQL（本次未动，避免和并行改动撞行）。

---

## 未覆盖 / 依赖条件（交付时要说清）

1. **多条件的运行时端到端**：需要「能写库的测试夹具」或一枚真实多条件徽章。当前只能 pend。
2. **浏览器渲染证据**（clamp 两行、窄屏无横向溢出、单 navbar/footer）：本轮未跑浏览器；静态契约已覆盖。
3. **整脚本全量跑**：未跑（会拉一次性 PG 集群跑完 M1.1–M1.4）；只做了定点三态验证 + `bash -n`。
4. **i18n 门禁红**：`adminBadgesI18n.test.ts` 现在红，属 `rule/` 在做的 UI 与新动态键族的收口项，不在本报告可修范围（禁止改 locale/组件）。
5. 目录侧样本判据偏宽（见附注），未修。

## 证据索引

- 脚本：`scripts/verify-badge-rules-acceptance.sh:576-673`（新判据）、`:583`（旧判据，已不存在）
- 后端：`app/Models/Badges/requirement_text.go:16-77`、`types.go:112-131`、`app/Http/Controllers/Badges/dto.go:31,142-153,218,246`、`controller.go:153-171,274-299,794-806`、`admin_service.go:292-356`
- 前端：`utils/forum/forumBadge.ts:122-131`、`components/forum/badges/SFBadgeCard.vue:101,203-236`、`components/profile/SFProfileBadgeTab.vue:120`、`composables/admin/badges/useAdminBadges.ts:16-31,295-303`、`utils/admin/adminBadges.ts:270-286`、`pages/admin/badges.vue:162-172`
- 测试：`tests/admin/adminBadgesI18n.test.ts:144-167,253-278`、`tests/admin/adminBadgesRecomputeResult.test.ts:40-69`、`tests/forum/badgeRequirementText.test.ts:75-140`、`tests/forum/profileBadgeTab.test.ts:372-417`、`tests/badges/badgeCatalogData.test.ts:137-170`
- 命令：`bun test tests/admin/adminBadgesI18n.test.ts …`（9 pass/1 fail）、`bun test /tmp/mut/probe.test.ts`（变异矩阵）、`./scripts/test-api-isolated.sh -run '^(TestPublicBadgeRequirementText|TestUpdateBadgeRuleNullCombinations)$' ./app/Models/Badges/...`（ok）、`bash -n scripts/verify-badge-rules-acceptance.sh`
