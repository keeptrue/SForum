# 信任等级 — 第二轮审查（M2 收口前）

Status: **1 阻断（基线不成立）+ 1 安全覆盖缺口（D28 零测试）**
Reviewer: 小审
Date: 2026-11-10 20:09–20:20
审查对象: `apps/api/app/Models/TrustLevels/`（P0-1/P0-2/P0-3/D29）、`Identity` 的装载期合并（D22/D23/D28）、`database/queries/identity.sql` + `database/sqlc/identity.sql.go`

---

## 0. 基线：lead 给的信号在我这里不成立

| 项 | lead 给的值 | 我实测 |
| --- | --- | --- |
| 包快照 | `cc970b52…f29191` | `2755637a…d5716b`（20:09:41） |
| `go test -count=1 ./app/Models/TrustLevels/` | 绿 | **build failed**（3 次重跑一致） |
| `go vet ./app/Models/TrustLevels/` | — | **红** |

```
resolver_test.go:50:26: undefined: trustlevels.AccountReader
resolver_test.go:169:62: not enough arguments in call to nilResolver.Grants
	have (context.Context, number, string)
	want (context.Context, int64, string, "time".Time)
```

成因：`resolver.go` 在 **20:09:03** 把 `Grants` 改成 4 参（`ctx, userID, kind identity.UserKind, createdAt time.Time`），测试（20:07:50）还是 3 参 + 引用已删除的 `AccountReader`。**这是本包第三次「报已落但测试编译不过」**（19:57、20:09 各一次，中间 20:01 一次）。

`go build ./...` 绿是真的——`go build` 不编译 `_test.go`。lead 那次 `go test` 又拿到了 `ok (cached)`。

**我的处置**：静态可验项全部做完；变异用 `go test -overlay=` 把 `resolver_test.go` 从编译集摘除（**只摘不补，不动任何断言**），在 `/tmp` 里做。下文每条都标了证据来源。

---

## 1. 已修且我实测通过（P0 三条）

### P0-1 正白名单 ✅ 真修好了

`registry.go:23-52`：`earnableWhitelist` 是 7 键硬编码字面量，`IsWhitelisted` 是唯一判据，`Validate`（`registry.go:104-117`）走它；`permission_class.go:20-24` 后缀表补了 `_override`（纵深防御）。
`registry_test.go:35-60`：`want` 是字面 7 键 + `slices.Equal`，外加 7 个 forbidden 键的**独立**断言（不复用分类谓词）。

变异实测（overlay 摘除 stale resolver_test，其余用仓库自己的测试）：

| 变异 | 结果 |
| --- | --- |
| `earnableWhitelist` 加 `user.permission_override` | **RED** `registry_test.go:47: 白名单 = [topic.create user.permission_override …]，期望字面集合 […]` |
| `earnableWhitelist` 删 `post.delete_own` | **RED** 同上（少一个） |

两条都红，且错误信息直接可读。lead 要求的「长度断言」已被 `slices.Equal` 隐含覆盖（长度是相等的一部分），不必再加一条。

### P0-2 阈值对齐 PM 表 ✅ 逐值核对一致

`policy.go:14-52`：TL1 `3/0/5/0/0/0`、TL2 `15/2/20/5/3/0`、TL3 `45/8/80/30/20/5`、TL4 `100/25/250/100/80/20`，与 `capability-map.md:215-223` **19 个值全部一致**。
附带 D27 也真落地了：`Limits` 里 TL3/TL4 不再是 `TopicCooldownSeconds: intPtr(0)`，全部 `{}`（继承站点默认）。这一条比数字更值钱——原来那版会凭空造出「高等级免冷却」的隐藏行为。

### P0-3 `resident` ✅

`types.go:35` = `"resident"`；`config_test.go` 的 8 处配置字面量也都用 `levelSlug:"resident"`，解码路径有覆盖。

---

## 2. 新发现

### R2-1（P1，安全）**D28 的 deny×earned 没有任何测试**

```
$ rg -l "mergeEarnedPermissions|earnedOnly|trustLevels|TrustLevel" app/Models/Identity/*_test.go
（空）
```

