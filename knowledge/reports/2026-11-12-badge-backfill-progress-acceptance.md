# 回扫进度可见（第一刀）独立验收矩阵

Status: **accepted（逐条表 `40 ok / 0 FAIL / 0 pend` —— `A-01` / `B-04` / `G-05` 已由 pend 转 ok，每条的判据未改、依据见 §8.1）** —— **脚本口径按条件并列，禁止把不同条件的数字相加或互相替换**：
`①` 基线（无 DB / 无会话 / 无 `--mutate`）= `ok 25 / FAIL 0 / pend 3`（GAP 补口后，2026-11-12T20:5x+0800）；
`②` **带一次性集群 + 一次性超管会话、无 `--mutate`** = **`ok 29 / FAIL 0 / pend 2`**（`qa-2026-11-12-badge-pend-conversion/factcandidates/backfill-db-session.log` 原文；pend = `G-05` + `MUT-*`，DDL 块在查库可用时一次产 5 条 ok：`B-04` 用例 + 列集合 + FK CASCADE + owner 同源 + 两索引）；
`③` **带一次性集群 + 超管会话 + `--mutate`（最终口径）** = **`ok 41 / FAIL 0 / pend 1`**（`factcandidates/backfill-db-session-mutate-final.log`，`EXIT=0`；`ok 变异覆盖：11/11 条全部执行`；唯一 pend 是 `G-05`，脚本明确不代跑浏览器）；
`④` **无 DB / 无会话 + `--mutate`** = **`ok 37 / FAIL 0 / pend 2`**（`mutations/backfill-full-mutate-after-coverage-fix.log`，pend = DDL 实测 + G-05）。
四个数字（25 / 29 / 41 / 37）是**同一脚本在四种条件下**的结果，口径对照见 `qa-2026-11-12-badge-pend-conversion/mutations/README.md`；不可相加、不可互换。
三条 pend 的转换实测于 2026-11-12T21:30–21:40+0800，代码基线 **`4e1876331`**（`apps/**`、`docs/**`、`contracts/**` 与该 commit 逐字节一致）；实测运行时 HEAD `efef52ddc`（仅 `tests/browser-admin-390x844.mjs` 的 badges profile 差异）；未提交态只有 `knowledge/**` 与 `extensions/fixtures/**`。
§5 的 GAP-1/2/3（后端 task-12）与 GAP-4（前端）**全部关闭**，对应断言已从 pend 转 ok。首版剩下的 3 条人工门 pend（真实库 DDL 实测 `A-01`/`B-04`、390 视觉 QA `G-05`）本轮已用真实条件补跑并转 ok，逐步证据见 §8.1，原始输出在 `knowledge/reports/qa-2026-11-12-badge-pend-conversion/`（`ddl/` + `390/`）；`MUT-*` 变异自检由老运整轮闭合（见 §8.2），脚本最终口径见 §Status ③（带集群 + 会话 + `--mutate`）。
计划：`knowledge/plans/2026-11-12-badge-rule-engine.md` §13#15、§15.2、§9
模块背景：`knowledge/modules/badges.md`（假绿七形态 / M2 四陷阱 / Known Residuals 6 的 429 纪律）
基线：**`4e1876331`**（三段提交 `4457106e2` apps / `082f98624` web / `4e1876331` contracts 之后，`apps/**` 与之逐字节一致）；**实测运行时 HEAD `efef52ddc`**（只差 `tests/browser-admin-390x844.mjs` 的 badges profile），21:29 之后的所有实测（390 QA 21:31、变异整轮 21:35+）都在这个 HEAD 上跑；未提交态只有 `knowledge/**` 与 `extensions/fixtures/**`。更早的工作树基线 `d502f19a3` 已作废。
执行载体：`scripts/verify-badge-backfill-progress.sh`（ok / FAIL / pend 三态，**不接门禁**）

本文件不写生产代码。它定义：断言 ID、验证方式、通过的**判据（按失败类写）**、
以及**删掉哪一行实现这条断言会红**（§3 变异矩阵，硬要求）。

---

## 0. 判定规则、环境与命令代号

### 0.1 三种结果（与 M1 矩阵同口径）

| 结果 | 含义 |
| --- | --- |
| ok | 按「验证方式」实际跑过，且满足判据 |
| FAIL | 代码已落地、行为不符判据（要修，归属写在行内） |
| pend | 前置不满足（切片未落地 / 无管理员会话 / dev 429 限流 / 夹具未备），**不算通过** |

**任何一条不得以「读代码看起来对」记为 ok。** 判据必须能写成「删掉实现 → 断言变红」。

### 0.2 环境

```bash
export TMPDIR=/home/inkedus/.cache/dsh-tmp
./scripts/dev.sh                 # PG / Redis / Mailpit
./scripts/api-dev.sh             # API（dev 内嵌 worker）
cd apps/web && bun run dev       # 用户自起的 3000，勿杀
```

DB 断言只走一次性集群（`./scripts/test-api-isolated.sh`）或**只读** `DATABASE_URL` 查询；
不用裸 `go test` 撞 dev 库。dev 后台写端点有 IP 限流：**429 → 退避重试 → 仍 429 记 pend，不判 FAIL**。

### 0.3 命令代号

| 代号 | 命令 |
| --- | --- |
| `CMD-ISO-JOB` | `./scripts/test-api-isolated.sh ./app/Jobs/Badges/...` |
| `CMD-ISO-BADGE` | `./scripts/test-api-isolated.sh ./app/Models/Badges/... ./app/Http/Controllers/Badges/...` |
| `CMD-ISO-ROUTE` | `./scripts/test-api-isolated.sh ./app/Support/Routes/...` |
| `CMD-BUILD` | `cd apps/api && go build ./...` |
| `CMD-OAS` | `ruby scripts/validate-openapi-refs.rb` |
| `CMD-CAT` | `node tests/validate-v3-p0-catalogs.mjs` |
| `CMD-ARCH` | `node tests/validate-architecture-boundaries.mjs` |
| `CMD-WEB` | `cd apps/web && bun test` |
| `CMD-TYPE` | `cd apps/web && bun run typecheck` |
| `CMD-DDL` | `DATABASE_URL=... psql -tAq -c '<SQL>'`（脚本内 `sqlv`，只读） |
| `CMD-API` | `curl -sS -D- -b "$SFORUM_SESSION_COOKIE" "$SFORUM_ACCEPT_API_URL/admin/badges/backfill-status"` |
| `CMD-DIFF` | `git diff -- <file>`（变异前保存副本，恢复后 `cmp` 校验） |

### 0.4 执行载体

`scripts/verify-badge-backfill-progress.sh`（ok / FAIL / pend，**不接门禁**）：

| 用法 | 作用 |
| --- | --- |
| `./scripts/verify-badge-backfill-progress.sh` | 全量：静态 + DDL + go/bun 测试 |
| `--no-tests` | 只跑静态/DDL（改一行代码就能跑） |
| `--in-flight` | task-1/task-2 落地中：结构性缺失记 pend。**收口时不许带这个 flag** |
| `--mutate [MUT-id]` | 按 `scripts/verify-badge-backfill-mutations.tsv` 做变异自检；锚点过期（sed 无改动）记 FAIL，不静默跳过 |
| `--strict` | 有 pend 即 exit 4（收口用） |

退出码：`0` 无 FAIL / `1` 有 FAIL / `2` 预检失败 / `4` strict 下有 pend。

### 0.5 断言前缀

