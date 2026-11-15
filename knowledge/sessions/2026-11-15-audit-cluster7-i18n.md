# 2026-11-15 审计簇 7 收口（i18n 齐活）

簇 7 全簇 **5 条**（RV-09 / RV-10 / NF-03 / UX2-19 / RV-28）一次落地。总表口径：
有效待修池 **102**，已修 **98**，剩余 **4 = P2 2 / P3 2**（P1 0；EX2-01 / RV-03 仍计
部分完成、未减号）。剩余明细：P2 = EX2-01 / RV-03（架构改动，单独排）；P3 = RV-46 /
RV-54（散点，Go + 测试 + `.env` 权限）。

## 口径纠偏（本轮最重要的一条）

审计写的三个数字**都不可复现**，本轮改成可复现口径，后续按新口径引用：

| 条目 | 审计数 | 实测 | 差异原因 |
|---|---|---|---|
| RV-28 | 21 处 / 8 文件 | **55 处 / 12 文件** | 审计自身路径清单已 23/10；漏了 `SFEditor.vue` 整文件（9）、`SFEditorToolbar` 只数模板属性（漏 16 条脚本标签 + `<option>`）、`SFFeedRow` 插值相邻文本（3）、`SFSearch` 默认 placeholder、登录/注册中文兜底、`SFTabs` |
| NF-03 | 15 个缺键 | **3 个** | 12 个是 `components/admin/rules/*` 的**前缀化短键**（`t = rawT(\`${props.i18nKeyPrefix}.${key}\`)`），真实键在 `admin.badges.rule` / `admin.forum.trustLevels.rule` 两族里都存在；照字面判缺会造出 12 个死键 |
| RV-10 | en 缺 28 键 | **28 键** | 与审计一致；HEAD 上 zh-only 全部且仅有 `userAppearanceSettings.*` |

RV-28 的扫描口径（可复现）：`@vue/compiler-sfc` 解析模板 AST + 剥注释后的 script，统计模板
文本节点、静态属性、script 字符串字面量；排除注释/style/import、dev-only
`pages/components.vue`、以及零依赖兜底 `errors/SFSystemErrorBootstrapFallback.vue`（i18n 插件
自身失败时不能用 `useI18n`，注释里已写明）。扣掉「只被 demo 使用」与「t() 命中后的死分支」，
生产可渲染子集 **45 处**，仍远大于审计的 21。

## Changed

- RV-09：两侧补 `common.cancel` / `common.more`（消费端本就引用这两个键，界面曾直接显示键名）。
- RV-10：en-US 补 `userAppearanceSettings.*` **28 键**真英文；该族在英文侧此前整族缺失。
- NF-03：只补真实缺失的 3 键（`common.cancel` / `common.more` /
  `admin.extensions.contributions.governance.disabledBadge`），**没有**臆造 `admin.rules.*`。
- UX2-19：`pages/admin/site-chrome.vue`、`pages/admin/settings/notifications.vue` 的
  `Redirecting…` 改 `t('common.redirecting')`，全仓 `grep Redirecting app/` 归零。
- RV-28：12 个前台组件清零硬编码中文（`SFEditor` / `SFEditorToolbar` / `SFFeedRow` /
  `SFPagination` / `SFSearch` / `SFAlert` / `SFToast` / `SFAvatar` / `SFTabs` / `SFComment` /
  `SFLoginFormPage` / `SFRegisterFormPage`）。新增 `common.dismissAlert|dismissNotification|
  anonymousUser|sectionTabs|redirecting|pagination.*`、`search.placeholder|filtersLabel`、
  `forum.feed.*`、`composer.editor.*`、`auth.loginOrRegister`。
- 测试面：新增 `tests/helpers/locale.ts`（读真 locale 的 `t`，缺键抛错）、
  `tests/framework/i18nCatalog.test.ts`（键集相等 + 字面引用 0 缺失 + 前缀化短键双宿主 +
  动态前缀命名空间）、`tests/framework/sharedComponentI18n.test.ts`（真 locale 挂载断言
  aria-label / placeholder / 可见文案，en 分支零 CJK；SFEditor 挂真 Tiptap）。
  6 个既有测试从「字面中文」改为「真 translator 翻出来的同一句话」，未删断言。

