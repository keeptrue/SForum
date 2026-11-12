# F-01 / GAP-C 实测：`GET /admin/badges/conditions` 的 `factCandidates[]`（HTTP 值级）

- 时间：2026-11-12 21:33–21:38 +0800
- 结论：**F-01 / GAP-C 转 ok** —— 有候选侧 `ok 24 / fail 0`，空候选侧 `ok 8 / fail 0`，两次都是真实 HTTP 响应 + 同一会话，未用任何 DTO 单测替代。
- 产物：`conditions-enabled.json`（原始响应）、`conditions-disabled.json`、`f01-run.log`、`empty-candidates.log`、`append-only-evidence.log`、两个断言脚本、两个驱动脚本。

## 1. 环境与前置

| 项 | 值 |
| --- | --- |
| API | `http://127.0.0.1:8080/api/v1`，**21:29:56 新构建**（pid 219173，晚于源码最新 mtime 21:09:08） |
| 库 | dev 库 `sforum-postgres:15432/sforum`（`public.sforum_dev_guard` 哨兵在位） |
| 会话 | 一次性账号 + 直改库授 `super_admin`（Lead 批准的方案 B），**全程未碰 `zhuchunshu`(id=1)** |
| 夹具 | `extensions/fixtures/plugins/sforum-query-surface-reference`（**连字符目录名**，`sforum.extension.json` 在 zip 根） |

## 2. 为什么不是「re-enable 墓碑」

Lead 最初要求走 `sforum.query-surface-reference` 现有 disabled 墓碑的 re-enable 路径。实测不可行，原因是代码级事实：

```
extensionSelectSQL()（postgres_store.go:859）
  FROM extensions
  JOIN extension_versions ON extension_versions.id = extensions.active_version_id
```

m34 卸载后留下的墓碑是 `status=disabled` 且 **`active_version_id IS NULL`**。这条 `JOIN` 直接把它挡在结果集外，于是所有带 `:id` 的入口（`enable` / `disable` / `DELETE`）都返回 **404 `extension.not_found`**——不是权限问题，是墓碑没有「运行中制品」可指向。这与计划 §15.4 记的「卸载后保留不可变身份」是同一件事。

**正路是 install-or-upgrade**：`POST /admin/extensions`（同一 `Service.InstallOrUpgradeArchive`）。实测 **201**，把该扩展重新装成 `status=disabled` + `active_version_id` 指回既有 `extension_versions` 行（id 33810），随后 `enable` 直接 **200**（制品此前已被精确信任过，无需再走 challenge）。

## 3. 有候选一侧（值级）

驱动 `f01-factcandidates.sh`，原始日志 `f01-run.log`；断言 `assert-factcandidates.mjs`。

```
== 2 安装并启用 sforum.query-surface-reference
  POST /admin/extensions -> 201 …"status":"disabled"…
  ok   enable 200（无需信任挑战）
  ok   库内 status=enabled
  ok   等过 factCandidates 的 5s 快照 TTL
== 3 GET /admin/badges/conditions
  ok   响应已存 conditions-enabled.json（5514 字节）
== 4 factCandidates 值级断言
  ok   items 是数组
  ok   factCandidates 是数组（不是 null/缺席）
  ok   items[].键集合 == 冻结 10 键（compare,describeEn,describeZh,key,labelEn,labelZh,params,unitEn,unitZh,valueKind）
  ok   items 里含 fact.count
  ok   factCandidates 条数 2 >= 期望 2
  ok   候选 key 非空（sforum.query-surface-reference.onboarding.tasks）
  ok   候选 …onboarding.tasks label.zh 长度=7
  ok   候选 …onboarding.tasks label.en 长度=26
  ok   variant …onboarding.tasks/tutorial value 非空
  ok   variant …onboarding.tasks/tutorial label.zh 长度=4 / label.en 长度=14
  ok   variant …onboarding.tasks/profile value 非空
  ok   variant …onboarding.tasks/profile label.zh 长度=6 / label.en 长度=13
  ok   候选 …verified_reports label.zh 长度=9 / label.en 长度=26
  ok   …onboarding.tasks variants[].value 全部非空（tutorial,profile）
  ok   …verified_reports 无 variants（按 spec 合法）
  ok   所有候选双语 label 非空（无假候选）
  候选总览: onboarding.tasks(zh=7,en=26,variants=2) | verified_reports(zh=9,en=26,variants=0)
  RESULT ok=24 fail=0
```

对应 Lead 给的判据逐条落地：`label.zh`/`label.en` 长度 > 0（7/26、9/26）、`variants[].value` 非空且双语 label 非空（tutorial/profile）、`items` 键集合仍等于冻结 10 键且含 `fact.count`。

