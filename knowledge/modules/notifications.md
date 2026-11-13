# Notifications Module

## Scope

The first release provides durable in-app notifications plus optional email
projection for replies, mentions, and pre-publication moderation queue entry,
approval, or rejection.

## Notification Platform V2

**点赞通知（2026-10-28 落地，2026-10-29 验证完成）**：新增核心类型 `reaction`
（category `engagement`；曾误落 `plugin_unknown`，已修正），站内默认开、邮件/推送
默认关；`Outbox.NotifyReactionTx` 与评论通知同事务，自赞跳过、去重键
`reaction:{type}:{id}:{actor}`（取消后再赞不重复）。邮件模板键 `forum.reaction`。
PG 集成覆盖自赞不投影与取消再赞只 1 条；浏览器 UI 未单独断言（API + DB 证据
充分）；未做真实投递验证。最终门禁 9 项 PASS、`test.sh --fast` 三线 exit 0。

**收藏提醒通知（2026-11-10 落地并实机验证）**：新增核心类型 `bookmark_reminder`
（category `engagement`，图标 `i-lucide-bell-ring`），**站内与邮件默认开**、推送默认
关——这是唯一一处邮件默认开的类型，因为提醒是用户自己预约的，投不到等于功能失效。
邮件模板键 `forum.bookmark_reminder`（中英双语），去重键 `bookmark_reminder:{bookmarkID}`
（每条收藏的每次提醒只投影一次）。投影与 `reminded_at` 抢占在同一事务：通知失败整条
回滚、下一轮重扫，不会出现「标了已提醒却没发」。目标已不可见的到期行只写
`reminded_at`、不产生通知（否则每轮重扫）。实机证据：worker 日志
`claimed=2 notified=1`、`notifications` 行 + `mail_deliveries status=sent`、Mailpit
实收「[SForum] 收藏提醒」，见 `storage/qa/bookmarks-reminder-evidence.txt`。

**原生推送设备注册（2026-10-02）**：Core 新增 `push_devices` 注册表与自服务端点
`GET/POST /api/v1/push/devices`、`DELETE /api/v1/push/devices/{deviceId}`：令牌按
`(platform, token_hash)` 唯一，重复注册刷新并改绑到当前账号，同 `deviceId` 的旧令牌
自动撤销（覆盖换账号、FCM/APNs 令牌轮换、重装），令牌以 SHA-256 指纹 + Core 密钥
密文落库且任何响应都不回显。传输（FCM/APNs 发送）仍属 provider 插件；本轮未声明
`native_push` 通道键——通道键 + provider 槽 + 参考插件应一起落地。
决策：`../decisions/2026-10-02-native-push-device-registry.md`。


Notification Platform V2 is **completed**:

- Task book:
  `../plans/archive/2026-07/2026-07-27-notification-platform-v2.md`
- Decision:
  `../decisions/2026-07-27-notification-platform-v2.md`
- Handoff:
  `../sessions/2026-07-27-notification-platform-plan-handoff.md`

M0-M7 are implemented and the final repository gate is green. The completed
task book is archived under `../plans/archive/2026-07/`; exact command results,
runtime proof, and explicitly skipped checks are recorded in
`../reports/2026-07-28-notification-platform-v2-final.md`.
V2 now provides correct create/approval fanout, one versioned registry and
policy resolver, dedicated admin and own-user settings, bounded plugin emission,
server-side inbox filters, durable-revision SSE, and a protected built-in Web
Push provider.

Production hotfix (2026-07-28): the legacy Core policy projection must aggregate
PostgreSQL boolean columns with `BOOL_OR`/`BOOL_AND`, never `MAX(boolean)`.
`MAX(boolean)` caused comment creation to roll back while reading notification
policy. A real PostgreSQL regression test now covers this compatibility query.

Realtime badge hotfix (2026-07-28): the public Navbar starts notification
realtime from an immediate auth-state watcher instead of relying on child/parent
mount ordering. An asynchronous EventSource failure now triggers an immediate
REST reconciliation; a terminally closed source reconnects after one second.
Visible subscribed pages also reconcile every 30 seconds and whenever the tab
becomes visible, so API restarts, proxy interruptions, and missed wake signals
cannot leave the bell badge frozen until a full page reload.

Cross-tab realtime fix (2026-08-02): tabs for the same authenticated user now
elect one EventSource owner with Web Locks and distribute revision/refresh
signals through a user-scoped BroadcastChannel. Closing the leader releases the
lock to a waiting tab; user ids scope both primitives so actor changes cannot
share realtime authority. Browsers without both APIs retain the prior per-tab
EventSource behavior, and every visible tab still performs the 30-second REST
reconciliation. This keeps the server's four-connections-per-user guard as a
defense instead of making five open tabs predictably return 429.

