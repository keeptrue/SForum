# Runbook: 徽章迁移的实机可逆验证（`scripts/verify-migration-reversible.sh`）

适用对象：改了 `apps/api/database/migrations/**` 的后端，以及要确认「这条迁移能不能回滚」
的审查者。一页看完即可上手。

## 这个脚本做什么

把 `202611100002_badges_and_user_activity.sql` 放到真实 PostgreSQL 上跑一遍完整可逆链路：

1. 起一个**一次性** PostgreSQL 集群（容器名 `sforum-migration-verify-<随机>`，随机宿主端口）；
2. 跑完整迁移链到最新（`go run ./cmd/migrate`，含 River）；
3. 用 goose 单步 down 回滚**最新已应用**的那一条迁移；
4. 断言对象全部消失：`user_activity` / `user_visits` / `badge_groups` / `badges` /
   `user_badges` 五张表、`users.top_badge_id` 列、三个稳定视图
   （`sforum_core_v1.forum_badges` / `forum_user_badges` / `forum_user_activity`）、
   `badge_granted` 的通知描述符与三条通道策略，以及 goose 版本回到 `202611100001`；
5. 再 up 一次，断言上面全部重建，且 12 个内置徽章、3 个分组
   （`activity` / `content` / `honor`）都在，goose 版本回到 `202611100002`。

结束后销毁集群。脚本幂等，可以反复跑。

## 怎么跑

```sh
./scripts/verify-migration-reversible.sh
```

通过时最后一行是：

```
全部通过：202611100002 可 down 回干净状态并重新 up 重建。
```

耗时参考：本机冷启动（拉镜像 + 160 条迁移 + River）约 10–20 秒，`go run` 编译另有几秒。

## 绝不碰开发库

- 脚本**不读**继承来的 `DATABASE_URL`，只用自己新建的集群；如果环境里有
  `DATABASE_URL`，会打印一行提示后忽略。
- 连上集群后第一件事是探测 `public.sforum_dev_guard` 哨兵；命中就 `exit 2` 立刻中止。
- 集群带 `sforum.disposable-migration-verify.v1` 的 database comment 和
  `com.sforum.migration-verify-owner` 容器标签，清理前会核对标签，避免误删别人的容器。

## 退出码

| 码 | 含义 | 先看哪里 |
| --- | --- | --- |
| 0 | 全通过 | — |
| 2 | 预检失败（缺 docker/go、集群起不来、命中开发库哨兵、端口绑定异常） | 上面最后一行 `[exit 2]` 说明；集群起不来时脚本已 dump `docker logs` |
| 3 | 目标迁移文件不存在 | 等 M0 把 `.sql` 落地，或改 `SFORUM_MIGRATION_TARGET_FILE` 指向别的文件做链路自检 |
| 4 | 完整迁移链 up 失败 | 上面 migrator 的 JSON 日志，通常是迁移 SQL 本身报错 |
| 5 | 单步 down 失败 | goose 报错；`latest applied migration is X, expected Y` 表示目标不是最新那条 |
| 6 | down 之后有对象残留 | 上面逐项 `FAIL` 列表 → Down 段漏删了这些对象 |
| 7 | 再次 up 失败 | Down 段删掉了 Up 段依赖的东西（例如把 Up 里 `CREATE SCHEMA`/角色/父表删了） |
| 8 | up 之后对象没重建全 | 上面逐项 `FAIL` 列表 → Up 段漏建，或内置数据条数变了 |

## 失败时保留现场

```sh
SFORUM_VERIFY_KEEP_CLUSTER=1 ./scripts/verify-migration-reversible.sh
```

失败后会打印容器名，进去手动看：

```sh
docker exec -it <容器名> psql -U sforum -d sforum_migration_verify
```

常用排查 SQL：

```sql
SELECT * FROM goose_db_version ORDER BY id DESC LIMIT 5;   -- 版本状态
\dt public.*badge*                                        -- 表还在不在
SELECT to_regclass('sforum_core_v1.forum_badges');        -- 视图还在不在
SELECT * FROM notification_type_policies WHERE type = 'badge_granted';
```

看完记得 `docker rm -fv <容器名>`。

## 可调参数（环境变量）

| 变量 | 默认 | 用途 |
| --- | --- | --- |
| `SFORUM_MIGRATION_TARGET_VERSION` | `202611100002` | 要验证的迁移版本（必须是最新已应用那条） |
| `SFORUM_MIGRATION_TARGET_FILE` | 按版本推出的 `.sql` 路径 | 迁移文件路径，用于文件名与版本号不一致的历史迁移 |
| `SFORUM_MIGRATION_PREVIOUS_VERSION` | `202611100001` | down 之后期望回退到的版本 |
| `SFORUM_EXPECTED_BUILTIN_BADGES` | `12` | 内置徽章条数断言 |
| `SFORUM_EXPECTED_BADGE_GROUPS` | `3` | 内置分组条数断言 |
| `SFORUM_TEST_POSTGRES_IMAGE` | `postgres:17-alpine` | 集群镜像，与 `test-api-isolated.sh` 共用同一变量 |
| `SFORUM_VERIFY_KEEP_CLUSTER` | `0` | 设为 `1` 保留集群排错 |

## 与其它脚本的关系

- 不动 `scripts/test-api-isolated.sh` 的既有语义：它服务 Go 测试分片，本脚本独立起集群，
  只借用了它的「一次性集群 + 标签所有权 + 真实认证就绪判定」这套做法。
- 不修改 `apps/api/database/migrations/` 下任何 `.sql`；它只读迁移文件并执行。
- 单步 down 需要一个能读到编译进二进制的迁移文件系统的程序；脚本在 `mktemp -d` 里生成
  一个最小 Go 程序（`go run`，复用 `apps/api` 的模块依赖），不用 goose CLI（CLI 会拉一堆
  无关数据库驱动）。