| 前缀 | 责任面 |
| --- | --- |
| `A-*` | 契约与注册面（迁移 / 路由 / 计数 / OpenAPI / wire 形状 / 缓存头与权限） |
| `B-*` | **来源真伪**：数字必须来自 job 写的行，不是从授予数反推 |
| `C-*` | 状态机：入队建行 / 续页 / 末页 / 页错 / 重试翻转 / 截断 / 写状态失败不掩盖主错 |
| `D-*` | Enqueuer 未接通不许静默（含反向：正常完成不许被标失败） |
| `E-*` | 前端降级（500 / 超时 / 形状不符 / 空 items / 429 纪律） |
| `F-*` | 失败可定位与呈现（值级文案 / 聚合顺序 / 24h 收隐 / 刷新按钮） |
| `G-*` | 横切（i18n 双向、值级文案、内部字段不泄露、几何/无新增壳） |
| `MUT-*` | 变异自检（断言红/绿的证明，不是断言本身） |

---

## 1. 冻结契约（验收对象，逐条对应断言）

1. 表 `badge_rule_backfill_runs`：`id`、`badge_id`（FK `badges` ON DELETE CASCADE）、`status ∈ {running,completed,failed}`、`last_user_id BIGINT NOT NULL DEFAULT 0`、`failed_cursor_user_id BIGINT NOT NULL DEFAULT 0`、`batch`、`error TEXT NOT NULL DEFAULT ''`、`started_at/updated_at`、`finished_at`（可空）；索引 `(badge_id, started_at DESC)` 与 `(status) WHERE status='running'`；Down 段 `DROP TABLE`；末尾 owner 对齐（`pg_get_userbyid` + `ALTER … OWNER TO`）。
2. `GET /api/v1/admin/badges/backfill-status` → `{"items":[{badgeId,slug,name,status,lastUserId,failedCursorUserId,startedAt,updatedAt,finishedAt,error}]}`；无 run → `{"items":[]}` + HTTP 200；`Cache-Control: no-store`；权限与 `rule-health` 一致（`badge.view`）。
3. `POST /admin/badges/recompute` 仍是 `{granted,refreshed,queued}`，**三键且只有三键**。
4. 路由计数 421→**422**（`Support/Routes/registry_test.go`），identities `routes` 同数，生成器已重跑，OpenAPI `paths/badges.yaml` + `schemas/badges.yaml` 已补。
5. 状态只由 job 侧写（入队侧建 running 行）；**任何数字都不从 `user_badges`/授予数反推**。

---

## 2. 断言矩阵

> 每条列三件事：**断言**、**判据（写失败类，不写实现细节）**、**删哪行实现会红**（对应 §3 变异 ID）。

### A. 契约与注册面

| ID | 断言 | 判据（失败类） | 会红的变异 |
| --- | --- | --- | --- |
| A-01 | 迁移落地且结构完整 | 表存在；列集合**等于**冻结集合（缺列红、多列也红——多出来的列没人写，是毒）；`status` CHECK 恰为三值；两个索引都在；FK 为 CASCADE；Down 段真的 `DROP TABLE`；owner 对齐块存在（`pg_get_userbyid` + `ALTER … OWNER TO`） | MUT-11 |
| A-02 | `/backfill-status` 注册在 `/:badgeID` **之前** | `RegisterRoutes` 体内 `Get("/backfill-status")` 的行号 < 第一个 `Get("/:badgeID…")`；且 `route_order_test.go` 对该路径有钉住断言。**失败类**：顺序错 → Fiber 把 `backfill-status` 当 badgeID 解析 → 500/「invalid id」，不是 404 | MUT-12 |
| A-03 | 路由计数双源一致 = **422** | `registry_test.go` 的期望值 = 422；`docs/extensions/v3/catalog-identities.json` 的 `routes` 长度 = 422；`Support/Routes/core_catalog_gen.go` 与 `catalogs/routes.json` 都含 `backfill-status`；生成物与源一致（重跑生成器无 diff） | MUT-7 |
| A-04 | OpenAPI 补齐且 `$ref` 合法 | `paths/badges.yaml` 有该 path（`operationId` 与路由 ID 一致）；`schemas/badges.yaml` 有响应/条目 schema 且 `required` 与 wire 一致；`CMD-OAS` exit 0 | MUT-8 |
| A-05 | `recompute` 恰好三键 | 把响应 decode 成 `map[string]json.RawMessage`，**键集合排序后 == `[granted,queued,refreshed]`**；多一个键红、少一个键红。**不做「至少包含」断言**（那正是 M1 漏 `queued` 的假绿形态） | MUT-6 |
| A-06 | `backfill-status` 键集合与空值形态 | 条目键集合 == 冻结 10 键（`badgeId,slug,name,status,lastUserId,failedCursorUserId,startedAt,updatedAt,finishedAt,error`）；无 run 时 JSON 里 `items` 是 `[]` **不是 `null`**（未初始化切片会输出 `null`）；`status` 值 ∈ 三枚举 | MUT-10 |
| A-07 | 缓存头与权限 | 200 响应带 `Cache-Control: no-store`；未登录 → 401；无 `badge.view` → 403（denied 路径也要有测试，不只是 allowed） | MUT-5 |

### B. 来源真伪（本刀最大假绿风险）

| ID | 断言 | 判据（失败类） | 会红的变异 |
| --- | --- | --- | --- |
| B-01 | 进度来自 **job 写的行**，不是授予数 | 夹具：某 badge 的 `user_badges` 行数 = **0**，但 job 写入 run 行 `last_user_id=500 status=running`。端点返回 `lastUserId == 500`（值级）。**失败类**：实现从 `max(user_badges.id)` / `count(*)` 反推 → 这里会得到 0/空 → 红 | MUT-4 |
| B-02 | 有授予、无 run 行 → **不合成条目** | 某 badge 有 10 条 `user_badges`，但表里无 run 行：端点 `items == []`（不出现该 badge）。**失败类**：实现按 badges 表 LEFT JOIN 出「0% 进度」的假行 | MUT-4 |
| B-03 | 同 badge 多 run 只取最新 | 旧 run `completed`（`started_at` 更早）+ 新 run `running`：只 1 条，`status=running`；把两条都返回 → 红 | — |
| B-04 | 徽章删除后无孤儿行 | 删 badge → run 行随 CASCADE 消失；端点不再返回该 badge。**失败类**：FK 写成不带 ON DELETE 的引用 → 删 badge 报错或留下孤儿 | MUT-11 |
| B-05 | 读取路径不碰 `user_badges` | `backfill_controller.go` / `backfill_runs.go`（读取侧）出现 `user_badges` / `COUNT(` 即 FAIL，需人工确认是不是又走了授予数。**注**：这是行程式 tripwire，不是判据本身，注释里出现不豁免但要人工复核 | MUT-4 |

### C. 状态机（四种转移 + 三条边界）

