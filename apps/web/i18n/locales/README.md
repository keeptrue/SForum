# locale 来源标注

`zh-CN.json` / `en-US.json` 是严格 JSON（`@nuxtjs/i18n` 直接解析），**不能写 `//` 注释**，
所以「这条文案是从哪来的」统一记在这里。新增键时若来源不是规范原文，请在此登记一行。

## `badges.*`（2026-11-10 补，徽章那一刀漏掉的整块命名空间）

| 键 | 来源 |
| --- | --- |
| `badges.tier.gold` / `silver` / `bronze` | `knowledge/reports/2026-11-10-badge-visual-spec.md` §5.1 原文（金牌 / 银牌 / 铜牌） |
| `badges.chip.ariaLabel` / `ariaLabelLink` | 同上 §5.1 模板原文 |
| `badges.card.ariaLabel` / `ariaLabelUngranted` / `ariaLabelNotEarned` / `earnedOn` / `notEarned` / `progress` | 同上 §5.1 模板原文 |
| `badges.group.count` | 同上 §8 原文（`{earned} / {total}`） |
| `badges.empty` | **来源：徽章测试的期望字符串**（`tests/forum/profileBadgeTab.test.ts` 断言 `还没有获得徽章`），非规范原文 |
| `badges.loadFailed` | **来源：徽章测试的期望字符串**（同上，断言含句号：`徽章加载失败。`），非规范原文 |
| `badges.retry` | **来源：徽章测试的期望字符串**（同上用例的重试按钮），非规范原文 |

另：`badges.chip.ariaLabelLink` 在 `SFBadgeChip.vue` 里当 `title` 用，而规范写的是「追加到
`aria-label` 末尾」。实现与规范的这处分歧属徽章域 residual，locale 值按实现填了完整句。

## `badges.worn.*`（2026-11-13 佩戴出口收口）

**来源：YG UI 复核，不是规范原文。** 这一族按「按钮文案必须写动作、状态只走 `aria-pressed`」
的口径逐条定的，规范里没有对应表。与上一版的差别：`wearing`（佩戴中）与 `restoreAuto`
（恢复自动展示）已删除——它们是状态词，已佩戴的那颗按钮改成动作「取消佩戴」；自动展示
变成底部常驻出口 `auto`。`unwearSuccess` 改名 `autoSuccess`，因为「已恢复自动展示」只可能
由 `auto` 出口触发，名字必须跟出口对齐。

| 键 | zh-CN | en-US | 出口 |
| --- | --- | --- | --- |
| `badges.worn.wear` | 佩戴 | Wear | 卡片/弹窗，未佩戴 |
| `badges.worn.unwear` | 取消佩戴 | Stop wearing | 卡片/弹窗，已佩戴（= 不佩戴任何徽章，`PUT {hidden:true}`） |
| `badges.worn.auto` | 自动展示徽章 | Show automatically | 底部常驻（`DELETE` 回 auto） |
| `badges.worn.hide` | 不佩戴徽章 | Wear no badge | 底部常驻（`PUT {hidden:true}`） |
| `badges.worn.hiddenEmpty` | 未佩戴徽章 | No badge worn | hidden 态状态行（不回落成自动徽章名） |
| `badges.worn.currentlyWearing` | 当前佩戴：{name} | Currently wearing {name} | manual 态状态行 |
| `badges.worn.hideSuccess` | 已不佩戴任何徽章 | No badge is worn now | 取消佩戴 / 不佩戴的成功 toast |
| `badges.worn.autoSuccess` | 已恢复自动展示徽章 | Badge display is back to automatic | 自动展示的成功 toast |

`wearSuccess`（已佩戴「{name}」）/ `notOwned` / `saveFailed` 沿用旧文案，不在本轮改动范围。

## 信任等级恢复默认披露 `admin.forum.trustLevels.restoreHint`（2026-11-10 改口径）

**来源：lead 裁决（I-23）修正规范 §3.4 原文，不是照抄。** 原文是「恢复默认只重置五档阈值，
不会影响用户已获得的等级」——**这句是错的**：等级是派生值（`LevelOf(activity, thresholds)`，
D4 不落列、不缓存、每请求重算），阈值一改，用户等级立刻按新阈值重判。原措辞会让运营在恢复默认后
收到「用户等级怎么变了」的投诉，然后去查一个不存在的 bug。现文案改成三段式：
只重置阈值与限额 / 等级会按新阈值重新判定 / 已获得的徽章与已发布的内容不受影响。

## `profile.likeReceivedCount` / `profile.daysVisited`（2026-11-10 补）

**来源：从代码推导，不是规范原文。** `SFProfileFeedTab.vue` 的统计条（主题 / 回复 / 参与类别 /
获赞 / 访问天数）直接引用了这两个键，而 HEAD 的 zh-CN / en-US 里都没有它们 —— 页面上因此显示裸键。
D1 规定资料页要显示获赞数与访问天数，所以补键而不是删展示：

| 键 | zh-CN | en-US | 依据 |
| --- | --- | --- | --- |
| `profile.likeReceivedCount` | 获赞 | Likes received | 与 `profile.trustLevel.metric.likeReceived`（获赞 / likes received）同词，取自 D1 的「资料页公开获赞数」 |
| `profile.daysVisited` | 访问天数 | Days visited | 与 `profile.trustLevel.metric.daysVisited` 同词 |

命名沿用同组既有键的 `xxxCount` / 裸名词风格（`topicCount` / `commentCount` / `categoryCount`）。

## `profile.trustLevel.*` / `admin.forum.trustLevels.*`

**2026-11-11 迁族**：信任等级从设置页 tab 提为独立页 `/control-panel/forum/trust-levels`，
键族由设置页前缀整体搬到 `admin.forum.trustLevels.*`（**键名不变**，只换前缀）；
设置页那条 tab 标签键已删除，页内分区标签改用 `admin.forum.trustLevels.tabs.*`。
下文历史记录里写的旧前缀即指这一族。

来源：`knowledge/reports/2026-11-10-trust-level-visual-spec.md` §8.1 / §8.2 原文（D21 终版显示名：
新用户 / 正式用户 / 常驻用户 / 资深用户 / 领袖）。两处**不在**原表里、经 lead 裁决补入：

- `profile.trustLevel.progress.text`（`{current} / {threshold}`）：§1.2 的 DOM 契约要求进度旁有纯数字，
  而规范禁止裸字符串；先例是 `badges.card.progress`。已折进规范 §8.1。
- `profile.trustLevel.metric.likeGiven` / `bookmarkCount` 与后台同名两条：规范原表只有 5 个 metric，
  而 Go `TrustLevels.Metrics` 是 6 个；缺这两条会让「点赞 / 收藏」被判成瓶颈时显示裸键。
- `admin.forum.trustLevels.tl0Hint`（迁族前的设置页同名键）：Go `config.go` 明确拒绝 TL0 出现在 `trust.levels` 里
  （TL0 由 `trust.new_user_*` 决定），所以后台第 0 行只读展示，需要一句说明。
  **不是**规范原文。

**已删除**：`profile.trustLevel.metric.accountAge` 与后台同名键（迁族后即 `admin.forum.trustLevels.metric.accountAge`，同样不存在）。
`account_age_days` 是幻影指标 —— Go `Requirements` 只有 6 个字段（`days_visited` / `topic_count` /
`comment_count` / `like_received_count` / `like_given_count` / `bookmark_count`），逐档账龄（C7）已作废，
只保留 `trust.new_user_days` 一个上限闸。保留这两个键会诱导后台写出 `DisallowUnknownFields` 拒绝的 JSON。