`Identity` 包内**零个测试文件**提到这条链路。现有 deny 测试只有既有的 `roles_test.go:144 TestDirectDenyOverridesRolePermission`（静态 deny，与本刀无关）和 `risk_evaluation_test.go:73`。

也就是说：lead 担心的「用『角色授予 + deny』构造会假绿」这个坑**根本还没被踩，因为压根没有这条测试**。SQL 有了（`identity.sql:60-84` 的 `UNION SELECT … FALSE`）、合并代码有了（`postgres_store_roles.go:338-360` 的 `current.denied[key]` 判断），**但没有一行测试证明 `earned ∖ denied` 成立**。

这是本刀的安全公式（D14/D28），M8 的 A6 就指着它。必须补，且必须按 lead 的口径构造：**deny 一个没有任何静态授予、只靠 earned 才可能拿到的键**。

### R2-2（P1）`created_at` 仍然读两次，R7 只搬了家没消掉

```
postgres_store_roles.go:234  SELECT created_at, kind FROM users WHERE id = $1   ← applyTrustLevel
postgres_store.go:412        SELECT created_at, kind FROM users WHERE id = $1   ← LoadActor
```

`applyTrustLevel` 已经把结果写进 `current.CreatedAt` / `current.Kind`，而 `LoadActor` 在 `GetCurrentUser` 之后又原样查了一遍同一行。G 组预算是 4→5，现在是 4→6。

修法很轻：`LoadActor` 直接读 `current.CreatedAt` / `current.Kind`，删掉 `postgres_store.go:410-416` 那段查询（注意保留 `ValidUserKind` 的兜底，它已经在 `applyTrustLevel:240-244` 做过了）。

### R2-3（P1）两个「同义」哨兵其实是两个值，`errors.Is` 永远不匹配

```
trustlevels/resolver.go:14      var ErrDisabled = errors.New("trustlevels: disabled")
identity/trust_levels.go:13     var ErrTrustLevelsDisabled = errors.New("identity: trust levels disabled")
identity/trust_levels.go:47     case errors.Is(err, ErrTrustLevelsDisabled):   // ← 匹配不上上面那个
```

resolver 的注释写「这里导出一个 identity 侧的同义哨兵」——`errors.New` 调两次就是两个不同的值，`errors.Is` 跨两者为 false。

后果：resolver 在「依赖没装齐」（`r == nil || activity == nil || policy == nil`）时返回 `trustlevels.ErrDisabled`，identity 侧认不出来 → 落到 `err != nil` 分支 → **每次请求打一条 warn**。D9/F 组明确要求关闭态「静默保持未解析，不写 warn」。

修法一行：`var ErrDisabled = identity.ErrTrustLevelsDisabled`（`trustlevels` 本来就 import `identity`，方向合法）。

### R2-4（P2）D25 只落了 kind 半条，系统账号半条没落

`resolver.go:73-75` 判 `kind != "" && kind != identity.UserKindHuman` → 返回 0/空集。但：

- `builtin-auto`（`bootstrap/builtin_plugin_auto_update.go:133`）与 `dev-auto`（`Extensions/dev_auto_runtime.go:29`）创建时**没有设置 kind**；
- `database/migrations/202610020003_user_kind.sql:9`：`kind TEXT NOT NULL DEFAULT 'human'`。

⇒ 这两类系统账号是 `human`，会正常参与等级判定并挣到等级。D25 点名的「系统账号（`builtin-auto` / `dev-auto`）直接返回 0」没有实现，也没有测试。

### R2-5（P2，M3 必查）bot 返回的是「已解析的 TL0」而不是「未解析」

`resolver.go:75` 对 bot 返回 `(0, nil, nil)`——err 为 nil ⇒ `resolveTrustGrants` 返回 `ok=true` ⇒ `trustLevelResolved = true`、`TrustLevel = 0`。

