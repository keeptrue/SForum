# 徽章三刀 `--mutate` 原始日志归档

本目录是**变异自检口径的原文归档**：每份 `.log` 都是脚本 stdout+stderr 的逐字复制（未改一字），
`ok / FAIL / pend` 汇总行可在文件末尾 `== 汇总` 段直接核对。

> 不带 `--mutate` 的基线口径（含带真库/带会话的取值级证据）见 `../factcandidates/backfill-db-session.log`。
> 两套数字**不可互相替代**：`--mutate` 口径会额外跑「变异段」并多出 `MUT-*` 与「变异覆盖」两类行，
> 而非变异段的 pend 数量取决于是否提供 `DATABASE_URL` / `SFORUM_SESSION_COOKIE`。

## 运行条件（所有日志共用，逐份再单独标注）

- 仓库：`/home/inkedus/Code/github/SForum`，HEAD = `efef52ddc`（三份脚本跑完后 lead 未再提交）
- `unset DATABASE_URL`：所以 DDL / 权限级断言记 **pend**（不是 FAIL）
- 无 `SFORUM_SESSION_COOKIE`：所以 HTTP 值级断言记 **pend**（不是 FAIL）
- `export TMPDIR=/home/inkedus/.cache/dsh-tmp`（`/tmp` 是 12G tmpfs，避免写满）
- 串行执行，整轮期间无其他人在改 `apps/api`、无其他人的 `go build/test/run`
- 唯一例外：`f01-stub-checks.log` 用桩会话 `SFORUM_SESSION_COOKIE=stub-cookie` 指向本地桩服务器，不写真值；
  `backfill-stdin-drain-*.log` 用 `SFORUM_ACCEPT_SQL_CMD='bash -c "cat >/dev/null"'` + 假 `DATABASE_URL` 复现 stdin 抽干

## 表格：修复后（当前口径）

| 日志 | 命令 | 条件（DB / 会话） | 原始 exit code | 脚本原文汇总行 | 变异条数 |
|---|---|---|---|---|---|
| `backfill-full-mutate-after-coverage-fix.log` | `./scripts/verify-badge-backfill-progress.sh --mutate` | 无 DATABASE_URL / 无会话 | **0** | `ok 37 / FAIL 0 / pend 2` | 11/11 + 覆盖自检 ok |
| `m3-full-mutate-after-coverage-fix.log` | `./scripts/verify-badge-m3-manifest.sh --mutate` | 脚本不读 DB / 无会话 | **0** | `ok 47 / FAIL 0 / pend 1` | 12/12 + 覆盖自检 ok |
| `fact-record-full-mutate-after-coverage-fix.log` | `./scripts/verify-badge-fact-record.sh --mutate` | 无 DATABASE_URL / 无会话 | **0** | `ok 34 / FAIL 0 / pend 0` | 14/14 + 覆盖自检 ok |

`ok 37 / 47 / 34` = 修复前的 `36 / 46 / 33` + 各 1 条新的「变异覆盖」自检行（判据未变，只多一条断言）。

## 表格：stdin 抽干缺陷的对照（同一脚本、同一环境，只差修复）

| 日志 | 命令 | 条件 | 原始 exit code | 汇总行 | MUT 行数 |
|---|---|---|---|---|---|
| `backfill-stdin-drain-before-fix.log` | `DATABASE_URL=… SFORUM_ACCEPT_SQL_CMD='bash -c "cat >/dev/null"' ./scripts/verify-badge-backfill-progress.sh --mutate` | 模拟「内层 `docker run -i` 抽干 stdin」 | **0** | `ok 31 / FAIL 0 / pend 2` | **6/11**（MUT-1/2/4/9/10/5 之后全消失） |
| `backfill-stdin-drain-after-fix.log` | 同上 | 同上 | **0** | `ok 37 / FAIL 0 / pend 2` | **11/11** + 覆盖自检 ok |

缺陷机制：变异循环把 tsv 挂在 **stdin**（`while … read … < "$tsv"`），而 `MUT-5` 的 `cmd_id=static`
会内层重跑本脚本；一旦 `DATABASE_URL` 存在，内层查库走 `docker run --rm -i … psql`，**`-i` 把继承来的
stdin（= 整个 tsv）抽干** → 外层 `read` 立刻 EOF → 后 5 条既无 ok、也无 FAIL、也无 pend，脚本仍 exit 0。
老后带一次性集群 + 会话实跑时命中同一形态：`ok 35 / FAIL 0 / pend 1`，只有 6 条 MUT 行。