| ID | 断言 | 判据（失败类） | 会红的变异 |
| --- | --- | --- | --- |
| C-01 | 续页 → `running` | 页成功 `done=false`：`status=running`、`last_user_id=next`、`failed_cursor_user_id=0`、`finished_at IS NULL`、`error=''`；且下一页被入队（游标 = next） | MUT-9 |
| C-02 | 末页 → `completed` | 页成功 `done=true`：`status=completed`、`last_user_id=next`、`finished_at` **非空**、`error=''` | MUT-2 |
| C-03 | 页错 → `failed`，游标**值级**可定位 | `failed_cursor_user_id == 该页 args.CursorUserID`（**不是 0、不是 next、不是第一页的 0**）；`error` 非空且含页/游标语义；`finished_at IS NULL`（river 还会重试）；日志仍打原错误 | MUT-1 |
| C-04 | 重试成功 → 翻回 `running` | 失败页后用**同一游标**重跑成功 `done=false`：`status=running`、`failed_cursor_user_id=0`、`error=''`。**失败类**：只在失败时写、成功时不清 → 永远停在 failed | MUT-9 |
| C-05 | 入队即建 `running` 行 | `queued>0` 之后、第一页跑完之前，表里已有该 badge 的 run 行（`status=running`、`last_user_id=0`）。**失败类**：只在页回调里 upsert → 「已入队但查不到」的空窗 | MUT-13 |
| C-06 | `error` 截断 ≤500 字符 | 喂 2KB 错误：`length(error) <= 500` 且非空（不因截断变空串） | — |
| C-07 | 状态写失败不掩盖主错 | 页成功但状态写失败 → `Work` 返回错误（让 river 重试当前页），不是 `nil`；页失败 + 状态写失败 → 返回**主错**（`errors.Is` 到 runner 错误），不是状态写错误 | — |

### D. Enqueuer 未接通不许静默

| ID | 断言 | 判据（失败类） | 会红的变异 |
| --- | --- | --- | --- |
| D-01 | `Enqueuer == nil` 且未完成 → `failed` + `error` 非空 | 状态行被写成 `failed` 且 `error` 非空（**不是**静默 `return nil`）；且 `Work` 的返回值本身不是「入队成功」语义（允许返回 nil，但状态必须可见） | MUT-14 |
| D-02 | `Enqueuer == nil` 但 `done=true` → `completed` | 反向判据：不许把「本来就跑完了」标成 failed（防御 MUT-14 的过度修正） | — |
| D-03 | 入队下一页失败 | `Enqueue` 返回错误 → `Work` 返回该错误（river 重试当前页），状态仍为 `running`（不标 failed，因为页本身没错） | — |

### E. 前端降级

| ID | 断言 | 判据（失败类） | 会红的变异 |
| --- | --- | --- | --- |
| E-01 | 端点 500 → `[]` + 可见提示 | 注入 500：composable 返回 `items:[]` 且 `failed=true`；组件渲染**可见**提示；无未捕获 rejection（`window.onunhandledrejection` 计数 0）；页面其余部分照常渲染 | MUT-3 |
| E-02 | 超时 / 网络 reject → 同上 | 注入 rejected promise（超时）：同上判据 | MUT-3 |
| E-03 | 形状不符 → **按失败处理**，不许当成功 | 喂裸数组 / `{items:null}` / `items[0].status` 非字符串 / 缺 `badgeId`：全部 `failed=true` + 提示。**失败类**：兼容两种形状（M1 的 rule-health composable 就是这样）→ 后端改形状时前端静默继续工作，契约漂移永不红 | MUT-3 |
| E-04 | `items:[]` → 不渲染该行、不渲染空态、不提示 | 正常空结果是「没事」，不是「加载失败」也不是「空数据引导」 | — |
| E-05 | dev 429 只记 pend | 验收脚本遇 429 退避后仍 429 → pend，不判 FAIL（纪律断言，不是产品行为） | — |

### F. 失败可定位与呈现

| ID | 断言 | 判据（失败类） | 会红的变异 |
| --- | --- | --- | --- |
| F-01 | failed 行文案**值级**含徽章名 + 用户 id | 渲染输出同时包含夹具里的徽章名（如 `元老`）与游标数字（如 `1234`）。**不做「键存在」断言**：键在值空（DTO 无 omitempty）时那条会假绿 | MUT-1、MUT-3 |
| F-02 | running 行文案值级含已扫描用户 id | 渲染输出包含 `lastUserId` 的数字值 | MUT-9 |
| F-03 | 多条 run 的聚合与顺序 | 5 条（2 running / 1 failed / 2 completed）→ 只显示 3 个徽章名 + 「等 2 枚」；顺序 running > failed > completed；「等 N 枚」的 N = 总数 − 3 | — |
| F-04 | 24h 收隐的**双向** | 全部 `completed` 且最早 `finishedAt` > 24h → 不渲染；全部 `completed` 但 < 24h → **仍渲染**（只测「不显示」会让「永远不显示」也通过） | MUT-2 |
| F-05 | 刷新按钮只刷本行 | 按钮带 `i-tabler-refresh` + `aria-label` + `title`；点击一次 → 进度 loader 调用 +1，**整页 badge 列表 loader 调用不变** | — |
| F-06 | recompute `queued>0` 自动刷新一次 | 现有 toast 流程后 loader 调用正好 +1（不是 0，也不是每次渲染都刷）；toast 文案语义不变 | — |

### G. 横切

| ID | 断言 | 判据（失败类） | 会红的变异 |
| --- | --- | --- | --- |
| G-01 | i18n 双向 | `adminBadgesI18n.test.ts` 在（引用键都存在 + locale 无多余键）；动态 status 文案登记进 `DYNAMIC_ENUMERATIONS`；zh/en 键集合相同 | — |
| G-02 | 文案值级 | 新文案 zh/en 均非空、无 `{…}` 占位残留、无裸数字拼接；含 `{n}` 之类占位符的必须真的被插值 | — |
| G-03 | 内部字段名不泄露 | 词边界检查 `\b(cmp|params|children|op|rule)\b` 于渲染文案；**检查器自身要有正反控**：`rule` / `op` 命中、`topics` 不命中（朴素子串检查会把 `topics` 判成 `op`） | — |
| G-04 | 不新增壳 | `SFAdminBadgesBadgesTab.vue` 内不新增 `UCard` / 页头 / tab；进度行模板在筛选区**之前**（按模板顺序断言） | — |
| G-05 | 390 无横向溢出 | 需管理员会话：`scrollWidth == innerWidth`、越界控件 0；拿不到会话 → pend 并写明缺什么（不编截图） | — |

---

## 3. 变异矩阵（硬要求：断言必须真的会红）

做法：改一个文件（先 `cp` 备份）→ 跑对应命令 → **必须有 red** 且 red 的理由对得上 →
`cp` 恢复 → `cmp` 确认字节一致 → 复跑必须全绿。脚本 `--mutate` 按
`scripts/verify-badge-backfill-mutations.tsv` 执行；锚点不匹配（sed 无改动）记 FAIL(mutation-anchor-stale)，
**不许静默跳过**。

| MUT | 删/改什么 | 必须变红的断言 | 期望输出特征 |
| --- | --- | --- | --- |
| MUT-1 | job 失败分支里写 `failed_cursor_user_id` 的那句 | C-03、F-01 | 失败行游标 = 0；前端只显示徽章名不显示用户 id |
| MUT-2 | 「`done` → `completed`」分支（改成一律 running 或直接 return） | C-02、F-04 | 末页停在 running / `finished_at` 恒 NULL；24h 收隐逻辑拿不到终态 |
| MUT-3 | 前端失败态提示（`failed` 时不渲染提示） | E-01、E-03、F-01 | 降级静默；失败行无文案 |
| MUT-4 | 读取侧改成从授予数反推（`lastUserId = COALESCE(max(user_badges.id),0)` 或 count） | B-01、B-02、B-05 | 零授予夹具显示 0；无 run 行却出现条目 |
| MUT-5 | 删 `setAdminNoStore(c)` / no-store 头 | A-07 | 响应头无 `no-store` |
| MUT-6 | `recomputeResponse` 加第四键（如 `skipped`） | A-05 | 键集合多一枚 |
| MUT-7 | `registry_test.go` 422 → 421 | A-03 | 计数门禁红 |
| MUT-8 | 删 OpenAPI path 条目 | A-04 | `$ref` 校验红 |
| MUT-9 | 成功页里不清 `failed_cursor_user_id` / 不写回 `running` | C-01、C-04、F-02 | failed 永不翻转；进度不前进 |
| MUT-10 | 列表初始化删掉（`items := []T(nil)` → `var items []T`） | A-06 | 空结果输出 `"items":null` |
| MUT-11 | 迁移 FK 去掉 `ON DELETE CASCADE` / 删 owner 对齐 DO 块 | B-04、A-01 | 删 badge 报 FK 错；owner 断言红 |
| MUT-12 | 把 `/backfill-status` 注册挪到 `/:badgeID` 之后 | A-02 | 路由顺序测试红（真打到端点会 500/invalid id） |
| MUT-13 | 入队侧不建 running 行（只在页回调 upsert） | C-05 | `queued>0` 后查不到行 |
| MUT-14 | `Enqueuer == nil` 恢复成静默 `return nil` | D-01 | 状态停在 running，error 空 |

