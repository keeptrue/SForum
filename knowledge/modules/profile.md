# Profile Module

Member public profiles and current-user profile settings.

## Backend

- `apps/api/app/Models/Profile` package: types, store interface, service,
  postgres store.
- `Service.SearchPublicUsers` / `Store.SearchPublicUsers` is the bounded public
  member lookup (default 8, max 20, query max 80 runes) used by the AI reply
  bot's `user-search` tool. It requires a non-empty query (an empty one would
  become a `%%` LIKE pattern and list every member), escapes LIKE
  metacharacters, matches `username_lower` and `lower(display_name)` on active
  accounts only, and returns `UserSearchHit{UserID, Username, DisplayName}`
  with no email/status/role field. It has no HTTP route of its own; the
  authenticated composer picker (`/api/v1/composer/references?kind=user`) stays
  the only browser-facing user lookup. Rationale:
  `decisions/2026-10-11-ai-reply-endpoint-identity-and-user-search.md`.
- `user_profiles` table (migration `202607070004_user_profiles.sql`):
  `user_id` PK referencing `users(id)` cascade delete; `bio`, `signature`,
  `location`, `website_url` text; `avatar_attachment_id` nullable reference to
  `attachments(id)` set null on delete; `created_at`/`updated_at`.
- Sparse by design: no background images, custom code, birthday, phone,
  follow counts, or gamification fields. `user_profiles` remains sparse after
  bookmarks shipped — the public bookmarks tab reads `content_bookmarks`
  through a `BookmarkLister` interface and adds no profile column.
- `ExtensionProfileTabProvider` (`app/Providers/profile_contributions.go`)
  resolves `forum.profile.tabs` for a viewer: tabs with an empty
  `payload.permission` are public, the rest require `viewer.Can(permission)`.
  `kind: widget` tabs carry a `componentId`, keep `url` empty (no extension
  proxy), and are capped at 8 per render; overflow is truncated in contribution
  order with a warning log. Link tabs are not capped. Decision:
  `decisions/2026-10-26-profile-tab-l2widget.md`.
- Profile responses include an `avatar` view object with `kind`, `url`,
  `attachmentId`, and `alt`. Uploaded avatars win; otherwise the service falls
  back to the configured default provider (`initials`, `gravatar`, or
  `static`). The older `avatarAttachmentId` field remains for compatibility,
  but avatar changes should use the dedicated avatar endpoints.
- Uploaded avatar views use the stable browser media URL
  `/media/avatars/{publicId}`. The Nuxt Host proxies that URL to the
  versioned attachment content API, keeping API versioning out of rendered
  profile and forum image URLs.
- Avatar view construction is shared through the neutral
  `apps/api/app/Support/Avatar` builder so Identity and Forum summaries reuse
  the same fallback behavior without importing the Profile model package.

### Endpoints

- `GET /api/v1/profiles/{username}` public profile: user summary + profile +
  public-visible topic/comment counts, 5 recent public topics, and a bounded
  public activity timeline. Counts and timeline rows only include active
  public topics and active comments in public active/locked topics.
  `extensionTabs` are filtered by the optional viewer's permissions; the
  permission key itself is never serialized.
- `GET /api/v1/profiles/{username}/activities?kind=topic|comment&page&perPage`
  paginated public activities. Comment items include `commentId` and
  `commentPage` (flat-view page, same formula as
  `GET /topics/{id}/comments/{commentId}/page`) so clients can build
  `/t/.../page/N#comment-{id}` deep links.
- `GET /api/v1/profile` current user's editable profile (login required).
- `PUT /api/v1/profile` update current user's profile (login required,
  current-user only). Validates length limits and website URL shape
  (`http://`/`https://`). It still accepts `avatarAttachmentId` for
  compatibility, but new avatar changes should use the dedicated endpoints.
  - Length caps count Unicode code points (runes), not UTF-8 bytes: `bio` 160,
    `signature` 200, `location` 100, `websiteUrl` 200. Counting bytes made 178
    Chinese characters (534 bytes) a 422 while the counter read 178/500 — the
    `bio` cap was 500 back then and is 160 now. Both sides count code points
    (`utf8.RuneCountInString` / `Array.from(value).length`), the same unit as
    `contracts/openapi/schemas/profile.yaml` `maxLength` and the admin path's
    `len([]rune(...))`. Guarded by
    `TestServiceUpdateMyProfileCountsRunesNotBytes`,
    `TestUpdateProfileBioCountsRunesNotBytes`, and
    `apps/web/tests/settings/profileStoryTabCounter.test.ts`; its emoji cases
    are the only ones that fail if the counter goes back to `String.length`.
