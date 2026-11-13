# 2026-11-13 徽章三件事：授予通知补全 + 自由佩戴 + 进度口径统一

Status: **landed（未提交，工作树；`HEAD = 33fcc7dc9`）**。三件都是补上一刀留下的洞：通知只接了自动
评估一条路径、`top_badge_id` 没有表达用户选择的位、进度条读的是 legacy 两列。模块笔记
`../modules/badges.md`（「进度口径」「授予通知与庆祝」+ Data Model）、`../modules/notifications.md`。

## Changed

**A. 授予通知覆盖四条路径**：领域侧端口 `GrantedBadge{ID,Slug,Name}` / `GrantNotifier` /
`SetGrantNotifier` / `notifyGranted`（失败只 `slog.Warn`，不回滚授予）；`Grant`、`BatchGrant`
（**只发 `grantedUsers`**）、`Recompute`（`recomputeNotifyLimit = 200`，超限**整体不发** + warn）接上，
自动 job 那支本来就有。`BadgeGrantedEvent.BadgeID` + `TargetType="badge"`（`<= 0` 不写目标），
`ResolveNotificationBadgeTarget` fail-closed 三重校验 → `/u/{username}?tab=badges`，
`bootstrap/notification_adapter.go` 按 `targetType` 分流。去重键仍 `badge:{slug}`，「按用户聚合」
只减少出口调用、**投影仍一枚一行**。

**B. 站内庆祝（展示层，插件可复用）**：载体是 `presentation.celebration` jsonb（**没有新列**，
插件用 `notificationTypes[].celebration` 声明）、**没有新增 Host 命令**；迁移
`202611130002_notification_celebrations.sql`（打标 + `user_celebration_cursors`）；首访
`initialized=false` + 空 items + 种子游标（**不写游标行**）、7 天窗 + `id > cursor` + 目标不可见整条丢弃 +
`GREATEST` ack；前端 `SFCelebrationHost.vue`（固定左下）+ `useCelebrations` + `celebrationPresentation`
（i18n 命中 → 服务端兜底 → 吃掉占位符三态）。

**C. 自由佩戴**：`users.top_badge_manual`（`top_badge_id` 语义改为「当前展示的徽章」）；
`wear_store.go` 的 `SetWornBadge`（CTE 单语句校验）/ `ClearWornBadge`（先清标记再重算）/ `DisplayBadge`
（`ROW_NUMBER()` 按 `topBadgeOrderSQL`）；重算 manual 有效则让路、失效则回落并清标记；
`GET/PUT/DELETE /badges/worn`（login、`private, no-store`、三端点同一 `BadgeDisplay`、
`forum.badge_not_owned` 422 / `forum.badge_worn_body_invalid` 400、静态段先于参数段）。顺带修掉两个
既有缺陷：集成 fixture 缺列（该包原本就红）、`DisplayBadge` 的 NULL Scan 与判别列选错。

**D. 进度口径统一**：`PublicBadge.Measure`（`json:"-"`）+ `measureForRule`（只认「单叶 + count + `gte` +
value>0」）；`*PostgresStore.MeasureProgress` 一次 `Prepare`（每类条件一条 SQL）+ fail-soft；
`ListUserBadges` 不再读 `ActivityCounters`（`progressFor` 已删除）且一律覆盖 `Progress`；
目录 DTO 新增 `current`/`threshold`（omitempty、匿名不查库、仍 `private, no-store`）——**这修订了 D37**。

## Decisions

- `../decisions/2026-11-13-badge-notification-and-celebration.md`（D1–D11 + T1–T7）
- `../decisions/2026-11-13-badge-worn-display.md`（D1–D9 + F1/F2 两个既有缺陷 + T1–T5）
- `../decisions/2026-11-13-badge-progress-from-rule.md`（D1–D5 + **「显式修订 D37」**小节）

三条都不改写 D35–D38 原文；`2026-11-11-badges-public-catalog-and-series-closeout.md` 只多一行索引。

## Next

- **提交前**：`contracts/openapi/{paths,schemas}/{badges,notifications}.yaml` 已改，先跑
  `ruby scripts/validate-openapi-refs.rb`。路由 tripwire 已抬到 **433 = 428 + 5**，三处必须同一批：
  `registry_test.go` 计数、`catalog-identities.json` 的 routes、`core_catalog_gen.go`。
- 真机验收：**桌面 1633×810 已实测**（见下「验证」节），**390×844 仍是缺口**（只有 CSS 层核对）。
  审查 **PASS-with-residual**（`../reports/2026-11-13-badge-notify-wear-progress-review.md`）：
  R7 已订正 T7 的断言层级口径，R1 / R2 已记入残留 39 / 40。

## Open Questions

1. 庆祝卡与 toast 的**同时出现**仍未被观察到：几何上浮卡在 `left:16px/bottom:16px`、toast 在右下，
   桌面实测浮卡不挡 toast，但两者同屏时的视觉优先级仍无证据。
2. `Service.activity` 字段已无读路径，保留只为不动构造签名（残留 36）；要不要下一刀删掉。
3. 佩戴的乐观更新没有服务端版本号，两个标签页同时操作时后写胜出（残留 38）。
4. `Recompute` 超 200 条时不发通知只有一条 warn（残留 35），运营侧没有 UI 提示。

## 验证（浏览器实机，lead 实测）

桌面 **1633×810**、登录态站长 `zhuchunshu`（user 1）。逐条记录见
`../reports/2026-11-13-badge-notify-wear-progress-review.md`「Lead 实机 QA 补充」（审查之后补的）。

1. **`/badges` 目录页有进度**：12 枚徽章里未获得的显示进度条 + 数值（常客 4/7、老住户 4/30、
   元老 4/180、初次发帖 0/1…），已获得的两枚（社区贡献金牌、优质解答银牌）**不画条**、只显示「已获得」。
2. **庆祝浮卡真的弹了**：临时手动徽章授予自己 → `notifications` 落行 `id=591`（`type=badge_granted`、
   `target_type=badge`、`target_id=39`、`dedupe_key=badge:qa-celebration-probe`）→ 刷新 `/badges` 后左下角
   出现「获得新徽章 / 你获得了「QA 庆祝探针」徽章。/ 查看」，`role=status` + `aria-live=polite`、
   整卡链到 `/u/zhuchunshu?tab=badges`、带关闭按钮；`left:16px bottom:16px`（不挡右下 toast）、
   宽 `min(22rem, calc(100vw - 32px))`。**`badgeName` 被填进句子** → 主路径不是兜底句。
3. **佩戴全链路**：已获得卡上「佩戴 / 佩戴中」按钮（`aria-pressed` 正确）；点后 `GET /badges/worn` =
   `mode=manual, worn=qa-celebration-probe, upgrade=community-contribution`，顶部出现「你现在可以换成
   「社区贡献」」——不覆盖用户选择、只提示可升级。
4. **失效回落**：撤销授予 + 删除探针徽章后 `users.top_badge_id=11`（社区贡献）、`top_badge_manual=f`。
5. **清理**：徽章回到 12、user 1 通知数回到 36、游标重置回 196；刷新后无庆祝卡。

**仍是缺口**：**390×844 没有截图证据**（当次浏览器无法改视口），只做了 CSS 层核对——浮卡
`min(22rem, calc(100vw - 32px))` 在 390 下 = 352px、左右各 16px 余量。**这不算移动端已验。**
