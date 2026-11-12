# M3.2 独立验收矩阵：Host 命令 `badges.fact.record`

Status: **accepted（逐条表全部 ok / FAIL 0 / pend 0）** —— **脚本口径按条件并列**：
- `①` 基线（无 `--mutate`）= `ok 19 / FAIL 0 / pend 1`（pend = 变异未跑）；
- `②` 无 DB/会话 + `--mutate`（首轮）= `ok 33 / FAIL 0 / pend 0`（14/14 变异，无覆盖自检）；
- `③` **`--mutate`（最终口径）= `ok 34 / FAIL 0 / pend 0`**（`mutations/fact-record-full-mutate-after-coverage-fix.log`；`ok 变异覆盖：14/14 条全部执行`，`EXIT=0`）。

GAP-M32-1/M32-2 补口后复跑于 **2026-11-12T21:09+0800**，代码基线 **`4e1876331`**（`apps/**`、`docs/**`、`contracts/**` 与该 commit 逐字节一致）；实测运行时 HEAD `efef52ddc`（仅 `tests/browser-admin-390x844.mjs` 的 badges profile 差异）；未提交态只有 `knowledge/**` 与 `extensions/fixtures/**`。
A-05（PolicyId）与 F-02（空 dedupeKey 双插入）已转 ok；**14 条变异全部「变红 → 恢复 → `cmp` 字节一致」**。
本轮补的「变异覆盖」自检是同一类教训的产物：**少跑一条变异曾能静默 exit 0**（backfill 的 stdin 抽干，见 `reports/2026-11-12-badge-backfill-progress-acceptance.md` §8.3）——本脚本与另两份一并加了 `变异覆盖：N/N` 断言，条数不符即 FAIL。

规范：`knowledge/reports/2026-11-12-badge-conditions-manifest-spec.md` §9 Errata 3 / 计划 §8.2
载体：`scripts/verify-badge-fact-record.sh` + `scripts/verify-badge-fact-record-mutations.tsv`（不接门禁）

---

## 0. 判定规则与环境

| 结果 | 含义 |
| --- | --- |
| ok | 按判据实际跑过并满足 |
| FAIL | 代码已落地、行为不符判据 |
| pend | 前置不满足（缺用例 / 变异未跑），不算通过 |

```bash
export TMPDIR=/home/inkedus/.cache/dsh-tmp
./scripts/verify-badge-fact-record.sh            # 全量（契约探针 + 隔离真库 + 403 映射）
./scripts/verify-badge-fact-record.sh --no-tests # 只跑静态 + 契约探针（约 10s）
./scripts/verify-badge-fact-record.sh --mutate   # 11 条变异
```

做法：四要素 / 输入解码 / 六 reason / 审计分层 / 归属 / 能力用**临时测试文件**注入 `HostAPI` 包
（`package hostapi`，复用既有 fake；跑完删除）。理由：这些构造器是 unexported，外部 `go run` 探针够不到；
而「读注释判断」在本刀恰好最危险（reason 与 four-elements 都是字面量契约）。真库语义跑实现方隔离用例。

---

## 1. 断言与终态

### A. 四要素契约

| ID | 断言 | 判据（失败类） | 结果 |
| --- | --- | --- | --- |
| A-01 | `ID == "badges.fact.record"`（**不带 `@N`**） | 值级相等；改成 `badges.fact.record@1` 即红（M1） | ok |
| A-02 | `Version == "1"` | 值级；改 `2` 即红（M2） | ok |
| A-03 | input ref == `sforum.badges.fact.record.input@1`（id 与 `@1` 分开存，compose 后相等） | 版本改 2 即红（M3） | ok |
| A-04 | result ref == `sforum.badges.fact.record.result@1` | 同上 | ok |
| A-05 | `Prepare` 的 Policy 决策携带 `PolicyId == "sforum.badges.fact.record@1"`、`Allowed`、`ResourceId == factKey`、`Reason` 非空 | 四值逐字：M16（PolicyId 去 `@1`）与 M17（ResourceId 换成命令名）各自能单独变红；命令 ID 仍无 `@N`（A-01/A-06 同时成立） | ok（补口后） |
| A-06 | 代码里没有把 `badges.fact.record@1` 当命令 ID 的字面量 | 出现即红（防「ID 带 @」回潮） | ok |
| A-07 | docs 声明 Policy ID = `sforum.badges.fact.record@1` | 文档 tripwire（与 A-05 成对：文档说 A、实现做 B 时必须有一条红） | ok |

