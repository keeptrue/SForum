> **已作废（2026-11-14）**：本文关于后台 i18n 键族的说法错误。真实族是 `admin.forum.trustLevels.*`（394 叶键），`admin.settings.trustLevels.*` 在两份 locale 里均为 `undefined`。见 `../modules/trust-levels.md` §命名与 i18n 三条硬约定。

# 信任等级 — 文档 vs 代码 对拍清单（2026-11-10）

Owner: pm（阿策）
Scope: `knowledge/modules/trust-levels.md`、`knowledge/sessions/2026-11-10-trust-levels.md`、`knowledge/index.md` 信任等级节、`docs/{zh-CN,en-US}/usage/trust-levels.md`
Method: 逐项在真实代码上复核（`apps/api/**`、`apps/web/**`）。**未改任何代码或计划**，只出清单。
Status: 5 条 P0（会误导运营与验收）+ 4 条 P1（数字/键名/行号）+ 3 条 P2（可更新）+ 已核实一致 10 条 + 「查过但没有」4 条。
Revision: 2026-11-10 第二版。P0-5 因 M3 落地重写；P1-2 / P2-1 / backend 派修行因「`trust.level.*` 是幻影键族」重写。

判定口径：**以代码为准**。凡文档说「没有 X」，我都在 `apps/api` / `apps/web` 里实测过 X 是否存在。

---

## P0 — 会让运营/验收得出错误结论（建议优先派修）

| # | 文件:行 | 文档说 | 代码是 | 建议改法 |
| --- | --- | --- | --- | --- |
| P0-1 | `modules/trust-levels.md:29`（状态表「**装配未做**」）、`Known Residuals 1` | 「`bootstrap/` 内 `rg "trustlevels\|TrustLevel"` **零命中** → `trustLevels` 恒为 nil，运行时仍是『未解析』」 | **已装配**：`bootstrap/api_assembly_domains.go:321` `identityStore.WithTrustLevelSource(infrastructure.trustLevelSource)`；`bootstrap/api_assembly_extensions.go:476` 同款注入 user-field store；`bootstrap/trust_levels_wiring.go` 提供 `Policy(ctx)`（含 `ErrDisabled`）。 | 把状态表该行改为「装配已落地（`api_assembly_domains.go:321`）」；Residual 1 改为「M1/M2 已落地并装配；M3–M8 未完成」 |
| P0-2 | `modules/trust-levels.md`「Feature Flag And Permissions」节 | 「开关：计划 D9 定了，**代码里还没有**。全仓 `rg '"features\.trust_levels"'` 零命中……D16 的五处注册**一处都没做**，所以 F 组断言现在无法执行」 | **五处全齐**：常量 `Options/feature_flags.go:35`、`optionDefinitions` `:63`、`FeatureFlagCatalog()` `:82`、`featureFlagRecommendedDefaults()` `:97`、结构体字段 `:130` + 读取 `:153`；另有 `service_normalize.go:190` 归一化分支。前端也已消费：`composables/useWebOptions.ts:448`。 | 改为「五处已注册（附行号）；F 组断言现在可执行」 |
| P0-3 | 同上节 | 「后台写权限 `trust.manage`……**迁移里没有该键**（`rg "trust\.manage" apps/api/database` 零命中）、`apps/web` 里零引用，落库链路未闭合」 | **迁移已落地**：`database/migrations/202611100004_trust_manage_permission.sql`（`:17` 插入键、`:23` 授 super_admin、`:30/:32` Down 回收）。seeds：`Identity/seeds.go:84`（常量）、`:144`（catalog 条目）。**前端也已闭合**：`apps/web/app/config/roleTemplates.ts:69` 有 `'trust.manage'`（`:68` 注释指向该迁移） | 改为「落库 + 前端均已闭合」，本条**可关闭** |
| P0-4 | `docs/zh-CN/usage/trust-levels.md:5-9`、`docs/en-US/usage/trust-levels.md:5-12` 顶部状态条 | 「**代码已就位，尚未接线**……装配与产品开关还没做，所以现在调整阈值不会改变任何用户的行为」 | 装配与开关都已落地（P0-1/P0-2 证据）。阈值保存**会**改变等级判定。 | 改为「已接线；M3（新人阶梯收编）未完成 → 等级会重算，但**限额**仍按年龄两态」——这句限定很重要，不能简单改成「全部生效」 |
| P0-5 | 两份运营页「怎么调等级阈值 / Adjusting tier thresholds」段 | 「页面上写着『保存后即时生效』——**这句话在接线完成之前是空的**……保存只写进配置，不会改变任何人的等级、限额或权限」 | **已过期（我上一轮审的是 M3 之前的快照）**。M3 已落地：`trustForActor` → `limitsForActor`（**`Forum/service_trust_limits.go:23`**，按架构门禁抽出的新文件），`newUserLimits:63` / `levelLimits:83` / `resolveLimit:101` 区分「新人只收紧 / 分档可显式覆盖」，`TrustPolicyAdapter.LevelLimits`（`Providers/forum.go:273`）经 `Providers.TrustPolicyAdapter` 接入，未动 `Options.Service`。**等级与限额都已生效**。 | 两份运营页统一改成「等级与限额均已生效」；**不要再拆成两半**，也不保留「这句话是空的」旧措辞 |

