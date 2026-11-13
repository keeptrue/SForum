# 徽章三件事（通知 / 庆祝 / 佩戴 / 进度）独立验收报告

- 审查对象：SForum 工作树（未 commit），`git status --short` 共 91 项（63 改 / 28 新增）
- 审查者：独立审查（只读，未改业务代码 / `contracts/` / `docs/extensions/`）
- 审查时间：2026-11-13
- 基准：`knowledge/decisions/2026-11-13-badge-{notification-and-celebration,worn-display,progress-from-rule}.md` 与模块笔记

## 判定

**PASS-with-residual**

四条验收轴（通知 / 庆祝 / 佩戴 / 进度）在代码、契约、生成物、测试四层全部对齐，本轮要求的命令全部实跑通过（Go 7 个包 + web 2816 用例 + typecheck + 4 个静态门），未发现 P0/P1；残留 7 项均为「覆盖不对称 / 文档口径强于实测 / 自愈型脏标记」级别，不影响用户可见行为。

---

## 逐条核对表

### 1. 通知（四条路径）

| 条目 | 结论 | 证据 |
|---|---|---|
| 手动授予发 1 条 | PASS | `app/Models/Badges/admin_service.go:311`（授予落库、`RecomputeTopBadge` 之后调 `notifyGranted`）；`TestGrantNotifiesExactlyOnce` PASS（断言 `len(calls)==1` 且 `granted[0]` 的 id/slug/name 三件套） |
| 手动授予重复不刷屏 | PASS | 已持有返回 `ErrBadgeAlreadyGranted` 早于通知；`TestGrantSkipsNotificationWhenAlreadyGranted` PASS |
| 批量授予只对新授予发（skipped 不发） | PASS | `grant_batch.go:102-108` 通知在 `grantedUsers` 循环内；`TestBatchGrantNotifiesOnlyNewGrants` PASS（50 人 / 48 新 / 2 skipped → 48 次调用，且显式断言 user 1、2 不在其中） |
| 重算 ≤200 逐条发 | PASS | `admin_service.go:365`（`recomputeNotifyLimit = 200`）+ `:442-478`（`notifyRecomputeGrants`）；`TestRecomputeNotifiesWithinLimit` PASS |
| 重算 >200 整体不发 | PASS | `admin_service.go:446-450` 超限只 `slog.Warn` 后 return；`TestRecomputeSkipsNotificationsOverLimit` PASS（201 条 → 0 次调用） |
| >200 时 `granted` 计数不变 | PASS | 同一测试断言 `result.Granted == 201`（阈值只影响通知） |
| `badge` 目标 fail-closed：未持有 | PASS | `notification_targets.go:30-42`（`JOIN user_badges` + `users.status='active'`）；`TestBadgeNotificationTargetIgnoresForeignUser` PASS |
| fail-closed：徽章停用 | PASS | 同上 `badges.enabled`；`TestBadgeNotificationTargetFailsClosedWhenBadgeDisabled` PASS |
| fail-closed：档位停用 | PASS | 同上 `JOIN badge_tiers t ... AND t.enabled`；`TestBadgeNotificationTargetFailsClosedWhenTierDisabled` PASS |
| 收回后 fail-closed | PASS | `TestBadgeNotificationTargetFailsClosedAfterRevoke` PASS（且断言 `(false,"",nil)`，不报错） |
| 关闭 `in_app` 后无通知行 | PASS（口径见残留 R3） | `outbox.go:204-243` → `policy.go:29-55`；`TestNotifyBadgeGrantedTxHonoursDisabledInApp` PASS（`count(*)==0`） |
| `Recompute` 响应严格三键 | PASS | `controller_test.go:229-252` 精确比对 `len(got)==len(want)`，`want = {granted,refreshed,queued}`；`TestRecomputeResponseCarriesAllCounters` PASS |
| 通知失败不回滚授予 | PASS | `grant_notifications.go:47-54`；`TestGrantNotificationFailureDoesNotFailGrant` PASS |
| 未装配 notifier 不报错 | PASS | `grant_notifications.go:48`（nil 早退）；`TestGrantSucceedsWithoutNotifier` PASS |
| 两条出口共用一份写通知实现 | PASS | `bootstrap/badges_wiring.go:81-114`（`notify` 一份 + 两个薄适配器）；`TestBadgesJobNotifierForwardsBadgeID` PASS（job 侧也带 `target=badge/ID`） |

### 2. 庆祝（展示层）

