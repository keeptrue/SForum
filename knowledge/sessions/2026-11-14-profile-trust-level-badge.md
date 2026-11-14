# 2026-11-14 资料页信任等级徽章 + 等级详情弹窗

## Changed

前端一处产品面，全部在 `apps/web`，**后端与 OpenAPI 零改动**（`viewer.conditions` / `viewer.nextLevel` / 每档
`requirements` 本来就已在契约里，这次只是补上前端消费）。

- `app/components/profile/SFTrustLevelBadge.vue`（新，163 行）：资料页头部身份行 `UID n` 之后的档位色小牌
  （`图标 + TL{n}`，20px 高与 `.sf-profile-uid` 同高，12% 档位色底 + 34% 环，文字恒前景色）。
  `interactive` 时是 `<button aria-haspopup="dialog">`，否则 `role="img"`。
- `app/components/profile/SFTrustLevelDetailModal.vue`（新，502 行）：UModal 弹窗 = 当前档 + 下一档门槛 +
  本人进度 + 规则条件 + 满级态；他人视角只有前两项。
- `app/components/profile/SFTrustLevelCard.vue`：新增可选 `interactive`，末尾追加透明覆盖按钮（`open` emit）。
  既有 class / `data-*` / `aria-*` / `ladder: () => []` 与「源码不得出现 `trustLevelLadder(`」全部保留；
  根节点仍是 `<section>`。hover/focus 用 **100%** `var(--tl-accent)`（34% 混色只有 1.63:1）。
- `app/composables/profile/useTrustLevel.ts`：`TrustLevelCondition` 类型、`TrustLevelView.nextLevel/.conditions`、
  `localizedTierName`、`trustLevelNextLevel`、`parseViewerConditions`、`trustLevelRequirementRows`；
  computed 里接上 viewer 的两个新字段。
- `app/components/profile/SFProfileShowPage.vue` / `SFProfileRightRail.vue`：页面级唯一一份弹窗 + 两处入口接线
  （`openTrustDetail()` 先 `closeMobileDrawers()` 再开，避免抽屉与弹窗两层 overlay）。
- `i18n/locales/{zh-CN,en-US}.json`：`profile.trustLevel.badge.ariaLabel` + `profile.trustLevel.detail.*` 共 9 键，
  两侧对称、无死键。
- 测试：新增 `tests/profile/trustLevelBadge.test.ts`(11)、`trustLevelDetailModal.test.ts`(11)、
  `trustLevelViewerConditions.test.ts`(12)；扩展 `tests/profile/trustLevelCard.test.ts`（+3 用例，纯追加）；
  `tests/admin/forum/trustLevelAccentAttribute.test.ts` 登记两个新组件，断言从「恰好一处」收紧为
  「至少一处 + 每一处都必须带 `data-sf-tl-accent`」。
- 规格：`reports/2026-11-14-profile-trust-level-badge-modal-spec.md`（形态/几何/对比度实测/降级/他人视角）。
- 模块笔记：`modules/trust-levels.md` §「等级标识的展示口径」已改为三入口口径（头部徽章 + 卡片 + 弹窗）。

## Decisions

- **推翻 2026-11-11 的「个人中心也不再加新徽章」**：产品 2026-11-14 明确要求头部加徽章、卡片可点开弹窗。
  论坛读面仍不显示等级，那条不变。已在规格 §0 与模块笔记里写明是口径变更，不是漂移。
- 头部徽章不沿用 2026-11-11 论坛那份「色点 + 无底色」（那份为 390px 作者行设计），改用与佩戴徽章 chip
  同族的「12% 底 + 34% 环 + 档位色图标 + 前景色文字」；徽章只写 `TL{n}`，不写档位名（长名会挤爆 390px 身份行）。
- 徽章环线的 34% **不承诺 3:1**（实测最差 1.63:1），可访问性由 `TL{n}` 文字承载（最差 11.80:1）；不为了
  「边界可见」加粗加深。
- 他人视角只给当前档 + 下一档门槛：阈值随公开目录下发、人人可见；后端不下发他人 `bottleneck`/`conditions`，
  前端也不许用阈值反推「还差多少」。
- 卡片不改成 `<button>`：`role="progressbar"` 会被吞进按钮可访问名。用覆盖层按钮，冻结契约零改动。
- `trustLevelNextLevel` 只保留一份实现（弹窗复用 helper）：两份等价实现的分歧不会报错，只会「测试全绿、
  行为漂移」。

## Next

- 前端门禁已全绿：`bun test tests/profile` 153 → 162（含护栏）、`bun test` 全量 **3046 pass / 0 fail**、
  `bun run typecheck` exit 0、`node tests/validate-architecture-boundaries.mjs` exit 0。
- 浏览器实测 10 张截图 + `qa-evidence.json`（61 条证据、failures 空）在
  `work-team/review/task-1-trust-level-badge/`，含焦点回归三条路径（徽章→X / 徽章→Esc / 卡片覆盖层→关闭，
  关闭后 activeElement 分别回到触发元素）。
- 未提交：`work-team/` 与 `.dsh/` 都不在 `.gitignore` 里，**提交时不要 `git add -A`**（1.3MB 截图 + 会话目录）。
- 可选的下一步（未做，别当待办自动开工）：等级权益（`earnable`）在弹窗里展示需要权限名的 i18n 映射，
  是独立的一件事。

## Open Questions

- `profile.trustLevel.badge.ariaLabel` 目前读作「信任等级 正式用户，TL1，已获得」——`TL1` 会被读屏念成
  「T L 1」。2026-11-11 论坛规范要求读作「信任等级 1」。若要统一，把该键的 `{code}` 换成 `第 {level} 级`
  即可，属于文案口径问题，未擅自改。
- 头部徽章的 `TL0` 是灰色虚线（设计如此，表达「未入门」），在只看截图时容易被读成「第二个 UID pill」。
  若产品希望 TL0 也更强，可选方案是给 TL0 加「新用户」文字——但那会让徽章宽度随档位变化，需要重新核 390px。
