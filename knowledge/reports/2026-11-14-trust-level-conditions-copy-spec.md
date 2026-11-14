# 文案规范：信任等级条件化门槛（6 错误码 + 14 条件 + 后台 UI + 影响弹窗三态）

Status: **已定稿**（2026-11-14，v2）。基线 HEAD `e4f8c3b1f`（M0 条件包已迁到 `Models/Conditions`，
本文所有路径按迁移后写）。Q1–Q6 已由 Lead 裁决，本文按裁决收口，不再有待确认项。

作者：pm（阿策）。本文件是**文案与键名的唯一真相**，后端与前端按此实现，不得各自另编一套。

---

## 0. 与原始简报不符的三处实测（已由 Lead 独立核实并采纳）

原始 task-3 与 briefing 照抄了 `knowledge/modules/trust-levels.md` 的旧口径。三处都是错的，本文按实测写：

| # | 简报原话 | 实测 | 证据 |
| --- | --- | --- | --- |
| C1 | 后台键族是 `admin.settings.trustLevels.*` | 真实族是 **`admin.forum.trustLevels.*`**（243 叶键）；`admin.settings.trustLevels` = 0 键，且被测试断言**必须不存在** | `apps/web/i18n/locales/zh-CN.json:5684`；`apps/web/tests/admin/forum/trustLevels.test.ts:833-839` |
| C2 | 「`trustLevels.test.ts` 的双向断言连多出来的键都判红」 | **部分成立**。usage 扫描式双向断言（多出键即红）只存在于 `adminBadgesI18n.test.ts:169-176`，**不覆盖** `admin.forum.trustLevels`。trustLevels 侧真会红的只有 4 类，见 §4.3 | 同上两组文件 |
| C3 | 有值态文案「按当前**已保存**的规则估算」 | 该句来自徽章弹窗（徽章是 GET-by-id，只能估已存规则）。本域 `rule-impact` 是 **POST 草稿请求体**，估的是即将保存的配置。照抄即事实错误 | 计划 §3.5 请求形状 `{tiers:[{tierId, requirements, rule}]}` |

源头修正落点：Lead 已在计划 §0 加实测行、§3.4 加纠错段、briefing 追加更正；`knowledge/modules/trust-levels.md` 由 M5 改。

### 0.1 裁决与推翻记录（v1/v2 里被推翻的写法已删）

**规则：每一条被推翻的旧写法，必须有一条能红的断言。** 只删条款不够——删掉之后没人知道它为什么被删，
下一个人会照旧计划加回来。所以下表每行的最后一列都是可执行判据，不是说明文字。

| 裁决 | 结论 | 本文落点 | 已删除的写法 | 能红的断言 |
| --- | --- | --- | --- | --- |
| Q1 | 影响面 = **满足该档条件的估算上界**，不是升降人数（聚合 SQL 算不准升级数）。文案只讲「满足这一档的条件」，**不得承诺**升降人数 | §3.3 | v1「≈ {count} 人**受影响**」 | 弹窗 `innerText` 不含「受影响」/「将升级」/「将降级」；且 `conditions.impact.meaningHint` 在两份 locale 都存在且非空 |
| Q2 | **新档也估算**（POST 草稿体，不依赖已保存 id）。「保存后才能估算」作废 | §3.3.1（无 D 态）、CP-4 | v1 `impact.newTierHint`、D 态、「保存后才能估算影响范围」 | **CP-4**：新建档弹窗出现 A/B/C 之一，且 `innerText` 不含「保存后才能估算」；另断言 `newTierHint` 键在 locale 里不存在 |
| Q2b（小设） | **数字全屏只出现一次**：有值态句子不带 `{n}`，`≈ N 人` 只在数字位渲染 | §3.3.2 | v1 句子里重复 `{n}` | **CP-5**：弹窗内 `≈` 出现次数 ≤ 1，且 `meaningHint` 渲染结果零数字字符 |
| Q3 | 第 6 条码转正：`earnable_not_declared`，进 specific 桶。**33 → 39**，前端断言 **39 / 21 / 18** | §1.1、§4.2、§4.3、§7 | v1「38 / 20 / 18」、v1「待裁决」 | `trustLevels.test.ts:594-604` 的 `toHaveLength(39 / 21 / 18)` + 并集 `toEqual(BACKEND_CODES)`；断言 `earnable_not_declared_plugin_disabled` **不在** `BACKEND_CODES` 里 |
| Q4 | 加 `earnablePermissionStates`，三态 **`valid` / `inactive` / `unknown`**（D22：不用 `orphan`） | §5.2 | v1 `active` / `inactive` / `undeclared`；v2 曾用的 `orphan` | 契约测试断言 `earnablePermissionStates[].state` 的 enum == `["valid","inactive","unknown"]`；`rg -in "orphan" contracts/openapi/schemas/trust_levels.yaml` → 0（**只在本域契约文件内**要求零命中；`orphan` 在 attachments / system 等无关模块是既有词，不要全局扫） |
| Q5 | `identity.profile` 枚举**四值** `bio` / `signature` / `website` / `location` | §2.4 | v1 三值 | 断言 `identity.profile` 的 `field` Options 长度 == 4 且含 `location`；`TestPhraseGolden` 里 `location` 样本逐字命中「已填写「所在地」」 |
| Q6 | 公开兜底名**不得裸拼 factKey**：中文「插件条件（当前不可用）」、英文「a plugin condition that is currently unavailable」；后台显示 factKey + 「未声明」 | §2.6 | v1 沿用 `fallbackFact` =「插件事实计数」并把 key 拼进句子 | 值级断言：用**未声明**的 factKey 生成句子，结果**等于**「插件条件（当前不可用）≥ N」（en 同义），且不含该 factKey 的字面串；`TestFactPhraseDoesNotRepeatTotal` 绿 |

### 0.2 交付完成态

| 小节 | 交付物 | 涉及文件 | 完成态 |
| --- | --- | --- | --- |
| §1 / §1.1 / §1.2 | 6 条新码的双语文案（含两成因分支） | 前端 locale；后端 `config.go` 常量表 | 定稿，可直接落盘 |
| §2 | 14 条条件目录文案 + `identity.profile` 完整定义 + `fact.count` 不可用措辞 | `Models/Conditions/registry.go`、`phrases.go`、`refnames.go`；`Models/Badges/requirement_text_test.go` | 定稿 |
| §3 | 后台 UI 四组文案（开关 / 面板五态 / 弹窗三态 / 失效提示） | 前端 locale + 新组件 | 定稿 |
| §4 | 键路径表（新增 / 复用 / 不得新增）+ 会红断言清单 | 前端 locale | 定稿，可作 frontend 落地清单 |
| §5 | 插件权限授予的运营语义 + 三态 DTO | 后端 DTO + 前端 earnable 矩阵 | 定稿（结论：加状态位） |

### 0.3 依赖顺序（谁先做、谁能并行）

```
§2 条件文案 ──┬─→ 后端 registry.go / phrases.go / refnames.go（M1）
              └─→ §3.1 §3.2 面板文案（M3，前端要有目录才渲染得出）
§1 错误码 ──────→ 后端 config.go 常量（M1）──→ 前端 BACKEND_CODES 39/21/18 + 两份 locale
§3.3 影响弹窗 ─→ 依赖 M1 的 POST /admin/trust-levels/rule-impact
§3.5 失效提示 ─→ 依赖 §5.2 的三态字段
§4 键表 ───────→ 必须在 frontend 动 locale 之前冻结
§5 ────────────→ 与 M1–M3 并行；唯一前置是 DTO 加字段
```

可并行：§2 与 §3 互不阻塞；§1 完全独立。唯一硬串行是 **§4 先于 locale 落盘**。

