# Trust Levels Module

## Purpose

在既有 RBAC 之上叠一层**按累计行为自动升降**的能力层。

- **是**：按「累计行为」解锁/收紧**已存在**的权限键；把现有「新人信任阶梯」（按注册
  时长两态）推广成 N 档。
- **不是**：第二套权限系统；不是徽章（徽章只展示，永不给能力）；不是审批流程。

唯一裁决点仍是 `identity.Actor.Can`。TL 是它的**第四个授予来源**，不是并列的第二套
检查——这一条决定了整个模块的形状，动它之前先读计划 §1。

## Current Status

**M1–M5 已落地（M5 = 资料页等级卡，2026-11-11 09:21 复核）；M6–M8 未完成；M9–M13 收尾进行中（M10 earnable 编辑器已落地，2026-11-11 09:40 实测组件在树）。**
（2026-11-10 全量对拍复核 + 2026-11-11 09:21 对拍 M5 收口，逐条在真实代码上验证。）

| 结论 | 证据 |
| --- | --- |
| M1 领域包 | `apps/api/app/Models/TrustLevels/`：`types.go` / `level.go` / `policy.go` / `registry.go` / `config.go` / `resolver.go` + 测试 |
| `Actor` 三字段 | `Identity/types.go`：`TrustLevel` / `TrustLevelResolved` / `Earned`（已导出，D23） |
| 窄接口（4 参） | `Identity/trust_levels.go:29`：`Grants(ctx, userID, kind, createdAt)`；`ErrTrustLevelsDisabled` 哨兵同文件 |
| D14 公式 | `Identity/postgres_store_roles.go` 的 `applyTrustLevel`：`Expand(static)` → 并 earned（跳过 denied 与已存在）→ 记 `Earned` |
| 派生视图已改名 | `Identity/permission_origin.go`：`GrantSource()` 返回 `PermissionOrigin`（D30），与 `Can()` 同构由 `TestGrantSourceMatchesCan` 守住 |
| **装配已落地** | `bootstrap/api_assembly_domains.go:321` `identityStore.WithTrustLevelSource(...)`；`api_assembly_extensions.go:476` 同款注入 user-field store；来源在 `api_assembly_infrastructure.go:59-60` + `bootstrap/trust_levels_wiring.go` |
| **开关五处已注册** | `Options/feature_flags.go:35`（常量）/ `:63`（definitions）/ `:82`（catalog）/ `:97`（推荐默认）/ `:130`（结构体字段）+ `:153`（读取）；归一化分支在 `service_normalize.go`；前端已消费（`useWebOptions.ts`） |
| **M3 已落地** | `Forum/service_trust_limits.go:23` `limitsForActor`（**不是** `service_ops.go:514`——那是调用点）、`:101` `resolveLimit`、`:63` `newUserLimits`、`:83` `levelLimits`；适配器 `Providers/forum.go:273` `TrustPolicyAdapter.LevelLimits` |
| 阈值配置 | `Options/trust_level_options.go`：单键 `trust.levels`，**schema v2**（`ConfigSchemaVersion = 2`，`tiers[]` + 不可变 `id` + `retired`，D-N2）；`DecodeConfig` 同时接受 v1/v2（v1 在内存升级）、`EncodeConfig` 只写 v2（D-N5）；`DefaultPolicy()` 已与 PM 定稿表逐值一致（复核见 `reports/2026-11-10-trust-levels-doc-audit.md` C1） |
| 后台页（2026-11-11 提为独立页） | 页面 `/control-panel/forum/trust-levels`（**用户/权限 → 信任等级**，徽章之后；2026-11-11 D-C7 从「论坛管理」归位，只搬菜单，页面 id/路由/身份不变），页内**五** tab：`overview`（缺省，zh「等级总览」）/ `tiers`（zh「档位管理」）/ `thresholds`（zh「门槛设置」）/ `limits`（zh「发言限额」，2026-11-11 追加）/ `earnable`（zh「能力解锁」），**已全部落地**（`tabIds` = `pages/admin/forum/trust-levels.vue:96`；四 tab 是 N 档改造期的形态，`limits` 见 `../decisions/2026-11-11-trust-level-limits-editor.md`；下表 `n8-mobile-tabs tab=4` 是**加第五个 tab 之前**的证据，tab 数已变；`tiers` 的浏览器表现已验（`storage/qa/trust-levels-n/20261111-184009-qa-reshoot-main.json`：`n8-mobile-cards` layout=cards rows=7、`n8-mobile-no-overflow` 390/390、`n8-mobile-tabs` tab=4；截图 `20261111-181057-qa-n5-desktop-tiers-tab.png`））（2026-11-11 收尾实测：`trust-levels.vue` 315 行，`normalizeTab` 回落 `overview`；冻结稿 `../reports/2026-11-11-trust-levels-admin-page-spec.md` §14.6 有浏览器证据）。文件：`pages/admin/forum/trust-levels.vue`、`components/admin/forum/trust-levels/tabs/SFAdminTrustLevelThresholdsTab.vue`、`components/admin/forum/trust-levels/tabs/SFAdminTrustLevelLimitsTab.vue`、`components/admin/forum/trust-levels/tabs/SFAdminTrustLevelEarnableMatrix.vue`、`composables/admin/forum/trustLevels/useTrustLevelsForm.ts` + `useTrustLevelEarnableMatrix.ts` + `useTrustLevelsAdminPage.ts`。**整页一次保存**（单键 `trust.levels`）。旧入口 `/control-panel/settings?tab=trustLevels` 重定向到新页，设置页的四处 tab 登记已删。裁决见 `../decisions/2026-11-11-trust-levels-standalone-admin-page.md`（取代本文件 D 组 #18）。白名单来自后台读面 `eligiblePermissions`（前端零硬编码），`?? []` 兜底不能改成 `?? null` |
| 公开面已落地（M4） | 4 条路由进 catalog（`core_catalog_gen.go` 计数 409 = 405 + 1 公开 + 3 后台）、OpenAPI `contracts/openapi/paths/trust_levels.yaml` 在、`Http/Controllers/TrustLevels/controller_test.go` 在。**公开响应不含 `limits`**（`dto.go`：`Limits` 带 `omitempty`，注释写明「只在后台读面出现」——公开「某档冷却 60 秒」等于泄露限流参数） |
| 资料页等级卡已接线（M5 收口） | `useTrustLevel.ts` 的 `loadViewer()` 真请求 `/trust-levels`；`SFProfileShowPage.vue:110` 传 `computed(() => profile.value?.trustLevel)`，`SFProfileRightRail.vue:74` 消费（2026-11-11 09:21 复核） |
| M6–M8 未完成 | 验收断言、性能实测、独立验收（M13 收尾中，见 `../sessions/2026-11-11-trust-levels-closing.md`） |

配套文档：计划 `../plans/2026-11-10-trust-levels.md`（§3 决断 D1–D32、§10 陷阱 T1–T19、
§13 修订记录）；统计地基 `../modules/badges.md`；handoff
`../sessions/2026-11-11-trust-levels-admin-page.md`（当前 hot，独立页重构）+
`../sessions/2026-11-11-trust-levels-closing.md`（收尾刀）+
`../sessions/archive/2026-11/2026-11-10-trust-levels.md`（上一刀，已归档）；作废说法底账
`../reports/2026-11-10-trust-level-superseded-claims.md`；独立页裁决
`../decisions/2026-11-11-trust-levels-standalone-admin-page.md`。

**纪律**：本文件每写一条实现断言，要么带路径+行号，要么写 `TODO(核实)`。不要因为计划里写了
就抄；也不要因为「代码里有」就当成「运行时生效」——两者都踩过。**否定句（「没有 X」）必须
每次对拍重测**，它们是滞后漂移的高发区。

**数字纪律**：本文件只允许出现**不随配置变化**的数 —— `1..8`（档数边界）、`默认 4 个可配置档`（+ TL0 = 默认 5 档）、`6 个指标`、`7 个可解锁键`。任何由固定档数算出来的数（`40`、`4×6`、`5 档 × 6 指标`）都是过期算术，改档数的人会被它误导一次，别写。

**替身纪律（2026-11-11 B2 教训，改 UI 语义时必须遵守）**：既有挂载测试把 `UButton` 桩成原生
`<button v-bind="$attrs">`，于是「我们的模板 → UButton → 原生 DOM」这一段**从未被真渲染过**——
谓词绿、桩绿、真实组件没人看。它与 P1（portal 元素的 `offsetParent` 恒 null 导致探针判假）
同属一类**证据等级错误：绿的不是产品，是我们自己的替身**。
**纪律：改禁用/隐藏/可访问性这类 UI 语义时，断言必须读最终渲染结果**（原生属性、`:disabled`、
`aria-*`、`cursor`、以及真实点击行为），不能读谓词函数或测试桩；桩只能证明接线，不能证明
外观与可交互性。

## Data Model

**本刀不新增表、不新增列。** `Level` 是纯函数派生值，不落列、不缓存（计划 D4）。

| 输入 | 来源 | 可逆（回退会降级） |
| --- | --- | --- |
| `topic_count` / `comment_count` | `user_activity` | 否（软删不回退） |
| `like_received_count` / `like_given_count` | `user_activity` | **是** |
| `bookmark_count` | `user_activity` | **是** |
| `days_visited` | `user_activity`（真值在 `user_visits`） | 否 |
| `accountAge` | `users.created_at` | 否 |

> `accountAge` **不是** `Requirements` 字段，它不进 `trust.levels` 的阈值配置，而是独立
> 「账龄闸」的输入（`level.go`：账龄 < `NewUserDays` → 最高 TL1，否则至少 TL1）。

六项计数与 `user_visits` 已由上一刀落地，见 `../modules/badges.md` 的数据模型节。
**可逆性不对称是降级语义的全部依据**（计划 D5）：只有可逆计数下降才降级；内容软删、
审核驳回、封禁都不降级。

阈值配置存储：**单个 JSON option 键 `trust.levels`**（D19），不新建配置表——与
`trust.new_user*` 同域、同后台页、同恢复默认机制。形态照抄 `seo.footer_links`
（`service_seo_footer_normalize.go:81`：JSON 值 + 严格解码 + 未知字段拒绝 + schema 版本
字段）。

> **不要退回「`trust.level.*` 扁平多键」**：默认 4 个可配置档 × **六指标** + 逐档限额已经数十个值，且档数改成 1..8 之后还会继续变，扁平键要过
> options 的 5 道注册（types / definitions / defaults / normalize case / `allOptionNames`
> 派生），且漏第 5 道会**静默丢弃**（T16）。指标是 **6 个不是 7 个**——账龄只保留
> `trust.new_user_days` 一个上限闸（`Policy.NewUserDays`），没有 `account_age_days` 字段。
> 这一条已实测过枚举点：`app/Models/Options/service_normalize.go`。

**阈值默认值的唯一来源是 `TrustLevels/policy.go` 的 `DefaultPolicy()`**，运营可在后台覆盖
并一键恢复；TL0 不参与判定，由 `trust.new_user*` 七键决定。

### 档位身份与序号（D-N1）

- 每档两个概念：不可变 `id`（`newcomer`…`leader` 或 `tier_<seq>`）与 `rank`（1..N）。
  `rank` 由 active 档位在 `policy.Tiers` 里的位次派生，**不落字段**；`Policy.LastRank()`
  只是数 active 档个数（`apps/api/app/Models/TrustLevels/types.go:231-240`）。
- **可执行判据一：插一档后原档配置不位移。** 由 `tier_identity_test.go` 的
  `TestInsertTierKeepsFollowingIdentities` 背书；变异验证方式是实现改成「按位次锚定」后
  该测试必须变红。
- **可执行判据二：所有编辑动作以 id 为键，不用数组下标。** 同文件另有
  `TestRanksAreContiguous`（rank 连续）、`TestRankZeroIsOwnedByTL0`（rank 0 归 TL0，
  档位数组从 TL1 起）、`TestTierIDIsNeverReused`（id 不复用——只有软删除才成立）三条守边界。
- `id` 生成：`tier_<seq>`，`seq` 解码期派生、不存字段、不接受客户端指定
  （`tier_icons.go` 的 `tierIDPattern` / `tierSeq`）。**这是「读代码 + 测试背书」的结论，
  不是运行时观测。**

### v1→v2 兼容（D-N5 + D-N13）

两个方向都要写清，缺一半就会误导下一个回滚的人：

- **升级方向**：`DecodeConfig` 同时接受 `schemaVersion` 1 与 2；v1 在**内存里**升级
  （id = 原 `levelSlug`、name = 内置双语名、icon = 默认序列）。`EncodeConfig` **只写 v2**。
  背书：`legacy_config_v1_test.go` 的 `TestLegacyV1UpgradesInMemory` /
  `TestLegacyV1EncodesAsV2` / `TestLegacyV1EqualsDefaultPolicyDecoded` /
  `TestLegacyV1EncodeIsStableAcrossSaves`。
- **写入时机（D-N13）**：库值升级**只发生在运营保存路径**（后台 PUT 与 options 归一化的
  写路径）；启动、读路径、`DecodeConfig` 一律不写库。读路径写库会造出「没人动过配置、
  库值却被改了」这种查不出源头的现象。
- **回滚方向**：旧二进制**解不了 v2**，所以回滚是**两步**——先回退镜像，**再**把
  `trust.levels` 的值回退到 v1 形状。只换镜像会让站点开机读不到配置并回落到推荐默认。
- wire 上 `levelSlug` 字段名**保留**，值 = tier id；内置五档 id 不变，存量消费方不断。

### 软停用（D-N8）

- **配置保留在配置 JSON 里**：`policy.Tiers` 含 retired 档（`Retired` 标志），
  `EncodeConfig` 按 `policy.Tiers` 迭代写出（`config.go:349-367`），所以停用后**往返不丢**。
  不是存在某张表或某个缓存里——本模块仍然零表。**这一条是读 `EncodeConfig` 可得 + 由
  `TestEncodeRetiredTierKeepsEarnable` 背书，不是运行时观测。**
- 判定与展示跳过 retired 档：`LastRank()` 只数 active，`LevelOf` 降序遍历跳过它们（`level.go`）。
- 撤回语义：停用会撤回该档贡献的 `earnable`（`Grants` 只在 active 档上累加），可能让人降权；
  保存前有二次确认与常驻披露（D-N8）。背书：`TestRetireThenRestoreKeepsRankAndConfig`。
