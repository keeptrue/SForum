# 信任等级 — 中期只读审查（M1 + 计划/契约一致性）

Status: **有阻断项**（3 条 P0，必须转 backend）
Reviewer: 小审（reviewer）
Date: 2026-11-10 20:0x
Scope: M0 决策记录 / M1 包 `apps/api/app/Models/TrustLevels/` / 计划 D1–D26 / PM 94 条验收
Out of scope: M2（Identity 接线，正在写）、M3、M4

---

## 0. 审查基线（必读：审查期间该包被改了 3 次）

本报告钉在下面的快照上，**不是**「当前工作树」：

```
快照 /tmp/tl_snap2（2026-11-10 20:01:48）
types.go      67367a8e86e2f9d00bd0e0600c23ffa1b65cd9e8dd6831d5c8d171871aa1e8ca
level.go      4a5ea093fee47bbd22189d2f5f55ee93976b9e58165222fcb289086168c5ba74
policy.go     4eaf93641a59df64eed79fe59648b7624f2761d74edb9b407651a6c276b48469
registry.go   a9746c2ccca1b100b858a4c7433b5d87800aeb2f15a36d10475cdf63a27f47fe
config.go     2b8c621db35751a886ec26bed2d859e144b7140b67a7e6e4491c22ac54160ce2
resolver.go   cb9f838a94dda149152f609a9adfcfe0957c7b61cd214217d4d40dd109d81f93
```

审查窗口内发生的事（有时间戳，可复核）：

| 时间 | 事件 |
| --- | --- |
| 19:57:31 | backend 报「M1 已落」。此刻包内 3 个测试文件（19:53–19:54）与实现**不匹配** |
| 20:00:00 | 我实测 `go test ./app/Models/TrustLevels/` → **build failed**，11 处 undefined / 参数不匹配（`EarnablePermissions`、`Earned`、`EarnedSet`、`NewResolver` 少一个参数、`Resolve` 不存在） |
| 20:00:04–20:00:20 | backend 重写 `registry_test.go` / `resolver_test.go`、新增 `config_test.go`、给 `Requirements` 补 json tag、给 `config.go` 换 `levelSlug` |
| 20:01:48 | `go vet` 干净、`go test` 绿（本报告基线） |
| 20:02+ | `app/Models/Identity` 进入半成品：`actor_postgres_tx.go:71: undefined: trust`、`LoadEffectiveActorTx` 调用参数不匹配 → TrustLevels 因依赖 identity **无法再测试** |

**结论（流程）**：「M1 已落 + 3 个测试全绿」这个陈述在 19:57–20:01 之间不成立。建议把「已落」的判定标准固定为「`go test` 绿 + 包内文件 sha256 快照」，而不是口头 + 行数统计。

---

## 1. 阻断级（P0，必须改）

### P0-1 后台可勾选白名单泄漏 `user.permission_override`（提权）

`apps/api/app/Models/TrustLevels/registry.go:105-117`

`EligiblePermissions()` 是**负面过滤**（排除 `IsAdminClassPermission` + `IsLegacyParentPermission`），而 D24（`plans:220-226`）要求的是**正白名单**（7 个内容键）。实测返回值：

```
ELIGIBLE count=8
[user.permission_override topic.create topic.edit_own topic.delete_own
 post.create post.edit_own post.delete_own attachment.upload]
```

`user.permission_override` 在 `Identity/seeds.go:20-21` 的注释是「编辑用户直接权限例外（**高危，单独授予**）」，还有一条专门迁移把它从 `user.manage` 展开里撤销（`database/migrations/202607120004_revoke_permission_override_from_user_manage.sql`）。

它为什么漏网：`Identity/permission_class.go:20-23` 的后缀表里是 `".override"`（点），而键名是 `permission_override`（下划线），`HasSuffix(".override") == false`。

