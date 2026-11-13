# 决策：徽章授予通知补全（四条路径）+ 站内庆祝展示层

日期：2026-11-13
状态：accepted
上游：`decisions/2026-11-10-badges-and-user-activity.md`（通知类型与去重键的初版）、
`decisions/2026-11-11-badges-public-catalog-and-series-closeout.md`（D35–D38，本文件**不改写**其中任何一条）、
`plans/archive/2026-07/2026-07-27-notification-platform-v2.md`（通知平台分层与插件扩展面）

## 背景：一条路径有通知，三条路径静默

`badge_granted` 自 2026-11-10 起就存在，但当时只接了**自动评估 job** 这一条出口
（`Jobs/Badges/evaluate_user.go` 的 `Notifier`）。运营真正会用的另外三条路径——
后台手动授予、批量授予、全量重算——在库里写入了 `user_badges` 行，**不写任何通知行**。
症状是「用户拿到徽章、收件箱里什么都没有」，而运营看到的是「授予成功」。
批量重算尤其糟：一次点击可能补出几百枚徽章，全部静默。

同时 `/badges` 目录页已经把「获得条件」以自然语言下发，但「我还差多少」只能靠用户自己算。
徽章授予是**低频、高情绪**事件（不像点赞），值得在站内弹一次庆祝卡——但当时没有任何
「这条通知值得庆祝」的表达方式。

## 裁决

### D1 通知出口是**领域侧端口**，`Badges` 不 import `Notifications`

`Models/Badges/grant_notifications.go` 定义 `GrantedBadge{ID,Slug,Name}` 与
`GrantNotifier` 端口，`AdminService.SetGrantNotifier` 注入；实现落在
`bootstrap/badges_wiring.go`（`badgeGrantedNotifier` 是唯一实现，两个薄适配器只做类型转换）。

理由：`Badges → Notifications` 一旦成立，一条「发通知」的需求会把邮件模板、收件箱策略、
渠道解析、偏好解析全部拖进徽章包，而模块笔记的 Dependency Direction 明确写着反向依赖不存在。
`GrantedBadge` 只有三个字段也是裁决的一部分：把完整 `Badge` 传进通知层，会让「通知能读什么」
这条边界随时间膨胀（今天读 name，明天读 rule，后天就有人读 `requirement_summary`）。

未注入（nil）= **不发通知且不是错误**：徽章授予与通知能力解耦，缺装配时的正确行为是
「授予成功、没有通知」，不是让运营的授予请求失败。

### D2 通知失败只 `slog.Warn`，不回滚授予

`notifyGranted` 吞掉错误。理由不是「怕麻烦」，而是**没有可回滚的事务边界**：
`GrantManual` 每条都是自动提交的独立语句，此时把错误上抛只会让运营看到「授予失败」
而库里明明有授予行——本模块反复出现的假状态之一。

### D3 `BatchGrant` 只对 `grantedUsers` 发，`skipped` 不发

批量授予的三态是 `granted` / `skipped` / `failed`，只有 `granted` 进 `grantedUsers`。
`skipped` 是「本来就持有」，再发一次等于凭重复点击刷通知；去重键会挡住，但**那是第二道闸**，
不该靠它兜住语义错误。

### D4 `Recompute` 的上限是 200，超限**整体不发**

`recomputeNotifyLimit = 200`。超过就一条都不发，只留一条 warn。
不采用「发前 200 条」：部分发送会让「谁被通知到」取决于行扫描顺序，运营既解释不了也复现不了。
warn 是必须的——否则「用户说没收到通知」会被当成缺陷去查。

`grantFromRulesCollectLimit = 1000`（`postgres_store.go`）**不是第二道产品阈值**，只是
`RETURNING` 行的收集上限：它只需「足够判出 200 已被突破」，同时把十万行授予的内存压成常数。
**唯一判据永远是 200**。改任何一个数之前先读这两个常量的注释。

自动评估 job 不受 200 约束（每次最多几枚），走自己的出口。

### D5 去重键仍是 `badge:{slug}`；「按用户聚合」只减少出口调用次数

`Notifications/fanout.go` 的 `NotifyBadgeGrantedTx` 用 `dedupe := "badge:" + slug`。
语义后果：同一用户同一徽章**终身只通知一次**，收回后重新授予也不再通知（徽章是终局荣誉）。

`notifyRecomputeGrants` 先按用户聚合再调用出口，这**只减少出口调用次数**
（一次调用带该用户的 N 枚）。`badgeGrantedNotifier.notify` 内部仍逐枚调
`NotifyBadgeGranted`，所以投影仍是**一枚徽章一条通知行**。不要照这段注释把它优化成
「一条通知列多枚」——那会改掉收件箱的既有语义。

