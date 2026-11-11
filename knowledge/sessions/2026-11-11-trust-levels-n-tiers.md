# 2026-11-11 Session Handoff — 信任等级 N 档（1–8）改造

Status: **骨架（实施中，2026-11-11 由 writer 落盘）**。本文件此刻**只记录已批准的口径与待填槽位**，
不代表任何实现已落地。落地后由 writer 复测再改写「Changed」。

计划：`../plans/2026-11-11-trust-levels-n-tiers.md`（approved）
决策：`../decisions/2026-11-11-trust-levels-n-tiers.md`（D-N1..D-N13，含三条 wire 层细化）
对拍：`../reports/2026-11-11-trust-levels-n-tiers-recon.md`（小研，硬编码点/契约消费方/断言清单）
上游（**不取代，文件未改**）：`2026-11-10-trust-levels.md`（M6–M8）、
`2026-11-11-trust-levels-baseline-narrowing.md`（D34 基线门）。

## 基线命令（落地后对拍用，原文照抄，不要等价改写）

```bash
# 判据 A：生产代码 LevelCount 归零
rg -n "LevelCount" apps/ --glob '!**/*_test.go'

# 判据 B：零数据库迁移
git diff --stat -- apps/api/database/

# 判据 C：等级不落库
rg -n "trust_level" apps/api/database/

# 判据 D：前端常量只剩回退兜底
rg -n "TRUST_LEVEL_SLUGS|TRUST_LEVEL_MAX" apps/web

# 判据 E：LevelLeader 常量声明已删 + 无引用残留（判据在编译期，不在正则）
rg -n "LevelLeader\s*(Level)?\s*=" apps/api/app/Models/TrustLevels/types.go   # 期望零命中
cd apps/api && go build ./...                                                   # 期望 exit 0
# 注释里的 LevelLeader 字面量是预期的（事故史），不要为了让它变绿去删注释
```

## 两次实测（各自为真，说的是不同时刻；不是矛盾）

| 时点 | 判据 A | 伴随状态 |
| --- | --- | --- |
| 2026-11-11T17:2x | **21 命中 / 6 文件**（`TrustLevels/{types,config,level,policy,registry}.go`、`Http/Controllers/TrustLevels/payload.go`；测试文件另 6 命中） | 改造前基线 |
| 2026-11-11T17:30 | **0 命中** | 编辑中途：`TrustLevels/{types,config,level,policy,registry,resolver}.go` mtime 17:29–17:30，新增 `tier_icons.go`，`ConfigSchemaVersion = 2`；同一时刻 `cd apps/api && go build ./...` **红**于 `app/Models/TrustLevels/config.go:170:6: declared and not used: index`（老后编辑中途产物，时间戳 2026-11-11T17:30，不另行处理） |
| 2026-11-11T17:31 | 0 命中 | 判据 E 抽查（当时口径粗糙）：`registry.go` / `resolver.go` / `controller.go` 已无 `LevelLeader` **代码**引用，`Policy.LastRank()` 在 `types.go`（**代码在途，落地后重测**）。现行判据见基线命令块 E：声明零命中 + `go build` exit 0 |
| 2026-11-11T17:4x | 0 命中 | `cd apps/api && go build ./...` **已干净**（无输出）；但 `legacy_config_v1_test.go` / `tier_identity_test.go` **尚未出现** ⇒ 三条件缺第 3 条，文档窗口仍未开 |

## 「文档窗口」开启判据（Lead 已采纳为验收口径）

三条同时成立，缺一不开写运营文档：

1. 判据 A = 0，且**不是编辑中途的偶然 0**（看 mtime 与 `go build`）；
2. `cd apps/api && go build ./...` 干净；
3. `legacy_config_v1_test.go` 与 `tier_identity_test.go` 存在，且随 `./scripts/test-api-isolated.sh` 变绿。

## Changed

### 文档与知识库（writer，2026-11-11，窗口开启于 17:57）

- `docs/zh-CN/usage/trust-levels.md`：状态块改「阶梯判定（1–8 档，默认 5 档）」并补四个分区名；
  `## 五档` → `## 阶梯：1–8 档，默认 5 档`（写明 TL0 固定、默认 4 个可配置档）；
  新增三节「增加、减少与插序」「停用与恢复」「命名与图标」；「怎么调等级阈值」「开启能力解锁」
  「怎么恢复默认」三节改掉「五档」事实陈述；FAQ 补「停用会撤回该档贡献的可解锁能力」。
- `docs/en-US/usage/trust-levels.md`：同结构逐句镜像；UI 名一律取自 locale 现值
  （Overview / Tiers / Thresholds / Capability unlocks；Add a tier / Insert a tier before this
  one / Move up / Move down / Retire / Restore / Retired / Chinese name / English name /
  Name suggestions / Icon）。
- `docs/{zh-CN,en-US}/usage/README.md`：索引行「五档信任等级」/「five tiers」改为
  「可配置的信任等级阶梯（1–8 档，默认 5 档）」/「a configurable tier ladder (1–8 tiers, 5 by default)」。
- **保留上一刀（`2026-11-11-trust-levels-admin-page.md`）的 4 处路径改写**：状态块、阈值数字句、
  「门槛设置」入口、「能力解锁」入口；只在其上做增量，未从 HEAD 重写。
- 文档纪律落实：不印图标白名单条数（只举默认前三 芽/叶/屋 并注明以页面显示为准）、
  不提 tier id、不提端点路径。`node tests/validate-docs.mjs` → `all checks passed`。

### 浏览器 QA（老运，`storage/qa/trust-levels-n/`）