---

## 1. 六条新配置错误码（zh-CN / en-US）

写作纪律（与既有 `errors.code.*` 同声口）：

- 不复述错误名，只给动作：删什么、减到多少、换哪个；
- 全部是 tier 级，页面已在「这一档有 N 处需要修正」处定位，文案**不再**写「第 N 档」；
- 禁词：`cmp` / `params` / `children` / `rule` / AST / 规则树。用「条件」「并且/或者」「非」「阈值」；
- 键路径 `admin.forum.trustLevels.errors.code.<code>`，全部进 `SPECIFIC_ERROR_CODES`（**15 → 21**）。

| code | zh-CN | en-US |
| --- | --- | --- |
| `rule_too_complex` | 条件的层数或条数超上限：请把「并且/或者」嵌套减到 5 层、条件减到 32 条以内，阈值不超过 10 亿。 | Too much nesting or too many conditions: keep to 5 levels and 32 conditions, with values under 1,000,000,000. |
| `rule_unknown_condition` | 有一条条件已不在可用目录里（可能是插件卸载后消失）：请删除它，或换成目录里还在的条件。 | A condition is no longer in the catalog, usually because a plugin was removed: delete it or pick one that still exists. |
| `rule_shape_invalid` | 条件的组合写得不成形：「并且/或者」至少要有一个条件，「非」必须恰好一个，计数条件要填阈值。 | The group is incomplete: “and/or” needs at least one item, “not” exactly one, and count conditions need a value. |
| `rule_reference_invalid` | 引用的对象已找不到（分类、标签、角色、登录方式、徽章或主题可能已被删除）：请重新选一项。 | What it points at is gone (category, tag, role, sign-in provider, badge, or topic may be deleted): choose again. |
| `rule_condition_not_available` | 这条条件不能用于信任等级（内容质量类条件不参与等级判定）：请删除它，或换成别的条件。 | That condition cannot be used for trust levels (content-quality conditions are excluded): delete it or use another one. |
| `earnable_not_declared` | 见 §1.1 —— 按成因分两句，默认句为「从未被声明」那支。 | 见 §1.1 |

字数核对（脚本实测，含标点）：5 条 rule 码 zh 总长 53 / 43 / 46 / 44 / 42 字符（汉字 35–44 个），
en 109 / 119 / 112 / 112 / 119 字符。均在 ≤60 汉字 / ≤120 字符内。

**为什么不复用徽章那 3 条**：徽章原文是「徽章规则过于复杂：层级不超过 5 层…」。它先说对象（徽章）再说动作，
本域在档位上下文里，重复对象层是噪声；且徽章用「层级」，与后台既有「嵌套 / 层数」不是同一个词。
**共享的是校准过的数字，不是句子。**

### 1.1 `earnable_not_declared`：一个码，两种成因

同一个稳定码，响应带 `params.cause`，前端据此选键。**它不是第 40 条码，不进 `BACKEND_CODES`**——
它是同一个码的两种措辞变体，`SPECIFIC_ERROR_CODES` 仍然只有 `earnable_not_declared` 一项。

| `params.cause` | 键（`admin.forum.trustLevels.errors.code.` 下） | zh-CN | en-US |
| --- | --- | --- | --- |
| `undeclared`（默认，字段缺失也走这支） | `earnable_not_declared` | 这个权限从未被任何插件声明，不能按等级解锁：请取消勾选，或换成插件声明过的权限。 | No plugin declares this permission, so it cannot unlock by level: uncheck it, or pick a permission a plugin declares. |
| `plugin_disabled` | `earnable_not_declared_plugin_disabled` | 声明这个权限的插件「{plugin}」当前已停用，勾选暂时不生效：请启用该插件后重试。 | Plugin “{plugin}” declares this permission but is disabled, so the check does nothing: enable it and try again. |

前端实现落点：`apps/web/app/composables/admin/forum/trustLevels/useTrustLevelSaveErrors.ts` 的键解析处加一条
「同码变体」分支（`code === 'earnable_not_declared' && params.cause === 'plugin_disabled'` → 用后缀键），
**不要**为此新增第 40 个码——那会把三处同步面扩大到四处。

字数（脚本实测）：zh 40 / 43 字符（汉字 36 / 30）；en 117 / 111 字符。均在 ≤60 汉字 / ≤120 字符内。

### 1.2 为什么必须分两句（修法不同，不能合并）

| 成因 | 事实 | 运营要做 | 合并成一句的后果 |
| --- | --- | --- | --- |
| `plugin_disabled` | 插件**装过、声明过**，现在停了；键仍在白名单语义内，运行时被过滤 | **启用插件**（1 步，配置不用动） | 写成「从未被声明」，运营会去删勾选，等插件恢复时配置已被自己删掉 |
| `undeclared` | 没有任何启用插件声明过这个键（卸载、改名、或手写错 key） | **删掉这个勾选**或换键 | 写成「插件停用」，运营会去插件管理里找一个根本不存在的插件 |

这与徽章刀 `ErrBadgeRuleModeConflict` 与 `ErrBadgeRuleInvalid` 分开的理由同源（`Models/Badges/errors.go` 注释已写明）：
同一类症状、不同修法，共用一个码就是让运维读一句没法执行的话。

---

## 2. 条件目录文案（14 条）

### 2.1 两张表，落点不同，不要混

| 层 | 字段 | 落点 | 谁读 |
| --- | --- | --- | --- |
| 后台目录文案 | `LabelZh` / `LabelEn` / `DescribeZh` / `DescribeEn` / `UnitZh` / `UnitEn` | `Models/Conditions/registry.go:214-351`（Go 字面量） | 后台规则编辑器、目录接口 |
| 公开句子 | `Phrase` | `Models/Conditions/phrases.go` + `refnames.go` | 资料页 / 徽章墙（只下发句子，**绝不下发 AST**） |

**硬约束（不要提案改）**：现有 13 条的公开句子被 `TestPhraseGolden` 逐字钉死
（`apps/api/app/Models/Badges/requirement_text_test.go:51,197`）。本刀**只新增** `identity.profile` 一句、
只改 `fact.count` 的**未声明分支**（§2.6，golden 用的是已声明样本，不受影响）。13 条已声明句子一个字都不动。
改 `Describe*` 不触发 golden（它不在 golden 里）。

「每条件必须有句子」的注册锁是 `TestEveryConditionHasPhrase`（同文件 `:166`，`len(definitions) != 13` 在 `:168`）；
条数 13 → 14 的另一处硬编码在 `Models/Conditions/conditions_postgres_integration_test.go:264-265`。**两处都要改。**

### 2.2 口径警告（5 条必写，写错运营就误解）