### B. 输入字段与真实解码路径

| ID | 断言 | 判据 | 结果 |
| --- | --- | --- | --- |
| B-01 | `userId` 十进制字符串 → int64（`"42"` → 42） | 喂 structpb 经 `Prepare` 看写口入参（不是读注释） | ok |
| B-02 | `delta` 十进制字符串 → int64，**负数允许**（`"-5"` → -5） | 同上 | ok |
| B-03 | `factKey`/`variant`/`dedupeKey` trim 后透传 | `"  acme.wiki.pages  "` → `acme.wiki.pages` | ok |
| B-04 | `occurredAt` RFC3339 → time 且等于原时刻 | 非法时间走 C-03 | ok |
| B-05 | `sourceExtensionID` 只取运行时身份（请求体不可伪造） | 写口入参 == context 的 extensionId | ok |

### C. 六个 reason 与审计分层

| ID | 断言 | 判据 | 结果 |
| --- | --- | --- | --- |
| C-01 | `invalid_user_id`（`"0"`） | reason 逐字 + 一条审计 + 未触达写口 | ok |
| C-02 | `invalid_delta`（`"0"`） | 同上 | ok |
| C-03 | `invalid_occurred_at`（非 RFC3339） | 同上 | ok |
| C-04 | `fact_key_not_declared`（未声明 key） | 同上 | ok |
| C-05 | `fact_key_not_declared`（空 key） | 同上 | ok |
| C-06 | `fact_write_failed`（写口报错） | reason 逐字 + 一条审计（M11 把 reason 换成参数错即红） | ok |
| C-07 | **反向**：域层审计不出现 `capability_denied` | 三条拒绝路径全扫，出现即红 | ok |
| C-08 | 审计写失败 **不改**返回码、不放行 | 注入 auditor error：reason 仍为 `fact_key_not_declared`（M7 把审计失败升级成 panic 即红） | ok |
| C-09 | 被拒请求不触达写口 | 见 C-01..C-05 的「recorder.inputs 为空」断言 | ok |

### D. 真库审计行（值级）

| ID | 断言 | 判据 | 结果 |
| --- | --- | --- | --- |
| D-01 | `audit_events` 一行：`action == extension.badge_fact_record.denied`，`metadata.reason/factKey/extensionId/userId` 值级相等，`actor_user_id IS NULL` | `TestBadgeFactDeniedAuditLandsInAuditEvents` exit 0；M13 删 metadata.extensionId → 红 | ok |

### E. factKey 归属与 fail-closed

| ID | 断言 | 判据 | 结果 |
| --- | --- | --- | --- |
| E-01 | 调用方**自己声明**的 key 通过 | 写口被调用 | ok |
| E-02 | 未声明的 key 拒绝 | `fact_key_not_declared` | ok |
| E-03 | **别的扩展声明了同一个 key**，调用方冒用仍拒绝 | 夹具：`other.plugin` 拥有 `other.plugin.pages`，`acme.wiki` 请求它 → 仍 `fact_key_not_declared`。只验「key 不存在」等于没验归属 | ok |
| E-04 | 声明读口 nil → fail-closed | `fact_key_not_declared`（不是放行）（M12 即红） | ok |
| E-05 | 声明读口报错 → `fact_write_failed`（不是「未声明」） | 二者修法不同：一个是数据错、一个是环境错 | ok |
| E-06 | 命令目录 **17** 条且含本命令 | `TestPostgresProtocolV2CommandRuntimePublishesDomainCommands` exit 0 | ok |

### F. 幂等与 delta/total（**并排**）