| 条目 | 结论 | 证据 |
|---|---|---|
| core 路径 label/body | PASS | `celebrations.go:124-152`（`coreCelebrationFallback` 逐语言回落）+ `:233-273`（`presentCelebration` 投影 `titleKey/bodyKey`）；`TestCelebrationWireShapeMatchesFrozenContract` PASS（断言 `label={徽章,Badge}`、body 非空且 zh≠en、`titleKey=celebrations.badge_granted.title`） |
| 插件路径 label/body | PASS | `registry.go:354`（`presentation.celebration` 落 jsonb）+ `:465`（Core 只标 `badge_granted`）+ `celebrations.go:138-143`（插件 `titleKey/bodyKey` 为 nil）；`TestPluginCelebrationUsesDescriptorText` PASS（描述符文案逐字命中、`Params` 不透传 payload） |
| `GREATEST` 游标不倒退 | PASS | `celebrations_store.go:103-119`；`TestAcknowledgeCelebrationsNeverRewinds` PASS（50→20 仍 50；80→80；-5 仍 80） |
| 首访 `initialized=false` + items 空 | PASS | `celebrations.go:185-197`；`TestListCelebrationsFirstVisitSeedsCursorWithoutItems` PASS |
| 首访**不写**游标行 | PASS | 同上（只读 `LatestNotificationID`）；同一测试复查 `CelebrationCursor` → `initialized==false` |
| 目标不可见整条丢弃（无 `targetPath:null`） | PASS | `celebrations.go:216-221` `continue`；`TestListCelebrationsDropsUnavailableTargets` PASS（不可见 0 条 / 可见 1 条，双向）；`TestCelebrationWireShapeMatchesFrozenContract` 对 `targetPath` 做 `Unmarshal` 成 string，null 会直接红 |
| `ResolveSafeTargets` 保序不筛行（`rows[index]` 对齐正确） | PASS | `presentation.go:18-48` 原地改写、长度不变；`ListCelebrations` 的 `rows[index]` 因此不会错位 |
| 前端 i18n 命中 | PASS | `celebrationPresentation.ts:145-152`；`celebrationHost.test.ts`「titleKey and bodyKey hits use the locale copy」PASS |
| 服务端兜底（键未命中） | PASS | 同文件 148-152；「a key the locale does not know falls back to the server label」PASS |
| 参数缺失三态 | PASS | `translateComplete` 的「空串 / 残留 `{name}` 都算未命中」；「an unsatisfied template falls back…」+「the eaten-placeholder formatter stays as the last line of defence」PASS |
| 未登录不产出 DOM | PASS | `SFCelebrationHost.vue:36-41` `allowed` 含 `Boolean(user.value)`；「a guest never mounts the card」PASS |
| feature 关闭不产出 DOM | PASS | 同上 `badgesEnabled`；「badges disabled hides the card」PASS |
| 无 item 不产出 DOM | PASS | `celebrationCardPresentation` 空返回 null；「no item renders no celebration DOM at all」PASS |
| 管理端路由不产出 DOM | PASS | `adminRoutes.routeId(route.path) !== null`；「an admin route hides the card」PASS |
| 路由顺序（`/celebrations` 不被 `:id` 吞） | PASS | `Notifications/controller.go:89-91`；`TestCelebrationsRouteIsNotSwallowedByDetail` PASS |
| 缓存头 private | PASS | `Notifications/controller.go:471-473, 493-495`；`TestCelebrationFeedWireAndCacheHeaders` / `TestCelebrationAckReturnsStoredCursor` 均断言 `private, no-store` |
| 装配缺失 503（不伪装空列表） | PASS | `Notifications/controller.go:464-466`；`TestCelebrationsUnavailableWhenStoreLacksCapability` PASS |

### 3. 佩戴

