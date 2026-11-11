# 2026-11-11 信任等级：档位颜色 + 删除墓碑（N 档刀的色彩与减档补齐）

Status: **accepted**（2026-11-11，Lead 批准；后端 / 前端 / 验收已落地）
计划：无独立计划文件。本刀是 `../plans/2026-11-11-trust-levels-n-tiers.md` 的下游补齐，
上游 D-N1–D-N15 一个字未改（本文件是**追加**决策，条目编号用 D-C 前缀以免与 D-N 撞号）。
上游：`2026-11-11-trust-levels-n-tiers.md`（D-N1–D-N15，N 档模型、id 永不复用、软停用）、
设计源 `../reports/2026-11-11-trust-tier-color-palette.md`（**§9 是现行 9 色实测，§6 的 8 色块已作废**）。

> **编号现状（2026-11-11 收尾后复核）**：本文件用 D-C1..D-C5 作正本编号。
> `apps/web/**` 的注释与 describe 名已全部统一到 `D-C1`（颜色）/ `D-C5`（CSS 变量侧）/
> `D-C2`（删除闸门）/ `D-C3`（墓碑），`rg -n 'D-N1[56]' apps/web/app apps/web/tests apps/web/nuxt.config.ts`
> **无输出**。现树里只剩 **Go 侧**在「恢复默认整份覆盖」语境内合法引用 `D-N15`
> （`policy.go:100`、`controller.go:213`），与上游 `2026-11-11-trust-levels-n-tiers.md:270` 同口径。
> 历史映射保留在此以防读到旧分支/旧手稿：D-N15(旧·颜色) → D-C1/D-C5，D-N16(旧·删除) → D-C2/D-C3。

## 问题

N 档落地后每档「有名字、有图标、有阈值、可停用」，但缺两件运营立刻会要的东西：

1. **档位没有识别色。** 资料页等级卡与后台档位行只能靠 `TL{n}` 数字区分；8 档站点上相邻两档
   视觉完全一致，运营在后台点错行的成本由配置本身承担。
2. **档位只增不减。** 软停用（D-N8）保留配置、跳过判定，但误建的档位会永久占着列表位置与
   `tier_<n>` 序号。运营真正想删掉它时，产品没有对应动作。

两条约束把解法夹在中间：

- **颜色不能是自由 hex。** 取值会进 class 与 CSS 变量名（`--sf-tl-<token>`），
  前端必须能静态枚举才有树摇与 Tailwind 扫描；而且运营填错时只会得到「类存在但样式不存在」
  的透明档位（`tier_colors.go:11-13`）。
- **删除不能物理抹掉 id。** D-N6 的「id 永不复用」是配置、审计、契约快照共同依赖的承诺；
  一旦让 `tier_7` 可以被派给另一档，历史记录里的旧 id 会静默指向另一档
  （`config.go:101-105` 的三条墓碑判据就是为这条承诺设的）。

## 决定 D-C1：档位颜色是**可选属性**，空 = 跟随位次；解码期**不填默认**

**裁决**：`tiers[].color` 是 9 色白名单 token（`slate / teal / emerald / sky / indigo / violet /
fuchsia / rose / amber`，`tier_colors.go:21-31`），顺序即默认色序（第 n 项属于 rank n，TL0 = `slate`）。
空串是**合法值**，表示「跟随位次自动」；解码期不写默认值，有效色由 `Policy.ColorOf(rank)` 解析后
才进读面（`types.go:348-367`、`payload.go:34,73`）。后台读面把白名单作为 `colorChoices` 下发
（`dto.go:139-141`、`controller.go:167`），前端不得自己抄一份。
表单兜底 `fallbackTiers()` 把 `color` 置空（`useTrustLevelsForm.ts:102-107`），公开面兜底
`FALLBACK_TIERS` 才带默认色（兜底展示需要色）。

**理由**：填充默认值会把「自动」冻成一次性取值。运营第一次保存 → 每档被写上当时的位次色；
之后上下移动档位，颜色不再跟着位次走 —— 那不是「默认按位次配色」，而是「首次保存时的快照」。
与 `icon` 的「填空默认」故意不同：图标是每档的独立属性，颜色是**位次属性**，语义不对称，
存储策略也不该对称。

**9 色而不是 8 色**：`MaxTiers = 8`，而 `slate` 被虚拟 TL0 占掉，rank 1..8 需要 8 个非 `slate`
token；8 色只提供 7 个，满配时 TL8 会回退到 `slate`，与 TL0 视觉同色 —— 最高权限与最低起点
看起来一样，是功能性缺陷而不是配色偏好（`tier_colors.go:15-18`、报告 §9.1）。
`amber` 专留给顶档（「amber = 终点」）。`registry_test.go:198-244` 用字面量表钉住整套对应关系，
含满配 `ColorOf(MaxLevel) == "amber"` 与 `DefaultTierColor(MaxLevel) == "amber"`。

**被否**：

- 只做前端自动配色、不落配置 —— 运营无法固定某档颜色（例如「TL5 必须是紫色」的品牌要求），
  且前后台各算一次，漂移不可见。