- **停用链路已浏览器验证**（`storage/qa/trust-levels-n/20261111-181057-qa-report.json` 的
  `tiers-retire-confirm` 断言）：点停用弹出二次确认「确认停用「TL2」？…该档新增解锁的能力可能
  从用户身上撤回，用户等级可能因此下调；按下保存后才会真正生效」，并出现「其它启用档位已覆盖…」
  的覆盖分支；截图 `20261111-181057-qa-n5-desktop-save-confirm.png`。
  **已闭环到原生层**（`storage/qa/trust-levels-n/report.md` §9 的 B2 行 + R2 读数）：N=1 时
  原生 `<button>` 的 `disabled`=true、`btn.disabled`=true、`matches(':disabled')`=true、
  `aria-disabled="true"`、`title` 带锁定文案、`cursor:not-allowed`、`opacity:0.45`；点它
  **无确认框、无弹窗、不发请求**。反向 N=2：`disabled`=[false,false]、**`aria-disabled` 属性不存在**
  （可用时不宣告，比宣告 `"false"` 更稳）、`title`=[null,null]、`cursor:pointer`、锁定与账龄闸
  文案都不出现。截图 `20261111-190105-qa-regress-r2-n1-retire.png` / `-r2-n2-retire.png`。

> **不要在文档里抄这组数字。** 当前 `DefaultPolicy()` 与 PM 定稿表
> （`reports/2026-11-10-trust-levels-capability-map.md`）有大量不一致，正在对齐（P0-2）。
> 对齐完成、且 backend 报过之后，再从代码里读一遍写进文档。**只印代码里能读到的数，
> 不印「应该会是这样」的数**——数字与代码不一致比没有数字更糟。

### 配置校验错误的文案边界（D-N14 的**显式例外**）

- 口径不变的部分：`Can()` 与整个领域层**不输出面向用户的文案**，i18n 由前端拼
  （`NameKey()` / `DescriptionKey()` / `ThresholdOptionKey()` 三个死生成器正因这条被删）。
- **显式例外**：配置校验失败时，`data.details.errors[]` 的每条明细带一个 `reason`
  （Go 侧中文散文），**仅供兜底**，展示路径以 `reasonCode` + 前端 i18n 为准。下一个人
  拿「后端不输出文案」来质疑 `reason` 字段存在时，先读 D-N14——这不是回归，是被裁定过的例外。
- **`reason`「不用于展示」在三处都写明了，缺一处接入方就会读错**：
  1. 领域层 `FieldError.Reason`（`Models/TrustLevels/config.go`）——注释写明「只用于服务端日志
     与『没有 i18n 键的旧前端』，**不用于正式展示**」；同结构里 `Code` 才是「明细里唯一参与
     展示决策的字段」，另有 `Params` 供插值。
  2. wire 层 `trustLevelConfigError.Reason`（`Http/Controllers/TrustLevels/dto.go:61-82`）
     ——这一处才是接入方真正会读到的，`ReasonCode` 注释指向 OpenAPI enum 与
     `ConfigErrorReasonCodes()` 同源。
  3. 契约层 `TrustLevelConfigErrorDetail.reason` 的属性描述
     （`contracts/openapi/schemas/trust_levels.yaml`）——「**不用于正式展示**：本仓口径是
     后端不输出文案、由前端按 `reasonCode` 拼」。
- 现值（读代码可得）：机器码 `CodeInvalidConfig = "trust_levels.invalid_config"`
  （`controller.go:20`）、明细上界 `MaxConfigErrorDetails = 8`（`config.go:50`）、
  枚举 33 条由 `ConfigErrorReasonCodes()` 提供并被 `TestConfigErrorReasonCodesAreStable`
  用字面量钉住（`config_test.go:410-445`）、与 OpenAPI enum 同序。**枚举条目不在本文件复制**
  （契约副本会过期），要取值看上面两处。
- **前端覆盖面护栏已落地**：33 个 code 分成「表单可达的 15 条（逐条 i18n）」与「配置体损坏的
  18 条（通用桶）」，`apps/web/tests/admin/forum/trustLevels.test.ts:550-603` 断言
  `BACKEND_CODES` 33 条、`SPECIFIC_ERROR_CODES` 15 条、`CONFIG_BUCKET_ERROR_CODES` 18 条、
  并集 size 33（不重不漏）；两份 locale 已有对应文案键。后端新增第 34 个 code 会让这条测试红，
  而不是在 en-US 站点悄悄显示中文。
  —— 现状是 locale 里还没有 `reasonCode` 文案键。
- **`reasonCode` / `params` 已落地**：枚举 33 条（`ConfigErrorReasonCodes()`），
  wire `trustLevelConfigError.ReasonCode` 注释写明「取值域见 OpenAPI enum、与之同源」，
  `dto.go` 的 `Reason` 注释已改成「**不用于正式展示**」，OpenAPI 对象级描述也已统一成
  「展示走 `reasonCode` + `params`，`reason` 只作技术细节兜底」——三处口径现已一致。


### 恢复推荐默认 = 整份覆盖（D-N15，**本刀引入的语义变更**）

- 现值：恢复默认走服务端整份覆盖（`POST /admin/trust-levels/restore-defaults` →
  `EncodeConfig(DefaultPolicy())`，路由见 `Http/Controllers/TrustLevels/controller.go:80`），
  覆盖范围 = 档位数量与顺序、阈值、限额、新增解锁、名称与图标，**已停用档被移除**。
- **这不是既有行为，是 2026-11-11 这一刀改的**：此前实现是「本地用推荐阈值覆盖表单、限额与
  解锁不动」，文案也照旧口径写。任何「恢复默认只动阈值」的表述都是过期口径，别照着写文档。
- **墓碑不在覆盖范围内**：`RestoreDefaults(previous)` 保留「既有墓碑 ∪ 被移除的 `tier_<n>` id」
  的保序并集（`policy.go:98-130`），内置档位 id 不入墓碑（`NextTierSeq` 基准是 5，内置档不会被复用）。
  控制器先读 previous 再覆盖，读失败 fail-open 到纯推荐值并 warn（`controller.go:208-231`）；
  读面 `recommendedConfig()` 仍是纯 `DefaultPolicy()`——推荐值 ≠ 恢复动作。
  裁决见 `../decisions/2026-11-11-trust-level-tier-color-and-delete.md`（D-C3 补强）。
- 破坏性动作的配套：恢复前阻塞式确认，确认框逐项列出会丢的五类配置（D-N15 的代价条款）。
  浏览器证据：`storage/qa/trust-levels-n/20261111-184009-qa-reshoot-main.json` 的
  `restore-config` 断言（收尾 restore-defaults 200、回读 5 档归一），以及
  `20261111-183915-qa-save-confirm-structure.png` 一类截图。

### 档位颜色（D-C1 / D-C5，**本刀新增**）

- **白名单 9 色**（`Models/TrustLevels/tier_colors.go:21-31`，顺序 = 默认色序 = rank）：
  `slate`(TL0) → `teal` → `emerald` → `sky` → `indigo` → `violet` → `fuchsia` → `rose` → `amber`(TL8)。
  **9 而不是 8**：`MaxTiers = 8` 且 `slate` 被虚拟 TL0 占掉，8 色会让满配 TL8 回退成 `slate`
  与最低起点同色（功能性缺陷）。取值（明暗两套 hex）与对比度核验见
  `../reports/2026-11-11-trust-tier-color-palette.md` **§9**（§6 的 8 色块作废）。
- **`tiers[].color` 是可选属性，空串 = 跟随位次**：解码期**不填默认**（填了就把「自动」冻成
  一次性取值，`config.go:193-196,484-486`）；有效色由 `Policy.ColorOf(rank)` 解析后下发
  （`types.go:348-367`、`Http/Controllers/TrustLevels/payload.go:34`），TL0 恒取色序首项。
  后台读面下发 `colorChoices`（`dto.go:139-141`），前端不得自己抄一份。
- **表单兜底把颜色置空**：`fallbackTiers()` 的每档 `color: null`（`useTrustLevelsForm.ts:102-107`），
  只有公开面兜底 `FALLBACK_TIERS` 带默认色——那份颜色服务「目录读不到也要有色」的展示，
  钉进表单就会在第一次保存时把颜色写死、之后移档不再跟位次走。
- **渲染单点**：`--sf-tl-<token>`（`assets/css/sforum-trust-colors.css`，`:root`/`.dark` 各一套）
  → `tierAccentStyle` 落成 `--tl-accent`（`utils/profile/trustLevelColors.ts:68-78`）；
  后台档位行与资料页 `SFTrustLevelCard` 共用，不与站点主题的 5 套 accent 联动。
- **渲染面共用共享块 `.sf-tl-mark`（D-C6，2026-11-11）**：五个后台分区（总览阶梯 / 档位管理 /
  门槛设置 / 发言限额 / 能力解锁）与资料页卡一律挂在 `assets/css/sforum-trust-colors.css` 的
  `.sf-tl-mark` 上，色相只认**行根内联** `--tl-accent`；共享块内**禁止**声明 `--tl-accent`
  （自定义属性在更深一层声明会遮蔽祖先内联值 —— 这正是改动前「配了色也只显示站点主色」的病根）。
  淡底 = `color-mix(in srgb, var(--tl-accent) var(--tl-accent-tint), var(--sf-card))`，
  `--tl-accent-tint` 默认 12%、停用档 6%/8%（暗），**不再用 `--sf-accent-soft`**。
  形状轴仍在（`data-step` 1 = 34% 环 + **透明底** + 档位色字形 / 2 = 1px 34% 环 + 12% 淡底 /
  3 = 2px 55% 环 + 12% 淡底 / 4 = 实心反色字；TL1 也有色，2026-11-11 回归修复见下），
  仅 `data-step` 从资料页卡根移到图标块上。
  **回归教训**：step1 曾照抄旧「强度阶梯」写 `color: var(--sf-fg-secondary)`，把 rank 1 的档位色
  整个吃掉（给 TL1 配色无效，其余档正常）——「同一档在档位管理是 teal、其余五面是中性灰」就是它的指纹。
  规则不随产品语义重写时不会报错，只会让配置项静默失效。裁决见 `../decisions/2026-11-11-trust-level-color-render-unification.md`；
  改色端到端证据（六面同变 + 已回滚）见 `storage/qa/trust-level-color-unify/REPORT.md`。

### 档位外观编辑：Modal + 全量图标 + 可选自定义色（D-A1..D-A7，**2026-11-12 新增**）

裁决见 `../decisions/2026-11-12-trust-level-appearance-modal-and-custom-color.md`，
几何与对比度核验见 `../reports/2026-11-12-trust-level-appearance-modal-spec.md`。

- **入口从行内面板改成 `UModal`**：`SFAdminTrustLevelAppearanceDialog.vue`（桌面 `sm:max-w-2xl`、
  `max-height:min(86dvh,720px)`、body 唯一滚动区、footer 吸底），由 `SFAdminTrustLevelTiersTab.vue`
  的 `appearanceTierId` 挂**唯一**实例；行只发 `appearance` 意图（`aria-haspopup="dialog"`）。
  旧的 `SFAdminTrustLevelIconPicker.vue` / `SFAdminTrustLevelColorPicker.vue` **已删除**。
- **图标 = 16 短名（默认序列）∪ `i-(lucide|tabler)-<name>` 全名**：`tier_icons.go` 的
  `hostIconPattern`；前端 `normalizeIcon` **不再剥** `i-lucide-` 前缀（剥了会得到白名单外短名，
  保存 422）；`setTierIcon` 闸门是形状校验，不再是 `choices.includes`。
  真实约束是「必须能被 `@nuxt/icon` serverBundle 解析」（`nuxt.config.ts:185-192`），不是静态可枚举。
- **颜色 = 9 token（推荐色板 + 默认色序）∪ 小写 `#rrggbb`**：`IsTierColor` = 白名单 ∪
  `^#[0-9a-f]{6}$`，`config.go` 落库前 `ToLower(TrimSpace())`。**D-C1 的其余语义不变**：
  color 仍可选、空 = 跟随位次、解码期不填默认、`colorChoices` 顺序仍是默认色序。
- **自定义色的明暗派生全在 CSS**：`sforum-trust-colors.css` 现在是 18 个字面量
  （`--sf-tl-<token>-light|-dark`）+ 三层映射（`:root, [data-sf-tl-scope="light"]` / `.dark` /
  `[data-sf-tl-scope="dark"]`）。hex 的**页面级**渲染由 `tierAccentStyle` 返回
  `--tl-accent-light/-dark` 两级源值，`--tl-accent` 由 `[data-sf-tl-accent]` 与
  `.dark [data-sf-tl-accent]` 算出——自定义色**不能**内联 `--tl-accent`（内联优先于 `.dark`）。
  token 路径仍内联 `--tl-accent`，存量配置零行为变更。
- **6 个落点必须带 `data-sf-tl-accent`**：TierRow / EarnableRow / ThresholdsTab / LimitsTab /
  OverviewTab / `SFTrustLevelCard`。漏一处只影响自定义 hex（token 不受影响），用遍历 SFC 的护栏兜住。
  `SFTrustLevelCard` 卡面原来的 `--tl-accent: var(--sf-tl-slate)` 兜底**已删**（0,2,0 特异度会压过
  全局规则，把 hex 档打回 slate）。
- **modal 内两条反馈口径**：格式非法 → 「应用」禁用 + 字段级错误（不发意图）；深色卡面对比度
  < 3:1 → 只提示不阻断（`appearanceDialog.colorLowContrast`）。预览双底卡固定底色
  `#ffffff` / `#1b201f` + `data-sf-tl-scope`，与页面模式解耦。
- **落盘仍只有一个入口**：modal 即时写表单草稿，「完成」只关闭，「恢复默认外观」= `setIcon(null)+setColor(null)`。
- **错误码名未改**：`icon_not_whitelisted` / `color_not_whitelisted` 只改文案（稳定标识符）。

### 删除与墓碑（D-C2 / D-C3，**本刀新增**）

- **删除只针对已保存为停用的档**：`canDelete(id) = retired && savedRetiredIds.includes(id)`
  （`useTrustLevelTierIntents.ts:92-98`），命中后仍需二次确认（墓碑不可恢复）。
  「停用未保存 → 删除 → 保存」被这条闸门拒绝；删除只移除 retired 项、不改任何 active 位次，
  所以不触发结构确认（`tierStructureChanged` 只比较 active id 序列，`useTrustLevelsForm.ts:722-724`）。
- **墓碑 = 顶层 `deletedTierIds`**（`omitempty`，按提交顺序，`config.go:166-182`）。解码期三条判据
  一律 422、不去重静默接受：`deleted_tier_id_invalid`（语法/身份不合法，含 `newcomer` 保留给 TL0）、
  `deleted_tier_id_duplicate`、`deleted_tier_id_present`（同一 id 又在 `tiers` 里）；
  rank 恒 0、`tierId` = 墓碑本身（`config.go:810-856`）。
- **`NextTierSeq()` 计入墓碑**（`types.go:404-417`），删掉的 `tier_7` 仍占位 → 下一个是 `tier_8`，
  保住 D-N6「id 永不复用」。前端 `cloneTrustLevelsForm` 不落空墓碑键（空数组 = 没有墓碑）。
