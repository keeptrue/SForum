# 2026-11-14 信任等级条件化门槛（M5 收口）Handoff

计划：`knowledge/plans/2026-11-14-trust-level-conditions.md`（Status: active，M0–M4 已落地、M5 收口完成；
1 笔未落 + 2 个并行单）。
模块笔记：`knowledge/modules/trust-levels.md`。

## Changed

- **模块笔记 §Extension Surface 写实**：原来是一整段 `TODO(核实)`（「计划未定等级域的稳定视图与
  HostQuery，这里不编」）。现在按四类面（views / queries / commands / events）逐条写清插件能做什么：
  事实条件声明、`permissionDefinitions[].trustLevelEarnable`、`trust_level.changed`（载荷 6 字段 +
  三条不发路径）、两条 HostQuery（目录 / 单用户现算、`incomplete|disabled` 时 `level` 为 null）、
  每日 sweep；§7 补上「7 家族不再手写两份」的现状（`v3PluginOnlyDeclarationFamilies` 是运行期唯一
  来源 + 清单⇄schema 双向比对）。本轮把该节里 6 处行号按 HEAD 实测校正（`v3_badge_conditions.go:61-146`
  / `:27`、`registry.go:31-39` / `:260-268` / `:299-308` / `:328`）。
- **新增 §回滚口径**：`trust.levels` 单键 JSON + `DisallowUnknownFields` → 含 `rule` 的配置被旧
  二进制整份拒绝；回滚顺序 = 先清空全部 `rule` 并保存 → 读回确认 → 再换二进制；`color` /
  `deletedTierIds` 对更早版本同理。解码失败后果写的是实测口径（未解析 / 等同 TL0 / 不渲染等级卡），
  不是「掉回默认策略」。
- **新增 §本刀的 9 条已知残留**：每条给「现状 + 为什么留着 + 谁负责/什么条件下消」，并附
  **§核验命令**表（14 行命令 + 本机输出）。本轮按实测重写四条：
  - 残留 5（`identity.profile`）：改为**本轮在办（task-2）**，保留「落地前不得按 14/12 描述现状」。
  - 残留 6（7 家族手写清单）：改为**已收口（`0b38458bd`，task-35）**——`v3PluginOnlyDeclarationFamilies`
    成运行期唯一清单，双向比对 + reflect 断言；**残留风险**改为「各家族 validator 里仍各自保留 kind
    判断（`v3_validate_platform.go:130/232/316/578/790`），是否构成第二入口由小审裁决（task-3）」，
    明确不写「已彻底解决」。
  - 残留 7（gofmt）：本刀点名 4 个仍在列，**今天全仓实测 26 个**（`go1.27.1` 口径），并写清
    「顺手全仓格式化会把无关 diff 卷进本单」的取舍。
  - 残留 8（`batches==2`）：**原说法不可复核**——全仓无 `batches` 计数器、无 `batch.UpsertMany`
    装饰器，事实写口是一次一条的 `RecordFact`；降级为「无来源记录」并指定消号条件。
- **新增 §本刀还剩什么没做**：引用计划 §6 的 7 条边界裁决；未落 1 笔（task-2）、并行单 2 个
  （task-4 夹具重放、task-3 清单审查）；一处注释待改（`config.go:845`）。
- **新增 §本刀验收矩阵**：引用 `reports/2026-11-14-m5-acceptance-matrix.md`（六条变异本人重打），
  并补两条它之后新增的（`incomplete/disabled` null 契约守门人、payload 双入队独立性）。
- **纠正后台 i18n 键族与叶键数**：族是 `admin.forum.trustLevels.*`，不是 `admin.settings.trustLevels.*`
  （三处证据：两份 locale 的 `settings` 族为 `undefined`、源码断言 `trustLevels.test.ts:848`、
  死键棘轮登记表收缩到空数组）。**叶键数按实测 394**；旧数 243 在任何一个已提交版本都不成立
  （`5a1d4083f` 实测 360、HEAD 394），死键棘轮注释里的 360 是 `5a1d4083f` 的旧值。