| key | 当前 `DescribeZh` | 改为（口径写进去） | 为什么 |
| --- | --- | --- | --- |
| `activity.count` | 按指标统计活跃度，可选全部时间或最近 N 天。 | 按指标统计活跃度，可选全部时间或最近 N 天。**获赞不含自赞**，窗口版与累计版口径一致。 | 窗口 SQL 显式 `r.user_id <> $1`（`Models/Conditions/resolve_activity.go` 的 `activityCountSQL` 注释段）；累计列写路径对自赞返 0。不写清会出现「累计 5 / 最近 30 天 0」的运营投诉。 |
| `content.hidden` | 被隐藏的主题数。这是唯一按内容状态判定的条件，通常配合「非」使用。 | 被**管理员隐藏的主题**数，**不是违规次数**；评论被隐藏不计入。通常放在「非」下面表达「没有违规记录」。 | SQL 是 `topics WHERE status='hidden'`，只到主题粒度（`Models/Conditions/resolve_content.go` 的 `hiddenSQL`）。写成「违规次数」会让运营以为含评论审核。 |
| `content.pinned` | 被置顶的主题数（含已软删）。 | **当前仍被置顶**的主题数（含已软删的主题），不是「曾经被置顶过」。 | SQL 是 `WHERE author_user_id = $1 AND is_pinned`，无历史表、无时间维度。 |
| `object.engagement` | 是否对指定主题点过赞、收藏过或评论过。 | 是否对指定主题点过赞、收藏过或评论过。**评论含自己主题下的评论**。 | comment 分支 `WHERE c.author_user_id = $1 AND c.topic_id = ...`，不排除主题作者本人（P-11 裁决，见 `Models/Conditions/resolve_object.go` 注释）。 |
| `fact.count` | 插件上报的事实累计值，可按窗口统计。 | 插件上报的事实累计值，可按窗口统计。**插件停用后这条条件无法判定，不等于 0**（见 §2.6）。 | `Models/Conditions/resolve_facts.go` 对未声明 factKey **返 error → unknown**（刻意不返 0，否则 `lte` / `not` 会 fail-open）。 |

### 2.3 全量 14 条（可直接抄进 `registry.go`）

「变更」列：`—` 保持现状；`改写` 仅 `Describe*` 改；`新增` 整条新加。单位列空 = 布尔条件，`UnitZh` / `UnitEn` 留空串。

| # | key | LabelZh / LabelEn | DescribeZh / DescribeEn | 单位 | 变更 |
| --- | --- | --- | --- | --- | --- |
| 1 | `activity.count` | 活跃计数 / Activity count | 按指标统计活跃度，可选全部时间或最近 N 天。获赞不含自赞，窗口版与累计版口径一致。 / Activity count by metric, all time or the last N days. Likes received exclude self-likes in both the windowed and lifetime counts. | 次 / times | 改写 |
| 2 | `activity.streak` | 连续访问 / Visit streak | 连续访问天数（以今天或昨天收尾）。断一天就归零。 / Consecutive visiting days, ending today or yesterday; one missed day resets it. | 天 / days | 改写 |
| 3 | `object.engagement` | 与主题互动 / Engaged with topic | 是否对指定主题点过赞、收藏过或评论过。评论含自己主题下的评论。 / Whether the user liked, bookmarked or commented on the given topic. Comments on the user's own topic count. | — | 改写 |
| 4 | `object.authoring` | 在分类/标签下创作 / Authored in category/tag | 在指定分类或标签下发布的主题数，可选窗口。软删的主题仍计入。 / Topics authored in the given category or tag, optionally within a window. Soft-deleted topics still count. | 篇 / topics | 改写 |
| 5 | `identity.account_age` | 账户年龄 / Account age | 账户注册至今的天数，按自然日折算。 / Days since the account was created, counted by calendar day. | 天 / days | 改写 |
| 6 | `identity.email_verified` | 邮箱已验证 / Email verified | 邮箱是否已验证。 / Whether the email address is verified. | — | — |
| 7 | `identity.oauth_link` | 已绑定第三方账号 / OAuth account linked | 是否绑定了指定的第三方登录（只认启用中的绑定）。 / Whether the given OAuth provider is linked and active. | — | — |
| 8 | `identity.role` | 拥有角色 / Has role | 是否拥有指定角色（按角色 key）。停用中的角色不算。 / Whether the user holds the given role key. Disabled roles do not count. | — | 改写 |
| 9 | `identity.avatar` | 已设置头像 / Avatar set | 是否上传过头像。 / Whether the user has uploaded an avatar. | — | — |
| 10 | `content.pinned` | 主题被置顶 / Topics pinned | 当前仍被置顶的主题数（含已软删的主题），不是「曾经被置顶过」。 / Topics still pinned right now, soft-deleted ones included — not "was pinned once". | 篇 / topics | 改写 |
| 11 | `content.hidden` | 主题被隐藏 / Topics hidden | 被管理员隐藏的主题数，不是违规次数；评论被隐藏不计入。通常放在「非」下面表达「没有违规记录」。 / Topics hidden by a moderator — not a violation count; hidden comments are not included. Usually used under NOT. | 篇 / topics | 改写 |
| 12 | `badge.owned` | 已拥有徽章 / Owns badge | 是否已拥有指定徽章。保存时会做循环依赖检测。 / Whether the user owns the given badge. Save-time cycle detection applies. | — | — |
| 13 | `fact.count` | 插件事实计数 / Plugin fact count | 插件上报的事实累计值，可按窗口统计。插件停用或未安装时这条条件无法判定，不等于 0。 / Accumulated plugin fact total, optionally within a window. Once the plugin is off or missing this condition cannot be decided — that is not the same as 0. | 次 / times | 改写 |
| 14 | `identity.profile` | 资料已填写 / Profile filled in | 资料里的简介、签名、个人网站或所在地是否已填写。填了就算，不看内容质量。 / Whether the profile bio, signature, website or location is filled in. Any content counts; quality is not judged. | — | 新增 |

### 2.4 新增条件 `identity.profile`（照抄即可）

- `ValueKind`: `ruleengine.ValueBoolean`（有 `Phrase`，无 `Compare`、无 `Unit`）
- `Params`: 单个 `{Name: "field", Kind: ruleengine.ParamEnum, Required: true, Options: []string{"bio","signature","website","location"}}`
  - **参数取值是 `website`，库里列名是 `user_profiles.website_url`**（`apps/api/database/migrations/202607070004_user_profiles.sql:8`）。别把列名当参数值，那是契约泄漏。
  - `location` 与其他三列同表同机制：`user_profiles` 的 `bio` / `signature` / `location` / `website_url` 四列都是 `TEXT NOT NULL DEFAULT ''`（迁移 `:5-8`）。**同一 SQL 判非空串即命中**，零成本。
  - 中文名用「所在地」——与既有 `admin.users.location`（`zh-CN.json`）和 `profileSettings.scope.publicFields`（「简介、所在地、网站…」）同一个词，不再另造「地区」。
- 真值来源：对应列 `<> ''`。注意是**非空串**判定，不是 `IS NOT NULL`（列 NOT NULL 且 DEFAULT ''，用 IS NOT NULL 会恒真）。
- 公开句子（**新增 golden 项**，与 `identity.role` 同款「谓语 + 名词表」结构；名词来自文案表，不得把 `bio` 这类 slug 拼进句子）：

```go
// profileFieldPhrases 是 identity.profile 的名词表。
// 与 authoringScopeEn 同一个理由：名词来自文案表，不来自参数取值。
var profileFieldPhrases = map[string][2]string{
    "bio":       {"简介", "bio"},
    "signature": {"签名", "signature"},
    "website":   {"个人网站", "website"},
    "location":  {"所在地", "location"},
}

func phraseIdentityProfile(params map[string]any, _ *ruleengine.Compare, _ RefNames) (string, string) {
    field := paramString(params, "field")
    labels, ok := profileFieldPhrases[field]
    if !ok {
        labels = [2]string{"资料", "profile"}
    }
    return fmt.Sprintf("已填写「%s」", labels[0]),
        fmt.Sprintf("\"%s\" filled in", labels[1])
}
```

golden 期望句（`phraseSamples` 新增一项，`field: "location"`——用四值里最后加的那个，顺便钉住枚举扩值）：
zh `已填写「所在地」` / en `"location" filled in`。

### 2.5 信任等级子集：14 → 12

排除 `content.pinned`、`content.hidden`（内容质量类，D4 / 计划 §6.2）。被排除的两条仍出现在**全集**目录里，
选中时报 `rule_condition_not_available`——所以 §1 那句必须能独立读懂「为什么这条不行」，它做到了。

