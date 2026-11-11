# 2026-11-10 Session Handoff — 信任等级（文档骨架）

## Changed

**本次只交付文档，没有写实现代码。** 信任等级（Discourse TL 形状）在 2026-11-10 立项；
我接手时 M0–M8 未落地，是把「计划 → 模块笔记 → 运营文档 → 索引」四条线先立起来并核实计划
里的事实断言；期间 backend 已提交 M1 与 M2 的 Identity 核心（见下方「第三轮」）。

- `knowledge/modules/trust-levels.md` — 新模块笔记。含 Purpose / Current Status /
  Data Model / Dependency Direction / Capability Resolution（四来源真值表 + D13/D14 的
  装载公式）/ 默认能力与限额 / Feature Flag And Permissions / Extension Surface /
  Known Residuals / Important Paths。实现断言一律带路径+行号，未落地的一律 `TODO(核实)`。
- `knowledge/sessions/2026-11-10-trust-levels.md` — 本文件。
- `docs/zh-CN/usage/trust-levels.md` + `docs/en-US/usage/trust-levels.md` — 运营者
  页面骨架，中英成对（`tests/validate-docs.mjs` 的平行结构校验要求）。
- `docs/{zh-CN,en-US}/usage/README.md` — 章节表各加一行（成对）。
- `knowledge/modules/README.md` — 模块表补上 `badges.md`（既有漂移）与
  `trust-levels.md`。
- `knowledge/index.md` — Active Workstreams 顶部新增「互动第四刀：信任等级」一节，
  status: in progress。**未改动其它节。**

第二轮（同日，按 lead 裁决回填）：数字按命令更正为 256；D13–D17 落进模块笔记与运营页；
运营页加了「默认不解锁任何权限」的诚实表述；模块笔记的 Known Residuals 扩到十条并注明
「不要当 bug 修」。

第六轮（同日，全量对拍——这是本文件最重要的一轮）：
按 PM 的 `reports/2026-11-10-trust-levels-doc-audit.md` 逐条在代码上复核，修掉**滞后型否定句**
（P0-1/2/3/4/5），并订正 P1/P2 全部条目：装配（`api_assembly_domains.go:321`）、开关五处
（`feature_flags.go:35/63/82/97/130/153`）、`trust.manage` 迁移
（`202611100004_trust_manage_permission.sql`）、M3（`limitsForActor` / `resolveLimit` /
`TrustPolicyAdapter.LevelLimits`）、`LoadActor` 4 次基线 + TL 1 次（R7 已收口）、
`GrantSource` 改名、三个死生成器全删、i18n 真实键族（`profile.trustLevel.*` /
`admin.settings.trustLevels.*`）、en 交集 `{Member}` 已知接受、i18n camelCase vs API
snake_case、`ThresholdOptionKey` 死函数已删。
**方法教训**：漂移方向是「滞后」不是「漏写」——正确修法是删掉过期的否定句，不是补一句新的
肯定句。否定句（「没有 X」）必须每次对拍重测。

第五轮（同日，补三件实测事实）：
- `modules/badges.md` 的 Known Residuals 新增第 11 条——徽章那一刀的 84 条断言 0 FAIL，但
  i18n 完整性不在覆盖范围：HEAD 的 `badges` 键整块缺失，由本刀补齐（两份 locale 各 +87/−0）。
- `modules/trust-levels.md` 新增「登记点」一节：新增后台 tab / 公开组件是 **8 处**（6 处
  代码与 locale + 2 条 catalog identity），漏 identity 是生成器抛错而非静默失败。
- 四条新残留：系统账号恒 TL0 是**构造性**保证且不保证不吃 TL0 限额（D25 收窄）；仓库已
  不能安全跑 `sqlc generate`（只手工同步了 `ListUserPermissions`）；deny 集合目前只覆盖
  候选集（D28，待修，T4 测试须用「deny 一个无静态授予的键」构造）；每档限额需显式存储
  （D27）；`CapabilitySource` 与 `hostapi.CapabilitySource` 撞名待改（D30）。
- 核对结论：`modules/frontend.md` **没有**「新增 tab 登记点」类清单（`rg` 零命中），
  无需订正；8 处登记点只写在 trust-levels 模块笔记里。

第四轮（同日，按 lead 裁决撤回与统一）：运营页与模块笔记**撤掉了 `DefaultPolicy()` 的
具体数字**（P0-2：与 PM 定稿表大量不一致，正在对齐；只印代码里读得到、且不会被改掉的
数）；TL2 slug 统一到 `resident` 的裁决已登记，对齐前文档不引用该档 i18n 键名（P0-3）；
构建红等过程态从模块笔记与 index 撤回；backend 收口后，handoff 里也已改为完成记录。

