# A-01 / B-04 真实库 DDL 实测（`badge_rule_backfill_runs`）

- 时间：2026-11-12 21:30–21:32 +0800
- 迁移：`apps/api/database/migrations/202611120003_badge_backfill_runs.sql`
- 目标：把 `reports/2026-11-12-badge-backfill-progress-acceptance.md` 里 **A-01 / B-04 的「真实库 DDL 实测 pend」** 抬成 ok。
- 结论：**Up 侧 15/15 PASS，Down 侧 4/4 + 版本回退 1/1 PASS，级联与 CHECK 行为探针 PASS。A-01、B-04 转 ok。**

## 1. 环境（一次性集群，绝不碰 dev 库）

`scripts/verify-migration-reversible.sh` 自建容器：

| 项 | 值 |
| --- | --- |
| 容器 | `sforum-migration-verify-ddad8f4e8d16487d298ec54a` |
| 镜像 | `postgres:17-alpine`（server_version **17.11**） |
| 端口 | `127.0.0.1:32780 -> 5432` |
| 库 | `sforum_migration_verify`（user `sforum`） |
| 开发库哨兵 `public.sforum_dev_guard` | **f（不存在）** —— 证明这不是 dev 库 |
| dev 库 `sforum-postgres:15432/sforum` | **本轮从未连接**（脚本显式忽略继承的 `DATABASE_URL`） |

保留集群的方式是 `SFORUM_VERIFY_KEEP_CLUSTER=1`，断言跑完后手动 `docker rm -fv`。

## 2. 完整迁移链 up → 单步 down → 再 up（脚本自带）

原始日志：
- `migration-reversible.log` —— **第一次**集群（容器 `sforum-migration-verify-ddad8f4e…`，端口 32780），全量 exit 0。
- `migration-reversible-2.log` —— **第二次**重建集群（收尾销毁后为「`--mutate` + DB + 会话」那一轮重新起的），同样全量 exit 0，末尾打印了新的容器名。两台的结论逐字相同。

```
== 完整迁移链 up
  ok   迁移链已到最新
== 生成 down 助手
  ok   已应用的最新迁移版本：202611120003
== 对 202611120003 单步 down
rolled back 202611120003 (202611120003_badge_backfill_runs.sql)
  ok   表 badge_rule_backfill_runs 已删除
  ok   索引 badge_rule_backfill_runs_badge_started_idx 已删除
  ok   索引 badge_rule_backfill_runs_running_idx 已删除
  ok   序列 badge_rule_backfill_runs_id_seq 已删除
  ok   goose 版本回退到 202611120002
== 再次 up
  ok   表 badge_rule_backfill_runs 存在
  ok   索引 badge_rule_backfill_runs_badge_started_idx 存在
  ok   索引 badge_rule_backfill_runs_running_idx 存在
  ok   外键指向 badges 且级联删除
  ok   status CHECK 只认三态
  ok   表 owner 与 public.badges 一致
  ok   goose 版本回到 202611120003
全部通过：202611120003 可 down 回干净状态并重新 up 重建。
```

**Down 侧（任务要求的 4+1）**：表、两个索引、序列 **都真的消失**（`to_regclass IS NULL`），goose `max(version_id)` 退回 **202611120002**。这是「物理回滚」，不是「文件里写了 DROP」。

## 3. Up 侧结构断言（本脚本补的那一半）

那个脚本只有笼统的「表/索引存在」，缺列集合、序列绑定、索引定义值级、FK 具体指向、owner 覆盖范围。补在 `ddl-up-assertions.sh`，结果 `ddl-up-assertions.log`：

```
INFO  container        sforum-migration-verify-ddad8f4e8d16487d298ec54a
INFO  psql_version     17.11
INFO  guard            f (must be f: dev guard absent)
PASS  A-01a  表 public.badge_rule_backfill_runs 存在                              t
PASS  A-01b  列集合 == 冻结 10 列   badge_id,batch,error,failed_cursor_user_id,finished_at,id,last_user_id,started_at,status,updated_at
PASS  A-01c  序列 badge_rule_backfill_runs_id_seq 存在                            t
PASS  A-01d  id 列的 serial 绑定指向该序列                                        public.badge_rule_backfill_runs_id_seq
PASS  A-01e  索引 _badge_started_idx 定义含 (badge_id, started_at DESC, id DESC)  yes
PASS  A-01f  索引 _running_idx 是 status=running 的局部索引                       yes
PASS  A-01g  索引总数 == 3（主键 + 两业务索引）                                   3
PASS  A-01h  FK 指向 public.badges（regclass 全等）                               yes
PASS  A-01i  FK 列 == badge_id                                                    badge_id
PASS  B-04   confdeltype == c（CASCADE）                                          c
PASS  A-01j  status CHECK 枚举集合恰为 completed,failed,running                   completed,failed,running
PASS  A-01k  status 默认值 == running                                             'running'::text
PASS  A-01l  表 owner == badges owner                                             t
PASS  A-01m  序列 owner == badges owner                                           t
PASS  A-01n  两索引 owner == badges owner                                         t
INFO  owner_actual  sforum_core_o_647c53928ce8ab21125c (badges owner: sforum_core_o_647c53928ce8ab21125c)
SUMMARY pass=15 fail=0
```