### 2.6 `fact.count` 的「不可用」措辞（Q6）

现状是**公开面裸拼内部 key**：`RefNames.Fact` 未命中时回落到 `fallbackFact`（`Models/Conditions/refnames.go:42`
=「插件事实计数」/ "a plugin fact"），但 `phraseFactCount` 仍会把名字套进 `「%s」%s` 模板，
于是 `acme.onboarding.tasks` 这类机读串会出现在资料页——既泄露内部命名空间，又不可读。

**公开面（改 `refnames.go` + `phrases.go`）**

| 位置 | 现值 | 改为 |
| --- | --- | --- |
| `fallbackFact`（`refnames.go:42`） | `{Zh: "插件事实计数", En: "a plugin fact"}` | `{Zh: "插件条件（当前不可用）", En: "a plugin condition that is currently unavailable"}` |
| `phraseFactCount` 未解析分支 | 套 `「%s」%s` / `"%s" total %s`，得到「「插件事实计数」≥ 3」/ `"a plugin fact" total ≥ 3` | **不套引号、不加 "total"**：zh「插件条件（当前不可用）≥ 3」、en `a plugin condition that is currently unavailable (≥ 3)`；带窗口时前缀/后缀照旧（zh「最近 7 天内插件条件（当前不可用）≥ 3」，en 尾部 ` in the last 7 days`） |

理由：英文模板是 `"%s" total %s`，把长句塞进去会变成 `"…unavailable" total ≥ 3`；
`TestFactPhraseDoesNotRepeatTotal`（`Models/Badges/requirement_text_test.go:445`）钉的正是这类拼接病。
golden 用的是**已声明**样本（`"签到（每日）"`），本改动不碰 golden。

**后台面（新键）**

| 键 | zh-CN | en-US |
| --- | --- | --- |
| `conditions.fact.undeclared` | 未声明 | Undeclared |
| `conditions.fact.undeclaredHint` | 没有启用中的插件声明这个事实键，这条条件无法判定；这一档所有人都不成立。 | No enabled plugin declares this fact key, so the condition cannot be decided; nobody qualifies for this tier. |

后台**必须显示 factKey 原文**（`acme.onboarding.tasks` + 「未声明」）：后台要的是可诊断性，公开面才是可读性。
这与 §2.4「名词不拼 slug」不矛盾——两面的目标不同，写在这里以免被同步成一套。

---

## 3. 后台 UI 文案（zh / en 成对）

统一口吻基线（取自同页既有键，不要另起风格）：陈述句、句末「。」、不用感叹号，技术词改成运营词
（「条件」「阈值」「档位」「解锁」）。

### 3.1 折叠面板的展开开关

实现为 `<button aria-expanded>` 且可见文字足够时**不另加 aria-label**；下面的 `ariaLabel` 只给「窄屏只留图标」形态用。

| 键 | zh-CN | en-US | 说明 |
| --- | --- | --- | --- |
| `conditions.toggle.more` | 更多条件（{count}） | More conditions ({count}) | 已有条件；`{count}` = 条件条数（含嵌套内的叶子） |
| `conditions.toggle.add` | 添加条件 | Add conditions | 一条条件都没有 |
| `conditions.toggle.ariaLabel` | {name}的更多条件，已设置 {count} 条 | More conditions for {name}: {count} set | 仅图标形态 |
| `conditions.toggle.ariaLabelEmpty` | {name}的更多条件，尚未设置 | More conditions for {name}: none set | 仅图标形态 |

（本节起，省略公共前缀 `admin.forum.trustLevels.`。）

措辞理由：`{count}` 用括号是既有惯例（`earnable.count` =「新增项 {checked}/{total}」、
`tiers.activeCount` =「启用 {count} / {max} 档」）。**不要**写成「N 个条件已配置」——那会把「展开」这个动作从标签里抹掉。

### 3.2 面板内的空态 / 加载态 / 失败态

| 键 | zh-CN | en-US |
| --- | --- | --- |
| `conditions.panel.title` | 附加条件 | Additional conditions |
| `conditions.panel.description` | 数值阈值之外再加一层：只有数值与这里全部满足，才升到这一档。 | A second gate on top of the numeric thresholds: a user reaches this tier only when both pass. |
| `conditions.panel.loading` | 正在读取条件目录… | Loading the condition catalog… |
| `conditions.panel.empty.title` | 这一档还没有附加条件 | This tier has no additional conditions |
| `conditions.panel.empty.description` | 现在只按上面的数值阈值判定。要加条件，点「添加条件」。 | Only the numeric thresholds decide this tier right now. Use “Add conditions” to add more. |
| `conditions.panel.loadFailed.title` | 条件目录加载失败，规则暂不可编辑 | The condition catalog failed to load; conditions cannot be edited |
| `conditions.panel.loadFailed.description` | 编辑器已锁定，避免在目录缺失时改坏已保存的条件。数值阈值不受影响，仍可正常保存。 | The editor is locked so stored conditions cannot be damaged while the catalog is missing. Thresholds are unaffected and still save normally. |
| `conditions.panel.loadFailed.reason` | 失败原因：{reason} | Reason: {reason} |
| `conditions.panel.loadFailed.retry` | 重试 | Retry |
| `conditions.panel.readOnly.title` | 当前账号只能查看 | Your account can view this only |
| `conditions.panel.readOnly.description` | 修改条件需要「信任等级管理」权限。 | Changing conditions requires the trust-level management permission. |
| `conditions.panel.featureOff` | 功能关闭时读不到条件目录，附加条件暂不可编辑；开关打开后会自动加载。 | The catalog cannot be read while the feature is off, so conditions are not editable here; it loads once the flag is on. |
| `conditions.panel.retired` | 已停用：这一档不参与判定，条件原样保留。 | Retired: this tier takes no part in evaluation; its conditions are kept as they are. |

**五态互斥判据**（前端必须按此分支，不许合并）：

1. 进行中 → `loading`；
2. 失败 → `loadFailed.*`。**绝不允许落成空白**：空白的后果是运营以为「这档没条件」而保存，等于把已存条件当成不存在；
3. 成功且 0 条 → `empty.*`；
4. 功能开关关闭 → `featureOff`（优先级高于 `loadFailed`，否则运营会去查网络）；
5. 无权限 → `readOnly.*`。

`readOnly` 与 `featureOff` 同时成立时显示 `readOnly`（权限是更根本的原因）。

### 3.3 影响确认弹窗三态（本轮最容易写错的地方）

#### 3.3.1 判据表（先定判据，再谈措辞）

后端逐档返回 `{tierId, estimated: int64|null, method, partial: bool, unsupportedConditions: []string}`。
**只有三态，没有第四态**（Q2：新档与老档同构，接口吃草稿，不依赖已保存 id）。
前端**必须**用 `estimated === null || estimated === undefined` 判空态：

| 状态 | 严格判据 | 渲染 | 禁止 |
| --- | --- | --- | --- |
| A 有值 | `typeof estimated === 'number'` | 数字位 `≈ {count} 人`（含 `0`） | 不得走空态；不得隐藏整行 |
| B 无法估算 | `estimated === null \|\| estimated === undefined` | 「无法估算」+ 不可估算条件清单 | **不出现 ≈**；不出现任何数字（尤其不出现「0 人」） |
| C 部分 | A 成立 且 `partial === true` | A 那一行 + 「只算了部分条件…」追加行 | 不得当成 A 的替代态——C 是 A 的**追加行** |