| ID | 断言 | 判据 | 结果 |
| --- | --- | --- | --- |
| F-01 | 同 `dedupeKey` 重复 → `inserted:false` 且 total 不变；换 key 才累加 | 真库 store 用例 exit 0 | ok |
| F-02 | **无 `dedupeKey` 的两次必须都插入**（空串不是幂等键） | `TestRecordFactEmptyDedupeKeyAlwaysInserts`：两次 `inserted=true`、total 1→2、`badge_facts` 行数 2（双验）；**M18**（把空串落成固定键 `__dedupe__`）→ 该用例 + ClampsTotalAtZero 红 | ok（补口后） |
| F-03 | **并排**：累计为负时**回执** `total=0` **且存储层** `user_fact_counters.total` 保留真实负值（-3） | 两处分别断言：store 用例 `Total == -3`，命令用例 `TestBadgeFactRecordClampsNegativeTotal` 回执 `"0"`。M9（回执不 clamp）与 M14（存储层也 clamp）各自能单独变红——**只验一面会把另一面写坏当正确** | ok |
| F-04 | int64 边界（MaxInt64）逐字回显不丢精度 | store 与命令两侧各一条 | ok |

### G. 能力与顶层码

| ID | 断言 | 判据 | 结果 |
| --- | --- | --- | --- |
| G-01 | `RequiredCapability == capabilities.BadgesFactWrite` | 值级 | ok |
| G-02 | 能力字面量 == `badges.fact.write` | 值级（目录/文档同源） | ok |
| G-03 | 顶层码 `forum.badge_fact_write_denied` → **HTTP 403**（不是 500） | `fact_denial_test.go` 断言 `fiber.StatusForbidden` + 码字面量；M15 改 500 即红 | ok |

### H. 拒绝链两端（真实生产者，Lead 特别要求）

| ID | 断言 | 判据 | 结果 |
| --- | --- | --- | --- |
| H-01 | **栈顶**：`models` 侧 `FactWriteDenial{Reason}` 的 `Unwrap` 指向 `ErrBadgeFactWriteDenied`，`ErrorCode` 返回 `forum.badge_fact_write_denied` | 契约探针断言：拒绝错误链可 `errors.Is(ErrBadgeFactWriteDenied)`、`BadgeFactWriteDenialReason` 取回逐字 reason；`ErrorCode` 值级 | ok |
| H-02 | **拒绝时挂的 reason**：命令层每条拒绝都由 `NewBadgeFactWriteDenied(reason)` 构造，协议 reason 从错误链投影（不是第二份字面量） | C-01..C-06 的 reason 断言 + M11（改构造点 reason 即红） | ok |

---

## 2. 测试缝专项：「loadActor 不留后门」

Lead 指定的安全面核查，结论 **通过**，证据如下：

1. **字段不可导出**：`Controller.loadActor` 是 struct 私有字段（`controller.go:58`），外部包不能赋值；只能经 `WithActorLoader`（导出方法，`controller.go:67`）。
2. **无生产装配调用点**：`grep -rn 'WithActorLoader' apps/api | grep -v _test` → 只有定义本身；`bootstrap/**`、`routes/**`、任何 DI 容器都没有调用。生产 `NewController(...)` 不设置它 → 恒 nil。
3. **HTTP 不可达**：没有任何路由/handler 把请求字段写到 `Controller` 上；插件是独立进程，Host 命令面也不暴露 Controller 句柄；无反射/容器注入路径。
4. **nil 时行为与改动前逐字一致**：`actor()` 的 nil 分支（`controller.go:157-164`）与 HEAD 版本（`git show HEAD:...controller.go:135-144`）逐行相同——`apphttp.OptionalActor` → `actor.ID <= 0` → `401 auth.required`。缝只在 `h.loadActor != nil` 时生效，且它**只替换主体解析**：权限仍走域层 `requireBadgePermission` 与路由守卫，`requireFeature` 不受影响。
5. **用途边界**：`viewer()`（公开读的 earned 判定）与后台 `actor()` 共用该缝；两条路径都保留「ID<=0 → 未登录/匿名」的收口，不存在「注入即放行」。