后果链（同一条谓词也是校验谓词）：`EligiblePermissions` 的过滤条件与 `Earnable.Validate`（`registry.go:90-92`）**完全同源** ⇒ 白名单返回 8 键即证明 `Validate` 会放行 ⇒ 运营在后台把 `user.permission_override` 勾给 TL4 ⇒ **所有 TL4 用户获得「编辑任意用户直接权限例外」** ⇒ 可给自己授予任意权限，等价全量提权。这不是「下一刀」的风险，是当前代码即可达。

违反：D24（正白名单）、PM `capability-map.md:422`（B10 断言白名单恰好 7 键）、PM `:202`（§4.3 #2「不要写成 strings.Contains 式负面判定」）。

修法：
1. `EligiblePermissions()` 改成 D24 的 7 键硬编码列表，`Validate` 也走它（白名单之外一律拒绝，而不是「不是管理类就放行」）；
2. 顺手给 `IsAdminClassPermission` 补 `permission_override` / `_override` 后缀（它是通用分类函数，其它调用点同样会漏）。

### P0-2 默认阈值与 PM 定稿表：19 个值里 17 个不同

`apps/api/app/Models/TrustLevels/policy.go:11-61` vs `knowledge/reports/2026-11-10-trust-levels-capability-map.md:215-223`

计划 `plans:272` 明确「阈值默认值以 PM 的 capability-map 为准」。逐值对拍（代码 → PM）：

| metric | TL1 | TL2 | TL3 | TL4 |
| --- | --- | --- | --- | --- |
| `days_visited` | 2 → **3** | 7 → **15** | 30 → **45** | 90 → **100** |
| `topic_count` | 0 → 0 | 1 → **2** | 5 → **8** | 20 → **25** |
| `comment_count` | 1 → **5** | 10 → **20** | 50 → **80** | 200 → **250** |
| `like_received_count` | 0 → 0 | 3 → **5** | 25 → **30** | 100 = 100 |
| `like_given_count` | 0 → 0 | 0 → **3** | 10 → **20** | 50 → **80** |
| `bookmark_count` | 0 → 0 | 0 → 0 | 0 → **5** | 20 = 20 |

只有 2 个值一致。PM `:383`（F6「on 时读默认阈值 == §5.1 表」）现在必红。

同一处还有一条**结构**不一致，不是调数字能解决的：

**`accountAge` 分级阈值在数据模型里不存在。** PM `:223` 给了 TL2=3 / TL3=30 / TL4=90 天，`:331`（C7）断言「age 2d/3d/4d → TL1/TL2/TL2」。而 `types.go:158-168` 的 `Policy` 只有 `NewUserDays`（一个上限闸），没有任何逐档账龄字段；`LevelOf`（`level.go:19-32`）也只读 `Levels[n]` 的 6 个行为指标。⇒ C7 按当前数据模型**不可实现**。

二选一，不能含糊：
- 加 `AccountAgeDays` 到 `Requirements`（要动 `Metrics`、`satisfies`、config JSON、后台表单、契约）；
- 或废掉 C7 并改 PM §5.1 表（`accountAge` 那行降级为「仅 TL0/TL1 的上限闸」）。
我建议后者：`days_visited ≥ N` 已经蕴含账龄 ≥ N，逐档账龄是冗余字段，PM 自己在 `:243` 也承认「与 days_visited 冗余」。

### P0-3 slug 是 `regular`，契约冻结的是 `resident`

`apps/api/app/Models/TrustLevels/types.go:32-38`（`nameSlugs[2] = "regular"`）vs `plans:264`（`常驻用户 resident`）、PM `:84`（「slug 不变，已冻结 `newcomer/member/resident/veteran/leader`」）、PM `:428`（H17）。

实测影响（不是推测）：

```
SLUG[2]="regular" nameKey="trust.level.regular.name"
DECODE_WITH_RESIDENT err=trustlevels: invalid config: 未知等级 "resident"
```

⇒ 任何按契约写出的 `trust.levels` 配置会被**直接拒绝**；i18n 键变成 `trust.level.regular.*`，H17 的 en-US 值集合断言（`Resident`）会红。改 slug 会牵动 36 个 i18n 键，越早改越便宜；现在改只需动 `types.go:35` + `config.go:26` 注释。