按 D2，`level == 0` 会套 `trust.new_user_*` 的新人限额。今天没炸是因为 Forum 的 `trustForActor` 有 `IsBot()` 旁路（T15 判据 2）。**M3 把 `trustForActor` 改成 `limitsForActor` 时，这条旁路必须原样保留**，否则 bot 会被新人限额限流（冷却 300s / 日限额 3）。建议在 M3 的验收里点名。

### R2-6（P3）`EligiblePermissions` 对「白名单里有、目录里没有」的键静默丢弃

`registry.go:136-146`：`if !known[key] { continue }`。当前被 `registry_test.go:47` 的字面集合断言兜住了（少一个就红），但那是测试的功劳，不是函数的。若将来白名单与目录漂移，后台只是少一个勾选框、无人报错。建议加一条 `len(EarnableWhitelist()) == len(EligiblePermissions())` 的自检。

---

## 3. sqlc 手工同步：形状正确，无手工错误

比对方法：把 `database/queries/identity.sql:46-84` 的块按 sqlc 规则剥注释，与 `database/sqlc/identity.sql.go:396-431` 的 const 逐字对比。

**唯一差异**：`.sql` 结尾 `ORDER BY 1;` ↔ 生成物 `ORDER BY 1`（sqlc 会剥掉语句末尾分号）。这是 sqlc 的正常行为，不是手工失误。

形状核对（与 sqlc v1.27 的 `:many` 模板一致）：

```
type ListUserPermissionsRow struct { Key string; Effective bool }
func (q *Queries) ListUserPermissions(ctx, userID int64) ([]ListUserPermissionsRow, error)
    rows, err := q.db.Query(ctx, listUserPermissions, userID)
    for rows.Next() { rows.Scan(&i.Key, &i.Effective) }
    rows.Err()
```

结论：**这次手工同步是忠实的**，没有引入手工错误。

真正的风险不在这次，在下一次：仓库现状的 `models.go` 是 84 行、`sqlc generate` 会产出 2481 行 + 一批 param 改名。这意味着**这个仓库已经不能安全地跑 `sqlc generate` 了**。建议二选一，并写进决策记录：① 把 sqlc 版本钉进 CI（`sqlc vet` + `sqlc diff` 只跑不改）；② 明确声明「生成物由人工维护」，删掉 generate 脚本，避免下一个人跑一次就炸出一个千行 diff。

---

## 4. lead 点名的两条

**① D28 的 deny 测试构造方式** → 见 R2-1：**不存在这条测试**，所以不存在「假绿」——是零覆盖。补的时候必须用「无静态授予、只靠 earned」的键（例如给 TL4 配 `topic.create`、DB 里 deny `topic.create`、且用户角色**没有** `topic.create`）。

**② P0-1 的测试是不是真能红** → 见 §1：两种变异都红，且 `user.permission_override` 那条是独立断言（`registry_test.go:51-57` 的 forbidden 列表），不是从 `EligiblePermissions()` 回环派生。

---

## 5. D18 依赖方向核查（lead 追加项）

| 检查 | 结果 |
| --- | --- |
| `Identity` 是否 import `trustlevels` | **否** ✅（`rg "Models/TrustLevels\|trustlevels" app/Models/Identity/` 只命中 `permission_class.go:8` 的一句注释） |
| `TrustLevelSource` 签名是否与实现一致 | **是** ✅ 两侧都是 `Grants(ctx, userID, kind UserKind, createdAt time.Time)` |
| `AccountReader` 是否被 Identity 反向依赖 | **不存在**：生产代码里已删除，只剩 `resolver_test.go` 的 8 处死引用（即 R2-1 的编译错误来源） |
| `TrustLevels` → `identity` 单向依赖 | ✅ `resolver.go:8`，方向合法（identity 是被依赖方） |

---

## 6. 结论与下一步

**已修好的**：P0-1（含变异）、P0-2（逐值）、P0-3、D27、D29 的签名形态、D22 的双 loader 共用 `resolveTrustGrants`、sqlc 手工同步。