第三轮（同日，发现实现已推进；**以下为当时状态，第六轮已全部更新**）：M1 与 M2 的 Identity 核心已由 backend 提交
（`app/Models/TrustLevels/`、`Actor.TrustLevel/TrustLevelResolved/Earned`、
`applyTrustLevel`、`CapabilitySource`、`WithTrustLevelSource`、`trust.levels` 单键 +
`DefaultPolicy`、后台「信任等级」tab），但**当时未装配、开关未注册**，`go build ./...`
当时红（D22 施工中）。模块笔记的 Current Status 已改成逐条带证据的状态表；运营页从
「规划中」改为「代码已就位、尚未接线」，并补了真实默认阈值表与两条措辞校准
（后台用户详情只显示授予、不显示挣得；`*_any` / lock / pin / manage 永不可挣得）。

## 核实时发现的三处「计划 ≠ 代码」

写文档的人最容易照抄计划。以下三条是 2026-11-10 在真实代码上核对的结果，**以代码为准**：

1. **`LoadActor` 是 4 次查询，不是计划说的 2 次。**
   路径：`GetCurrentUser` 主查询（`postgres_store.go:103`）→ `loadCurrentUserAccess`
   的 `ListUserRoleKeys` + `ListUserPermissions`（`postgres_store_roles.go:246-251`）
   → `SELECT created_at, kind`（`postgres_store.go:380` 附近）。计划 D4 的「+1 次 PK
   查询」与 G 组 p95 基线都要按 4→5 重算。

2. **deny 的证据指错了函数。** 计划写 `postgres_store_permissions.go:49`，那是
   `ListActiveUsersWithPermission`（收件人查询）。actor 装载路径上的 deny 真身是
   `database/queries/identity.sql:46-76` 的 `denied_permission_keys` CTE，之后经
   `ExpandEffectivePermissions`（`postgres_store_roles.go:263`）展开父→子。
   结论（deny 在装载期生效）不变，证据要换。

3. **`.Can(` 非测试调用点是 256，含测试 280。** 本条我第一版报成 226 —— 错因是用
   `rg -c`（按**行**计数）求和，同一行多个调用被吞掉。数这个数只认命令：

   ```bash
   rg -o "\.Can\(" -g '*.go' -g '!*_test.go' | wc -l   # 256
   rg -o "\.Can\(" -g '*.go' | wc -l                    # 280
   ```

   结论（D1/D13 的「`Can()` 零改动」）不变。

## Decisions

本刀未新增决策——计划的 D1–D21 是唯一决策来源。第二轮由 lead 裁决的条目已回填：

- **D13 装载期合并**：合并点只有 `loadCurrentUserAccess`（`Actor.Permissions` 与下发前端
  的 `CurrentUser.Permissions` 的唯一物化点），`Can()` 一个字节不改。附带的第二个理由：
  `policy.go:29` 有 `Permissions == nil → false` 早退，「在 `Can` 里加分支」结构上走不通。
- **D14 deny 压过 earned**：`effective = Expand(static) ∪ (earned ∖ denied)`，顺序硬性
  为「先展开父→子、再并 earned、最后减 denied」；`ListUserPermissions` 要把候选集与是否
  生效一起返回，不许写第二个 deny CTE 副本。`Actor.Denied` 方案被否（会出现两份「能不能」
  的表示）。
- **D15/D16/D17**：earnable 与父键零交集；开关进 5 处并顺手补 `features.badges` 的缺口；
  earnable 必须有编辑入口（默认 ∅ + 可配置才是正确形态）。Z3（收窄 member 基线、改由 TL1
  授予）明确不做，另开一刀——它的前置是重写 F 组口径。
- **D18 依赖倒置**：Identity 只声明窄接口 `TrustLevelSource`，`TrustLevels` 实现，
  `bootstrap` 装配；Identity 不许 import `TrustLevels` / `Badges` / `Options`。activity
  复用 `Badges.GetCounters`，bootstrap 里的适配器做类型转换（编译期检查，字段漂移即编译失败）。
- **D19 阈值用单键 `trust.levels`（JSON）**，照抄 `seo.footer_links` 的严格解码形态；
  44 个扁平键要过 options 5 道注册 ≈ 220 处改动，且漏第 5 道会静默丢弃（T16）。
