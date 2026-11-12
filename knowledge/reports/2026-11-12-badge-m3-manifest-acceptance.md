# 徽章 M3.1 独立验收矩阵（manifest `badgeConditions[]` + 能力 + 事实候选目录 + `factCandidates`）

Status: **accepted（逐条表全部 ok / FAIL 0 / pend 0；脚本最终口径见下）** —— 基线复跑于 **2026-11-12T20:43:15+0800**，
代码基线 **`4e1876331`**（`apps/**`、`docs/**`、`contracts/**` 与该 commit 逐字节一致）；实测运行时 HEAD `efef52ddc`（仅 `tests/browser-admin-390x844.mjs` 的 badges profile 差异）；未提交态只有 `knowledge/**` 与 `extensions/fixtures/**`。

**脚本口径按条件并列（不同条件不可相加、不可互换）**：
- `①` 基线（无会话 / 无 `--mutate`）= `ok 34 / FAIL 0 / pend 2`（pend = `F-01` + `MUT-*`）；
- `②` 无会话 + `--mutate` = `ok 47 / FAIL 0 / pend 1`（`mutations/m3-full-mutate-after-coverage-fix.log`，pend 仅 `F-01`）；
- `③` **带超管会话 + `--mutate`（最终口径）= `ok 48 / FAIL 0 / pend 0`**（`factcandidates/m3-mutate-final.log`；`F-01` 行原文 `ok F-01 factCandidates 值级（items=13 fullKeys=7 unitless=6 facts=2 clean）`；`ok 变异覆盖：12/12 条全部执行`）。

12 条变异全部「变红 → 恢复 → `cmp` 字节一致」；`F-01` 的 `pend` 已转 `ok`（依据见 §2 F 表与 §4.1）。

规范：`knowledge/reports/2026-11-12-badge-conditions-manifest-spec.md`（含 Errata 2/3）
落点核实：`knowledge/reports/2026-11-12-badge-m3-impl-facts.md`
执行载体：`scripts/verify-badge-m3-manifest.sh` + `scripts/verify-badge-m3-manifest-mutations.tsv`（都不接门禁）

---

## 0. 判定规则、环境与载体

### 0.1 三种结果（与第一刀同口径）

| 结果 | 含义 |
| --- | --- |
| ok | 按判据实际跑过并满足 |
| FAIL | 代码已落地、行为不符判据 |
| pend | 前置不满足（无会话 / 缺夹具），**不算通过** |

**不得以「读代码看起来对」记为 ok**；每条 ok 都对应 §6 的可复跑命令。

### 0.2 环境

```bash
export TMPDIR=/home/inkedus/.cache/dsh-tmp
./scripts/verify-badge-m3-manifest.sh            # 全量（含隔离真库 + 门禁）
./scripts/verify-badge-m3-manifest.sh --no-tests # 只跑静态 + 独立探针（最快，改一行就能跑）
./scripts/verify-badge-m3-manifest.sh --mutate   # 变异自检（12 条，约 4 分钟）
```

DB 断言只走 `./scripts/test-api-isolated.sh`（一次性集群）；本脚本**不**读 `DATABASE_URL`。

### 0.3 断言前缀

| 前缀 | 责任面 |
| --- | --- |
| `A-*` | **schema 原文**（逐字符；读文件，不读 Go 内嵌副本） |
| `B-*` | 双侧校验：`Validate`（Go）与 `ValidateV3JSONSchema` 各自的覆盖范围 |
| `C-*` | 候选可见性 / 冲突 / variant 判定 / 能力目录 |
| `D-*` | 隔离真库：卸载后 unknown 成对、variant 三态、单测包 |
| `E-*` | 生成文档与门禁 |
| `F-*` | HTTP 级 `factCandidates` 值级（需会话） |
| `M3M-*` | 变异自检 |

---

## 1. 冻结契约（验收对象）

1. `badgeConditions[]`：`key`（pattern `^[a-z0-9][a-z0-9._-]{1,120}$`）、`contractVersion`（**恒等于 `key@N`**）、`label{zh-CN,en-US}`、可选 `unit`、`compare`（⊆ `gte/gt/lte/lt/eq`，非空）、可选 `variants`（≤16，`value` 非空唯一）。**不设 `valueKind`**。
2. 能力 `badges.fact.write` 进能力目录、启用弹窗渲染、`capabilities.md` 重生成。
3. 候选目录只来自**已启用 plugin** 的声明；冲突三类（撞 Core / 越界 / 重复）拒绝且不静默覆盖。
4. `GET /admin/badges/conditions` 顶层 additive `factCandidates`；`items` 形状与 M2 逐键一致；空候选为 `[]`。
5. 卸载后求值：`Matched=false, Incomplete=true` + `Errors` 含 `resolve_failed`；声明接上后 `Incomplete=false`。
6. variant：声明值可存 / 未声明值 422 / 卸载后（无候选）仍可存。