> 判定：这是**生产代码为可测性开的窄缝**，不是后门；风险点是「未来有人在 bootstrap 里顺手 Bind 它」——建议在 `WithActorLoader` 注释里补一句「禁止在非测试装配调用」（当前注释已写「测试用」，等级足够，不加也不阻塞）。

### H-03 Policy.ResourceId 语义核查（Lead 指定，不预设答案）

对照既有命令的 `PolicyDecision.ResourceId` 取值：

| 命令 | ResourceId | 语义 |
| --- | --- | --- |
| `notifications.emit` | `typeID` | 本次**被写的对象**（通知类型） |
| `sforum.events.emit` | `input.Name` | 被写对象（事件名） |
| `attachments.extension.claim` | `input.AttachmentPublicID` | 被写对象（附件） |
| `attachments.extension.list` | `"extension"` | 作用域（列表查询无单一对象） |
| `ai.complete` | `resourceID` | 被写/被消费对象 |
| **`badges.fact.record`（现状）** | `prepared.input.FactKey` | 本次被写的事实 key（该决策约束的正是「这个 key 是否属于调用方」） |

**判定：符合既有语义**——`ResourceId` 是「该策略决策所针对的资源」，不是命令名。`factKey` 正是这条决策要授权的对象
（归属校验就是围绕它）；若放命令 ID，策略审计会退化成「所有写入同名」，反而丢掉唯一有用的定位信息。
**建议保持现状**，无需老后改。

---

## 3. 变异矩阵（14 条，全部变红并恢复）

| MUT | 改哪行 | 变异 | 必红断言 / 实测红名 |
| --- | --- | --- | --- |
| M1 | `v2_command_badges.go` ID | `badges.fact.record` → `…@1` | A-01/A-06：`TestZZM32ContractElements` |
| M2 | Version | `"1"` → `"2"` | A-02 |
| M3 | SchemaVersion | `"1"` → `"2"` | A-03/A-04 |
| M5 | 归属校验 | 拒绝条件加 `&& false`（保留变量使用，避免 build failed 混淆红因） | E-02/E-03：`TestZZM32Reasons`/`…Ownership` |
| M7 | 审计调用 | `_ =` → 失败 panic | C-08：`TestZZM32AuditLayering` |
| M9 | 回执 clamp | `if total < 0` → `if false` | F-03：`TestBadgeFactRecordClampsNegativeTotal` |
| M11 | 写失败 reason | `fact_write_failed` → `invalid_delta` | C-06/E-05 |
| M12 | 声明读口 | 删 fail-closed 条件 | E-04 |
| M13 | 审计 metadata | 删 `extensionId`（用 `audit` 命令跑真库用例——首版用 `hostapi` 只跑探针，抓不到 D-01，已修正） | D-01：`TestBadgeFactDeniedAuditLandsInAuditEvents` |
| M16 | Policy 常量 | `sforum.badges.fact.record@1` → 去掉 `@1` | A-05：`TestZZM32PolicyID` |
| M17 | Policy.ResourceId | `prepared.input.FactKey` → `CommandBadgesFactRecordID` | A-05：`TestZZM32PolicyID` |
| M18 | 空 dedupeKey 写入 | 落成固定键 `'__dedupe__'`（「顺手去重」形态） | F-02：`TestRecordFactEmptyDedupeKeyAlwaysInserts`（行数与累计同时错） |
| M14 | 存储层 clamp | counter UPDATE 加 `GREATEST(...,0)` | F-03：`TestRecordFactClampsTotalAtZero` |
| M15 | HTTP 映射 | 403 → 500 | G-03：`TestMapBadgeErrorProjectsFactDenial` |

恢复：备份 → sed → 跑命令要求非 0 → `cp` 恢复 → `cmp` 字节一致；锚点不匹配记 FAIL，不静默跳过。
**M5 首版曾因删判断导致 `declared` 未使用而 build failed**——那种红不算证据，已改成保留使用的变异并复跑。

---

## 4. 执行记录与 exit code