要点：

- **列集合是「等于」不是「包含」**：冻结 10 列一个不多一个不少（多出来的列没人写，是毒）。
- **索引总数 == 3**：主键 + 两条业务索引，防止有人偷偷加索引却没人维护。
- **`status` CHECK 用枚举值集合断言**，不是「包含三个词」——多一个值也红。
- **owner 覆盖表 + 序列 + 两索引**，四条全部等于 `public.badges` 的 owner（`sforum_core_o_647c53928ce8ab21125c`），对齐计划 §13#16 的 kernel 收敛前条件。

> 首轮有 4 条红是**我断言写错**（`bool::text` 得 `true` 不是 `t`、`regclass::text` 省了 `public.` 前缀、CHECK 定义里带引号），修正后 15/15。产品侧没有红。

## 4. B-04 行为探针：删 badge → run 行随 FK 消失

> 文件说明（三份别混看）：
> - `cascade-probe.log` —— **干净复跑**（探针 A 级联 + 探针 B CHECK，均含自清理计数）。
> - `cascade-probe-failed-attempt.log` —— **首轮失败留档**：探针 SQL 拼接把 `badges_rule_check`
>   的 psql 报错文本当成了变量值（`syntax error at or near ":"`），文件开头写明「产品侧无红」。
> - `migration-reversible.log` / `migration-reversible-2.log` —— 前后**两台**一次性集群的
>   完整链日志（第一次跑完按收尾要求销毁，为 `--mutate` 那一轮重建了第二次）。

只断言 `confdeltype='c'` 是「看目录」，这里再加一条真实删除：

```
== B-04 级联删除行为探针（一次性集群，非 dev 库）
### 探针 A：删 badge → run 行随 FK 消失
  建 badge id=16
  删前 run 行数: 1
  DELETE badge（无手动级联）: ok（无报错）
  删后 run 行数（期望 0）: 0
  全表孤儿 run 行（期望 0）: 0
```

**失败类反证**：若 FK 写成不带 `ON DELETE` 的引用，第 4 步会报 `violates foreign key constraint`，第 5 步会是 1。本集群走的是 CASCADE 分支。

## 5. A-01 status CHECK 行为探针

目录断言「恰好三值」还要证明它真的拦得住：

```
### 探针 B：status CHECK 恰为三值 + 大小写敏感
  合法值 running    -> accepted
  合法值 completed  -> accepted
  合法值 failed     -> accepted
  非法值 queued     -> ERROR: new row … violates check constraint "badge_rule_backfill_runs_status_check"
  非法值 RUNNING    -> ERROR: new row … violates check constraint "badge_rule_backfill_runs_status_check"
  该 badge 下 run 行数（期望 3）: 3
  删 badge 清理: ok
  收尾 run 行总数: 0 ；收尾 qa-* badge: 0
```

大小写敏感（`RUNNING` 被拒）与三态白名单同时成立，且清理后无残留。

## 6. 复现命令

```bash
export TMPDIR=/home/inkedus/.cache/dsh-tmp
cd /home/inkedus/Code/github/SForum

# 1) 一次性集群上跑完整链 up → down → up，并保留现场
SFORUM_VERIFY_KEEP_CLUSTER=1 SFORUM_MIGRATION_TARGET_VERSION=202611120003 \
  bash scripts/verify-migration-reversible.sh

# 2) 对保留下来的容器跑 Up 侧断言
cd knowledge/reports/qa-2026-11-12-badge-pend-conversion/ddl
CID=<日志末尾打印的容器名> bash ddl-up-assertions.sh

# 3) 收工销毁
docker rm -fv <容器名>
```

## 7. 残留

- 本轮只覆盖 `202611120003` 一条。迁移链里更早的徽章迁移（`…0002` 的 `badge_rules` 回推、`…0001` 的 `badge_tiers` 拆外键）由 `verify-migration-reversible.sh` 的同文件分支覆盖，不在本刀范围。
- 脚本 `ddl-up-assertions.sh` 依赖容器内 `psql`（宿主无 psql），换机器需保持 `postgres:17-alpine` 可用。