---

## 2. 断言矩阵与终态结果

### A. schema 原文（逐字符）

| ID | 断言 | 判据（失败类） | 结果 |
| --- | --- | --- | --- |
| A-01 | `key.pattern` **逐字符** == `^[a-z0-9][a-z0-9._-]{1,120}$` | 读 `schemas/manifest-v3.schema.json` 原文比较；放宽/收紧任一字符即红（M3M-01） | ok |
| A-02 | `$defs.badgeConditions` **无 `valueKind`** | 出现即红（M3M-02） | ok |
| A-03 | 条目属性集合 **恰好** `{key,contractVersion,label,unit,compare,variants}` | 多/少一个属性即红（抓「只加 struct 不加 schema」的反向） | ok |
| A-04 | 根 `$ref` + `$defs` nullable array + 条目 `additionalProperties:false` | 三处任一不符即红 | ok |
| A-05 | 上限：`badgeConditions.maxItems=32`、`variants.maxItems=16` | 改任一数值即红 | ok |
| A-06 | `key` 长度 2–121 | 与 DB CHECK 同界；改界即红 | ok |
| A-07 | `compare`：`minItems 1` / `maxItems 5` / 枚举五值（**无 `ne`**） | 放回 `ne` 或去掉 minItems 即红 | ok |
| A-08 | `label`/`unit` 的 `required` 均含 `zh-CN`+`en-US` | 缺一语种即红 | ok |
| A-09 | `variants[].value.pattern` == `^[a-z0-9][a-z0-9._-]{0,63}$` | 与 Go 常量脱钩即红 | ok |

### B. 双侧校验（Go vs schema，逐类分开判）

探针独立构造清单（`go run` 临时程序，只调用导出 API，不 import 实现方测试函数），
基线夹具 = `extensions/fixtures/plugins/sforum-query-surface-reference/sforum.extension.json`。

| ID | 断言 | Go 判据 | schema 判据 | 结果 |
| --- | --- | --- | --- | --- |
| B-valid | 全量合法（variants + unit + `compare:["gte","lte"]`） | 过 | 过 | ok |
| B-no-variants | 省略 `variants` | 过 | 过 | ok |
| B-no-unit | 省略 `unit` | 过 | 过 | ok |
| B-no-backend-empty-entry | backend 存在但 entry 空 | 拒（**必须带 `rule #2` 文案**） | 放行（schema 无 minLength） | ok |
| B-no-backend-key | 整个 `backend` 键缺失 | 拒（`rule #2`） | **放行**（边界见 §5 GAP-B） | ok |
| B-non-plugin | `type:theme` | 拒（拒因来自**通用主题校验**，见 §5 GAP-A） | 放行 | ok |
| B-no-prefix | key 无 `<id>.` 前缀 | 拒 | 放行 | ok |
| B-drift | `contractVersion` ≠ `key@N` | 拒（`contractVersion drift`） | 放行 | ok |
| B-empty-compare | `compare: []` | 拒（`rule #4`） | 拒（minItems 1） | ok |
| B-seventeen-variants | 17 个 variants | 拒（`rule #5`） | 拒（maxItems 16） | ok |
| B-label-missing-en | label 只有 zh | 拒（`rule #6`） | 拒（required en-US） | ok |

> 判据刻意**绑定 rule 编号/错误语义**：只断言「拒了」会把「被别的规则拒掉」当成这条规则的功劳——本轮的 M3M-03 就是这么暴露出来的（见 §4）。

### C. 候选可见性 / 冲突 / variant / 能力