- **新错误码 4 条**（`config.go:81,103-105`，码表总数 33 = 原 29 + 4）：
  `color_not_whitelisted`（tier 级，rank 可用）、`deleted_tier_id_invalid` /
  `deleted_tier_id_duplicate` / `deleted_tier_id_present`（配置级）。
  **文案口径**：只有 `color_not_whitelisted` 有专属 i18n 文案（表单可达）；
  三条 `deleted_tier_id_*` 走 config 通用桶，没有专属文案——既有护栏规定 bucket 码不许有专属句。

### 等级标识的展示口径 + 徽章「做过又撤回」（2026-11-11 定稿）

**当前口径（生效中）**：信任等级标识**只在个人中心（资料页）显示**。唯一展示入口是资料页右栏的
`SFTrustLevelCard`（图标 + 名称 + `TL{n}` 数字 + 进度刻度，已按档位色上色）。论坛读面
（话题列表 / 详情 / 回复）一律不显示等级；**个人中心也不再加新徽章**——那张卡就是全部。
不要再给论坛行加徽章，也不要重新引入 `authorTrustLevel` 载荷。

**历史（写下来是防重复劳动，不是待办）**：2026-11-11 当天曾实现过论坛三处内联徽章 + 后端批量载荷，
同日按产品口径全部撤回（`46c2f52cb` 同时 revert `735aeed8c` 与 `4f9f360b8`）：

- 曾经实现：`components/forum/SFTrustTierBadge.vue`（色点 + `TL{n}` 文字，TL0 用同尺寸 `inset` 空心环）、
  列表/详情作者块/评论 `#name-suffix` 三处接入、`TrustLevels.BatchResolver`（每页固定 2 条 SQL：
  `users WHERE id=ANY`、`user_activity WHERE user_id=ANY`）、`Forum.AuthorTrustLevelReader` 聚焦协作者、
  契约 `TrustTierColor`（9 值 enum）与 `AuthorTrustLevel`。
- 为什么撤：产品要求等级标识只在个人中心出现，论坛读面不暴露等级；没有消费者之后，那 2 条 SQL
  仍然加在每个列表/详情/评论页上；契约里留着「下发但没人用」的字段会让下一个读契约的人以为前台有等级标识。
- 规格仍在：`../reports/2026-11-11-trust-level-badge-spec.md`（`54f2387f9` 补回、`37a28b67b` 在文首标注
  口径已变）。将来若要在某个**受限面**加等级标识，直接复用那份形态/密度/对比度结论，不要重做。
- 值得留的结论：「色点 + 数字」全量最差 5.16:1（浅色 teal/parchment）；淡底数字牌在 `parchment` 下
  teal 仅 4.38:1 被否；TL0 用同尺寸盒子 + `inset 0 0 0 1px` 空心环，避免档位变化引起作者名折行。

### 分档发言限额（D-L1..D-L4，**2026-11-11 新增**）

- **三态 = 后端的三种输入**（`limits` 在 v2 里是必填字段，省略即 422）：
  `null` = 该档完全继承站点默认；对象 = 逐字段覆盖（对象里某字段为 `null` = 该字段仍继承）；
  `0` = 不限额 / 免冷却，与「没配」严格区分。前端 `trustLevelsPayload` 只写 `null` 或四键对象，
  **永不写出省略**（`useTrustLevelsForm.ts:675-708`）。
- **纯函数层**：`composables/admin/forum/trustLevels/useTrustLevelLimits.ts`（`limitsModeOf` /
  `toLimitsForm` / `parseLimitInput` / `TRUST_LEVEL_LIMIT_KEYS`），`parseLimitInput` 先 `trim` 判空
  再走 `/^\d+$/`，空串永不经 `Number()` 变成 `0`；非法值 fail-closed 返回等值副本。
- **写严读宽**：写侧 `TRUST_LEVEL_LIMIT_MAX`（冷却 30 天 / 每日 100000）只挡手滑；
  读侧 `normalizeLimits` 的 `boundedLimit` **故意无上限**，只把负数/非有限数收敛成 `null`——
  读时截断会把服务端已存的更大值静默改小。
- **粘贴回弹**：`blockNonIntegerKey` 只拦 keydown；`change`/`blur` 时 `onLimitCommit` 用同一套解析复核，
  非法就显式写回 prop 值（fail-closed 不重渲染，不写回就会在 DOM 里留脏文本）。
- **不触发结构确认**：`tierStructureChanged` 只比 active id 序列（`useTrustLevelsForm.ts:722-724`），
  改限额不改变谁落在哪一档。恢复默认的确认清单 `restoreLossItems` 已加 `limits`。
- 后端零改动：解码/校验（`config.go` 的 `decodeLevelLimits` 三态 + `CodeLimitsMissing`）在 N 档那刀就位。
  裁决见 `../decisions/2026-11-11-trust-level-limits-editor.md`。

## Dependency Direction

```
Identity（声明窄接口 TrustLevelSource，不 import 任何域包）
   ↑ 实现                      ↑ 装配（**已做**：bootstrap/api_assembly_domains.go:321）
TrustLevels ──→ Badges（复用 GetCounters 读 activity，不写第二份 SQL）
   └──→ Options（读 trust.levels 阈值）
bootstrap ─→ 把 TrustLevels 的实现注入 Identity
Forum ─╳→ TrustLevels      （Forum 不认识等级域）
```

四条方向约束（D18）：

1. **Identity 不许 import `TrustLevels` / `Badges` / `Options`** —— 三者都 import
   identity，反向就是循环依赖。Identity 只声明窄接口
   `TrustLevelSource { Grants(ctx, userID) (level int, earned []string, err error) }`，
   `LoadActor` 在 `level > 0` 时调用它；接口返回 error 时按 D7 fail-closed 到空集。
2. **TrustLevels 实现该接口**（4 参：`ctx, userID, kind, createdAt`）：读 activity + 读
   options 阈值 → `LevelOf()` → 映射 earnable。`createdAt` 由装载方传入，resolver 不再自己
   读库（D29 修订）。装配在 `bootstrap`。
3. **activity 只读一处**：复用 `Badges.GetCounters`（`activity_store.go:58`，无行返回
   全 0 不报错），**不许写第二份 SQL**。bootstrap 里的适配器把 `Badges.Activity` 转成
   `TrustLevels.Activity`——转换是编译期检查，字段漂移会直接编译失败，比「列集合断言」
   更硬。
4. **Forum 不许 import TrustLevels。** M3 已落地：Forum 侧声明 `TrustPolicyResolver`
   （`Forum/service_trust_limits.go`），实现由 `Providers/TrustPolicyAdapter` 提供
   （`Providers/forum.go:273` 的 `LevelLimits`），`bootstrap` 注入。与 `Badges` 通过
   `Forum.BadgeReader` 反向注入是同一个套路。**注意函数位置**：`limitsForActor` 在
   `Forum/service_trust_limits.go:23`，`service_ops.go:514` 附近是 `containsOutboundLink`，
   不是它（PM 对拍报告此处指错，已记入不一致清单）。

## Capability Resolution — 四来源真值表

### 现状（已核实，**没有**已挣得来源）

`app/Models/Identity/policy.go:22` 的 `Can` 只有三条分支：

```
Can(perm) = IsActive()
         && ( IsSuperAdmin()                    // 绕过一切，含 deny
           || hasExpandedPermission(perm) )     // 角色授予 + 用户覆盖，含父→子继承
```

- `hasExpandedPermission`：`app/Models/Identity/permission_compat.go:57`。
- deny 在**装载时**由 SQL 排除：`database/queries/identity.sql:46-76`
  （`ListUserPermissions` 的 `denied_permission_keys` CTE），再经
  `ExpandEffectivePermissions`（`app/Models/Identity/postgres_store_roles.go:263`）。
- `super_admin` 绕过 deny 是既有设计，`policy.go` 有注释说明（L6）。

### 目标形状（D13 + D14，**已实现于装载期**）

`Can()` **一个字节都不改**（D13）。已挣得在**装载期**合并：`loadCurrentUserAccess`
（`app/Models/Identity/postgres_store_roles.go:245`）是 `Actor.Permissions` 与下发前端
`can()` 的 `CurrentUser.Permissions` 的**唯一物化点**，合并在这里两个消费者自动一致。

「在 `Can()` 里加分支」这条路还有第二个结构性理由走不通：`policy.go:29` 有
`Permissions == nil → false` 的早退，任何「在 `Can` 里额外查一个字段」的实现会在到达
它之前就返回 false。

装载公式（D14，顺序是硬的）：

```
effective = ExpandEffectivePermissions(static_effective) ∪ (earned ∖ denied)
```

先展开父→子，再并 earned，最后减 denied——这样 earned 永远不可能触发父权限展开。
`ListUserPermissions` 必须把**候选集 + 是否生效**一起返回（一次查询、deny 逻辑仍只有
一处），**不许新写第二个 deny CTE 副本**。

实现位置：`Identity/postgres_store_roles.go` 的 `applyTrustLevel`（入参是
`[]store.ListUserPermissionsRow`，逐行看 `Effective`）。它同时填
`CurrentUser.trustLevelResolved` 与 `Actor.Earned`（只含 `earned ∖ static ∖ denied`，
D23），后者专供 `GrantSource()` 解释来源、**不参与裁决**。

四来源真值表（A 组断言就按这张表枚举，`earned` 与 `deny` 组合是重点）：

| static | earned | deny | super_admin | `Can()` |
| --- | --- | --- | --- | --- |
| 0 | 0 | 0 | 0 | false |
| 1 | 0 | 0 | 0 | true |
| 0 | 1 | 0 | 0 | true |
| 1 | 1 | 0 | 0 | true |
| 0 | 0 | 1 | 0 | false |
| 1 | 0 | 1 | 0 | false（deny 压 static） |
| 0 | 1 | 1 | 0 | **false**（earned 不得复活被 deny 的键，计划 T4） |
| 1 | 1 | 1 | 0 | **false** |
| 任意 | 任意 | 任意 | 1 | true（super_admin 绕过一切） |

> **D14 的存在理由（已裁决，勿再讨论）**：deny 在装载期就把键从 `Permissions` 删掉了
> （`identity.sql:46-76`），`Actor` 上**不留痕**。若把 earned 并进 `Can()`，可解锁且可
> 被 deny 的键（`topic.create`）会被复活，直接违反 T4；D6 的结构不变量只保证
> earnable ∩ 管理类 = ∅，**挡不住**这种情况。
> 候选方向里 `Actor.Denied` 集合**已被否**：它会让 `Actor.Permissions` 不再等于有效集，
> 出现两份「能不能」的表示。选定方案是「装载期 deny 之后合并」。

### 默认能力与限额（D15 / D16 / D17 定稿）

**默认 earnable = ∅。** PM 已核实 `SeedMemberPermissions`（`Identity/seeds.go:153-161`）
含全部 7 个非管理内容键，其余 8 个非管理键是员工权限（any / lock / pin / revision），
进 earnable 会撞 D6。**所以「TL2 解锁发帖」在默认安装里不存在**——权限解锁是运营在
后台开启的框架能力，不是开箱行为。

> **已被 D34 修正（2026-11-11，Z3-b″）**：上一段最后半句「不是开箱行为」在**权限集合**层面
> 已不成立——`topic.create` / `attachment.upload` 两个基线键由**装载期减法门**在 TL0 扣下、
> TL1 起恢复，默认安装即生效。`earnable` 默认空集这一半仍然为真（D17 不动）。
> 机制、四条不扣与已知缺口见文末「D34 增量」。

| TL | 名称（slug，D10 修订版） | 默认解锁 | 默认限额 |
| --- | --- | --- | --- |
| 0 | 新用户 `newcomer` | ∅ | 沿用 `trust.new_user*` 七键现值 |
| 1–4 | 正式用户 / 常驻用户 / 资深用户 / 领袖 | ∅（运营可配） | 站点默认，不叠加额外限制 |

> 本表的「默认解锁」列只描述 **earnable**。D34 之后，TL0 的**有效权限集合** = `member` 基线
> 7 键 − 基线门 2 键 = 5 键；TL1+ = 7 键。两者不要混为一谈。

> 计划 §5 的表仍写着初稿名（新成员 / 成员 / …），**以 D10 为准**（全档带「用户」、与
> 角色词族零字面重叠）。计划 §5 与 D10 的这处不一致已报给 lead，等计划侧回填。

站点默认冷却与日限额都是 `0`（`Options/service.go:980-988`：
`forum.topics.cooldown_seconds`、`forum.topics.daily_limit`、
`forum.comments.cooldown_seconds`、`forum.comments.daily_limit`），所以「TL3 免冷却 /
TL4 日限额放宽」这类文案在本仓库是假的，**已从计划与文档里删掉**。默认安装只有
TL0 与 TL1+ 之间有差异——这是事实，照实写。**D34 之后差异不止限额**：TL0 还少两个基线键
（见文末「D34 增量」）。

两条结构约束：D15 —— earnable 与 `legacyPermissionChildren` 的**父键**必须零交集
（否则 earned 会隐式展开出一串子权限）；D16 —— `features.trust_levels` 要进 5 处
（常量、optionDefinitions、结构体读取、`FeatureFlagCatalog()`、
`featureFlagRecommendedDefaults()`），并顺手补 `features.badges` 的既有缺口
（`feature_flags.go:63-98`）。

### 派生视图（不是第二权威）

`Actor.GrantSource(perm) → super_admin | static | earned | none` 只读、只在装载时
算一次，**只能解释 `Can()` 的结论，不能反过来影响它**。消费者只有两个：前端解释
「为什么我不能发帖」、后台展示「这个能力谁能给、从哪来」。

**已实现且已定调**（`Identity/permission_origin.go`）：`static` 刻意**不区分**「角色授予」
与「用户 allow 覆盖」——DB 层已把两者合并成同一份列表并排除了 deny，要区分必须多一次
查询，不值得放在每个已认证请求的热路径上。所以派生视图只有四值
`none / super_admin / static / earned`，没有 `override`。与 `Can()` 的同构关系由
`policy_test.go` 的表驱动断言守住。

### 改动范围

`apps/api` 非测试代码现有 **256 处 `.Can(` 调用点**（全仓含测试 280）。数这个数只认
命令，不认估算——本文件与计划都在这三个数上错过，T19 已把它定为纪律：

```bash
rg -o "\.Can\(" -g '*.go' -g '!*_test.go' | wc -l   # 256
rg -o "\.Can\(" -g '*.go' | wc -l                    # 280（含测试）
```

这 256 个点**一行都不用改**——它们继续只问 `Can()`。

## 新人阶梯与 TL0（M3 已收编）

