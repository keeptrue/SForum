# 2026-11-24 搜索页「极光玻璃」落地（forum.search 独立岛）

## Changed

- 新增 `apps/web/app/components/forum/search/`：`SFSearchPage.vue`（岛根 + 区域出口）、
  `SFSearchHero.vue`（胶囊搜索框，包 `SFSearch`）、`SFSearchQuickTerms.vue`（真实标签 chips）、
  `SFSearchResultSurface.vue`（唯一玻璃面 + 四态 + 加载更多）、`SFSearchResultCard.vue`
  （容器 ≥900px 的卡阵项）、`useSearchFeed.ts`（URL→防抖→`searchTopics`→分页）。
- 新增 `apps/web/app/assets/css/sforum-search.css`：`--sf-aurora-*` 两套 token（`:root` / `.dark`），
  全部从 `--sf-accent-rgb` / `--sf-accent-dark` 派生，无写死品牌色。由 `SFSearchPage.vue`
  的 `<style src>` 引入（不新增 `nuxt.config.ts` 条目）。
- `SFThemeTemplate.vue`：`forum.component.search_page` 改绑 `SFSearchPage.vue`（**唯一决定
  `/search` 渲染什么的地方**）。`pages/search.vue` 的 fail-closed slot 同步换组件。
- V3 稳定身份：`SFSearchPage`、`SFSearchHero`、`SFSearchQuickTerms`、
  `SFSearchResultSurface`、`SFSearchResultCard` 已以 `public` owner 登记到
  `docs/extensions/v3/catalog-identities.json`，并重生成 UI 与 Go 组件目录。
- `SFHomePage.vue`：抽走搜索分支（`loadTopicPage` 的 `searchTopics`/空列表分支、
  `feedState` 的 `search-empty`、标题/空态文案的 `isSearchPage` 分支、空态 `data-sf-region`
  常量化为 `topic-list-empty`、island 身份常量化为 `forum.component.home_page`）。
  保留 `isSearchPage` 仅用于区域出口 page id 与回退路径。
- `SFNavbar.vue`：`/search` 上不渲染 `SFPublicMobileSearchBar`（搜索地标 3 → 2，名字唯一）。
- i18n：`search.*` 补 6 个 key（`landmarkHero` / `submit` / `heroSubtitle` / `quickTermsLabel` /
  `resultCount` / `clearQuery`），zh-CN 与 en-US 成对。

## Decisions

- **岛绑定是唯一开关**：主题模板 `<sf-search-page>` 名字不变，所以不用重打主题 artifact；
  改 Host 侧映射即可。`pages/search.vue` 的 slot 只在 fail-closed 回退生效。
- **结果渲染器二选一**：容器 ≥900px 走 `SFSearchResultCard`（摘要才是搜索相关性的依据），
  <900px 走 `SFHomeTopicRow`。两套同时存在于 DOM，由 `@container (min-width:900px)` 切换，
  因此 SSR 首帧就正确、没有 hydration 换布局的 CLS。
- **`backdrop-filter` 只在两处**：胶囊与结果面容器。卡片/行只保留 fill + 1px 亮边 + 阴影。
  ≤640px 结果面降级为不透明表面并关掉磨砂。
- **卡片内不能有嵌套 `<a>`**：HTML 解析器遇到内层 `<a>` 会提前闭合外层链接，把 meta 行
  甩出卡片（实测导致右侧统计贴边/被裁）。标签因此用 `<span>`。
- 移动端光晕降级：两团、`blur(40px)`、`animation:none`；`prefers-reduced-motion` 冻结而非隐藏。

## Next

- 命中高亮已接入（2026-11-24 补）：`utils/forum/searchHighlight.ts` 的
  `tokenizeQuery` / `highlightSegments` 把文本切成 `{text,hit}` 分段，模板用
  `<mark v-if>` / `<template v-else>` 输出，**不用 `v-html`、不拼字符串**。
  两条路径都生效：卡阵 `SFSearchResultCard` 高亮标题 + 摘要；行式
  `SFHomeTopicRow` 通过新增的可选 `highlightQuery` prop 高亮标题（默认空串，首页零影响）。
  多词按空格分词、大小写不敏感、长词优先、停用词跳过。单测 `tests/forum/searchHighlight.test.ts`。
- 剩余：行式路径不渲染摘要，所以窄屏只能高亮标题；若产品要窄屏也看摘要，需要给行组件加摘要行。
- 时间格式化 helper 在 `SFHomePage.vue` 与 `SFSearchPage.vue` 各有一份，出现第三个调用方时
  抽到 `utils/forum/forumTime.ts`。
- `SFHomePage` 末尾的 `SFContentColumnFooter` 内部又是一个 `<footer class="sf-footer">`，
  与主题 `<sf-footer>` 叠成两个 footer（既有问题，本次未改）。

## Open Questions

- `forum.search` 受 `features.search` 门控；本机 dev 库该开关是 `disabled`，页面会 404
  （`pages.data_not_found`）。运行时验收时临时置为 `enabled`，验完已还原。
