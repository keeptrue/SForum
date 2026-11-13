# 决策：资料页/预览卡的「佩戴中徽章」取自 badges 的批量读口

日期：2026-11-13
状态：accepted
上游：`decisions/2026-11-13-badge-worn-display.md`（`top_badge_id` 语义 = 当前展示的徽章）、
`decisions/2026-11-13-badge-appearance-single-source.md`（徽章外观的唯一真源与「不许第二条投影路径」）

## 背景

帖子/评论旁的 chip 由 Forum 读 SQL 的 `topBadgeSelectSQL`/`topBadgeJoinSQL` 投影
（`users.top_badge_id`，佩戴优先、否则排序第一）。但公开资料页聚合 `profile.PublicProfile`
**没有**任何徽章字段：`/u/{username}` 头部与用户预览卡（同一个 `GET /profiles/{username}`）
都拿不到「这个人现在戴的是哪枚」，于是资料页与帖子旁显示不一致——同一页面上一个人有两套身份装饰。

## 裁决

### D1 复用 `badges.TopBadgesFor`，适配器留在装配层

`Profile.Service` 只认一个窄接口：

```go
type TopBadgeReader interface {
    TopBadgesFor(ctx context.Context, userIDs []int64) (map[int64]forum.BadgeRef, error)
}
func (s *Service) WithTopBadgeReader(reader TopBadgeReader) *Service
```

实现是 `bootstrap/profile_badge_wiring.go` 的 `profileTopBadges{store: *badges.PostgresStore}`：
把 `badges.BadgeRef` 的 `Appearance{NameZh,NameEn,Color}` 映射成 `forum.BadgeRef` 的
`TierName{zh,en}` + `TierColor`（前端 `ForumBadgeRef` 与 chip 组件已按这个形状写死）。

与 `profileActivityCounters`（`bootstrap/profile_activity_wiring.go`）同一条纪律：
**Profile 与 Badges 之间没有直接 import，Profile 不写第二份徽章 SQL。**
两个装配点（API 的 `profileProvider` 与 PageViewModels 的 `pageProfileService`）都接同一个适配器，
否则两个入口会给出两套身份装饰。

### D2 顺手修掉 `TopBadgesFor` 的等级过滤

它原来只判 `badges.enabled`，`LEFT JOIN badge_tiers AS tier_def` 不过滤等级启用。
既然它从此有了生产消费者，口径必须与 chip 的 `topBadgeJoinSQL` 一致：
改成 `JOIN badge_tiers AS tier_def ON tier_def.key = badges.tier AND tier_def.enabled`。

停用一档等价于这一档不存在（`badge-worn-display` D5 的失效窗口）。不修的话，运营停档 +
重算尚未跑的那段时间里，资料页会挂着一枚「帖子旁不显示、佩戴面板说已回落」的徽章。

**不是为了修 NULL 扫描**：`badgeTierColumnsSQL` 四列都是 `COALESCE(...,'')`，旧 SQL 本来就不会把
NULL 扫进 `string`；改成 INNER JOIN 的唯一目的是可见性口径。

### D3 失败姿态：fail-soft，只 `slog.Warn`

徽章读失败省略 `topBadge`，不让资料页 500。理由：获赞/访问天数是**数字**，读失败会给假事实
（`K-04` 的原始症状），所以那两个计数失败即整页失败；徽章是**装饰**，缺它只是少一枚 chip，
不会让页面说错话。装配漏接的兜底不是读路径报错，而是 `bootstrap/profile_badge_wiring_test.go`
（真库断言适配器返回同一枚且外观非空），与 `profile_activity_wiring_test.go` 同一思路。

### D4 预览卡优先取评论载荷

`SFComment` 已把 `comment.topBadge` 用在作者行上，同一份值作为 prop 传进 `SFCommentUserPreview`：
用户点开的那颗 chip 与卡片里显示的必须是同一枚（评论读模型有 20–30s 缓存，资料接口 `no-store`，
两者可能差一个 TTL）。`@提及` 没有载荷，回落 `profile.topBadge`。

卡片里的 chip **不传 `username`**：名字本身已经是 `<NuxtLink>`，再套一层 `<a>` 是非法 HTML
（与 `SFCommentAuthorRow` 在预览按钮内的约束同源）。

### D5 本人页跟随本地佩戴状态

`useBadgeWorn` 是全局 `useState`。本人在徽章 tab 里刚换过佩戴时，头部 chip 立即跟随
（`mode=hidden` 则消失），不要求刷新；他人页只认接口字段（没有本地状态可跟）。
`worn.loaded` 为假时一律回落 `profile.topBadge`，所以未进过徽章 tab 的页面行为不变。

**全局态必须带账号归属**（审查期补的硬约束）：登录/登出都是客户端跳转（`router.push` /
`setUser`），不整页刷新，`useState` 会带着上一个账号的 `loaded`/`wornSlug` 活下来。所以
`useBadgeWorn` 记录 `ownerId`（每次成功读写后写当前 `viewerId`），`ensureLoaded()` 只在
`loaded && ownerId === viewerId` 时短路，消费方用 `loadedFor(userId)` 判定。不这么做的话：
上一个账号选了「不佩戴」→ 新账号头部永远空着；上一个账号戴过人人持有的 slug → 新账号头部
显示上一账号那枚。徽章 tab 本来就有这个潜在问题，本刀把它扩到了头部，所以一并修掉。

裁决逻辑抽在 `apps/web/app/utils/profile/profileHeaderBadge.ts` 的 `resolveHeaderBadge`（纯函数），
七条优先级各带理由、由表驱动测试覆盖；页面只负责把来源凑齐。

### D6 明确不做

- 右栏 `SFProfileRightRail` 的身份小字保持 `@name · UID n`，不叠 chip（同一屏两处 chip 会抢注意力）。
- 主题 ViewModel（`themecompiler.ProfilePageViewModel`）不加字段：内置主题只挂 `<sf-profile-page>`
  Host 岛，chip 在岛内渲染，第三方主题挂同一个岛即可获得；加 ViewModel 字段等于为一个装饰改主题契约。
- 不做 `features.badges` 门控：帖子旁 chip 现状也是数据驱动、不由开关裁剪，资料页跟随同一行为。

## 被否方案

| 方案 | 为什么否 |
| --- | --- |
| Profile store 自己 JOIN `badges`/`badge_tiers` 投影徽章列 | 第三份可见性口径（`enabled` + 等级 `enabled`），漂移后不会报错，只会让同一个人在两处显示不同徽章 |
| 前端读 `/profiles/{u}/badges` 猜哪枚在戴 | 该接口只回答「获得了哪些」，不含展示位；猜「最高级」会在手动佩戴时错，且多一次请求 |
| Forum 新增「按用户取当前徽章」读口 | 论坛读模型是列表向（列尾追加七列的投影约定），单用户读口要复制别名与 JOIN 约定，收益只有省一次函数调用 |
| 徽章读失败即 500 | 装饰性字段拖垮整页；且失败窗口内用户完全看不到资料页，比少一枚 chip 更糟 |

## 后果与披露

- 每次公开资料页多一次 `users` 主键查询（`TopBadgesFor` 是批量签名，未来预览卡批量场景可直接复用）。
- 佩戴/停档窗口内资料页 chip 与帖子 chip 可能差一个评论缓存 TTL（20–30s），属已知口径差异。
- `TopBadgesFor` 从「无生产调用方」变成有消费者，模块笔记里那条待删记录随之作废。