| 条目 | 结论 | 证据 |
|---|---|---|
| 迁移 `202611130003` 可逆（up→down→up 真跑） | PASS | `database/migrations/badge_wear_migration_test.go:75-176` 在隔离 schema 真跑三段，断言列出现 / 既有行 `DEFAULT FALSE` / `top_badge_id` 未被误伤 / down 后列消失 / 二次 up 后回到默认 FALSE；`TestBadgeWearMigrationUpDownUp` PASS |
| 迁移 `202611130002` 可逆（up→down→up 真跑） | PASS | `notification_celebrations_migration_test.go:100-200` 真跑，且断言 `presentation \|\| {...}` 保留 icon、down 只 `- 'celebration'`、不误伤其它类型；`TestCelebrationMigrationUpDownUp` PASS |
| `SetWornBadge` 三重校验 | PASS | `wear_store.go:72-99` 单条 CTE（`badges.enabled` + `badge_tiers.enabled` + `user_badges` 持有）；`TestSetWornBadgeRejectsWhatTheUserDoesNotOwn` PASS（5 个拒绝输入 + 反证「授予后同 slug 成功」） |
| 拒绝不写库 | PASS | 同一测试断言 `(top=gold, manual=false)` 未被改动 |
| `RecomputeTopBadge` 不覆盖有效 manual | PASS | `postgres_store.go:449-478`；`TestSetWornBadgeIsKeptByBothRecomputePaths` PASS（且顺带钉住 `TopBadgesFor` chip 读到 silver） |
| `RecomputeAll` 不覆盖有效 manual | PASS | `postgres_store.go:596-635`（`manual_valid` 分支）；同一测试 PASS |
| 失效回落 + 清标记：徽章停用 | PASS | `wornBadgeStillValidSQL`（`postgres_store.go:435-448`，`badges.enabled`）；`TestRecomputeFallsBackWhenWornBadgeStopsBeingValid/badge_disabled` PASS |
| 失效回落 + 清标记：收回 | PASS | 同上（`EXISTS user_badges`）；`/grant_revoked` PASS |
| 失效回落 + 清标记：删除 | PASS | 同上（授予行级联消失 → EXISTS 失败）；`/badge_deleted` PASS（FK `ON DELETE SET NULL` 后仍回落到 gold 且清标记） |
| 失效回落 + 清标记：档位停用 | PASS | 同上（`badge_tiers.enabled`）；`/tier_disabled` PASS |
| `RecomputeAll` 失效回落（独立 SQL 分支） | PASS（覆盖 1/4，见残留 R2） | `TestRecomputeAllClearsInvalidWornBadge` PASS（`refreshed==1`、manual 清成 FALSE、回落 gold、二次跑 `refreshed==0`） |
| Forum chip 读路径未改坏 | PASS | `postgres_store.go` 未触碰 `TopBadgesFor`；`TestSetWornBadgeIsKeptByBothRecomputePaths` 断言 chip 读到佩戴值；`TestDecorateTopicsFillsTopBadgeInOneQuery` / `TestDecorateCommentsFillsTopBadge` 均 PASS |
| 401 早于 body 解析 | PASS | `wear_controller.go:94-99`（`h.actor` 先于 `Bind`）；`TestWornBadgeEndpointsRequireAuth` PASS（含坏 body 仍 401） |
| 422 + `forum.badge_not_owned` | PASS | `controller.go:805-807` + `errors.go:75-84`；`TestWornBadgePutRejectsUnownedBadge` PASS（并显式钉住字面量 `forum.badge_not_owned`） |
| 400 + 专属码（body 非法） | PASS | `wear_controller.go:99-103`；`TestWornBadgePutRejectsMalformedBody` PASS |
| wire 形状 `tierName{zh,en}` + `tierColor`（不是 `tierAppearance{NameZh}`） | PASS | `dto.go:115-151`（`badgeDisplayRef` 显式投影）；`TestWornBadgePutResponseUsesFlattenedTierShape` 在**响应字节**上断言 `tierAppearance`/`NameZh`/`NameEn` 不出现、`tierName` 键集恰为 `{zh,en}`、值 `银牌/Silver`、`tierColor=slate` PASS |
| 三键（含 null）永不省略 | PASS | `badgeDisplayResponse`（`dto.go:128-134`）无 `omitempty`；`TestWornBadgeGetKeepsNullFieldsForEmptyWall` 断言原始字节含 `"worn":null` 等三条 PASS |
| 静态段先于参数段 | PASS | `controller.go:96-103`；`TestWornBadgeRoutesAreRegisteredAsStaticSegments` PASS |
| feature 关闭 403 | PASS | `TestWornBadgeEndpointsHonorFeatureGate` PASS（GET/PUT/DELETE 三条） |
| 读失败 500（不伪装空态） | PASS | `TestWornBadgeReadFailureIsServerError` PASS |

### 4. 进度

