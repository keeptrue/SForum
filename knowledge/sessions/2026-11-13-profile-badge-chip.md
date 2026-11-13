# 2026-11-13 资料页 / 预览卡的「佩戴中徽章」

## Changed

- 后端：`PublicProfile.topBadge`（`json:"topBadge,omitempty"`）。数据源 `users.top_badge_id`，
  经 `badges.PostgresStore.TopBadgesFor` + 装配层适配器 `bootstrap/profile_badge_wiring.go`
  转成 `forum.BadgeRef`（`Appearance` → `TierName{zh,en}` + `TierColor`）。`profile.Service` 只认窄接口
  `TopBadgeReader`，Profile 与 Badges 无直接 import、无第二份徽章 SQL。两处装配（`profileProvider`
  与 PageViewModels 的 `pageProfileService`）都接上。
- 后端顺手修：`TopBadgesFor` 的 `LEFT JOIN badge_tiers` → `JOIN ... AND tier_def.enabled`。
  它原来只判 `badges.enabled`，有了消费者后会在「停档 + 重算未跑」窗口挂出一枚 chip 不显示的徽章。
- 前端：`SFProfileShowPage` 头部改 `sf-profile-intro__name-row`（`h1` + chip），
  `identity-meta`（`@handle` + `UID`）原样紧随其后；`SFCommentUserPreview` 新增 `topBadge` prop
  并渲染无链接 chip；`SFComment` 把 `comment.topBadge` 传进卡片。
  后续（同一刀的二段，2026-11-14）：chip 改 `button`，点击打开全局徽章说明弹窗，不再跳 `?tab=badges`。
- 契约：`contracts/openapi/schemas/profile.yaml` 的 `PublicProfile` 增 `topBadge`
  （`$ref ./badges.yaml#/BadgeRef`）；`BadgeRef` 说明补「也用于资料页身份行」。
- 知识库：`modules/badges.md` 新增「资料页展示位」一节、`modules/profile.md` 补身份块三层结构、
  新决策 `decisions/2026-11-13-profile-top-badge-source.md`。

## Decisions

- 复用 `TopBadgesFor`（批量、单列读口），不在 Profile 写第二份 SQL，也不给 Forum 加单用户读口。
- 失败姿态 fail-soft（`slog.Warn` + 省略字段）：徽章是装饰，不让资料页整页 500；
  装配漏接由真库适配器测试兜住。
- 预览卡优先评论载荷（与点开的那颗 chip 一致），`@提及` 回落资料接口；卡片内 chip 不传 username。
- 本人页跟随 `useBadgeWorn` 本地状态（换佩戴即时生效、hidden 立即消失），他人页只认接口字段。
- 不做：右栏身份小字叠 chip、主题 `ProfilePageViewModel` 加字段、`features.badges` 门控。

## Evidence

- `cd apps/api && go build ./...` → BUILD_OK
- `./scripts/test-api-isolated.sh ./app/Models/Profile/ ./app/Models/Badges/ ./bootstrap/` →
  `ok Profile 0.134s` / `ok Badges 9.860s` / `ok bootstrap 26.077s`
- `cd apps/web && bun test` → 2921 pass / 0 fail（279 files）；`bun run typecheck` exit 0
- `node tests/validate-architecture-boundaries.mjs` → passed（2232 files）
- `ruby scripts/validate-openapi-refs.rb` → OK（3378 refs / 65 files）；`node tests/validate-docs.mjs` → passed
- 浏览器（`localhost:3000`，dev API 已热重载）：
  - `/u/zhuchunshu`：`.sf-profile-intro__name-row .sf-badge-chip` = `dalao`/gold，
    `rowHeight == h1Height == 32`（chip 不增高），chip 垂直居中（delta 0），UID 仍在下一行，
    `scrollWidth 1623 <= innerWidth 1633`。（chip 现已改为 `button`，不再有 `href`；点击开弹窗。）
  - 390×844（`tests/browser-public-390x844.mjs`）：`overflow=false clipped=0 consoleErrors=0`；
    该脚本对 `/u/*` 的 `header==1` 期望不适用（页面本身有多个 `<header>`），不影响本次结论。
  - 评论作者预览卡：chip `dalao` 渲染为 `SPAN`，`a a == 0`（无嵌套链接）。
  - `@提及` 预览：用户无徽章时 `hasChip=false`，卡片 `state=ready`。
  - 本人页换佩戴 `dajiaoniu` → 头部 chip 无刷新变 `dajiaoniu`；点「不佩戴徽章」→ chip 消失且行高仍 32；
    已恢复原佩戴（`top_badge_id=40 dalao, manual=t, hidden=f`）。

## Review round（小审 PASS-with-residual → 已全部处理）