**`trustForActor` 已改名为 `limitsForActor`（`Forum/service_ops.go:514`）**，限额改由等级决定，
七个 option 键全部保留、数据未迁移。限额与权限仍然分离（D8）：限额回答「多频繁」，`Can()`
回答「能不能」，限额不进 `Can()`。

今天仍在生效的五道判据（`service_trust_limits.go:23-57`，收编时逐条保留，少一条就是
「昨天注册的新版主被新人限流」）：

| 判据 | 条件 | 行为 |
| --- | --- | --- |
| 1 | 策略未注入或 `CreatedAt` 为零值 | 不限制 |
| 2 | `actor.Kind.IsBot()` | 不限制（AI 配额与审核闸门另有约束） |
| 3 | `settings.manage` 或 `topic.edit_any` | 不限制（员工不进新人限制） |
| 4 | `TrustLevelResolved` | TL0 → 新人七键；TL1+ → 站点默认 + 分档覆盖 |
| 5 | 未解析（开关关闭 / 统计读失败） | 回落年龄两态：`days <= 0` 或已过窗口 → 不限制，否则新人七键 |

`resolveLimit`（`:101`）区分「新人只收紧 / 分档可显式覆盖」：TL1–TL4 的限额默认全部继承
站点默认（D27，JSON `null` = 继承），放宽必须由运营显式配置。

七个 option 键的**字面量**（注意第一项是下划线、其余是点分，别照计划抄错）：

| 键 | 后台默认值（`SFAdminSiteNewcomersTab.vue`） |
| --- | --- |
| `trust.new_user_days` | 7（0–365） |
| `trust.new_user.topic_cooldown_seconds` | 300 |
| `trust.new_user.comment_cooldown_seconds` | 60 |
| `trust.new_user.daily_topic_limit` | 3 |
| `trust.new_user.daily_comment_limit` | 30 |
| `trust.new_user.forbid_outbound_links` | 开 |
| `trust.new_user.forbid_attachments` | 关（死配置，见残留 3） |

定义在 `apps/api/app/Models/Options/types.go:115-121`；后台入口是
`apps/web/app/components/admin/settings/site/tabs/SFAdminSiteNewcomersTab.vue`
（i18n 键 `admin.settings…newcomers`，中文标签「新人限制」）。

年龄从「唯一判据」降级为「上限闸」：`Policy.NewUserDays` 表示「最多当多久新成员」，活动量
足够可提前到 TL1，但**不能提前越过 TL1**（`level.go` 的两段式判定）。

## Feature Flag And Permissions

**开关 `features.trust_levels` 已五处注册**（D16）：常量 `Options/feature_flags.go:35`、
`optionDefinitions` `:63`、`FeatureFlagCatalog()` `:82`、`featureFlagRecommendedDefaults()`
`:97`（推荐开）、结构体字段 `:130` + 读取 `:153`；归一化分支在 `service_normalize.go`。
前端已消费（`composables/useWebOptions.ts`）。**F 组断言现在可以执行。**

关闭后的目标行为（F 组判据）：已挣得集合恒空、限额回落到年龄两态。实现路径是
`TrustLevels/resolver.go` 的 `ErrDisabled` → identity 侧 `ErrTrustLevelsDisabled` →
**静默**保持「未解析」，不写 warn（关闭不是故障）。**开关必须在调用 resolver 之前短路**
（D31）：关掉的功能不花查询，同时让「同一二进制翻开关」成为性能 A/B 的唯一手段。

**后台写权限 `trust.manage` 已闭合**：常量与目录 `Identity/seeds.go:84` / `:144`、
迁移 `database/migrations/202611100004_trust_manage_permission.sql`（插键 + 授
super_admin + Down 回收）、前端 `apps/web/app/config/roleTemplates.ts:69`。
奇偶校验 `TestEverySeededPermissionReachesTheDatabase` 覆盖「seeds 有、迁移没有」。

**不做 `trust.view`**（D9）：后台读面复用 `admin.access` + 只读展示。论证**不是**
「`badge.view` 是死权限」——那条前提已被 2026-11-11 实测推翻（见下），现行论证是：
`trust.manage` 已经有**四道门**（页面 `admin.access` / 侧栏 `settings.site.manage` /
读 `admin.access` / 写 `trust.manage`，证据 `core_catalog_gen.go:231-233` +
`adminModules.ts:111-116`），再补一个 `trust.view` 只会造出**第五个权限位**，而读面本来
就被 `admin.access` 覆盖；新增一个权限位的成本（seeds + 迁移 + 权限目录 + 前端登记点 +
可达性审计）大于收益。

> **订正（2026-11-11 09:30）**：父计划 §6 用「`badge.view` 是死权限」来论证
> 「权限可达性有前提」，该前提**不成立**：`badge.view` 在 `core_catalog_gen.go:44` 有守卫、
> 在 `adminModules.ts:229` 被侧栏消费（本文件作者 09:28 复核同款结论）。
> **结论保留**（不做 `trust.view`），论证链按上一段重写。父计划作为历史证据**不改**；
> 详见 researcher 审计 §6。顺带提醒：`badge.view` 是活权限，别在别处再当反例用。

## Extension Surface

口径按 `decisions/2026-10-18-extension-data-surface-policy.md` 的四类面（views / queries /
commands / events）。信任等级在 2026-11-14 已把四类面各占一条，逐条如下。

### 1. 事实条件声明 → 走 **manifest 声明 + Host 命令**（M1–M2）

插件用 `userFactConditions`（中性名，canonical）声明可上报的事实键，`badgeConditions` 是同一份
东西的兼容别名：两个数组合计上限 **32**、`key` 跨数组唯一、`contractVersion` 必须恰好
`key@N`、扩展必须有 `backend` 且 `protocolVersion: 2`
（`app/Support/ExtensionManifest/v3_badge_conditions.go:61-146` 的 `validateFactConditions`，
上限常量同名文件 `:27` 的 `BadgeConditionsMaximum`）。
数据由 Host 命令 `badges.fact.record` 写入，调用方需声明能力 `badges.fact.write`
（`app/Support/Capabilities/catalog.go:33`）。

同一份声明**同时喂徽章规则与信任等级规则**（`app/Models/Conditions/facts.go` 的合并入口）。
未声明的 factKey 让叶子变 *unknown* → 该档不成立（fail-closed），已声明的查不到行按 `0` 算。
插件停用后它的 key 在 5 秒目录刷新窗口内从候选消失（`bootstrap/badges_fact_catalog.go:49` 的
`badgesFactCatalogTTL = 5 * time.Second`），引用过它的规则仍可解码、只是不再命中。

### 2. 可被等级授予的权限 → 走 **manifest 声明 + 运行期白名单 + 写期拒绝**（M4a）

插件在自己的 `permissionDefinitions[]` 上声明 `trustLevelEarnable: true`
（`app/Support/ExtensionManifest/v3_platform_types.go:224-229`，缺省 `false` 是安全默认）。
运行期白名单 = 核心七键 ∪ 已启用插件声明的键（`app/Models/TrustLevels/registry.go:31-39` 的
`earnableWhitelist`、`:260-268` 的 `State`、`:299-308` 的 `FilterGranted`）；解码期只判结构；
写期对「从未被声明」的键报 `422 earnable_not_declared`
（`registry.go:328`、`app/Models/TrustLevels/config.go:98`）。读面三态 `valid / inactive / unknown`。

**完整裁决与降回条件**：`decisions/2026-11-14-trust-level-earnable-plugin-keys.md`。

### 3. 等级变更事件 → 走 **event 面（observe 型，只观察不拦截）**（M4b-2）

事件名 `trust_level.changed`（常量 `app/Support/Events/catalog.go:49`，目录定义 `:114`），
载荷 `userId / fromLevel / toLevel / fromTierId / toTierId / computedAt`。
发事件的前置是「重算结果与变更账本不同」（`app/Models/TrustLevels/recompute.go:157`），因此
**三种路径不发**：求值 `incomplete`（`recompute.go:117`）、该用户首次落账本（`:143`，只落账本）、
结果未变化（`:148`）；产品面关闭时整段跳过（`Skipped: "disabled"`）。
插件侧消费这条链的受支持路径是「事件 → 事实」（经 `badges.fact.record` 上报），事件本身是
Host 的可观测面。

**触发点共 6 处**（复用既有徽章评估入队点，一次 `EnqueueUserEvaluations` 同时入队两个 job，
`bootstrap/badges_wiring.go:42-60`，两者互不吞错）：
`Forum/postgres_store_reactions.go:62`（被赞者）与 `:63`（点赞者）、
`Forum/postgres_store_bookmarks.go:47`、`Forum/postgres_store_comment_writes.go:134`、
`Forum/postgres_store_topic_mutations.go:122`、`Badges/visits.go:137`（访问）。
策略保存另走游标批量入队（不是这 6 处）。

**每日 sweep 兜底**：`app/Support/Jobs/core_schedules.go:27`（`ScheduleTrustLevelsSweep`，按账本
陈旧度重算），装配在 `bootstrap/worker.go:819`；它覆盖「角色变更 / OAuth 解绑 / 徽章收回」
这类没有活动事件的变化。

### 4. 读等级目录与单用户等级 → 走 **HostQuery 面**（M4b-3）

| Query ID | 语义 |
| --- | --- |
| `sforum.core.public_trust_level_catalog.list` | 档位目录（level / levelSlug / displayName / icon / color / requirements / earnable） |
| `sforum.core.public_user_trust_level.get` | 单用户当前等级，**现算**（`Resolver.Evaluate`），不读账本表 |

注册：`app/Support/HostAPI/query_registry_core.go:444` 与 `:452`；常量
`query_registry_core.go` 同目录 `v2_query_runtime.go:42`/`:45`。
`get` 的 `state ∈ {resolved, incomplete, disabled}`，后两者 `level` / `tier_id` 为 `null`——
「等级是 0」与「算不出来」在调用方那里必须可区分（`app/Support/HostAPI/query_registry_core_schema.go:226-228`）。
守门人：`app/Support/HostAPI/trust_level_queries_postgres_integration_test.go:211`
（`TestUserTrustLevelSourceKeepsIncompleteAndDisabledNullable`）与 `:133`
（`TestUserTrustLevelSourceComputesInsteadOfReadingLedger`）。

**引擎侧新增的「计算形态」裁决**：`decisions/2026-11-14-hostquery-computed-definitions.md`。

### 5. 求值路径的架构约束（插件不可见，但会影响插件观察到的时延与失败姿态）

条件求值在 `TrustLevels` 侧拼一棵**合成 `all` 根**，只 `Prepare` 一次、逐档 `Eval` 复用 memo；
合成根只装「数值预筛已过且带 rule」的档位，且不进入任何返回值。
**完整裁决与降回条件**：`decisions/2026-11-14-trust-level-synthetic-rule-root.md`。

### 6. 明确不做

见计划 `plans/2026-11-14-trust-level-conditions.md` **§6「明确不做」的 7 条**
（审核信号条件 / 内容质量条件 / 阅读行为 / RPC 条件求值器 / 等级反规范化列 / 公开的逐条清单 /
规则改严后的追溯收回）与本文件上文 §「已作废结论 → D. 明确不做」。这里不重复解释。

### 7. manifest-v3 schema 的 kind 分支（M4a-O1 结论，2026-11-14）

七个「只允许插件」的声明家族在 schema 里各有 `if required <family> → then.type.const=plugin`
分支：`identity` / `badgeConditions` / `userFactConditions` / `permissionDefinitions` / `seo` /
`aiTools` / `notificationTypes`（`app/Support/ExtensionManifest/schemas/manifest-v3.schema.json`
的 `allOf`，实测 7 条分支）。

这七个家族**不再手写两份**：Go 侧清单 `v3PluginOnlyDeclarationFamilies`
（`app/Support/ExtensionManifest/v3_validate_platform.go:95-106`）是 kind 判据的唯一来源，
`validatePluginOnlyFamilies()`（同文件 `:112-124`）在 `validatePlatform` 入口集中判一次 kind；
测试做**双向比对**（清单 ⇄ schema 分支，任一多出即红并指名，
`v3_schema_kind_families_test.go:190-200`）。

> **曾经的缺口（已修，`bcbe314ff` / task-7）**：`manifest.go` 的 `isThemeManifestSupported`
> （定义 `:688-712`）原先在 `validateV3Manifest` **之前**就拒主题，而它显式查
> `SEO` / `Identity` / `PermissionDefinitions` / `Backend`——identity / seo / permissionDefinitions
> 这 3 个家族在主题侧走不到清单判断。修复把 `validatePluginOnlyFamilies(manifest)` 提到
> `validateManifest` 里、`isThemeManifestSupported` **之前**判一次，**7 个家族全部由清单先拒**且
> 报错带家族名。见残留 6。

`notificationTypes` 刻意**只补 kind、不加 backend 要求**：Go 侧只要求 plugin，schema 若同时要求
backend 会拒掉 Go 会放行的包。它的主题侧双侧判据（Go 拒 + schema 拒）在 `0b38458bd` 里被删过一次，
**已由 `bcbe314ff` 恢复**：`v3_schema_kind_families_test.go:376` 的
`TestManifestV3SchemaRequiresPluginForNotificationTypes`；另加 `:323` 的
`TestManifestV3PluginOnlyFamiliesBehaviourallyRejectThemes`，逐个跑满 7 个家族
（aiTools / notificationTypes / seo / identity / badgeConditions / userFactConditions /
permissionDefinitions）。

**没有任何生产路径只跑 `ValidateV3JSONSchema` 而不跑 `Validate`**：装载路径两处 schema 校验
（`load.go:99` 原始 body、`:116` canonical 重编码后）之后，`:120` 必定调用 Go 侧 `Validate`。
主题侧的顺序现在是：清单（`validateManifest`，带家族名）→ `isThemeManifestSupported` →
`validateV3Manifest`。所以主题声明 plugin-only 家族时，**第一条报错来自清单**；schema 层的拒绝
理由指向 `type=plugin`。两侧都拒，但**理由不同**，读错误信息时别把它们当成同一道闸。

### 8. 条件与影响面估算的边界（`identity.profile` 不参与估算）

保存前的「≈ N 人」由 `app/Models/Badges/rule_impact.go` 算，走**聚合 SQL**（`countLeafMatches`，
`:145` 起）而不是逐用户求值。它只认识写进 case 的那些条件：
`identity.email_verify` / `identity.avatar` / `identity.role` / `identity.account_age` 与六个数值指标
（`:183-199`），其余一律落到 `default: return 0, false, nil`（`:204-205`）。

**`identity.profile` 刻意不在其中**：该条件被列进「无法估算」（`impactMethodUnsupported`）；
若同一档还有可估的数值门槛，AND 分支仍给上界但标 `partial=true`
（`impactMethodStrictestLeafPartial`，`rule_impact.go:104`；`Partial` 的语义见 `:25-26`）。
弹窗里它显示「无法估算」而不是「0 人」——**这是设计，不是 bug**。