---

## 2. 高（P1，M2 落地前必须定）

### P1-1 D25（bot/系统账号不获授予）在当前接口形态上不可实现

`resolver.go:69`（`Grants(ctx, userID) (int, []string, error)`）+ `Identity/trust_levels.go:22`（`TrustLevelSource` 同形）拿不到 actor 的 `Kind`。D25（`plans:228-230`）要求 `kind != human` 与系统账号（`builtin-auto` / `dev-auto`）直接返回 level 0 + 空集。要么扩接口签名（加 `kind`），要么 Resolver 再读一次 `users.kind`（+1 查询，与 G 组预算冲突）。

**接口形态要现在定**：M2 一落地，这个签名就有 45 个 `LoadActor` 调用点的影响面。

### P1-2 `newUserDays` 双权威 + 缺字段 fail-open

`config.go:20`（`json:"newUserDays"`）、`config.go:63-68`

- D2（`plans:98`）说 `trust.new_user_days` 这个既有 option 键**保留、语义不变**；D19 又在 `trust.levels` JSON 里放了 `newUserDays`，且 `DecodeConfig` 用它覆盖默认值。**同一语义两个存储位置** = 本刀明令禁止的「两个答案」。
- 实测缺字段行为：省略 `newUserDays` → 解码**成功**且 `newUserDays=0` ⇒ 新人闸关闭，全员至少 TL1。其余每个字段缺失都拒绝，只有这一项静默降级，方向与 D7「失败关闭」相反。

修：`newUserDays` 从 `trust.new_user_days` 派生，不进 `trust.levels`；若坚持放进来，则设为必填（用 `*int` 判缺失）。

### P1-3 `created_at` 同一请求读两次；G 组预算「4→5」不成立

`resolver.go:29-31`（`AccountReader`）+ `Identity/postgres_store.go:412`（`SELECT created_at, kind FROM users WHERE id = $1`）

`LoadActor` 自己已经读过同一列。按当前接口形态，一次 `LoadActor` 的真实增量是 **+2**（`created_at` + `user_activity`），除非 M2 把 412 那次读删掉或把结果喂给 Resolver。

而且三个口径互相矛盾：计划 `:26`/`:75`/`:337` 说基线 4 次查询、目标 5 次；PM `:394`（G3）还写着「`LoadActor` 2 → 3 次」。**G2/G3 的结论在口径统一前没有意义。**

### P1-4 PM 的 C19 与 D2/代码直接冲突（FAIL-by-design 条目）

PM `:343`：C19「`age < trust.new_user_days`，活动满足 TL2 全部阈值 → **TL2**」。`level.go:20-25` 在窗口内**封顶 TL1**，计划 `:88`（D2/§2.1）也是「不能提前越过 TL1」。

PM 自己在 `:346` 承认这是冲突点并建议「接受 AND 累积语义」，但 C19 的期望值仍写着 TL2 ⇒ **没改完**。M8 执行时这条会红，而红的不是实现。建议 C19 期望改为 TL1，并把「窗口内封顶」写成一条独立断言。

---

## 3. 中（P2）

**P2-1 `Metrics` 是导出的可变包级 slice** — `types.go:87-94`。任何调用方 `trustlevels.Metrics[0] = "x"` 就能改判定表与 `validateRequirements` 的遍历集。D4 的「纯函数」主张应包含不可变边界：改 `[6]string` 数组 + clone 访问器，或 unexported + `Metrics()`。

**P2-2 `validateLimits` 用 map 遍历决定报错顺序** — `config.go:155-166`。两个限额同时为负时报哪一条不确定 ⇒ 错误文案不可复现，对 M8 的逐字断言是隐患。改有序切片。