`MUT-1/2/3/4/7/8` 是 Lead 点名的六条，其余是本次新增（覆盖「空 `items` 必须是 `[]`」
「owner 对齐」「入队建行」这些**容易被后续刀改回去**的点）。

---

## 4. 本刀特有的假绿风险（逐条对应防法）

1. **数字从授予数反推**：这是最像真的假进度——授予数有值、进度看着在涨，但和回扫游标无关。防法 = B-01 的零授予夹具 + B-02 的反向夹具 + B-05 的读取路径 tripwire。
2. **失败态只验键存在**：`error` / `failedCursorUserId` 键一定在（无 omitempty），值可以是空串/0。防法 = C-03/F-01 全值级。
3. **前端「兼容两种形状」**：M1 的 `useAdminBadgeRuleHealth` 同时接受 `{items}` 与裸数组 → 后端换形状前端静默活。本刀契约已冻结，E-03 要求只认一种。
4. **只验 running 不验翻转**：C-04 是唯一能抓「失败后永远 failed」的断言，漏了它，重试用尽后的锁死只有线上能发现。
5. **空夹具断言**：`items == []` 在夹具为空时是空断言。B-01/B-02 必须先断言夹具非空（`user_badges` 0 行是**故意**的，run 行 1 条是**必须有**的）。
6. **契约 tripwire 只在数字上**：421→422 只钉了一个数，identities/catalogs/OpenAPI 三面要一起钉（A-03/A-04），否则「数改了但生成物没重跑」照绿。
7. **测试借环境**：需要管理员会话的 F-05/G-05、需要 dev 后台写端点的项一律 pend 并写明缺什么；`items: []` 不需要会话即可验。

---

## 5. 缺陷与缺口台账（终版）

**没有行为级 FAIL**。以下是执行期发现的测试/门禁缺口，全部「代码看着对但证明缺席」那一类，
建议由实现方补，不阻塞第一刀收口：

| ID | 缺口 | 归属 | 建议 | 等级 |
| --- | --- | --- | --- | --- |
| GAP-1 | 新端点控制器测试只验 wire 形状与 `items` 包装，没有 no-store/403/401 断言 | task-12（后端） | **已关闭**：`backfill_status_test.go` 三条路径独立断言（no-store 逐字 / 403 / 401），脚本 RUN=3 PASS | closed |
| GAP-2 | 缺「有授予、无 run 行 → `items` 为空」用例 | task-12（后端） | **已关闭**：`TestBackfillRunsIgnoreGrantRows` PASS；B-02 转 ok | closed |
| GAP-3 | 缺「done+nil → completed」与「入队失败 → 返错且仍 running」两条用例 | task-12（后端） | **已关闭**：`CompletesLastPageWithoutEnqueuer` / `KeepsRunningWhenEnqueueFails` 均 PASS | closed |
| GAP-4 | `queued>0` 后自动刷新只有代码路径，没有 loader 计数断言 | 前端 | **已关闭**：真值表 + 页壳挂载断言调用次数（`adminBadgeRecomputeBackfillRefresh.test.ts`）；F-06 转 ok | closed |

**变异覆盖的诚实边界**：`MUT-11`（FK 去 CASCADE）/`MUT-12`（把 `/backfill-status` 挪到
`/:badgeID` 后）/`MUT-13`（入队侧不建 running 行）没有进自动化变异表——前两者要移动/删除语句，
`sed` 锚点不适配；`MUT-11` 另需真实迁移库。它们当前的可信替代证据：
`TestBackfillRunsVanishWithBadge`（fixture DDL 里的级联）、`route_order_test.go`（顺序钉住）、
`TestBackfillRunsProgressLifecycle` 首段（`StartBackfillRun` → running 行）。**迁移文件本身的
CASCADE 语句原先只有静态断言** —— 2026-11-12T21:30–21:32 已用一次性集群补实（`A-01` 的 `B-04 confdeltype == c` + `ddl/cascade-probe.log` 的真实删除探针，见 §6.2 与 `qa-2026-11-12-badge-pend-conversion/ddl/`），本条不再是残余边界。

---

## 6. 执行记录（终版，2026-11-12T20:29+0800 起；代码基线 `4e1876331`，`apps/**` 与之逐字节一致；实测运行时 HEAD `efef52ddc`，未提交态只有 `knowledge/**` 与 `extensions/fixtures/**`）

> **pend 转换记录（2026-11-12T21:30–21:40+0800）**：`A-01` / `B-04` / `G-05` 三条的转 ok 见 §8.1，原始输出在 `knowledge/reports/qa-2026-11-12-badge-pend-conversion/`（`ddl/` 十五条 DDL 断言 + 迁移可逆 + 级联探针；`390/` 浏览器证据 JSON + 两张 PNG + 清理复核）。**判据列一字未改**，§2 的判据文本保持原文。

### 6.1 命令与原始 exit code

| 代号 / 命令 | exit | 输出要点 |
| --- | --- | --- |
| `./scripts/verify-badge-backfill-progress.sh`（全量，**不带** `--in-flight`，GAP 补口后复跑） | **0** | `ok 25 / FAIL 0 / pend 3` |
| `-run 'TestBackfillStatus(ServesNoStore…|Denies…|RequiresLogin)'`（A-07） | **0** | `RUN=3` |
| `-run 'TestBackfillRuns(ProgressLifecycle|IgnoreGrantRows|KeepsOnlyLatestRowPerBadge|VanishWithBadge)'`（B-01..B-04） | **0** | 4 个用例全 PASS |
| `-run 'TestBackfillRulesWorker(CompletesLastPageWithoutEnqueuer|KeepsRunningWhenEnqueueFails)'`（D-02/D-03） | **0** | 2 个用例 PASS |
| `./scripts/test-api-isolated.sh ./app/Models/Badges/... ./app/Jobs/Badges/... ./app/Http/Controllers/Badges/...` | **0** | 四个包全 `ok`（一次性集群，迁移 165 条 + river migration） |
| `./scripts/test-api-isolated.sh -v -run 'Backfill' …`（C-08 合法路径） | **0** | `run 计数 12`（三态 / 空 items / latest / cascade / clamp / wire） |
| `cd apps/web && bun test`（脚本内） | **0** | E/F/G 行为测试全绿（实现方报告全量 2554 pass） |
| `ruby scripts/validate-openapi-refs.rb` | **0** | 3296 refs 绿（入口三条漏引用已由老后补） |
| `./scripts/verify-badge-backfill-progress.sh --mutate` | **0** | 11 条变异全部「变红→恢复→`cmp` 字节一致」 |
| E-06 临时探针 `bun test tests/admin/zzAcceptanceBackfillDegradation.test.ts` | **0** | `1 pass / 0 fail / 4 expect`，探针跑完**已删除**（不进仓库） |

