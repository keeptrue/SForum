# 信任等级 — 第三轮（M2 收口验证）

Status: **5 项全过，2 条假绿风险，1 条验收条目本身要改**
Reviewer: 小审
Date: 2026-11-10 20:18–20:26
判据: 自己跑 `go vet` / `go test -count=1` / 自己取 hash，不采信上游报告

## 0. 基线（我自己取的）

```
go vet ./app/Models/Identity/ ./app/Models/TrustLevels/            → 干净
go test -count=1 ./app/Models/TrustLevels/                        → ok 0.013s
go test -count=1 -run 'TestMerge|TestDeny|TestResolve' ./app/Models/Identity/ → ok
./scripts/test-api-isolated.sh ... -count=1 -run TestListUserPermissionsReturnsDenyOnlyKeys → ok 0.144s（非 cached）
```

变异运行器：`/tmp/tlreview/mutate.sh`（`go test -overlay`，不改工作树）。它自己有三条护栏，都实测过会误报：

| 护栏 | 自测 |
| --- | --- |
| 锚点未命中 → `NOT_APPLIED` | 故意写错锚点 → 退出码 2，不会静默空转 ✅ |
| 变异体编译不过 → `BUILD_FAIL` | 哨兵变异最初漏了 `errors` import → 报 `BUILD_FAIL` 而不是「红」 ✅ |
| 测试正则一条没匹配 → `NO_TESTS_RAN` | — |

---

## 1. A（D28 SQL 层）：通过，且对照组有牙

| 步骤 | 命令 | 结果 |
| --- | --- | --- |
| 基线 | `test-api-isolated.sh ... -count=1` | `ok 0.144s`（非 cached） |
| M4b 删掉 `UNION SELECT denied_permission_keys.key, FALSE …` | overlay | **RED** `:92 deny-only 键没有出现在 ListUserPermissions 结果里` |
| M4c 把 `NOT EXISTS(...) AS effective` 改成 `AND FALSE AS effective`（让一切都不生效） | overlay | **RED** `:115 allow 覆盖没有生效：查询形状坏了` |

M4c 是关键：它证明**对照组有牙**——不是「只测拒绝不测允许」那种假绿形态。对照组（`:98-116`，只 allow 的键必须 `effective=true`）真的会在「查询把什么都判成不生效」时红。

**A 结论：通过。** 构造用的是「只有 deny override、无任何角色授予」的键，正是要求的那种；对照组存在且有效。

### 1.1 但发现一个工具层问题：`test-api-isolated.sh` 默认不加 `-count=1`

我第一次跑基线（不带 `-count=1`）拿到的是：

```
ok  github.com/keeptrue/sforum/apps/api/app/Models/Identity  (cached)
```

**cached**。也就是说 §14 第 2 条对**这个脚本**同样成立，而脚本本身不兜。任何人用 `./scripts/test-api-isolated.sh` 当证据，都可能拿到上一版绿。建议 ops 在脚本里给 `go test` 固定加 `-count=1`（`scripts/` 是 ops 的文件域，我不动）。

## 2. B（关闭态静默）：通过，但静默测试本身对「哨兵分叉」不敏感

| 步骤 | 结果 |
| --- | --- |
| 哨兵变异（`var ErrDisabled = identity.ErrTrustLevelsDisabled` → 独立 `errors.New`） | **RED** `trust_levels_sentinel_test.go:18: 不是同一个值` ✅ |
| 对照组（`:41-49`）`DeadlineExceeded` 必须写 warn | 存在，且用 `buf.Reset()` 后断言 warn 文本，是真断言不是只看返回值 ✅ |

**假绿风险（P2，值得记）**：同一个变异下，`TestResolveTrustGrantsDisabledIsSilent` **是绿的**。原因：它喂的是 `silentTrustSource{err: ErrTrustLevelsDisabled}`（identity 侧自己的值），而生产路径喂的是 `trustLevelPolicySource.Policy` 返回的 `trustlevels.ErrDisabled`。两条值一旦分叉，行为测试照样绿，只有指针测试会红。

今天安全（两条都在）。风险在于「指针测试看起来像实现细节测试」——下一个人删它的时候，行为测试会继续绿着放行，而线上每请求一条 warn。建议在 `trust_levels_disabled_silent_test.go` 加一句交叉引用（一行注释，成本极低）：本测试对哨兵分叉不敏感，那条由 `trust_levels_sentinel_test.go` 守。

## 3. 4×4 真值表：通过，真 16 组