- `POST /api/v1/profile/avatar` uploads and sets the current user's avatar.
  The actor must be logged in, active, have `attachment.upload`, and the runtime
  option `avatar.allow_upload` must be enabled.
- `DELETE /api/v1/profile/avatar` removes the uploaded avatar and returns the
  profile decorated with the configured fallback avatar.
- `PUT /api/v1/auth/locale` updates the logged-in user's private default
  language. Empty input restores the runtime `site.default_locale`; other
  values must be among `site.supported_locales`.

Profile update is login-required and current-user only; no admin profile
editor in V1.

### Bookmarks（收藏 tab，2026-11-10）

- `GET /api/v1/profiles/{username}/bookmarks` 与 `GET /api/v1/profile/bookmarks`
  返回同一形状（`ProfileBookmarkPage`：`items` + `page`/`perPage`/`total`）。
  视角决定数据面，服务端裁剪，前端不负责过滤：
  - 他人/匿名：只出 `visibility='public'`，且条目里 `remindAt`/`remindedAt`
    **必须为空**（行主的提醒是私有调度数据）；
  - 本人：含私密条目，带 `visibility` 供 UI 加锁标记，提醒两列保留。
- 缓存头按视角分裂：他人/匿名 `public, max-age=30` + `Vary: Cookie`；本人
  `private, no-store` + `Vary(Cookie, Authorization, Accept-Language)`。
- 资料页第 4 个内置 tab 为「收藏」，顺序 主题 / 回复 / 收藏 / 关于；受
  `features.bookmarks` 控制，关闭时 tab 根本不进 `profileTabs`，激活态回落到
  「主题」。提醒 chip 只在本人视角渲染（模板与 tooltip 双保险）。
- 后端通过最小接口 `BookmarkLister` 注入，Forum 不认识 Profile、Profile 不写
  收藏 SQL；适配器在 `app/Providers/forum_bookmarks.go`。
- 数据口径、隐私三层闸与残余见 `modules/forum.md` 的 Bookmarks 段与
  `decisions/2026-11-10-content-bookmarks.md`。

### 插件设置面（`forum.settings.profile.tabs`，2026-11-13）

- 个人设置页的插件 tab 来自宿主贡献点，不再是插件自建页面。声明 →
  投影 → 读模型 → 写入策略四段都在宿主侧：
  `ExtensionManifest.settings_section_*`（声明与校验）、
  `app/Providers/settings_surface_contributions.go`（裁决成 `Section`）、
  `settings_surface_sync.go`（投影进 entity-meta）、
  `settings_surface_policy.go`（读写策略）、`settings_surface_meta.go`（值的读写适配）。
  决策：`decisions/2026-11-13-settings-surface-contributions.md`。
- 允许的 tab 形态只有两种：`fields`（宿主按 `fields[]` 渲染表单，值按用户存）
  与 `component`（挂载本包声明的公开 L2 组件，保存权归插件）。`settings`
  形态只属于站点设置页，在个人页上声明会被 manifest 校验拒绝。
- 读：`GET /profile` 新增可选 `settingsSections`（已按治理覆盖、权限、可见性过滤并
  完成排序/截断）与 `settingsValues`（只含当前 tab 声明过的键）。**有 sections 时
  `settingsValues` 恒下发**，空则 `{}`：`*map[string]any` + `omitempty` 会让空 map 整键消失，
  前端就无法区分「没有插件字段」与「有字段但一个值都没存」，两者该渲染的东西相反。
  测试 `TestAttachSelfSettingsAlwaysSendsSettingsValues`。公开资料页
  `GET /profiles/{username}` 的 `meta` 只出 `visibility=public` 且有权的字段；
  `owner`/`admin` 永不出现在公开响应里。未注入 provider 时响应与改造前逐字节等价。
