# 信任等级：五档固定阶梯 → 运营自定义 N 档（1–8）

Status: approved（2026-11-11，Lead 批准；后端/前端/验收/运维并行实施中）
落盘：writer 于 2026-11-11 逐字落盘 Lead 批准原文，未改口径；本文件不取代上游三份计划（`2026-11-10-trust-levels.md` / `2026-11-11-trust-levels-closing.md` / `2026-11-11-trust-levels-baseline-narrowing.md`），只作其下游延伸。
上游：`2026-11-10-trust-levels.md`（五档模型与 D1–D32）、`2026-11-11-trust-levels-baseline-narrowing.md`（D34 基线门，语义不变）

## 1. 目标

把「TL0–TL4 五档」改成「TL0 固定 + TL1..TLn 可增删/插序/命名/配图标」的运营自定义阶梯（N ∈ 1..8），全部一次性交付：后端领域模型、`trust.levels` 配置 v2、后台四 tab 编辑器、公开契约、资料页等级卡、文档与验收。

验收标准：

1. N = 1 / 3 / 5 / 8 四组配置都能保存、判定、公开下发、卡片渲染、后台展示。
2. 在第 k 位插入一档后，原本第 k 档已配的 requirements / limits / earnable 仍挂在同一档（id 锚定不变量，有测试证据）。
3. 停用（软删除）一档后其配置原样保留，重新启用即恢复。
4. 存量 schemaVersion 1 的 `trust.levels` 值无损读入，前端/后台表现与今天一致。
5. `git diff --stat -- apps/api/database/` 为空（零数据库迁移）。
6. 生产代码 `rg -n "LevelCount" apps/` 零命中；`TRUST_LEVEL_SLUGS` / `TRUST_LEVEL_MAX` 只作为「无目录时的回退兜底」存在。
7. 浏览器证据：`/control-panel/forum/trust-levels` 桌面 + 390×844 无横向溢出、四 tab 可切换、活动面板正确。

## 2. 已核实的事实（决定成本边界）

| 事实 | 证据 |
| --- | --- |
| 等级不落库，是每请求纯派生值 | `apps/api/database/migrations/` 与 `sqlc/` 对 `trust_level` 零命中 |
| 5 是编译期形状，不是 UI 常量 | `TrustLevels/types.go`：`LevelCount = 5`、`nameSlugs [LevelCount]string`、`Policy.Levels [LevelCount]Requirements`、`Policy.Limits [LevelCount]LevelLimits` |
| 配置解码强制「恰好 TL1–TL4」且拒绝 TL0 | `config.go` `DecodeConfig` |
| 判定按 rank 从高到低找第一个满足 | `level.go` `LevelOf` |
| 上限闸锚定 Rank 1 | `level.go` 账龄闸 → `LevelMember` |
| 基线门锚定 Rank ≥ 1 | `baseline_grants.go` `level >= int(LevelMember)` |
| 「已最高档」判据是 `LevelLeader` | `Http/Controllers/TrustLevels/controller.go:108`、`resolver.go` `Progress` |
| 公开面只下发 `level` / `levelSlug`，不下发名称 | `Profile/service.go:679`、`dto.go` `levelDefinition` |
| 前端名称/图标完全来自本地 5 元素数组 | `useTrustLevel.ts` 的 `TRUST_LEVEL_SLUGS` / `TRUST_LEVEL_ICONS` / `TRUST_LEVEL_MAX`、`SFTrustLevelCard.vue` |
| 前端表单把档数写成常量，且推荐默认值在前端有第二份副本 | `useTrustLevelsForm.ts:37-40, 98-103` |
| 新增组件必须补 V3 catalog identity | `knowledge/modules/trust-levels.md`「登记点 9 处」 |
| 验收脚本有一个死变量指向不存在的文件名 | `scripts/verify-trust-levels-acceptance.sh:76` → `paths/trust-levels.yaml`（实际是 `trust_levels.yaml`，全脚本零使用） |

## 3. 必须保住的不变量