| 条目 | 结论 | 证据 |
|---|---|---|
| `metric` 为 NULL 的规则徽章在资料页有**值级**进度 | PASS | `types.go:153`（`Measure: measureForRule(b.Rule)`）+ `service.go:104-122`；`TestProfileBadgesEmitRuleDerivedProgress` PASS：`current` 断言为数字 `2`、`threshold` 为数字 `10`（注释明写「不能只是键存在」） |
| 进度不再来自 legacy `metric/threshold` | PASS | `service.go` 删除 `ActivityCounters`/`progressFor`；`postgres_store.go:240-246` 删除 `item.Progress.Threshold = *badge.Threshold`；`TestMeasureProgressReadsRuleOnlyBadges` PASS |
| `lte` 无进度 | PASS | `progress_measure.go:34-58`（只收 `CompareGTE`）；`TestProfileBadgesOmitProgressForUnmeasurableRules/lte` PASS（且断言 `measurer.calls==0`） |
| 多条件组无进度 | PASS | 同函数 `root.IsGroup()` 即 nil；`/all-group` PASS |
| 布尔条件无进度 | PASS | 同函数 `Cmp == nil` 即 nil + `ValueKind != ValueCount`；`/boolean` PASS |
| 目录页登录有进度 | PASS | `controller.go:228-237`；`TestPublicCatalogRealServiceMergesProgressOnlyForViewer` PASS（`current==2`/`threshold==10` 值级） |
| 目录页匿名无进度 | PASS | 同函数 `if actor, ok := h.viewer(c); ok` 之外不取；`TestPublicCatalogAnonymousSkipsProgress` PASS |
| 匿名**不查库** | PASS | 代码路径：`publicCatalog` 匿名分支完全不进 `ViewerCatalogProgress`；`Service.ViewerCatalogProgress` 另有 `userID <= 0` 早退（`public_catalog.go:97-101`）。`TestPublicCatalogRealServiceMergesProgressOnlyForViewer` 断言 `store.userBadgeReads == 0` PASS |
| earned 行两处都不输出这两个键 | PASS | `dto.go:91-95`（profile：earned 直接 return）+ `dto.go:244-250`（catalog：`if !granted`）；`TestPublicCatalogMergesViewerProgress`（earned 的 `first-post` 即使夹具带进度也不输出）+ `TestProfileItemProgressKeepsFlatShape` 均 PASS |
| `/badges` 缓存头仍 `private, no-store` | PASS | `controller.go:238` → `setViewerScopedNoStore`；`TestPublicBadgeCatalogIsPrivateCacheable` 做**精确相等**断言（不是包含）+ `TestPublicCatalogProgressKeepsPrivateCacheHeader` PASS |
| 阈值不进公开定义面 | PASS | `PublicBadge.Measure` 为 `json:"-"`（`types.go:130`）；`TestPublicBadgeMeasureNeverSerializes` PASS（序列化后不含 `measure`/`leaf`/`"threshold":10`）；`TestPublicCatalogDTOCarriesNoDefinitionMetricField` 反射核对 `PublicCatalogEntry` 无 `Threshold`/`Metric` 字段 PASS |

### 5. 契约与生成物

| 条目 | 结论 | 证据 |
|---|---|---|
| `registry_test` 计数 433 | PASS | `Support/Routes/registry_test.go:528-535`（428 + 5，注释逐条说明 5 条路由的 guard 口径）；`TestCoreRouteCatalogHasExactReviewedGuardParity` PASS |
| `catalog-identities.json` 五条与 Go 注册逐字一致 | PASS | `docs/extensions/v3/catalog-identities.json` 新增 5 条（`badges.get/set/clear_worn_badge`、`notifications.list/acknowledge_celebrations`），与 `core_catalog_gen.go:297-299, 347-348` 的 id/contractVersion/method/path 逐字相同；`node scripts/v3-catalog/generate.mjs --check` → `V3 P0 catalogs are current (18 files)` |
| `extension docs --check` 无 drift | PASS | `go run ./cmd/sforum extension docs generate --check` → `OK catalog docs in sync: .../docs/extensions/catalogs` |
| openapi refs 通过 | PASS | `ruby scripts/validate-openapi-refs.rb` → `checked 3377 refs across 65 files` |
| 组件目录收录庆祝宿主 | PASS | `ComponentCatalog/core_catalog_gen.go` 新增 `core.component.shared.sf_celebration_host`，与 `catalog-identities.json` 的 ui 段一致 |
| 契约描述与实际实现同源 | PASS | `contracts/openapi/schemas/badges.yaml` 的 `BadgeDisplayRef` 键集与 `dto.go` 的 `badgeDisplayRef` 六字段逐字一致；`PublicBadgeCatalogEntry.current/threshold` 声明为 `[integer,null]` 且非 required，与 `omitempty` 指针一致 |
| 插件清单可声明 celebration | PASS | `ExtensionManifest/v3_platform_types.go:19-23` + `schemas/manifest-v3.schema.json`（`celebration: boolean`） |

### 6. 反假绿（逐条「删掉实现会不会红」）

