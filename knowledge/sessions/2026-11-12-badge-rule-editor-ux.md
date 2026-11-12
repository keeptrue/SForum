# 2026-11-12 徽章条件规则编辑器 UX 收口

Status: **landed（未提交）**——工作树改动；6 轮开发 + 终审变异验证 + Lead 在真实 dev server 复验 + 视觉修复一轮（用户截图反馈，三处 DOM 缺陷）。
前序：`2026-11-12-badge-rule-engine-handoff.md`（M1–M3 收口）；模块：`../modules/badges.md`
「条件规则编辑器（2026-11-12 二轮改造）」；计划：`../plans/2026-11-12-badge-rule-engine.md` §10 / §15.2。

## Changed

用户报的两个运营可见 bug，根因是**同一种控件存在两条实现**：

1. **「指标 / 时间窗口」下方多出输入框（显示 `days_visited`、`[object Object]`）**：
   `SFAdminBadgeRuleLeaf.vue` 里 `objectRef` 的 `v-if` 另起了一条条件链，尾部兜底
   `<input v-else type="text">` 对 `enum` / `window` 重复渲染。
2. **「指标」下拉点开空白**：后端 `conditionParamDTO.Options []string`（与 OpenAPI
   `BadgeConditionParam.options: array<string>` 一致）下发裸字符串，前端类型曾写成
   `{value,labelZh,labelEn}[]` → 每个 `<option>` 的 value 与文案都是 `undefined`。
   测试没抓到，是因为**夹具自己按对象形状写的**。

改动落点：

- **参数控件唯一实现** `components/admin/badges/rule/SFAdminBadgeRuleParamField.vue`（306 行，单一
  `v-if/v-else-if` 链）；`SFAdminBadgeRuleLeaf.vue` 445 → 247、`SFAdminBadgeRuleSimpleForm.vue`
  482 → 332 全部改为渲染它。
- **选项文案**走 `utils/admin/badgeRuleParams.ts` 的三级 i18n 键回退
  （`conditions.params.<p>.options.<v>` → `metrics.<v>` → `conditions.enums.<v>` → 原始值）；
  `options` 类型改为 `string[]` 与 wire 对齐；读入口 `useAdminBadgeConditions.normalizeConditionsPayload`
  归一 `params[].options`。
- **双模式**：新增纯函数 `utils/admin/badgeRuleSentence.ts`（`isSimpleRule` / `flattenAllLeaves` /
  `buildFlatAllRule` / `leafSentence` / `ruleSentence`，注入 `t`/`te`）+ `SFAdminBadgeRuleSimpleForm.vue`；
  `SFAdminBadgeRuleEditor.vue` 加 `data-rule-mode` 切换与常显一句话预览，规则含 `any`/`not`/嵌套组时
  按 `rule.modeAdvancedOnly` 降级只读（**只降级，不改 AST、不 emit**）。
- **去噪音**：删掉弹窗顶部「指标 / 阈值」只读两栏与 `form.legacyRuleHint`（连同 `form.metric` /
  `form.threshold` 三个 i18n 死键）；规则 JSON 改成默认折叠的原生 `<details>`「开发者视图」，复制按钮
  在 `<summary>` 行上（折叠态一次点击）。
- **错误呈现**：`FIELD_SLOT_KEYS` 抽到 `utils/admin/badgeFieldSlots.ts`（23 行）；没有展示位的字段错误
  （`metric` / `threshold` 等）合并进通用提示，不再静默丢失。
- **视觉修复（用户截图反馈「UI 有问题」，量真实 DOM 定位三处）**：① 带单位的阈值字段比其他字段高
  **21px**、label 不齐（`align-items: flex-end` + 单位当第三行渲染）；② 单位「次 / 篇」掉到 input 下方
  单独一行；③ 删除按钮被 `flex-wrap` 挤到控件行下面成了孤立图标。修法：单位改成 input 的**行内后缀**
  （`.sf-simple-field__value` / `.sf-rule-field__value` 包裹 input + unit，两模式统一，`days_visited`
  仍不出单位）；简单模式控件行 `align-items: flex-start`，字段按「条件 `2 1 180px` : 比较 `1 1 132px` :
  阈值 `0 1 104px`」分层；删除按钮移进卡片头 `.sf-simple-rule__head`（与人话同一行）。

## Decisions

1. **简单模式只表达「扁平 all 条件列表」**；`any` / `not` / 嵌套组一律降到高级模式只读提示，
   **绝不压平**——压平会改变判定 → 误发徽章。
2. **`initialMode` 是首次渲染种子**，不随 prop / 规则变化；误用只降级视图，不改数据。
3. **叶子根规则的行内错误**：迁移后现网徽章（如 `regular-visitor`）根节点本身是
   `{root:{type:'activity.count',…}}`，此时 `error.path` 为 `[]`；仅当本规则是叶子根时映射到第 0 行，
   其余空路径错误走通用提示。

## Next

- 二轮改动**尚未提交**（工作树：新增 6 文件、修改 16 文件，均在 `apps/web`），提交时按
  `feat(web)` 一笔带上，勿与规则引擎 M1–M3 的收口提交混在一起。
- 下次动编辑器先读模块笔记该节的两条禁令：新增参数类型只加在 `ParamField`；不要在任何调用方
  另起条件链。

## Open Questions

1. **390×844 实机未验**（当次载体不支持视口模拟）；后续 **340px 是行宽模拟**，只证明行内几何，窄屏与键盘可达性仍是结构级证据。
2. **`fact.count` 预览句子不带计量单位**：单位在 `factCandidates`（`UnitZh/UnitEn`），未注入句子模块。
3. **三处同源手抄的指标清单**（`AUTO_SUMMARY_METRICS`、i18n 测试常量、服务端指标表）待收敛到单一来源。
4. **`normalizeConditionsPayload` 会静默丢弃非字符串 `options`**：后端若真改成对象型，表现为空下拉
   而不是显式报错；唯一兜底是 OpenAPI + 测试。

## 验证

- 门禁：全量 `bun test` **2606 pass / 0 fail**（263 文件；视觉修复轮由 2603 → 2606）、`tests/admin/` **1196 pass**、
  `bun run typecheck` exit 0、`node tests/validate-architecture-boundaries.mjs` **passed**（2175 生产文件）。
- 终审（小审）做了变异验证：重建「另起链 + 兜底文本框」→ **3 fail**；把 `options` 当对象消费 →
  **6 fail**；还原后 `sha256sum -c` 全 OK。task-6 又对 7 条新断言做了「改坏 → 红 → 还原」。
- 真实 dev server（`:3000` + `:8080`）渲染 QA：简单模式「累计访问天数 ≥ 7」、指标下拉 6 项全中文
  （主题数 / 回复数 / 获赞数 / 送出赞 / 收藏数 / 访问天数）、指标与时间窗口各 **1** 个控件、
  无 `[object Object]`、模式来回切换规则 JSON **字节稳定**（无归一化）、桌面无横向溢出。
- 视觉修复的结构锁 **3 条**（单位与 input 同父、`days_visited` 无单位、删除按钮在卡片头）
  + **4 条反向确认**（含「单位不是字段的直接子元素」、`.sf-simple-rule__controls` 内无删除按钮）。
  **340px 行宽**实测：控件按「条件 → 比较 / 阈值 → 指标 / 时间窗口」三行落、行内
  `scrollWidth == clientWidth`、换行前后删除按钮 y 不变。**340px 是行宽模拟，不是 390×844 视口**。