- **21 pass / 1 fail**（`20261111-184009-qa-reshoot-main.json`）。通过项：四 tab 与 `?tab=` 同步、
  N=8 桌面 `layout=cards`、390×844 `layout=cards rows=7` 且无横向溢出 390/390、窄屏工具栏
  均有文本或 `aria-label`+`title`、图标选择器键盘可达、停用二次确认与覆盖分支、N=2 单档披露、
  插序保存确认与取消不写入。**未通过**：`save-field-errors`（名称超 32 字保存 → 行内字段错误
  0 处、页面级错误块 false、无 toast；拒绝本身正确但运营看不到提示）。
- 模块注记里三条浏览器 TODO 已按上述证据逐条消掉并填入具体文件；**新增一条**
  `TODO(核实：字段级错误渲染，QA save-field-errors FAIL)` 与一条
  `TODO(核实：最后一档置灰的浏览器用例)`（该点只有实现层证据）。

### 文档语义变更（D-N15，本刀引入）

- 「恢复推荐默认」从「本地只重置阈值」改成**服务端整份覆盖**（`POST /admin/trust-levels/restore-defaults`
  → `EncodeConfig(DefaultPolicy())`）：档位数量与顺序、阈值、限额、新增解锁、名称与图标全回退，
  已停用档被移除；破坏性动作前有阻塞式确认并逐项列出会丢的五类配置。两份运营文档已改引 locale 现值。

### 后端 / 前端

（由老后、小前落地后补；本文件不代替他们的交接。）

## Decisions

- **索引（正本见 `../decisions/2026-11-11-trust-levels-n-tiers.md`，此处不复制理由与被否）**：
  D-N1..D-N14 —— 身份与序号分离、`trust.levels` v2、命名池非存储字段、图标白名单、
  v1/v2 兼容、`tier_<seq>`、active 1..8、软停用、推荐默认值单源、四 tab 单保存口、
  分档限额只读、矩阵响应式、**保存路径才写库**、**配置校验错误走统一机器码 +
  `reasonCode` 枚举明细**、**恢复推荐默认 = 整份配置覆盖（D-N15）**。三条 wire 层细化挂在 D-N2 / D-N5 下
  （读面仍叫 `levels[]`、`level` 与 `levelSlug` 并存、前端常量收窄到回退分支）。
- **两条成本洞见**（这一刀能小步落地的原因）：① 等级不落库 ⇒ 零数据库迁移；
  ② `Actor.TrustLevel int` 语义仍是 rank ⇒ Identity 与 Forum 零改动。
- **回滚是两步**（D-N13 的代价）：先回退镜像，**再**把 `trust.levels` 的值回退到 v1 形状。

## Next

1. **后端 / 前端 / 文档 / QA：均已落地**（判据见基线命令块 A–E；文档与模块注记见 Changed）。
2. **唯一未闭**：字段级错误渲染（`save-field-errors` FAIL）。修掉后把模块注记那条
   `TODO(核实：字段级错误渲染，QA save-field-errors FAIL)` 消掉并补新的浏览器证据。
3. **收口已完成**：两条判据命令均已归零（见下），`../index.md` 的 Latest Handoff 已指向本文件。
   - `rg -n "TODO\(核实：等老运双宽度 QA\)" knowledge/modules/trust-levels.md` → 零命中（exit 1）
   - `rg -n "TODO\(核实：前端 locale 与覆盖测试落地后\)" knowledge/modules/trust-levels.md` → 零命中（exit 1）
4. 仍留在模块注记里的两条非本刀待办：`TODO(核实)`（等级域稳定视图/HostQuery，L407 附近）与
   新增的「最后一档置灰的浏览器用例」——它们**不在**上面两条判据的范围内，别误判成回归。

## 回滚须知（下次回滚的人不会想到，先写在这）

- **回滚是两步，不是一步**（D-N13）：旧二进制无法解码 v2 ⇒ 先回退镜像，**再**把
  `trust.levels` 的值回退到 v1 形状；只换镜像会让站点开机读不到配置并回落到推荐默认。
- 库值升级只发生在运营保存路径，所以「没人保存过」的站点在回滚时不需要回退值
  —— 判断依据是看 `trust.levels` 的 `schemaVersion`。

## 写作用域规则（Lead 于 2026-11-11 定，执行中）

1. **双语文档单写手**：这两份文件落地前只由 writer 整文件写；其余人不得整文件覆盖，
   只能在 writer 的新内容里保留那 4 处已生效的路径改写。
2. **以工作树当前内容为基线做增量**，绝不从 HEAD 重写。那 4 处在
   `docs/{zh-CN,en-US}/usage/trust-levels.md` 各 4 处：① 状态块第二行「后台…页」；
   ② 「具体数字以…为准」句；③ 「怎么调等级阈值」入口；④ 「开启能力解锁」入口
   （来源见 `2026-11-11-trust-levels-admin-page.md` 第 17–19 行；收口时 Lead 要对拍这一段）。

## Open Questions

- **`limits` 编辑器**（D-N11 推迟的那一刀）何时接：它的空值表达（继承 vs 免冷却 vs 覆盖）
  需要独立设计，不在本次范围。
- **命名池内容**由谁维护、加档时是否应给新档预填一个推荐名？（现在按 D-N3 只做一键填入。）
- **8 档上限**是否属于「运营可再谈」还是硬纪律：改上限要同时改前端矩阵布局与校验，
  目前按计划视为硬上界。
- **新档默认门槛全 0 的语义**：「只要比它低的档都满足就直接落到新档」——落地后实测确认，
  确认后再写进运营文档，不提前写死。
- 既有残留「TL4 是最高档」三处（`registry.go` 的 `All()`、`controller.go:108`、
  `resolver.go:116`）**本次一并修复**，不是历史遗留；验收时按决策记录里的复算命令对拍。
