# 信任等级 — M8 验收（reviewer 部分）

Status: **2 阻断（验收矩阵本身会产出假 PASS）+ 4 处变异全过 + 浏览器 1/3 通过、2/3 未取证**
Reviewer: 小审
Date: 2026-11-10 20:58–21:2x
证据根：`storage/qa/trust-levels/`

## 0. 基线（自取，带时间）

```
20:58:29  sha256 TrustLevels/*.go | sort | sha256sum = 1dda35344a2717669db2c6d3391de5a2
          （与 lead 给的 1dda3534… 逐位一致）
go vet   ./app/Models/{TrustLevels,Identity} ./app/Http/Controllers/TrustLevels  → 干净
go test -count=1 ./app/Models/TrustLevels/ ./app/Http/Controllers/TrustLevels/   → ok / ok
```

---

## 1. 阻断一：验收矩阵引用的 30 个测试名，29 个在全仓不存在

矩阵的「怎么验」列大量写成 `go test ... -run 'TestXxx'`。实测：

| 项 | 数 |
| --- | --- |
| 矩阵引用测试名 | 30 |
| 全仓实际存在 | **1**（`TestEverySeededPermissionReachesTheDatabase`） |
| 不存在 | **29** |

而 `go test -run` 匹配不到任何测试时**退出码是 0**：

```
$ go test -count=1 -run 'TestEarnableDisjointFromAdmin' ./app/Models/TrustLevels/
ok   …  0.016s [no tests to run]
EXIT=0
```

⇒ 任何按矩阵字面写脚本、以退出码或 `ok` 判 PASS 的实现，会把这 29 条**全部记成 PASS**，而一条都没跑。这是本刀第九次「绿了但没测」，形状与 §14 那两次同源（绿的含义没被界定）。

真实名字与矩阵名字的对应（部分，供 ops 改脚本）：

| 矩阵写的 | 实际存在的 |
| --- | --- |
| `TestEarnableDisjointFromAdmin`（B1–B2） | `TestEarnableValidateCoversWholeCatalog`（更强：全 catalog 双射） |
| `TestEligibleExactlySeven`（B10） | `TestEligiblePermissionsIsLiteralWhitelist` |
| `TestDefaultEarnableEmpty`（B8） | `TestDefaultEarnableIsEmpty` |
| `TestLevelBoundary`（C 组） | `TestLevelBoundaries` |
| `TestCanDenyOverridesEarned`（A6） | `TestMergeEarnedPermissionsHonoursDeny` |
| `TestCan*`（A1–A14） | `TestCanTruthTable`（16 组，含 GrantSource） |

**建议**：矩阵的「怎么验」列不要手写测试名，改为 `go test -list '.*' <pkg>` 的真实输出；脚本里加一条硬断言——`-run` 之后必须看到 `=== RUN`，否则记 `SKIP/未取证` 而不是 PASS。

证据：`B/B0-matrix-testname-mismatch.txt`

## 2. 阻断二：DB 门禁测试会静默 SKIP，而 `go test` 仍报 ok

```
$ go test -count=1 -v -run 'TestUpdateTrustLevelsPersistsNormalizedConfig' ./app/Models/Options/
--- SKIP: TestUpdateTrustLevelsPersistsNormalizedConfig (0.00s)
PASS
$ echo $?   # 0
```

⇒ 不带 `SFORUM_TEST_DATABASE_URL`（即不走 `./scripts/test-api-isolated.sh`）时，这一批测试**一条都没跑**，输出仍是 `ok`。与 §14 的 cached 是同一类：**绿的含义没被界定**。

**建议**：验收脚本对 DB 门禁测试必须同时满足「走了 isolated harness」+「日志里没有 SKIP」+「日志里有 `=== RUN`」。

## 3. 我负责的 4 处变异：全部按预期红，红/绿两份都在

### 3.1 B3/B4 白名单塞危险键 —— 有牙，但**矩阵那行的读法是歧义的**

变异体：`registry.go` 的 `earnableWhitelist` 追加 `user.ban` / `moderation.review`（`go test -overlay`，不改工作树）。

| 跑哪个测试 | 结果 |
| --- | --- |
| `TestEarnableValidateCoversWholeCatalog`（矩阵称之为「B1」） | **ok（绿）** |
| `TestEligiblePermissionsIsLiteralWhitelist` | **FAIL（红）** |
| 改回（无 overlay） | ok（绿） |

含义：矩阵写「跑 B1 必须红」，但按字面跑那个「遍历 catalog 的结构断言」是**绿的**——因为它的判据是「Validate 接受与否与白名单一致」，白名单多一个键时两边仍然一致。真正挡住变异的是**字面集合**那条测试。