**裁决（2026-11-14，lead）**：不给它补一份 count。理由是不引入**与求值 SQL 不同源的第二份口径**：
估算一旦和判定用两套 SQL，运营会拿两个对不上的数对账，而估算本来只是「上界参考」。
**谁负责/何时消**：边界裁决，不是待办；将来若要支持估算，先解决「同一口径」再谈数字。

## 登记点：新增后台 tab 或公开组件是 **9 处**，不是 6 处

V3 目录的生成器 `scripts/v3-catalog/generate.mjs` 要求每个新增组件在
`docs/extensions/v3/catalog-identities.json` 有 reviewed identity，**缺失时直接抛错**
（不是静默失败）。所以完整登记点是：

| # | 位置 |
| --- | --- |
| 1 | 路由 shell 的 tab 联合类型（独立页形态 = `pages/admin/forum/trust-levels.vue` 的 `TrustLevelsTab`） |
| 2 | 同一文件的 `validTabs` 数组 |
| 3 | 同一文件的 tab 列表（label + icon） |
| 4 | 同一文件的 tab → 组件映射 |
| 5 | 组件文件本身（`components/admin/forum/trust-levels/tabs/SFAdminTrustLevelThresholdsTab.vue` 等） |
| 6 | 两份 locale 的 i18n 键（`zh-CN.json` / `en-US.json`） |
| 7 | `docs/extensions/v3/catalog-identities.json` 的 reviewed identity（页一条 + 每个组件一条） |
| 8 | 生成产物里的 catalog 条目（由生成器写入，`--check` 会红） |
| 9 | `tests/validate-admin-framework.ts` 的 `adminPagePathsById` 映射（**新增后台页**才需要，新增 tab 不需要）——漏了是门禁红 |

漏 #1–#4 的后果是**静默回落**（tab 不显示或回落到第一个 tab）；漏 #7 的后果是**生成器
抛错**，比静默温和，但同样挡提交；漏 #9 的后果是静态门禁直接红
（`Admin module registry points to an unknown page id`）。别按「6 处」记。

**2026-11-11 修订：本刀从「设置页内 tab」改成「独立页 + 页内 tab」，9 处全要，只是
#1–#4 的目标文件换了。N 档改造在同一 shell 内加到四 tab（新增 `tiers` 与图标选择器），2026-11-11 又追加 `limits`（现为五 tab），**不新增 page id**，所以 #9 无新增行、#7 只补两条 active identity（`SFAdminTrustLevelTiersTab.vue`、`SFAdminTrustLevelIconPicker.vue`）。
**2026-11-12 修订**：外观选择器拆成 `SFAdminTrustLevelAppearanceDialog.vue` / `SFAdminTrustLevelAppearancePreview.vue` / `SFAdminTrustLevelColorField.vue`，旧的 `SFAdminTrustLevelIconPicker.vue` / `SFAdminTrustLevelColorPicker.vue` 已删除，所以 #7 现在是「新增 3 条 active identity + 2 条迁到 retired 清单」并重生成 `core_catalog_gen.go`。

| 形态 | 需要 | 说明 |
| --- | --- | --- |
| **设置页内的 tab**（无独立 page id，旧形态，已被取代） | #5–#8 | #1–#4 指 `pages/admin/settings/index.vue`；#9 不适用 |
| **独立后台页 + 页内 tab**（现形态） | **#1–#9 全部** | #1–#4 指新 shell `pages/admin/forum/trust-levels.vue`；#5 是每个 tab 的组件文件（现为五个 tab + `SFAdminTrustLevelAppearanceDialog.vue` / `AppearancePreview` / `ColorField`）；#9 必须有 `/forum/trust-levels` 一行 |
| 独立后台页但无 tab | #5–#9 | #1–#4 不适用（没有 tab 数组可登记） |

判据只有一条：`adminModules.ts` 里有没有它的 `id`。有 `id` 就必须同时补
`adminPagePathsById` 一行（否则门禁红），并把 `pages/admin/<id>.vue` 写进
`definePageMeta({ layout: 'admin' })` + `defineOptions({ name: '<componentName>' })`
+ `useAdminPage('<id>')`。**没有 `id` 的 tab 不要往那张表里加**——加了一张没有对应
registry 条目的映射是死行。

### 迁移既有组件时：catalog identity 按 `source` 匹配

生成器**不是**按组件名或目录匹配 UI identity，而是按 `source` 路径建索引：
`scripts/v3-catalog/generate.mjs:705` `const uiMap = reviewedIdentityMap(activeUI, item => item.source, 'active UI sources')`。
所以移动文件时的规则是硬性的：

1. **必须改 `source`**，否则新路径在 `uiMap` 里查不到 → 生成器抛
   `new or moved UI surface needs a reviewed stable identity mapping: <source>`
   （`generate.mjs:802-803`），`--check` 直接红。
2. **`id` 与 `contractVersion` 一律不动。** 它们只用于唯一性/保留，不参与匹配
   （`generate.mjs:701-702` 对全部 identity 做 `id` / `contractVersion` 唯一性校验，
   另有 `retiredUIByID` / tombstones 账本）。改 id 等于**把旧 id 提前退役**，而退役是
   append-only 墓碑流程，不属于一次文件移动。
3. `state` 保持 `active`，`kind` / `owners` 不变（漂移会各自抛错：kind 见
   `generate.mjs:805`，ownership 见 `:808`）；页面身份（`kind: 'page'`）的 `owners` 必须
   恰好一个（`generate.mjs:730`）。

本刀实际动到的六个 UI identity（2026-11-11 收尾实测，`docs/extensions/v3/catalog-identities.json`）：

| id | 处置 | source（文件名） |
| --- | --- | --- |
| `...admin.sfadmin_trust_level_earnable_matrix` | `active`，**只改 source**（文件换目录） | `SFAdminTrustLevelEarnableMatrix.vue` |
| `...admin.sfadmin_trust_level_earnable_tab` | **新增** `active`（earnable 面板 = frame + 矩阵） | `SFAdminTrustLevelEarnableTab.vue` |
| `...admin.sfadmin_trust_level_thresholds_tab` | **新增** `active`（旧 tab 拆成只吃 props 的分区组件，文件名也变了） | `SFAdminTrustLevelThresholdsTab.vue` |
| `...admin.sfadmin_trust_levels_overview_tab` | **新增** `active`（overview 纯展示面板） | `SFAdminTrustLevelsOverviewTab.vue` |
| `...page.admin.forum.trust_levels` | **新增** `kind: page` / `owners: ["admin"]` | `pages/admin/forum/trust-levels.vue` |
| `...admin.sfadmin_trust_levels_tab` | **转 `retired`**（旧 `SFAdminTrustLevelsTab.vue` 已删），`id`/`contractVersion` 原样保留、`source` 保留历史路径 | 旧路径 |

**两条处置的分界线**（这是本节真正要传下去的东西）：

- **文件移动（同内容、同职责）→ 只改 `source`**，`id` / `contractVersion` / `state` 全不动。
  earnable 矩阵就是这一类。
- **改名或拆分（职责变了）→ 新增 identity，旧 identity 转 `retired`**。阈值 tab 就是这一类：
  它从「自带表单状态与 footer 的 tab」变成「只吃 props 的分区组件」，不是同一样东西。
  **不要**把退役当改名用（改 `id` 去复用一条 active 记录），退役走的是 append-only 墓碑账本。

**实测（2026-11-11 16:50 复核）**：`node scripts/v3-catalog/generate.mjs --check` →
`V3 P0 catalogs are current (18 files).`；新页已进生成产物
（`docs/extensions/v3/catalogs/admin-surfaces.md:38` 的 `core.admin.page.forum_trust_levels`、
`ui-surfaces.md:164` 的 `core.component.page.admin.forum.trust_levels`）。
注意**两套 id 不是同一个东西**：Admin Surface 登记用 `core.admin.page.*`，UI identity 用
`core.component.page.*`，别互相引用。


**已浏览器验证**（`storage/qa/trust-levels-n/20261111-184009-qa-reshoot-main.json`，21 pass / 1 fail，
失败项单列在下一段）：四 tab 切换与 `?tab=` 同步（点第 2/3/4 个 tab → `panel=tiers|thresholds|earnable`；**当时还没有 `limits` tab，现为五 tab，这一轮的 tab 数证据已过期**）、
N=8 桌面矩阵 `layout=cards`、390×844 `layout=cards rows=7` 且无横向溢出（390/390）、窄屏工具栏动作
均带文本或 `aria-label`+`title`、图标选择器键盘可达（`20261111-184200-qa-icon-picker-keyboard.png`）。

**当时另有一条 FAIL（现已闭环，见下段）**：`save-field-errors` —— 名称超 32 字保存时行内字段
错误 0 处、页面级错误块 false、无 toast、配置未落库（拒绝本身正确，但**运营看不到提示**）。
证据是同一份 json；首轮「21 pass / 1 fail」的记录保留，它是「修了两轮」的起点，不是现行缺陷。

**已闭环（2026-11-11 终验）**：最初 `save-field-errors` FAIL 的根因**不在渲染，而在通道**——
当时保存走通用 `PUT /admin/web-options`，其 422 不回 `details.errors[]`；18:50 之后切到专用
`PUT /admin/trust-levels` 才带字段级明细。终验 `--phase=main` 实测真实 422 + 行内落点
（截图 `storage/qa/trust-levels-n/20261111-185340-qa-final-422-inline-error.png`）。
证据总表 `storage/qa/trust-levels-n/report.md` §9（`--phase=regress` 15/0、`--phase=main` 22/0）。
**历史保留**：最早的 FAIL 证据 `20261111-184009` 跑在通道切换之前，已作废——不删，是因为
「修了两轮」本身就是事实。

## Known Residuals（不要当 bug 修）

> **N 档改造一并修复，不是遗留（别当债捡起来）。** 「TL4 是最高档」的三处残留假设已全部改走
> `Policy.LastRank()`：`Earnable.All(lastRank)`（`registry.go:81-83`，上界由调用方传入）、
> 后台 maxed 判据（`Http/Controllers/TrustLevels/controller.go:115`）、Progress 上界
> （`resolver.go:112`）。
>
> **判据在编译期，不在正则**（常量的声明已经从 `types.go` 删掉了）：
> - `rg -n "LevelLeader\s*(Level)?\s*=" apps/api/app/Models/TrustLevels/types.go` → **零命中**（声明已删）
> - `cd apps/api && go build ./...` → **exit 0**（没有引用残留：Go 里删掉的标识符一旦被引用就编译不过）
> - `rg -n "func .*LastRank" apps/api/app/Models/TrustLevels/` → 命中 `types.go` 的 `LastRank`
>
> **注释里出现这个字面量是预期的，不构成残留。** `registry.go:79` 与 `resolver.go:108` 记的是
> 一起真实事故（「这里曾经写死 `Grants(LevelLeader)`，于是 8 档站点上第 5–8 档的键会从
> `All()` 里静默消失」）。拿正则去逼这两行注释消失，等于为了让门禁变绿而销毁事故史——
> 与 `EligiblePermissions` 那条「无消费者 ≠ 可删」是同一个道理。

1. **M1–M5 已落地（含公开面与资料页等级卡），M6–M8 未完成。** 别再按「未接线 / 端点不存在」
   理解现状：等级、限额、公开等级接口、资料页等级卡都已生效。
2. **父键展开出的子键被 deny 后会被加回**（既有怪癖，本刀不修）。deny 一个由父权限展开出来
   的子键（持有父键时），`ListUserPermissions` 把它标成不生效，但
   `ExpandEffectivePermissions` 随后按父键重新加回。修它会改变既有静态语义，另开一刀。
3. **`trust.new_user.forbid_attachments` 是死配置，运营勾了无效。** 值会存、后台会显示、
   `Options.TrustPolicy.ForbidAttachments` 也读出来，但**没有消费者**：`limitsForActor` 只读
   `forbid_links`。D20 裁决本刀不接线（接线是运行时行为变更，会破坏 F 组基线）。
   别在文档里写成生效的开关（「存下来但没人读」是这类配置的共同形状）。
4. **默认 earnable = ∅**，所以「TL2 解锁发帖」在默认安装里不存在。权限解锁是运营开启的框架
   能力（D17 要求必须有编辑入口，否则机制是死代码）。裁决见
   `../decisions/2026-11-11-trust-level-earnable.md`（D33：走 A 路补编辑器，默认空集不变）。
   **D34 补充**：「TL1 解锁发帖」这件事在默认安装里**存在**，但它走基线门、不走进 earnable——
   所以别把本条的「不存在」推广成「等级默认不影响权限」。
5. **TL1–TL4 默认限额 = 站点默认**（D27：`null` = 继承，全档默认继承）。站点默认冷却与日限额
   都是 `0`，所以默认安装只有 TL0 与 TL1+ 之间有差异。
6. **`LoadActor` 的查询基线是 4 次**（主查询 + 角色 + 权限 + `created_at`/`kind` 合并读），
   信任等级再加 1 次 activity 读 → **4→5**（R7 的「同请求读两次」已收口，别再按 4→6 算）。
   按 T17：`LoadActor` 有 45 个调用点、单请求可能调 ≥2 次，性能预算按 +N 算。
7. **deny 的真实位置**是 `database/queries/identity.sql`（`denied_permission_keys` CTE），
   不是 `postgres_store_permissions.go:49`（那是收件人查询）。
8. **派生视图只有四值，没有 `override`**：`GrantSource()` 返回 `PermissionOrigin`
   （`none` / `super_admin` / `static` / `earned`），`static` 刻意不区分「角色授予」与「用户
   allow 覆盖」——多一次查询不值得放在热路径。这是已定调的设计（D30），不是待补的 TODO。