### Plugin Extension Surface

| Surface | V2 contract |
| --- | --- |
| Type declaration | Manifest V3 `notificationTypes`; inert, extension-namespaced, exact schema file/digest, never plugin-required |
| Emission | Host API v2 `notifications.emit@1`; actorless exact artifact/grant/epoch/instance admission |
| SDK | `pluginv2.Host.NotificationEmitRequest` / `EmitNotification` |
| Recipient authority | Explicit active users only, maximum 50; Host policy and preferences remain authoritative |
| Payload/target | 16 KiB structured payload validated against the exact Draft 2020-12 schema; declared target equality only |
| Idempotency/rate | Host Command receipt transaction plus 60 committed requests per rolling minute |
| Audit | Accepted and rejected metadata is redacted; payload values and recipient ids are never audit fields |
| Closed surfaces | Raw notification/preference/delivery table writes, required plugin types, session/actor forgery, arbitrary URLs, and bulk broadcast |

The executable reference fixture is
`extensions/fixtures/plugins/sforum-notification-reference`. Disabled,
uninstalled, stale-artifact, or cross-namespace callers cannot emit; historical
rows retain versioned structured payload and use Host fallback presentation.

**徽章授予通知（2026-11-10 落地，2026-11-13 补齐四条路径）**：新增核心类型 `badge_granted`
（category `engagement`，图标 `i-lucide-award`），站内默认开、邮件/推送默认关（与 reaction 同
姿态），邮件模板键 `forum.badge_granted`（中英齐全——默认关闭不等于可以不写，用户
打开邮件通道后无模板会静默失败）。

**去重键是 `badge:{slug}`**（`fanout.go` 的 `NotifyBadgeGrantedTx`），不是按授予行 id：
同一用户同一徽章**终身只通知一次**，手动收回后重新授予也不再通知（徽章是终局荣誉）。
按 `user_badge_id` 去重会牵动 `GrantAutomatic` 的签名，把「授予是否新建」与「通知去重键」
耦合在一起。旧笔记写的 `badge_granted:{userBadgeID}` 与代码不符，已按代码订正。

**四条授予路径全部发通知**（`bootstrap/badges_wiring.go` 的 `badgeGrantedNotifier` 是唯一实现，
两个薄适配器只做类型转换）：规则自动 job、`AdminService.Grant`、`BatchGrant`（**只发
`grantedUsers`，`skipped` 不发**）、`Recompute`（`recomputeNotifyLimit = 200`，超限**整体不发**，
只留一条 `slog.Warn`）。通知失败只 warn，**不回滚授予**——每条 grant 都是自动提交的独立语句，
没有可回滚的事务边界。细节见 `modules/badges.md`「授予通知与庆祝」与
`../decisions/2026-11-13-badge-notification-and-celebration.md`。

**`badge` 是新的通知目标类型**（`TargetType="badge"`，`TargetID` 是徽章 id）：
`BadgeGrantedEvent.BadgeID <= 0` 时**不写目标**（收件箱渲染成无链接的纯文本，不是 `/badges/0`
死链）。目标解析走 `bootstrap/notification_adapter.go` 的 `targetType == "badge"` 分支 →
`Badges.ResolveNotificationBadgeTarget`（三重校验：徽章启用 + 仍持有授予行 + 等级启用），
fail-closed 返回 `(false,"",nil)` 而不报错，目标恒为 `/u/{username}?tab=badges`。
**不加这条分支的症状是徽章通知整条变 `targetAvailable=false`**——Forum 的解析器不认识 `badge`。

**语义与互动通知相反的一处**：管理员给自己授予徽章**会**发通知。自赞跳过是因为
那是互动噪音；徽章是公开荣誉，自己给自己发一枚运营徽章也需要留下通知记录。
三种授予来源（规则自动 / 管理员手动 / 批量重算）在 `user_badges` 里可区分，见
`modules/badges.md` 的判别式表。

### 站内庆祝展示层（`presentation.celebration`，2026-11-13）

庆祝**不是新的通知类型、不是新的授予路径、不是新的 Host 命令**：徽章授予照旧写
`notifications` 行（`badge_granted`），这一层只回答「哪些已发生的通知值得在前端弹一张庆祝卡」。