⇒ 矩阵 B3/B4 的判据应改为「`TestEligiblePermissionsIsLiteralWhitelist` 必须红」，并注明「catalog 双射测试对『白名单被扩』不敏感」。否则执行者会得出「守卫无效」的错误结论，或者反过来把绿当通过。

证据：`B/B3-mutation-red.txt`、`B/B3-restored-green.txt`

### 3.2 H15 activity 适配器是编译期检查 —— 通过，但**天真的变异会给出错误证据**

第一次变异（只改 `ActivityCounters.TopicCount` 字段名）→ `go build` 红，但错误全在 `Badges` 包内部，**没有一条指向适配器**：

```
/tmp/…/activity_mut.go:78:12: c.TopicCount undefined
app/Models/Badges/activity_store.go:66:31: counters.TopicCount undefined
BUILD_EXIT=1
```

这不能证明 H15 的主张（「字段漂移会让适配器编译失败」），因为 Badges 自己先炸了，编译根本没走到 bootstrap。

正确变异（`Badges` 内部同步改名，**adapter 不动**）→ 唯一的错误正是适配器：

```
bootstrap/trust_levels_wiring.go:69:31: counters.TopicCount undefined
BUILD_EXIT=1
```

改回 → `go build ./...` 退出 0。

证据：`H/H15-red.txt`、`H/H15-green.txt`

### 3.3 `normalizeOptionValue` 的 `case NameTrustLevels` 删除 —— 通过

变异：删掉 `service_normalize.go:185-189` 整个 case（overlay），在 isolated DB 上跑：

```
--- FAIL: TestUpdateTrustLevelsPersistsNormalizedConfig
    trust_levels_options_write_integration_test.go:66: Update(trust.levels) 失败：options: invalid option（D-8 的症状就是这里返回 ErrInvalidOption）
--- FAIL: TestRestoreDefaultsPersistsRecommendedConfig
FAIL
```

绿态（同一 harness，无 overlay）：`ok 0.146s`。

注意：这两条测试在**不带 DB** 时是 SKIP（见 §2），所以「绿」必须来自 isolated harness，否则无效。

### 3.4 控制器权限顺序 —— 通过（静态核对）

`app/Http/Controllers/TrustLevels/controller.go`：

```
151:  actor, err := h.requireAdmin(c, identity.PermissionTrustManage)
156:  if err := c.Bind().Body(&request); err != nil {
```

`requireAdmin` 在 `Bind()` **之前**，且写路径直接判 `trust.manage`（不是只判 `admin.access`）⇒ 未授权用户无法通过「非法 body 换 422」探测配置格式。

## 4. 浏览器三项：1 通过、2 未取证

### 4.1 他人视角（I-03/I-04）—— **通过（载荷层）**

匿名 `GET http://127.0.0.1:3000/u/switchprobe` → 200。SSR 载荷中 `trustLevel` 的值为 `{"level":<int>,"levelSlug":"member"}`，全文 `progress` / `bottleneck` 出现次数均为 **0**。
⇒ 他人视角只有 `level` + `levelSlug`，无 progress / bottleneck / 剩余量。证据：`I/I-03-I-04-otherview.txt`

### 4.2 F4（关开关 → 资料页不渲染等级卡）—— **未取证，且当前会「因错误的原因通过」**

`apps/web/app/composables/profile/useTrustLevel.ts:120` 仍是 `computed<TrustLevelView | null>(() => null)`；`SFProfileRightRail.vue:74` 是 `v-if="props.trustLevel"`。数据源没接 ⇒ 等级卡**在任何开关状态下都不渲染** ⇒ F4 必然「通过」，但它证明不了开关语义。

解除条件：`useTrustLevel` 接真实数据源 + 提供会话；届时必须**先证明开开关时渲染、再证明关开关时不渲染**。证据：`F/F4-NOT-EVIDENCED.md`

### 4.3 I-22 / I-23（后台阈值页 / 390×844）—— **未取证**

缺 `admin.access + trust.manage`（戊）的已登录会话；dev 库里没有矩阵 §1.2 的 QA 账号（只有 owner + `seed_*`）。`tests/browser-admin-390x844.mjs` 已具备 390 视口与溢出检测，只差 `SFORUM_SESSION_COOKIE`。
解除条件与命令见 `I/I-22-NOT-EVIDENCED.md`、`I/I-23-NOT-EVIDENCED.md`。

补充判据（防假绿）：I-22 点 `restoreDefaults` 后必须断言 **PUT 请求数为 0**，否则「表单回填」不能证明「没有自动保存」。

## 5. pend 合理性判断：**本轮无法执行**

