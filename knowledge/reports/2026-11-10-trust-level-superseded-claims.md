# 信任等级视觉规范 — 已作废结论清单（四轮冻结的修订记录）

Date: 2026-11-10
Author: designer（小设）
用途: 供 writer 合并进 `knowledge/modules/trust-levels.md` 的 Known Residuals / 修订记录。
说明: 每条一行，只记「**哪些说法已经作废**」，以及作废理由与可复核的证据位置。
      规范正文 `knowledge/reports/2026-11-10-trust-level-visual-spec.md` 是终稿（第四次冻结），
      本文件是它的变更底账 —— 读到旧版规范或旧讨论时，以本清单为准。

---

## A. 我自己写错、后来推翻的（设计侧）

1. **「`runRestore` 会弹既有确认框」** —— 错。`runRestore` 直接 `apply()` + success Toast，无确认步骤；
   因此 `UAlert` 的「恢复默认不会影响用户已获得的等级」成为 D12 的**唯一**披露点，禁止改写成 Toast。
   证据：`apps/web/app/composables/settings/useSettingsSection.ts:60-68`。
2. **`__maxed` 的 `margin:0`** —— 错。与档位名零间距会被读成同一段文本；改 `margin:10px 0 0`（与进度块同高，替换时不跳动）。
   证据：`apps/web/app/components/profile/SFTrustLevelCard.vue:270-272`。
3. **「窄屏 2 列 + 末项 `span 2`」** —— 在 6 个指标下错。6 是偶数，`2×6` 整除；硬跨 2 会造出
   `(1,2)(3,4)(5,·)(6跨2)` 的半格空洞。规则改为**指标数为奇数时才启用 `span 2`**。
4. **「settings 子 tab 不进 V3 routes/UI-surface 目录」** —— 错。新增**组件**必须有 reviewed identity，
   否则 `scripts/v3-catalog/generate.mjs:793` 直接抛错；登记点由 6 处改为 **8 处**（+2 条 catalog identity）。
   同时保留原结论的另一半：`SFAdminFixedTabNav` 本身不需要注册。
5. **「阈值是一指标一键 `trust.level.{level}.{metric}`」** —— 错。实际是**单个 JSON 选项键 `trust.levels`**，
   值为 `{ schemaVersion, levels: [{ levelSlug, requirements, limits, earnable }] }`。
   证据：`apps/api/app/Models/TrustLevels/config.go:23-36`、`apps/web/app/composables/admin/settings/useTrustLevelsThresholds.ts:12`。
6. **指标表里的 `account_age_days`** —— 该字段不存在。`Requirements` 只有 6 个指标，账龄只保留
   `trust.new_user_days` 一个上限闸（`Policy.NewUserDays`）。证据：`apps/api/app/Models/TrustLevels/types.go:78-85`。
7. **键数 36 → 41 → 39** —— 中间值作废。终稿 39 = 原 36 + 新增 5（`progress.text`、`likeGiven`×2、`bookmarkCount`×2）
   − 删除 2（`metric.accountAge`×2）。
8. **断点算式按 5 项、再按 7 项算过两版** —— 均作废。按 6 项重算：`6` / `3+3` / `2+2+2`，每档零空格。
9. **TL1 显示名两度变更** —— 「成员」→「正式成员」→ **「正式用户」**（终稿整组带「用户」：新用户/正式用户/常驻用户/资深用户/领袖）。
   slug 始终是 `member`，未动。

## B. 指令与实现被推翻、后经实测纠正的

10. **「指标 7 个」** —— 错，实际 6 个（多算了 `account_age_days`）。证据：`types.go:78-85` 的 `Requirements` 六字段。
11. **「i18n 审查断言看 `roles.*`」** —— 该命名空间不存在。实际是 `admin.roleCatalog.*`，
    且产品文案把「角色」叫**用户组**（`admin.nav.roles` =「用户组」）。已进计划 D21「设计侧实测纠正」。
12. **后台桌面网格「5 列 → 7 列」** —— 两版都作废，终稿 6 列。
    核查时实现曾是 `repeat(5, …)`，与规范（当时写 7）、与真实契约（6 个指标）三边不一致；
    唯一会说话的是 `DisallowUnknownFields`（保存被拒）。**已修**：2026-11-10 复核为
    `repeat(2, …)` / `repeat(3, …)` / `repeat(6, …)`（`SFAdminTrustLevelsTab.vue:285/296/307`），三边一致。
13. **`adminSurfaceCatalog.test.ts` 计数 48→49 曾疑似「新功能多算一条」** —— 不是。那 +1 是徽章页；
    信任等级 tab 是**设置页内组件**不是 page，正确做法就是不进 page 级 placements，只进 `ui-surfaces`。
    证据：`node scripts/v3-catalog/generate.mjs --check` → `V3 P0 catalogs are current (18 files)`。

