# 2026-11-11 信任等级：分档发言限额编辑器 + 徽章「做过又撤回」

## Changed

**保留：分档发言限额编辑器（`da1d01683`）**

- 后台第 5 个分区 `limits`（zh「发言限额」）：`pages/admin/forum/trust-levels.vue` 的 `tabIds`
  现为 `overview/tiers/thresholds/limits/earnable`；`restoreLossItems` 加 `limits`。
- 新 `composables/admin/forum/trustLevels/useTrustLevelLimits.ts`（151 行，纯函数）+ 
  `components/admin/forum/trust-levels/tabs/SFAdminTrustLevelLimitsTab.vue`（289 行，只画面板）。
- 三态与后端输入一一对应：`null` 全继承 / 对象逐字段（子字段 `null` = 仍继承）/ `0` = 不限额；
  `trustLevelsPayload` 未改，`limits` 恒存在（`null` 或四键对象），省略形态前端永不写。
- 后端零改动（解码/校验在 N 档那刀就位）；改限额不触发结构确认。

**已撤回：论坛等级徽章 + 作者等级载荷（`735aeed8c` + `4f9f360b8` → `46c2f52cb`）**

- 做过的东西（**这是历史，不是待办**）：`SFTrustTierBadge.vue` 接三处（列表 / 详情作者块 / 评论
  `#name-suffix`，插入序 昵称→楼主标→TL→奖励 chip，TL0 同尺寸 `inset` 空心环）；
  `TrustLevels.BatchResolver`（每页固定 2 条 SQL：`users WHERE id=ANY`、`user_activity WHERE user_id=ANY`）；
  `Forum.AuthorTrustLevelReader` 聚焦协作者；契约 `TrustTierColor`（9 值 enum）与 `AuthorTrustLevel`。
- 为什么撤：产品口径改为「等级标识只在个人中心（资料页）显示」，论坛读面一律不显示，个人中心也不再加
  新徽章（资料页右栏 `SFTrustLevelCard` 是唯一入口）。没有消费者之后，那 2 条 SQL 仍会加在每个话题
  列表/详情/评论页；契约里留着「下发但没人用」的字段会误导下一个读契约的人。
- 现状复核：`rg 'authorTrustLevel|SFTrustTierBadge|TrustTierColor' apps/api apps/web contracts` 无命中。
- 视觉规格仍在：`reports/2026-11-11-trust-level-badge-spec.md`（`54f2387f9` 补回、`37a28b67b` 文首标注
  口径已变）。将来在受限面加等级标识可直接复用，不用重做对比度。
  （原 revert 说明写「保留该报告」但实际删了——这是本轮抓到的一处说明与 diff 不一致，已由上述两个 docs commit 修正。）

**知识库**

- 新增 `decisions/2026-11-11-trust-level-limits-editor.md`（D-L1..D-L4）。
- `modules/trust-levels.md`：后台页「四 tab」→「五 tab」；新增「等级标识的展示口径 + 徽章做过又撤回」
  与「分档发言限额」两节；`n8-mobile-tabs tab=4` 的浏览器证据标注为「加第五个 tab 之前，已过期」。
- 徽章那份 D-B 决策**没有落盘到 `decisions/`**（草稿留在 `/tmp/pm-drafts/`）：结论压缩进模块笔记，
  避免给后人一份「看似生效」的决策记录。

**门禁（来源）**：`bun test` 2265 pass / 0 fail（我实跑，240 files / 22661 expect；比撤前少 11 条，
正是被撤的徽章用例）；`bun run typecheck` exit 0（我实跑）；`validate-architecture-boundaries.mjs` exit 0
（我实跑，2108 files）；`go build ./...` exit 0（我实跑）；`validate-openapi-refs.rb` 3194 refs（我实跑，
从 3199 回落到撤前值）；Go 隔离套件 141 包 0 FAIL（Lead 实跑，日志 `~/.cache/tmp/lead-iso3.log`，我核过计数）。

## Decisions

- **D-L1..D-L4 生效**（`decisions/2026-11-11-trust-level-limits-editor.md`）：三态不发明第四种；
  写严读宽（输入护栏 ≠ 业务上限，读侧截断会静默改小配置）；受控输入必须显式回弹；
  改限额不触发结构确认。
- **D-B1..D-B4 保留为「规格与撤回理由」，当前生效状态 = 撤回**：等级派生值不能 JOIN 只能批量现算、
  fail-open 且留痕、载荷只有三字段、徽章形态「色点+数字」+ TL0 空心环（淡底数字牌在 `parchment`
  下 teal 4.38:1 被否）。这四条只在「将来于受限面重做」时才作为前提使用，**不要**据此恢复论坛读面载荷。

## Next

- 本刀无遗留实现项。下一刀候选见 Open Questions。

## Open Questions

- `useTrustLevelsAdminPage.ts` 已 **548 行**（超 500 阈值，未超 1000 硬线）：建议把 save / restore
  编排拆成独立 composable，页面编排只留状态与装配。
- `tests/admin/forum/trustLevels.test.ts` 仍有直接对页面源码做 `toContain` 的断言（既有风格）：
  建议改成挂载级断言，否则模板一改文案就红、而真行为没变。
- 徽章撤掉后有三条旧 Open Question 一并失效，**不要当待办**：`lastReplyAuthor` 无等级字段、
  匿名 `GetTopic` / `GetTopicBySlug` 装饰的额外 `Policy()` 读取、徽章未接 `SFFeedRow.vue`。
  这些对象在 `46c2f52cb` 里已经不存在。