- 写：`PUT /profile` 的 `meta` 是 `{ <fieldKey>: string|number|boolean|null }`，
  `fieldKey` 是 entity-meta 组合键（`x.<扩展段>.<裸键>`），`null` 表示删除该字段值。
  只接受标量（对象/数组按非法值 422）；能定位到字段的一律 `422` +
  `data.fields[fieldKey]`（键是组合 fieldKey，值是由 `profile.settings.field_{unknown,forbidden,invalid,required}` 本地化后的文案）。
  未声明 / 被运营停用 / 无写权限的键**一律拒绝而不是静默丢弃**。
- 顺序：核心资料字段先落库，插件字段最后写（`UpdateMyProfile` → `writeSettingsMeta`）。
  两者无共同不变量，因此不引入跨域事务；meta 失败整次请求仍是 422，前端保留 dirty
  重发即收敛。逐字段写入是刻意的——entity-meta 的错误不带字段名。
- 生命周期：enable / upgrade / 生命周期状态发布先投影（失败即 `extension.preflight_failed`，
  状态变更整体回滚）；disable 把该扩展的字段整体翻成 `enabled=false`，定义与值都保留。
  投影同时做**差集清扫**：该扩展仍有、但本次不再声明的定义置 `enabled=false`（值保留），
  重新声明即恢复——所以升级改字段名的结局是「旧键停用、值还在」，而不是一个仍可读写、
  `visibility=public` 时还能被匿名读到的孤儿定义。
  投影只挂在改 status 的原语上（`PostgresStore.Enable` / 生命周期状态发布 / `Disable` /
  启动 reconcile），任何绕过它们的入口都会留下「表单能看、保存必 422」的坑，详见
  `modules/extensions.md`「投影只有一个入口」。
- 字段键的扩展段**无条件**带 8 位 FNV-1a：可读前缀压平后 `sforum.smtp` 与 `sforum-smtp`
  同形，身份必须由哈希保证。插件作者不拼这个键，只读下发的 `fieldKey` 并原样回写。

### Nickname（昵称）

- 昵称是展示标签，不是身份：`users.display_name` **不唯一**，唯一性只属于
  `username_lower`。决策见 `decisions/2026-10-20-nickname-policy.md`。
- `PUT /api/v1/profile` 接受 `displayName`（空串 = 回退用户名），成功时返回完整
  `PublicProfile`（含 `nickname` 状态），与 `GET /profile` 同形状。
- `GET /api/v1/profile/nickname/availability?value=` 只读预检，返回 `available`、
  `duplicates`（重名只提示）与 `conflict`（i18n key）。路由
  `core.route.profile.nickname_availability` 走 `core.guard.profile.self`。
- 策略来自 `profile.nickname.*` 公开选项（开关、冷却分钟、最小/最大长度、
  曾用名是否公开）；保留词复用 `identity.username.reserved`；昵称不得与另一位成员的
  `username` 完全同名（大小写不敏感）。
- 自助改名在 Identity 的 `NicknameService` 里完成：校验 → 冷却（事务内行锁 + 数据库
  时钟）→ 写入 `users.display_name` / `nickname_changed_at` → `user_nickname_changes`
  历史 → `audit_events(action=identity.nickname.change)`。管理员改名走既有 admin 事务，
  只写历史（`source=admin`），不写 `nickname_changed_at`，因此不占用成员的冷却。
- 改名成功后由 `PostgresSiteEngine.SyncAuthorDisplayName` 回写
  `search_documents.author_display_name` 与 `metadata_tsv`；失败只记日志，不回滚改名。
- 公开主页的 `previousDisplayNames` 只在 `profile.nickname.history_public` 开启时出现，
  最多 1 条；后台用户详情始终可看最近 20 条（`AdminUserDetail.nicknameHistory`）。

## Frontend

- `/u/{username}` public profile page uses the default theme three-column
  shell. The center column renders a compact member summary and locale-aware
  daily activity groups for real public topics and replies; the right rail
  renders real public profile fields, public topic/reply stats, and recent
  public topics. It does not show unimplemented social counts, levels, follows,
  private messages, portfolios, or placeholder stats.