| 命令 | exit | 输出要点 |
| --- | --- | --- |
| `./scripts/verify-badge-fact-record.sh`（补口后） | **0** | `ok 19 / FAIL 0 / pend 1`（pend=变异未跑） |
| `… --mutate`（全量，首轮） | **0** | `ok 33 / FAIL 0 / pend 0`（14/14 变异变红并恢复，无覆盖自检） |
| `… --mutate`（**最终口径，含覆盖自检**） | **0** | `ok 34 / FAIL 0 / pend 0`；`ok 变异覆盖：14/14 条全部执行`（`mutations/fact-record-full-mutate-after-coverage-fix.log`） |
| `-run 'TestBadgeFactDeniedAuditLandsInAuditEvents'` | **0** | 真库审计行 + metadata 值级 |
| `-run 'TestRecordFact(DedupeIsIdempotent|ClampsTotalAtZero|KeepsInt64Precision)'` | **0** | RUN=3（幂等 / 存储负值 / 精度） |
| `-run 'TestBadgeFactRecordClampsNegativeTotal'` | **0** | 回执 clamp=0（与存储 -3 并排） |
| `-run 'TestMapBadgeErrorProjectsFactDenial'` | **0** | 403 + 顶层码字面量 |
| `-run 'TestBackfillStatusRequiresBadgeView'`（第一刀 GAP-1 域层路径） | **0** | 域层 `identity.ErrPermissionDenied` 且 store 零调用；HTTP 403 那条由 `backfill_status_test.go` 覆盖 → **两条路径都验到了** |
| `-run 'TestRecordFactEmptyDedupeKeyAlwaysInserts'`（F-02） | **0** | 空 dedupeKey 双插入（行数+累计双验） |
| `-run 'TestBadgeFactRecordPolicyIDVersusCommandID'`（实现方 A-05 用例） | **0** | 请求侧无 `@`、策略侧带 `@1`、ResourceId=factKey |

---

## 5. 缺陷与缺口台账

| ID | 现象 | 归属 | 判定 | 建议 |
| --- | --- | --- | --- | --- |
| GAP-M32-1 | **A-05：Policy 决策缺失**（docs/计划声明 `Policy ID = sforum.badges.fact.record@1`，实现 0 条 policy） | HostAPI（task-17） | **已关闭**：新增 `BadgeFactRecordPolicyID = "sforum.badges.fact.record@1"`，preparation 返回 `Policy[{PolicyId, ResourceId: factKey, Allowed: true, Reason}]`；命令 ID 仍无 `@N`。探针四值断言 + M16/M17 双变异 | closed |
| GAP-M32-2 | F-02：无 `dedupeKey` 的两次都插入没有显式用例 | 后端（task-17） | **已关闭**：`TestRecordFactEmptyDedupeKeyAlwaysInserts`（inserted 双 true + total 累加 + 行数 2），脚本按 PASS 行断言 | closed |
| GAP-M32-3 | `unit` 未在 DTO？不适用（M3.1 已验）。本刀无其它缺失 | — | — | — |

---

## 6. 复跑溯源

- 基线：**`4e1876331`**（`apps/**`、`docs/**`、`contracts/**` 与该 commit 逐字节一致；M3.2 实现随后由 `4457106e2`/`082f98624`/`4e1876331` 提交）；**实测运行时 HEAD `efef52ddc`**（只差 `tests/browser-admin-390x844.mjs` 的 badges profile）；未提交态只有 `knowledge/**` 与 `extensions/fixtures/**`。
- 时间：全量 `2026-11-12T21:09+0800`（含变异整轮，14 条）；`sdk/**` 未做任何变异（Lead 明示老后正在写 `sdk/plugin/v2/badges.go`）。
- 环境：`TMPDIR=/home/inkedus/.cache/dsh-tmp`；DB 只走 `./scripts/test-api-isolated.sh`；契约探针是临时文件、跑完删除（`trap` 清理）。
- 失效条件：改动 `v2_command_badges.go`、`fact_denial.go`、`facts_store.go`、`controller.go` 的 fact 映射后需重跑 `--mutate`。
