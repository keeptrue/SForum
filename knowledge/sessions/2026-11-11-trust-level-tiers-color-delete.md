# 2026-11-11 信任等级：档位颜色（9 色）+ 删除墓碑

## Changed

**后端口（apps/api）**

- 新增 `Models/TrustLevels/tier_colors.go`：9 色白名单 + 默认色序（第 n 项属于 rank n，TL0 = `slate`）+ `DefaultTierColor`。
  **9 而不是 8**：`MaxTiers = 8` 且 `slate` 被虚拟 TL0 占掉，8 色会让满配 TL8 回退成 `slate` 与最低起点同色（`tier_colors.go:15-18`）。
- `types.go`：`Tier.Color` 存**原始值**（空 = 跟随位次，解码不填默认）、`Policy.ColorOf(rank)` 解析有效色
  （TL0 恒取首项）、`NextTierSeq()` 计入 `DeletedTierIDs`。
- `config.go`：`color` 校验（`color_not_whitelisted`，rank 可用）；`deletedTierIds` 三条判据
  （`deleted_tier_id_invalid` / `_duplicate` / `_present`，rank 恒 0、不去重静默接受）；码表 29 → **33** 条。
- `policy.go` 新增 `RestoreDefaults(previous Policy)`：墓碑 = 既有墓碑 ∪ 被移除的 `tier_<n>` id（保序去重，
  内置 id 不入墓碑）；`controller.go` 的 `restoreDefaults` 改为「读 previous → RestoreDefaults → EncodeConfig → save」，
  读失败 fail-open 到纯推荐值并 `slog.Warn`；`recommendedConfig()` 仍是纯 `DefaultPolicy()`。
- 读面 `payload.go` 下发已解析的 `color`；后台读面 `dto.go` 下发 `colorChoices`（前端不抄白名单）。

**前端（apps/web）**

- 新增 `assets/css/sforum-trust-colors.css`（`--sf-tl-<token>` 明暗两套，单一来源）；`trustLevelColors.ts` 落 `--tl-accent`，后台档位行与资料页共用。
- 新增 `SFAdminTrustLevelColorPicker.vue`；`useTrustLevelsTiers.ts` 接颜色与墓碑 id；
  `useTrustLevelTierIntents.ts` 的删除闸门 = 已停用 **且** 已保存为停用；`useTrustLevelsForm.ts` 的
  `fallbackTiers()` 把 `color` 置空（避免首次保存把「自动」冻成取值）。
- D-N8 修订落地：停用披露从页面级常驻 `UAlert` 改为确认 modal + 停用行内常驻「已撤回 N 项解锁」徽标，列表底部另有墓碑脚注。
- 错误码文案：`SPECIFIC_ERROR_CODES` 15 条（新增 `color_not_whitelisted`）、`CONFIG_BUCKET_ERROR_CODES` 18 条（含三条 `deleted_tier_id_*`，无专属文案）。

**契约**：`contracts/openapi/schemas/trust_levels.yaml`（`color` 可选 + 白名单说明、`colorChoices`、
`deletedTierIds`、reasonCode enum 33 条）、`paths/trust_levels.yaml`（恢复默认保留墓碑）。

**验证证据（本轮实跑，非引用他人）**

- `cd apps/api && go build ./...` → **exit 0**。
- `./scripts/test-api-isolated.sh` → **141 包 `ok`、`^FAIL` 计数 0**（日志 `/tmp/pm-api-test.log`）。
- `ruby scripts/validate-openapi-refs.rb` → `OpenAPI references OK: checked 3194 refs across 65 files`。
- `cd apps/web && bun test` → **2246 pass / 0 fail**（239 files、22536 expect）。
- `cd apps/web && bun run typecheck` → **exit 0**（`.nuxt-typecheck` 生成、无报错）。
- `node tests/validate-architecture-boundaries.mjs` → **exit 0**（2105 production files scanned）。

## Decisions

新增 `decisions/2026-11-11-trust-level-tier-color-and-delete.md`：**D-C1..D-C5 + D-N8 修订**。