- **D20 `forbid_attachments` 本刀不接线**（运行时行为变更会破坏 F 组基线），只登记为残留。
- **D21 命名**：JSON 字段 `levelSlug`、禁止裸 `slug`；档位名按 D10 修订版全档带「用户」。

## Next

- **M1（backend）**：`app/Models/TrustLevels/` 纯函数 `Level()` + `LevelPolicy` +
  earnable registry + `user_activity` 读适配。落地后回来把模块笔记里所有
  `TODO(核实)` 换成带行号的事实（阈值配置已定 D19 单 JSON 键，不再是嵌套键问题）。
- **M2 收尾（backend）——已完成（2026-11-10）**：① 装配落地
  （`bootstrap/api_assembly_domains.go:321` + `api_assembly_extensions.go:476` +
  `bootstrap/trust_levels_wiring.go`）；② `features.trust_levels` 五处注册齐
  （`feature_flags.go:35/63/82/97/130` + 读取 `:153`）；③ D22 的 `LoadEffectiveActorTx`
  已接 `TrustLevelSource`（4 参 `Grants`），调用点全部更新，`go build ./...` 通过。

- **M3（backend）**：`trustForActor` → `limitsForActor`。七个 option 键全保留、不迁移。
- **M4 已完成（2026-11-10 复核）**：4 条路由进 catalog（409 = 405 + 4）、OpenAPI
  `paths/trust_levels.yaml`、`controller_test.go` 都在；公开响应**不含 `limits`**
  （`dto.go` 的 `omitempty` + 「只在后台读面出现」注释）。**M5 进行中**：资料页等级卡组件已挂，
  数据源 `useTrustLevel.ts` 仍 `() => null`。
- **M5 余项**：前端展示接线（等级卡数据源）、earnable 编辑入口。**注意**：
  `tests/validate-docs.mjs` 会校验文档里出现的每个 `METHOD /api/v1/...` 必须同时存在于
  OpenAPI 与 Go 路由目录，所以运营文档现在刻意不写端点路径；M4 落地后拿到端点清单再补。
- **M6（writer）**：把 `docs/{zh-CN,en-US}/usage/trust-levels.md` 从「规划中」改成
  已验证的操作步骤（后台阈值页路径、五档默认值的实际数值、恢复默认的实际按钮文案）。

## Open Questions

1. **等级要不要进 `sforum_core_v1` 与 HostQuery？** 派生值不落库，视图只能现算；要不要
   为此接受每次查询多读一行 `user_activity`，计划未答。
2. **`CapabilitySource` 如何区分 `static` 与 `override`？** 装载期两者已被合并成一个
   `Permissions` map（`postgres_store_roles.go:246-264`），没留痕迹。要区分就得在装载期
   多留一份信息——与 D14 要求的「denied 集合一起返回」是同一类改动，建议合并成一次设计。
3. **`features.trust_levels` 关闭时的「逐字节等价」怎么证明？** F 组断言需要一个可执行的
   对照手段（现无），建议在 M7 之前定下来，否则这条验收无法给出证据。
4. **`trust.new_user.forbid_attachments` 的死配置要不要顺手接上？** 现在值会存、后台会显示、
   `Options.TrustPolicy.ForbidAttachments` 也读出来了，但没有消费者。本刀只登记为残留
   （模块笔记 Known Residuals 3），未接。要接就得在附件上传策略里加一条新人判据，属于
   另一刀的授权范围。
5. ~~TL2 的 slug 待统一~~ —— **已关闭**：代码是 `resident`，真实 i18n 键族是
   `profile.trustLevel.name.*` 与 `admin.settings.trustLevels.level.*`（`trust.level.*`
   不存在，三个死生成器已删）。
6. **解锁能力（earnable）的编辑入口还没做。** 后台 tab 只编辑阈值，`limits` / `earnable`
   原样带回、不提供编辑器。D17 要求「必须有入口，否则机制是死代码」，这一条在 M4/M5 落地前
   是**未闭合**的。
7. ~~运营页要不要印默认阈值数字~~ —— **已裁决：不印**（lead，2026-11-10）。理由：阈值可配，
   文档里印一份必然与代码分叉，且分叉后没人会发现（数字不像函数名会编译报错）。正确形态是
   「以站点设置 → 信任等级页显示的当前值为准；该页有『恢复推荐默认』按钮，点了就能看到内置
   推荐值」。PM 的 C1（`DefaultPolicy()` 与定稿表逐值一致）作为**代码事实**留在报告里，供
   实现者核对，不进运营页。