| 断言 | 删掉实现会怎样 | 结论 |
|---|---|---|
| `TestGrantNotifiesExactlyOnce` | 删 `a.notifyGranted(...)` → `calls==0` → 红 | 真断言 |
| `TestBatchGrantNotifiesOnlyNewGrants` | 把通知移出 `grantedUsers` 循环（发全部 50）→ `calls==50` → 红；删掉 → 0 → 红 | 真断言（两向） |
| `TestRecomputeNotifiesWithinLimit` / `…OverLimit` | 删通知 → ≤200 侧 0 次 → 红；删 200 判定 → >200 侧 201 次 → 红 | 两侧成对，缺一不可 |
| `TestRecomputeCallsNotifierOncePerUser` | 改成逐行调用 → `calls==2` → 红 | 真断言 |
| `TestBadgeNotificationTargetFailsClosed*` | 删 `targetType=="badge"` 分支 → 落到 nil `forum` store → 失败；把 `available` 恒真 → 三个 fail-closed 用例红 | 真断言 |
| `TestProfileBadgesEmitRuleDerivedProgress` | `measureForRule` 恒 nil → `measurer.calls==0` → 红；DTO 漏投影 → `current` 非 2 → 红 | 真断言（因果链） |
| `TestProfileBadgesOmitProgressForUnmeasurableRules` | 放开 `lte`/布尔/多条件 → `calls==1` 或键出现 → 红 | 真断言 |
| `TestPublicCatalogRealServiceMergesProgressOnlyForViewer` | 匿名分支也调 `ViewerCatalogProgress` → `userBadgeReads>0` → 红 | 真断言 |
| `TestSetWornBadgeIsKeptByBothRecomputePaths` | `RecomputeTopBadge` 忽略 manual → top=gold → 红；`RecomputeAll` 忽略 `manual_valid` → 红 | 两条 SQL 各一条，缺一漏一 |
| `TestRecomputeFallsBackWhenWornBadgeStopsBeingValid` | `wornBadgeStillValidSQL` 少任一 JOIN → 对应子用例（停用/收回/删除/档位）红 | 四种失效真的各自可分辨 |
| `TestWornBadgePutResponseUsesFlattenedTierShape` | handler 改回 `apphttp.OK(c, display)` → 原始字节出现 `tierAppearance`/`NameZh` → 红 | 打在响应字节上，绕不过 DTO 单测 |
| 两个迁移 `UpDownUp` | Down 少 `DROP COLUMN` / 少 `- 'celebration'` → 计数断言红 | 真跑 up→down→up |
| `TestListCelebrationsFirstVisit…` | 首访写游标行 → `initialized==true` → 红 | 真断言 |
| `TestAcknowledgeCelebrationsNeverRewinds` | 改成 `SET cursor = EXCLUDED` → 20<50 → 红 | 真断言 |
| `TestCelebrationWireShapeMatchesFrozenContract` | `targetPath` 出 null → `Unmarshal` 成 string 报错 → 红；键多/少 → `assertExactKeys` 红 | 真断言 |

**专项复核（题面点名的三处）**

1. **夹具是否自己按错误形状写** —— 未发现。`wear_controller_test.go` 的夹具用**领域** `BadgeRef{Appearance}`（正确），断言落在 wire 上并显式禁止 `tierAppearance`/`NameZh`/`NameEn`；前端 `useBadgeWorn` 同时接受对象与裸 slug 两种输入，测试覆盖两种，与生产投影一致。
2. **`notifyRecomputeGrants` 注释口径** —— 已改正，不再说「一个用户一条通知带 N 枚」。`admin_service.go:451-455` 明写「这只减少**通知出口的调用次数**（一次调用带该用户的 N 枚），不改变投影结果——`badgeGrantedNotifier` 仍逐枚写通知行，去重键是 `badge:{slug}`，所以「同一用户拿到 3 枚」最终是 3 条通知行、不是 1 条。不要照这段注释去优化成「一条通知列多枚」」。`TestRecomputeCallsNotifierOncePerUser` 的注释与决策文档 D5 / 陷阱 T1 同口径。
3. **是否只断言「键存在」** —— 新写的进度断言全部值级：`profile_badges_progress_test.go:168-172`（`current` 必须等于数字 2）、`public_catalog_test.go:370-376`（3 / 10）、`badge_progress_shape_test.go:34-55`（0 / 3、2 / 3）。仅有的「键存在」断言都是**否定**形式（`if _, present := item[key]; present { fail }`），用于「不得出现」的场景，语义正确。

### 7. 越界检查