## Decisions

- **组件默认文案不能在 `withDefaults` 里求值**：`t()` 只在 setup 内可用，故
  `SFEditor` / `SFSearch` / `SFAvatar` / `SFTabs` / `SFComment` 的默认值改 `undefined`，
  组件内用 `computed` 兜底；`??` 而非 `||`，显式传空串仍优先（全仓目前无人传空串）。
  注意：`SFComment.actions` 的显式 `[]`（零按钮空态）与默认 `undefined`（一个有回复的按钮）
  必须可区分——真正的因果是「默认值由 `() => [...]` 改成 `undefined`」，不是 `??`/`||`
  的差别（`[]` 恒为真值，两者等价）。
- **不把审计的 `admin.rules.*` 当命名空间造键**：前缀化短键的宿主域由 prop 决定，造死键会
  让「键存在但永远不被渲染」成为新的噪声来源。反例保护写进棘轮：短键必须在**每一个**已知
  宿主族里都能查到。
- **i18n 棘轮必须能证伪**：三条常驻断言都带反真空绿下界（键集 >6000、引用面 >4000、前缀
  >100、短键 ≥30），避免正则失配后变成永远通过。变异验证由实现方 7 次 + 小审 8 次 + Lead
  2 次独立完成（删键 / 改名 / 引用不存在键 / 写回硬编码），均 `sha256sum -c` 复原一致。
- **`SFSystemErrorBootstrapFallback` 保留中文**：它是 i18n 插件自身失败时的零依赖兜底，
  引入 `useI18n` 会在启动期二次崩溃——属正确取舍，不是漏改。

## 行为变更（进 v3.1.0 说明时无需单列，但要知道）

- 英文界面首次有 `userAppearanceSettings` 整族文案；此前英文外观设置页显示键路径。
- `SFAlert` / `SFToast` / `SFFeedRow` 的关闭与投票按钮新增 `:title`（与既有按钮习惯一致），
  键盘与读屏名称不变。
- `SFEditor` 的 footer 计数改为插值键（`{count} 字` / `{count} characters`），
  `maxCharacters` 后缀仍拼在计数里（`12 / 12000 characters`）。

## Next

- 散点 RV-46（`AdminOverview` 7 条路由只有 `admin.access` 粗门）+ RV-54（5 控制器 0 条
  denied-path 测试、`.env` 0664→0600）一轮做完，与前端零交集。
- 簇 12 尾巴（EX2-01 内核级隔离 / RV-03 core-only 裁决）是架构改动，单独排。
- 总表 §11 新轮次与顶部进度表（93 → 98、剩余 9 → 4）待提交后补，需要 commit hash。

## Open Questions

- **同类残留（审计口径外，未修）**：`utils/editor/shortcodes.ts:59-66,499,503` 的
  `fallbackLabel` 会直接渲染在编辑器/正文（英文界面仍见「用户引用暂不可用」）；
  `utils/sfEditor.ts:202-207` 的 emoji `label` 进 `title`/`data-label`。要不要把 RV-28 的
  「前台硬编码中文」口径扩到 utils 层？扩了就要重新数一遍（本轮只覆盖 `components/**` 与两
  个重定向壳）。
- `i18nCatalog.test.ts` 的 `RULE_HOST_PREFIXES` 是手写两族，新增宿主族会静默逃逸；要不要
  改成从 `i18nKeyPrefix="..."` 用法处自动枚举？
- `common.cancel` 与 20 余条领域 `*.cancel` 重复，`common.more` 与 `nav.more` 同文案：RV-09
  修法合理，但后续要不要收敛成一族？
- `SFAvatar` 传 `name=''`、`SFTabs` 传 `ariaLabel=''` 会回退到默认文案（当前全仓无人传空串）；
  要不要改成「空串 = 空」而不是回退？