修复（三份脚本一致）：①循环改 `read -r -u 3 … done 3<"$tsv"`，不再占 stdin；②内层命令统一
`run_mutation_cmd "$cmd_id" </dev/null`；③新增覆盖自检，条数不符即 `FAIL 变异覆盖`（少跑=假绿，从此不可能静默）。

## 表格：历史轮次（修复前，保留作对照）

| 日志 | 命令 | 条件（DB / 会话） | 原始 exit code | 脚本原文汇总行 | 变异条数 |
|---|---|---|---|---|---|
| `backfill-full-mutate.log` | `./scripts/verify-badge-backfill-progress.sh --mutate` | 无 DATABASE_URL / 无会话 | **0** | `ok 36 / FAIL 0 / pend 2` | 11 条全红并恢复（无覆盖自检） |
| `m3-full-mutate-before-fix.log` | `./scripts/verify-badge-m3-manifest.sh --mutate`（B 段期望修正**前**） | 脚本不读 DB / 无会话 | **1** | `ok 44 / FAIL 2 / pend 1` | 12 条全红并恢复 |
| `m3-full-mutate-after-fix.log` | 同上（B 段期望 `fail fail` + 防静默 + F-01 空集合/逐项键集合修好**后**；信封修复**前**） | 脚本不读 DB / 无会话 | **0** | `ok 46 / FAIL 0 / pend 1` | 12 条全红并恢复 |
| `fact-record-full-mutate.log` | `./scripts/verify-badge-fact-record.sh --mutate` | 无 DATABASE_URL / 无会话 | **0** | `ok 33 / FAIL 0 / pend 0` | 14 条全红并恢复 |
| `smoke-backfill-MUT-1.log` | `./scripts/verify-badge-backfill-progress.sh --mutate MUT-1` | 无 DATABASE_URL / 无会话 | **0** | `ok 26 / FAIL 0 / pend 2` | 1 条（单条冒烟） |
| `smoke-fact-record-M1.log` | `./scripts/verify-badge-fact-record.sh --mutate M1` | 无 DATABASE_URL / 无会话 | **0** | `ok 20 / FAIL 0 / pend 0` | 1 条（单条冒烟） |
| `m3-single-M3M-01.log` | `./scripts/verify-badge-m3-manifest.sh --mutate M3M-01`（`M3M-*` 前缀修复后） | 脚本不读 DB / 无会话 | **0** | `ok 35 / FAIL 0 / pend 1` | 1 条（证明单条可点） |
| `m3-single-MUT-999.log` | `./scripts/verify-badge-m3-manifest.sh --mutate MUT-999`（防静默） | 脚本不读 DB / 无会话 | **1** | `ok 34 / FAIL 1 / pend 1` | **0 条** → 显式 `FAIL MUT-999 不存在` |
| `f01-stub-checks.log` | `SFORUM_SESSION_COOKIE=stub-cookie SFORUM_ACCEPT_API_URL=http://127.0.0.1:18099 ./scripts/verify-badge-m3-manifest.sh --no-tests` × 4 发 | 无 DATABASE_URL / 桩会话 + 本地桩 | 1 / 0 / 1 / 0（逐发） | 逐发 `F-01` 行 | 仅 F-01 段 |

> **核对说明（2026-11-12 22:3x，逐份对 `== 汇总` 段核过）**：本目录每份 `.log` 的「汇总行」与表格逐字一致；
> 「原始 exit code」在多数日志里**没有内嵌 `EXIT=` 标记**，故按「`FAIL 0` → 0、`FAIL > 0` → 1」推定；
> 只有 `f01-stub-checks.log`（`[EXIT=1]/[EXIT=0]` 四发）与 `../factcandidates/backfill-db-session-mutate-final.log`
> （`EXIT=0`）是日志内嵌值。MUT 行数用 `grep -c "变异后变红"` 复核：`31` → 6 行、`37/36/41` → 11 行。

## 数字为什么不是同一个（口径表）

