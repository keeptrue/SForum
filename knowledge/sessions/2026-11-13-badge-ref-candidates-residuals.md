# 2026-11-13 徽章 objectRef 候选残留收口（R1–R5）hot handoff

## Changed

- **前端读侧纯逻辑 `apps/web/app/utils/admin/badgeRefCandidates.ts`**：`refKindManual(): boolean`
  → **`refKindMode(): 'select' | 'unavailable' | 'empty'`**（旧函数已删，不留别名）；
  新增 `declaredRefKinds(items)` 与 `refCandidatesMissingKinds(items, raw)`。
- **参数控件 `SFAdminBadgeRuleParamField.vue`**：文本框 `data-ref-manual-state="empty|unavailable"`；
  两句提示分开（`refEmptyHint` / `refUnavailableHint`，删 `refManualHint`）；白名单外 `refKind`
  走 `data-ref-unknown-kind`（并压掉那句笼统的「候选取不到」）。
- **规则区 `SFAdminBadgeRuleSection.vue`**：`data-ref-candidates-incomplete` 横幅；
  prop 链 `pages/admin/badges.vue → tab → 编辑弹窗 → 规则区` 接通 `refCandidatesMissingKinds`。
- **影响面 `useAdminBadgeRuleImpact.ts` + `SFAdminBadgeRuleImpactDialog.vue` + 编辑弹窗**：
  新增 `status='manual'` 与 `markManual()`、纯函数 `shouldLoadRuleImpact({isEdit,badgeId,draftMode,savedMode})`；
  手动徽章**不发请求**，中性「无法估算」；`error` 才是红色「获取失败」。
- **后端 `AdminService.EstimateRuleImpact`**：`grant_mode='manual'` → 200
  `{estimated:null, method:"unsupported_condition", partial:false, unsupportedConditions:[]}`。
- **新错误码 `forum.badge_rule_mode_conflict`**：`errors.go`（sentinel + 常量 + `ErrorCode` 顺序）、
  `admin_service.go` 两处 wrap、`controller.go` 的 `mapBadgeError`（放在 `ErrBadgeRuleInvalid` 之前）、
  `Localization/messages.go` zh+en、`contracts/openapi/{paths,schemas}/badges.yaml` 两码分工 prose。
- **测试**：`badgeRefCandidates.test.ts`（三态 + declared/missing 五条 + 两条 composable）、
  `adminBadgeRuleLeafParams.test.ts`（`data-ref-manual-state` 逐态 + 未知 kind 诊断）、
  `adminBadgeRuleImpact.test.ts`（manual 中性 / manual×error 互斥 / `shouldLoadRuleImpact`）、
  Go：`TestEstimateRuleImpactOnManualBadgeIsNeutral`、`TestManualBadgeWithRuleIsModeConflict`、
  `TestMapBadgeErrorPutsModeConflictBeforeRuleInvalid`，并改 `TestAdminCreateBadgeRejectsInvalidRules`
  的 `manual with rule` 期望（收紧到新码，不是放宽）。
- **顺带（为守 500 行纪律，不是功能）**：`unmappedFieldMessages` 从弹窗搬进
  `utils/admin/badgeFieldSlots.ts`，`SFAdminBadgeEditDialog.vue` 503 → **492** 行，架构计数回 216。
- **验收脚本一处补强**：`verify-badge-rules-acceptance.sh` 的 `X-03` 新错误码清单追加新码。

## Decisions

- **「取不到」与「本来就没有」在文案上必须分开，但渲染仍只两叉**：运营后果一样（都没可选项），
  差别只在「该不该去查后端」。三态纯函数 + `data-ref-manual-state` 是这条的唯一实现。
- **目录诊断三条不许合并**：老后端（无字段，静默）/ 有数组缺类（真回归，横幅）/ 白名单外 kind（控件级诊断）。
  白名单外的 kind **不进「缺失」桶**——它不可能出现在响应里，报缺失只会误导。
- **手动徽章的影响面是中性事实**：后端唯一真相源（200 + `estimated:null`），前端据**库里的授予方式**
  决定问不问；`0` / `null` / `manual` / `error` 四态互不顶替。store 层语义不放宽（残留 33）。
- **mode 冲突必须独立成码**：`manual` 却带 `rule` 的修法是「改授予方式或清规则」，与「改指标/阈值」
  是两条完全不同的动作；共用 `forum.badge_rule_invalid` 会让运维改错地方。

## Next

- **验收结论：5 条残留全部关闭**（4 条语义 + 2 条覆盖缺口）。报告
  `reports/2026-11-13-badge-ref-candidates-residuals.md`（逐条「改了什么 / 判据 / 变异证据 / 未做与原因」）。
- 门禁：`./scripts/test.sh --fast` 三线 PASS（api 347s / web 50s / static 16s）；架构 216（基线）、
  OpenAPI 3312 refs、docs、tzdata 全 exit 0；`bun test` 2643 pass / 0 fail；typecheck exit 0。
  验收脚本两轮：无库 **ok 20 / FAIL 0 / pend 53（:8080 API 在跑）**，带库 **ok 78 / FAIL 0 / pend 9**。
- **下一刀仍是控件族统一**：`fact.count` 的 `factKey`/`variant` 还是原生 `<select>`
  （`object.engagement.topicId` 也不动，理由见模块笔记残留 28/29）。
- 未动：`site.timezone`（站点级决策，需用户拍板）；`fact.count` 分支一行未改；零新增路由（计数仍 423）。

## Open Questions

- **手动 → 自动这条路径还该不该弹「保存前确认影响面」？** 现在弹窗会以中性态出现（无数字、无警报），
  作用是「确认要切自动」，与「确认影响面」的原始名义已经不完全一致。若将来觉得多余，删它的条件是
  「切自动时没有任何需要确认的影响面」，需要产品拍板——**不要因为「看起来没信息」就顺手删**。
- `.env` 里的管理员会话是上一刀留下的；本轮沿用未重建（未猜账号密码）。会话失效时验收脚本会退化成
  pend（脚本本身这样设计），别把它读成缺陷。