### 6.2 断言逐条结果

| 断言 | 判据（失败类） | 实跑依据 | 结果 |
| --- | --- | --- | --- |
| A-01 | 迁移结构完整（缺列/多列/枚举/索引/FK/Down/owner 对齐任一缺失即红） | 静态全项在位（`202611120003_badge_backfill_runs.sql`）**＋ 真实库 DDL 实测**：一次性集群 `sforum-migration-verify-ddad8f4e8d16487d298ec54a`（`postgres:17-alpine` 17.11，`guard=f` 证明非 dev 库）上跑 `ddl/ddl-up-assertions.sh` → `SUMMARY pass=15 fail=0`（A-01a..A-01n 十四条：表存在 / 列集合**等于**冻结 10 列 / 序列与 `id` serial 绑定 / 两索引 indexdef 值级 / 索引总数==3 / FK 指向 `public.badges` / FK 列只有 `badge_id` / status CHECK 枚举集合恰为 `completed,failed,running` / status 默认 `running` / 表·序列·两索引 owner 与 `badges` 同源 `sforum_core_o_647c53928ce8ab21125c`）；行为探针：`queued` 与 `RUNNING` 均被 CHECK 拒；Down 侧 `ddl/migration-reversible.log` exit 0（表·两索引·序列真的消失、goose 版退回 `202611120002`、再 up 全部重建） | ok |
| A-02 | `/backfill-status` 注册早于 `/:badgeID`，且有回归测试钉住 | 顺序检查 + `route_order_test.go` 命中 | ok |
| A-03 | 计数 422 四面一致（registry_test / identities / `core_catalog_gen.go` / `catalogs/routes.json`） | 四面均 422 且含该路径 | ok |
| A-04 | OpenAPI：操作定义 + entrypoint 路径映射 + schema，`$ref` 绿 | `adminBadgeBackfillStatus` + `"/admin/badges/backfill-status"` + `BadgeBackfillRun`，`validate-openapi-refs` exit 0 | ok |
| A-05 | `recompute` 恰好三键（多/少/改名都红） | 结构体 json 标签 == `{granted,refreshed,queued}`；MUT-6 改名后红 | ok |
| A-06 | 条目 == 冻结 10 键；空结果是 `[]` 不是 `null` | DTO 键集合精确相等；MUT-10 让空切片变 `nil` → `TestBackfillRunsEmptyIsEmptySlice` 红 | ok |
| A-07 | 成功 `Cache-Control: private, no-store`；缺 `badge.view` → 403 `permission.denied`；未登录 → 401 | `backfill_status_test.go` 三条路径各自独立断言（GAP-1 已补）；脚本跑 `-run 'TestBackfillStatus(ServesNoStore…|Denies…|RequiresLogin)'` → RUN=3 全 PASS。**域层同样验到**：`TestBackfillStatusRequiresBadgeView` 断言 `identity.ErrPermissionDenied` 且 store 零调用（两条路径都覆盖，不是只验 HTTP） | ok |
| B-01 | 授予数 0 时进度仍显示游标（反证不从 `user_badges` 反推） | `TestBackfillRunsProgressLifecycle`：fixture 无授予行，游标 500→900→1200 真实回读；MUT-4（改成 `count(user_badges)`）该用例红 | ok |
| B-02 | 有授予、无 run 行 → 不合成条目 | `backfill_runs_grants_test.go` 的 `TestBackfillRunsIgnoreGrantRows`（GAP-2 已补）PASS；读取 SQL 以 runs 为驱动表 | ok |
| B-03 | 同徽章多 run 只回最新一条 | `TestBackfillRunsKeepsOnlyLatestRowPerBadge` 绿 | ok |
| B-04 | 删徽章 → run 行随 FK 消失，端点无孤儿行 | `TestBackfillRunsVanishWithBadge` 绿；迁移 CASCADE 语句静态在位；**迁移级由真实库补实**：`A-01` 的 `B-04 confdeltype == c` PASS（目录级）＋ `ddl/cascade-probe.log` 的真实删除探针（插 badge 13 与 running run 行 → `DELETE badge` 无报错 → 删后 run 行 **1→0**、全表孤儿 **0**）；失败类反证写明：FK 不带 `ON DELETE` 时第 4 步会 `violates foreign key constraint`、第 5 步为 1 | ok |
| B-05 | 读取侧不出现 `user_badges`/`COUNT(` 反推 | 读取两文件无命中（注释已排除） | ok（tripwire） |
| C-01 | 续页 → `running` + 新游标 + 清零 | `RecordsRunningProgress` + 真库 lifecycle；MUT-9 红 | ok |
| C-02 | 末页 → `completed` + `finished_at` 非空 | `RecordsCompletedOnLastPage` + lifecycle；MUT-2 红 | ok |
| C-03 | 页错 → `failed` 且 `failed_cursor_user_id == 该页 args.CursorUserID`（值级） | `RecordsFailedCursor`（cursor 250，非 0 非返回游标）；MUT-1 红 | ok |
| C-04 | 重试成功 → 翻回 `running`，`error`/`failed_cursor` 清零 | `RetryAdvancesAfterFailure` + lifecycle；MUT-9 红 | ok |
| C-05 | 入队即建 `running` 行（第一页跑完前就可见） | lifecycle 首段：`StartBackfillRun` → `running`、游标 0、无 `finished_at` | ok |
| C-06 | `error` 截断 ≤500 字符且非空 | `TestBackfillRunsClampStoredError` 绿 | ok |
| C-07 | 状态写失败不掩盖主错 | `ReturnsErrorWhenProgressWriteFails` + `KeepsMainErrorWhenFailureWriteFails` 绿 | ok |
| C-08 | **合法的不许被误拒**：running/completed/failed + 空 `items` 全 200 且形状合规 | `-run Backfill -v`：12 个用例全绿（含 `TestBackfillStatusResponseWrapsItems`、`TestBackfillRunStatusWireShape`） | ok |
| D-01 | `Enqueuer=nil` 且未完成 → `failed` + error 非空 | `FlagsBrokenChainWhenEnqueuerMissing` 绿；MUT-14 恢复静默后红 | ok |
| D-02 | 反向：`done=true` 且 `Enqueuer=nil` → `completed` | `TestBackfillRulesWorkerCompletesLastPageWithoutEnqueuer`（GAP-3 已补）PASS | ok |
| D-03 | 入队下一页失败 → 返回错误，状态仍 `running` | `TestBackfillRulesWorkerKeepsRunningWhenEnqueueFails`（GAP-3 已补）PASS | ok |
| E-01 | 端点 500 → `[]` + 可见提示 + 无未捕获 rejection | `a throwing endpoint degrades to an empty list plus a visible failure flag`；MUT-3 红 | ok |
| E-02 | 超时/网络 reject → 同上 | 同上用例族（rejection 路径） | ok |
| E-03 | 形状不符 → 按失败处理（不许兼容裸数组） | `rejects a bare array so contract drift cannot stay green` + `wrong-shaped payload is a failure` | ok |
| E-04 | `items:[]` → 不渲染、不提示 | `an empty items list is success with nothing to show` + `renders nothing when there is no run` | ok |
| E-05 | dev 429 只记 pend | 未触发（本次无写端点调用） | ok（纪律） |
| E-06 | **降级方向**：500 时页面主体（筛选区 + 表格）仍渲染 | 临时探针挂**真实** tab + **真实** row：`failed=true` → `admin.badges.backfill.unavailable` 可见、`USelect` 计数 >0、`badge-table-stub` 在、提示在筛选区之前；`1 pass / 4 expect` | ok（探针已删） |
| F-01 | failed 文案值级含徽章名 + 用户 id | `failed names both the badge and the exact user id`（值级） | ok |
| F-02 | running 文案值级含已扫描用户 id | `running says where it has scanned to…` | ok |
| F-03 | 聚合顺序 running>failed>completed、≤3 名 + 「等 N 枚」 | `summarizeBackfillRuns` 三条用例全绿 | ok |
| F-04 | 24h 收隐**双向**（<24h 仍渲染） | `inside 24h still shows` + `older than 24h renders nothing` | ok |
| F-05 | 刷新按钮只刷本行（图标/aria/title + 回调只到壳层） | tab 挂载用例：行在筛选区前、`refresh` 回调触发 1 次 | ok |
| F-06 | `queued>0` 自动刷**一次**（0 不刷） | GAP-4 已补：`shouldRefreshBackfillAfterRecompute` 真值表（0/null/负数/NaN 均 false）+ 页壳挂载断言 loader 调用次数（`queued>0` 恰 1 次、0 与缺失 0 次） | ok |
| G-01 | i18n 双向（引用存在 + locale 无多余键） | `adminBadgesI18n.test.ts` 绿 + 新键双语命中 | ok |
| G-02 | 值级文案无占位残留/裸数字 | 行用例按值断言文案（含被截断名字与 id） | ok |
| G-03 | 内部字段不泄露 + 检查器正反控 | 脚本词边界检查器自检（`rule`/`op` 命中、`topics` 不命中）通过；locale 值无泄露 | ok |
| G-04 | 不新增壳；行在筛选区之前 | 模板顺序断言 + `UCard`/`UTabs` 与 HEAD 计数比较不增 | ok |
| G-05 | 390 无横向溢出 | 真实 Chrome 154 headless + CDP `Emulation.setDeviceMetricsOverride`（390×844 dpr3）跑 `tests/browser-admin-390x844.mjs` 的 `SFORUM_ADMIN_PROFILE=badges` 档案 **exit 0**、`failures: []`：`scrollWidth 390 ≤ innerWidth 390`（`horizontalOverflow=false`）、页面级越界控件 `clippedPageCount=0`（**并排记录 `clippedRawCount=48`**，全部位于表格横向滚动容器内，先排除滚动容器再计数，两项一起记才防「排除法吃掉真溢出」）、`[data-testid="badge-backfill-progress"]` `rowCount=1` 且在筛选区上方（`rowBeforeFilter=true` + 几何 `rowBottom 439 ≤ filterTop 451`）、失败态值级文案 `回扫失败于用户 #137` / `涉及徽章：老住户`、`consoleErrors: []`；一次性超管（id=498）与夹具行单事务删除，复核 6 项计数全 0。证据 `qa-2026-11-12-badge-pend-conversion/390/evidence-390-failed.json` + 两张 PNG | ok |