9. **命名与 i18n 有三条硬约定**：
   - 真实键族只有两个：公开侧 `profile.trustLevel.*`、后台侧 **`admin.forum.trustLevels.*`**。
     `trust.level.*` 与 `trustLevels.*` 都**不是** i18n 键族（前者已零命中，`NameKey()` /
     `DescriptionKey()` / `ThresholdOptionKey()` 三个死生成器都已删除）。
     **旧说法「后台侧是 `admin.settings.trustLevels.*`」已作废**（2026-11-14 实测，三处证据）：
     ① `apps/web/i18n/locales/{zh-CN,en-US}.json` 的 `admin.settings.trustLevels` 均为
     `undefined`，`admin.forum.trustLevels` 在两份 locale 都存在；
     ② 源码断言 `apps/web/tests/admin/forum/trustLevels.test.ts:848`
     （`expect(locale.admin.settings.trustLevels).toBeUndefined()`）把这条钉成了红；
     ③ 死键棘轮 `apps/web/tests/admin/forum/trustLevelI18nDeadKeys.test.ts` 以
     `admin.forum.trustLevels` 为命名空间，登记表已收缩到空数组（任何新孤儿键当场红）。
     **叶键数按实测：394**（`zh-CN` 与 `en-US` 同数）。计数命令：
     `cd apps/web && node -e 'const j=require("./i18n/locales/zh-CN.json");const f=(n,p="")=>n&&typeof n==="object"&&!Array.isArray(n)?Object.keys(n).flatMap(k=>f(n[k],p?p+"."+k:k)):[p];console.log(f(j.admin.forum.trustLevels).length, j.admin.settings?.trustLevels)'`
     → `394 undefined`。**两个旧数都不可复现，已按实测改正**：
     `git show <rev>:apps/web/i18n/locales/zh-CN.json` 同法计数 → `5a1d4083f` = **360**
     （`rule` 76 / `conditions` 35 / `ruleConditions` 0）、`0b38458bd` = **394**
     （`ruleConditions` 补到 42，同时 F1 删掉 8 条孤儿键：360 + 42 − 8 = 394）。
     所以 **243 在任何一个已提交版本都不成立**（写 ui-spec 的那一版就是 360）；
     死键棘轮注释里的 360 是 `5a1d4083f` 的旧值。两处已在各自文件按实测改正，
     `apps/web` 里那条注释归属下一次碰该测试的单。
     **旧族在 `knowledge/` 之外的唯一引用是那条断言本身**
     （`grep -rn 'admin\.settings\.trustLevels' apps/ scripts/ tests/ docs/` → 仅
     `apps/web/tests/admin/forum/trustLevels.test.ts:848`，即它只以**反例**存在）；
     `scripts/verify-trust-levels-acceptance.sh` 已零引用，H6/H17 只断 `profile.trustLevel.name`
     单族（`:1943-1971`）。
   - **「档位名 ∩ 角色名 == ∅」只对 zh 成立**：`admin.roleCatalog.member.alias` 的 **en 值也是
     `Member`**，与 TL1 en 值相同 ⇒ en 交集 = `{Member}`，**已知且接受**。机器断言只对 zh 生效，
     别写成双语。
   - **i18n metric 键是 camelCase**（`topicCount` / `daysVisited`），**API `requirements` 是
     snake_case**（`topic_count` / `days_visited`）。写「两者相等」的断言必红。
10. 文档侧：运营页**刻意不写端点路径**——`tests/validate-docs.mjs` 要求文档里每个
    `METHOD /api/v1/...` 同时存在于 OpenAPI 与 Go 路由目录，M4 未落地前写端点会直接把门禁
    打红。M4 落地后拿到端点清单再补。
11. **存在第二个 actor loader：`LoadEffectiveActorTx`（`Identity/actor_postgres_tx.go:112`）**，
    已按 D22 接入 `TrustLevelSource`，但它**历史上不展开父→子权限继承**（该文件内
    `ExpandEffectivePermissions` 出现 0 次）。后果：`extension.manage` 持有者在**插件命令
    路径**今天就被误拒。本刀**不修**这个缺口（另开一刀）。**看到「插件命令 403 而 HTTP 放行」
    先查这条，不要当成 TL 的 bug。**
12. **`EligiblePermissions()` 无生产消费者，但不能删。** 唯一调用点是 `registry_test.go`，它
    断言的是**字面白名单集合**（与实现走不同路径），且守着一段真实事故史（旧实现是负面过滤 +
    `.override` 后缀表）。判据：**无生产消费者 ≠ 可删**，要看测试是否走了不同路径、是否守着
    事故史——与「自证式断言不算断言」互补。
13. **系统账号「恒 TL0」是构造性保证，不是断言保证**（D25 收窄后）：机器人（`kind != human`）
    由 `Grants` 直接不授予；系统账号（`builtin-auto` / `dev-auto`）`kind` 仍是 `human`，靠
    「没有 `user_activity` / `user_visits` 行 ⇒ 计数全 0 ⇒ 构造性停在 TL0」。**边界**：只保证
    「挣不到等级」，**不保证「不吃 TL0 限额」**——若走 forum 写路径会被新人限额拦（冷却 300s、
    日限额 3）。
14. **仓库已不能安全跑 `sqlc generate`**：本刀只手工同步了 `ListUserPermissions` 一个查询
    （小审逐字比对：形状正确，唯一差异是 sqlc 剥掉的 `ORDER BY 1` 分号）。当前 sqlc 版本会
    把 `database/sqlc/models.go` 从 84 行推到约 2481 行、重命名 param、留下一批未跟踪文件。
    要修单开一刀。
15. **deny 全量已修（D28）**：`identity.sql` 现在 `UNION SELECT permission_key, FALSE FROM
    denied_permission_keys`，所以「被 deny 但无静态授予」的键也进结果集。**纪律保留**：T4 的
    测试必须用「deny 一个没有任何静态授予的键」构造，用「角色授予 + deny」会假绿。
16. **限额已改为显式存储（D27）**：哨兵是 JSON `null`（`*int` + nil），**不是 0**——0 在限额里
    有语义（免冷却）。编码/解码两侧都要有 null 往返测试，否则后台表单会把 null 丢成 0，那是
    一次静默策略变更。
17. ~~资料页等级卡的数据源恒 `null`（M5 待办）~~ **已收口**：2026-11-11 09:21 实测
    `useTrustLevel.ts` 的 `loadViewer()` 真请求 `/trust-levels`，`SFProfileShowPage.vue:110`
    已传 `profile.value?.trustLevel`。**编号保留**，避免下文与计划 §5-T20 的「残留 17」引用断链。
18. **`GrantSource` / `PermissionOrigin` 是改名后的名字**（D30）：旧名 `CapabilitySource` 与
    `hostapi.CapabilitySource`（管插件能力集合与 secret 写门禁）撞名，编译能拦但 review 会
    持续误判。看到旧名先确认是不是在说 hostapi 那个。

19. 资料页等级卡的档位名走 `Providers/profile.go` 的 `trustTierCatalog`：options 侧命中进程内 30s TTL 缓存（`Options/service.go:657-698`），但 `DecodeConfig` **没有记忆化**，每次调用整份解码一次。实测（`decode_bench_test.go` / `trust_tier_catalog_bench_test.go`，`-benchmem`）：资料页实际路径 ≈73–81µs / 42.8KB / 126 allocs；其中约 15µs 是既有缓存命中模式，45–60µs 是本次新增的整份解码。开关关闭或等级未解析时一次都不调用。量级可接受、暂不优化；要压的最小改动是按 raw JSON 字符串做一层 30s memo。
    - 基准源码：`apps/api/app/Models/TrustLevels/decode_bench_test.go`、`apps/api/app/Providers/trust_tier_catalog_bench_test.go`；跑法 `cd apps/api && go test ./app/Models/TrustLevels/ ./app/Providers/ -run '^$' -bench 'BenchmarkDecodeConfig|BenchmarkTrustTierCatalog' -benchmem`。
    - 基准内断言了 `store.List` 调用数在循环中不变，证明量的是缓存命中态，不是每请求打库。

## 本刀（2026-11-14 条件化门槛）的验收矩阵

**权威清单在 `../reports/2026-11-14-m5-acceptance-matrix.md`**：计划 §4.6 要求的六条变异
（数值预筛失效 / 规则错误被吞成成立 / 公开面泄露 AST / 未声明 factKey 返回 0 /
`incomplete` 仍发事件 / 插件键绕过白名单）**全部由 pm 在本机重打一遍**并逐条指到测试函数，
每条都贴了实际红出来的断言原文。**六条对应的测试名**（复跑命令见该报告 §4）：

| # | 变异 | 该红的测试（能红） | 需要库？ |
| --- | --- | --- | --- |
| 1 | 数值预筛失效（删 `satisfies` 短路） | `app/Models/TrustLevels/level_rules_test.go` `TestNumericFailureSkipsRuleEvaluation` | 否 |
| 2 | 规则错误被吞成成立（`Matched \|\| Incomplete`） | 同文件 `TestRuleFailureIsFailClosed` | 否 |
| 3 | 公开面泄露 AST（`publicLevelDefinition` 加 `Rule`） | `app/Http/Controllers/TrustLevels/rule_controller_test.go` `TestPublicPayloadNeverCarriesRule` | 否 |
| 4 | 未声明 factKey 返回 0（删「未声明 → error」） | `app/Models/Conditions/conditions_object_identity_integration_test.go` `TestFactCountUndeclaredKeyIsUnknown` | **是**（`test-api-isolated.sh`） |
| 5 | `incomplete` 仍发事件（删 incomplete 闸的 `return`） | `app/Models/TrustLevels/recompute_postgres_integration_test.go` `TestRecomputeIncompleteNeitherEmitsNorOverwritesLedger` | **是** |
| 6 | 插件键绕过白名单（`FilterGranted` 去掉 `Grantable`） | `app/Models/TrustLevels/earnable_registry_test.go` `TestGrantsDropsInactiveAndUnknown` | 否 |

本文件不复述断言原文，只补该报告之后新增的两条：

| # | 变异怎么打 | 该红的测试 | 状态 |
| --- | --- | --- | --- |
| 7 | `public_user_trust_level.get` 在 `incomplete` / `disabled` 时把 `level` / `tier_id` 写成 `0`（而不是 `null`） | `app/Support/HostAPI/trust_level_queries_postgres_integration_test.go:211` `TestUserTrustLevelSourceKeepsIncompleteAndDisabledNullable` | 已证（该测试同时守着「现算而非读账本」：`:133` `TestUserTrustLevelSourceComputesInsteadOfReadingLedger`） |
| 8 | 让「徽章评估入队失败」连带吞掉信任等级重算入队（两个入队互相依赖） | `app/bootstrap/user_evaluations_enqueuer_test.go:64` `TestEnqueueUserEvaluationsKeepsJobsIndependent` | 已证（`:44` `TestEnqueueUserEvaluationsEnqueuesBothJobs` 管「两个都发」，`:64` 管「一个失败不吞另一个」） |

## 回滚口径（**换二进制前必读**）

`trust.levels` 是**单键 JSON**，解码走 `DisallowUnknownFields`：配置里出现当前二进制不认识的
字段，**整份**配置被拒绝（不是跳过那一档，`app/Models/TrustLevels/config.go:376-390`）。
解码失败的后果见 §「配置校验错误的文案边界」旁注与验收矩阵第 3 条：整站按未解析处理
（没有任何已挣得权限，权限面等同 TL0）、资料页不渲染等级卡、`identity` 侧每次求值一条 warn。

因此**从带条件的新版本回滚到不认识 `rule` 的旧版本**，顺序是：

1. 用**新版本**把所有档位的 `rule` 清空并保存（`PUT /admin/trust-levels`，逐档 `rule` 置空）；
2. 确认读回配置里 `rule` 全部消失；
3. 再换回旧二进制。

`color` 与 `deletedTierIds` 对更早的版本同理（`app/Models/TrustLevels/config.go:23-30` 记着
同一口径：只有运营**真正用过**这些字段时旧二进制才整份拒绝，没用过则逐字节兼容）。

## 本刀（2026-11-14 条件化门槛）的 9 条已知残留

每条按**三件套**写，验收时一眼能判断过没过：**现状**（可复核的事实 + 命令）、
**为什么留着**（取舍或依赖）、**谁负责/什么条件下消**（人或任务 + 消号条件）。
「已收口」的条目也照样写残留风险，不许只写「已解决」。

1. **`bootstrap/worker.go:748` 那处调用没有自动化断言。**
   **现状**：`registerTrustLevelWorkers` 内部（`bootstrap/trust_levels_worker.go:116-127`）有测试
   （重复注册撞车法），但把 `:748` 这一行删掉 CI 仍绿（实测 `grep -n` 命中 748）。
   **为什么留着**：要覆盖得跑整个 `newWorkerWithPool`（需 pool / Redis / cfg 全套 harness），
   为一行调用引入整套装配 harness 不划算。
   **谁负责/何时消**：装配层回归由 review 兜底；消号条件 = 有人搭出 worker harness，或给 `:748`
   补一条「删掉就红」的断言。
2. **`fakeTrustLevelSource` 强化后，「执行器对不一致行的处理」不再可测。**
   **现状**：夹具在 `app/Support/HostAPI/trust_level_queries_test.go:20-31`，它只记录被问到的
   `user_id`，不再能构造「源返回不一致行」的现场。
   **为什么留着**：那种用例本该测生产源（真库）；用假源测它只会得到一条自证断言。
   **谁负责/何时消**：消号条件 = 真库集成测试覆盖「不一致行」分支，归属下一个碰
   `trust_level_queries_*_test.go` 的单。
3. **夹具正样本的运行期取证是一次性的。**
   **现状**：包级可重放路径成立且常跑——`extensions/fixtures/plugins/sforum-badge-facts/`
   （`README.md` 给了 `scripts/ci/reference-packages-gate.sh` 与 `extension validate` 两条命令）；
   但「装进 dev 库 → 在真实弹窗里截正样本」这段**没有脚本化重放路径**，dev 库被清后原有证据
   失效（`404 extension.not_found`）。
   **为什么留着**：可重放路径当时由并行单补，收口时点仍未落地，所以按事实写「运行期取证不可重放，
   只有包级门禁可重放」。
   **谁负责/何时消**：**task-4** 在办；落地后本行改成「一条命令可重放」并贴命令。
4. **CI 与本地 bun 版本不同。**
   **现状**：CI 钉 `1.3.14`（`.github/workflows/ci.yml:80/112`）、本地 `1.4.2`，两边从未跑过同一份用例。
   **为什么留着**：收口以 **CI 结论**为准，本地绿只作参考；`scripts/m5-gates.sh:101-107` 已把两边
   版本号写进每份日志首行。
   **谁负责/何时消**：无单独归属；出现「本地绿 / CI 红」时先看版本差异，CI 镜像与本地对齐时自然消。