`storage/qa/trust-levels/_summary.txt` 不存在（运维清单未落）。没有清单就无法逐条判断「解除条件是否成立」。
**这是本轮唯一一项按指示应做而未做的**，原因是输入缺失，不是没跑。

## 6. 顺带一条契约判断（lead 问的 `earnable: null`）

`earnable` 是 `[]string` 语义的列表字段，但 Go 侧 nil map/slice 序列化成 `null`。建议**公开响应统一成 `[]`**：
- OpenAPI 里它是 `type: array`，`null` 与 schema 不符；
- 前端 `for (const k of earnable)` 遇到 `null` 会抛；
- 已有 `limits` 用 `omitempty` 做「键不出现」，`earnable` 却是「键出现但值为 null」——同一份响应里两种「空」的表示法。

## 7. 结论

| 项 | 判定 |
| --- | --- |
| 矩阵测试名 | **阻断**（29/30 不存在，假 PASS 通道） |
| DB 门禁测试 SKIP | **阻断**（假 PASS 通道） |
| B3/B4 变异 | 通过（但矩阵判据需改成字面集合那条） |
| H15 变异 | 通过（红/绿两份，错误精确指向适配器） |
| normalize case 变异 | 通过（红/绿两份，均在 isolated DB） |
| 控制器权限顺序 | 通过 |
| 他人视角 | 通过（载荷层） |
| F4 / I-22 / I-23 | **未取证**（缺会话；F4 另有「因错误原因通过」的风险） |
| pend 合理性 | **无法执行**（`_summary.txt` 缺失） |

---

## 8. 复跑（21:1x–21:2x）：两个阻断已解除，F4 转通过

### 8.1 阻断一（矩阵测试名）—— **已解除，我复验过**

矩阵已重写（`21:05:09`，26314 字节）。我的复验方式与之前不同：**只提取可执行形式 `T: ^Name$`**，不把「旧名 → 真名」的对账表算进去。

```
可执行 T: ^Name$ 引用: 37
全仓存在: 37
缺失: 0
```

之前那 30 个不存在的名字，现在只出现在 §7 的对账表里（例如 `TestCanStatic` → `TestCanTruthTable`、`TestLevelIsPure` → **不存在 → PEND**），不再出现在任何可执行行。**这是正确的修法**：保留错误记录 + 给出真名，而不是把错误记录删掉。

### 8.2 阻断二（DB 门禁 SKIP 记 PASS）—— 判据已改

矩阵新增 R1–R5，其中 R3 把 DB 依赖条目统一改成 `./scripts/test-api-isolated.sh ./pkg/ -run '^Name$'`，SKIP 记 SKIP。与我的证据（`--- SKIP` + `PASS` + exit 0）一致。

### 8.3 F4 —— **通过（复跑，数据源已接）**

判据改为「开开关必须渲染」后，用匿名 SSR 实测（他人视角，`isSelf=false`）：

| 状态 | 卡片标记 | 档位名文本 | `progressbar` |
| --- | --- | --- | --- |
| 开（原值 `enabled`） | 命中 | `>正式用户<` | **0** |
| 关（`disabled`，缓存 ~4s 后） | **0** | 0 处 | — |
| 恢复 `enabled` | 命中 | `>正式用户<` | 0 |

⇒ 开→渲染、关→不渲染、恢复→再渲染，三态都验过；他人视角无进度块。**F4 通过。**
开关已恢复为 `enabled`（复跑后实测确认）。

**仍未取证**：本人视角的进度块（需甲登录 + `viewer`）。判据两条（有 `viewer` 渲染进度 / 无 `viewer` 无进度块）都未验。

### 8.4 E5（`Level()` 纯度）—— 已把第一轮手工证据挂上

见 `storage/qa/trust-levels/E/E5-purity-reviewer.md`。四条独立证据（无 DB / 无 `time.Now` / 无包级状态依赖 / 500 次零漂移）+ 一条变异敏感性证据。
诚实标注：这是**手工证据**，不是断言，代码改动后不会自动变红；PEND 的解除条件仍是补一条把输入输出表固化的测试。
另：`Metrics` 仍是导出的可变 `[]string`（第一轮 P2-1，未修），它不影响 `LevelOf`，所以 E5 成立，但「纯」的边界应包含不可变。

### 8.5 复跑后的结论

| 项 | 判定 |
| --- | --- |
| 矩阵测试名（阻断一） | **已解除**（37/37 可执行名存在） |
| DB SKIP 记 PASS（阻断二） | **判据已改**（R3） |
| F4 | **通过**（三态） |
| E5 | PEND，但已有手工证据 |
| I-22 / I-23 / 本人视角进度 | **未取证**（缺戊/甲会话） |
| pend 合理性判断 | **仍无法执行**（`_summary.txt` 不存在） |