**必须收口的**：
1. **R2-1（安全）**：补 D28 的 `earned ∖ denied` 测试，按「无静态授予 + 只靠 earned」构造。这是唯一有安全后果的缺口。
2. **R2-2**：删掉 `LoadActor` 里那次重复的 `created_at/kind` 查询。
3. **R2-3**：把两个哨兵合成一个值。
4. **基线**：`resolver_test.go` 修到 4 参 + 删 `AccountReader` 引用，`go vet` 与 `go test -count=1` 双绿后再报冻结。

**未取证（不算缺陷，但 M8 前必须补）**：bootstrap 装配（`rg TrustLevel bootstrap/` 仍零命中，含 D31 开关短路）、`features.trust_levels` 5 处注册、`trust.manage` 迁移与 seeds、D30 改名、D26 `RestrictActor`、4×4 真值表、D6/D15 变异、H15、Forum M3/M4。

---

## 7. 补记（20:14–20:17，读完 bootstrap wiring 后重新取证）

### 7.1 撤回：本报告 §6 关于 D31「bootstrap 零命中」的结论是错的

先纠正来源：那句话**不是我的 grep**，是我转述 lead 上一封消息里的「`bootstrap/` 对 `TrustLevel` 零命中」，没有自己跑一遍就写进了报告。按 §14 第 3 条（信号 = 消费者自己跑出来的 + 自己取的 hash），这是我的错。

实测（20:14:18）：

```
rg -c "TrustLevel|trustlevels" bootstrap/
  bootstrap/trust_levels_wiring.go:20
  bootstrap/api_assembly_infrastructure.go:2
  bootstrap/api_assembly_extensions.go:1
  bootstrap/host_command_runtime.go:2
  bootstrap/api_assembly_domains.go:1
```

`bootstrap/trust_levels_wiring.go` 已落地，且质量高于预期：

| 检查 | 结果 |
| --- | --- |
| `newTrustLevelSource` 调用点 | **全仓 1 处**（`api_assembly_infrastructure.go:296`）✅ |
| D31 开关短路位置 | `trust_levels_wiring.go:31`，在 `WebOption(NameTrustLevels)` **之前** ✅ |
| 接口一致性 | `var _ identity.TrustLevelSource = (*trustlevels.Resolver)(nil)` 编译期断言 ✅ |
| activity 适配器 | 逐字段引用 `Badges.ActivityCounters`（编译期检查，D18/H15）✅ |
| `trust.levels` 5 处注册 | definitions / default / normalize / allOptionNames / feature flag 均在 ✅ |

**改判：D31「已落地，待性能实测」**，不是「未取证」。

### 7.2 D31 的一句话精度补充（不是缺陷）

`applyTrustLevel`（`postgres_store_roles.go:228-238`）在调用 `resolveTrustGrants` **之前**先执行 `SELECT created_at, kind FROM users`。所以「关闭时不发任何 TL 查询」只对 `trust.levels` 与 `user_activity` 成立；这条 `created_at/kind` 在开关关闭时**仍会发**。

它不是浪费（M3 的新人阶梯要用 `CreatedAt`/`Kind`），但 G 组的 A/B 拿不到「TL 的全部开销」的干净 Δ —— 测出来的是「TL 减去这一条查询」。报告口径建议写成「关闭时 TL 新增查询 = 0，共享查询 1 条」。

### 7.3 R2-1 改判：D28 的测试已经补上了，构造正确，Go 层有牙；但 **SQL 层无覆盖**

`Identity/trust_levels_merge_test.go`（20:16 出现）的构造正是要求的那种：

```go
current := &CurrentUser{ID: 7, Permissions: []string{"post.create"}}   // topic.create 无静态授予
decisions: {Key: "topic.create", Effective: false}                      // 只被 deny
earned := mergeEarnedPermissions(current, []string{"topic.create", "attachment.upload"})
```

两条变异实测：

| 变异 | 结果 | 说明 |
| --- | --- | --- |
| M4a 删掉 `mergeEarnedPermissions` 里的 `current.denied[key] ||` | **RED** `trust_levels_merge_test.go:50: 被 deny 的 topic.create 被已挣得集合复活了` | Go 层有牙 ✅ |
| M4b 删掉 `database/sqlc/identity.sql.go` 里 `UNION SELECT denied_permission_keys.key, FALSE …` 整段 | **GREEN** | **SQL 层零覆盖** ❌ |