> 第一轮的 `items 不是数组` 是**我漏了 `{code,message,data}` 解包**；修正后 24 全绿。产品侧没有红。

## 4. 空候选一侧（`[]` 不是 `null`）

驱动 `empty-candidates.sh`，原始日志 `empty-candidates.log`，响应 `conditions-disabled.json`（4364 字节）。

顺序铁律：**先验空候选，最后删账号**（上一版把删号写在前面，导致 401 `auth.required`，判据根本没被验到——这条已改正）。另外必须先等 **7s**：`badgesFactCatalogTTL = 5 * time.Second`，disable 后立刻请求会读到旧快照。

```
== 0 前置  库内状态: disabled|active=NULL   → ok 墓碑态
           等 7s 让候选快照 TTL（5s）过期
== 2 GET /admin/badges/conditions
  ok   factCandidates 是数组
  ok   factCandidates 不是 null（未初始化切片会序列化成 null）
  ok   factCandidates == []（length 0）
  ok   items 是数组
  ok   items[].key 不含 fact.count（无候选 → 该条件退出展示面）
  ok   items 条数 == 12（实测 12）
  ok   每一项键集合 ∈ {9 键, 9 键 + unit}（6 项带 unit）
  ok   所有项都落在两种合法形态之一
  RESULT fail=0
== 4 最后删账号  复核 users/user_roles/user_sessions/user_credentials = 0000
```

两条值得记下的实现事实：

1. **`fact.count` 会随候选消失**。`AdminService.BadgeConditions` 返回 `conditions.Core().PublishableDefinitions(candidates)`，而它是**展示面**（源码注释：objectRef 没有候选的条件不出现）。所以「无候选 → items 12 条且无 `fact.count`」是设计行为，不是丢键。
2. **`unitZh/unitEn` 是 `omitempty`**。`conditionDTO` 里这两个字段只在有计量单位时出现，所以 items 的合法键集合只有「9 键」和「9 键 + unit」两种，不能一律按 10 键断言。首轮 `有 6 项键漂移` 是我判据写错，不是产品漂移。

## 5. 收尾：扩展与账号

| 项 | 测试前 | 测试后 | 说明 |
| --- | --- | --- | --- |
| `extensions` 行 | `disabled \| active=NULL \| staged=NULL` | **同左** | 产品路径的 uninstall 不清 `active_version_id` 投影，脚本定向还原（`UPDATE … SET active_version_id=NULL`，**不删墓碑行**） |
| `extension_versions` | 1 | **1** | 同一行（id 33810） |
| `plugin_runtime_publication_members` | 1（rev 1277，m34 基线） | **4**（1277/1285/1287/1289） | **append-only，删不掉，属刻意保留项**（见下） |
| 一次性账号 | — | **删除并复核 0/0/0/0** | 每轮都是新账号，跑完即删 |

### 5.1 `plugin_runtime_publication_members` 为什么清不回 1

这不是「SQL 写错」，是**设计上禁止**。`202607160027_plugin_runtime_publications.sql` 装了三条拒绝触发器，原始定义与拒绝输出见 `append-only-evidence.log`：

```
plugin_runtime_publication_member_immutable   BEFORE DELETE OR UPDATE  → reject_plugin_runtime_desired_mutation()
plugin_runtime_publication_member_no_truncate BEFORE TRUNCATE          → reject_plugin_runtime_desired_mutation()
plugin_runtime_publication_member_full_set    AFTER INSERT (DEFERRED)  → validate_plugin_runtime_desired_full_set()
函数体：BEGIN RAISE EXCEPTION 'plugin runtime desired publications are append-only'; END;
```

实测取证（`DELETE` 失败不改变数据）：

```
DELETE … WHERE extension_id='sforum.query-surface-reference' AND publication_revision=1289;
ERROR:  plugin runtime desired publications are append-only
CONTEXT:  PL/pgSQL function reject_plugin_runtime_desired_mutation() line 3 at RAISE
删除尝试后 count(*) = 4（未变）；该扩展 1289 行仍在 = 1
UPDATE  → 同样 RAISE
TRUNCATE → ERROR: cannot truncate a table referenced in a foreign key constraint
           （plugin_runtime_applied_members 引用它）
```

保留行的精确身份（`publication_revision` 为唯一身份，四行是**同一制品**）：

| publication_revision | extension_id | extension_version_id | version | package_digest |
| --- | --- | --- | --- | --- |
| 1277 | sforum.query-surface-reference | 33810 | 1.0.0 | `6d89402f…c83bc`（m34 基线） |
| 1285 | 同上 | 33810 | 1.0.0 | 同上（本轮第 1 次 enable） |
| 1287 | 同上 | 33810 | 1.0.0 | 同上（本轮第 2 次 enable） |
| 1289 | 同上 | 33810 | 1.0.0 | 同上（本轮第 3 次 enable） |