### D6 `TargetType="badge"`；`BadgeID<=0` 不写目标；目标解析 fail-closed

- `BadgeGrantedEvent.BadgeID` 是唯一的目标来源。`BadgeID <= 0` 时 `targetType` 留空、
  `targetID = 0`，收件箱渲染成「无链接的纯文本通知」，而不是指向 `/badges/0` 的死链。
- `Badges/notification_targets.go` 的 `ResolveNotificationBadgeTarget` 做三重可见性校验
  （徽章仍启用 + 用户仍持有授予行 + 徽章所在等级仍启用），任一不满足返回 `(false,"",nil)`
  而不是报错：这是**读路径装饰**，一次解析失败只该让这一个链接消失，不该把整个收件箱变成 500。
- 目标固定是 `/u/{username}?tab=badges`（查看者自己的资料页徽章 tab），不是公开目录页：
  目录页只标 `earned`，点进去看不到「我拿到了什么」。用户名走 `url.PathEscape`，
  不依赖当前用户名规则。
- `bootstrap/notification_adapter.go` 按 `targetType` 分流：`"badge"` 走徽章域，
  `moderation_*` 走审核域，其余仍走 Forum。**不加这条分支的症状是徽章通知整条变
  `targetAvailable=false`**（Forum 的解析器不认识 `badge`）。

### D7 庆祝是**描述符上的展示层**：`presentation.celebration`，不加列、不新增 Host 命令

迁移 `202611130002_notification_celebrations.sql` 只做两件事：给 `badge_granted` 的
`presentation` jsonb 打 `{"celebration":true}`（`presentation || …` 保留既有 icon 等键），
以及建 `user_celebration_cursors`。

为什么不加列：插件也要能声明庆祝（`manifest-v3.schema.json` 的 `notificationTypes[].celebration`，
`ManifestNotificationType.Celebration`，落库在 `registry.go` 的 `presentation` 里）。
读面判据统一是 `presentation->>'celebration' = 'true'`——加列就会变成「Core 列 + 插件 jsonb」
两套判据。Core 侧 `coreTypeDescriptors()` 里 `Celebration: value.id == TypeBadgeGranted`：
回复/提及/审核结果是流程的一部分，弹庆祝卡会变成噪音。

**没有新增 Host 命令**：读面恒为 Core 的 `GET /notifications/celebrations`，插件只能声明
「我这条值得庆祝」，不能自建庆祝读面。这与「插件数据面只走四个受批面」的纪律一致。

### D8 首访不回放历史

`ListCelebrations` 在 `initialized == false` 时返回 `initialized=false` + **空 items** +
`cursorId = LatestNotificationID(user)`，并且**不写游标行**。

不写行是关键：写了 `initialized` 就变 true，而前端要靠 false 触发「先 ack 种子游标、本轮不再拉取」这一步
（`useCelebrations` 的 `refresh`）。老用户升级后不该被几个月前的徽章刷屏。

### D9 7 天窗 + `id > cursor` + 目标不可见整条丢弃

- 窗口 `celebrationWindow = 7 * 24h`，SQL 里用 `now() - interval`：这里比的是**时间戳**，
  不是「哪一天」的日历界，所以不违反「按日取界必须 Go 传参」那条纪律。
- 游标是硬边界（`n.id > $2`），ack 用 `GREATEST` 语义（`AcknowledgeCelebrationCursor`）：
  前端 ack 是尽力而为的，两条 ack 乱序到达时「置等于」会把已经看过的新通知重新弹一遍。
- 可见性复用 `ResolveSafeTargets`，**目标不可见就整条丢弃**，不返回 `targetPath=null` 的空卡。
- `DefaultCelebrationLimit = 3`，`total` 封顶 `celebrationTotalCap = 99`：`total` 是聚合文案里
  的数字，不是分页总数，封顶避免「已获得 4823 枚」这种无信息量读数。

### D10 Core 兜底文案在服务端，且**逐语言**回落

`coreCelebrationFallback` 只给 `badge_granted` 两句话（`徽章` / `你获得了新徽章`）。
存在的理由：Core 描述符的 `presentation` 只有 icon（`202611100002` 的种子就是
`{"icon":"i-lucide-award"}`），前端在 i18n 模板缺参时回落到 `body` 纯文本，会得到空串或
描述符默认值 `Notification`——前者是空卡，后者是错误文案。