**P2-3 TL0 / 越界等级在 `Earnable` 里被静默丢弃** — `registry.go:120-141`（`Normalize`）。实测 `Earnable{LevelNewcomer: {"topic.create"}}.Validate()` → `nil`，`Grants(TL4)` → 空。这正是 `config.go:14` 自己写的「拒绝比静默丢弃安全」的反面（也是 T16 的坑）。`Validate` 应显式拒绝 `LevelNewcomer` 与越界键。

**P2-4 `satisfies` 硬编码 6 个字段，而 `Metrics` 是可扩展列表** — `level.go:40-47`。往 `Metrics` 加第 7 个指标（例如 P0-2 讨论的 `accountAge`）不会被 `satisfies` 读取，`validateRequirements` 却会把它当已知指标放行 ⇒ 配置成功、永不生效。需要一条结构断言把 `Metrics` 与 `satisfies` 的字段集对拍（反射或显式键表）。

---

## 4. 低（P3）

- **P3-1** `LimitsFor` 的注释与实现不符、且位置不对：`resolver.go:93-99` 注释写「`Limits` 返回该等级的限额配置」，方法名却是 `LimitsFor`；`Policy` 的其它方法都在 `types.go`。
- **P3-2** 口径不一致：计划 `:258` 正文「其余 **8 个**非管理键是员工权限」，PM `:29`「非管理类键只有 **15 个**」，而代码 `IsAdminClassPermission` 判出的非管理键是 **8 个**（7 内容 + `user.permission_override`）。三种口径。这不是数字游戏——P0-1 正是从这个缝里漏出来的。

---

## 5. 变异验证（我自己跑的，不是读 backend 的报告）

方法：`go test -overlay=...`，把仓库内 3 个测试文件从编译集里摘掉、注入 reviewer 侧独立测试，**不改仓库任何文件**。

| 步骤 | 命令 | 结果 |
| --- | --- | --- |
| 基线 | `go test -overlay=ov_base.json ./app/Models/TrustLevels/` | `ok ... 0.026s` |
| 变异 | `level.go:43` `activity.LikeReceivedCount` → `activity.LikeGivenCount` | `--- FAIL: TestMetricsIndependentlyRead: 前置失败: 满活动应为 TL4, 得到 3` |
| 判定 | — | **变异敏感成立** |

但要说清楚两点：

1. backend 报的「改坏 → 立刻红」**无法用仓库内测试复现**——19:57 那一刻仓库内 3 个测试文件与实现脱节（11 处编译错误），谈不上红绿。
2. 20:02 之后 `Identity` 半成品，TrustLevels 无法编译 ⇒ 我现在**无法**用仓库内测试重跑这条变异。**M2 落地后必须重跑，并把「红→改回→绿」的原始输出写进 M8 证据**（计划 §12 第 2 条要求）。

另外 `Earnable.Validate` 的守卫是有效的（实测）：`user.ban` / `settings.manage` / 未知键全部拒绝。它漏的只有 `user.permission_override` 这一类**不带后缀特征**的高危键（P0-1）。

---

## 6. 契约漂移预判：94 条里哪些是结构断言，哪些是抽查

**结构性（下一刀绕不过，保留）**：B1/B2/B3/B4/B5/B7（遍历 catalog + 变异）、H5（权限奇偶校验）、H14（import 列表）、H17（值集合交集）、F9（feature 常量数 == catalog 条目数）、A14（逐键快照对比）。

**抽查型（会被绕过，建议升级）**：