- **ui-spec 的深度阶梯标注作废并同步 S1/S2**：`reports/2026-11-14-trust-level-conditions-ui-spec.md`
  里「d1 274 … d5 叶子内容 = 170」那张阶梯作废（层级口径 / 步长 / 量错对象三处叠加），正文另外 5 处
  仍在引用 170 的地方（D-3 决策行、§1.5 否决理由、§3.2 两处控件宽、§8 一句话交付）改成实测口径
  （最内层 `clientWidth=208`、叶子 182、约 22px/层）；Q-12 判据改为「最深层 `clientWidth ≥ 200`
  且 `scrollWidth == clientWidth`、页面无横向溢出」；Q-02/Q-03 按计划 §4.4 的 S1/S2 更正口径改写
  （面板不给 shell 基线**新增** scroller / 新增 `overflow≠visible` 元素——基线本身不是 0/1）。
- **生成物**：`docs/extensions/v3/catalogs/backend-surfaces.{json,md}` 只补两条
  （`trust_level.changed`、`trustlevels.sweep`），`node scripts/v3-catalog/generate.mjs --check`
  → `V3 P0 catalogs are current (18 files).`
- 计划头部 Status、`plans/README.md` 的 Current Plans 行、`index.md` 的 Latest Handoff 同步为
  「1 笔未落 + 2 个并行单」（原文写「2 笔未落」，其中 7 家族那笔已被 `0b38458bd` 落掉）。

## Decisions

本刀三份决策记录（本次收口只做索引，不重写）：

- `decisions/2026-11-14-trust-level-earnable-plugin-keys.md` —— 可挣得权限白名单从「解码期硬拒」
  改为「运行期过滤 + 写期拒绝」（D24 修订）；含「一次插件停用 → 整份解码失败 → 全站等同 TL0」
  的逐段链路与状态枚举为什么不用 `orphan`。
- `decisions/2026-11-14-trust-level-synthetic-rule-root.md` —— 合成根只 `Prepare`、禁 `Validate`、
  只装「数值已过且带 rule」的档位、合成根不出包；含「外部拿不到合成根由编译器保证」的论证。
- `decisions/2026-11-14-hostquery-computed-definitions.md` —— HostQuery 增加「计算形态」，
  复用 SQL 侧同一份归一化/投影/校验实现。

## Next

1. **task-2**（`identity.profile` 条件，计划 §3.3）：落地后回写残留 5 为「已实施 + commit id +
   共享目录 13→14 / 信任子集 11→12」，四处硬编码计数与前端 `field` 四值文案同批。
2. **task-3**（`0b38458bd` 审查）：结论回来后回写残留 6 的「残留风险」段——判为第二入口就把 kind
   判据收到清单一处，判不是就写明为什么两个判据可以并存。
3. **task-4**（夹具正样本可重放）：落地后把残留 3 从「运行期取证不可重放」改成「一条命令可重放」。
4. 用可用 QA 账号重跑验收脚本的 HTTP 段，把 `H3 / I-09 / I-11 / I-13 / I-24` 的 pend 清掉
   （残留 9：`pend` 不能当绿）。

## Open Questions

- `app/Models/TrustLevels/config.go:845` 的注释仍写「全站等级掉回默认策略」，与实测口径
  （未解析 / 等同 TL0）不一致。改它是一行 + 同一注释块口径对齐，等下一次碰该文件的单顺手改。
- 运行期取证的可重放性：包级门禁（`scripts/ci/reference-packages-gate.sh`）常跑，但「装进 dev 库 →
  真实弹窗截正样本」没有脚本化路径，dev 库一清证据就失效。task-4 在补这条路径。
- `apps/web/tests/admin/forum/trustLevelI18nDeadKeys.test.ts` 的注释仍写「这个族有 360 个键」，
  实测 394。改它属 `apps/web`，本单不碰；归属下一次碰该测试的单。