- **D-C1** 颜色是可选属性，空 = 跟随位次；9 色白名单；解码不填默认；有效色由 `ColorOf` 解析进读面。
  被否：只做前端自动配色、自由 hex。
- **D-C2** 删除只针对「已保存为停用」的档；两步闸门拒绝「停用未保存 → 删除 → 保存」；删除永不改 rank，
  因此不触发结构确认。被否：任意档直接删、不留墓碑的硬删。
- **D-C3** 删除留 `deletedTierIds` 墓碑，`NextTierSeq` 计入，保住「id 永不复用」；
  **补强**：`RestoreDefaults` 也保留墓碑（原条目只说了 `NextTierSeq`）。被否：允许 id 复用。
- **D-C4** 回滚口径：新字段全 `omitempty`，未用新能力的站点逐字节不变；用过之后回滚旧二进制会因
  `DisallowUnknownFields` 整份解码失败 → 必须同时回退 `trust.levels` 的值。**这是数据形状升级，不是缓存问题**。
- **D-C5** 档位色用 `--sf-tl-<token>` 单一映射 + `--tl-accent` 注入，与站点 5 套主题 accent 解耦。
  被否：Tailwind 字面类映射（后台/前台各写一份）。
- **D-N8 修订**：停用披露从「页面级常驻 UAlert」改为「确认 modal + 行内常驻徽标」；徽标由数据派生、
  跨刷新仍在，所以信息不丢。被否：顶部 UAlert 再加一行（同级披露会被当成过期残留，且滚出视口问题依旧）。

模块注释 `modules/trust-levels.md` 已补：9 色 token 与自动色序、删除与墓碑语义、新错误码 4 条、码表总数 33
（原 29 / 14 专属 / 15 桶 → 33 / 15 / 18），并把恢复默认的墓碑保留写进 D-N15 小节。

## Next

- 本刀无遗留实现项。下一刀候选（颜色分布/主题联动）见 Open Questions，等 Lead 排优先级。

## Open Questions

- **`SFAdminTrustLevelTiersTab.vue` 已 526 行**（超 500 行复核阈值，未超 1000 硬线）。下刀建议抽出
  `SFAdminTrustLevelTierRow.vue`（行内：色点/图标/名称/id/停用徽标/操作按钮），把 526 压回 400 以下。
- **墓碑态只有测试级证据**：删除 → 保存 → 重新加载后墓碑仍生效，目前由 `restore_defaults_test.go`、
  `controller_restore_test.go`、`trustLevelSaving`/`trustLevelTiers` 单测覆盖；浏览器级（真实点击删除 +
  刷新后确认 `deletedTierIds` 仍在、新档从更大序号开始）尚未取证。
- **violet / amber 档位色与同色系主题 accent 完全同色**：violet 浅 `#7c3aed` / 深 `#a78bfa` 与主题 accent
  比值 1.00，amber 深 `#fbbf24` 与 amber 主题 `--sf-accent-dark` 同样 1.00（报告 §9.5）。当前形态（3px 色条 +
  12% 淡底）不与按钮并置，影响有限；若走查判定「紫主题下 TL5 像按钮」，按报告 §4 口径**改几何与形态，不改色相**。
- **已修（2026-11-11 收尾）**：`apps/web/**` 的旧编号与过期数字都已对齐——注释统一成 `D-C1`（颜色）/
  `D-C5`（CSS 变量侧）/ `D-C2`（删除闸门）/ `D-C3`（墓碑），三处「8 色白名单」改写成「数量 = 合法 rank 数」
  （`MaxLevel + 1`，`useTrustLevelsTiers.ts`、`useTrustLevelsForm.ts`、`useTrustLevel.ts`）；
  `rg -n 'D-N1[56]' apps/web/app apps/web/tests apps/web/nuxt.config.ts` 无输出。Go 侧 `D-N15`
  仍合法（= 恢复默认整份覆盖，`policy.go:100`、`controller.go:213`），不需要改。
- **本刀没有独立计划文件**：`plans/2026-11-11-trust-levels-n-tiers.md` 状态未变（该计划是上游，本刀是其下游补齐），
  `plans/README.md` 未新增行。若 Lead 要求可追溯，可补一份短计划或直接以决策记录为准。