**JS 陷阱（「把 0 写成无法估算」的唯一成因）**：`0` 在 JS 里是 falsy。任何
`estimated ? t('...estimate', {count}) : t('...unestimable')` 的写法都会把 **0 人渲染成「无法估算」**。
必须写成 `estimated === null || estimated === undefined` 才进 B 分支。进 code review 清单与 QA 判据（CP-1）。

#### 3.3.2 文案表

**数字全屏只出现一次**（Q2b）：句子不带 `{n}`，`≈ N 人` 只在数字位渲染。

| 键 | zh-CN | en-US |
| --- | --- | --- |
| `conditions.impact.title` | 保存前确认影响面 | Confirm the impact before saving |
| `conditions.impact.scopeHint` | 按这次要保存的配置估算。 | Estimated from the configuration about to be saved. |
| `conditions.impact.estimate` | ≈ {count} 人 | ≈ {count} people |
| `conditions.impact.meaningHint` | 数的是满足这一档条件的人数，不是会升级或降级的人数。 | This counts people who meet this tier's conditions, not how many will move up or down. |
| `conditions.impact.unestimable` | 无法估算 | Cannot estimate |
| `conditions.impact.nullHint` | 这里没有人数：表示无法估算，不代表 0 人。 | No number here means the audience is unknown, not zero. |
| `conditions.impact.unsupportedTitle` | 以下条件无法参与估算： | These conditions cannot take part in the estimate: |
| `conditions.impact.partialHint` | 只算了部分条件，这个数字是上界，真实人数可能更少。 | Only some conditions were counted, so this is an upper bound; the real number may be smaller. |
| `conditions.impact.loading` | 正在估算影响面… | Estimating the impact… |
| `conditions.impact.loadFailed` | 影响面估算获取失败，仍可继续保存。 | The impact estimate could not be loaded. You can still save. |
| `conditions.impact.row` | {name}：{detail} | {name}: {detail} |
| `conditions.impact.consequence` | 保存后，不再满足条件的用户可能被降级，其已解锁的能力会一并收回。 | After saving, users who no longer qualify may drop a tier, and the abilities it unlocked are withdrawn with it. |
| `conditions.impact.confirm` | 继续保存 | Continue to save |
| `conditions.impact.confirmAnyway` | 仍然保存 | Save anyway |
| `conditions.impact.cancel` | 取消 | Cancel |

**Q1 的措辞纪律**：`meaningHint` 是把「≈ N 人」钉回「满足条件」的那一句，**不可省**。没有它，
`consequence` 里的「可能被降级」会让运营把数字读成降级人数——那是聚合 SQL 算不准的数，不能承诺。
反过来，任何位置都**不得**出现「≈ N 人将升级 / 将降级」。

`method` 依据行（灰度小字，逐 method 一条）：

| method | zh-CN | en-US |
| --- | --- | --- |
| `single_leaf` | 依据：这一档只有一个条件，按它估算。 | Basis: this tier has a single condition, estimated from it. |
| `strictest_leaf` | 依据：多个必须同时满足的条件里，取最严格的一条估算。 | Basis: the strictest of the conditions that must all hold. |
| `strictest_leaf_partial` | 依据：取最严格的一条可估条件，其余条件无法估算。 | Basis: the strictest estimateable condition; the rest cannot be estimated. |
| `sum_of_leaves` | 依据：多个「或」的条件相加，真实人数通常少于这个数。 | Basis: a sum over any-of conditions, so the real audience is usually smaller. |
| `complement_of_leaf` | 依据：按「排除某一条条件」估算。 | Basis: estimated by excluding a single condition. |
| `unsupported_condition` | 依据：这一档有无法估算的条件。 | Basis: this tier contains conditions that cannot be estimated. |

**注意**：这 6 条与徽章 `admin.badges.rule.impact.method.*` 措辞同源但**各存一份**。前端**禁止**在信任等级页
直接 `t('admin.badges.rule.impact.method.single_leaf')`：跨族取词会让徽章改名时静默改掉等级页文案，
且徽章那份主语是「规则」。两份一致是**有意的重复**。

#### 3.3.3 三态结构（供 designer 出几何，文案位置不可变）

```
A 有值
┌ 保存前确认影响面 ─────────────────────────────┐
│ 按这次要保存的配置估算。                        │
│ 「正式用户」：  ≈ 128 人                       │  ← 数字全屏只出现在这一处
│ 数的是满足这一档条件的人数，不是会升级或降级的人数。│
│ 依据：多个必须同时满足的条件里，取最严格的一条。   │
│ 保存后，不再满足条件的用户可能被降级，……          │
│                              [取消] [继续保存]  │
└──────────────────────────────────────────────┘

B 无法估算（不得出现 ≈，不得出现 0）
┌ 保存前确认影响面 ─────────────────────────────┐
│ 按这次要保存的配置估算。                        │
│ 「资深用户」：  无法估算                        │  ← estimated === null
│ 这里没有人数：表示无法估算，不代表 0 人。         │
│ 以下条件无法参与估算：                          │
│ · 插件条件（当前不可用）                        │  ← §2.6 的公开/后台措辞分工
│ · 与指定主题互动                                │
│                              [取消] [仍然保存]  │
└──────────────────────────────────────────────┘

C 部分（= A 那一行 + 追加两行，不是新页面）
│ 「资深用户」：  ≈ 64 人                        │
│ 只算了部分条件，这个数字是上界，真实人数可能更少。  │
│ 以下条件无法参与估算： · 连续访问                │
```

新建档位与老档位**版面完全一致**（Q2）：有三态之一就渲染，没有「保存后才能估算」这种东西。

`unsupportedConditions` 装的是**条件 key**（如 `fact.count`）。展示时用条件目录把 key 换成 §2.3 的中文名；
目录不可用时回落成「{key}」——这是**后台面**，允许（公开面才禁止，见 §2.6）。
>5 条时的折叠收口在 UI 规范文档；本文件只管文案：折叠条的 `{count}` 与 `conditions.toggle.more` 同一写法。

#### 3.3.4 三态 QA 判据（可机器断言）

| ID | 断言 | 判据 |
| --- | --- | --- |
| CP-1 | `estimated: 0` 渲染 A 态 | DOM 出现「≈ 0 人」，且弹窗文本**不出现**「无法估算」 |
| CP-2 | `estimated: null` 渲染 B 态 | DOM 出现「无法估算」，且 `innerText.includes('≈') === false` |
| CP-3 | `partial: true` 时两行都在 | 同时命中 `estimate` 与 `partialHint` |
| CP-4 | **新档也必须估算**（Q2） | 新建档的弹窗出现 A/B/C 之一；弹窗 `innerText` **不含**「保存后才能估算」 |
| CP-5 | 数字只出现一次（Q2b） | 弹窗内 `≈` 出现次数 ≤ 1，且 `meaningHint` 文本里零数字 |
| CP-6 | 无内部词 | `innerText` 不含 `cmp` / `params` / `children` / `AST` / `ruleengine` |

### 3.4 保存确认

它就是 3.3 弹窗里的 `conditions.impact.consequence` 那一行（两步确认 = 弹窗 + `继续保存`），
**不新造第二个键**。若后续要独立第二步（如影响面超阈值才二次确认），复用同一个键。

| 键 | zh-CN | en-US |
| --- | --- | --- |
| `conditions.impact.consequence` | 保存后，不再满足条件的用户可能被降级，其已解锁的能力会一并收回。 | After saving, users who no longer qualify may drop a tier, and the abilities it unlocked are withdrawn with it. |

### 3.5 规则失效的只读提示（三种，修法不同所以文案必须不同）

三种的**运行时后果完全不同**，文案必须分别说清「会怎样」，否则运营会把三种当成同一类问题：

