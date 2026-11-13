# 决策：徽章规则 objectRef 候选（`refCandidates`）

日期：2026-11-13
状态：accepted
上游：`plans/2026-11-12-badge-rule-engine.md`（§5.2.1 冻结的参数形状、§13#11）、
`decisions/2026-11-12-badge-plugin-facts.md`（`factCandidates` 的先例）

## 背景：一个「半根接口」的占位

计划把 `objectRef` 定义成「**有候选列表的引用**」，并明确禁止退化成自由文本
（`plans/2026-11-12-badge-rule-engine.md:226`，原话「能选就别让人手打」）。但 M3 只给
`refKind=fact` 接了候选来源，`category/tag/role/provider/badge` 五类从未接通，
前端 `SFAdminBadgeRuleParamField.vue` 对非 fact 的 objectRef 渲染一个**只有占位 option**
的 `<select>`（`admin.badges.conditions.candidatesPending` = 「候选列表待后端下发」）。

后果不是文案问题，是**功能死路**：`badge.owned` / `object.authoring` /
`identity.role` / `identity.oauth_link` 四条条件的必填参数恒为空 → 前端镜像校验抛
`rule.param_required` → 后台**配不完**、保存恒报「缺少必填参数。」。而
`tests/admin/adminBadgeRuleLeafParams.test.ts:284-288` 把「只有 1 个 option」钉成了断言，
等于把未完成状态写进基线——所以它一直是绿的。

后端其实早就 ready：`checkRuleReferences` 校存在性、`RuleHealth` 报 `missing_*`、
环检测也在。缺的只有候选下发 + 控件接线。

## 决策

**D1 候选在 `Models/Badges` 内直查 Core 表，不走 Forum / Identity service。**
沿用 `ref_names_postgres.go` 文件头的既有先例（`conditions` 包同样按此口径读 Core 数据面）。
理由：那些 service 的读口是「列出全部 + 分页 + 权限 + 视图模型」，当下拉候选用要么翻页
要么改签名，两边都被绑死；直查只是一条 `SELECT slug, name, <flag 列>`。

**D2 候选面聚合在既有 `GET /admin/badges/conditions`（additive 顶层 `refCandidates`），
不新增端点、不新增路由。**
与 `factCandidates` 同一个设计语言：一个编辑器一次请求拿全，降级只在一处发生。新增端点
要付路由登记 + identities + 生成物 + OpenAPI 四份成本，收益仅是「少 5 条小查询」。

**D3 恒返回五个 kind，固定顺序 `badge → category → tag → role → provider`。**
缺失的类别用 `unavailable: true` 显式表达，**绝不靠「键不在」表达**——那会把「取不到」
和「真的没有」混成一件事。顺序定义只有一处（`conditions.refKindOrder`），下发、补齐、
测试断言都读它。

**D4 `unavailable`（取不到）与「候选为空」语义分开、UI 上都退化成文本输入。**
对运营后果一致（都没有可选项），所以渲染一致；映射里分别保留，联调时能区分。
之所以两者都不能渲染空下拉：空下拉传达的是「不能选」，而运营真正需要的是「先手填 slug，
保存期健康检查会校验」。

**D5 `flags: ["hidden"|"disabled"]`，不做服务端本地化。**
分类/标签/角色/徽章名在库里只有单语名，只有 provider 有双语声明（由调用方 zh→en→id 回落）。
把「已隐藏 / 已停用」这类提示词放进服务端等于给每种语言加一份文案，而前端 i18n 已经有了。
**被 flag 标记的候选项仍然可选**：运营需要引用「当前隐藏」的分类，因为「当时发在该分类」
是历史事实（口径见 plan §5.2.2 #5）；禁用会让人以为规则写不出来。

**D6 每类上限 `RefCandidatesLimit = 500` + `truncated`。**
`SELECT … LIMIT 501` 探测截断（多取一行比再跑 COUNT 便宜，也不会把「恰好 500」与
「还有第 501」混为一谈）。截断后靠 `USelectMenu` 的客户端搜索缩小范围；不做远程分页搜索。