M4b 绿的含义：`TestDeniedPermissionKeysIncludesDenyOnlyKeys` 只测**消费方**（纯函数），没有任何测试证明**查询真的会把 deny-only 键返回**。谁把那段 `UNION` 删了，全部测试仍然是绿的，而 D28 的洞原样复活。

补法（按代价排序）：① 集成测试：真库造一条 deny-only 行，跑 `ListUserPermissions`，断言键出现且 `Effective=false`；② 退而求其次：对 `listUserPermissions` 常量做形状断言（含 deny-only 段），弱但比没有强。

### 7.4 R2-3 改判：两个哨兵已经合成一个值

实测（overlay 注入的探针）：

```
trustlevels.ErrDisabled               = "identity: trust levels disabled"
identity.ErrTrustLevelsDisabled       = "identity: trust levels disabled"
errors.Is(trust, ident) = true ; errors.Is(ident, trust) = true ; same pointer = true
```

⇒ `resolveTrustGrants` 的 `case errors.Is(err, ErrTrustLevelsDisabled)` 现在会命中，关闭态走静默分支。**R2-3 已修**。

仍缺的是**断言**：没有任何测试证明「关闭态不写 warn」。lead 要求的那条变异（policy 返 `ErrDisabled` + `slog` handler 捕获 warn）仍然要做，否则下次谁把哨兵改回 `errors.New` 都不会红。

### 7.5 撤回：D30 内层 `if` 是**对的**，我上一轮判断错了

`permission_origin.go:44-50` 的分支：

```go
if a.Permissions != nil && hasExpandedPermission(a.Permissions, permission) {
    if a.Earned[permission] { return PermissionOriginEarned }
    return PermissionOriginStatic
}
```

我上一轮说「`Earned` 只存独占键 ⇒ 该分支不可命中 ⇒ 注释与代码相反」。**这是错的。** `mergeEarnedPermissions`（`postgres_store_roles.go:346-359`）会把 earned 键 **append 进 `current.Permissions`**，同时记进 `earnedOnly`：

```go
current.Permissions = append(current.Permissions, key)   // :352
earnedOnly[key] = true                                   // :356
```

所以 `Earned ⊆ Permissions`，`hasExpandedPermission` 为真，内层 `if` **必然可达**，返回 `Earned` 是**正确**行为。

**结论：这个分支不能删。** 按我上一轮的建议「删掉内层 `if` 或加不可达断言」，会让所有已挣得键都报 `static`——一个真实回归。请勿执行那条修改建议；D30 的「注释错」已由新注释（`:43-47` 说明可达性）修好，代码无需改动。

我犯错的原因：读 `GrantSource` 时用的是 D23 之前的形状假设（`Earned` 与 `Permissions` 分离），而同一轮我明明读过 `mergeEarnedPermissions` 的 append，没有把两处接上。

### 7.6 `types.go` 的 `CapabilitySource` 注释已修

`rg "CapabilitySource" app/Models/Identity/types.go` → 无命中。⑤ 已闭环。

### 7.7 本报告基线更新

```
20:15:04  TrustLevels 包 sha256 = bd6dc26aa92a2966c4570cb8db008bd7ed643fa322265e23ee5c6a733ba6666c
          go vet 干净 / go test -count=1 → ok
20:16:21  Identity: permission_origin.go 40b369612aee6b9c / postgres_store_roles.go b9b514aa7d5c122e
          trust_levels.go 12b50c927fcabd32 / trust_levels_merge_test.go 9e59dc60af786bec
          sqlc/identity.sql.go e5704e06a5a212f8
          go vet 干净 / go test -count=1 -run 'TestMerge|TestDeny|TestResolve' → ok
```

**M2 此刻处于可测试的绿状态**（这是本刀第一次）。三条变异（M1/M2/M3）全部在真包上跑、无需 overlay 摘文件。