`trust_levels_merge_test.go:182`：四个嵌套 `[]bool{false,true}` 循环（static × earned × deny × super）= 16，末尾 `if combinations != 16 { t.Fatal }` 兜住「以后被谁改成挑几组」。

每行同时断言三件事：
- `actor.Can(permission) == wantCan`
- `actor.GrantSource(permission) == wantOrigin`
- `(wantOrigin != None) == wantCan`（期望值自身的一致性）

不是「4×4 里挑 6 组」，也不是「只断言 Can、GrantSource 空话」。**通过。**

一个观察（不是缺陷）：该表手工构造 `Actor{Permissions, Earned}`，测的是 `Can`+`GrantSource` 的组合语义，不测装载器；装载器由 M4a 那条独立覆盖。分层是对的。

## 4. import cycle 纪律：遵守

`trust_levels_merge_test.go` 是 `package identity`，import 只有 `fmt / slices / testing / store`——**没有** `trustlevels`。✅

## 5. `created_at` 单次读：通过

```
$ rg -c "SELECT created_at, kind FROM users" app/Models/Identity/
app/Models/Identity/postgres_store_roles.go:1
```

全包**一处**（`applyTrustLevel`），`LoadActor` 的重复读已删。G 组口径按「关闭时 TL 新增查询 = 0，共享查询 1 条」写。

---

## 6. 顺带评估：`202611100004_trust_manage_permission.sql`

**可逆性：成立。** Up 是纯增量（`INSERT ... ON CONFLICT DO NOTHING`），Down 删两张表里的对应行；迁移前 `trust.manage` 不存在，所以 Down 不会误删历史授予。

**四处落地核对**（PM 的 H4）：

| 位置 | 状态 |
| --- | --- |
| `Identity/seeds.go:84,144` | ✅（`Module: "identity"`，与迁移一致） |
| `database/migrations/202611100004_…sql` | ✅ |
| `apps/web/app/config/roleTemplates.ts:69` | ✅（与迁移的 `operator` + `super_admin` 一致） |
| i18n 双语 | ❌ 不存在 |

**但第四处不是缺陷，是 PM 的验收条目写错了**：`badge.manage` 同样不在 `apps/web/i18n/` 里（全仓 `rg badge\.manage apps/web` 只命中 `adminModules.ts` / `roleTemplates.ts` / `pages/admin/badges.vue` 的注释）。权限键的显示文案走 DB 的 `permissions.description`（seeds.go:144 已给），不是 i18n。

⇒ **H4 的「i18n 双语」这一项会产出一个假 FAIL**。建议改成「seeds / 迁移 / roleTemplates / 权限目录描述」四处；i18n 的要求属于**档位名**（H6/H17），不属于权限键。

**Down 段注释与 SQL 不符（lead 指的那处，确认存在）**：

```sql
-- 只拆这条迁移自己加的那把钥匙：不触碰运营者后来手动授予的其它角色。
DELETE FROM role_permissions WHERE permission_key = 'trust.manage';
```

SQL 删的是**该键的全部授予**，包括迁移之后运营手动授予给自定义角色的。注释说的「只拆自己加的那把」不成立。

**SQL 是对的、注释是错的**：下一句要 `DELETE FROM permissions WHERE key='trust.manage'`，残留的 `role_permissions` 会违反外键，所以必须先把该键的全部授予清掉。建议改注释为：

> Down 会删除 `trust.manage` 的**全部**角色授予（含迁移之后手动授予的），因为权限行本身要被删除、残留授予会违反外键；其它权限键的授予不受影响。

---

## 7. 结论

| 项 | 判定 |
| --- | --- |
| A D28 SQL 层 + 对照组 | **通过**（基线非 cached、两个变异一红一红） |
| B 关闭态静默 + 对照组 | **通过**（哨兵变异红、对照组真断言 warn） |
| C 4×4 真值表 | **通过**（16 组、双断言） |
| D import cycle 纪律 | **通过** |
| E `created_at` 单次读 | **通过** |
| 迁移可逆性 | **通过** |
| 待改的验收条目 | H4 去掉「i18n 双语」（假 FAIL） |
| 待补的注释 | 迁移 Down 段注释；静默测试加一条交叉引用 |

**M2 可以视为收口。** 剩下两条都是「改文字」级别，不挡 M3。

## 8. 本轮未覆盖

M3（Forum 收编）、M4（HTTP 契约）、M5（前端）、`features.trust_levels` 的 5 处目录注册完整性测试（F8/F9）、D32 前后端展开表三腿测试、D30 的 `GrantSource` 与 `hostapi.CapabilitySource` 无同名残留（当前 `permission_origin.go` 命名已对，未做全仓扫描断言）。