**D7 `fact` 分支一行不动。**
它的候选带双语 label 与 variants（M3 已验收），动它会重开那一刀的验收面。控件族统一
（factKey/variant 也用同一个可搜索控件）列为下一刀的残留。

**D8 失败语义：单类失败只让该类 `unavailable`，端点始终 200；错误只 `slog.Warn`。**
实现比计划更宽——`error` 时保留 store 返回的**部分结果**，而不是整体退化成五项全空：
一次 tags 抖动不该放大成「所有候选都没了」。有测试钉住
（`TestBadgeConditionsRefCandidatesKeepsPartialResultsOnError`）。

**D9 provider 的保存期判据与候选面必须同源（验收期发现的 P1）。**
候选来自 identity registry 的**已声明**快照，而保存期 `checkRuleReferences` 原本只查
`identity_external_links`（只记「有人绑定过」）。两者拼在一起就自相矛盾：插件刚装、
零绑定，候选里那个 provider **必存不进去**（422 `forum.badge_rule_reference_invalid`,
文案还写「可能已被删除」）——本刀要消灭的死路在 `identity.oauth_link` 上原样存在。
修法不是「过滤候选」，而是**保存期接受「绑定表 ∪ 已声明」**，复用 `rule_health.go` 的
`knownProviders`（不许写第二份 SQL）。依据是计划 §5.2.1 原文「值必须能对上
`identity_external_links.provider_id` **或** identity registry 的已声明 provider」——
是实现漏了后半句，不是放松约束。失效仍由 `rule_health.missing_provider` 兜，
与 `fact.count` 未声明 key 允许保存同一条纪律：**规则是运营资产，存得下、健康页提醒**。
拼错的 id 因两边都未命中仍 422。

## 未采用

- **bootstrap 跨域 provider**（在装配层代理 Forum/Identity service）：会引入 actor 传播与
  权限耦合，且这五类名/键在后台本来就不是秘密。
- **前端分别调 5 个既有 admin 端点**：零契约改动，但 5 次请求 + 5 份形状归一 + 各端点权限
  不一致导致的部分失败，且「编辑器唯一数据源」这条设计会被打破。
- **退化成自由文本（`string`）**：计划 §5.2.1 明令禁止；手打 slug 打错只会得到
  「保存成功、永不命中」——比空下拉更坏，因为没有任何可见异常。
- **隐藏这 4 条条件直到候选就绪**：止血，但不解决问题（这些条件正是简单模式下最有用的）。

## 后果与披露

- `badge.view` 持有者现在能看到：隐藏分类/隐藏标签的展示名、全部角色 `key + alias`、
  全部徽章 `slug + name`（含停用）、identity registry 已声明的 auth provider `id + label`。
  这是「配规则必须知道可选什么」的直接代价；**不含**任何凭据、密钥、用户数据。
- `SetDeclaredProviders` 同批注入，`rule_health` 从此认识「已声明但零绑定」的 provider
  （判定是 `绑定表 ∪ 已声明`，严格超集，不产生新的误报）。
- 这是**后台只读候选面**，刻意不进 `sforum_core_v1` 视图、不新增 HostQuery：插件没有消费面
  （插件要读规则走 `forum_badges` 视图即可）。按 AGENTS.md，刻意缺失的读面在模块笔记写明理由。
- 路由计数本刀不变（仍 423）。**注意**：423 不是本刀抬的，是 `7c07edf9d` 把
  `core.route.badges.admin_badge_rule_text` 写进 identities 与生成物却漏改 tripwire，
  HEAD 上 `go test ./app/Support/Routes/` 本来就是红的，本刀顺手补正（1 行 + 账注释）。

## 复核点

1. 新增 objectRef 类条件参数时，`RefKind` 必须落在 `conditions.refKindOrder` 已有的五类里，
   否则编辑器没有候选来源、会直接走文本兜底（可配但不友好）。
2. 任何「为了少一次请求」把候选塞进 `items` 的改动都会让「条件名」被「下拉文案」污染，
   参考 `factCandidates` 当初为什么不进 `items`。
3. 上限从 500 改小时，`truncated` 提示会变常见；改大时要重新评估每类行数对弹窗打开耗时的影响。