---

## 7. 变异样本（11 条，全部「变红 → 恢复 → 字节一致」）

运行方式：`./scripts/verify-badge-backfill-progress.sh --mutate`（读 `scripts/verify-badge-backfill-mutations.tsv`）。
每次变异先 `cp` 备份，跑对应命令要求 **非 0**，再恢复并 `cmp` 校验；锚点不匹配记 FAIL（不静默跳过）。
**exit 0 = 11/11 都变红**。

| MUT | 改哪行 | 变异内容 | 复跑命令 | 变红的断言 / 实测红名 | 恢复 |
| --- | --- | --- | --- | --- | --- |
| MUT-1 | `Jobs/Badges/backfill_rules.go`（失败分支） | `recordFailure(..., job.Args.CursorUserID, ...)` → 游标写 `0` | iso-job | C-03/F-01：`TestBackfillRulesWorkerRecordsFailedCursor` | `cmp` 一致 |
| MUT-2 | 同上（末页分支） | `AdvanceBackfillRun(ctx, badgeID, next, true)` → `false` | iso-job | C-02/F-04：`TestBackfillRulesWorkerRecordsCompletedOnLastPage` | `cmp` 一致 |
| MUT-3 | `SFAdminBadgeBackfillRow.vue:50` | `v-if="failed \|\| summary"` → `v-if="summary"` | web | E-01/E-03/F-01：`(fail) … a failed endpoint still renders a visible hint instead of a silent gap` | `cmp` 一致 |
| MUT-4 | `Models/Badges/backfill_runs.go`（读取 SQL） | `runs.last_user_id` → `COALESCE((SELECT count(*) FROM user_badges …), 0)` | iso-badge | B-01/B-02：`TestBackfillRunsProgressLifecycle` | `cmp` 一致 |
| MUT-5 | `Controllers/Badges/backfill_controller.go:33` | 删 `setAdminNoStore(c)` | static | A-07：`FAIL A-07 no-store / 权限（缺： no-store）` | `cmp` 一致 |
| MUT-6 | `Controllers/Badges/dto.go:490` | `json:"queued"` → `json:"queuedTotal"` | static | A-05：`FAIL A-05 recompute 恰好三键（实际键：granted,queuedTotal,refreshed,）` | `cmp` 一致 |
| MUT-7 | `Support/Routes/registry_test.go:521` | `!= 422` → `!= 421` | route | A-03：`TestCoreRouteCatalogHasExactReviewedGuardParity` | `cmp` 一致（中途一次工具调用被打断，已手工复核并把 421 复原为 422，随后复跑该变异再次红+恢复） |
| MUT-8 | `contracts/openapi.yaml:721` | 路径映射改成 `…-disabled` | static | A-04：`FAIL A-04 OpenAPI 补齐（openapi.yaml 没把 … 映射到该操作）` | `cmp` 一致 |
| MUT-9 | `Models/Badges/backfill_runs.go`（Advance SQL） | `failed_cursor_user_id = 0` / `error = ''` → 自赋值 | iso-badge | C-01/C-04：`TestBackfillRunsProgressLifecycle` | `cmp` 一致 |
| MUT-10 | 同上（列表初始化） | `items := []BackfillRunStatus{}` → `var items []BackfillRunStatus` | iso-badge | A-06：`TestBackfillRunsEmptyIsEmptySlice` | `cmp` 一致 |
| MUT-14 | `Jobs/Badges/backfill_rules.go:119` | `recordFailure(…"backfill enqueuer not wired")` → `return nil` | iso-job | D-01：`TestBackfillRulesWorkerFlagsBrokenChainWhenEnqueuerMissing` | `cmp` 一致 |

**Lead 点名的六条**（失败游标 / done→completed / 前端失败提示 / 从授予数反推 / 422→421 / 删 OpenAPI）对应
`MUT-1`/`MUT-2`/`MUT-3`/`MUT-4`/`MUT-7`/`MUT-8`，全部实测变红。另 5 条（`MUT-5/6/9/10/14`）是本次补的
「容易被后续刀改回去」的点。

---

## 8. 复跑溯源

