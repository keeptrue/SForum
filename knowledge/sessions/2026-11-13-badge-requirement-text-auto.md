# 2026-11-13 徽章「获得条件」自动解释器

Status: **landed（未提交，工作树 69 项改动里的一部分）**——公开说明改用服务端生成，运营手填口径作废。
模块笔记：`../modules/badges.md`「获得条件的自动解释器」节；决策：
`../decisions/2026-11-13-badge-requirement-text-auto.md`；前序：
`2026-11-12-badge-rule-engine-handoff.md`、`2026-11-12-badge-rule-editor-ux.md`。

## Changed

一句话：运营手写的两个「获得条件」textarea 换成服务端从规则生成的只读预览，公开目录 / 资料页徽章墙 /
后台列表三处共用同一条生成链路。

- **唯一实现**：`conditions/phrases.go`（269 行，13 种条件的双语措辞 + `Definition.Phrase`）+
  `requirement_text_render.go`（244 行，树遍历）。中文用标点连接（`，并且` / `，或者`，无尾空格）；
  组内子句 > 4 压成「满足全部 N 个条件，例如：A、B、C」、`any` 压成「满足其中任一条件（共 N 个）」；
  单句 > 100 字符的叶子降级为「满足『条件名』条件」，整段 > 160 降级为「满足 N 个条件，其中最关键的是：…」。
- **永不空白**：手工徽章 →「由管理员手动授予」；脏规则（`RuleBroken`）→ 泛称句（**不谎称**手动授予）；
  取不到名字 → 泛称回落。任何形状都有 zh + en。
- **引用真名**：`CollectRefs` → `RefSet` → `RefNameLoader` → `ResolveRefNames`，每类一次 `= ANY($1)`；
  provider 名来自 identity registry 的插件 label（装配 `bootstrap/api_assembly_domains.go`）。渲染是纯函数，
  不存在 N+1。
- **预览端点**：`POST /admin/badges/rule-text`（`badge.view`、只读、`no-store`、与公开面同一渲染函数、
  非法规则给与保存相同的 422）。
- **前端**：两个 textarea → 只读预览 +「由规则自动生成」；`useBadgeRuleTextPreview` 300ms 防抖 +
  序号丢弃过期响应 + 失败保留旧文案且不阻断保存；`toAdminBadgeInput` 不再发 `requirementSummary`；
  `badgeRuleNeedsSummary` / `AUTO_SUMMARY_METRICS` 删除。
- **保留但不读**：`requirement_summary_zh/en` 列、`POST`/`PATCH` 的 `requirementSummary`、后台 wire 回显
  都还在（旧客户端 + 回滚用），服务端仍接受并落库；**没有任何文案读路径再用它**。

## Decisions

1. 句子**只**由服务端生成，不再有「运营手填优先」这条口径（多条件/带窗口的规则运营填不准，
   手填时代的真实故障就是公开面没有说明）。
2. 生成器与预览端点、公开面**同一实现**：预览看到的句子必须逐字等于公开面那句，否则预览是假承诺。
3. 手工徽章与脏规则给**两套不同**的固定句；把脏规则说成「由管理员手动授予」等于把脏数据说成运营决定。
4. 废弃列与 PATCH 字段**保留一版**：回滚只需恢复读列 + 回退该 commit，不必写迁移。

## Next

- 提交时与规则引擎 / 编辑器 UX 的收口分开：本刀改动横跨 `apps/api`（生成器 + 端点 + 装配）、
  `apps/web`（预览 + 表单）、`contracts`（新端点 schema）。**当前工作树未提交**。
- 新增条件类型时三件套一起做：`conditions/registry.go` 的 `Definition.Phrase`、`phraseSamples()` 的
  golden、i18n 无关的双语句子——缺任何一件注册测试直接红。

## Open Questions

1. **`fact.count` 句子仍不带计量单位**：`PhraseFunc` 拿不到 `FactCandidate.UnitZh/UnitEn`（见模块笔记残留 27）。
2. **390×844 与键盘可达性**仍是结构级证据（沿用 `2026-11-12-badge-rule-editor-ux.md` 的结论，340px 只是行宽模拟）。
3. **`Badges/conditions` 两条日期敏感测试在干净 HEAD 也红**（`TestIdentityConditions`、
   `TestActivityStreakEndsTodayOrYesterday`），与本刀无关，需单独修。

## 验证

- web 全量 `bun test`：**2610 pass / 0 fail**，263 文件、24,300 `expect()`、exit 0（本轮本机实跑）。
- Go 定向（本机实跑）：`go test ./app/Models/Badges/ -run 'TestEveryConditionHasPhrase|TestPhraseGolden|
  TestGroupComposition|TestManualBadgeHasFixedSentence|TestBrokenRuleDoesNotClaimManual|TestDeepNestingAlwaysRenders'`
  → `ok`（0.03s，无需数据库）。`TestEveryConditionHasPhrase` 会**实际调用**每个条件，空串当场红。
- 终审（审阅侧）穷举 936 种形状组合 0 失败；Go `Models/Badges` 与 `Http/Controllers/Badges` 的定向 DB
  测试 ok（数字以终审为准，本文件只记本机实跑的两条）。