1. `Actor.Can()` 一个字节不改；已挣得仍在装载期合并（`Identity/trust_level_grants.go`）。
2. `Actor.TrustLevel int` 语义保持 = rank。Identity 与 Forum 零改动。
3. TL0 语义仍由 `trust.new_user_*` 七键 + 基线门（`topic.create` / `attachment.upload`）决定，不进 `trust.levels`。
4. `limits` 的 `null`（继承）≠ `0`（免冷却）。
5. `earnable` 只做累加，且仍在正白名单内（七键不变）。
6. 失败姿态 fail-closed：解码失败整体拒绝，不做部分修正。
7. 不新增权限位（不加 `trust.view`）。

## 4. 决定项

| # | 裁决 |
| --- | --- |
| D-N1 身份与序号分离 | 每档有不可变 `id`（配置与契约身份）与 `rank`（1..N，判定与展示序号）。`rank` 由 active 档位在数组中的顺序派生，不落字段。 |
| D-N2 存储形状 v2 | `{"schemaVersion":2,"tiers":[{id,name:{zh,en},icon,requirements,limits,earnable?,retired?}]}`。数组顺序即顺序；`retired:true` 为软停用，保留全部配置，判定与 UI 跳过。TL0 不在数组内。 |
| D-N3 命名池是编辑辅助，不是存储字段 | 存储只有 `name{zh,en}`；命名池只在 UI 里一键填入这两个字段。回退链：`name[locale]` → `name[另一语言]` → 内置五档 i18n 键（id 命中时）→ `TL{rank}`。 |
| D-N4 图标白名单 + 默认序列 | 16 个 lucide 图标白名单，服务端校验；未选时按 rank 取默认序列（前 5 = 现有 芽/叶/屋/山/冠）。零配置时视觉与今天一致。 |
| D-N5 兼容 | `DecodeConfig` 同时接受 v1 与 v2；v1 在内存升级为 v2（id = 原 slug、name = 内置双语名、icon = 默认序列）。`EncodeConfig` 只写 v2。`levelSlug` 字段保留，值 = tier id；内置五档 id 不变 → 存量消费方不断。 |
| D-N6 id 生成 | 新档 id = `tier_<seq>`，`seq` = `max(所有档含 retired 的数字后缀, 5) + 1`，解码期派生，不存字段、不接受客户端指定。只软删除所以 id 永不复用。 |
| D-N7 档数边界 | active 档数 1..8；至少保留 1 个 active。 |
| D-N8 减档 = 软停用 | 停用会撤回该档贡献的 `earnable`（可能降权），保存前必须二次确认 + 常驻披露，沿用现有 `UAlert` 披露点纪律（不放 Toast）。 |
| D-N9 推荐默认值唯一来源 | 后台读面的 `recommendedConfig` 为准；前端本地表降级为 `FALLBACK_TIERS`（仅读面不可用时兜底），并加一条等值测试防漂移。 |
| D-N10 页面结构 | 四 tab：`overview` / `tiers`（新）/ `thresholds` / `earnable`。默认仍 `overview`。保存入口仍只有页级 footer 一处。 |
| D-N11 分档限额维持只读 | 本刀不做 `limits` 编辑器，保存时原样带回；新档 `limits` 写 `null`。 |
| D-N12 响应式矩阵 | `earnable` 矩阵 ≤6 档保持表格；7–8 档切成「按档分组卡片」（每档一张卡、卡内 7 个勾选）。 |

> **追加口径（不属批准原文，落盘后补记）**：对拍报告
> `../reports/2026-11-11-trust-levels-n-tiers-recon.md` 逼出 **D-N13（v1 → v2 只在运营保存时写库）**
> 与三条 wire 层细化（读面继续叫 `levels[]`；`level` 与 `levelSlug` 并存；前端常量收窄到回退分支），
> 以及 **D-N14（配置校验错误 = 统一机器码 + `reasonCode` 枚举明细，`reason` 仅兜底不展示）**，
> 正文见 `../decisions/2026-11-11-trust-levels-n-tiers.md`。读本表时须连带读这三条，
> 否则会漏掉「回滚是两步」这个代价。

## 5. 后端改动（apps/api）