| 条目 | 结论 | 证据 |
|---|---|---|
| 无无关模块被顺手改 | PASS | 91 项逐条对照改动清单，全部落在 badges / notifications / 对应前端 / 生成物 / 知识库；未见无关域（forum、identity、extensions 运行期）被改 |
| 未把 `public` 缓存头加回 `/badges` | PASS | `controller.go:200` 的 `setPublicCacheable` 只属于 `publicProfileBadges`（actor 无关）；`publicCatalog` 仍是 `setViewerScopedNoStore`；`TestPublicBadgeCatalogIsPrivateCacheable` 精确断言 |
| 未改历史决策原文 | PASS | `git diff --numstat knowledge/decisions/2026-11-11-badges-public-catalog-and-series-closeout.md` → `2 0`（仅新增「2026-11-13 修订索引」两行，0 删除，且正文自述「本条原文不改」） |
| 生成物与源码同步（无手改生成文件） | PASS | `v3-catalog --check` 与 `extension docs --check` 同时通过；两份 `core_catalog_gen.go` 的条目与 Go 注册/JSON 三处一致 |

---

## P0 / P1 清单

**无。** 未发现导致用户可见错误、数据损坏、权限绕过或契约破裂的问题。

---

## 残留清单（可接受项 + 理由）

| # | 残留 | 为什么可接受 |
|---|---|---|
| R1 | `RecomputeAll` 在「佩戴的徽章被删除**且**该用户没有其它可展示徽章」时不会清 `top_badge_manual`：`top_badge_id` 被 FK `ON DELETE SET NULL`（`202611100002_badges_and_user_activity.sql:105`）置空后与 `best.badge_id`（同为 NULL）`IS DISTINCT FROM` 为假，行被 `WHERE` 跳过 | 无用户可见影响：`DisplayBadge` 在 `manual && wornChoice==nil` 时按 auto 呈现（`wear_store.go:199-210`），chip 也不显示该徽章；下一次 `RecomputeTopBadge`（授予/收回/取消佩戴触发）会把它清成 FALSE。属于自愈型脏标记，且 `refreshed` 计数不受影响（确实没有可见变化） |
| R2 | 四种失效（停用/收回/删除/档位停用）在 `RecomputeTopBadge` 上逐一有测试，在 `RecomputeAll` 上只测了「徽章停用」+「有效 manual 不被覆盖」 | 两条 SQL 共用**同一个** `wornBadgeStillValidSQL` 常量（`postgres_store.go:435-448`），四条件谓词被 4 种子用例钉死；两条路径的差异只在 CASE/WHERE 分支逻辑，而该分支已由 1 个失效 + 1 个有效用例覆盖。风险实质很低，但覆盖确实不对称 |
| R3 | `in_app` 关闭的集成断言走的是**类型策略**（`notification_type_policies.enabled=FALSE`），不是**用户级偏好**（`notification_preferences.state='disabled'`）行 | 两者都经 `deliveryEnabled`（`policy.go:29-55`）同一函数：`!enabled` 早退与 `preference=='disabled'` 是同一出口的两条分支，前者已被本测试钉住，后者属平台既有行为（本轮未改）。要补只需一行 `INSERT`，属可选加固 |
| R4 | `useBadgeWorn` 没有登录态切换时的 reset（`useCelebrations` 有 `watch(user.id)`） | 影响仅限「同一标签页内换账号且不整页刷新」时，佩戴按钮的高亮可能是上一个账号的 slug；徽章 slug 本身是公开数据，无泄露；点错也只是执行一次幂等的 DELETE（清自己的 manual 标记）。整页刷新即恢复 |
| R5 | `ListCelebrations` 取 `limit+1` 行后再过滤不可见目标：若最早 `limit+1` 条全部不可见，窗口外的可见条目本轮不展示 | 窗口是 7 天、`total` 封顶 99、一次只展示一张卡；前端每次 `dismiss` 推进游标，下一轮自然看到。已在 `celebrations.go:72-76` 注释中明示「items 可能少于 total」 |
| R6 | `notifyRecomputeGrants` 对每个**不同**徽章各做一次 `store.GetBadge`（≤200 次） | 重算是低频运营动作，且有 `badgeCache` 去重；相对「一次补出上万行授予」的写放大，这点读放大可忽略 |
| R7 | 决策文档 T7 的判据写的是「四条路径各一条**集成**断言，且都查 `notifications` 行」；实际是 2 条行级集成断言（手动授予、job 端口）+ 2 条端口级值断言（批量、重算），后两者与前者共用同一份 `badgeGrantedNotifier.notify` 实现 | 复合覆盖成立：写通知的循环只有一份实现（`badges_wiring.go:81-91`），两条装配边各有行级端到端证据，批量/重算的断言还校验了 `ID/Slug/Name` 的值（不只是「被调用过」）。属文档口径强于实测，建议下轮把 T7 判据改成「两条装配边行级 + 两条领域边端口级」以免后人误判为漏测 |

---

## 我实际跑过的命令

在 `/home/inkedus/Code/github/SForum` 下，全部实跑（未抄他人报告）：

