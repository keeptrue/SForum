# 2026-11-13 徽章「获得条件」改为服务端生成

Status: **landed（未提交，工作树改动）**
模块笔记：`knowledge/modules/badges.md`「获得条件的自动解释器」节
Handoff：`knowledge/sessions/2026-11-13-badge-requirement-text-auto.md`
上游：`2026-11-12-badge-rule-engine.md`（规则是唯一判定来源）、
`2026-11-12-badge-plugin-facts.md`（插件条件经 `fact.count` 进入目录）

## 问题

徽章的公开说明（`requirement_summary_zh/en`）一直是**运营手填**。规则引擎上线后这条口径直接崩了：

- 条件树可以有 `all`/`any`/`not`、嵌套、时间窗口、`fact.count`，运营既填不准也填不全；
- 手填留空时公开目录与资料页会**完全没有说明**（手填时代最常见的真实故障）；
- 中英双列还要求运营写两遍，只填一种时另一种是空的。

于是「怎么拿到这枚徽章」这个公开面必须回答的问题，答案取决于运营有没有填文案——这不是可接受的产品口径。

## 决定 1 — 句子只由服务端生成，手填口径作废

`BuildRequirementText` / `BuildRuleRequirementText` 是唯一入口：规则树 → 双语自然语言。
`conditionParamDTO` 的文案、`Definition.Phrase`（`conditions/phrases.go`）与渲染器
（`requirement_text_render.go`）三处合起来就是全部措辞来源，**不存在第二个句子作者**。

为什么不放在前端：公开目录（SSR + 主题）、资料页徽章墙、后台列表/弹窗三处消费，
前端生成意味着三份实现与三份 i18n，且中文断句规则（标点连接、压缩、降级）要在模板里重写一遍。
放服务端还顺手解决了「预览与公开面不一致」这类最难查的假承诺。

**代价**：措辞改动要发版（不能靠运营改字）；每加一种条件必须同时写 `Phrase` + golden 样本，
否则注册测试判红。这是刻意的——新条件没有公开说明等于又把空白带回公开面。

## 决定 2 — 生成器 = 公开面 = 预览端点，同一实现

`POST /admin/badges/rule-text` 的 handler 只做「解码 → 调域层 → 编码」，句子全部来自同一个域函数；
测试 `TestBuildRequirementTextSharesPreviewImplementation` 逐字断言两处输出相等。
权限取 **`badge.view`**：端点不写库、不审计、不入队，且同一批管理员本就能从 `GET /admin/badges`
读到规则原文，改 `badge.manage` 只会让只读管理员看不到自己有权读的东西。

非法规则在预览时返回**与保存相同**的 422 码——预览与保存共用一条校验链，
否则会出现「预览能出句子、保存被拒」这种让人以为端点坏了的组合。

## 决定 3 — 手工徽章与脏规则给两套**不同**的固定句

- 手工徽章（`Rule == nil`）：`由管理员手动授予` / `Granted manually by an admin`。
- 脏规则（`RuleRaw` 非空但解不开，`RuleBroken`）：泛称句「满足一个自定义条件」。

把脏规则也说成「由管理员手动授予」等于把**脏数据说成运营决定**，运营看到后不会去修数据；
两套句子让「数据坏了」这件事在公开面上仍然是诚实的（后台另有 `rule_health` 的 `undecodable_rule` 指路）。

## 决定 4 — 废弃列与 PATCH 字段保留一版

`requirement_summary_zh/en` 列、`POST`/`PATCH` 的 `requirementSummary` 字段、后台 wire 的回显
都保留：服务端照旧接受并落库，旧客户端不会 422。但**任何文案读路径都不再读它**——
公开目录、资料页徽章墙、后台列表与编辑弹窗全部走生成的 `RequirementText`（`Public()` 现算，
不拿结构里缓存的旧文案）。前端 `toAdminBadgeInput` 不再发送该字段。

**为什么不一次性删列**：删列要写迁移且不可逆；保留一版让回滚只需恢复读列 + 回退该 commit，
且旧客户端在灰度期不会因缺字段而报错。

## 回滚方式

1. 回退本刀 commit（生成器 + 端点 + 前端预览 + 契约）。
2. 恢复读列：公开面重新从 `requirement_summary_zh/en` 取文案（列与数据都还在，**不需要迁移**）。

回滚后新规则的徽章会重新变成「手填为空 → 没有说明」的状态，这是接受的回滚代价，不是新缺陷。

## 不采用的方案

- **规则引擎里存一份生成的句子**（写入时生成）：规则改一次就得回填全库，且脏规则/旧数据永远补不上；
  现算只花一次批量名字查询（`ResolveRefNames` 的 `= ANY($1)`）。
- **运营手填 + 自动生成并列，手填优先**：这正是本刀要废掉的那条旧口径——两条来源必然分叉，
  且公开面到底显示哪句取决于有没有填，无法解释给运营。
- **前端生成句子**：见决定 1（三份实现、断句规则重写、预览与公开面不一致）。

## 代价与已知残留

- `fact.count` 句子不带计量单位（单位在 `FactCandidate`，`PhraseFunc` 拿不到）；
- 措辞改动需要发版；新增条件必须补 `Phrase` + golden；
- `Badges/conditions` 有两条日期敏感测试在干净 HEAD 也红，与本刀无关，需单独修。