| ID | 断言 | 判据（失败类） | 结果 |
| --- | --- | --- | --- |
| C-01 | 候选只影响 `fact.count`，其余内置条件恒在 | `PublishableDefinitions(nil)` 与 `(+1 candidate)` 的键集合差 **恰好** `[fact.count]`；差为空或多出别的键即红（M3M-10） | ok |
| C-02 | 内置目录非空且加候选后 +1 | `12 → 13`；任一为空即红 | ok |
| C-03 | 三类冲突只留 1 条合法候选 | 冲突后候选数 ≠ 1 即红 | ok |
| C-04 | 冲突分类：撞 Core / 越界 / 重复 各 1 条，**不静默覆盖** | 任一 reason 缺失即红；撞 Core 被静默接受即红（M3M-08） | ok |
| C-05 | variant 判定：声明值放行 / 未声明值拒绝 / **无候选放行** / 空 variant 放行 / 无 variants 放行 | 五条逐一比对**实际布尔值**；「无候选放行」被改成拒绝即红（M3M-09，防过度修正） | ok |
| C-06 | 能力 `badges.fact.write` 在目录（risk=medium） | 找不到即红 | ok |
| C-07 | 能力文案自解释（人工判定） | zh=`写入插件事实计数`、en=`Write plugin fact counters`；**判定通过**：中文把「fact」译成「事实计数」而非裸术语，英文用领域词 fact counters 且 Description 写清「只经 Host 命令、宿主取整/边界/审计」，运营读得到风险；未出现「能力」这种自指词 | ok（人工） |

### D. 隔离真库与单测

| ID | 断言 | 判据（失败类） | 结果 |
| --- | --- | --- | --- |
| D-01 | 卸载后 unknown **成对**：未声明 → `Matched=false, Incomplete=true` + `resolve_failed`；声明接上 → `Incomplete=false` 且成立 | 只用 `-run 'UndeclaredFactUnknown|DeclaredFactReadsCounters'` 跑**两条**并计数 `RUN>=2`；只跑一条即红；把 resolver 写死返 error 会让第二条红（M3M-12） | ok（RUN=2） |
| D-02 | variant 三态用例齐全：可存 / 未声明拒 / **卸载后仍可存** | `RUN>=3`；缺「卸载后仍可存」即红（防把合法规则判非法） | ok（RUN=3） |
| D-03 | manifest/候选/DTO 单测包 exit 0 | 任一包红即红 | ok（见 §8 并发事故说明） |

### E. 生成文档与门禁

| ID | 断言 | 判据 | 结果 |
| --- | --- | --- | --- |
| E-01 | `docs/extensions/catalogs/manifest-v3.md` 含 `badgeConditions` 且**无** `valueKind` | 未重生成或残留 valueKind 即红 | ok |
| E-02 | `docs/extensions/catalogs/capabilities.md` 含 `badges.fact.write` | 未重生成即红 | ok |
| E-03 | `node tests/validate-docs.mjs`、`node tests/validate-v3-p0-catalogs.mjs` | exit 0 | ok（两条 exit 0） |

### F. HTTP 级 `factCandidates` 值级

| ID | 断言 | 判据 | 结果 |
| --- | --- | --- | --- |
| F-01 | `GET /admin/badges/conditions` 的 `factCandidates[]` 值级：`key` 非空、`label.zh/en` 长度 > 0、`variants[].value` 非空且 label 双语非空；`items` 键集合与 M2 逐键一致；空候选为 `[]` | 键在值空即红；`null` 即红 | **ok**（判据未改）。三条真实条件各跑一次（详述见表格后的 ①②③）：
① **带候选**（一次性超管 uid=502 + 安装并启用 `sforum.query-surface-reference`，等过 5s 快照 TTL）→ `f01-run.log` `RESULT ok=24 fail=0`：两条候选 `onboarding.tasks`(zh=7/en=26, variants=tutorial/profile) 与 `verified_reports`(zh=9/en=26, 无 variants)；
② **空候选**（墓碑态 + 等 7s）→ `empty-candidates.log` `RESULT fail=0`：`factCandidates == []`（不是 `null`）、`items` 12 条且无 `fact.count`、键集合只有「9 键」与「9 键 + unit」两种；
③ **脚本内**（最终口径）→ `ok F-01 factCandidates 值级（items=13 fullKeys=7 unitless=6 facts=2 clean）`。
首次带会话真跑时红的是**检查器自己**（没解信封 + 静默接受空集合），修法与本轮桩验证见 §4.1；桩四发：信封形态 → 绿、无信封空集合 → 红、无信封键漂移 → 红、无信封 flat → 绿（不误红）。 |

---

## 3. 变异矩阵（12 条，全部变红并恢复）

命令：`./scripts/verify-badge-m3-manifest.sh --mutate`（读 `scripts/verify-badge-m3-manifest-mutations.tsv`）。