`presentCelebration` 逐语言回落（描述符只补了中文时英文仍拿到实义文案），而不是整体二选一。
它**不承担翻译职责**：正常路径仍是前端 i18n 的 `titleKey`/`bodyKey`。
这段兜底**不放** `Support/Localization/messages.go`——那张表是错误码文案，塞展示文本会让
「每个错误码都有双语文案」的完整性测试被无关文本污染。

### D11 前端三态文案链：i18n 命中 → 服务端兜底 → 吃掉占位符

`celebrationPresentation.ts`（纯函数）：
`translateComplete(titleKey)`（`te` 为真 **且** 译完没有残留 `{name}`）→
`formatCelebrationTemplate(服务端 label/body)` → 类型名键 → 通用键 → `translateEaten`。
最后一步宁可断句也不把 `{badgeName}` 画到页面上（`formatCelebrationTemplate` 顺带收拾
缺参留下的空引号与多余空白）。

`SFCelebrationHost.vue` 固定 `bottom-4 left-4`：`app.vue` 的 toast 在右下角，两者不能叠。
宿主只在四个条件同时成立时存在：已登录、站点开启徽章功能、当前不是管理端路由、页面可见。
`items` 为空时不渲染任何 DOM。

## 被否方案

| 方案 | 为什么否 |
| --- | --- |
| 四条授予路径各自写一份通知代码 | 同一判断四份实现；`badgeGrantedNotifier` 现在只有一份 `notify`，适配器只做类型转换 |
| `Badges` 直接 import `Notifications` | 反向依赖会把邮件模板/渠道解析拖进徽章包（见 D1） |
| 通知失败上抛、让授予「失败」 | 没有可回滚的事务边界；只会造出「报错但库里有行」的假状态 |
| 重算「发前 200 条」 | 谁被通知到取决于扫描顺序，不可解释、不可复现 |
| 按 `user_badge_id` 去重 | 要改 `GrantAutomatic` 的签名，把「授予是否新建」与「通知去重键」耦合；且收回再授予会重复通知，与「终局荣誉」口径冲突 |
| 给 `notification_type_descriptors` 加 `celebration` 列 | 插件声明落在 `presentation` jsonb，加列 = 两套判据（见 D7） |
| 新增 Host 命令 `notifications.celebrate@1` | 庆祝是**展示层**，不是新的写入路径；新增命令会打开「插件自建读面」的口子 |
| 庆祝自带独立偏好开关 | 庆祝读的是 `notifications` 行，关掉 `in_app` 就没有行可读；另做一套偏好等于让「关掉站内通知但仍被弹卡」成为可能 |
| 首访回放 7 天历史 | 老用户升级后被几个月前的徽章刷屏（见 D8） |
| 前端本地存游标 | 跨设备不一致；服务端 `GREATEST` 已经是幂等答案 |

## 陷阱（现象 → 根因 → 判据）

**T1 「按用户聚合」被读成「一条通知列多枚」**
现象：有人为了「减少通知条数」把重算路径改成一条通知带 N 枚徽章。
根因：`notifyRecomputeGrants` 的注释说的是「减少出口调用次数」，聚合发生在**调用层**，
投影层（`NotifyBadgeGrantedTx`）仍逐枚写行，去重键是 `badge:{slug}`。
判据：同一用户拿到 3 枚徽章后，`notifications` 里必须是 **3 行**，且 `dedupe_key` 三个不同值。
断言要查行数，不要查「出口被调用了几次」。

**T2 `initialized` 语义写反**
现象：老用户登录被历史徽章刷屏，或新用户永远看不到庆祝。
根因：首访分支**写**了游标行（或前端把 `initialized=false` 当成「没有庆祝」直接跳过）。
判据：`GET /notifications/celebrations` 首访必须返回 `initialized=false` + `items: []` +
`cursorId` = 该用户当前最大通知 id（无通知为 0），且 `user_celebration_cursors` **没有该用户的行**；
第二次（ack 后）才 `initialized=true`。两个方向都要断言。

**T3 `total` 被当成条目数**
现象：前端用 `total` 判断「有没有庆祝」，于是 `items` 为空时仍弹一张空卡/一张只剩关闭按钮的卡。
根因：`total` 是 **SQL 候选数**（封顶 99），`items` 是过了目标可见性之后的子集——
徽章被收回/停用会让条目整条消失。
判据：构造「有候选、全部目标不可见」的夹具，响应必须是 `total > 0` 且 `items == []`，
前端不得渲染庆祝 DOM。