| 类型 | 触发 | 真实后果（证据） | 运营要做什么 |
| --- | --- | --- | --- |
| 引用对象不存在 | 分类/标签/角色/provider/徽章/主题被删 | 该条件返回**确定值** 0/false，条件照常求值，等于「没做过」 | 重新选一项 |
| 条件不可用 | factKey 没有被任何启用插件声明 | `Conditions/resolve_facts.go` 返 **error → unknown → 该档不成立**（fail-closed） | 重新启用插件，或删掉这条条件 |
| 条件设置损坏 | 该档 `rule` 无法解码 | 整份配置**未解析** → 已挣得集合为空、限额回落（`Models/TrustLevels/resolver.go:12,64-66`） | 修配置或回滚；**这一档所有人都不成立** |

| 键 | zh-CN | en-US |
| --- | --- | --- |
| `conditions.health.title` | 这条条件现在不生效 | This condition is not in effect |
| `conditions.health.missingRef` | 引用的对象已被删除，这条按「没有做过」算。请重新选一项。 | What it points at was deleted, so it counts as "not done". Choose it again. |
| `conditions.health.missingRefTopic` | 引用的主题已被删除，这条按「没有做过」算。请重新选一条主题。 | The referenced topic was deleted, so it counts as "not done". Pick another topic. |
| `conditions.health.missingRefCategory` | 引用的分类已被删除，这条按「没有做过」算。请重新选一个分类。 | The referenced category was deleted, so it counts as "not done". Pick another category. |
| `conditions.health.missingRefTag` | 引用的标签已被删除，这条按「没有做过」算。请重新选一个标签。 | The referenced tag was deleted, so it counts as "not done". Pick another tag. |
| `conditions.health.missingRefRole` | 引用的角色已被删除，这条按「没有做过」算。请重新选一个角色。 | The referenced role was deleted, so it counts as "not done". Pick another role. |
| `conditions.health.missingRefProvider` | 引用的登录方式已被删除，这条按「没有做过」算。请重新选一个登录方式。 | The referenced sign-in provider was deleted, so it counts as "not done". Pick another one. |
| `conditions.health.missingRefBadge` | 引用的徽章已被删除，这条按「没有做过」算。请重新选一枚徽章。 | The referenced badge was deleted, so it counts as "not done". Pick another badge. |
| `conditions.health.unavailable` | 这条条件当前无法判定（声明它的插件已停用），**这一档所有人都不成立**。请重新启用插件，或删除这条条件。 | This condition cannot be decided (the plugin that declares it is off), so **nobody qualifies for this tier**. Re-enable the plugin or delete the condition. |
| `conditions.health.corrupt` | 这一档的条件设置已损坏，读不出来：所有人都会按未解析处理（不升档、解锁一并失效）。请修正配置或恢复推荐默认。 | This tier's conditions are corrupt and cannot be read: everyone is treated as unresolved (no tier, no unlocks). Fix it or restore the defaults. |
| `conditions.health.badge` | 有失效项 | Needs attention |
| `conditions.health.badgeAria` | {count} 项条件失效 | {count} condition(s) need attention |

措辞理由：`unavailable` 与 `corrupt` 都**直说后果**（「所有人都不成立」/「所有人都会按未解析处理」），
因为这是 fail-closed 的真实行为；含糊成「暂不可用」会让运营以为只是显示问题而不去修。
反过来 `missingRef` 必须说「按没有做过算」——它不是故障，是一个确定的判定结果。

---

## 4. i18n 键路径清单

### 4.1 只有两个族

| 族 | 用途 | 证据 | 状态 |
| --- | --- | --- | --- |
| `admin.forum.trustLevels.*` | 后台信任等级页全部文案 | `zh-CN.json:5684`（243 叶键） | **唯一后台族** |
| `profile.trustLevel.*` | 资料页信任等级卡 | 21 叶键 | **唯一公开族** |
| `admin.settings.trustLevels.*` | — | 测试断言必须 `undefined` | **不得新增**（C1） |

### 4.2 键表（省略公共前缀 `admin.forum.trustLevels.`）

| 键路径 | 状态 | 备注 |
| --- | --- | --- |
| `conditions.toggle.more` | 新增 | §3.1 |
| `conditions.toggle.add` | 新增 | 空态用 |
| `conditions.toggle.ariaLabel` | 新增 | 仅图标形态 |
| `conditions.toggle.ariaLabelEmpty` | 新增 | 仅图标形态 |
| `conditions.panel.title` / `.description` / `.loading` | 新增 | §3.2 |
| `conditions.panel.empty.title` / `.description` | 新增 | §3.2 |
| `conditions.panel.loadFailed.title` / `.description` / `.reason` / `.retry` | 新增 | §3.2，**必须有消费者** |
| `conditions.panel.readOnly.title` / `.description` | 新增 | §3.2 |
| `conditions.panel.featureOff` | 新增 | §3.2 |
| `conditions.panel.retired` | 新增 | §3.2 |
| `conditions.impact.title` | 新增 | §3.3 |
| `conditions.impact.scopeHint` | 新增 | 与徽章那句**不同义**（C3） |
| `conditions.impact.estimate` | 新增 | 唯一带 `≈` 的键 |
| `conditions.impact.meaningHint` | 新增 | **Q1 必需**，不可省 |
| `conditions.impact.unestimable` | 新增 | 不含数字 |
| `conditions.impact.nullHint` / `.unsupportedTitle` / `.partialHint` | 新增 | — |
| `conditions.impact.loading` / `.loadFailed` | 新增 | — |
| `conditions.impact.row` / `.consequence` | 新增 | §3.3 / §3.4 |
| `conditions.impact.confirm` / `.confirmAnyway` / `.cancel` | 新增 | — |
| `conditions.impact.method.*`（6 条） | 新增 | 与徽章同名不同键，禁止跨族取词 |
| `conditions.fact.undeclared` / `.undeclaredHint` | 新增 | §2.6 后台面 |
| `conditions.health.title` / `.missingRef` / `.missingRef{Topic,Category,Tag,Role,Provider,Badge}` / `.unavailable` / `.corrupt` / `.badge` / `.badgeAria` | 新增 | §3.5 |
| `errors.code.rule_too_complex` | 新增 | §1，**只在 SPECIFIC 桶** |
| `errors.code.rule_unknown_condition` | 新增 | 同上 |
| `errors.code.rule_shape_invalid` | 新增 | 同上 |
| `errors.code.rule_reference_invalid` | 新增 | 同上 |
| `errors.code.rule_condition_not_available` | 新增 | 同上 |
| `errors.code.earnable_not_declared` | 新增 | §1.1 默认（`undeclared`）支 |
| `errors.code.earnable_not_declared_plugin_disabled` | 新增 | §1.1 变体键，**不是第 40 条码**，不进 `BACKEND_CODES` |
| `earnable.plugin.title` / `.description` / `.source` / `.highRisk` | 新增 | §5.1 |
| `earnable.inactive.title` / `.hint` / `.aria` | 新增 | §5.2，`inactive` 态专用（DTO 同名直连，D22） |
| `earnable.orphan.title` / `.description` | 复用 | 既有键，**一字不动**，继续只服务 `unknown` 态（§5.2 为何不叫 orphan） |
| `sections.thresholds.description` | 复用 | 不新增第二份分区说明 |
| `zeroUnlimited` | 复用 | 但**不得**用于条件面板（它是数值阈值语义，条件里 0 是合法阈值） |
| `errors.tierTitle` | 复用 | 档位级错误抬头，6 条新码共用 |
| `errors.field.requirements` | 复用 | 新码若归因到阈值字段 |
| `conditions.catalog.*`（条件名 / 说明） | **不得新增** | 条件目录由服务端下发（`registry.go:76-78` 写明为何不进 locale）。前端抄一份，第一次加条件就是两套口径 |
| `profile.trustLevel.conditions.*` | **不得新增** | 公开句子由服务端 `Phrase` 下发（D15）；资料页只渲染字符串，加一份必然是死键 |
| `profile.trustLevel.metric.*` | 只能 6 键 | `trustLevels.test.ts:853-854` 用 `toEqual` 钉死集合 |
| `admin.badges.rule.impact.method.*` | 复用者禁止 | 信任等级页**不得** `t()` 徽章族键 |