| 环节 | 事实 |
| --- | --- |
| 载体 | `notification_type_descriptors.presentation` jsonb 的 `celebration: true`（**没有新列**）。Core 只标 `badge_granted`（`registry.go` 的 `Celebration: value.id == TypeBadgeGranted`：回复/提及/审核结果是流程的一部分，弹卡会变噪音） |
| 插件声明 | Manifest V3 `notificationTypes[].celebration`（`ManifestNotificationType.Celebration`，`manifest-v3.schema.json` 里是可选布尔）→ `pluginDescriptors` 落进同一个 `presentation` jsonb。**读面判据统一是 `presentation->>'celebration' = 'true'`**，所以插件与 Core 一条 SQL 全覆盖 |
| 迁移 | `202611130002_notification_celebrations.sql`：`presentation \|\| '{"celebration":true}'` 打标（保留既有 icon 等键）+ 建 `user_celebration_cursors`（Down 先拆表再退标记） |
| 读面 | `GET /notifications/celebrations`（`ListCelebrations`）/ `POST /notifications/celebrations/seen`（`AcknowledgeCelebrations`）。login 守卫；注册在 `/:id` **之前**，否则 `celebrations` 会被当成通知 id |
| 首访 | 没有游标行 → `initialized=false` + **空 items** + `cursorId = LatestNotificationID`，且**不写游标行**。写了 `initialized` 就变 true，而前端要靠 false 触发「先 ack 种子游标、本轮不再拉取」；老用户升级后不该被几个月前的徽章刷屏 |
| 窗口/边界 | `celebrationWindow = 7 天`（SQL 里 `now() - interval`：比的是**时间戳**，不是日历界，所以不违反「按日取界必须 Go 传参」）+ `n.id > cursor`；`DefaultCelebrationLimit = 3`；`total` 封顶 99 |
| 可见性 | 复用 `ResolveSafeTargets`，**目标不可见整条丢弃**，不返回 `targetPath=null` 的空卡 |
| ack | `GREATEST` 语义（`ON CONFLICT DO UPDATE` + `GREATEST`），游标只进不退：前端 ack 是尽力而为的，乱序到达时「置等于」会把看过的通知重新弹一遍 |
| 文案 | `titleKey`/`bodyKey` 是 Core 类型的 i18n 键，插件类型为 `null`；`label`/`body` 是 `{zh,en}` 兜底（Core 在服务端有 `coreCelebrationFallback`，因为 Core 描述符的 presentation 只有 icon）。**逐语言回落**，不整体二选一 |
| 前端 | `SFCelebrationHost.vue` 固定左下（`bottom-4 left-4`，右下是 toast）；只在「已登录 + 徽章功能开 + 非管理端路由 + 页面可见」时存在；`items` 为空不渲染任何 DOM。文案链三态：i18n 命中（`te` 且译完无残留 `{name}`）→ 服务端 `label`/`body` → 吃掉占位符 |

**刻意不给 `user_celebration_cursors` 建 `sforum_core_v1` 视图、也不新增 HostQuery**（AGENTS.md
要求刻意缺失的读面写明理由）：它是**每用户的展示进度位**，唯一的消费者是 Core 自己的庆祝读面；
插件既没有正当消费面（插件要表达「我这条值得庆祝」只需清单里的 `celebration`），
把它暴露成稳定视图等于给「谁看到了哪张卡」发一个契约。插件真正需要的是通知/授予数据，
那两条已有稳定视图。

已知残留两条：① `total` 是 SQL 候选数，可见性过滤后 `items` 可能更少——**前端不得用 `total`
决定是否弹卡**（`items` 为空就不渲染）；② 庆祝跟随 `in_app` 偏好，用户关掉站内渠道时根本没有
行可读，庆祝自然消失（不另做一套偏好）。

## Persistence and Delivery

- `notifications` remains recipient-owned and adds category/payload version plus
  bounded target metadata without changing legacy ids, types, read state, or
  dedupe keys.
- `notification_type_descriptors`, global policies, user preferences, recipient
  revisions, and exact-artifact lifecycle state are additive and CAS/revisioned.
- `mail_deliveries` is the durable email projection and River job authority.
- `notification_channel_deliveries` is the generic external ledger. Its
  `notification_id` is deliberately nullable because `in_app=false` with
  `web_push=true` must not fabricate an inbox row; the row carries a bounded
  payload/target envelope and per-subscription attempt ledger.
- Comment writes create reply/mention inbox rows, deliveries, and jobs inside
  the existing comment transaction. Moderation decisions do the same before
  decision commit.
- New and requeued pending topics/comments create `moderation_pending`
  projections inside the owning Forum transaction. Recipients are active users
  whose current effective RBAC grants `moderation.review`, including
  `super_admin`; a direct deny excludes ordinary users and the submitting actor
  is skipped. Target/revision/recipient dedupe keeps retries idempotent.