| # | 命令 | 结果 |
|---|---|---|
| 1 | `git status --short` / `git status --short \| wc -l` | 91 项（63 M / 28 ??） |
| 2 | `git diff --numstat knowledge/decisions/2026-11-11-badges-public-catalog-and-series-closeout.md` | `2	0`（仅新增） |
| 3 | `cd apps/api && go build ./...` | exit 0 |
| 4 | `./scripts/test-api-isolated.sh ./app/Models/Badges/ ./app/Http/Controllers/Badges/ ./app/Models/Notifications/ ./app/Http/Controllers/Notifications/ ./database/migrations/ ./bootstrap/ ./app/Support/Routes/` | 7/7 `ok`（Badges 10.05s、bootstrap 51.73s、migrations 0.49s、Routes 1.21s 等） |
| 5 | 同上 + `-v -count=1 -run '<聚焦正则>'` | 逐条 PASS：`TestGrantNotifiesExactlyOnce`、`TestBatchGrantNotifiesOnlyNewGrants`、`TestRecomputeNotifiesWithinLimit`、`TestRecomputeSkipsNotificationsOverLimit`、`TestRecomputeCallsNotifierOncePerUser`、`TestSetWornBadge*`(3)、`TestClearWornBadgeFallsBackToAutomatic`、`TestRecomputeFallsBackWhenWornBadgeStopsBeingValid`、`TestRecomputeAllClearsInvalidWornBadge`、`TestDisplayBadge*`(2)、`TestWornBadge*`(11)、`TestProfileBadges*`(2)、`TestPublicCatalog*`(13)、`TestNotifyBadgeGrantedTx*`(4)、`TestListCelebrations*`(3)、`TestAcknowledgeCelebrationsNeverRewinds`、`TestCelebrationWireShapeMatchesFrozenContract`、`TestPluginCelebrationUsesDescriptorText`、`TestCelebration*`(6)、`TestBadgeWearMigration*`(2)、`TestCelebrationMigration*`(2)、`TestManualBadgeGrantWritesClickableBadgeNotification`、`TestBadgeNotificationTarget*`(4)、`TestBadgesJobNotifierForwardsBadgeID`、`TestCoreRouteCatalogHasExactReviewedGuardParity`、`TestMeasureProgress*`(3)、`TestBadgePublicDerivesMeasureFromRuleOnly` |
| 6 | `cd apps/web && bun test` | **2816 pass / 0 fail**，274 files，25217 expect，23.34s |
| 7 | `cd apps/web && bun run typecheck` | exit 0（`vue-tsc --noEmit` 通过） |
| 8 | `ruby scripts/validate-openapi-refs.rb` | `OpenAPI references OK: checked 3377 refs across 65 files` |
| 9 | `node scripts/v3-catalog/generate.mjs --check` | `V3 P0 catalogs are current (18 files)` |
| 10 | `node tests/validate-architecture-boundaries.mjs` | `passed (2227 production files scanned, 216 files remain above the 500-line review threshold)` |
| 11 | `cd apps/api && go run ./cmd/sforum extension docs generate --check` | `OK  catalog docs in sync` |

**没跑的命令 / 没做的验证，及原因**

| 未执行 | 原因 |
|---|---|
| `./scripts/test.sh`（全量门） | 题面给定的验收矩阵是上述聚焦集合；全量门含生产构建与兼容农场，与本轮四条轴无增量证据，且会与并发改动抢资源 |
| 变异测试（真的删实现看是否变红） | 题面要求「只读审查，不改业务代码」，且工作树有并发改动——改文件会让别人的改动被误判。改用「逐条读断言 + 反推红/绿」的方式完成第 6 条，结论已列在上表 |
| 浏览器视觉 QA（庆祝浮卡位置、佩戴按钮） | 矩阵未要求；且需要用户手动起的 dev server（3000 端口），不属于本次只读审查范围 |
| 真实 HTTP 端到端（起 API 打接口） | 矩阵要求的是「测试名 / 文件:行 / 命令输出」级证据，`bootstrap` 与 `Controllers` 两层已分别覆盖装配边与传输边 |
| 数据库迁移在**生产数据量**下的锁影响 | 两条迁移都是 `+sforum OnlineSafe`，`ADD COLUMN ... NOT NULL DEFAULT FALSE`（PG11+ 免回填）与 `CREATE TABLE`；压测不在本轮范围 |

---

## 备注：本轮最容易被后人改坏的三处（供后续会话参考）