- `SFProfileRightRail` owns the shared desktop/mobile rendering of public
  details, real statistics, recent topics, and extension links. On mobile the
  left navigation and this right rail use the default-theme shared drawers.
- The identity line never repeats `@username` when the resolved display name is
  already the username: an empty nickname comes back from the API with
  `displayName` backfilled to the username, and a nickname may literally equal
  it. The public header and the rail share
  `shouldShowUserHandle(displayName, username)` from
  `app/utils/forum/userDisplay.ts` — the same rule `SFUserIdentityName` already
  applied to comments and notifications. `UID {n}` always stays, and hiding the
  handle leaves no leading `·` or doubled space. Account-handle contexts (user
  menu, profile settings preview) deliberately keep their `@username`.
- The header identity block is two layers: `sf-profile-intro__name-row`
  (`h1` + optional worn-badge chip) and `sf-profile-intro__identity-meta`
  (`@handle` + `UID n` + trust-level badge). The meta row (location /
  website / joined) sits after the identity row and is not part of that
  block. The bio is **not** part of the header: it renders only in the
  `about` tab (`sf-profile-about`), so a page shows the same sentence once
  instead of twice. The `profile.bioEmpty` key stays — the about tab and the
  profile settings preview still use it. The chip is `SFBadgeChip` fed by
  `PublicProfile.topBadge` (source: `users.top_badge_id`, see
  `modules/badges.md`), and is 18px against a 32px
  (25px on ≤680px) h1 line box, so it adds no header height. On the owner's own
  profile the chip follows `useBadgeWorn`'s local state instead of the response
  field: wearing another badge updates it without a reload, and "wear nothing"
  removes it. That global state is actor-scoped (`ownerId` / `loadedFor`) because
  login and logout are client-side navigations, so an unscoped `useState` would
  leak the previous account's worn badge into the next one; the seven-step
  priority lives in the pure `resolveHeaderBadge` (`utils/profile/profileHeaderBadge.ts`).
  The user preview card (`SFCommentUserPreview`) shows the same
  chip, preferring the comment payload's `topBadge` so the card never disagrees
  with the chip that opened it; inside the card the chip is link-free because
  the name is already a link.
- A badge chip is a `<button>`, not a link: clicking it opens the global badge
  explainer dialog (`SFBadgeDetailDialogHost` in `app.vue`, state in
  `composables/forum/badges/useForumBadgeDialog.ts`) instead of navigating to
  `?tab=badges`. The dialog body is the same `SFBadgeDetailBody` the badge wall
  modal uses, so the two entries cannot drift. The chip payload has no
  description/requirement/granted-at, so the dialog fetches
  `GET /profiles/{username}/badges` once per username and resolves the entry by
  slug; a failure shows an explicit "unavailable" state rather than a fabricated
  badge built from the chip's fields. Because it is a button, it must never sit
  inside another button or link — hence the comment author row renders it as a
  sibling of the preview trigger, and the preview card passes no `username` so
  the chip stays a plain `span`.
- Extension tab contributions (`forum.profile.tabs`) have two shapes.
  `extensionRoute` / `hostLink` tabs stay right-rail links; `widget` tabs join
  the main tab bar after `about` and mount `SFExtensionWidget` in the panel
  area. The right rail filters widget tabs out so one contribution never
  appears twice. Switching tabs unmounts the widget (`v-if`, not `v-show`), so
  an L2 lease and its CSS never outlive the panel. Widget order is `order`, then
  `extensionId`, then `id` - independent-field code-unit comparison, the same
  key the backend uses for truncation - and the built-in
  `topics`/`replies`/`about` positions never move.
- A tab `permission` is a server-side visibility filter, not a data boundary:
  the field is `json:"-"` and never reaches the browser, and plugin routes must
  authorize their own data. `GET /profiles/{username}` resolves an optional
  actor and degrades to anonymous when the session cannot be resolved;
  `GetPublicProfile` keeps its anonymous signature for `PageViewModels` and
  `AITools`, so host read surfaces do not become viewer-dependent.