### 4.3 会红的断言（逐条列清）

| 位置 | 断言 | 触发红的写法 |
| --- | --- | --- |
| `trustLevels.test.ts:594-604` | `BACKEND_CODES` **39**、`SPECIFIC` **21**、`CONFIG_BUCKET` 18、并集 39、两桶不相交 | 只加 locale 不加 `BACKEND_CODES`；或加了码不进桶 |
| `trustLevels.test.ts:606-613` | 每条 SPECIFIC 码在**两份** locale 都有 `errors.code.<code>` | 只写 zh-CN；或把 `earnable_not_declared` 漏进 SPECIFIC |
| `trustLevels.test.ts:615-643` | 桶内码在 locale 里**必须没有**专属文案 | 给桶内码顺手加一句「更友好」的文案 → **红**（这就是 C2 说的「多出来也判红」的真实落点） |
| `trustLevels.test.ts:803-804` | `section.metric` 键集合 `toEqual` 六项 | 给 metric 加第七项 |
| `trustLevels.test.ts:833-839` | `admin.settings.trustLevels` 与 `admin.settings.tabs.trustLevels` 必须 `undefined` | 把新键挂到 `admin.settings.*`（C1 的坑） |
| `adminBadgesI18n.test.ts:169-176` | `admin.badges` 族 usage 双向（多出键即红）+ zh/en 键集完全相等 | 往 `admin.badges.*` 加本域要用的键 |

结论：**`admin.forum.trustLevels` 的 `conditions.*` 新键没有自动的「未使用即红」护栏**（C2）。
每个新增键必须在本 PR 内有消费者（组件里真的 `t()` 它），否则留下死键；reviewer 按 §4.2 逐键核对，不能指望测试。
唯一的例外是 `errors.code.earnable_not_declared_plugin_disabled`——它由 `useTrustLevelSaveErrors.ts` 的变体分支消费。

---

## 5. 插件权限授予的运营语义

### 5.1 勾选时的风险提示

运行语义（M4.2）：插件声明 `trustLevelEarnable: true` 的权限键可进核心白名单，由运营在「能力解锁」矩阵里勾给某档；
勾选 = **该档及以上**新增解锁。三个风险点都不是技术风险，是**谁能拿到能力**的产品风险：

1. 勾了不等于立刻生效——默认用户组已授予该键时，用户实际来源仍是用户组（既有 `earnable.baseline.*` 已覆盖，**复用**）；
2. 插件停用后勾选失效，**降级会收回**（§3.4）；
3. 插件声明的可能是高风险能力（导出 / 审核 / 委派），运营需要看到来源插件与键。

| 键 | zh-CN | en-US |
| --- | --- | --- |
| `earnable.plugin.title` | 来自插件的权限（{count}） | Permissions from plugins ({count}) |
| `earnable.plugin.description` | 这些键由插件声明可被等级授予。勾选后，该档及以上的用户会获得这项能力；插件停用后勾选失效，用户会被收回。 | Plugins declared these keys as grantable by tier. Checking one gives the ability to that level and above; if the plugin is turned off, the grant stops applying and users lose it. |
| `earnable.plugin.source` | 来自插件：{plugin} | From plugin: {plugin} |
| `earnable.plugin.highRisk` | 这项能力涉及{scope}，请确认只授予真正需要的档位。 | This ability covers {scope}; grant it only to the tiers that truly need it. |

`highRisk` 的 `{scope}` 取 manifest 权限的 `description`（`ManifestPermissionDefinition.Description`，
`Support/ExtensionManifest/v3_platform_types.go`）。**该字段为空就不渲染这一行**——不要造一句「涉及未知范围」。

### 5.2 插件停用后，已保存的授予配置显示什么（Q4 / D22）

**必须显示「失效」，所以后端要有状态位。** 理由不是审美，是信息不足：后台读面现在只下发
`eligiblePermissions []string`（`Http/Controllers/TrustLevels/dto.go:130`）。插件停用后其键从白名单消失，
前端看到的现象与既有「孤儿键」`earnable.orphan.*` **完全一样**，但两者修法相反：

| 现象 | `unknown`（从未被声明） | `inactive`（扩展停用或卸载） |
| --- | --- | --- |
| 保存时 | 后端拒绝并**移除**该键（既有行为） | 后端**保留**（惰性失效，M4.2 的刻意设计） |
| 修法 | 换个键 | **重新启用插件** |
| 用户侧 | 无影响（本来也没生效） | **能力被收回** |

不区分，运营会按「孤儿」的处理方式（删掉勾选）去处理一次临时停用，等插件恢复时配置已被自己删掉。

DTO（字段名可议，三态语义不可议）：

```json
"eligiblePermissions": ["topic.create", "..."],
"earnablePermissionStates": [
  {"key": "acme.export", "state": "inactive", "plugin": "acme-crm"},
  {"key": "acme.audit",  "state": "valid",    "plugin": "acme-crm"},
  {"key": "acme.legacy", "state": "unknown",  "plugin": ""}
]
```

| `state` | 含义 | UI | 文案键 |
| --- | --- | --- | --- |
| `valid` | 声明它的扩展在启用中 | 正常可选 | — |
| `inactive` | 扩展已停用或卸载 | 勾选保留、标「失效」、仍可保存 | `earnable.inactive.title` / `.hint` / `.aria`（同词直连） |
| `unknown` | 从未被任何扩展声明 | 按既有孤儿处理：保存时移除 | 既有 `earnable.orphan.title` / `.description`（**复用**） |

| 键 | zh-CN | en-US |
| --- | --- | --- |
| `earnable.inactive.title` | 插件已停用 | Plugin disabled |
| `earnable.inactive.hint` | 声明这项能力的插件当前已停用或已卸载：勾选保留，但**不生效**。重新启用插件后自动恢复。 | The plugin declaring this ability is off or uninstalled: the check is kept but **does not apply**. It resumes when the plugin is re-enabled. |
| `earnable.inactive.aria` | {permission}：插件 {plugin} 已停用，勾选保留但不生效 | {permission}: plugin {plugin} is off, the check is kept but does not apply |

#### 为什么这个态不叫 `orphan`（D22，已写进计划 §4.5）

`orphan` 一词在本域**已经名花有主**：既有 `earnable.orphan.*` 的语义是「键不在白名单、保存时移除」，
正好等于本 DTO 的 `unknown`。若把「扩展停用/卸载」也命名为 `orphan`，就会出现一个词指两件事，
而这两件事的修法完全相反（换键 vs 启用插件）。靠映射层弥合是不够的——映射层只有现在这个作者知道，
下一个人看到 `state: "orphan"` 会自然去渲染 `earnable.orphan.*`，于是 `unknown` 的提示被写成「等插件启用」，
**修法彻底错**。所以从源头消掉这个词：DTO 用 `inactive`。

落地口径：

- DTO 枚举 = `valid` / `inactive` / `unknown`，**契约里不再出现 `orphan` 作为 state 值**；
- i18n 侧 `earnable.orphan.*` **保持既有语义、一字不动**（已有消费者：`SFAdminTrustLevelEarnableMatrix.vue`），
  继续只服务 `unknown`；新态同词直连 `earnable.inactive.*`。