- Markdown mentions are discovered from goldmark AST text nodes; fenced and
  inline code are ignored. Duplicate usernames and self-notifications are
  filtered.
- Reply and mention remain separate notification types when both apply.
- One resolver combines active type/channel, Core-required state, admin hard
  limits/recommendations, user `inherit/enabled/disabled`, recipient eligibility,
  and provider availability. Existing Mail policy routes are compatibility
  projections over this authority.

Core email delivery is opt-in by default: recommended restoration keeps
`in_app` enabled and sets `email` disabled for reply, mention, and moderation
types. Migration `202607280071_notification_email_opt_in_defaults.sql` applies
that transition only to untouched V2 rows without a saved legacy choice, so an
operator's explicit existing email policy is not overwritten. Core fanout now
resolves all three channels through the same transaction-scoped layered policy;
a saved user `enabled` override cannot bypass a site-disabled email channel.

## API and UI

Authenticated self-service routes:

- `GET /api/v1/notifications`
- `GET /api/v1/notifications/:id`
- `GET /api/v1/notifications/unread-count`
- `PATCH /api/v1/notifications/:id/read`
- `POST /api/v1/notifications/read-all`
- `GET /api/v1/notifications/stream`
- `GET /api/v1/notifications/celebrations` / `POST /api/v1/notifications/celebrations/seen`
  (celebration display layer; registered before `/:id`)
- `GET|PUT /api/v1/notification-preferences`
- `POST /api/v1/notification-preferences/restore`
- `GET|POST|DELETE /api/v1/web-push/*` (current-user subscriptions only)
- `POST /api/v1/admin/notifications/test` (`settings.manage`, recipient fixed
  to the current actor, type `admin_test`, no email projection)

`settings.notifications.manage` protects the dedicated admin policy, channel
selection/reset/test, and redacted delivery-health APIs. Compatibility
inheritance from `settings.manage` remains; `settings.mail.manage` does not
silently grant notification authority.

The default theme adds `/notifications` and a Navbar unread badge. API queries
always bind the current session user ID; another user's notification appears as
not found.

The inbox and preview use sibling routes: `/notifications` for the list and
`/notifications/:notificationId` for the independent detail surface. Nuxt owns
them through `pages/notifications/index.vue` and
`pages/notifications/[notificationId].vue`; a flat parent page must not shadow
the nested route. Clicking a row opens the detail without leaving the
notification surface. Only the explicit target action navigates to the current
authorized topic or comment.

`GET /api/v1/notifications/:id` binds the recipient to the current actor and
returns 404 for missing or foreign rows. Forum previews are resolved and
re-authorized at read time, so the response contains the current visible topic
title, reply excerpt, author, and parent/original context rather than a stored
body snapshot. The Page Registry identity is `forum.notification.show` with
contract `sforum.page.notification_show@1`; its theme ViewModel supplies only
the shell while the Host detail island retains recipient authorization and
notification data ownership.

The inbox uses server-authoritative category/type/unread filters and cursor
pagination. `useNotifications` shares one EventSource, coalesces refresh signals,
and reconciles through REST on connect/error, tab visibility, and a 30-second
visible-page fallback interval. SSE contains revision signals only; durable
PostgreSQL rows and recipient revision remain truth.

The public Navbar bell opens a notification-domain preview instead of routing
directly to the inbox. Its fixed tabs cover all, reply, and mention notifications;
each tab requests and defensively renders at most the latest three API-ordered
rows, with a persistent hint that complete history remains in the notification
center. The desktop surface is anchored below the bell, while narrow viewports
use a body-teleported bottom sheet so Navbar positioning cannot clip it.
The preview header also exposes the personal notification-preferences route.
On that route, the device-level Browser Notifications section appears before
the per-type preference catalog and cannot shrink inside the desktop settings
scroll column, so its enable/disable state remains visible in the first viewport.

Notification SSE connections use bounded leases and controlled reconnection.
The process-wide PostgreSQL `LISTEN` revision hub owns a cancellable listener
lifecycle. API bootstrap failure and normal shutdown stop and await that hub
before closing the shared pgx pool, so an acquired listener connection cannot
hide the original startup error by blocking `pgxpool.Close()`.
The Nuxt API route owns a dedicated Node stream proxy that destroys both the
upstream request and response when the downstream browser disconnects. The API
also expires every stream after one minute so abandoned proxy connections
cannot retain one of the four per-recipient slots indefinitely. The client
closes `EventSource` after any error, reconnects with exponential backoff capped
at 30 seconds, and disposes the source during Vite HMR; native unbounded
`EventSource` retry is not relied on. REST reconciliation remains available
during every disconnect.