- Widget tabs are L2 mounts, so the profile page aggregates their component
  refs into the document CSP through `GET /extensions/runtime/page-policy`. The
  route shell is the single fetch point (next bullet) and passes
  `collectProfileWidgetRefs()` to `SFPageOutlet`; `SFPageOutletResolver`
  aggregates once on the native path and `SFThemeTemplate` once on the
  theme-template path (mutually exclusive, pinned by
  `apps/web/tests/extensions/profile-page-policy.test.ts`). Omitting the refs
  does not block the mount: the app then writes no `Content-Security-Policy` for
  that response, and L2 JS (`import()`), CSS (`<link rel=stylesheet>`) and
  descriptor/asset fetches are same-origin, so `script-src`/`style-src`/
  `connect-src 'self'` would allow them anyway. The costs are a missing
  digest-bound style pre-injection on the theme-template path (first-frame
  flash) and no document policy / `X-SForum-Document-Policy-Digest` evidence.
  Do not diagnose a broken widget tab as "CSP blocked it".
- `collectProfileWidgetRefs` drops invalid refs one at a time, deliberately
  unlike `collectRegionWidgetRefs` (all-or-nothing): one plugin's bad
  contribution must not void the whole page's L2 policy, and the dropped tab
  fails closed on its own.
- The route shell is the single profile fetch point: `useProfileShowData()`
  (`apps/web/app/composables/profile/useProfileShowData.ts`) creates the one
  `useAsyncData` and provides the refs; the island injects them. Default options
  are deliberate - they keep Nuxt's payload purge. Two independent callers
  would cost two separate things: differing options log `Incompatible options
  detected` in dev (`asyncData.js`), and because `payloadExtraction: false`
  (`apps/web/nuxt.config.ts`) the server has no `static.data` to reuse, a
  second same-key call re-executes the handler even with identical options
  (default dedupe `cancel` aborts the first and sends another). Do not add
  `getCachedData`/`dedupe` back on either side; a future second consumer should
  call `useProfileShowData()` instead.
- On a response that carries the strict document policy (any page with L2 refs,
  including a profile that has widget tabs), `img-src 'self'` blocks avatar
  images hosted off-site: the gravatar provider (default base URL
  `https://gravatar.com/avatar/<hash>`) and the `static` provider when its
  operator-configured URL points off-site. The shared builder would also honor
  an external `avatar.Attachment.URL` (`app/Support/Avatar/view.go:88`), but no
  current avatar source populates it - the `attachments` table has no URL
  column, storage URLs are computed at read time and never reach the avatar
  source - so uploaded avatars resolve to the same-origin
  `/media/avatars/{publicId}`. `SFAvatar`'s existing `@error`
  fallback renders initials instead - that fallback working as designed, not a
  new degradation path. The default `initials` provider and a default install
  (no L2 refs, so no document policy) are unaffected. Adding an
  operator-configured avatar source allowlist to `img-src` in the policy layer
  is a separate backend task.
- Public activity links use existing legal forum routes:
  `/t/{topicId}/{slug}` for topics and
  `/t/{topicId}/{slug}/page/N#comment-{id}` for replies (`/page/N` omitted when
  N=1). Page is pre-baked from API `commentPage` because URL fragments are not
  sent to the server; hash-only links always SSR page 1.
- The edit-profile entry on `/u/{username}` is self-only UI backed by the
  existing login-required current-user profile API; hiding the button is not a
  permission boundary.
- `/settings/profile` current-user settings page with avatar preview,
  upload/remove controls, safe defaults, and a clear save flow (success
  auto-dismisses after 5s; errors do not).
- 昵称输入由 `components/settings/SFProfileNicknameField.vue` 拥有：本地格式/长度
  预检、400ms 防抖的 `nickname/availability` 预检、分钟级冷却倒计时（30s 刷新）、
  重名提示与字段级错误。保存仍由资料页统一提交；成功后页面更新
  `profile.value.displayName` 与 auth session 的 `displayName`，导航栏即时同步。
  组件只在昵称真的变化时才提交 `displayName`。
- `/settings/profile` uses the
  `forum.settings.profile` Page Registry surface with a canvas layout: desktop
  left account rail, center editable profile form, right public preview, and
  shared left/right mobile drawers on narrow screens.