### 5.3 写路径错误码（Q3）

拒绝时用 `earnable_not_declared`，按 §1.1 分两种成因给文案。**不复用** `earnable_not_whitelisted`：
后者语义是「不在白名单」，修法是「换个键」；前者要区分「等插件启用」与「删掉这个键」。
这与徽章把 `ErrBadgeRuleModeConflict` 与 `ErrBadgeRuleInvalid` 分开是同一个理由。

---

## 6. 裁决归档（原待确认项，已全部关闭）

| # | 原问题 | 裁决 | 本文落点 |
| --- | --- | --- | --- |
| Q1 | 影响面是「满足规则人数」还是「等级变化人数」 | **满足该档条件的估算上界**；文案不得承诺升降人数（聚合 SQL 算不准升级数，比徽章更保守是故意的） | §3.3.2 `meaningHint` |
| Q2 | 新档要不要估算 | **要**。POST 草稿体不依赖已保存 id；v1 的 D 态与 `newTierHint` 作废 | §3.3.1、CP-4 |
| Q2b | 弹窗里数字出现几次 | **一次**。句子不带 `{n}`，`≈ N 人` 只在数字位 | §3.3.2、CP-5 |
| Q3 | 第 6 条码 | **加** `earnable_not_declared`（specific 桶），33 → **39**，断言 **39 / 21 / 18** | §1.1、§4.2、§4.3、§7 |
| Q4 | 是否加失效状态位 | **加**，三态 `valid` / `inactive` / `unknown`（D22 改名：不用 `orphan`，避免与既有 i18n 键同词不同义） | §5.2 |
| Q5 | `identity.profile` 是否含 `location` | **含**，四值 | §2.4 |
| Q6 | 公开兜底名能否裸拼 factKey | **不能**；公开「插件条件（当前不可用）」，后台显示 factKey + 「未声明」 | §2.6 |

---

## 7. 验收：怎样一眼判断过没过

| 检查项 | 判据 | 责任人 |
| --- | --- | --- |
| 6 条错误码落地 | 两份 locale 的 `errors.code.rule_*` 5 键 + `earnable_not_declared` 及其 `_plugin_disabled` 变体存在且与 §1 逐字一致；`BACKEND_CODES` **39** / `SPECIFIC` **21** / `CONFIG_BUCKET` 18 | frontend + backend |
| 14 条条件文案落地 | `registry.go` 14 条 `Describe*` 与 §2.3 逐字一致；`identity.profile` 四值枚举在列；条数 13→14 两处（`requirement_text_test.go:168`、`conditions_postgres_integration_test.go:264`） | backend |
| 公开句子只增不改 | `TestPhraseGolden` 13 条老期望句**零 diff**，新增 `identity.profile` 一项；`TestFactPhraseDoesNotRepeatTotal` 绿 | backend |
| fact 兜底不泄 key | 未声明 factKey 时公开响应句子里**不出现** `acme.` 这类命名空间串；后台显示 factKey + 「未声明」 | backend + QA |
| 折叠开关 | 有条件 →「更多条件（{count}）」；无条件 →「添加条件」 | frontend |
| 面板失败态 | 模拟目录接口 500：显示「条件目录加载失败，规则暂不可编辑」，**不是空白** | frontend + QA |
| 三态互斥 | CP-1…CP-6 全绿；重点 CP-1（0 走 A 态）、CP-2（null 无 `≈`）、**CP-4（新档也估算）**、CP-5（数字只出现一次） | QA |
| 失效提示分三种 | 三种场景文案不同；`unavailable` 明确写「这一档所有人都不成立」 | frontend |
| 三态状态位 | `earnablePermissionStates` 三值齐全且 **enum = `valid`/`inactive`/`unknown`**（契约里无 `orphan`）；`inactive` 渲染 `earnable.inactive.*`，`unknown` 渲染既有 `earnable.orphan.*` | backend + frontend |
| 无第三键族 | `rg "admin\.settings\.trustLevels" apps/web` → 0 | reviewer |
| 无死键 | §4.2 每个「新增」键在 `apps/web/app` 里都有 `t()` 消费者（例外仅 `_plugin_disabled` 变体） | reviewer |
| 无内部词 | 公开面与后台 `innerText` 均无 `cmp` / `params` / `children` / `AST` | QA |
| 无 emoji 图标 | 新增组件零 emoji 字符 | reviewer |

---

## 8. 附：本轮实测到的落点（供实施直接引用）

| 事项 | 落点 |
| --- | --- |
| 13 条条件定义 | `apps/api/app/Models/Conditions/registry.go:214-351` |
| `Definition` 结构（含 `Phrase` 与「必须写句子」注释） | 同文件 `:79-96` |
| 「条件文案为什么在 Go 而不在 locale」 | 同文件 `:76-78` |
| 句子实现与三条硬约束 | `Models/Conditions/phrases.go:11-19`、`:24`（`PhraseFunc`） |
| 引用名回落（`fallbackFact` 现为「插件事实计数」/ "a plugin fact"） | `Models/Conditions/refnames.go:40-42` |
| 未声明 factKey 走泛称回落的访问器 | 同文件 `:182-191` |
| `fact.count` 英文模板 `"%s" total %s` | `Models/Conditions/phrases.go:263-268` |
| 「每条件必须有句子」注册锁 | `apps/api/app/Models/Badges/requirement_text_test.go:166`、`:168`（13） |
| 句子逐字 golden | 同文件 `:51`（`phraseSamples`）、`:197`（`TestPhraseGolden`） |
| `total` 不重复的拼接病测试 | 同文件 `:445`（`TestFactPhraseDoesNotRepeatTotal`） |
| 目录条数 13 的另一处硬编码 | `Models/Conditions/conditions_postgres_integration_test.go:264-265` |
| 错误码常量 + 稳定枚举 | `Models/TrustLevels/config.go:66`、`:108-144`（`ConfigErrorReasonCodes()` 在 `:146-151`） |
| `FieldError`（rank / tierId / field / code / params / reason） | 同文件 `:224-243` |
| 前端码桶（SPECIFIC / CONFIG_BUCKET） | `apps/web/app/composables/admin/forum/trustLevels/useTrustLevelSaveErrors.ts:138-177` |
| 前端桶位断言 | `apps/web/tests/admin/forum/trustLevels.test.ts:594-643` |
| 徽章影响弹窗三态既有文案（措辞同源） | `admin.badges.rule.impact.*` |
| `unsupportedConditions` 返回条件 key | `Models/Badges/rule_impact.go:72-143`（`node.Type` 入 `unsupported`） |
| 可估 / 不可估清单 | 同文件 `:145-201`：可估 = `activity.count`(仅 gte)、`content.pinned`、`content.hidden`、`identity.email_verified`、`identity.avatar`、`identity.role`、`badge.owned`、`identity.account_age`；不可估（`:200` 合并 case）= `object.authoring`、`object.engagement`、`identity.oauth_link`、`activity.streak`、`fact.count` |
| 后台读面白名单字段 | `apps/api/app/Http/Controllers/TrustLevels/dto.go:130`（`eligiblePermissions []string`） |
| `user_profiles` 四列 | `apps/api/database/migrations/202607070004_user_profiles.sql:5-8` |
| 所在地的既有中文名 | `admin.users.location` =「所在地」 |
| 插件权限声明的 label / description | `apps/api/app/Support/ExtensionManifest/v3_platform_types.go`（`ManifestPermissionDefinition`） |
| 既有孤儿键消费者 | `apps/web/app/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelEarnableMatrix.vue` |