---

## P1 — 数字、键名、行号错（不误导结论，但会被验收抓）

| # | 文件:行 | 文档说 | 代码是 | 建议改法 |
| --- | --- | --- | --- | --- |
| P1-1 | `modules/trust-levels.md:64` | 「四档 × **七指标** + 每档限额 ≈ **44** 个值」 | `Requirements` 只有 **6** 个字段（`TrustLevels/types.go:78-85`），`Metrics` 6 项（`:93-100`）。4 档 × 6 指标 + 4×4 限额 = **40** 个值。 | 七→六，44→40 |
| P1-2 | `modules/trust-levels.md:350` | 「i18n 断言：`trustLevels.*` 与 `admin.roleCatalog.*` 不得同词不同义」 | `trustLevels.*` **不是 i18n 键族**（只是 Go 字段名 / 前端 tab 名）。真实键族有两个（逐条走出 locale JSON）：公开侧 `profile.trustLevel.*`、后台侧 `admin.settings.trustLevels.*`。**注意 `trust.level.*` 也不存在**：两个 locale 里零命中，`NameKey()` / `DescriptionKey()` 已被删除。 | 改成 `profile.trustLevel.*` + `admin.settings.trustLevels.*` 两个键族 |
| P1-3 | `modules/trust-levels.md:26` | 「`postgres_store.go:73 WithTrustLevelSource`；`postgres_store_roles.go:217` 读 `s.trustLevels`」 | 实际 `postgres_store.go:72`（注释在 `:67`）；读取在 `postgres_store_roles.go:247`（`if s.trustLevels == nil`），解析在 `:250`。 | 行号订正 |
| P1-4 | `modules/trust-levels.md` Data Model 表 | 把 `accountAge` 与六项计数并列在同一张「输入」表 | `accountAge` **不是** `Requirements` 字段，它是独立闸的输入（`level.go:23-40`：账龄 < `NewUserDays` → 最高 TL1；否则至少 TL1）。它进 `LevelOf` 但**不进阈值配置**。 | 表下加一句「账龄是闸不是阈值，不进 `trust.levels`」 |

---

## P2 — 可以更新（已从「待定」变成「已定」）

| # | 文件:行 | 现状 | 建议 |
| --- | --- | --- | --- |
| P2-1 | `modules/trust-levels.md:366`、`sessions/…` Open Question 5 | 「TL2 slug 待统一到 `resident`……对齐后键名是 `trust.level.resident.*`，届时再写进文档」 | 代码已是 `resident`（`types.go:40-46` `nameSlugs`），但**该键名形式不存在**——真实键是 `profile.trustLevel.name.resident` 与 `admin.settings.trustLevels.level.resident`。 | 写真实键名，OQ5 可关闭 |
| P2-2 | `modules/trust-levels.md:181` | 「计划 §5 的表仍写着初稿名（新成员 / 成员 / …）」 | 计划 §5 已改为 新用户/正式用户/…（`plans/2026-11-10-trust-levels.md:296-297`）。该告警可撤 |
| P2-3 | `sessions/2026-11-10-trust-levels.md` M2 收尾段 | 「三处调用点未更新，`go build ./...` 会失败 / 当前红」 | 装配调用已存在（P0-1）。**我没跑 build**，无法判定编译态 → 建议 backend 跑一次 `go build ./...` 后把该段改为完成记录 |

---

## 已核实一致（不必改，列出来是为了让下个会话不用重查）

