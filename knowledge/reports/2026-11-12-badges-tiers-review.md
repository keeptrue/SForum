# 徽章等级表改造 — 独立验收报告

- 日期：2026-11-12
- 验收人：小审（reviewer）
- 范围：task-1（后端等级表 / 批量授予 / 候选搜索）、task-2（后台四分区 / 等级外观 / 搜索批量授予 / 公开面统一配色）
- 结论：**通过（初验为有条件通过；P1 与写路径 QA 发现的 P2 均已修复并复验）** —— 8 条门禁全绿，写路径浏览器证据已补齐。

## 1. 门禁结果

| # | 命令 | 结果 |
| --- | --- | --- |
| 1 | `cd apps/api && go build ./...` | 通过（exit 0） |
| 2 | `./scripts/test-api-isolated.sh` | 第一次 exit 1，失败项全部是 `/tmp` tmpfs 写满（`disk quota exceeded`）：SecretStore/HostAPI/ThemeCompiler `build failed`、bootstrap 写 zip 失败、cmd/sforum link 失败、Extensions `TestSyncBuiltins*` 编译失败；135 包 ok。改 `TMPDIR=/var/tmp/gotmp GOTMPDIR=/var/tmp/gotmp` 重跑后 job 报 exit 0，SecretStore/HostAPI/RuntimeDiagnostics/SystemTier 均 ok，**尾行 EXIT 值因 shell 崩溃未读到，待复核** |
| 3 | `cd apps/web && bun test` | 通过，2379 pass / 0 fail |
| 4 | `cd apps/web && bun run typecheck` | 通过（仅 `@nuxt/robots` 的 `/api/**` 既有 WARN） |
| 5 | `node tests/validate-architecture-boundaries.mjs` | 通过（2127 production files，213 个 >500 行） |
| 6 | `ruby scripts/validate-openapi-refs.rb` | 通过（3263 refs / 65 files） |
| 7 | `node scripts/v3-catalog/generate.mjs --check` | 通过（18 files current） |
| 8 | `SFORUM_MIGRATION_TARGET_VERSION=202611120001 ./scripts/verify-migration-reversible.sh` | 通过（down：表/外键/索引删除 + 旧 CHECK 恢复 + 无越档徽章 + goose 回退 202611100004；up：表重建 / 内置 3 行 / gold position=0 / 外键 / CHECK 移除 / 索引 / 12 枚徽章 / goose 版本） |

> 环境注记：`/tmp` 是 5.9G/7.5G 的 tmpfs，被历史 QA 残留（`/tmp/qa-chrome-*`、`/tmp/go-build*`）写满。这不是代码问题，但它会让 Go 测试**看起来**大面积红。任何一次「Go 包 build failed」先看 `df -h /tmp`。

## 2. 浏览器 QA（CDP 脚本 `storage/qa/2026-11-12-badges-qa.mjs`）

登录态用 `qa_badge_ui`（密码取自 `storage/qa/creds.txt` 矩阵，未落盘）。

- 四个 tab「徽章/分组/等级/授予记录」在 1633×810 与 390×844 全部渲染，`aria-selected` 随 `?tab=` 正确切换，无 disabled。
- 每个 tab `documentElement.scrollWidth == clientWidth` → **无页面级横向溢出**。
- 小前点名「待人工确认」三处：
  1. 390 宽颜色行：`.sf-tier-color-row` scrollW=406 / clientW=318 → **行内横向滚动成立**，`rowOverflowPage=false`；9 色块 +「按位置自动」chip 保持单行。
  2. 图标 picker：**双层滚动区成立**（弹窗 body + `.sf-icon-picker__grid` max-height 288px）；桌面 grid 1180/288，390 下 2368/288；搜索 60 项/页。
  3. 授予结果区 footer 吸底：**未验**（写路径未跑通）。
- 公开面 `/badges`：12 枚（3 gold / 3 silver / 6 bronze），`data-tier` 与等级外观正常，明暗 × 1633/390 无溢出，**aria-label 无裸键**（`badges?.tier.*` 命中 0）。

