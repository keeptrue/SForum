# 2026-11-11 信任等级：色相单源渲染 + 后台 IA 归位（D-C6 / D-C7）

状态：**accepted（2026-11-11，独立验收 PASS）**
承接：`2026-11-11-trust-level-tier-color-and-delete.md`（D-C1 / D-C5 定了 9 色白名单与「空串 = 跟随位次」）。
本文件只裁决两件渲染层的事：**色怎么到渲染面**、**后台入口挂在哪个文件夹**。
证据：`storage/qa/trust-level-color-unify/`（`REPORT.md` + 截图 + `evidence/qa-*.json`）。

## 病根（先读这一条，否则下一个人会原地重踩）

CSS 自定义属性**在更深一层声明就会遮蔽祖先的内联值**，与选择器优先级无关 —— 只要子元素自己声明了
`--tl-accent`，它就不再继承祖先那一份。改动前四个后台分区各自在**图标 span** 上写
`--tl-accent: var(--sf-accent)`（暗色再补 `.dark … { --tl-accent: var(--sf-accent-dark) }`），
于是不论档位配了什么色，图标块永远显示站点主色；淡底统一用 `--sf-accent-soft`，一页排开分不出层级。
资料页卡同病（`SFTrustLevelCard.vue` 卡面同款声明）。

**同一类问题的第二种形态：陈旧的设计规则会让配置项静默失效。** 共享块最初照抄了旧「强度阶梯」的
`[data-step="1"] { color: var(--sf-fg-secondary) }` —— 那套阶梯服务的是「图形本身没有档位色、只靠深浅
区分」的年代，第一档故意不着色。色相改成按档位派生之后，这条规则就把 **rank 1 的档位色整个吃掉**：
给 TL1 配任何颜色都无效，而其余四档正常。修因见下（D-C6 的 step 1 口径）。两条病根的共同点：
**规则没有跟着产品语义重写时，不会报错，只会让配置项失效** —— 所以验收必须「配一个非默认值再看渲染」，
不能只看代码里有没有那个变量。

## D-C6 色相唯一来源

- **唯一来源**：`tierColorOf(tier, rank)`（`apps/web/app/utils/profile/trustLevelColors.ts`）→
  `tierAccentStyle()` 注入**行根**的内联 `--tl-accent: var(--sf-tl-<token>)`；token 取值在
  `apps/web/app/assets/css/sforum-trust-colors.css`（`:root` / `.dark` 各一套，9 色）。
- **档位图形一律走共享 `.sf-tl-mark`**（同文件，`data-step` 给形状）。共享块内**禁止**声明
  `--tl-accent`，兜底只能写 `var(--tl-accent, var(--sf-tl-slate))`；兜底取 slate token 而不是站点
  accent —— 站点 accent 随主题变，档位色不该随主题变。
- **淡底口径**：`color-mix(in srgb, var(--tl-accent) var(--tl-accent-tint), var(--sf-card))`，
  `--tl-accent-tint` 默认 12%，`[data-retired="true"]` 6%（暗色 8%），与
  `SFAdminTrustLevelTierRow.vue` 的色条口径一致。**禁止再用 `--sf-accent-soft` 当档位淡底**。
  无 `color-mix` 的浏览器停在基础中性态（阶梯规则整体 `@supports` 包住），不写退化渐变。
- **形状梯度保留，但强度从第一档就带色相**（第二轴，与色相独立）：
  `data-step` 1 = `color-mix(--tl-accent 34%, --sf-border)` 环 + **透明底** + 档位色字形
  （色相已在，只不着底）／ 2 = `1px` 34% 环 + 12% 淡底 ／ 3 = `2px` 55% 环 + 12% 淡底 ／
  4 = 实心 + 反色字（`color: var(--sf-card)`）；`data-level="0"` 虚线。
  环线权重（1px / 2px / 实心）与视觉规范 §2.2 一致，改共享块不许顺手改权重。
  没有 `color-mix` 的浏览器整段阶梯不生效，退回 `.sf-tl-mark` 基础中性态（可读、无色）。
- **尺寸留在分区**：共享块只给几何与配色，`width/height` 与圆角由各分区给
  （`--sf-tl-mark-radius`：后台 20/24px，资料页 28px）。
- **`data-step` 属于图标块自己**：资料页卡原来把 `data-step` 放在卡根，改后移到
  `.sf-tl-mark` 上 —— 形状是「图标块」的属性，不是卡片的属性；`data-level="0"` 的虚线选择器两处都保留。

被否：

1. 每个分区继续各写一份 —— 复制必漂移，遮蔽复发一次就要再查六处；
2. 用 `@property` 或 `!important` 解决 —— `@property` 改的是继承/初始值语义，不解决「子层声明遮蔽祖先」；
   `!important` 对自定义属性不生效；
3. 把色写进 Tailwind 任意值类 —— 失去 9 色白名单与明暗两套 token 的单点取值。

## D-C7 后台 IA 归位（只搬菜单）

- `apps/web/app/config/adminModules.ts` 的 `/forum/trust-levels` 从「论坛管理」移到
  「用户/权限」，排在**徽章之后**。
- **只搬菜单**：页面 id、路由 `/control-panel/forum/trust-levels`、`AdminForumTrustLevels`
  组件名、V3 reviewed identity、API 路径、`trust.manage` 门控**全部不变**。
- 理由一（先例）：B-28 已把「徽章」从论坛域移到「用户与权限」，判据是「主体是用户还是内容」。
  信任等级调的是用户的信任度（阈值/限额/能力解锁都是用户属性），不是版块或论坛设置。
- 理由二（身份稳定性）：扩展 admin surface 的 `targetId` 绑在页面身份上。改路由要退役旧身份并重生成
  V3 目录（`scripts/v3-catalog/generate.mjs` 会因缺 identity 直接抛错），已安装扩展的 target 随之断开；
  搬菜单不改身份，零迁移。
- 守卫：`tests/validate-admin-framework.ts` 断言「论坛文件夹不含它」+「恰好一个文件夹含它」；
  `apps/web/tests/admin/adminForum.test.ts` 断言「用户/权限」children 顺序与唯一入口。

被否：把路由改成 `/control-panel/trust-levels`（或 `/trust-levels`）—— 语义更干净，但要退役旧页面身份 +
重生成目录 + 迁移扩展 target，收益只是 URL 观感，本刀不做。

## 顺带收口（同一刀，不是新决策）

分类/标签图标色从 4 处内联副本收敛到 `apps/web/app/utils/forum/forumTaxonomy.ts` 的
`taxonomyIconColor()` / `taxonomyColorInputValue()`（管理台与公开页共用一份；空串或非法值回落站点
accent / 推荐色 `#0f766e`）。与原副本行为等价；新增 `.trim()` 只把「全空格串」判为未配置，无害。