| MUT | 改哪行 | 变异 | 必红断言 / 实测红名 |
| --- | --- | --- | --- |
| M3M-01 | `schemas/manifest-v3.schema.json` key pattern | `{1,120}` → `{1,121}` | A-01（`实际：^[…]{1,121}$`） |
| M3M-02 | 同上 | 插入 `"valueKind":{"type":"string"}` | A-02 + A-03（属性集合多一个） |
| M3M-03 | `v3_badge_conditions.go` rule #2 | 删「无 backend」判断 | B-no-backend-empty-entry（go=pass） |
| M3M-04 | 同上 contractVersion 等式 | 等式判断 → `if false` | B-drift（go=pass） |
| M3M-05 | 同上 rule #4 | `len(compare)==0` → `if false` | B-empty-compare（go=pass，schema 仍拒） |
| M3M-06 | 同上 variants 上限 | `16` → `17` | B-seventeen-variants（go=pass） |
| M3M-07 | 同上 rule #6 | 双语 label 判断 → `if false` | B-label-missing-en（go=pass） |
| M3M-08 | `conditions/facts.go` 撞 Core key | 判断 → `if false` | C-04（冲突分类缺 `core_condition_key`） |
| M3M-09 | 同上「无候选放行」 | `if !ok \|\| len(variants)==0` → `if !ok` | C-05（no-variants=false） |
| M3M-10 | `conditions/registry.go` 隐藏 fact.count | `if !hasCandidates` → `if false` | C-01（diff=[]）+ C-02（13→13） |
| M3M-11 | `Controllers/Badges/dto.go` | label 置空结构体 | `TestFactCandidateWireShape`（值级断言红，键集合断言不会红） |
| M3M-12 | `Support/RuleEngine/eval.go` | 删 `resolve_failed` 记录 | `TestEvaluateRuleMarksUndeclaredFactUnknown` |

恢复方式统一：备份 → `sed` → 跑命令要求非 0 → `cp` 恢复 → `cmp` 字节一致；锚点不匹配记 FAIL，不静默跳过。

**覆盖自检（2026-11-12 收口新增）**：脚本每轮末尾打印 `ok 变异覆盖：12/12 条全部执行`，少于预期条数即 `FAIL 变异覆盖`。起因是 backfill 脚本的变异循环被 `docker run -i` 抽干 stdin、后 5 条静默跳过却仍 exit 0（`reports/2026-11-12-badge-backfill-progress-acceptance.md` §8.3）——**「跑过了」不等于「该跑的跑了」**，所以三份脚本统一把「跑了几条」也变成断言。

**最终口径原文**：`qa-2026-11-12-badge-pend-conversion/factcandidates/m3-mutate-final.log`（带超管会话 + `--mutate`，`ok 48 / FAIL 0 / pend 0`）。

---

## 4. 本轮自查抓到的两个「我自己的假绿」

1. **注解式断言**：C-05 首版写成 `[[ "$(pv variant.$v)" == *'(want=true)'* ]]`——只比对了
   **期望注解**，实际值是 `false` 也能过。M3M-09「未变红」把它顶出来，已改成逐值全串比较
   （`true(want=true)` / `false(want=false)`）。
2. **拒绝原因未绑定**：B-non-plugin 首版只断言 `go=fail`，而实际上 Go 的拒绝来自
   **通用主题校验**（`theme+backend` 不被支持），`badgeConditions rule #1` 根本没执行。
   现在探针输出 `goerr=`，拒绝类断言绑定 `rule #N` / `contractVersion drift` 语义；这也直接
   产出了 GAP-A。

> 这两条正是模块笔记「假绿清单」里的「验证对象为空」「断言集覆盖不到横切面」的变体：
> **通过的断言不等于跑过断言**。

### 4.1 F-01 检查器自己的两个缺陷（第一次带会话真跑才暴露）

F-01 挂了两轮 pend，理由是「无管理员会话」。**第一次真拿到会话去跑，红的是检查器自己**，不是产品：

| # | 缺陷 | 后果 | 形态归类 |
| --- | --- | --- | --- |
| 1 | `payload.factCandidates` **没解信封**——真实响应是 `{code,message,data:{…}}`，候选在 `data.factCandidates` | 带会话后取值恒 `undefined` → 断言恒走空分支，**永远不可能绿，也永远不可能红**（等于没断言） | 假绿清单「**验证对象为空**」 |
| 2 | **静默接受空集合**：`facts=[]` 时值级 `for` 循环 0 次 → 输出 `clean` | 「没有任何候选」被当成「所有候选都合格」，候选目录被清空反而更绿 | 假绿清单「**断言集覆盖不到横切面**」 |