`DISABLE TRIGGER` 是明令禁止的（为让证据好看而破坏完整性）。正确处置就是**如实记录**。

## 6. 复现

```bash
export TMPDIR=/home/inkedus/.cache/dsh-tmp
cd knowledge/reports/qa-2026-11-12-badge-pend-conversion/factcandidates

# 有候选：安装→启用→取响应→值级断言→disable/uninstall→还原→删号
bash f01-factcandidates.sh          # 退出码 0 = 全绿

# 空候选：墓碑态 + 等 7s TTL → 取响应 → 断言 [] / 12 条 / 无 fact.count → 删号
bash empty-candidates.sh            # 退出码 0 = 全绿
```

两个脚本都只写本目录，都自带账号清理与计数复核；`f01-factcandidates.sh` 退出码非 0 时仍会走完清理段。

## 7. 残留与已知偏差

- **`plugin_runtime_publication_members` 3 行增量不可清**（见 §5.1），Lead 已定为其「刻意保留项」。
- `f01-run.log` 中「步骤 6 空候选」段有 1 条 FAIL（候选仍非空）：那是**同一进程内 disable 后未等 5s TTL**，属脚本缺陷；该判据已由 `empty-candidates.sh` 单独跑通（§4），两份日志都留在目录里，不裁剪。
- 本轮没有改任何 `apps/api` 源码，也没有跑 `go build`/`go test`/`go run`（老运变异整轮期间）。

## 8. 最终口径（2026-11-12 22:0x，脚本修复后的实跑原文）

三个时点的日志都留在本目录，**不要混引**：

| 文件 | 时点 | 内容 | 为什么不是最终口径 |
| --- | --- | --- | --- |
| `m3-mutate-envelope-fixed.log` | 21:55 | m3 `ok 47 / FAIL 0 / pend 0`，F-01 `facts=2` | 信封解包已修，但脚本还没有「变异覆盖自检」 |
| `m3-mutate-final.log` | 22:11 | m3 `ok 48 / FAIL 0 / pend 0` + `ok 变异覆盖：12/12 条全部执行` | **最终口径** |
| `backfill-db-session.log` | 21:49 | backfill（DB + 会话，无 `--mutate`）`ok 29 / FAIL 0 / pend 2` | 未跑变异，`MUT-*` 落 pend |
| `backfill-db-session-mutate-skip-bug.log` | 21:58 | backfill（DB + 会话 + `--mutate`）`ok 35 / FAIL 0 / pend 1` | **变异被静默跳过的现场证据**：11 条只跑了 6 条（`MUT-1/2/4/9/10/5`） |
| `backfill-db-session-mutate-final.log` | 22:1x | backfill（DB + 会话 + `--mutate`）`ok 41 / FAIL 0 / pend 1` + `ok 变异覆盖：11/11 条全部执行` | **最终口径** |

### 8.1 两个被诊断出来的脚本缺陷（都不是产品缺陷）

1. **信封层级**：`verify-badge-m3-manifest.sh` 的 F-01 段直接读 `payload.factCandidates`，而端点走 `apphttp.OK` 信封 `{code,message,data}`，真实数据在 `payload.data` → 恒 undefined，先报「不是数组」再报「为空」。修法 `const root = payload.data ?? payload`（老运已改）。
2. **stdin 抽干**：`verify-badge-backfill-progress.sh` 的 `while … read … < "$tsv"` 让 tsv 挂在 stdin，而 `MUT-5` 的 `cmd_id=static` 会内层再跑本脚本；内层因为 `DATABASE_URL` 已 export 而走 `docker run --rm -i … psql`，`docker run -i` 把继承来的 stdin（= tsv）整份抽干 → 外层 `read` 随即 EOF，**后 5 条变异静默消失**且汇总里既无 ok 也无 FAIL。修法：`read -r -u 3 … done 3<"$tsv"` + `run_mutation_cmd … </dev/null` + 覆盖自检（老运已改，`executed != expected` 直接 FAIL）。

不设 `DATABASE_URL` 时缺陷 2 不触发——这正是它长期没被发现的原因。

### 8.2 最终实跑原文

```
# m3-mutate-final.log
== 汇总
  ok 48 / FAIL 0 / pend 0
  ok    F-01 factCandidates 值级（items=13 fullKeys=7 unitless=6 facts=2 clean）
  ok    变异覆盖：12/12 条全部执行
EXIT=0

# backfill-db-session-mutate-final.log
== 汇总
  ok 41 / FAIL 0 / pend 1
  pend 清单：
    - G-05 390 视觉 QA — 需浏览器实测（tests/browser-admin-390x844.mjs），脚本不代跑
  ok    变异覆盖：11/11 条全部执行
EXIT=0
```