截图：`storage/qa/2026-11-12-badges-tiers-tab-{badges,groups,tiers,grants}-1633.png`、`...-tiers-dialog-picker-1633.png`、`...-badges-{badges,tiers,grants}-390.png`、`...-tiers-dialog-390.png`、`...-tiers-dialog-picker-390.png`、`...-tiers-list-390-dark.png`、`...-tiers-dialog-390-dark.png`、`...-public-badges-1633-light.png`（+dark/390 变体）。

## 3. 问题

### P1（阻塞）

**停用「最后一个启用等级」没有任何保护，会把全站最高级徽章清空。**

- `apps/api/app/Models/Badges/admin_tiers.go:82-107`：`UpdateTier` 只做入参校验与落库，启用状态变化后直接 `RecomputeAll`。
- `apps/api/app/Models/Badges/tiers_store.go:91-105`：`UPDATE badge_tiers ...` 不看其它档位。
- 对比删除路径 `tiers_store.go:150-157` 有 `remainingEnabled == 0 → ErrBadgeTierInUse`。
- `postgres_store.go:29-39` 的所有最高级徽章查询都 `JOIN badge_tiers t ON t.key = badges.tier AND t.enabled`，全档停用 → JOIN 空 → `RecomputeAll` 把 `top_badge_id` 落空。
- 前端 `SFAdminBadgesTiersTab.vue:62-78` 的 toggle 也没有这层拦截。
- 复现：连续 `PATCH /api/v1/admin/badges/tiers/{gold,silver,bronze}` `{"enabled":false}` → 帖子旁与资料页的最高级徽章全部消失。
- 修复口径：与删除一致，保留 ≥1 个启用档，否则 422；前端在「最后一档」时禁用 toggle 并给说明。

### P2

- 等级 tab hint 文案与实现不一致：`admin.badges.tiers.hint` 只说「不参与新授予」，实现还把它排除出最高级徽章竞争。
- 390 下颜色行需要横向拖动才能触达「按位置自动」与最后几色（设计有意 `overflow-x:auto`，可发现性代价真实存在）。
- 图标 picker 嵌套滚动：grid 自己 288px 滚动区落在弹窗 body 滚动区内，390 下滚动手势先被 grid 吃掉。

### P3

- `useAdminBadges.ts:203` 注释称「空搜索词后端返回空列表」，实际 `grant_candidates.go:78` 的 `WHERE $1 = ''` 会返回前 20 个用户；UI 侧空查询不发请求，线上无泄露，属注释与后端能力不符。
- Palette parity 测试 `tests/admin/forum/trustLevelNameFallback.test.ts:197-203` 保留 4 个候选路径，两处分叉会静默取第一份；建议收敛成单一断言。
- 迁移 down 的 `UPDATE badges SET tier='bronze'`（`202611120001_badge_tiers.sql:72`）没动 `updated_at`。
- bronze 种子色是 hex `#b45309`，前端兜底 `BUILTIN_TIER_COLORS.bronze='rose'`（`forumBadge.ts:41`），只在载荷缺失时生效。
- `app/Support/Palette/` 目录名与包名 `palette` 不一致（Go 允许，但与仓库其它 Support 包惯例不符）。

## 4. 两处已知偏离的判定

- **9 色 palette 上提到 `app/Support/Palette`：可接受。** 跨领域共享、无领域依赖，Badges 不 import TrustLevels，方向单向；TrustLevels 保留同名薄包装让调用点零改动；前端 parity 断言对象仍是同一份 9 色。附注：建议目录名与包名对齐。
- **`docs/extensions/v3/catalog-identities.json` 补 8 条路由 + 6 个组件身份：可接受且必需。** 与 `generate.mjs` 新增的 `/api/v1/badges/tiers` public 策略、`reviewedGuardPolicies` 条目、registry tripwire 410→418 完全对得上，不是用改脚本糊过检查。

## 5. 未验（不要当已验）

- 写路径全部：等级新建 / 改色 / 改图标 / 调序 / 停用 / 删除 + 迁移目标、批量授予（2 人 + 重复 → skipped）、授予结果区 footer 吸底、编辑弹窗等级下拉只列启用档。
- 关闭 `features.badges` 后徽章端点 403、后台三态告警。
- 帖子列表 chip / 资料页 chip（当前库里没有用户持徽章，基线 0，需先授予才有 chip）。