**修法（已在 `scripts/verify-badge-m3-manifest.sh` 里，可复核）**：
- 解包：`const root = payload && typeof payload === 'object' && payload.data ? payload.data : payload`（无信封形态仍兼容直出）；
- 空集合判红：`if (facts.length === 0) bad.push('factCandidates 为空：F-01 判据要求至少 1 条候选…')`——把「0 条候选」从「干净」改成「红」。

**收口时的桩验证（原文已归档，逐发可核）**：`qa-2026-11-12-badge-pend-conversion/mutations/f01-stub-checks.log`，桩 `f01-stub.mjs` 在 `127.0.0.1:18099` 回放固定 JSON，让脚本真实走 `curl → node` 链路，四发：

| 桩载荷 | 期望 | 实测（日志行原文） |
| --- | --- | --- |
| 无信封 + `factCandidates: []` | FAIL | `FAIL F-01 factCandidates 值级（bad=factCandidates 为空：F-01 判据要求至少 1 条候选…）` |
| **真实信封** `{"code":200,"message":"OK","data":{…}}` | PASS | `ok F-01 factCandidates 值级（items=2 fullKeys=1 unitless=1 facts=1 clean）` |
| 无信封 + 键漂移 item（`a,b,key`） | FAIL | `FAIL F-01 factCandidates 值级（bad=items 键漂移: a,b,key）` |
| 无信封 flat（兼容形态） | PASS（不许误红） | `ok F-01 factCandidates 值级（items=2 fullKeys=1 unitless=1 facts=1 clean）` |

第 2 发正是原缺陷的复现点：**修之前**这一发只能看到桩自己捏的无信封形态，所以「信封没解」永远看不见；第 3 发证明键集合改成逐项（`fullKeys` / `unitless`）后，单条漂移也会红，不再只盯 `items[0]`。

另有一条同源现象留在证据里：`qa-2026-11-12-badge-pend-conversion/factcandidates/README.md` §3 记录首轮断言脚本「`items 不是数组`」也是同一个漏解包，修正后 24 条全绿——**同一形态在两个检查器上各犯一次**，说明「带会话跑一次」这个动作本身就是最有效的检查。

---

## 5. 缺陷与缺口台账

| ID | 现象 | 归属 | 判定 | 建议 |
| --- | --- | --- | --- | --- |
| GAP-A | `badgeConditions` **rule #1（非插件）不可达**：`type:theme` + backend 会先被通用主题校验拒（`extensions: invalid manifest`，无 rule 文案） | 后端（manifest 校验） | 非缺陷，但属**防防御性代码 + 断言无法绑定 rule** | 若要让 rule #1 可观测：把 badgeConditions 的类型检查前移到主题支持校验之前并保留文案；否则在规范里标注「rule #1 为纵深防御、行为不可区分」。本轮断言只写「Go 拒绝」 |
| GAP-B | **schema 单独不要求 `backend`**：根 `required` 不含它，`allOf` 条件只对含 `content`/`seo`/`identity` 的清单生效；只带 `badgeConditions` 的清单能过 schema | 后端（schema） | 真实安装安全：`load.go` 在 schema 后**必跑** `Validate`（rule #2）| 建议 schema 补一条 `allOf: if required badgeConditions then required backend`，把「两条路径都拒」变成事实而非依赖调用顺序 |
| GAP-C | HTTP 级 `factCandidates` 值级未实测（无管理员会话） | 验收环境 | **closed（2026-11-12）**：拿到会话后实跑转 ok，见 §2 F-01 与 §4.1 | 已关闭；判据未改 |
| GAP-D | 端到端夹具 `extensions/fixtures/plugins/sforum-badge-facts/` 不存在，`sforum-query-surface-reference` 也还没有 `badgeConditions[]` | 后端（M3.2 范围） | 不阻塞 M3.1（契约层已验） | M3.2 落地时补夹具 + `database.grants` 含 `host_commands` + `capabilities` 含 `badges.fact.write` |

