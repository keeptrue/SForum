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

## `admin.settings.trustLevels.restoreHint`（2026-11-10 改口径）

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

## `profile.trustLevel.*` / `admin.settings.trustLevels.*`

来源：`knowledge/reports/2026-11-10-trust-level-visual-spec.md` §8.1 / §8.2 原文（D21 终版显示名：
新用户 / 正式用户 / 常驻用户 / 资深用户 / 领袖）。两处**不在**原表里、经 lead 裁决补入：

- `profile.trustLevel.progress.text`（`{current} / {threshold}`）：§1.2 的 DOM 契约要求进度旁有纯数字，
  而规范禁止裸字符串；先例是 `badges.card.progress`。已折进规范 §8.1。
- `profile.trustLevel.metric.likeGiven` / `bookmarkCount` 与后台同名两条：规范原表只有 5 个 metric，
  而 Go `TrustLevels.Metrics` 是 6 个；缺这两条会让「点赞 / 收藏」被判成瓶颈时显示裸键。
- `admin.settings.trustLevels.tl0Hint`：Go `config.go` 明确拒绝 TL0 出现在 `trust.levels` 里
  （TL0 由 `trust.new_user_*` 决定），所以后台第 0 行只读展示，需要一句说明。
  **不是**规范原文。

**已删除**：`profile.trustLevel.metric.accountAge` 与 `admin.settings.trustLevels.metric.accountAge`。
`account_age_days` 是幻影指标 —— Go `Requirements` 只有 6 个字段（`days_visited` / `topic_count` /
`comment_count` / `like_received_count` / `like_given_count` / `bookmark_count`），逐档账龄（C7）已作废，
只保留 `trust.new_user_days` 一个上限闸。保留这两个键会诱导后台写出 `DisallowUnknownFields` 拒绝的 JSON。