## C. Go 侧契约/注释漂移（**截至 2026-11-10 已关闭**；本清单只记「哪个说法作废」）

> 三条均由 backend 修完并经设计侧复核。**这是带日期的复核结论，不是永久保证**：
> 若有人回退 C 组任一修复，本条重新生效，下面的零命中会立刻变回命中。
> 复核命令（可重跑，任何时刻都能验证当前状态）：
>
> ```bash
> grep -rn "regular" apps/api/app/Models/TrustLevels/          # 期望：零命中
> grep -rn "NameKey\|DescriptionKey" apps/api/app/Models/TrustLevels/   # 期望：零命中
> grep -n  "repeat(" apps/web/app/components/admin/settings/site/tabs/SFAdminTrustLevelsTab.vue  # 期望：2/3/6 列
> ```
>
> 模块笔记里可删（它是给人读的「现在怎么做的」），此处留档只为「别再写回去」。

14. **`config.go:30` 注释曾称 slug 为 `newcomer/member/regular/veteran/leader`** —— `regular` 从未存在，
    实现是 `resident`（`types.go:33-38` 的 `nameSlugs`）。**已修**：注释同步为 `resident`，全包零命中。
    **字面量使用边界**：本条引用的旧标识**不应出现在任何代码或文档中**；本底账是唯一允许出现它的地方
    （反引号内 + 紧跟「从未存在」的否定句），因为一条被推翻的声明必须可被逐字引用才能被反驳。
    模块笔记按「曾把 TL2 的 slug 写成另一个词」处理，是正确取舍。
15. **`types.go:88` 注释曾称 options 键后缀为 `trust.level.<n>.<metric>`** —— 与单键 `trust.levels` + `levels[]` 不符。
    **已修**：注释明写该方案作废。原稿提到的 `ThresholdOptionKey` 曾存在、**已于 2026-11-10 删除**
    （见第 17 条的时序说明），所以「表单字段名 / 契约字段名由某个 Key 函数生成」这句话**现在**不成立 ——
    前端表单字段名与契约字段名各自字面书写，后端只认单键 JSON。
16. **`types.go:41` 的 `NameKey()` 曾是无人消费的死生成器** —— 返回 `trust.level.<slug>.name`，
    而前端用 `profile.trustLevel.name.*`，`trust.level.*` 在两份 locale 里零命中。
    唯一「消费者」是 `registry_test.go:146` 的自证式断言（断言自己的前缀），证明不了任何事。
    **已修**：`NameKey()` / `DescriptionKey()` 与该条测试均已删除（零命中复核）。
    纪律保留：**自证式断言不算断言**（计划 §14 第 4 条）。
17. **`ThresholdOptionKey()` 的真实历史（本条本身是「存在性断言必须带时间」的活标本）**：
    - 它**曾存在**于 `apps/api/app/Models/TrustLevels/policy.go:65`（注释在 `:61`，用途「后台表单字段名」），
      当时确为零调用者，据此判「删」并由 backend 执行。
    - **截至 2026-11-10 20:2x 复核**：`grep -rn "ThresholdOptionKey"`（`.go/.ts/.vue/.json`，排除 node_modules）
      **零命中**，`policy.go` 内除 `DefaultPolicy()` 外已无函数 —— 即**已删除**，不是「从未存在」。
    - 所以「它是第三个死函数」这句在**指令发出时**为真、在**我复核时**已过期；两个观察都对，
      错在传播时没带时间。**本清单任何「存在/不存在」的结论都按此条口径理解：带命令、带日期。**
    - 判据保留并用对：**有生成器 ≠ 有契约，看消费者不看函数名** —— 先确认函数此刻存在，再确认谁在调用它。
    顺带把相邻一个容易误判的也钉住：`EligiblePermissions()`（`registry.go:122`）**没有生产消费者**
    （唯一调用点是 `registry_test.go:44`），但它的测试断言的是**字面白名单集合**（与实现不同路径），
    且有真实事故背景（旧实现是负面过滤 + `.override` 后缀表），所以它**不是死函数、不能按第 16 条删**。
    结论：无生产消费者 ≠ 可删；要看「测试是否走了不同路径」和「是否守着一段事故史」。

## D. 明确不做（不是作废，是边界，避免下一个人重新提）

17. **等级卡放徽章 tab** —— 否。等级是身份元数据，徽章只展示不给能力；混放会误导。落右栏（每 tab 可见）。
18. **后台新开独立页 `/control-panel/trust-levels`** —— 否。需要第二套页头/工具栏，且改 V3 目录计数，收益为零。
19. **后台画能力解锁勾选矩阵** —— 否。M4 只冻结阈值读写；能力映射未定稿前画矩阵就是第二套真相。
20. **在「`L<4` 且下一档全阈值 0」时写「已达最高等级」或「继续活跃即可升级」** —— 否。
    前者是错的，后者是编的；等 M4 暴露「被上限闸压住」的信号再写 `nextHint`。
