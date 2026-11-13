# 2026-11-13 通知收件箱 UI/UX 与 SEO 修复（frontend）

Status: **landed（未提交，工作树改动）**——`/notifications` 与 `/notifications/:id` 的
中列底色、筛选面、空状态、i18n 缺键、SEO 归属、移动端头部、未读徽章 hydration 一次收口。
模块笔记：`../modules/notifications.md`「API and UI」的 Inbox presentation rules 与
SEO ownership 两段。

## Changed

- **中列底色**（`SFNotificationsPage.css`）：`.sforum-notifications__main` 从
  `var(--sf-public-bg)`（#f3f5f7）改成 `var(--sf-public-surface)`，与 settings/moderation
  三栏页对齐；详情页复用同一个类，两页一起修好。实测 `rgb(255, 255, 255)`。
- **桌面单一筛选面**：`@media (min-width: 981px)` 内隐藏 `__filter-strip` 与 `__filter-note`；
  ≥981px 左栏类型导航可见，≤980px 左栏 `display:none`、横向条保留。
  同一 `activeFilter` 驱动的两个控件不再同时出现。
- **空状态单分支**：删掉 `v-else-if="!items.length"`，只留 `v-else-if="!filteredItems.length"`，
  按 `activeFilter` 在 `notifications.empty*` / `filter.empty*` 之间切换文案与图标。
  服务端已按 type/unread 筛选，`items` 为空就是「这个筛选下没有通知」。
- **`SFEmptyState` 新增 `icon?: string`**（默认 `i-lucide-inbox`，模板 `:name="props.icon"`）：
  之前通知页传的 `icon="i-lucide-bell-off"` 落成无效 HTML 属性，实际一直渲染默认 inbox 图标。
  `iconLabel` 优先级与其他调用方行为不变（全仓 73 个调用点里只有通知页传 `icon`）。
- **类型/图标目录单源**：`notificationsPresentation.ts` 导出 `notificationTypeIcon(type)`
  （兜底 `i-tabler-bell`），`notificationPresentation` 与 `SFNotificationTypeNav` 都走它，
  左栏本地 `filterIcons` 只留 `all` / `unread`。`bookmark_reminder` 入目录（`i-tabler-bookmark`）
  并在 `reaction` 之后插入 `notificationFilters`；zh/en 补 `filter/types/body.bookmark_reminder`，
  同时补 `filter.reaction`——它本来在目录里但两个语言都缺，页面上直接漏出原始键。
- **文案口径改写**（zh/en 同步，key 完全一致）：`description`/`emptyDescription` 补点赞与徽章；
  `filter.emptyDescription` 不再接收 `{count}`；`filter.loadedScope` 改成「基于当前已加载的 N 条」；
  `unreadSource` 说明是全量未读；右栏 `loaded*` 改成「本页概览 / 仅本页 / 本页通知 / 本页未读」。
- **SEO 归属**：列表路由壳与详情岛都改 `useSForumSeo({ …, noindex: true })`，详情页删裸 `useHead`；
  routeRules 补 `'/notifications'` 与 `'/notifications/**'`（`cache: false` + `robots: { index: false }`）；
  `isProtectedSEOPath` 抽到 `server/utils/seoProtectedPaths.ts` 并补 `/notifications`、`/settings`，
  中间件 import 调用并把 env 读到的 adminPrefix 传进去。
- **移动端详情页头部**（`SFNotificationDetailPage.css`）：`@media (max-width: 560px)` 块补
  `.sforum-notification-detail__head { align-items: flex-start; }`。详情页先引入列表 CSS、后引入
  详情 CSS，同特异性下基线的 `align-items: flex-end` 胜出；而窄屏头部已是 column，`flex-end`
  作用在交叉轴，把「返回 / 标题 / 副标题」整块推到右边缘。桌面 `flex-end` 保留。
  实测 390×844 返回链接 x 从 281.98 → 14（等于 main 的 14px 左内边距）。
- **未读徽章 hydration**：`SFNotificationPreview.vue` 与 `SFPublicMobileBottomNavigation.vue`
  的徽章 span 各外包 `<ClientOnly>`（无 fallback）。SSR 时 navbar 先出 HTML（未读数 0），
  页面 `useAsyncData` 之后才写共享 state，payload 里已有值 → 客户端首帧渲染徽章、服务端没有，
  触发 `Hydration node mismatch`。`v-if` 条件与 `99+` 截断原样保留；未改 `refreshUnreadCount`
  时机、未动 `app.vue`。实测两页整页 reload 后 console 0 行，挂载后徽章仍为 24。
- **测试**：2873 → **2875**。task-1 五条（i18n 三键、图标不回退、SEO 归属、CSS token + 桌面隐藏、
  空状态单分支）+ 受保护路由/robots 断言 + 新增 `tests/seo/protectedPaths.test.ts`（4 条）；
  task-3 两条（移动端 header `flex-start`、两处徽章 `ClientOnly` 包裹）。

## Decisions

- **不新增页内 footer**：内容列底部保持干净，`<SFContentColumnFooter` 的既有禁令继续成立。
- **不改 loaded-scope 计数语义**：只改文案把「本页」说清楚；计数仍只统计当前已加载页，
  与「未读总数」是两个口径，靠文案区分而不是改算法。
- **SEO 双层而不是一层**：routeRules 管渲染出来的文档，middleware 的 `X-Robots-Tag` 管
  不经过页面渲染器的响应；只有一层时总有一类响应漏网。
- **抽取而非内联**：`isProtectedSEOPath` 埋在中间件里时新增受保护路由只能靠肉眼确认；
  抽成 util 后用 `tests/seo/protectedPaths.test.ts` 真值表钉住（含 `/notificationsfoo`、
  `/control-panelx` 这类边界）。
- **徽章用 ClientOnly，而不是把 unread 取数搬到 `app.vue`**：徽章是装饰性内容，晚一帧出现
  没有信息损失；navbar 已有同款模式（语言/外观/用户菜单都在 `ClientOnly` 里）；搬进 app
  启动链路会让每个页面（含公开页）多一次 SSR 请求，收益只是数字早一帧。

## Next

- 未提交，等 lead/reviewer 复核后由 lead 决定提交时机。
- 若后续新增通知类型，需同时补 `notificationFilters`、`notificationIcons`、zh/en 的
  `filter/types/body` 三处 key——新测试会红，但值得把「三处」写进 checklist。

## Open Questions

- **M5（静态 CSS 断言的盲区）**：移动端 header 那条只断言「mobile 块里**存在**一条
  `{` 后紧跟 `align-items: flex-start` 的规则」，所以有人把 `align-items: flex-end`
  作为**重复声明**追加在同一规则之后时不会红（后者才生效）。收紧办法：对
  `styles.slice(mobileStart)` 追加 `expect(...).not.toContain('align-items: flex-end')`，
  或改成匹配规则体「最后一条 align-items」的正向断言。
- 若以后要求未读徽章 SSR 首屏可见，需要把 unread 取数前移到 app 启动链路（当前刻意不做）。
- 主题模板 `extensions/builtin/themes/sforum-default/templates/notifications.html`
  本次刻意未动；若以后要让主题自己画筛选面，桌面隐藏规则需要跟着搬到主题层。
- 390×844 下横向筛选条可见但内容列底部仍有较大留白（`padding-bottom: 52px`），
  属于既有几何，本轮未调。