- 自由 hex 输入 —— 深色模式对比度不可控：报告 §9.3 的核验矩阵是逐个 token 验过的，
  连报告自己给的候选 `fuchsia-600 #c026d3` 都因压在 12% 混色底只有 3.93:1 被退到 700 级；
  自由输入等于把这件事交给运气。而且变量名无法静态枚举。

## 决定 D-C2：删除只针对**已保存为停用**的档；两步闸门，且永不改 rank

**裁决**：删除入口只在「该档已经是停用态 **且** 该停用态已随保存落到服务端」时可用：
`canDelete(id) = tier.retired && savedRetiredIds.includes(id)`（`useTrustLevelTierIntents.ts:92-98`），
然后二次确认（墓碑不可恢复）才真正从数组移除并追加墓碑（同文件 `:102-126`）。
删除只从 `tiers[]` 里拿掉一个 **retired** 项，active 档的位次序列不变，因此
`tierStructureChanged()`（`useTrustLevelsForm.ts:722-724`，只比较 active id 序列）不会触发结构确认。

**理由**：删除是破坏性的，但它不改变任何 active 档的判定结果。把「停用」与「删除」分成两次保存，
让运营先看到「这一档停用后谁会掉级」（即 D-N8 的 modal 披露），再决定是否永久移除；
在同一个未保存会话里直接删掉一档则跳过了这条披露。

**被否**：

- 任意档直接删除 —— 等于给「误删 active 配置」开一条无确认路径；active 档的阈值/限额/earnable
  只能靠恢复默认或手工重配找回。
- 「停用未保存 → 删除 → 保存」也允许 —— 这个组合会在一次保存里同时完成「停用」和「移除」，
  运营从未见过停用会产生什么后果；两步闸门正是为堵这条绕过。

## 决定 D-C3：删除留 id 墓碑（`deletedTierIds`），`NextTierSeq` 计入

**裁决**：墓碑是与 `tiers[]` 同层的顶层数组 `deletedTierIds`（按提交顺序，`omitempty`，
`config.go:166-182`）；解码期逐条校验三条判据 —— 语法/身份不合法（含保留给 TL0 的 `newcomer`）、
重复、与 `tiers` 里仍在的档位冲突 —— 任一命中整份 422，**不去重静默接受**
（`config.go:810-856`）。`Policy.NextTierSeq()` 同时扫描 `Tiers` 与 `DeletedTierIDs` 的数字后缀
（`types.go:404-417`），因此删掉的 `tier_7` 仍占位，下一个新档是 `tier_8`。
前端 `cloneTrustLevelsForm` 也不落空数组键：`{deletedTierIds: []}` 与「没有墓碑」是同一件事。

**理由**：墓碑只存 id、不存配置，成本是一个字符串数组；它保住的是 D-N6 的跨系统承诺。

**补强：恢复默认也保留墓碑（本刀实现时补的，原 D-C3 只说了 `NextTierSeq`）。**
「恢复默认」是整份覆盖（D-N15），若它按 `DefaultPolicy()` 纯覆盖，就会把墓碑一起抹掉 ——
这个按钮于是悄悄打破同一份契约里的「id 永不复用」。
落地形态：领域层 `RestoreDefaults(previous Policy) Policy`（`policy.go:98-130`），
墓碑 = 「既有墓碑 ∪ 本次被移除的 `tier_<n>` 档位 id」的保序并集；**内置档位 id 不进墓碑**
（`NextTierSeq` 基准是 `len(builtinTierIdentities)=5`，内置四档即使被移除也不会被复用，
留墓碑只是噪声）。控制器 `restoreDefaults` 改为「读 previous → `RestoreDefaults` → `EncodeConfig` → save」，
读失败 **fail-open** 到纯推荐值并 `slog.Warn`（`controller.go:208-231`）—— 配置已经坏掉的站点
更需要这个按钮可用。读面 `recommendedConfig()` 仍是纯 `DefaultPolicy()`（`controller.go:345-351`）：
**推荐值 ≠ 恢复动作**，前者描述「推荐配置长什么样」，后者必须携带历史墓碑。
测试：`restore_defaults_test.go`（保留既有墓碑、把被移除的自建档位转墓碑、编码→解码→新建档、
无 previous 时纯推荐值且不写 `deletedTierIds`）+ `controller_restore_test.go`。

**被否**：允许 id 复用（删掉即可重新派发）—— 历史审计、契约快照、运营文档里的旧 id 会静默
指向另一档，而且没有任何一处报错。

## 决定 D-C4：回滚口径 = 新字段一律 `omitempty`，回滚要**同时**回退值

**裁决**：`color` 与 `deletedTierIds` 都是 `omitempty`（`config.go:26-30,193-196,170-175`）。
没用过新能力的站点，写出的值与引入这两个字段之前**逐字节相同**，上一版二进制仍能正常解码。
一旦运营真的选过颜色或删过档，旧二进制会在 `DisallowUnknownFields` 上整份解码失败；
此时回滚必须**同时**回退 `trust.levels` 的值（恢复成旧二进制认识的形状）。