**T4 庆祝跟随 `in_app` 偏好，被当成 bug**
现象：用户关掉站内通知后，拿到徽章没有庆祝卡，被报成「庆祝不生效」。
根因：庆祝读的是 `notifications` 行，`in_app=false` 时根本没有行。
判据：这是设计内行为，写进文案与残留清单；不要为它加第二套偏好。

**T5 `badge` 目标类型漏进 Forum 解析器**
现象：徽章通知在收件箱里显示「目标不可用」，或点了没反应。
根因：`notificationTargetVisibilityAdapter.ResolveNotificationTarget` 少了 `targetType == "badge"`
分支，于是走 `forum.ResolveNotificationTarget`，Forum 不认识这个类型。
判据：`targetType="badge"` 的 `TargetType` 必须由徽章域判定；注入 `badges == nil` 时应
`(false,"",nil)`（fail-closed），不是 panic、不是 500。

**T6 空卡**
现象：庆祝卡弹出来只有图标，没有标题/正文。
根因：Core 描述符 `presentation` 只有 icon，前端 i18n 缺键或模板缺参时回落到空 `body`。
判据：值级断言——`presentCelebration` 对 `badge_granted` 必须给出 zh/en 都非空的
`label`/`body`；前端 `celebrationCardPresentation` 在 title 与 body 都为空时返回 `null`
（宁可不画卡，也不画空白浮层）。

**T7 四条路径只补了三条**
现象：手动授予有通知、批量有通知、重算有通知，但规则自动授予没通知（或反之）。
根因：job 侧用 `badgesjobs.GrantedBadge`、领域侧用 `badges.GrantedBadge`，两套类型两个适配器；
只改一边时另一边静默。

**判据：两条装配边行级 + 两条领域边端口级**（2026-11-13 审查后订正；原文写「四条都查
`notifications` 行」，实测只有两条是行级，见
`../reports/2026-11-13-badge-notify-wear-progress-review.md` 的 R7）。

| 边 | 路径 | 断言层级 | 断言什么 |
| --- | --- | --- | --- |
| 装配边 | 管理员手动 `Grant` | **行级**（真库真实 `notifications` 行） | `TestManualBadgeGrantWritesClickableBadgeNotification`：恰好 1 行、`target_type='badge'`、`target_id`、payload 的 `badgeId`/`badgeName`、目标可解析成 `/u/{username}?tab=badges` |
| 装配边 | 规则自动 job | **行级** | `TestBadgesJobNotifierForwardsBadgeID`：job 适配器把 `ID` 传下去（漏传的表现正是「自动授予的通知不可点」） |
| 领域边 | `BatchGrant` | **端口级**（fake `GrantNotifier`） | `TestBatchGrantNotifiesOnlyNewGrants`：48 次调用 × 每次 1 枚、只含 `grantedUsers` |
| 领域边 | `Recompute` | **端口级** | `TestRecompute{NotifiesWithinLimit,SkipsNotificationsOverLimit,CallsNotifierOncePerUser}`：≤200 逐条、>200 **零**调用、出口调用次数 = 受影响**用户数** |

**为什么批量/重算不做成行级**：它们的出口在 `bootstrap` 装配，走真库会把**200 条上限的语义**
与**投递投影**耦在一起——要验「超限整体不发」就得造 201 条真实授予 + 201 行通知，而投影本身的
正确性（去重键、payload 形状、渠道）已由 `notifications` 包与装配边的行级用例覆盖。
端口级才能精确断言「发了几次、给谁、发了哪几枚」，且不受去重键与投递实现影响。

**复合覆盖成立的前提**：写通知的循环只有一份实现（`bootstrap/badges_wiring.go:81-96` 的
`badgeGrantedNotifier.notify`），两个适配器只做类型转换。所以「两条行级 + 两条端口级」不是
四份互相独立的证据，而是「同一实现的两端各有端到端证据 + 两条只差类型转换的边有值级证据」。
**新增第五条第授予路径时，先判断它走哪条边**：装配边要有行级用例，领域边要有端口级值断言。

## 复核点

1. 新增授予路径时先问「它走 `AdminService` 还是 job」：前者注入 `GrantNotifier`，后者注入
   `badgesjobs.GrantedNotifier`，**不要**在两处各写一份写通知的循环。
2. 任何「让庆祝自带偏好/让插件自建庆祝读面」的改动都要回到 D7 的裁决。
3. `recomputeNotifyLimit`（200）与 `grantFromRulesCollectLimit`（1000）不是同一类常量，
   改前者是产品决策，改后者只是内存预算。