1. `wornBadgeStillValidSQL`（`postgres_store.go:435-448`）是 `RecomputeTopBadge` / `RecomputeAll` 两处「什么算可展示」的唯一判据；任何一处另写比较都会让 chip 与佩戴面板分叉，且两边都不报错。
2. `publicProfileBadges` 的 `public, max-age=30` 与 `publicCatalog` 的 `private, no-store` 是**互斥语义**（被查看者 vs 查看者）；本轮把进度加进两边，缓存口径必须继续按「进度属于谁」判定，不能因为「两边都有 current/threshold」就统一。
3. `notifyRecomputeGrants` 的「按用户聚合」只减少出口调用次数，投影仍是一枚徽章一条通知行（去重键 `badge:{slug}`）；把它优化成「一条通知列多枚」会改掉收件箱既有语义。

---

## Lead 实机 QA 补充（审查之后补，2026-11-13）

> 本节的证据在**上述审查结论之后**采集（lead 手动操作 dev server 的浏览器），不属于原审查矩阵；
> 结论不改动上文的 PASS-with-residual 判定，只把「浏览器视觉 QA 未做」这一条未执行项补上桌面部分。

环境：桌面 **1633×810**，登录态 = 站长 `zhuchunshu`（`user_id = 1`）。

| # | 做了什么 | 实测结果 | 对应断言 |
| --- | --- | --- | --- |
| 1 | 打开 `/badges` | 12 枚徽章里**未获得**的显示进度条 + 数值：常客 4/7、老住户 4/30、元老 4/180、初次发帖 0/1…；**已获得**的两枚（社区贡献金牌、优质解答银牌）**没有进度条**，只显示「已获得」 | 目录页 `current`/`threshold` 下发成立；「earned 不画条」在真实渲染里成立 |
| 2 | 用 admin API 临时建一枚手动徽章并授予自己 | `notifications` 落行 `id=591`、`type=badge_granted`、`target_type=badge`、`target_id=39`、`dedupe_key=badge:qa-celebration-probe`；刷新 `/badges` 后左下角出现卡片「获得新徽章 / 你获得了「QA 庆祝探针」徽章。/ 查看」 | 授予通知的四条路径之一（手动）端到端可点；`badge` 目标类型与 `badge:{slug}` 去重键与代码一致 |
| 3 | 检查浮卡的 DOM 与几何 | `role=status`、`aria-live=polite`；整卡链接到 `/u/zhuchunshu?tab=badges`；带关闭按钮；`left:16px bottom:16px`（不挡右下 toast）、宽 `min(22rem, calc(100vw - 32px))`。**`badgeName` 已填进句子**（不是「你获得了新徽章」兜底句） | 庆祝展示层 `celebrationPresentation` 的「i18n 命中 → 服务端兜底 → 吃掉占位符」三态里，**主路径（i18n + params）确实被走到**；R7 那次「T7 判据订正」的前提在此得到旁证 |
| 4 | 资料页 `?tab=badges` 点「佩戴」 | 已获得卡上「佩戴 / 佩戴中」按钮 `aria-pressed` 正确；点后 `GET /badges/worn` = `mode=manual, worn=qa-celebration-probe, upgrade=community-contribution`；页面顶部出现提示条「你现在可以换成「社区贡献」」 | `SetWornBadge` + `DisplayBadge` 的 `upgrade` 语义（提示而非错误）在真实渲染里成立；**不覆盖用户选择** |
| 5 | 撤销授予 + 删除探针徽章 | `users.top_badge_id = 11`（社区贡献）、`top_badge_manual = f` | `RecomputeTopBadge` 的失效回落分支（本次测的是「徽章被删除」这一种）端到端成立；与 R2 记的「`RecomputeAll` 覆盖不对称」不冲突——那是测试覆盖问题，不是行为问题 |
| 6 | 清理 | 删探针徽章（`badges` 回到 12）、删那条通知（user 1 通知数回到 36）、庆祝游标重置回 196；刷新后无庆祝卡、徽章数 12 | 清理可逆，dev 库未留脏数据 |

**仍未验（不要当成已验）**：**390×844 移动端没有截图证据**——当次浏览器无法改视口，只做了 CSS 层
核对：浮卡 `min(22rem, calc(100vw - 32px))` 在 390 下 = 352px，左右各 16px 余量。
「同一时刻庆祝卡与右下 toast 同时出现」的视觉优先级也仍未被观察到（几何上不重叠，但没有同屏截图）。

**与审查结论的关系**：R1 / R2 / R7 三条残留不受本次实测影响。R7 的订正（断言层级）反而被第 3 条
实测旁证；R1（`RecomputeAll` 在「删徽章且无其它可展示徽章」时不清标记）与本次第 5 条走的是
`RecomputeTopBadge`，是两条不同路径，**不要用第 5 条的结果去关 R1**。