**理由**：这是**数据形状的单项升级，不是缓存问题**。漂移不会自己消失，重启也不解决；
把「换回旧镜像」当成完整回滚步骤，会得到「等级判定退回未解析而静默失效」——静默失效比报错更贵。

**被否**：把新字段藏进第二把 options 键 —— 多一处注册点就多一个静默丢弃口，且导出/导入、
契约快照都要处理两条路径（与 D-N2 否掉「档位与元数据分存」同一理由）。

## 决定 D-C5：档位色用 CSS 变量承载，与站点主题 accent 解耦

**裁决**：唯一取值来源是 `apps/web/app/assets/css/sforum-trust-colors.css` 的
`--sf-tl-<token>`（`:root` / `.dark` 各一套，9 色）；组件只写 `--tl-accent: var(--sf-tl-<token>)`
（`utils/profile/trustLevelColors.ts:68-78` 的 `tierColorVar` / `tierAccentStyle`），
后台档位行与前台信任卡共用同一套 token。色条的混合比例由组件自己的 `--tl-accent-strength`
控制，色名与强度互不耦合。

**理由**：档位颜色表达「这一档的识别色」，不参与任何主题变量推导。换主题不该让档位色跟着漂，
改档位色也不该影响站点 accent。单一映射 + 单一注入点，避免后台与前台各写一份字面量。

**被否**：Tailwind 字面类映射（`bg-teal-600` 等）—— 后台档位行与前台信任卡两处各写一份，
新增一色要改两处，且拿不到 `.dark` 下的替换值（报告给的是明暗两套 hex）。

## D-N8 修订：停用披露从「页面级常驻 UAlert」改为「确认 modal + 行内常驻徽标」

**原裁决**（`../plans/2026-11-11-trust-levels-n-tiers.md:59`）：停用会撤回该档贡献的 `earnable`
（可能降权），保存前必须二次确认 + **常驻披露**，沿用现有 `UAlert` 披露点纪律（不放 Toast）。

**修订**：披露拆成两处，都不放 Toast：

1. **确认那一刻**：阻塞式 modal 承担后果披露（`pages/admin/forum/trust-levels.vue:290-300`），
   逐项列出可能被撤回的权限键，或明确「其它启用档位已覆盖该档的全部解锁，预计不会撤回任何能力」。
2. **停用之后**：已停用行内挂**常驻徽标**「已撤回 {count} 项解锁」
   （`SFAdminTrustLevelTiersTab.vue:126-130,217-228`，i18n `admin.forum.trustLevels.tiers.retiredImpact`）。
   列表底部另有墓碑脚注（`tiers.tombstones`）：已删 id 永不复用。

**为什么信息不丢**：页面顶部那条常驻 UAlert 在列表第 6 档按下停用时早已滚出视口 ——
披露在物理上不可见等于没有。徽标挂在**那一行**上，由数据派生
（`retiredTierImpactKeys(tiers, id)` 现场计算，不存字段），跨刷新仍在；0 项时不渲染，避免噪声。
Toast 只报保存结果，不承担状态。

**被否**：保留顶部 UAlert 并再加一行说明 —— 两处「同级」披露会让运营以为其中一处是过期残留，
且滚出视口的问题依旧。

## 实施记录（本刀落地时的实测口径）

- 领域层：`tier_colors.go`（白名单 + 默认色序 + `DefaultTierColor`）、`types.go` 的 `Color` /
  `ColorOf` / `NextTierSeq`、`policy.go` 的 `RestoreDefaults`、`config.go` 的 color 校验与
  `validateDeletedTierIDs`。
- 后台 HTTP 面：`payload.go`（读面下发的 `color` 已解析）、`dto.go`（`colorChoices`）、
  `controller.go`（`restoreDefaults` 读 previous 后覆盖）。
- 契约：`contracts/openapi/schemas/trust_levels.yaml`（`color` 可选 + 白名单说明、`colorChoices`、
  `deletedTierIds`、`reasonCode` enum 增 4 条）、`paths/trust_levels.yaml`（恢复默认保留墓碑的说明）。
- 前端：`useTrustLevelsTiers.ts`（颜色白名单 + 墓碑 id）、`useTrustLevelTierIntents.ts`（停用/删除两步闸门）、
  `useTrustLevelsForm.ts`（`fallbackTiers` 置空色、墓碑不落空键）、
  `SFAdminTrustLevelColorPicker.vue`、`tabs/SFAdminTrustLevelTiersTab.vue`、
  `SFAdminTrustLevelTierConfirmDialog.vue`、`sforum-trust-colors.css`、`SFTrustLevelCard.vue`。
- 颜色取值与对比度核验：`../reports/2026-11-11-trust-tier-color-palette.md` §9（9 色，明暗两套 hex，
  含 fuchsia 的插入理由与相邻可辨性）。**§6 的 8 色块作废**，不要照抄。