| ID | 为什么是抽查 | 建议升级为 |
| --- | --- | --- |
| H11 / H12 | 「构造一个用户看能否到 TL2」是单点抽查；PM §2.5 说「每个 metric 声明来源开关」，但 `Requirements` 没有任何 flag 字段 | `Metrics` 与来源开关一一对应，`satisfies` 读取时按 flag 归零；断言「每个 metric 都有来源开关声明」 |
| B6 | 断言「新增权限键恰好 1 个」，删一个旧的加一个新的也过 | catalog 里 `trust.*` 前缀集合 == `{trust.manage}` |
| H4 | 枚举 4 个文件路径，漏第 5 处派生面抓不到（T16 的坑） | 按 catalog 派生处遍历，集合包含断言 |
| F7 / F8 | 「5 处齐全」是数数 | 常量集合 ⊆ 每个派生集合 |
| H16 | 「全文无裸 `slug`」是字符串搜索，字段改名抓不到；且 `Level.Slug()` 方法名本身就是「裸 slug」的字面来源 | 对外 JSON schema 由结构体标签派生并快照 |
| G1 / G2 / G5 | 只给 p95 阈值，没有基线脚本与测量方法 | 附测量脚本 + 同机同库的前后两次原始数据 |

---

## 7. 判词：现在有几处已经不一致

**9 处**（计划 / 决策 / 实现 / 验收条目四方之间，全部可复核）：

| # | 不一致 | 位置 | 级 |
| --- | --- | --- | --- |
| 1 | 可勾选白名单 7 键（D24）↔ 实际 8 键含 `user.permission_override` | `registry.go:105` ↔ `plans:220-226` | **P0** |
| 2 | 默认阈值 19 值中 17 值不同 | `policy.go:11-61` ↔ PM `:215-223` | **P0** |
| 3 | `accountAge` 分级阈值在数据模型里不存在 | PM `:223`/`:331`(C7) ↔ `types.go:158-168` | **P0** |
| 4 | slug `regular` ↔ 契约冻结的 `resident` | `types.go:35` ↔ `plans:264` / PM `:84`/`:428` | **P0** |
| 5 | bot/系统账号授予：D25 要求 ↔ 接口无 `kind` | `plans:228` ↔ `resolver.go:69` | P1 |
| 6 | `newUserDays` 两个存储位置 | `plans:98`(D2) ↔ `config.go:20` | P1 |
| 7 | `LoadActor` 查询数：PM「2→3」↔ 计划「4→5」↔ 代码实际 +2 | PM `:394` ↔ `plans:26/75` ↔ `resolver.go:29` + `postgres_store.go:412` | P1 |
| 8 | 年龄闸语义：C19 期望窗口内到 TL2 ↔ 代码封顶 TL1 | PM `:343` ↔ `level.go:20-25` / `plans:88` | P1 |
| 9 | 非管理键计数 15 / 8 / 8 三种口径 | PM `:29` / `plans:258` / `identity/permission_class.go:13` | P3 |

**一句话**：M1 的**纯函数内核是干净的**（`LevelOf` 无 DB、无 `time.Now`、无包级可变状态、无 map 迭代顺序依赖，500 次重算零漂移；变异敏感）。问题全在**边界**：白名单的判定方式、默认值与定稿表的对拍、契约里的字段在数据模型里不存在、接口形态装不下 D25。这四条都属于「M2 一落地就固化」的，改晚一步就是 45 个调用点的返工。

## 8. 建议的下一步

1. backend 立刻处理 P0-1（白名单改正列表）——这是唯一有安全后果的一条。
2. P0-2 / P0-3 需要 lead 拍板「以 PM 表为准还是以代码为准」；我建议**以 PM 表为准**，因为计划 §5.3 已经写了这条口径。
3. P1-1 / P1-2 / P1-3 必须在 M2 合并前定，否则接口和预算都要返工。
4. M2 落地后，把 M1 包冻结在某个 sha256 上，我再跑第二轮（含 B 组变异重跑 + 仓库内测试红绿记录）。

## 9. 本轮未覆盖

- M2（`Identity/{policy,postgres_store,types,seeds,trust_levels,permission_class}.go`）——审查时处于半成品（`actor_postgres_tx.go:71: undefined: trust`），按 lead 要求未碰。
- M3 / M4 / M5。
- `trust.levels` 在 Options 的 5 处注册（T16）——尚未落地，属 M2/M4。
- `trust.manage` 尚未进 seeds/迁移（M4）；落地后 `IsAdminClassPermission` 因 `.manage` 后缀会正确挡住它。