- 代码基线：**`4e1876331`**（`4457106e2` + `082f98624` + `4e1876331` 三段提交，`apps/**` 与之逐字节一致）；**实测运行时 HEAD `efef52ddc`**（只差 `tests/browser-admin-390x844.mjs` 的 badges profile），未提交态只有 `knowledge/**` 与 `extensions/fixtures/**`。`d502f19a3` 是提交前的 HEAD，已不是本轮基线。
- 复跑时间：变异整轮 `2026-11-12T20:26:45+0800` → `20:29:25+0800`；全量脚本 `20:33+0800` 复跑 **exit 0**（`ok 18 / FAIL 0 / pend 7`）；**GAP 补口后 `2026-11-12T20:5x+0800` 复跑 exit 0（`ok 25 / FAIL 0 / pend 3`）**，A-07/B-01..B-04/D-02/D-03/F-06 全部转 ok。
- 环境：`export TMPDIR=/home/inkedus/.cache/dsh-tmp`；DB 测试只走 `./scripts/test-api-isolated.sh`（一次性集群，未碰 dev 库）；`air` 监督器未参与（本刀证据全部来自隔离测试与静态/门禁命令）。
- **pend 转换（2026-11-12T21:30–21:40+0800）**：`A-01`/`B-04` 由一次性迁移集群实测（`postgres:17-alpine` 17.11，`guard=f`）、`G-05` 由真实 Chrome 154 headless 390×844 实测；三条的原始输出在 `knowledge/reports/qa-2026-11-12-badge-pend-conversion/`，逐条四段式见 §8.1。
- **变异自检（2026-11-12 22:15 后）**：`./scripts/verify-badge-backfill-progress.sh --mutate` 在「带一次性集群 + 超管会话」条件下 **`ok 41 / FAIL 0 / pend 1`**、`EXIT=0`、`ok 变异覆盖：11/11 条全部执行`；无 DB/会话的对照口径 `ok 37 / FAIL 0 / pend 2`。两种口径与 `31`/`36`/`29` 的差异全部由「DB/会话是否提供 + 是否带 `--mutate` + 是否含覆盖自检」解释，见 `qa-2026-11-12-badge-pend-conversion/mutations/README.md`。
- 载体：`scripts/verify-badge-backfill-progress.sh` + `scripts/verify-badge-backfill-mutations.tsv`（两者都不接 `test.sh` 门禁；未落地的断言在这里是 pend）。
- 失效条件：本报告的行为证据有效期 = 上述时间之后相关实现未变；改动 `backfill_runs.go` / `backfill_rules.go` / `backfill_controller.go` / `SFAdminBadgeBackfillRow.vue` 后需重跑 `--mutate`。

### 8.1 pend → ok 转换记录（2026-11-12T21:30–21:40+0800）

> 本节的「实际条件」一律指**证据文件所记录的那一次运行**；这些证据是在 `4457106e2`/`082f98624`/`4e1876331` 落地后、HEAD 已为 `efef52ddc` 时产生的，本轮未重跑。判据未改，转换只动「结果」列与实跑依据。

三条 pend 的**判据一字未改**（§2 表原样保留）。每条按「原 pend 原因 → 用了什么真实条件 → 实测输出 → 改了哪一行」写。
原始输出全部在 `knowledge/reports/qa-2026-11-12-badge-pend-conversion/`，本文件不重抄日志，只摘关键行。

#### A-01 迁移落地且结构完整（真实库 DDL）

- **原 pend 原因**：跑测环境**无 `DATABASE_URL` 且宿主无 `psql`**（脚本 `verify-badge-backfill-progress.sh:300`
  走 `pend 'B-04/A-01 DDL 实测' '查库不可用'`），只有对 `202611120003_badge_backfill_runs.sql` 的静态断言。
- **真实条件**：`SFORUM_VERIFY_KEEP_CLUSTER=1 SFORUM_MIGRATION_TARGET_VERSION=202611120003 bash
  scripts/verify-migration-reversible.sh` 自建一次性集群
  `sforum-migration-verify-ddad8f4e8d16487d298ec54a`（`postgres:17-alpine` 17.11，端口 `127.0.0.1:32780`，
  库 `sforum_migration_verify`），跑完**保留现场**再用容器内 `psql` 跑 `ddl/ddl-up-assertions.sh`。
  dev 库 `sforum-postgres:15432/sforum` **本轮从未连接**；集群内 `public.sforum_dev_guard` = **f**（证明不是 dev 库）。
- **实测输出**（`ddl/ddl-up-assertions.log` 原文末行 `SUMMARY pass=15 fail=0`；`ddl/migration-reversible.log` exit 0）：
  `PASS A-01b 列集合 == 冻结 10 列 badge_id,batch,error,failed_cursor_user_id,finished_at,id,last_user_id,started_at,status,updated_at`；
  `PASS A-01e 索引 _badge_started_idx 定义含 (badge_id, started_at DESC, id DESC)`；
  `PASS A-01f 索引 _running_idx 是 status=running 的局部索引`；`PASS A-01g 索引总数 == 3`；
  `PASS A-01h FK 指向 public.badges`；`PASS A-01j status CHECK 枚举集合恰为 completed,failed,running`；
  `PASS A-01k status 默认值 == running`；`PASS A-01l/m/n 表·序列·两索引 owner == badges owner`
  （`INFO owner_actual sforum_core_o_647c53928ce8ab21125c`）。行为侧：`queued` 与 `RUNNING` 都被
  `badge_rule_backfill_runs_status_check` 拒绝。Down 侧：表·两索引·序列 `to_regclass IS NULL`，
  `goose 版本回退到 202611120002`，再 up 全部重建。
- **改了哪一行**：§6.2 表 `A-01` 行「ok（静态）；真实库 DDL 实测 pend」→「ok」并在实跑依据列补上面 15 条断言；
  §5「变异覆盖的诚实边界」末句由「真实库 DDL 需一次性集群（本次 pend）」改为「已补实」；Status 的基线口径由 `ok 25 / pend 3` 改写为四条件并列（①②③④）。
- **诚实边角（不影响判据）**：
  - `ddl/cascade-probe.log` 里同一次运行留下的**首段报错**（`new row for relation "badges" violates check
    constraint "badges_rule_check"`、`syntax error at or near ":"`）是探针脚本自身的问题
    （`badge_id` 取值与 `psql` 变量插值），该文件后半段的第二段探针才是干净的一次（`badge_id=15`、`accepted` ×3、
    `queued`/`RUNNING` 被拒、清理后 0 行）。README §5 引的是第二段，本报告也只认第二段。
  - `ddl/migration-reversible.log` 末行打印的保留容器名（`5bbfeae7…`）与 `ddl-up-assertions.sh` 实际连的容器
    （`ddad8f4e…`）不是同一个字符串，说明中途重建过一次集群；两段日志的 `psql 17.11` 与 `guard=f` 一致，
    断言内容自洽。

#### B-04 徽章删除后无孤儿行（迁移级）

- **原 pend 原因**：行为侧 `TestBackfillRunsVanishWithBadge` 已 ok，但**迁移文件本身的 `ON DELETE CASCADE`
  只有静态断言**，没有真实库删除。
- **真实条件**：同一个一次性集群，两路证据——目录级 `confdeltype`，行为级真实 `DELETE`。
- **实测输出**：`PASS B-04 confdeltype == c（CASCADE），删 badges 行不留孤儿 c`；
  `ddl/cascade-probe.log`：插 `badge_id=13` + 1 行 `status=running` 的 run 行 → `DELETE badge`（不带任何手动级联）
  **无报错** → run 行计数 **1 → 0** → 全表 `LEFT JOIN` 孤儿检查 **0**。失败类反证写在 README §4：
  FK 不带 `ON DELETE` 时第 4 步会 `violates foreign key constraint`、第 5 步为 1。
- **改了哪一行**：§6.2 表 `B-04` 行「ok（行为）；迁移级 DDL pend」→「ok」；§5 末句同上。