- **P1 佩戴态没按账号归属**：`useBadgeWorn` 的 `useState` 是全局单例，登录/登出是客户端跳转不刷新，
  上一个账号的 `loaded/wornSlug` 会漏给下一个账号（选了「不佩戴」→ 新账号头部永远空；戴过通用 slug
  → 新账号显示上一账号那枚）。修法：`ownerId` 归属 + `loadedFor(userId)`，`ensureLoaded()` 只在归属
  匹配时短路；`badgeWorn.test.ts` 补了「切换账号要重取」的行为测试（桩 `useState('auth:user')` 那一格）。
- **P2 头部优先级只有源码字符串断言**：抽成纯函数 `utils/profile/profileHeaderBadge.ts` 的
  `resolveHeaderBadge`（7 条优先级各带理由），8 例表驱动；删掉锁实现的两条 `toContain`，页面同时
  去掉与纯函数重复的 `loaded` 分支。
- **P3 契约文案过强**：`profile.yaml` 原写「never shows two different badges on one page」，改成
  「同源；chip 载荷有缓存 TTL，佩戴/停档瞬间可能短暂不一致」。
- **P4 知识库假理由**：删掉「改 JOIN 顺带修 NULL 扫描」——`badgeTierColumnsSQL` 全是 `COALESCE`，
  旧 SQL 本来就不会扫 NULL；改 JOIN 的唯一目的是可见性口径。`TopBadgesFor` 注释补上
  「两个读口都不判 `top_badge_hidden`，hidden 依赖写入不变量」。
- **观察项**（不改）：`PublicProfileUserID` 为拿 userID 会多跑一次徽章查询（非 N+1）；两处装配
  没有测试守着（删任一行测试仍绿，靠 curl 证据）；长昵称接近列宽时 chip 会整体换到第二行
  （+24px），CSS 注释已改成诚实表述，短昵称实测行高差 0。

## Second segment（2026-11-14）：chip 点击改成徽章说明弹窗

- 新交互：`SFBadgeChip` 有 username → `<button aria-haspopup="dialog">` 调 `useForumBadgeDialog().openBadgeDialog`；
  无 username → span。**不再跳** `/u/x?tab=badges`。
- 新文件：`composables/forum/badges/useForumBadgeDialog.ts`（全局状态 + 按 username 缓存
  `GET /profiles/{u}/badges` + 按 slug 命中 + 焦点归还）、`components/forum/badges/SFBadgeDetailDialogHost.vue`
  （挂 `app.vue`，复用 `SFBadgeDetailModal`，loading/failed 都带可见关闭）、
  `composables/forum/badges/useBadgeWearToggle.ts`（从 tab 抽出的佩戴动作 + toast，`SFProfileBadgeTab` 改用）。
- 评论作者行：chip 移到预览按钮的兄弟位；交互分支 `SFUserIdentityName` 加 `linked=false`，
  顺带删掉 `previewCapture` 捕获监听，`SFComment.toggleUserPreview(event)` 改为从
  `event.target.closest('.sf-comment__author-trigger, .sf-comment__avatar-trigger')` 找触发按钮
  （原写法依赖 `event.currentTarget`，在自定义事件里是 undefined —— 被捕获监听掩盖的真实 bug）。
- 清理：`forumUserBadgesPath` 零调用已删；i18n 删 `badges.chip.ariaLabelLink`、加
  `badges.chip.ariaLabelDialog` / `badges.dialog.loading` / `badges.dialog.failed`（README 已登记）。
- 证据：`bun test` 2935 pass / 0 fail（281 files，25834 expect）；typecheck exit 0；架构门 passed（2235 files）；
  `validate-docs` passed。浏览器：`/t/1221/fetch-url` `button a`=0、chip 全部不在交互元素内（`chipsInsideInteractive`=0）；
  点评论作者名仍开预览卡（`state=ready`，卡片内 `a a`=0）；点 chip 开详情
  （`大佬/金牌/未分组/徽章说明/dalao 牛鼻/获得条件/获得于 2026-11-13/取消佩戴/关闭`），URL 不变，
  预览卡同时关闭；ESC 后 `document.activeElement` 回到 `BUTTON.sf-badge-chip[data-badge-slug=dalao]`，
  `body.overflow` 回 `visible`；他人页 `/u/seed_44888364289b` 弹窗只有「关闭」（无佩戴按钮）。

## Next

- 主题 ViewModel（`themecompiler.ProfilePageViewModel` / `PublicUserView`）未加徽章字段：
  第三方主题若想自己画头部需要扩这个契约（内置主题走 Host 岛，不受影响）。
- 未提交：本刀与工作区里既有的 bio 长度、身份行、佩戴出口改动一起待提交。