另：`TestManifestV3SchemaDeclaresBadgeConditions`（实现方）已覆盖 pattern/maxItems/要求，
但它的「缺 backend → schema 拒」判据依赖 `completeV3Manifest()` 里的 `content`/`seo` 条件
（见 GAP-B），注释「schema 的 backend.entry 也有 minLength:1」在 `$defs.backend` 里**不存在**
（该字段只在 allOf 的 then 分支里）。本报告的 B-no-backend-* 两行已把真实边界写清。

---

## 6. 执行记录与原始 exit code

| 命令 | exit | 输出要点 |
| --- | --- | --- |
| `./scripts/verify-badge-m3-manifest.sh` | **0** | `ok 34 / FAIL 0 / pend 2`（2026-11-12T20:43:15+0800，基线口径） |
| `… --mutate`（无会话） | **0** | `ok 47 / FAIL 0 / pend 1`（`mutations/m3-full-mutate-after-coverage-fix.log`，pend 仅 `F-01`） |
| `… --mutate`（**带超管会话，最终口径**） | **0** | `ok 48 / FAIL 0 / pend 0`；`ok 变异覆盖：12/12 条全部执行`；`F-01` 行 `items=13 fullKeys=7 unitless=6 facts=2 clean`（`factcandidates/m3-mutate-final.log`） |
| `… --no-tests` | **0** | `ok 29 / FAIL 0 / pend 4`（纯静态 + 探针，基线口径） |
| 隔离真库 D-01（`-run Undeclared…|Declared…`） | **0** | `RUN=2`（成对） |
| 隔离真库 D-02（`-run ValidateBadgeRule(Accepts…|Rejects…|Allows…Gone)`） | **0** | `RUN=3`（三态） |
| D-03 单测三包（manifest / conditions / controllers） | **0** | 首次撞并发编辑 build failed，重跑 0（§8） |
| `node tests/validate-docs.mjs` / `validate-v3-p0-catalogs.mjs` | **0** / **0** | 文档与 catalog 生成一致 |
| `… --mutate`（首轮，历史） | 见 §3 | 12/12 各自变红并恢复；那一轮 exit 1 的唯一原因是 §8 的瞬时 build failed；此后加 `变异覆盖` 自检并补齐信封/空集合修法，最终口径见上两行 |

---

## 7. 变异样本原始输出（节选）

```
M3M-01  ok  … A-01 key pattern（实际：^[a-z0-9][a-z0-9._-]{1,121}$）
M3M-03  ok  … B-no-backend-empty-entry（期望 go=fail js=pass，实际 go=pass js=pass）
M3M-09  ok  … C-05 variant 判定（不符： no-variants=false(want=true)）
M3M-11  ok  … --- FAIL: TestFactCandidateWireShape (0.00s)
M3M-12  ok  … --- FAIL: TestEvaluateRuleMarksUndeclaredFactUnknown (0.19s)
```

---

## 8. 复跑溯源与并发事故

- 基线：**`4e1876331`**（`apps/**`、`docs/**`、`contracts/**` 与该 commit 逐字节一致；M3.1 实现随后由 `4457106e2`/`082f98624`/`4e1876331` 提交）；**实测运行时 HEAD `efef52ddc`**（只差 `tests/browser-admin-390x844.mjs` 的 badges profile）；未提交态只有 `knowledge/**` 与 `extensions/fixtures/**`。
- 时间：全量 `2026-11-12T20:43:15+0800`；变异整轮 `20:40–20:42`。
- **并发编辑事故（已处理，不影响结论）**：`--mutate` 整轮运行期间，后端（task-12）正在修改
  `apps/api/app/Jobs/Badges/**` 与 `app/Models/Badges/**`，导致脚本开头 D-03 的
  `Controllers/Badges` 包瞬时 `[build failed]`；随后单跑同命令 **exit 0**。
  未发生「恢复旧内容覆盖他人改动」：本轮的变异文件（schema / v3_badge_conditions.go /
  conditions/facts.go / conditions/registry.go / dto.go / RuleEngine/eval.go）与 task-12 的
  写面不重叠，且每次恢复都 `cmp` 验过字节。
- **纪律**：`--mutate` 会原地改生产文件，**不要**与正在改同一批文件的同事并行跑；
  开跑前建议确认 `git status` 里目标文件最近 2 分钟无变动（本次实测教训）。
- 失效条件：本报告证据有效期 = 上述时间之后 `badgeConditions` 校验器、schema、
  `conditions/facts.go`、`registry.go`、`factCandidateDTO`、`RuleEngine/eval.go` 未变；
  改动任一文件需重跑 `--mutate`。