## 7. 修复记录（2026-11-12，P1）

- `apps/api/app/Models/Badges/tiers_store.go`：`UpdateTier` 改为事务 + `FOR UPDATE` 锁行；`input.Enabled == false` 时先数「除自己以外还有几个启用档」，为 0 返回 `ErrBadgeTierInUse`（409 `forum.badge_tier_in_use`），与 `DeleteTier` 同口径。
- `apps/api/app/Models/Badges/badges_tiers_postgres_integration_test.go`：新增 `TestUpdateTierRejectsDisablingLastEnabledTier`（拒绝 + 不落库 + 有其它启用档时允许）。
- `apps/web/app/components/admin/badges/tabs/SFAdminBadgesTiersTab.vue`：`enabledCount` + `isLastEnabled()`，最后一档的开关禁用并给 `title` 提示，`toggleEnabled` 同步拦截。
- i18n `admin.badges.tiers.hint` 改为同时说明「不能选给新徽章」与「不参与最高级徽章竞争」；新增 `admin.badges.tiers.lastEnabledHint`（中英）。

**验证状态（2026-11-12 复验，shell 恢复后）**：`go build ./...` BUILD_OK；`bun run typecheck` 通过；`bun test` 2379 pass / 0 fail；`./scripts/test-api-isolated.sh ./app/Models/Badges/ ./app/Http/Controllers/Badges/ ./database/migrations/` 三包全 ok（含新增集成测试）；架构边界 / OpenAPI refs / v3-catalog --check 全通过；迁移文件未改，可逆验证沿用上一轮结论。dev 库临时授权已回滚（`badge.manage` 0 行），`badge_tiers` 仅 gold/silver/bronze 三行。

### 7.1 P2：新建等级的 position 落 0（写路径 QA 发现，已修）

- 现象：`POST /admin/badges/tiers` 不传 `position` 时落 0，与 gold 并列最高档；因 `topBadgeOrderSQL` 首键是 `t.position`，挂在新档位的徽章会与金牌徽章争同一名次。`SFAdminBadgeTiersTab.vue` 的注释却写「追加到末尾」。
- 修法：新增哨兵 `TierPositionAppend = -1`（`tiers.go`），`validateTierInput` 放行该哨兵；`tier_handlers.go` 的 `createInput()` 缺省填哨兵；`tiers_store.go` 的 `CreateTier` 用 `INSERT ... SELECT CASE WHEN $6 < 0 THEN max(position)+1 ELSE $6 END` 在**同一条语句**里解析，避免并发创建拿到同一个 max+1。显式传 0 仍然被尊重。
- 测试：`tiers_test.go` 的负 position 用例改成 `-2`，并新增「哨兵被接受」；集成测试新增 `TestCreateTierAppendsToEnd`（缺省 → 3，显式 0 → 0）。
- 复验：`go build ./...`、Badges/Controllers 两包 `test-api-isolated` 全 ok，静态门禁全通过。

### 7.2 写路径浏览器证据（小审，CDP 脚本 + 43 张截图，`storage/qa/`）

- 等级：新建（含色块 + picker 选图标）→ 改色改图标 → 上移/下移 → 停用 → 删除（迁移下拉只列启用档、未选目标前按钮禁用、两步确认文案切换）。
- 徽章编辑弹窗等级下拉正确排除停用档。
- 批量授予：2 字符搜出 9 条候选；已持有置灰且点不动；实测 **已授予 1 / 已持有 1 / 失败 0**（HTTP 200，skipped 不是错误）。
- 授予结果区 footer：390×500 下 body 真溢出（194/176），滚到底 footer `top 348→348` 不动且不在滚动区内。
- P1 的 UI 保护实测：停掉 gold/silver 后 bronze 的开关变 disabled 且带提示，随后恢复三档。
- 库复原：override `DELETE 1` → 0 行（回滚后 PATCH 返 403 作反证）；`badge_tiers` 三行全启用；`badges` 12 枚、无 `qa-tmp%` 残留；一次性等级与徽章（id=29）已删。