| 口径 | 命令 | backfill 汇总行 | 说明 |
|---|---|---|---|
| full `--mutate`（当前） | 加 `--mutate`，无 DB/会话 | `ok 37 / FAIL 0 / pend 2` | 含覆盖自检；DB/会话缺失把 2 条记 pend |
| full `--mutate`（带一次性集群 + 会话） | 同上，有 DB + cookie | **实测 `ok 41 / FAIL 0 / pend 1`**（= `29 + 11 变异 + 1 覆盖自检`，`EXIT=0`；原文 `../factcandidates/backfill-db-session-mutate-final.log`） | **pend 1 = `G-05`**（脚本只打「需浏览器实测」提示、不代跑浏览器），`41 + 1 = 42` 自洽；G-05 由 390 实测在**矩阵层**补实，与脚本口径不互相覆盖 |
| `--no-tests` | 加 `--no-tests` | **本列不填 backfill 的值（未归档）**；`ok 29 / FAIL 0 / pend 4` 是 **m3** 的口径（见 `reports/2026-11-12-badge-m3-manifest-acceptance.md` §6），此前串档写在本列、已纠正 | 非变异段降级为 pend（`D 隔离真库` / `E-03` / `F-01` / `MUT-*`）；**两脚本 `--no-tests` 数字不可互换** |
| 带真库+会话的基线 | 不带 `--mutate` | 见 `../factcandidates/backfill-db-session.log` | 与变异口径不可互换 |

所以 `25 / 29 / 31 / 35 / 36 / 37 / 41` 是**同一脚本在不同条件下**的数字；`6` 与 `11` 是**同一条**「跑了几条变异」，不是汇总数。逐条对应：
- `25` = 无 DB/无会话/无 `--mutate`（基线）；`29` = 带 DB+会话/无 `--mutate`；
- `41` = 带 DB+会话 + `--mutate`（**最终口径**，pend 1 = G-05）；`37` = 无 DB/会话 + `--mutate`；
- `36` = 无覆盖自检的旧口径；`35` = 老后带集群实跑时命中 stdin 抽干（只有 6 条 MUT 行）；
- `31` = 同缺陷的模拟复现（6 条 MUT 行）。
**不可相加、不可互换。**

## `f01-stub-checks.log` 的四发判据

桩文件 `f01-stub.mjs`（同目录）在本机 `127.0.0.1:18099` 回放固定 JSON，让脚本真实走 `curl → node` 链路：

| 载荷 | 期望 | 实际 |
|---|---|---|
| 无信封 + `factCandidates: []` | FAIL（空集合判据） | `FAIL …（bad=factCandidates 为空：F-01 判据要求至少 1 条候选…）` |
| **真实信封** `{"code":200,"message":"OK","data":{…}}` | PASS | `ok …（items=2 fullKeys=1 unitless=1 facts=1 clean）` |
| 无信封 + 键漂移 item（`a,b,key`） | FAIL（逐项键集合） | `FAIL …（bad=items 键漂移: a,b,key）` |
| 无信封 flat（兼容形态） | PASS（不许误红） | `ok …（items=2 fullKeys=1 unitless=1 facts=1 clean）` |

`items` 的合法键集合只有两种：10 键全集，或去掉 `omitempty` 的 `unitEn/unitZh` 的 8 键。

## 复现方式

```bash
cd /home/inkedus/Code/github/SForum
export TMPDIR=/home/inkedus/.cache/dsh-tmp
unset DATABASE_URL
./scripts/verify-badge-backfill-progress.sh --mutate
./scripts/verify-badge-m3-manifest.sh --mutate
./scripts/verify-badge-fact-record.sh --mutate

# stdin 抽干缺陷的复现（不需要真库：用会抽干 stdin 的 SQL 桩）
DATABASE_URL='postgres://stub@127.0.0.1:1/stub' \
  SFORUM_ACCEPT_SQL_CMD='bash -c "cat >/dev/null"' \
  ./scripts/verify-badge-backfill-progress.sh --mutate   # 修复后应 11/11 + 覆盖自检 ok

# 桩复现：另开 shell 跑同目录 f01-stub.mjs（F01_PAYLOAD=<载荷> F01_PORT=18099 node f01-stub.mjs）
```

变异段会**原地改生产文件再恢复**：跑之前确认无人在改同一批文件，跑完 `git diff --stat -- apps/api apps/web` 应为空。