5. **`identity.profile` 条件 —— 已实施（`9169ba30d`，task-2）。**
   **现状**：共享条件目录 **14** 条、信任等级子集 **12** 条
   （`app/Models/TrustLevels/config_rule_test.go:250-262` 的 `TestRuleConditionSubset` 同时断言
   `len(shared)-2` 相对式与 `== 12` 快照；`app/Models/Conditions/registry.go:330` 是
   `KeyIdentityProfile` 的注册项）。4 处硬编码计数已同批改（`requirement_text_test.go` 的
   13→14 与第 15 个样本、`conditions_postgres_integration_test.go` 13→14、
   `rule_catalog_test.go` 6→7、`config_rule_test.go` 11→12），七处相对式断言原样未动。
   判据要点：「资料行不存在」判 **false**（`user_profiles` 按需 upsert，无行是常态），
   未知 field 才判 **unknown**。**落点（注册项 / 解析器 / 批处理分派 / 文案 / 测试）与
   「无行 → false」的判据依据见 `../reports/2026-11-14-identity-profile-condition-facts.md`**
   （2026-11-14 只读核实，task-2 就是照它实施的）。
   **实现后又补的第 5 处枚举（`b7acf2268` + `fdd881b9e`，task-11）**：这个条件的取值原本散在
   注册项 `Options`、解析器白名单、`profileFilledSQL` 的**手写 CASE**、文案表与测试五处；手写 CASE
   没跟枚举绑定，按 `registry.go:80-82` 记的审查 §5.2 实证——**加第 5 个取值时整包测试全绿、运行期
   静默恒 `false`**（在 `not` 子树下还会反转成 `true`）。修法是把枚举收成**一处源表** `profileFields`
   （取值 + 列名同一张表，`registry.go:83-91`），三处由它派生：`profileFieldOptions`
   （`registry.go:94`，注册项 `Options` + 下拉顺序）、`isProfileField`（`resolve_identity.go:315`，
   解析器白名单）、`buildProfileFilledSQL`（`resolve_identity.go:251`，CASE 分支与列名）。
   **今日形态 = 一处源表 + 文案表 + 测试独立列映射**：文案表 `profileFieldPhrases`
   （`phrases.go:244`，覆盖率由测试断）；测试侧 `profileColumns`
   （`conditions_object_identity_integration_test.go:130-135`）**故意不派生**，好让源表把
   `website` 的列写成 `website_url` 以外的东西时当场红。**加一个取值**：源表加一行（SQL 分支、
   白名单、下拉自动跟随），文案表补一条（否则覆盖率断言红）。
   **为什么留着**：不再留着——本条已消，保留在此只为记「13→14 / 11→12 的迁移已完成」。
   **谁负责/何时消**：已消；**遗留边界**（不是缺口）：它**不参与影响面估算**，
   见 §Extension Surface 第 8 条。
6. **7 家族手写清单 —— 已收口（`0b38458bd`）+ 主题侧缺口已修（`bcbe314ff`）；只剩「第二入口」待审（task-3）。**
   **现状**：`v3PluginOnlyDeclarationFamilies`（`app/Support/ExtensionManifest/v3_validate_platform.go:95-106`）
   是「只允许插件声明」家族的**运行期唯一清单**（家族名 + `Declared` 谓词），
   `validatePluginOnlyFamilies()`（`:112-124`）在 `validatePlatform` 入口集中判一次 kind；
   测试改成**双向比对**（清单 ⇄ schema 的 `if/then.type.const=plugin` 分支，两侧任一多出即红并指名，
   `v3_schema_kind_families_test.go:190-200`），并用 reflect 断言每个家族名都是 Manifest 的真
   JSON 字段（否则 `Declared` 谓词是死代码）。原缺口「新增家族忘了写分支」已无静默窗口：
   忘改 schema 由测试指名，忘改清单则该家族根本不受 kind 约束。
   **为什么留着（残留风险，不是清零）**：各家族自己的 validator 里**仍各自保留 kind 判断**——
   `validateAITools`（`:130`）、`validateNotificationTypes`（`:232`）、`validateSEO`（`:316`）、
   声明侧 backend 要求（`:578`）、可执行 provider（`:790`）。它们还管字段级规则，但与清单判的是
   同一个 kind，**是否构成「第二入口」**（清单删了、validator 仍拒 ⇒ 行为不变而清单失真）由小审独立裁决。
   **曾经的缺口（2026-11-14 小审指出）—— 已修（`bcbe314ff`，task-7）**：清单原先是**第二个**判据，
   `manifest.go:485` 的 `isThemeManifestSupported` 在 `validateV3Manifest` **之前**就拒主题
   （显式查 `SEO` / `Identity` / `PermissionDefinitions` / `Backend`，`:688-712`），使
   identity / seo / permissionDefinitions 3 个家族在主题侧走不到清单；`0b38458bd` 还静默删掉了
   `TestManifestV3SchemaRequiresPluginForNotificationTypes`。修复后 `validatePluginOnlyFamilies(manifest)`
   在 `validateManifest` 里、主题闸**之前**判一次（7 家族全部由清单先拒、报错带家族名），
   notificationTypes 的主题侧测试已恢复（`:376`），并新增 `:323` 的行为判据跑满 7 家族。
   **仍开的部分**：各家族 validator 里残留的 kind 判断是否构成「第二入口」，**审查 task-3** 出结论；
   在它出来前本条不写「已彻底解决」。
7. **`gofmt` 既有漂移：本刀点名 4 个，今天全仓实测 26 个。**
   **现状**：点名的 4 个仍在列——`bootstrap/badges_backfill_notification_test.go`、
   `app/Models/Forum/service_trust_limits.go`、`app/Models/Forum/service_trust_limits_test.go`、
   `app/Support/Jobs/plugin_schedule_periodic.go`；`cd apps/api && gofmt -l . | wc -l` → **26**
   （本机 `go1.27.1` 口径，逐行清单见 §核验命令）。
   **为什么留着**：这 26 个都在更早/别人的单里，漂移是**工具链口径**问题而不是本刀引入；
   顺手全仓格式化会把无关 diff 卷进本单，破坏「一单一事」，也让小审没法按路径审。
   **谁负责/何时消**：谁碰该文件谁格式化；本单只登记数字，不认领。
8. **`batches==2` 在树里，但它钉的是设置写钩子的批量转发，不是事实写口 —— 原句描述不准，已改写。**
   **现状**：断言真实存在——`bootstrap/trust_levels_policy_watch_test.go:94-95`
   （`if store.batches != 2 { … 内层 UpsertMany 被调用 %d 次，期望 2（转发丢了会静默退化） }`，
   计数器在 `:14` / `:39`），被测对象是 `trustPolicyWriteHook.UpsertMany`
   （`bootstrap/trust_levels_policy_watch.go:60-66` 整片转发 `batch.UpsertMany(ctx, inputs)`）。
   它比的是**调用次数**：两次外层 `UpsertMany`（一次带 2 键、一次带 1 键）→ 内层恰好 2 次，
   所以「转发丢成逐条 `Upsert`」会红，这一点是**已覆盖**的，原句说「未覆盖」不成立。
   真正没覆盖的是**批量内部的键保真**：该测试只看 `enqueued` 与 `store.batches`，从不检查
   `store.upserts` 的条数/顺序，所以「N 键进 → N 行出、顺序不变」没有断言。
   另：事实写口与这条无关——`ProtocolV2BadgeFactRecorder.RecordFact`
   （`app/Support/HostAPI/v2_command_badges.go:56`）是**一次一条**，没有批量 API。
   **为什么留着**：批量内部保真属结构性覆盖，补它要一条比 `name` 更细的假源（记条数 + 顺序），
   而 `Options.Service` 的批量语义已有 `Options` 侧测试兜底，重复收益低。
   **谁负责/何时消**：归属下一个碰 `trustPolicyWriteHook` 或 `Options` 批量路径的单；
   若 `Options.Service` 换批量实现，这条断言必须跟着重写（它现在的绿依赖「一次调用一次转发」）。
9. **验收脚本 HTTP 段依赖 QA 账号登录。**
   **现状**：`scripts/verify-trust-levels-acceptance.sh` 的登录链是 `戊 → 丁b → TLRW/TLRO`
   （`:452-473`），脚本自己记着「`creds.txt` 的密码会随环境轮换而滞后（2026-11-11 实测 戊/丁b 401）」；
   本刀收口当天 `戊 账号登录失败` 使 `H3 / I-09 / I-11 / I-13 / I-24` 五项 `pend`（其余 HTTP 项正常）。
   **为什么留着**：凭据是运营侧资产，轮换节奏不受本单控制；脚本已经把退路写成代码而不是注释。
   **谁负责/何时消**：运营侧提供可用账号后重跑这五项（**task-4** 的 seed 路径同时会缓解夹具侧取证）；
   `pend` 不能当绿——证据里必须点名「因凭据 pend」。

### 核验命令（上面 9 条与 §Extension Surface 的事实来源）

| 事实 | 命令 | 输出（2026-11-14 本机） |
| --- | --- | --- |
| recompute 唯一注册面调用点 | `cd apps/api && grep -n 'registerTrustLevelWorkers(registry, deps.TrustLevels' bootstrap/worker.go` | `748:`（任务描述里写的 744 是笔误） |
| 注册面内部两个 job | `grep -n 'RegisterRecompute' bootstrap/trust_levels_worker.go` | `126:` user / `127:` batch |
| fake 源现状 | `grep -n 'fakeTrustLevelSource' app/Support/HostAPI/trust_level_queries_test.go` | `20:` 定义、`:31` `UserTrustLevel` |
| 夹具包存在 | `ls extensions/fixtures/plugins/sforum-badge-facts/` | `README.md backend schemas sforum.extension.json` |
| CI bun 钉值 vs 本地 | `grep -n 'bun-version' .github/workflows/ci.yml` + `bun --version` | `80/112: 1.3.14` + `1.4.2` |
| 共享目录 14 / 子集 12（`9169ba30d` 起） | `sed -n '250,262p' apps/api/app/Models/TrustLevels/config_rule_test.go` | `共享目录 14 条（含 identity.profile）` / `子集是 **12**` |
| 7 家族唯一清单 | `grep -rn 'v3PluginOnlyDeclarationFamilies' apps/api/app/Support/ExtensionManifest/` | 生产侧 `v3_validate_platform.go:95` + 测试 `:190` |
| schema 的 7 条 plugin-only 分支 | `node -e '<枚举 allOf 里 then.type.const=plugin>' apps/api/app/Support/ExtensionManifest/schemas/manifest-v3.schema.json` | `branches: 7`（seo/identity/badgeConditions/userFactConditions/aiTools/notificationTypes/permissionDefinitions） |
| 残留 kind 判据 | `grep -n 'TypePlugin' apps/api/app/Support/ExtensionManifest/v3_validate_platform.go` | `113/130/232/316/578/790` |
| 主题闸先于清单（3/7 家族） | `grep -n 'isThemeManifestSupported' -A 25 apps/api/app/Support/ExtensionManifest/manifest.go` | `:485` 调用、`:688-712` 定义；显式查 `SEO`/`Identity`/`PermissionDefinitions`/`Backend` |
| notificationTypes 主题侧测试已删 | `grep -rn 'TestManifestV3SchemaRequiresPluginForNotificationTypes' apps/api/app/Support/ExtensionManifest/` | 零命中；现存 `v3_schema_kind_families_test.go:101` 只跑 seo / aiTools |
| identity.profile 不参与估算 | `grep -n 'KeyIdentityProfile' apps/api/app/Models/Badges/rule_impact.go` | 零命中 → 落 `:204-205` 的 `default`（`return 0, false, nil`） |
| gofmt 漂移面 | `cd apps/api && gofmt -l . \| wc -l` | `26` |
| `batches==2` 的真实位置 | `cd apps/api && rg -n 'batches' --glob '*.go' .` | `bootstrap/trust_levels_policy_watch_test.go:14/39/94-95`（另有 `Search/reindex_test.go:165` 与 `cmd/sforum/revisions.go:33`，与本域无关） |
| QA 凭据退路 | `grep -nE 'login_any_admin_reader\|401' scripts/verify-trust-levels-acceptance.sh` | `:452`、`:461` |
| 后台 i18n 叶键数 | `cd apps/web && node -e '<枚举 leaves>'`（全文见 §命名与 i18n 三条硬约定） | `admin.forum.trustLevels = 394` / `admin.settings.trustLevels = undefined` |
| 死键棘轮 | `cd apps/web && bun test tests/admin/forum/trustLevelI18nDeadKeys.test.ts` | `4 pass / 0 fail` |
| 生成物一致性 | `node scripts/v3-catalog/generate.mjs --check` | `V3 P0 catalogs are current (18 files).` |

## 本刀还剩什么没做

- 计划 §6「明确不做」的 **7 条**（审核信号条件 / 内容质量条件 / 阅读行为 / RPC 条件求值器 /
  等级反规范化列 / 公开的逐条清单 / 规则改严后的追溯收回）——是边界裁决，不是待办。
- `identity.profile` 条件：**已实施（`9169ba30d`，task-2）**，共享目录 **14** / 信任子集 **12**，
  见残留 5。它的「不参与影响面估算」是边界裁决，见 §Extension Surface 第 8 条。
- 夹具正样本的运行期重放路径：**本轮在办（task-4）**，见残留 3。
- 7 家族清单：清单本体已落（`0b38458bd`），主题侧「清单不是首个判据」与 notificationTypes 测试被删
  这两处缺口**已修（`bcbe314ff`，task-7）**——清单提到 `validateManifest` 里、主题闸之前，
  7 家族全部由清单先拒，主题侧测试恢复并新增跑满 7 家族的行为判据。剩「各家族 validator 里的 kind
  判断是否第二入口」由 **task-3** 出结论，见残留 6。
- `config.go:845` 那条注释仍写「全站等级掉回默认策略」；准确口径见 §回滚口径
  （未解析 / 等同 TL0，不涉及 `DefaultPolicy()` 替换）。**归属**：下一次碰该文件的单顺手改。

## 已作废结论（读旧版规范或旧讨论前先看这里）

这刀计划改了 32 条决策、视觉规范冻了 4 次。**下一个会话最需要的不是终稿，而是「哪些说法
已经作废」**——否则会照着旧版规范重新实现一遍已被推翻的东西。

终稿规范：`../reports/2026-11-10-trust-level-visual-spec.md`（第四次冻结：39 键 / 6 指标 /
8 处登记点）。本节的底账来自 `../reports/2026-11-10-trust-level-superseded-claims.md`
（designer 整理，20 条）。**清单与终稿规范冲突时以规范为准。**

> **2026-11-11 起，规范本身也有一条不再成立**：§0 第 23 行「阈值页是既有 tab 数组里的
> 一项，不是新页面」已被 `../decisions/2026-11-11-trust-levels-standalone-admin-page.md`
> 取代（本文件 D 组 #18 同步标注）。规范的其余部分（DOM 契约、几何、断点）继续有效；
> 独立页的页头几何沿用同一份基线，不需要第二套规范。

### A. 设计侧自纠（不要照旧版规范实现）

1. 「`runRestore` 会弹既有确认框」—— 错：它直接 `apply()` + success Toast，没有确认步骤。
   所以 `UAlert` 里那句「恢复默认不会影响用户已获得的等级」是 D12 的**唯一**披露点，
   禁止改写成 Toast。证据：`composables/settings/useSettingsSection.ts:60-68`。
