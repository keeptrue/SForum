# 2026-11-13 徽章佩戴出口收口（取消佩戴 = 不佩戴、底部常驻「自动展示徽章」）

Status: **landed（未提交，工作树；`HEAD = 53928c4a8`）**。前半是**前端出口语义**（三态
auto / manual / hidden 与 `PUT {hidden:true}` 早已由 `202611130004_badge_wear_hidden.sql` +
`wear_store.go` 落地，本轮只把三个出口摆正、文案从状态词改成动作词）；同日**后端返工**一次
（YG 实测 hidden 态点佩戴假成功，见 Changed 最后一条与决策「修订三 · D11」）。

## Changed

- `SFBadgeDetailModal.vue` / `SFProfileBadgeTab.vue` 的卡片按钮：已佩戴时可见文字与 `title` 都是
  「取消佩戴」（原「佩戴中」+ title「取消佩戴」）；`aria-pressed` 仍只表达状态。
- `SFProfileBadgeTab.vue` 的 `toggleWear`：已佩戴分支从 `worn.clear()`（`DELETE` 回 auto）改为
  `hideWorn()`（`PUT {hidden:true}`）。`hideWorn` / `restoreAuto` 各加当前态 guard——当前态那颗
  点了不发请求，且**不用 `disabled`**（保持可聚焦）。
- 底部状态条改成两个并列出口：`.sf-profile-badges__worn-auto`（自动展示徽章，`DELETE`，
  `mode === 'auto'` 时 `aria-pressed` + `--active`）与 `.sf-profile-badges__worn-hide`
  （不佩戴徽章，`PUT {hidden:true}`，`isHidden` 时按下）；容器 `flex-wrap: wrap` + `row-gap: 8px`。
- i18n `badges.worn`：新增 `auto`，删 `wearing` / `restoreAuto`，`unwearSuccess` → `autoSuccess`，
  `hideSuccess` 改「已不佩戴任何徽章」。来源登记在 `apps/web/i18n/locales/README.md`
  （YG UI 复核，非规范原文）。
- 测试（值级断言）：`tests/badges/badgeDetailModal.test.ts`、`tests/profile/badgeWorn.test.ts`、
  `tests/forum/profileBadgeTab.test.ts` 按新语义改写——已佩戴文案 + `PUT {hidden:true}` + 整轮无
  `DELETE`、底部两颗按钮的 `aria-pressed`、当前态点了 `calls` 长度不变、i18n 新键集与「旧键已删」。
- **小审返工（同日）**：R1 补上卡片可见文字的断言（`expect(button.text()).toBe('取消佩戴')`）——
  此前只断言 `title`，把可见文字改回「佩戴中」是 24 pass / 0 fail 的假绿，现在变异 A 必红；
  R2「目录页的站内入口」注释从状态条上方移到 `.sf-profile-badges__more` 正上方；R3 状态行补
  `min-width: 0` + `overflow-wrap: anywhere`（超长英文徽章名在 390 会撑破 flex 容器）；R5 补
  「hide 失败回滚 + 常驻错误 toast」用例（`apiError(..., 500)` → `saveFailed`）。
- **后端返工（同日，task-3，已 PASS）**：YG 实测「hidden 态点佩戴 → 提示成功但没戴上」。两处根因：
  ① `SetWornBadge` 只写 `top_badge_id` / `top_badge_manual`，漏清 `top_badge_hidden`，而
  `DisplayBadge` 先判 hidden 就 return → **200 + `mode=hidden` 的假成功**；② `RecomputeAll` 把 hidden
  行算进重算作用域，全量重算会碰它（变更计数与「什么都不戴」的语义打架）。修复落点：
  `SetWornBadge` 三列一条 UPDATE（补 `hidden = FALSE`）；`RecomputeAll` 加作用域闸门
  `AND NOT users.top_badge_hidden`（`SET` 的 hidden 分支只作纵深防御）；`RecomputeTopBadge` 对 hidden
  行写 `top_badge_id = NULL` 并给 `manual` 加 `NOT top_badge_hidden`（半坏行收敛成
  `(NULL, FALSE, TRUE)`）。`TestWearingBadgeAfterHideUnhides` /
  `TestHideWornBadgeClearsSlotAndSurvivesRecompute` 转绿，新增
  `TestRecomputeTopBadgeRepairsHalfBrokenHiddenRow`；
  `./scripts/test-api-isolated.sh ./app/Models/Badges/ ./app/Http/Controllers/Badges/ ./database/migrations/`
  三包 ok、`go build ./...` exit 0；Lead 浏览器复验 hidden → `PUT {slug}` 现返回 `mode=manual` +
  `worn` + `upgrade`。

## Decisions

- `../decisions/2026-11-13-badge-worn-display.md`「2026-11-13 修订二 · D10」：取消佩戴 = hidden、
  自动展示 = `DELETE`、auto 态旧按钮是可见空操作；上一轮 label-in-name 结论不变。
- 同一决策「修订三 · D11」（后端返工后补）：hidden 行不进全量重算作用域（闸门，不是镜像 `CASE`）、
  半坏行由单用户重算收敛成 `(NULL, FALSE, TRUE)`、`refreshed` 不为清标记扩语义。
- 模块 `../modules/badges.md`：Data Model 补 `users.top_badge_hidden`；新增「佩戴的三个出口」
  （状态机表 + 按钮文案必须写动作）；陷阱区加「佩戴按钮写状态不写动作」三条判据。

## Next

- **浏览器已验（Lead，1633×810，站长 zhuchunshu，2026-11-13）**：manual 态弹窗/卡片可见文字
  「取消佩戴」；点它 → `PUT /api/v1/badges/worn` 200（hidden）+ toast「已不佩戴任何徽章」+
  状态行「未佩戴徽章」+ 不佩戴按钮按下；点「自动展示徽章」→ `DELETE` 200 + toast
  「已恢复自动展示徽章」+ auto 按钮按下 + 自动徽章卡片「取消佩戴」；hidden 态点已按下的
  「不佩戴徽章」无请求无 toast（guard）。截图在 `/home/inkedus/下载/sforum-badge-qa/`
  （01-manual / 02-modal-worn / 03-hidden / 04-auto / 05-wear-after-hidden-fixed）。
- **真实 390 视觉复验（仍未做）**：Lead 只做了 **358px 容器结构验证**（底部两按钮换行、
  无横向溢出、60 字符无空格英文名 `scrollWidth = clientWidth`），**没有 390×844 视口截图**；
  自动化浏览器 `window.resizeTo` 被忽略，无法切视口。R3 的 `min-width: 0` +
  `overflow-wrap: anywhere` 已在 358px 容器 + 超长名下实测不溢出（截图 06）。
- 提交时与工作树里同批的佩戴/通知改动一起走。同日后端返工（task-3）改了
  `apps/api/app/Models/Badges/{wear_store,postgres_store}.go` 与集成测试；`contracts/openapi/*` 未改。
- 残留不变：佩戴乐观更新没有版本号（模块 Known Residuals #38）。

## Open Questions

1. `manual` 态点「自动展示徽章」会丢用户选择（回 auto）。这是 D10 的预期语义，但是否需要确认弹窗
   或撤销 toast 由产品决定，本轮不做。
2. `hidden` 态下状态行「未佩戴徽章」与「不佩戴徽章」按钮的按下态是两条冗余线索；保留是为了让
   「当前为什么不戴」可读，将来若加图标可考虑合并。