The inbox and independent detail page share `SFNotificationTypeNav` in the
desktop left rail and mobile navigation drawer. Both surfaces use the same
loaded-scope count semantics and fixed type/icon catalog. On a detail page the
current notification type is highlighted; selecting any type restores the
existing inbox filter state and returns to `/notifications`.

The notification page uses the canonical `topic.create` permission helper for
the shared forum navigation. Below the desktop right-rail breakpoint, the same
unread summary/current-detail component is shown in the right mobile drawer;
do not hide the drawer instance with the desktop rail media rule.

Notification list sources are type-aware. Core `reply` and `mention` rows carry
a current actor summary and use the actor's configured `AvatarView`; moderation
results, admin tests, actorless plugin notifications, and unknown types use a
bounded Tabler icon fallback instead of fabricated notification-name initials.
The actor summary is populated in the recipient-owned list/detail store only
for user-authored Core types and is cleared together with actor/target/payload
when target re-authorization fails.

Forum targets are re-authorized at read time. Hidden/deleted/non-public topics,
inactive comments, unknown targets, and resolver errors fail closed by clearing
actor, payload, target id/type/path and returning `targetAvailable=false`.
`moderation_pending` targets are separately re-authorized against the current
actor's `moderation.review` permission and open the stable pre-publication
workbench query. Losing review authority makes the target unavailable even when
the recipient still owns the historical notification row.

Comment deep links carry the flat-view page number: `ResolveNotificationTarget`
emits `/t/{topic}/page/{N}#comment-{id}` for page N>1 (page 1 keeps the legacy
`/t/{topic}#comment-{id}` form), computed with the same public-scope
`CountCommentsBefore` + `comments_per_page` + `MaxTopicPage` clamp as
`ResolveCommentPage` and profile activity links. The topic page's slug/mode
normalization redirect preserves both the `/page/N` segment and the hash so
id_slug/slug sites do not drop the page for cross-page notification clicks.
The same-page stale-list gap is closed in `useTopicCommentAnchor`: when the
anchor target should be on the current page but is missing from the loaded
list (a new reply arrived without a live append), the composable forces one
`refreshComments()` per anchor id, letting its watch then scroll and flash.

Admin policy and external-channel management live as tabs under the unified
`/control-panel/settings/mail` surface; the old
`/control-panel/settings/notifications` URL redirects to the corresponding
tab. Personal preferences and Web Push live at `/settings/notifications`. The
selected Web Push provider is `sforum.web-push`; VAPID secrets remain in
SecretStore and API responses expose only `secretSet` plus the public key. The
Host worker is fixed at `/_sforum/notifications/sw.js` with scope
`/_sforum/notifications/`, imports no plugin code, and accepts only bounded
same-origin click paths.

The personal subscription list exposes active devices only. Revoked rows stay
in the server-side audit and delivery relationships but disappear from the
settings UI. Browser-enabled state requires both a live browser
`PushSubscription` and its matching active Host subscription id, so stale local
storage cannot make a revoked device appear enabled. Untouched Core reply,
mention, and moderation Web Push policies default to enabled/recommended;
`admin_test` remains disabled, and migrations plus restore-default behavior
preserve explicit operator choices.

The unified admin route uses the shared settings geometry. Type Policy and
External Channels remain independent fixed Core tabs and focused panels beside
the mail tabs. The route shell owns tab/query state and refreshes only the
active tab, while API policy and `settings.notifications.manage` remain
authoritative and do not grant mail access.

The admin policy row uses a dedicated email-notification control. Disabling a
channel also clears its recommended state. Personal settings render a chooser
only for active, site-enabled, available, user-configurable channels; a
site-disabled email row renders a managed-state badge instead, even when a
historical user override remains stored. The shared settings mobile navigation
constrains its category select to the content width to prevent horizontal
overflow at `390x844`.

M8 moderation approval replay re-reads `source_format` and projects editor
documents through the Host public side-effect view before mention fanout.
Protected descendants therefore do not produce mention notifications or leak
through notification previews. Topic/comment write tests prove only public
mentions enter transactional fanout; protected child markers, usernames, and
URLs remain absent from observe envelopes and their webhook serialization.

Digests, scheduled summaries, unsubscribe-link semantics, broadcast/marketing,
and additional vendor channels remain deferred.

M10B re-ran protected shortcode mention/notification leakage checks: only the
public side-effect projection enters fanout, and protected descendants remain
absent from notifications, observe events, and webhook projections. Release
completion remains blocked by immutable artifact promotion.