2. `__maxed` 的 `margin:0` —— 错：与档位名零间距会被读成同一段文本。改 `margin:10px 0 0`
   （与进度块同高，替换时不跳动）。证据：`components/profile/SFTrustLevelCard.vue:270-272`。
3. 「窄屏 2 列 + 末项 `span 2`」—— 在 6 个指标下错：6 是偶数，硬跨 2 会造出半格空洞。
   规则改为**指标数为奇数时才启用 `span 2`**。
4. 「settings 子 tab 不进 V3 目录」—— 错：新增**组件**必须有 reviewed identity，否则
   `scripts/v3-catalog/generate.mjs` 直接抛错；登记点由 6 处改为 **8 处**（+2 条 catalog
   identity）。保留原结论的另一半：`SFAdminFixedTabNav` 本身不需要注册。
5. 「一指标一键 `trust.level.{level}.{metric}`」—— 错：实际是单个 JSON 选项键 `trust.levels`，
   值为 `{ schemaVersion, levels: [{ levelSlug, requirements, limits, earnable }] }`。
   证据：`TrustLevels/config.go`、`composables/admin/settings/useTrustLevelsThresholds.ts:12`。
6. 指标表里的 `account_age_days` —— **该字段不存在**；`Requirements` 只有 6 个指标，账龄只
   保留 `trust.new_user_days` 一个上限闸。证据：`TrustLevels/types.go` 的 `Requirements`。
7. 键数 36 → 41 → 39 —— 中间值作废。终稿 39 = 原 36 + 新增 5（`progress.text`、
   `likeGiven`×2、`bookmarkCount`×2）− 删除 2（`metric.accountAge`×2）。
8. 断点算式按 5 项、再按 7 项算过两版 —— 均作废。按 6 项重算：`6` / `3+3` / `2+2+2`。
9. TL1 显示名两度变更：「成员」→「正式成员」→ **「正式用户」**（终稿整组带「用户」）；
   slug 始终是 `member`，未动。

### B. 指令与实现被实测纠正

10. 「指标 7 个」—— 错，实际 **6 个**（多算了不存在的 `account_age_days`）。
11. 「i18n 审查断言看 `roles.*`」—— 该命名空间**不存在**。实际是 `admin.roleCatalog.*`，
    且产品文案把「角色」叫**用户组**（`admin.nav.roles` =「用户组」）。已进计划 D21。
12. 后台桌面网格「5 列 → 7 列」—— 两版都作废，终稿 **6 列**。核查时实现仍是 `repeat(5, …)`，
    与规范、与真实契约（6 个指标）三边不一致；唯一会说话的是 `DisallowUnknownFields`
    （保存被拒）。已转 frontend 改 6 列。**2026-11-10 复核：现为 `repeat(6, …)`，已对齐。**
13. `adminSurfaceCatalog.test.ts` 计数 48→49 曾疑似「新功能多算一条」—— 不是。那 +1 是徽章页；
    信任等级 tab 是**设置页内组件**不是 page，正确做法就是不进 page 级 placements，只进
    `ui-surfaces`。证据：`node scripts/v3-catalog/generate.mjs --check`。

### C. Go 侧注释/契约漂移 —— **backend 已修完，本条可删**

14. ~~后端把 TL2 的 slug 写成另一个词~~ —— 已改为 `resident`（复核方式已变：`types.go` 的 `nameSlugs` 在 N 档改造中删除，改看 `DefaultPolicy()` 的档名与 `tier_icons.go` 的默认图标序列），
    `config.go` 注释同步。旧词已不在仓库里，按 `resident` 检索即可。
15. ~~`types.go` 注释称 options 键后缀为 `trust.level.<n>.<metric>`~~ —— 已改：注释明写该方案
    作废；`ThresholdOptionKey` 只用于**表单字段名与契约字段名**，不是 options 键。
16. ~~`types.go` 的 `NameKey()` 是无人消费的死生成器~~ —— 已删，**三个死生成器全部清掉**：
    `NameKey()` / `DescriptionKey()` / `ThresholdOptionKey()`。
    `ThresholdOptionKey` 的准确说法（提交级证据，`f63ad2cf9` 之后可断言）：
    **它从未进入任何提交的 Go 代码，仅存在于本刀的工作树（`policy.go:65`）并被删除；当前
    仓库中该字符串只出现在 `knowledge/` 的记录里。**
    验证命令：`git grep -c "ThresholdOptionKey" HEAD -- '*.go'` → 零命中；
    `git log -S"ThresholdOptionKey"` → 只有 `f63ad2cf9` 这一次提交，且命中文件全在 `knowledge/`。
    **纪律：自证式断言不算断言**（计划 §14 第 4 条）——断言自己的前缀证明不了任何事；
    判据是**有生成器 ≠ 有契约，看消费者不看函数**。

### D. 明确不做（是边界裁决，**不是待修项，别当 TODO 捡起来**）

17. 等级卡放进徽章 tab —— 否。等级是身份元数据，徽章只展示不给能力，混放会误导；
    落右栏（每个 tab 可见）。
18. ~~后台新开独立页 —— 否。需要第二套页头/工具栏，还改 V3 目录计数，收益为零；它是设置页内
    的固定 tab。~~ **已被 `../decisions/2026-11-11-trust-levels-standalone-admin-page.md`
    取代（2026-11-11，D-A）**：信任等级现在是独立页 `/control-panel/forum/trust-levels`，
    页内五 tab（overview / tiers / thresholds / limits / earnable；`limits` 为 2026-11-11 追加）。旧结论的成本（页头、目录计数）是真实的，
    变的是收益判断——前提「只有阈值表单一个可编辑面」在 D33 之后不再成立。同一条被取代的
    还有 `../reports/2026-11-10-trust-level-visual-spec.md` §0 第 23 行的「阈值页是既有 tab
    数组里的一项」。
19. 后台画能力解锁勾选矩阵 —— 否。M4 只冻结阈值读写；能力映射未定稿前画矩阵就是第二套真相。
20. 在「`L<4` 且下一档全阈值 0」时写「已达最高等级」或「继续活跃即可升级」—— 否。前者是错的，
    后者是编的；等 M4 暴露「被上限闸压住」的信号再写 `nextHint`。

## 验收与证据（M8）

**原始证据在 `storage/qa/trust-levels/`**，该目录被 `.gitignore:118`（`storage/`）忽略，
**提交后不存在**——这是刻意的：提交的是**可复现的仪器**，不是几十兆日志。重跑即得，命令见下。

**最终数字有两个口径，两个都要知道**（否则下一个人会以为其中一份错了）：

| 来源 | 数字 | 口径 |
| --- | --- | --- |
| `../reports/2026-11-10-trust-levels-m8-final-status.md`（reviewer） | **124 行：ok 75 / 未实现 49 / FAIL 0** | 合并口径：reviewer 独立证据优先（浏览器实测 / 变异红绿 / 代码级 / 静态链路），其次运维 `_summary.txt`，两边都无则「未实现」+ 理由 |
| 运维 `_summary.txt` | ok 67 / 未实现 57 | 纯自动化基线：只算脚本实际跑到的矩阵 ID |

**重跑命令**（按序执行；证据落回 `storage/qa/trust-levels/`）：

```bash
# 1) 全量测试日志（A-3 的输入）
#    **-v 必须有**：不带它 go test 不输出 === RUN，A-3 的硬断言会判「测试没跑」并 FAIL。
./scripts/test-api-isolated.sh -v ./... > /tmp/iso.log 2>&1

# 2) 验收 + 摘要（生成 _summary.txt 与 _unimplemented.tsv）
SFORUM_TL_TEST_LOG=/tmp/iso.log ./scripts/verify-trust-levels-acceptance.sh

# 3) F-2 夹具与开关证据
./storage/qa/fixture-trust-earned.sh --apply --flag-off-evidence

# 4) D-8 阈值下探/恢复证据
./storage/qa/seed-trust-levels-matrix.sh --thresholds-only --lower-thresholds

# 5) G 组性能（D31：同一二进制翻开关做 A/B，不用 worktree、不加临时开关）
./storage/qa/trust-levels-perf.sh --capture-baseline && ./storage/qa/trust-levels-perf.sh --compare

# 6) 变异验证（6 条必须全 RED，改回再全绿）
./scripts/verify-trust-levels-acceptance.sh --mutations
```

> 第 1 步的 `-v` 不是排版偏好，而是 A-3 判据的**输入前提**：那条硬断言守的不是「断言是否
> 正确」，是「**断言是否真的执行过**」。本刀它当场抓到过一次——`-v` 被当成包选择传入，
> `=== RUN` 计数 0，看起来全绿实则一条没跑。

**证据升级（首次提交后必须做，`knowledge/README.md` 有同款约定）**：提交前的存在性结论是
**观察级**（`grep` 只证明「此刻没有」）；首次提交后必须回填**提交级**
（`git log -S` + `git grep … HEAD`），因为只有版本历史能区分三种情况：
**从未存在** / **曾经存在且已删** / **只存在于未提交的工作树**。
本刀的 `ThresholdOptionKey` 正是第三种——提交前说「零调用者、已删」是观察级，说「从未存在」
是错的，提交后才能给出唯一正确的说法（见「已作废结论」C 组 16）。

**方法学收获（比上面任何一条都耐用）**：验收矩阵与验收脚本之间也需要一条契约。本刀出现过
「矩阵 30 个测试名里 29 个不存在」，而 `go test -run` 匹配不到时**退出码是 0**，脚本会把它们
全记成 PASS。判据：**矩阵是唯一编号源，脚本按矩阵 ID 落盘，矩阵有而脚本没有的显式标「未实现」**
——省略会让人以为覆盖了。与「自证式断言不算断言」是同一条链：**没有执行证据的绿，不是绿。**

## D34 增量（2026-11-11，基线收窄 Z3-b″）

裁决：`../decisions/2026-11-11-trust-level-baseline-narrowing.md`；计划
`../plans/2026-11-11-trust-levels-baseline-narrowing.md`（§1 死锁、§4-bis 裁决）；
验收 `../reports/2026-11-11-trust-levels-baseline-narrowing-matrix.md`（N 组 16 条）。
代码提交 `185be5701`，文档提交 `69f455a42`。

**机制**（三句话）：

1. `TrustLevels/baseline_grants.go:23` 的 `BaselineGrants()` 是**代码常量**（两个键：
   `topic.create` / `attachment.upload`），**不进** `DefaultPolicy().Earnable`，后台不可配。
2. 装载期 `Identity/trust_level_grants.go` 的 `applyTrustGrants`（`:35`）→ `deductBaselineGrants`
   （`:58`）在等级已解析且低于 TL1 时从 `Permissions` 扣除；`mergeEarnedKeys`（`:78`）仍在同处合并 earned。
   **合并点只有一个**：`LoadActor` 与 `LoadEffectiveActorTx` 都走它。
3. **四条不扣**：开关关闭 / 等级未解析 / bot（`Kind.IsBot()`）/ 该键在
   `user_permission_overrides` 里是 `effect='allow'`（运营手动放开不被静默推翻）。
   第四条在事务装载里另有一份 `allowed` 集合（同批 override 行，不额外查库）。

**读面字段**：`GET /admin/trust-levels` 新增 `baselinePermissions`（`Http/Controllers/TrustLevels/dto.go:75`，
字段 `Baseline`）。它与 `eligiblePermissions` 同级、语义相反：后者是「允许被等级授予的白名单」（仍 7 键），
前者是「TL0 会被扣掉的基线键」。字面集合由 `TestAdminBaselinePermissionsLiteral` 钉住。
前端据此把这两列渲染成**静态门控标记**（无勾选框，不可取消），其余 5 键才是可勾选。

**双 loader 一致性修复**：`Identity/actor_postgres_tx.go` 的 `LoadEffectiveActorTx` 原先只
`SELECT status, kind`，`actor.CreatedAt` 全程未赋值 → 账龄被当成无限长 → 插件命令路径**永远不低于 TL1**。
本刀补读 `created_at` 并 `Scan(&actor.CreatedAt)`。修复前同一 TL0 用户在 HTTP 路径 5 键、事务路径 7 键。

**已知缺口（照实登记，别当成已修）**：

1. **后台用户详情仍把被扣的键算作生效**：详情页展示角色与用户级覆盖，不反映装载期减法，
   所以 TL0 用户详情仍显示「能发主题」而实际请求被拒。以实际请求为准。
2. **L5 / L8 两条路径不合并 earned**（`postgresTxStore.LoadCurrentUserAccess` 注释自认；CLI 无
   `WithTrustLevelSource`），本刀下表现为 fail-open（TL0 多键），不是挡人。清单见
   `storage/qa/trust-levels-z3/inventory-loaders.md`。
3. **`trust.new_user.forbid_attachments` 仍是死配置**（D20 未修）：减法门实现了它想表达的**行为**，
   但那条 option 本身依旧无消费者。别把本刀记成 D20 的修复。
4. **父→子继承缺口（residual 3）不变**：事务装载不做父→子展开，本刀只补了 `CreatedAt`。

## Important Paths

- 计划：`knowledge/plans/2026-11-10-trust-levels.md`
- 本刀：`knowledge/plans/2026-11-11-trust-levels-baseline-narrowing.md`、
  `knowledge/decisions/2026-11-11-trust-level-baseline-narrowing.md`
- 基线门：`apps/api/app/Models/TrustLevels/baseline_grants.go`、
  `apps/api/app/Models/Identity/trust_level_grants.go`
- 统计地基（已 landed）：`apps/api/app/Models/Badges/`、`../modules/badges.md`
- 裁决点：`apps/api/app/Models/Identity/policy.go`（`Can`）、
  `permission_compat.go`（父→子继承）
- 装载：`apps/api/app/Models/Identity/postgres_store.go`（`LoadActor`）、
  `postgres_store_roles.go`（`loadCurrentUserAccess`）、
  `database/queries/identity.sql`（`ListUserPermissions`，deny 的真身）
- 类型：`apps/api/app/Models/Identity/types.go`（`Actor`，M2 在此加字段）
- 待收编：`apps/api/app/Models/Forum/service_ops.go`（`trustForActor`）
- 配置：`apps/api/app/Models/Options/types.go`（`trust.new_user*` 七键）、
  `app/Models/Options/service_normalize.go`
- 后台 UI：`apps/web/app/pages/admin/forum/trust-levels.vue`（独立页 shell）、
  `apps/web/app/components/admin/forum/trust-levels/tabs/`（三个分区组件）、
  `apps/web/app/composables/admin/forum/trustLevels/`（表单 / 矩阵 / 页面编排）；
  新人限制（TL0 的数值来源）仍在
  `apps/web/app/components/admin/settings/site/tabs/SFAdminSiteNewcomersTab.vue`
- 权限奇偶校验：`apps/api/database/migrations/permission_seed_parity_test.go`