| # | 项 | 证据 |
| --- | --- | --- |
| C1 | **`DefaultPolicy()` 与 PM 定稿表逐值一致** | `TrustLevels/policy.go:15-58`：TL1 3/5；TL2 15/2/20/5/3；TL3 45/8/80/30/20/5；TL4 100/25/250/100/80/20。与 `reports/2026-11-10-trust-levels-capability-map.md` §5.1 **完全相同**，无需对齐 |
| C2 | 两份运营页「五档 × **六指标**」 | 与 `Metrics`（`types.go:93-100`）一致 |
| C3 | 「新人限制七项」字面量（首项下划线、其余点分） | 与 `Options/types.go:115-121` 一致 |
| C4 | 「TL1–TL4 今天没有可观测的限额差异」 | 与 `content_limits.go:37-46`（站点默认冷却/日限额均为 0）一致 |
| C5 | 「账龄闸：窗口内最高 TL1」 | 与 `level.go:23-30` 一致 |
| C6 | 「`forbid_attachments` 当前不生效」 | 与 `limitsForActor`（`Forum/service_trust_limits.go:23` 起，只读 `forbid_links`）一致；D20 裁决不接线 |
| C7 | **i18n 真实键族 = 两个**（逐条走出 locale JSON） | 公开侧 `profile.trustLevel.{title,name,code,nextHint,maxed,earned,notEarned,ariaLabel,progress,metric}`；后台侧 `admin.settings.trustLevels.{title,description,tl0Hint,restoreDefaults,restoreHint,zeroUnlimited,resetChanges,level,metric}`。`trust.level.*` 与 `trustLevels.*` 在两个 locale 里都 **0 命中** |
| C8 | **en-US 交集不为空（已知且接受）** | `admin.roleCatalog.member.alias` 的 en 值 = `Member`，TL1 en 值也 = `Member` → en 集合交集 = `{Member}`。D21 的机器断言只对 **zh** 成立（zh 交集为空）。必须写进文档，否则下一个人会当 bug 修，或加一条 en 断言把它跑红 |
| C9 | **M3 已落地** | `limitsForActor`（`Forum/service_trust_limits.go:23`）、`resolveLimit`（`:101`）、`TrustPolicyAdapter.LevelLimits`（`Providers/forum.go:273`）。`service_ops.go` 1208→1129 行，`:511` 现在是 `containsOutboundLink`——**照旧行号去找会读错函数**（笔笔实测抓到的） |
| C10 | i18n metric 键是 **camelCase**，API JSON 是 **snake_case** | i18n：`topicCount/commentCount/likeReceived/likeGiven/bookmarkCount/daysVisited`；JSON `requirements`：`topic_count/.../days_visited`。写「i18n metric 键 == `Metrics`」的断言必红 |

---

## 查过但没有（这些「零命中」结论同样是交付物）

| # | 我查了什么 | 结论 |
| --- | --- | --- |
| N1 | `knowledge/modules/*.md` 里除 `trust-levels.md` 外是否有文件描述信任等级/阈值 | **没有**。`rg -l "trust\.levels\|trustLevels\|信任等级" knowledge/modules/` 只命中 `trust-levels.md`（`badges.md` 只提「为信任等级预留」）。**不需要跨模块订正** |
| N2 | 两份运营页是否描述了 `trust.levels` 的 JSON 形状（`schemaVersion` / `levelSlug` / `requirements` / `limits` / `earnable`） | **全零命中**。运营页刻意不写 JSON（运营不手写它）→ **运营页无需对拍 JSON 形状**，JSON 断言只进技术验收（我报告 §6.10） |
| N3 | 幻影字段 `account_age_days` 是否传播到模块笔记/运营页 | **没有**。它只出现在 4 处，且**全部是「声明它不存在」的守卫**：`apps/web/tests/admin/settings/trustLevels.test.ts`（前端单测断言 `Metrics` 不含它）、`TrustLevels/config.go` 注释、`reports/…visual-spec.md`（设计侧撤键记录）、我自己的报告。**没有一处把它当真实字段用** |
| N4 | `docs/extensions/v3/catalogs/*` 的 trust 命中 | 来自 V3 目录生成物（后台 tab 的 UI surface），**不含阈值/等级数字**，无需对拍 |

---

## 给 lead 的派修建议（按文件聚合，避免多人改同一文件）

| 派给 | 文件 | 条目 |
| --- | --- | --- |
| writer | `knowledge/modules/trust-levels.md` | P0-1、P0-2、P0-3、P1-1、P1-2、P1-3、P1-4、P2-1、P2-2 |
| writer | `knowledge/sessions/2026-11-10-trust-levels.md` | P2-3（等 backend 跑完 build 再改） |
| writer | `knowledge/index.md` 信任等级节 | P0-1 同源那句「`bootstrap` 无装配、`features.trust_levels` 未注册」 |
| writer | `docs/{zh-CN,en-US}/usage/trust-levels.md` | P0-4、P0-5（两份要同步改，别只改中文） |
| backend | — | 只剩一件：跑一次 `go build ./...` 并把结果给 writer（P2-3 的前置）。<br>~~`ThresholdOptionKey()` 死函数：接上或删掉~~ **已关闭**：三个生成器（`NameKey` / `DescriptionKey` / `ThresholdOptionKey`）均已删除，`rg` 全仓零命中 |