#### G-05 390 无横向溢出（需管理员会话）

- **原 pend 原因**：脚本 `verify-badge-backfill-progress.sh:429` 走 `pend 'G-05 390 视觉 QA' '无管理员会话'`——
  没有管理员会话就拿不到 `/control-panel/badges` 的渲染结果。
- **真实条件**：一次性超管 `qa_pend_mwdl6414`（id=498，注册 201 → `user_roles` 授 `super_admin`，
  `auth/session` 复核 `roleKeys:["member","super_admin"]`、权限含 `admin.access`/`badge.view`），
  用 CDP `Network.setCookie` 注入 43 字符 httpOnly 会话；dev 库只插 1 行夹具
  （`badge_rule_backfill_runs`：`badge_id=2` resident「老住户」`status=failed last_user_id=137
  failed_cursor_user_id=137`）；真实 Chrome 154 headless + `Emulation.setDeviceMetricsOverride(390×844 dpr3)`
  跑 `tests/browser-admin-390x844.mjs` 的 `SFORUM_ADMIN_PROFILE=badges` 档案。
- **实测输出**（`390/badges-390-failed.log` 末行 `all admin 390x844 acceptance assertions passed`，
  `390/evidence-390-failed.json` `failures: []`）：
  `scrollWidth=390 ≤ innerWidth=390`、`horizontalOverflow=false`、`clippedPageCount=0`（**并排记 `clippedRawCount=48`**）、
  `rowCount=1`、`rowBeforeFilter=true`、`rowBottom=439 ≤ filterTop=451`、`inputTop=451`、
  `rowText="回扫失败于用户 #137 | 涉及徽章：老住户"`、`consoleErrors=[]`。
  清理：单事务删夹具行 + 账号四表，复核 `users(498)=0 / user_roles=0 / user_sessions=0 / user_credentials=0 /
  backfill_runs(qa-pend:%)=0 / user_roles_orphans=0`，`badge_rule_backfill_runs` 总数回到 **0**；
  删后拿旧 cookie 打端点 → **401**。
- **改了哪一行**：§6.2 表 `G-05` 行「无管理员会话 | pend（缺会话）」→ 值级实测依据 + 「ok」；Status 计数同上。
- **诚实边角**：
  - 这条断言**只覆盖 `status=failed` 一态**；`running`/`completed` 的 390 布局本轮没重拍
    （上一轮 `reports/qa-2026-11-12-badge-backfill-progress/` 的独立探针截图早于本轮，不能当本条证据）。
  - 证词里 48 个「原始越界」全部落在表格横向滚动容器内，是**预期滚动**，不是溢出；两项并排记录正是为了
    防止「排除滚动容器」把真溢出一起吃掉。判据仍只看页面级 `clippedPageCount == 0`。
  - 首跑是红的（`rowCount=0`，首屏 2.5s 内数据未到），修法是**让档案轮询等进度行出现（≤15s）**，
    不是放宽断言；等不到仍照常测量 → 仍会红。这一点写进 `390/G-05-390-overflow.md` §4。
  - 本条的浏览器脚本改动（`tests/browser-admin-390x844.mjs`：`--no-proxy-server` + `SFORUM_ADMIN_PROFILE` 档案）
    属 D 组未提交改动；默认档案 `route-inspector` 的 6 条断言改前改后逐字节相同
    （`route-inspector-baseline-pre-edit.log` / `route-inspector-after-edit.log`）。
  - **脚本对 G-05 只打「需浏览器实测」的 pend 提示、不代跑浏览器**，所以脚本口径里 G-05 永远是 pend；本报告的逐条表按「证据是否存在」判定（G-05 由脚本外 390 实测补实），两者口径不同，不能互相覆盖。
    脚本汇总的四种条件口径见 Status 的 ①–④（**互不相加**），逐条表是 `40 ok / 0 FAIL / 0 pend`。

### 8.2 `MUT-*` 变异自检：已闭合（2026-11-12 整轮）

「变异自检默认不跑（需 `--mutate`）」**不再是待办**：老运的变异整轮已跑完三份脚本并各自 exit 0 ——
backfill **11/11**、m3 **12/12**、fact-record **14/14**，每条都是「先变红 → `cp` 恢复 → `cmp` 字节一致」。
本报告 §3/§7 的 11 条锚点未变，复跑口径见 §7 首行的 `--mutate` 命令。

**原文归档**：`qa-2026-11-12-badge-pend-conversion/mutations/README.md`（含「数字为什么不是同一个」口径表：`29 / 31 / 35 / 36 / 37 / 41` 是不同条件，不是互相矛盾）。backfill 本轮最终口径原文 `factcandidates/backfill-db-session-mutate-final.log`（`ok 41 / FAIL 0 / pend 1`、`变异覆盖：11/11`、`EXIT=0`）；m3 是 `factcandidates/m3-mutate-final.log`（`ok 48 / FAIL 0 / pend 0`、`变异覆盖：12/12`）；fact-record 是 `mutations/fact-record-full-mutate-after-coverage-fix.log`（`ok 34 / FAIL 0 / pend 0`、`变异覆盖：14/14`）。

### 8.3 变异循环被 `docker run -i` 抽干 stdin（本轮挖出的真缺陷）

`--mutate` 循环把 tsv 挂在 **stdin**（`while … read … < "$tsv"`），而 `MUT-5` 的 `cmd_id=static` 会**内层重跑本脚本**；一旦 `DATABASE_URL` 存在，内层查库走 `docker run --rm -i … psql`，`-i` 把继承来的 stdin（= 整个 tsv）抽干 → 外层 `read` 立刻 EOF → **后 5 条变异既无 ok、也无 FAIL、也无 pend，脚本仍 exit 0**。

**只有「带 DB」时才复现**——所以此前的 `ok 36`（无 DB 口径）把这一条完整掩盖到本轮。

| 条件 | 原始日志 | 汇总行 | MUT 行数 |
| --- | --- | --- | --- |
| 修复前（模拟 stdin 被抽干） | `mutations/backfill-stdin-drain-before-fix.log` | `ok 31 / FAIL 0 / pend 2` | **6/11**（`MUT-1/2/4/9/10/5` 之后全消失） |
| 修复后（同条件） | `mutations/backfill-stdin-drain-after-fix.log` | `ok 37 / FAIL 0 / pend 2` | **11/11** + `变异覆盖` 自检 ok |

**同一形态在两条独立记录里各现一次**：老运自己这一轮 + 老后带一次性集群实跑的 `ok 35 / FAIL 0 / pend 1`（只有 6 条 MUT 行）——`35 = 29 + 6` 的来路正是它，不是「少跑了几条」的笔误。而 `ok 41 - ok 29 = 11 变异 + 1 覆盖自检 = 12` 才是完整口径的差额。

**修法（三份脚本一致，已可复核）**：
- 循环改 `while IFS=$'\t' read -r -u 3 …; done 3<"$tsv"`（`verify-badge-backfill-progress.sh:452/474`），不再占 stdin；
- 内层命令统一 `run_mutation_cmd "$cmd_id" </dev/null`（同文件 `:465`）；
- **三份脚本各加一条覆盖自检**：`变异覆盖：$executed/$expected 条全部执行`，不等即 `FAIL 变异覆盖`（backfill `:483-487`、m3 `:571-575`、fact-record `:448-452`）——少跑从此不可能静默。

这正是本刀反复出现的同一类形态：**「跑过了」不等于「该跑的跑了」**。判据不变，只把「有没有跑完」也变成断言。