领域包 `app/Models/TrustLevels/`：`types.go`（删 LevelCount/nameSlugs/定长数组，新增 `Tier`、`LocalizedName`、`Policy.Tiers`、访问器 `RequirementsOf`/`LimitsFor`/`LastRank`/`ActiveTiers`）、`level.go`、`config.go`（SchemaVersion 2 + v1/v2 分派 + 校验）、`policy.go`、`registry.go`、`resolver.go`、`baseline_grants.go`（注释改 rank 表述）、新增 `tier_icons.go`。
HTTP 面 `Http/Controllers/TrustLevels/`：`payload.go`、`dto.go`（增 `displayName{zh,en}`/`icon`/`retired`/`editable`、`iconChoices`/`namePool`/`tierLimits`/`nextTierSeq`）、`controller.go:108` maxed 判据。
薄适配：`Providers/forum.go:289`、`Profile/service.go:679`。
公开契约：`contracts/openapi/schemas/trust_levels.yaml` 同步，ConfigInput 描述改 v2 并注明 v1 仍被接受。

## 6. 前端改动（apps/web）

`useTrustLevel.ts`（固定数组降级 FALLBACK_*、新增目录加载 useAsyncData 稳定 key `trust-levels-catalog`）、`useTrustLevelsForm.ts`（模型改 `tiers: TierForm[]`）、新增 `useTrustLevelsTiers.ts`（纯函数 CRUD + id 生成 + 1..8 边界 + 插入锚定）、`useTrustLevelEarnableMatrix.ts`（列数与总格数按 active 档数）、`useTrustLevelsAdminPage.ts`（接入 recommendedConfig）、新增 `SFAdminTrustLevelTiersTab.vue` 与 `SFAdminTrustLevelIconPicker.vue`、改造三个现有 tab 组件、`pages/admin/forum/trust-levels.vue` 四处登记、`SFTrustLevelCard.vue` 新增 ladder prop、两份 i18n。

登记点：page shell 四处 + 两个新组件 + 两份 locale + `docs/extensions/v3/catalog-identities.json` 两条 active identity，然后重新生成 catalog。无新 page id，第 9 点不适用。

## 7. 测试与门禁

Go：更新 config/level/registry/resolver/level_purity/controller 六处测试；新增 `legacy_config_v1_test.go` 与 `tier_identity_test.go`。
Web：更新五个现有测试文件；新增 `trustLevelTiers.test.ts` 与 `trustLevelNameFallback.test.ts`。
门禁顺序：architecture-boundaries → v3-catalog --check → validate-openapi-refs → go build + test-api-isolated → typecheck + bun test + build → verify-trust-levels-acceptance → 浏览器双宽度 QA → test.sh --fast。

## 8. 边界与失败模式

| 场景 | 行为 |
| --- | --- |
| N = 1 | 账龄闸不再产生差异；后台常驻披露该后果 |
| 停用唯一 active 档 | 拒绝（422 字段级） |
| 停用中间档 | rank 重算、判定跳过；该档 earnable 撤回 → 二次确认 + 常驻披露 |
| 插档导致等级变化 | 派生值无迁移；确认框写明「保存后所有用户按新阶梯重新判定」 |
| `name.en` 为空 | 回退 `name.zh` 并提示英文站将显示中文名 |
| 非法图标/重复 id/id 格式/档数越界 | 422，定位到具体档位与字段 |
| v1 存量配置 | 接受并升级；schemaVersion 非 1 非 2 → 整体拒绝 |
| 后台读面不可用 | 停用「恢复推荐默认」+ 常驻错误条，不回落到本地副本静默改写 |

## 9. 风险与缓解

1. 文件行数越界（矩阵组件 667 行、表单 composable 470 行）→ 先拆再改逻辑。
2. 公开面 SSR 闪名 → 目录 useAsyncData 稳定 key；失败才回退代号。
3. N 档矩阵移动端溢出 → D-N12 分组卡片，双宽度证据。
4. 前端默认值第二份副本漂移 → D-N9 等值测试。
5. 验收脚本口径滞后 → 沿用「矩阵是唯一编号源」纪律；顺手修第 76 行死变量。

## 10. 明确不做

不做 limits 编辑器；不做多模板体系；不做等级反规范化列/重算按钮/阅读类指标/提名审批；不改 `Actor.Can()`、不改 `Actor.TrustLevel` 类型与语义、不新增权限位；不动 TL0 七键与基线门语义。
