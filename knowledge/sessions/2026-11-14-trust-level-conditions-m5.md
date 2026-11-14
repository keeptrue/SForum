# 2026-11-14 信任等级条件化门槛（M5 收口）Handoff

计划：`knowledge/plans/2026-11-14-trust-level-conditions.md`（Status: active，M0–M4 已落地、M5 收口完成；
0 笔未落 + 2 个并行单）。
模块笔记：`knowledge/modules/trust-levels.md`。

## Changed

- **模块笔记 §Extension Surface 写实**：原来是一整段 `TODO(核实)`。现在按四类面（views / queries /
  commands / events）逐条写清插件能做什么：事实条件声明、`permissionDefinitions[].trustLevelEarnable`、
  `trust_level.changed`（载荷 6 字段 + 三条不发路径）、两条 HostQuery（目录 / 单用户现算、
  `incomplete|disabled` 时 `level` 为 null）、**6 处触发点**（reactions ×2 / bookmarks / comments /
  topics / visits，`bootstrap/badges_wiring.go:42-60` 一次入队两个 job）、每日 sweep；
  §7 写清单本体 + 3/7 家族的运行期缺口；**§8 新增「条件与影响面估算的边界」**。
- **新增 §回滚口径**：`trust.levels` 单键 JSON + `DisallowUnknownFields` → 含 `rule` 的配置被旧
  二进制整份拒绝；回滚顺序 = 先清空全部 `rule` 并保存 → 读回确认 → 再换二进制。
- **9 条残留统一成三件套**（现状 + 为什么留着 + 谁负责/什么条件下消），并附**核验命令表**。
  本轮按事实改写的四条：
  - 残留 5：`identity.profile` **已实施（`9169ba30d`，task-2）**——共享目录 **14** / 信任子集 **12**，
    4 处硬编码计数同批改、七处相对式断言未动；遗留边界是「不参与影响面估算」（见 §Extension Surface 8）。
  - 残留 6：7 家族清单**已收口但有已知缺口**——`0b38458bd` 让 `v3PluginOnlyDeclarationFamilies`
    成为清单本体 + 清单⇄schema 双向比对，但「运行期唯一事实源」对 **identity / seo /
    permissionDefinitions 3 个家族不成立**（`manifest.go:485` 的 `isThemeManifestSupported` 在
    `validateV3Manifest` 之前就拒主题），且该提交**静默删掉**了 notificationTypes 的主题侧双侧测试
    （现存 `TestManifestV3SchemaKindFamiliesRejectThemes` 只跑 seo / aiTools）。修复在办（task-7），
    审查 task-3。**不写「已彻底解决」。**
  - 残留 7：gofmt 点名 4 个仍在列，全仓今天实测 **26** 个（`go1.27.1` 口径）。
  - 残留 8：`batches==2` **确实存在**于 `bootstrap/trust_levels_policy_watch_test.go:94-95`，
    钉的是设置写钩子的批量转发（不退化成逐条 `Upsert`）；原句「未覆盖」不成立，真正的空档是
    「批量内部键保真（N 键进 → N 行出、顺序不变）」没有断言。
- **新增 §本刀验收矩阵**：引用 `reports/2026-11-14-m5-acceptance-matrix.md`（六条变异本人重打），
  并补「变异 → 能红的测试名」六行表（10 个测试名逐个 grep 核过路径）+ 之后新增的两条。
- **纠正后台 i18n 键族与叶键数**：族是 `admin.forum.trustLevels.*`，不是 `admin.settings.trustLevels.*`；
  **叶键实测 394**，旧数 243 在任何已提交版本都不成立（`5a1d4083f` = 360、HEAD = 394；
  差额 = `ruleConditions` 补到 42 − F1 删掉的 8 条孤儿键）。
- **ui-spec 的深度阶梯作废 + S1/S2 同步**：`reports/2026-11-14-trust-level-conditions-ui-spec.md` 的
  「d1 274 … d5 叶子内容 = 170」阶梯标注作废（层级口径 / 步长 / 量错对象三处叠加），正文另外 5 处
  引用 170 的地方改成实测口径（最内层 `clientWidth=208`、叶子 182、约 22px/层）；Q-12 判据改为
  「最深层 `clientWidth ≥ 200` 且 `scrollWidth == clientWidth`、页面无横向溢出」；Q-02/Q-03 按
  计划 §4.4 的 S1/S2 更正改写（比面板带来的**增量**，shell 基线不是 0/1）。
- **生成物**：`docs/extensions/v3/catalogs/backend-surfaces.{json,md}` 只补两条
  （`trust_level.changed`、`trustlevels.sweep`），`node scripts/v3-catalog/generate.mjs --check`
  → `V3 P0 catalogs are current (18 files).`
- 计划 Status、`plans/README.md`、`index.md` 的 Latest Handoff 同步为「0 笔未落 + 2 个并行单」。

## Decisions

本刀三份决策记录（收口只做索引，不重写）：

- `decisions/2026-11-14-trust-level-earnable-plugin-keys.md` —— 可挣得权限白名单从「解码期硬拒」
  改为「运行期过滤 + 写期拒绝」（D24 修订）。
- `decisions/2026-11-14-trust-level-synthetic-rule-root.md` —— 合成根只 `Prepare`、禁 `Validate`、
  只装「数值已过且带 rule」的档位、合成根不出包。
- `decisions/2026-11-14-hostquery-computed-definitions.md` —— HostQuery 增加「计算形态」，
  复用 SQL 侧同一份归一化/投影/校验实现。

另有一条**边界裁决（2026-11-14，lead）**记在模块笔记 §Extension Surface 8：`identity.profile`
不参与影响面估算（`rule_impact.go` 落 `default` → 「无法估算」，档位还有数值门槛则 `partial=true`），
理由是不引入与求值 SQL 不同源的第二份 count 口径。

## Next

1. **task-7**（老后）：7 家族清单的 3/7 运行期缺口 + notificationTypes 主题侧测试补回；
   收口后把残留 6 从「已收口但有已知缺口」改成「已收口」，并写明主题闸与清单的先后关系。
2. **task-3**（小审）：各家族 validator 残留 kind 判据是否构成第二入口；结论回来后回写残留 6。
3. **task-4**（老运）：夹具正样本可重放；落地后把残留 3 改成「一条命令可重放」并贴命令。
4. 用可用 QA 账号重跑验收脚本的 HTTP 段，把 `H3 / I-09 / I-11 / I-13 / I-24` 的 pend 清掉
   （残留 9：`pend` 不能当绿）。

## Open Questions

- `app/Models/TrustLevels/config.go:845` 的注释仍写「全站等级掉回默认策略」，与实测口径
  （未解析 / 等同 TL0）不一致。改它是一行，等下一次碰该文件的单顺手改。
- `apps/web/tests/admin/forum/trustLevelI18nDeadKeys.test.ts` 的注释仍写「这个族有 360 个键」，
  实测 394；在 `apps/web` 里，归属下一次碰该测试的单。
- copy-spec 的叶键数与测试行号：**已在 task-9 修正**（叶键按实测 394、禁第二族的断言在
  `847-848`），并给三份把旧族写死的历史报告加了作废横幅。
- 运行期取证的可重放性：包级门禁常跑，但「装进 dev 库 → 真实弹窗截正样本」仍无脚本化路径，
  由 task-4 补。