- The three-column settings chrome (left `SFHomeNavigation` +
  `SFSettingsAccountNav`, page head with drawer toggles, right rail, mobile
  drawers, `SFContentColumnFooter`, category-group fetch) is owned by the
  shared `SFSettingsShell` component; profile, personal appearance, login
  methods, local password, security, personal access tokens, and notification
  settings pages only fill its `default` / `#rail` / `#head-actions` slots. The
  rail slot renders once and is reused by both the desktop aside and the mobile
  right drawer. New account settings pages should add a link in
  `SFSettingsAccountNav` and wrap content in `SFSettingsShell`.
- The profile settings page keeps all writes on the existing `useProfileApi`
  contract: `GET /profile`, `PUT /profile`, `POST /profile/avatar`, and
  `DELETE /profile/avatar`. Avatar upload UI requires both public runtime
  option `avatar.allow_upload` and `attachment.upload`; API policy remains
  authoritative.
- Profile form editing is draft-based. Save is disabled until local edits are
  dirty, reset discards unsaved changes with a neutral Toast, save/avatar
  success Toasts auto-dismiss after 5 seconds, field-level validation stays
  beside the relevant inputs, and blocking errors remain visible.
- 插件分区与核心 tab 共用一根 tab 栏：`SFProfileSettingsTabNav.vue` 把分区清单与错误计数
  翻成 tab 条目，呈现与键盘语义归 `SFSettingsTabNav.vue`（面板模型下
  `role=tab/tablist`、roving tabindex、←/→ 循环、Home/End）；
  分区清单由 `utils/settings/profileSettings.ts` 的 `composeProfileSettingsTabs`
  合成——核心 `identity`/`story`/`links` 在前，插件分区按 `effectiveSortOrder` 追加；
  同 id 重复、缺 label、`component` 缺 `componentId`、字段分区过滤后为空的分区
  **在这里就被丢掉**（不会渲染成空壳 tab）。分区选中态属于 URL `?tab=`，composable 不持有。
- 插件分区的面板只由 `components/settings/profile/SFProfileExtensionTabPanel.vue` 渲染：
  `fields` 用中性渲染器 `SFDeclarativeSettingsField`（随页脚统一保存），
  `component` 挂 `SFExtensionWidget` 并显示「保存由插件自己负责」——此时页脚保存按钮
  禁用并给出原因，避免两套保存互相覆盖。
- 一次保存只发一个 `PUT /profile`（核心字段与 `meta` 同体）。失败时 `useProfileSettingsDraft`
  只写 `fieldErrors`（键就是 `fieldKey`），**不清空草稿、不刷新快照**，因此 dirty 状态保留；
  页脚用 `errorSummary` 显示第一个出错分区与计数，点「跳到出错分区」才切 tab
  （`firstProfileSettingsErrorTab` → `jumpToErrorTab`），不自动抢走用户正在输入的分区。
- 插件分区 id 就是贡献 `id`，与核心 tab id 同命名空间：贡献 id 不能叫
  `identity`/`story`/`links`，否则整个分区被去重丢掉。
- The profile identity section includes a private default-language selector.
  `useUserLanguage` maps API BCP 47 values (such as `en-US`) to Nuxt locale
  keys (such as `en`), persists a signed-in choice, and applies it to i18n.
- The read-only username field displays its public path prefix from the runtime
  `site.domain` option (`{domain}/u/`) instead of a hard-coded product domain.
- Navbar user menu links to public profile and profile settings.
- `useProfileApi` composable + `AvatarView`/`ProfileData`/`PublicProfile`
  types.
- `AvatarView` is also used by auth current-user state and forum author
  summaries; first-party UI should pass it into `SFAvatar`.
- `SFAvatar` emits remote `AvatarView` URLs (including Gravatar) directly in
  SSR HTML. It falls back to initials only after the image reports a real load
  error; do not reintroduce client-only preloading that swaps avatars after the
  first paint.
- Uploaded avatars are already normalized by the backend and render through a
  plain `<img>` at `/media/avatars/{publicId}`. Do not send these API-backed
  relative URLs through Nuxt IPX: IPX treats them as local filesystem assets
  and returns `IPX_FILE_NOT_FOUND`.
