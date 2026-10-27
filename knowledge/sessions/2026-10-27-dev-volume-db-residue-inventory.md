# 2026-10-27 dev 环境卷归属与库残留盘点（P2，只读）

任务：`task-2`。执行人：ops（老运）。**本轮全部只读**：未删除任何卷 / schema / 扩展，未修改
`compose.yaml`、`compose.dev.yaml`，未执行 `docker volume rm|prune`、`DROP`、`TRUNCATE`。
`sforum.marketplace` 的 FK 问题只给方案。

盘点时间窗：2026-10-27 07:56–08:20（Asia/Shanghai）。
DB 连接方式：`set -a; . ./.env; set +a` 后
`docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" sforum-postgres psql -U sforum -d sforum`
（密码取自 `.env` 的 `POSTGRES_PASSWORD`，长度 6；`DATABASE_URL` 里硬编码的 `sforum:sforum`
与 `.env` 一致）。

---

## 0. 结论摘要

1. **两套数据卷互不相干**：`sforum-postgres-pgdata`（2.2G，活库，由 compose project
   `sforum-postgres` 的手动容器持有）与 `sforum_postgres_data`（46M，孤儿，`sforum` project
   的遗留）是**两个不同的 PG 数据目录**。`docker compose -f compose.yaml -f compose.dev.yaml config`
   当前解析出的 `postgres_data` 指向**孤儿卷**，所以任何 `dev.sh` 启动都在用错的库。
2. **dev.sh 现在根本起不来 postgres**：`compose.dev.yaml` 把 postgres 发布到
   `127.0.0.1:15432`，而该端口已被 `sforum-postgres` 容器占用。只改卷名不够，必须同时解决端口/容器归属。
3. **49 个 schema**：可删 **39** 个（36 个 `lifecycle_close_*` + 2 个 `bps_*` + 无前缀遗留重复的
   `sforum_gamification`，合计约 12 MB），必留 **6** 个（`public`、`sforum_core_v1`、
   `sforum_host_extensions`、`ops_bk_20261027`、`sforum_ext_*gamification`、`sforum_ext_*oauth2_provider`），
   待定/待确认 **4** 个（`sforum_ext_*qa_store_proof`、`sforum_ext_*marketplace`、
   `sforum_ext_*commerce_workflow`、`sforum_ext_*custom_content`）。
4. **启动日志每轮 4 条 uninstall cleanup 告警，但只有 3 个根因**（908/909 是同因的两条），
   不能用一个办法收口：
   - 703 `sforum.web-search`：**过期墓碑**（active version 已被重装换成 23106，墓碑仍钉 14155）→ 按设计永不收敛。
   - 908/909 `sforum.marketplace`：**FK 23503**，`extension_version_gc_removals.extension_id`
     是 `ON DELETE RESTRICT`，而清理事务删 `extensions` 前不删该表 → 任何被 GC 过版本的扩展都卸载不了。
   - 1154 `qa.store.proof`：`requested_by_user_id IS NULL`（dev-auto 无凭据通道创建），
     而 provider 选择失效化强制要求 actor>0 且 audit>0 → **连免键收尾端点也救不了**。
5. **额外发现（比 schema 更占地方）**：`storage/extensions` 已膨胀到 **22 GB**，其中 38 个被打断的
   `.snapshot-*` 暂存目录占 **1.6 GB**；`sforum.marketplace` 的 **active 版本目录已从磁盘丢失**
   （DB 仍指 `active_version_id=4283`）；数据库里另有 23 个 `sforum_core_o_*` 角色 + 5 组
   `p4.e2e.preserve.*` 孤儿角色。

---

## 1. 卷 / 容器 / compose project 归属

### 1.1 三个 compose project

```
$ docker compose ls -a
NAME                STATUS        CONFIG FILES
sforum              exited(1)     /home/inkedus/Code/github/SForum/compose.yaml,/home/inkedus/Code/github/SForum/compose.dev.yaml
sforum-postgres     running(1)    /home/inkedus/server/docker/compose/postgres/docker-compose.yml
sforum-redis        running(1)    /home/inkedus/server/docker/compose/redis/docker-compose.yml
```

### 1.2 容器

```
$ docker ps -a --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}\t{{.Label "com.docker.compose.project"}}'
sforum-postgres   postgres:17-alpine       Up 33 hours (healthy)   127.0.0.1:15432->5432/tcp   sforum-postgres
sforum-redis      redis:7-alpine           Up 33 hours (healthy)   127.0.0.1:16379->6379/tcp   sforum-redis
sforum-mailpit-1  axllent/mailpit:v1.30.7  Exited (0) 4 days ago                              sforum
recursing_hypatia postgres:17-alpine       Exited (255) 11 days ago 127.0.0.1:32774->5432/tcp  (无 project)
interesting_brown cloudflare/cloudflared:latest Exited (0) 2 days ago                          (无 project)
```

- `sforum-postgres` 的 compose 定义在 `/home/inkedus/server/docker/compose/postgres/docker-compose.yml`，
  `sforum-redis` 在 `.../compose/redis/docker-compose.yml` —— **都不在本仓库**，本仓库 compose 只是它们的
  "同端口竞争者"。
- 本仓库 `sforum` project 只剩一个退出的 mailpit；`postgres`/`redis`/`api`/`web` 容器都不存在。

### 1.3 卷对应表

| 卷名 | 持有容器 | compose project | 挂载点 | 大小 | 最后写入 | 是否含数据 |
|---|---|---|---|---|---|---|
| `sforum-postgres-pgdata` | `sforum-postgres`（Up，healthy） | `sforum-postgres` | `/var/lib/docker/volumes/sforum-postgres-pgdata/_data` → 容器 `/var/lib/postgresql/data` | 2.2 G | **活库**：`pg_wal` mtime 2026-10-27 07:50:25，`base` 07:36:29 | 是，PG 17 数据目录，库 `sforum` 1085 MB |
| `sforum-redis-data` | `sforum-redis`（Up，healthy） | `sforum-redis` | `.../sforum-redis-data/_data` → 容器 `/data` | 50 M | 2026-10-27 06:53:21 | 是，AOF 持久化中 |
| `sforum_postgres_data` | **无**（LINKS=0） | `sforum`（本仓库） | `.../sforum_postgres_data/_data` | 46 M | 2026-10-18 09:49（最新文件 `pg_wal/000000010000000000000001`、`global/pg_internal.init`） | 是，PG 17 数据目录（`PG_VERSION=17`，`base/` 下 4 个库目录：1/4/5/16384） |
| `sforum_redis_data` | **无**（LINKS=0） | `sforum`（本仓库） | `.../sforum_redis_data/_data` | 20 K | 2026-10-18 08:12:14 | 基本空（仅目录骨架） |

卷标签（`docker volume inspect`）证明归属：

```
sforum-postgres-pgdata  → com.docker.compose.project=sforum-postgres, volume=pgdata,   CreatedAt 2026-10-04T01:46:59
sforum-redis-data       → com.docker.compose.project=sforum-redis,    volume=redisdata,CreatedAt 2026-10-04T01:47:13
sforum_postgres_data    → com.docker.compose.project=sforum,          volume=postgres_data, CreatedAt 2026-10-18T08:11:59
sforum_redis_data       → com.docker.compose.project=sforum,          volume=redis_data,    CreatedAt 2026-10-18T08:11:59
```

时间线一致：10-04 手动起了 `sforum-postgres`/`sforum-redis`（各自独立 project）；10-18 08:11 有人跑过
`./scripts/dev.sh`，`sforum` project 另建了 `sforum_postgres_data`/`sforum_redis_data`，09:49 之后停用，
从此两套并存。

宿主目录 mtime 需要 sudo（`/var/lib/docker` 属 root）：

```
$ sudo -n stat -c '%y  %n' /var/lib/docker/volumes/*/_data
2026-10-04 01:46:59  sforum-postgres-pgdata/_data      # 目录 mtime 不随文件写变化，实际活库（见 pg_stat_file）
2026-10-27 06:53:21  sforum-redis-data/_data
2026-10-18 09:48:57  sforum_postgres_data/_data
2026-10-18 08:12:14  sforum_redis_data/_data

$ sudo -n du -sh /var/lib/docker/volumes/*/_data
2.2G  sforum-postgres-pgdata/_data
50M   sforum-redis-data/_data
46M   sforum_postgres_data/_data
20K   sforum_redis_data/_data
```

活库"最后写入"用 `pg_stat_file` 从库内取证（比宿主 mtime 准）：

```sql
select 'base',(pg_stat_file('base')).modification
union all select 'pg_wal',(pg_stat_file('pg_wal')).modification
union all select 'postgresql.auto.conf',(pg_stat_file('postgresql.auto.conf')).modification;
-- base 2026-10-27 07:36:29+08 | pg_wal 2026-10-27 07:50:25+08 | postgresql.auto.conf 2026-10-04 01:46:59+08
```

### 1.4 Meilisearch

**从未启动过**，`meili_data` 卷不存在：

```
$ docker volume ls --format '{{.Name}}' | grep -i meili   → 无输出（exit 1）
$ docker ps -a --format '{{.Names}} {{.Image}}' | grep -i meili → 无输出（exit 1）
$ ss -ltn | grep 17700 → 无输出（exit 1）
```

`.env` 里 `MEILI_PORT=17700` 被注释；`compose.yaml` 的 `meilisearch` 带 `profiles: ["search"]`，
`dev.sh` 的 `DEPENDENCY_SERVICES=(postgres redis mailpit)` 不含它。结论：**不参与本次整治**，
`meili_data` 无需盘（不存在），若要启用再按 `--profile search` 单独拉起。

### 1.5 端口占用（dev.sh 冲突点）

```
$ ss -ltnp | grep -E ':(15432|16379|17700)\b'
LISTEN 127.0.0.1:15432     # sforum-postgres 容器
LISTEN 127.0.0.1:16379     # sforum-redis 容器
# 17700 无监听
```

`compose.dev.yaml` 把 postgres/redis 都发布到同一对端口（`${POSTGRES_PORT:-15432}` /
`${REDIS_PORT:-16379}`，`.env` 正是 15432/16379）。因此 `sforum` project 的 postgres/redis
**即使卷改对了也会因端口冲突起不来**——除非先停掉手动容器。

---

## 2. 49 个 schema 分类

统计口径：`pg_namespace` 排除 `pg_*` 与 `information_schema`，正好 49 个（`pg_temp_64`/`pg_temp_91`/
`pg_toast_temp_*` 是会话临时 schema，不计入）。

聚合（按判定分组）：

| 分组 | schema 数 | 合计大小 |
|---|---|---|
| A 测试残留（`lifecycle_close_*` 36 + `bps_*` 2） | 38 | 11 MB + 816 kB |
| B 遗留孤儿（`sforum_gamification`） | 1 | 192 kB |
| C 必留（`public`/`sforum_core_v1`/`sforum_host_extensions`/`ops_bk_20261027`） | 4 | 143 MB |
| C 必留（`sforum_ext_*`） | 6 | 952 kB |

判定依据（代码溯源）：

- `lifecycle_close_*`：`apps/api/cmd/sforum/recovery_lifecycle_close_test.go:45`
  `schema := fmt.Sprintf("lifecycle_close_%d", time.Now().UnixNano())` —— CLI 恢复集成测试每次建一个，
  共 36 个，集中在 2026-10-17 03:25/03:26/03:34 与 2026-10-19 02:57/03:03 六个时间簇。
- `bps_*`：`apps/api/app/Models/Extensions/builtin_plugin_runtime_publication_postgres_integration_test.go:38`
  `schema := fmt.Sprintf("bps_%d_%s", time.Now().UnixNano(), label)` —— 2 个。
- `sforum_host_extensions`：`apps/api/database/migrator/core_extensions.go:9` 是 `pg_trgm` 的家目录，
  **必留**（删了 FTS 的 trigram 索引就没了）。
- `sforum_gamification`（无前缀）：与 `sforum_ext_s_sforum_gamification_06d2aa87b537e7967c0b` 表结构完全相同
  （7 张表），但 `grep -rn "sforum_gamification" apps/api extensions` **零引用**，且数据为 0 行
  （对照：带前缀 schema 的 `user_points_ledger` 有 4 行）→ 早期无前缀命名遗留的重复 schema，建议删。

完整 49 行清单：

| # | schema | 表数 | 大小 | 判定 | 依据 |
|---|---|---|---|---|---|
| 1 | `bps_1791728523299914460_concurrent_idempotent` | 12 | 408 kB | 可删 | 测试残留：builtin_plugin_runtime_publication 集成测试建 |
| 2 | `bps_1792179262369592623_upgrade_a_b` | 12 | 408 kB | 可删 | 测试残留：builtin_plugin_runtime_publication 集成测试建 |
| 3 | `lifecycle_close_1792178705766461712` | 6 | 248 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 4 | `lifecycle_close_1792178705870909859` | 6 | 248 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 5 | `lifecycle_close_1792178705955465832` | 6 | 248 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 6 | `lifecycle_close_1792178706050299409` | 6 | 248 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 7 | `lifecycle_close_1792178736734322046` | 6 | 320 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 8 | `lifecycle_close_1792178736840743218` | 6 | 320 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 9 | `lifecycle_close_1792178736942985434` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 10 | `lifecycle_close_1792178737038595722` | 6 | 264 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 11 | `lifecycle_close_1792178746077127828` | 6 | 320 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 12 | `lifecycle_close_1792178746178291601` | 6 | 320 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 13 | `lifecycle_close_1792178746288425836` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 14 | `lifecycle_close_1792178746389509949` | 6 | 264 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 15 | `lifecycle_close_1792178768882003229` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 16 | `lifecycle_close_1792178768996706541` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 17 | `lifecycle_close_1792178769105700275` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 18 | `lifecycle_close_1792178769200741769` | 6 | 264 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 19 | `lifecycle_close_1792178790625257705` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 20 | `lifecycle_close_1792178790757948544` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 21 | `lifecycle_close_1792178790906169151` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 22 | `lifecycle_close_1792178791036951111` | 6 | 264 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 23 | `lifecycle_close_1792179268315648882` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 24 | `lifecycle_close_1792179268467035034` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 25 | `lifecycle_close_1792179268605469098` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 26 | `lifecycle_close_1792179268721660266` | 6 | 264 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 27 | `lifecycle_close_1792349849384290531` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 28 | `lifecycle_close_1792349849555549402` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 29 | `lifecycle_close_1792349849690199906` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 30 | `lifecycle_close_1792349849816939256` | 6 | 264 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 31 | `lifecycle_close_1792349872090738910` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 32 | `lifecycle_close_1792349872233337129` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 33 | `lifecycle_close_1792349872372511930` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 34 | `lifecycle_close_1792349872514893746` | 6 | 264 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 35 | `lifecycle_close_1792350238498376676` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 36 | `lifecycle_close_1792350238660561575` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 37 | `lifecycle_close_1792350238903821689` | 6 | 336 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 38 | `lifecycle_close_1792350239113002782` | 6 | 264 kB | 可删 | 测试残留：cmd/sforum recovery lifecycle_close_test.go 建 |
| 39 | `ops_bk_20261027` | 7 | 368 kB | 必留 | 人工备份 schema |
| 40 | `public` | 154 | 142.5 MB | 必留 | 核心/扩展宿主 schema |
| 41 | `sforum_core_v1` | 0 | 0 B | 必留 | 核心/扩展宿主 schema |
| 42 | `sforum_ext_s_qa_store_proof_b0300eab510b04f82cb4` | 0 | 0 B | 待定 | 随 qa.store.proof 卸载处置（当前 0 表） |
| 43 | `sforum_ext_s_sforum_commerce_workflow_009a6a1cd1aea4c2f7d6` | 1 | 32 kB | 待确认 | 孤儿：extensions 表无对应行 |
| 44 | `sforum_ext_s_sforum_custom_content_b580e6973ebc405f2a23` | 3 | 176 kB | 待确认 | 孤儿：extensions 表无对应行 |
| 45 | `sforum_ext_s_sforum_gamification_06d2aa87b537e7967c0b` | 7 | 272 kB | 必留 | builtin sforum.gamification 数据 schema |
| 46 | `sforum_ext_s_sforum_marketplace_1fb93ebc6a8d374292f4` | 5 | 104 kB | 待定 | 随 sforum.marketplace 卸载处置 |
| 47 | `sforum_ext_s_sforum_oauth2_provider_69c7bc503d0b8e2a5a49` | 6 | 368 kB | 必留 | sforum.oauth2-provider 在用（enabled） |
| 48 | `sforum_gamification` | 7 | 192 kB | 可删 | 无前缀遗留重复 schema，0 行数据，代码零引用 |
| 49 | `sforum_host_extensions` | 0 | 0 B | 必留 | 核心/扩展宿主 schema |

> 说明：`public` 142.5 MB / 154 张表，是当前真实业务库（`sforum_core_v1` 是视图层，0 表）。
> `ops_bk_20261027` 见 §6.4——它是人工备份，但**内容不是你以为的那份**。

---

## 3. 扩展清单：dev 示例包 vs 测试垃圾

判定依据：`.env` 的 `EXTERNAL_EXTENSION_ROOTS`（dev 装配只扫这些目录）：

```
/home/inkedus/Code/github/SForum/extensions/dev
/home/inkedus/Code/github/sforum-oauth2-provider
/home/inkedus/Code/github/sforum-fetch-url
/home/inkedus/Code/github/sforum-web-search
/home/inkedus/Code/github/sforum-app-store/plugin
```

各目录的 `sforum.extension.json` 声明的 id 与版本（读文件得到）：

| 外部根 | 声明 id | 版本 |
|---|---|---|
| `extensions/dev/plugins/sforum.host-action-probe` | `sforum.host-action-probe` | 1.0.0 |
| `extensions/dev/plugins/wiki.harvester` | `wiki.harvester` | 0.1.0 |
| `sforum-oauth2-provider/plugins/sforum-oauth2-provider` | `sforum.oauth2-provider` | 1.6.0 |
| `sforum-fetch-url/plugins/sforum.fetch-url` | `sforum.fetch-url` | 1.0.3 |
| `sforum-web-search/plugins/sforum.web-search` | `sforum.web-search` | 1.1.0 |
| `sforum-app-store/plugin/plugins/sforum.store` | `sforum.store` | 0.1.1 |

测试垃圾判定补充：`acme.*` / `qa.store.proof` / `sforum.marketplace` 在
`apps/api`、`apps/web`、`tests`、`scripts` 全仓 `grep` **零引用**；`p4.e2e.preserve.<UnixNano>` 的 id
形状就是测试生成的。`sforum.page-*-reference` 在测试里**只作为 fixture 名**出现
（如 `apps/api/app/Support/Extensions/IntegrationTests/page_loader_reference_fixture_integration_test.go`），
测试会自行安装，运行中的那几份是残留。

完整 30 行（`gc行` = `extension_version_gc_removals` 行数，`pending墓碑` = 未收敛的 uninstall tombstone）：

| id | type | status | source | active_ver | staged | gc行 | pending墓碑 | 分组 | 可否安全 disable |
|---|---|---|---|---|---|---|---|---|---|
| `acme.guardprobe` | plugin | installed | uploaded | 6257 | - | 0 | 0 | 测试垃圾 | 是（已 disabled；disable 不清理墓碑） |
| `acme.iconprobe` | plugin | enabled | uploaded | 5773 | - | 3 | 0 | 测试垃圾 | 是 |
| `p4.e2e.preserve.1792178786289107715` | plugin | disabled | uploaded | 9034 | - | 0 | 0 | 测试垃圾 | 是（已 disabled；disable 不清理墓碑） |
| `p4.e2e.preserve.1792179265065189443` | plugin | disabled | uploaded | 9065 | - | 0 | 0 | 测试垃圾 | 是（已 disabled；disable 不清理墓碑） |
| `qa.store.proof` | plugin | disabled | uploaded | 21890 | - | 0 | 1 | 测试垃圾 | 是（已 disabled；disable 不清理墓碑）；pending 墓碑需单独收敛 |
| `selection.pg.1791720803491962621` | plugin | disabled | uploaded | 938 | - | 0 | 0 | 测试垃圾 | 是（已 disabled；disable 不清理墓碑） |
| `selection.pg.1791720966179938357` | plugin | disabled | uploaded | 956 | - | 0 | 0 | 测试垃圾 | 是（已 disabled；disable 不清理墓碑） |
| `selection.pg.1791728556567116637` | plugin | disabled | uploaded | 1392 | - | 0 | 0 | 测试垃圾 | 是（已 disabled；disable 不清理墓碑） |
| `selection.pg.1792376678734201215` | plugin | disabled | uploaded | 16493 | - | 0 | 0 | 测试垃圾 | 是（已 disabled；disable 不清理墓碑） |
| `sforum-shortcodes` | plugin | enabled | builtin | 21082 | 22864 | 43 | 0 | builtin（系统） | 否：is_system=true / is_deletable=false |
| `sforum.auth-github` | plugin | installed | builtin | 22861 | - | 47 | 0 | builtin（系统） | 否：is_system=true / is_deletable=false |
| `sforum.content-policy` | plugin | enabled | builtin | 19535 | 22862 | 42 | 0 | builtin（系统） | 否：is_system=true / is_deletable=false |
| `sforum.fetch-url` | plugin | enabled | uploaded | 22937 | 22872 | 55 | 0 | dev 示例包 | 是（会中断当前 dev 流程） |
| `sforum.gamification` | plugin | disabled | builtin | 2964 | - | 0 | 0 | builtin（系统） | 否：is_system=true / is_deletable=false |
| `sforum.host-action-probe` | plugin | enabled | uploaded | 19690 | - | 0 | 0 | dev 示例包 | 是（会中断当前 dev 流程） |
| `sforum.marketplace` | plugin | disabled | uploaded | 4283 | 11345 | 3 | 2 | 测试垃圾 | 是（已 disabled；disable 不清理墓碑）；pending 墓碑需单独收敛 |
| `sforum.oauth2-provider` | plugin | enabled | uploaded | 22956 | 22871 | 14 | 0 | dev 示例包 | 是（会中断当前 dev 流程） |
| `sforum.page-component-reference` | plugin | enabled | uploaded | 7630 | - | 0 | 0 | 测试垃圾 | 是 |
| `sforum.page-loader-reference` | plugin | disabled | uploaded | 7267 | 7434 | 0 | 0 | 测试垃圾 | 是（已 disabled；disable 不清理墓碑） |
| `sforum.page-loader-reference-live` | plugin | enabled | uploaded | 7435 | - | 0 | 0 | 测试垃圾 | 是 |
| `sforum.search-site` | plugin | enabled | builtin | 19536 | 22863 | 42 | 0 | builtin（系统） | 否：is_system=true / is_deletable=false |
| `sforum.smtp` | plugin | enabled | builtin | 19538 | 22865 | 41 | 0 | builtin（系统） | 否：is_system=true / is_deletable=false |
| `sforum.storage-fs` | plugin | enabled | builtin | 19550 | 22866 | 46 | 0 | builtin（系统） | 否：is_system=true / is_deletable=false |
| `sforum.storage-s3` | plugin | enabled | builtin | 19672 | 22867 | 46 | 0 | builtin（系统） | 否：is_system=true / is_deletable=false |
| `sforum.store` | plugin | enabled | uploaded | 22897 | - | 8 | 0 | dev 示例包 | 是（会中断当前 dev 流程） |
| `sforum.web-push` | plugin | enabled | builtin | 7220 | 22868 | 14 | 0 | builtin（系统） | 否：is_system=true / is_deletable=false |
| `sforum.web-search` | plugin | enabled | uploaded | 23106 | 22873 | 0 | 1 | dev 示例包 | 是（会中断当前 dev 流程）；pending 墓碑需单独收敛 |
| `wiki.harvester` | plugin | disabled | uploaded | 4273 | 22870 | 0 | 0 | dev 示例包 | 是（当前已 disabled） |
| `acme.switchtest` | theme | disabled | uploaded | 5914 | - | 0 | 0 | 测试垃圾 | 是（已 disabled；disable 不清理墓碑） |
| `sforum.default-theme` | theme | enabled | builtin | 19542 | 20769 | 11 | 0 | builtin（系统） | 否：is_system=true / is_deletable=false |

要点：

- **`acme.iconprobe` 当前是 `enabled`**，且已有 3 行 gc 记录 —— 将来真去卸载它会立刻撞上 908/909 同一个 FK。
- `sforum.marketplace`（disabled，0.1.0，包内 `name="SForum Marketplace"`，来源 `uploaded`、不在任何
  `EXTERNAL_EXTENSION_ROOTS` 里）与 `sforum.store`（enabled，0.1.1，来自 `sforum-app-store/plugin`）
  职责重叠，判断为旧命名的历史残留——**这是推断**：`sforum-app-store` 仓库里没有留下 `sforum.marketplace`
  的字样或迁移提交，只能凭包名与同 0.1.x 版本线判断。若确要认真对待它，需要 YG 确认一句"marketplace 是不是废案"。
- 10 个 builtin（`is_system=true, is_deletable=false`）动不了，别在计划里排它们。
- **disable 不清理墓碑**：对 703/908/909/1154 相关扩展做 disable，sweep 仍会每 5 分钟刷同样的 WARN。

---

## 4. uninstall cleanup 失败：4 条告警、3 个根因

### 4.1 现场证据

当前 API 日志 `/tmp/ops-api-dev4.log`（07:56:59 一轮，共 56 条失败记录 = 14 轮 × 4）：

```
2026/10/27 07:56:59 WARN uninstall cleanup sweep item failed operationId=703 extensionId=sforum.web-search
  error="... purge lifecycle cleanup lifecycle-cleanup-c42bdff3...: bootstrap: production lifecycle cleanup exact artifact conflict"
2026/10/27 07:56:59 WARN ... operationId=908 extensionId=sforum.marketplace
  error="... purge lifecycle cleanup lifecycle-cleanup-0c43d3a0...: delete exact lifecycle extension identity:
          ERROR: update or delete on table \"extensions\" violates foreign key constraint
          \"extension_version_gc_removals_extension_id_fkey\" on table \"extension_version_gc_removals\" (SQLSTATE 23503)"
2026/10/27 07:56:59 WARN ... operationId=909 extensionId=sforum.marketplace   （同上，cleanup_id ...f1c30047...）
2026/10/27 07:56:59 WARN ... operationId=1154 extensionId=qa.store.proof
  error="... clear uninstalled provider selections: provider slot selection invalidation requires actor and audit evidence"
```

首次出现 06:39:58，之后约每 5 分钟一轮（`extension_lifecycle_cleanup` sweep 调度）。

pending 墓碑（4 条，全部 `cleanup_mode=uninstall_preserve`、`terminal_result=succeeded`）：

```
$ select c.operation_id, c.target_extension_id, c.status, o.operation, o.terminal_result, o.removal_mode ...
703  sforum.web-search   pending  uninstall  succeeded  preserve  completed 10-19 02:30
908  sforum.marketplace  pending  uninstall  succeeded  preserve  completed 10-24 21:03
909  sforum.marketplace  pending  uninstall  succeeded  preserve  completed 10-24 21:04
1154 qa.store.proof      pending  uninstall  succeeded  preserve  completed 10-26 10:52
```

### 4.2 703 `sforum.web-search` —— 过期墓碑（设计上永不收敛）

判定依据：`lockExactExtension` 要求 `extensions.active_version_id == retained_version_id`：

`apps/api/bootstrap/extension_lifecycle_cleanup_postgres.go:125`
```go
if !activeVersionID.Valid || activeVersionID.Int64 != request.RetainedVersionID {
    return errProductionLifecycleCleanupConflict
}
```

实测对比：

| operation | 墓碑 retained_version_id | 当前 active_version_id | 匹配 |
|---|---|---|---|
| 703 | 14155（digest `9abf2558…`） | **23106**（digest `2da65b90…`，1.1.0） | ✗ |
| 908 | 4283（`d6c41b93…`） | 4283 | ✓ |
| 909 | 4283 | 4283 | ✓ |
| 1154 | 21890（`38ce1e20…`） | 21890 | ✓ |

也就是说 10-19 02:29 卸载 web-search 后，它又被重装/升级到 1.1.0（当前 enabled），墓碑钉的旧版本
14155 已经不是 active。同时墓碑的 `retained_package_path`（`…/1.0.0/9abf2558…`）在磁盘上
**已被 GC 删除**（`ls` 报 No such file）。两条 fail-closed 条件都成立。

**安全解法（需产品决策，不是运维能自己拍的）**：
- 语义上需要"墓碑作废（abandoned）"这一终态：当 retained 身份不再匹配、且该扩展当前存在并可用时，
  把墓碑标为 `finalized`（附 `finalized_operation_revision`/`finalized_at` + 审计事件），**不做物理 purge**。
- 或者更保守：让 sweep 跳过"retained 版本已不是 active"的墓碑，不再每 5 分钟刷 WARN，只保留在管理端
  "清理挂起"徽标里等人工确认。
- 任何方案都不要去"把 active_version_id 改回 14155"来凑条件——那会把正在用的 1.1.0 打成失效制品。

### 4.3 908/909 `sforum.marketplace` —— FK 23503（真正的阻塞点）

链路（逐跳有证据）：

1. sweep 找到 pending tombstone（`extension_lifecycle_cleanup_records`，`record_kind='uninstall_tombstone'`,
   `status='pending'`）→ `finalizeLifecycleUninstall(..., forced=true)`。
2. 资产发布隔离、附件引用释放、provider 选择清理都通过。
3. `commitPurge` 依次执行（`apps/api/bootstrap/extension_lifecycle_cleanup_postgres.go:160-190`）：
   `DELETE page_provider_bindings` → `UPDATE extension_trust_grants`（revoke）→
   `UPDATE extension_frontend_trust_grants` → `DELETE mail_provider_selection` →
   **`DELETE FROM extensions WHERE id=$1 AND active_version_id=$2`**。
4. 该 DELETE 被 `extension_version_gc_removals_extension_id_fkey` 拦下：

```
$ select conname, pg_get_constraintdef(oid) from pg_constraint
  where conrelid='extension_version_gc_removals'::regclass;
extension_version_gc_removals_extension_id_fkey | FOREIGN KEY (extension_id) REFERENCES extensions(id) ON DELETE RESTRICT
```

5. 拦截者就是今天 04:26:20 那轮版本 GC 写下的 3 行：

```
 id  | extension_id        | version | digest           | bytes_freed | by_user | created
 288 | sforum.marketplace  | 0.1.0   | f23889138ec0ba7e | 59220423    | 1       | 10-27 04:26:20
 289 | sforum.marketplace  | 0.1.0   | 55419673180374e9 | 59220423    | 1       | 10-27 04:26:20
 290 | sforum.marketplace  | 0.1.0   | a16126e3ef364751 | 59220423    | 1       | 10-27 04:26:20
```

**根因**：`extension_version_gc_removals` 是"已释放字节"的审计**锚点表**，`RESTRICT` 是刻意的完整性约束
（仓库自己的测试注释把删除顺序写成硬约束）：

`apps/api/app/Models/Extensions/postgres_store_version_gc_integration_test.go:127-133`
```go
// 顺序是硬约束，不是风格：
//  1) plugin_runtime_publication_members 对 extension_versions 是 RESTRICT ...
//  2) 锚点表 extension_version_gc_removals 对 extensions 也是 RESTRICT；
//  3) 最后删 extensions，其余子表是 CASCADE。
```

而生产清理路径 `commitPurge` **没有执行第 2 步**（它只删 `page_provider_bindings`、
revoke 两个 trust grant 表、删 `mail_provider_selection`，然后直接删 `extensions`）。
于是**任何被 GC 过版本的扩展都无法卸载**——`acme.iconprobe`（3 行）、`sforum-shortcodes`（43 行）、
`sforum.fetch-url`（55 行）、`sforum.oauth2-provider`（14 行）、`sforum.store`（8 行）全都有同样隐患，
只是 builtin 不可卸载才没暴露。

**安全解法（只给方案，不执行）**：

- 方案 A（推荐，代码修）：在 `commitPurge` 的同一事务内，`DELETE FROM extensions` 之前先
  `DELETE FROM extension_version_gc_removals WHERE extension_id = $1`。台账是"包已删"的记录，
  身份删除时它已无保留意义；若审计需要，改为在同一事务写一条 `extension_identity_purged` 审计事件后再删。
- 方案 B（改约束）：把 FK 改成 `ON DELETE CASCADE`。代价是静默丢审计行，且 `RESTRICT` 原本可能是
  刻意用来防止"身份还在但台账先没了"的错序，不建议。
- 方案 C（仅 dev 数据修复）：备份后
  `DELETE FROM extension_version_gc_removals WHERE extension_id='sforum.marketplace';`
  再等 sweep（≤5 分钟）自行收敛。**必须先备份**（见 §6.4 的备份现状），且这只治 908/909，
  治不了 703/1154，也不防下一次 GC 再种一行。
- 顺序提醒：无论哪个方案，都要先让 703（过期墓碑）和 1154（actor 缺失）有结论，否则 sweep 会一直有失败项，
  日志噪声掩盖真实进度。

### 4.3.1 受影响扩展清单（给代码修单的验收依据）

`commitPurge` 的删除序列里**只处理了 1 个**会 RESTRICT 的子表（`page_provider_bindings` 是 CASCADE，
它删得动；`mail_provider_selection` 是 CASCADE，也删得动）。`extensions` 上真正 `ON DELETE RESTRICT`
的子表有 5 张，`commitPurge` 一张都没清：

```
$ \d extensions   # 摘录 Referenced by 里 RESTRICT 的部分
attachment_storage_instances_extension_id_fkey        FOREIGN KEY (extension_id) REFERENCES extensions(id) ON DELETE RESTRICT
extension_builtin_removals_extension_id_fkey          ... ON DELETE RESTRICT
extension_missing_artifact_removals_extension_id_fkey ... ON DELETE RESTRICT
extension_version_gc_removals_extension_id_fkey       ... ON DELETE RESTRICT
notification_channel_selections_extension_id_fkey     ... ON DELETE RESTRICT
```

`commitPurge`（`apps/api/bootstrap/extension_lifecycle_cleanup_postgres.go:160-190`）实际只做：
`DELETE page_provider_bindings` → `UPDATE extension_trust_grants`（revoke）→
`UPDATE extension_frontend_trust_grants` → `DELETE mail_provider_selection` → `DELETE extensions`。
所以修的时候**五张都要按顺序过一遍**，不能只补 gc 那一条（否则 `p4.e2e.preserve.*` /
`selection.pg.*` / `page-loader-reference` 那 7 个会在 missing_artifact 上撞第二次）。

全库现状矩阵（`exists` 扫描，22 个扩展命中至少一张 RESTRICT 子表）：

| extension_id | status | is_deletable | gc_removals | missing_artifact | builtin_removals | attachment_storage | notif_channel | 撞 FK 风险 |
|---|---|---|---|---|---|---|---|---|
| `sforum.fetch-url` | enabled | true | 55 | 0 | 0 | 0 | 0 | **高：卸载必撞** |
| `sforum.auth-github` | installed | false | 47 | 0 | 0 | 0 | 0 | 低（builtin 不可卸载，但同路径复用即撞） |
| `sforum.storage-fs` | enabled | false | 46 | 0 | 0 | 0 | 0 | 低（builtin 不可卸载，但同路径复用即撞） |
| `sforum.storage-s3` | enabled | false | 46 | 0 | 0 | 1 | 0 | 低（builtin 不可卸载，但同路径复用即撞） |
| `sforum-shortcodes` | enabled | false | 43 | 0 | 0 | 0 | 0 | 低（builtin 不可卸载，但同路径复用即撞） |
| `sforum.content-policy` | enabled | false | 42 | 0 | 0 | 0 | 0 | 低（builtin 不可卸载，但同路径复用即撞） |
| `sforum.search-site` | enabled | false | 42 | 0 | 0 | 0 | 0 | 低（builtin 不可卸载，但同路径复用即撞） |
| `sforum.smtp` | enabled | false | 41 | 0 | 0 | 0 | 0 | 低（builtin 不可卸载，但同路径复用即撞） |
| `sforum.oauth2-provider` | enabled | true | 14 | 0 | 0 | 0 | 0 | **高：卸载必撞** |
| `sforum.web-push` | enabled | false | 14 | 0 | 0 | 0 | 0 | 低（builtin 不可卸载，但同路径复用即撞） |
| `sforum.default-theme` | enabled | false | 11 | 0 | 0 | 0 | 0 | 低（builtin 不可卸载，但同路径复用即撞） |
| `sforum.store` | enabled | true | 8 | 0 | 0 | 0 | 0 | **高：卸载必撞** |
| `acme.iconprobe` | enabled | true | 3 | 0 | 0 | 0 | 0 | **高：卸载必撞** |
| `sforum.marketplace` | disabled | true | 3 | 0 | 0 | 0 | 0 | **高：卸载必撞** |
| `p4.e2e.preserve.1792178786289107715` | disabled | true | 0 | 1 | 0 | 0 | 0 | **高：卸载必撞** |
| `p4.e2e.preserve.1792179265065189443` | disabled | true | 0 | 1 | 0 | 0 | 0 | **高：卸载必撞** |
| `selection.pg.1791720803491962621` | disabled | true | 0 | 1 | 0 | 0 | 0 | **高：卸载必撞** |
| `selection.pg.1791720966179938357` | disabled | true | 0 | 1 | 0 | 0 | 0 | **高：卸载必撞** |
| `selection.pg.1791728556567116637` | disabled | true | 0 | 1 | 0 | 0 | 0 | **高：卸载必撞** |
| `selection.pg.1792376678734201215` | disabled | true | 0 | 1 | 0 | 0 | 0 | **高：卸载必撞** |
| `sforum.gamification` | disabled | false | 0 | 0 | 1 | 0 | 0 | 低（builtin 不可卸载，但同路径复用即撞） |
| `sforum.page-loader-reference` | disabled | true | 0 | 1 | 0 | 0 | 0 | **高：卸载必撞** |

读法：

- **`is_deletable=true` 且 gc_removals>0 的 5 个**是当下"一卸就炸"的：`sforum.fetch-url` 55、
  `sforum.oauth2-provider` 14、`sforum.store` 8、`acme.iconprobe` 3、`sforum.marketplace` 3。
- **`is_deletable=true` 且 missing_artifact=1 的 7 个**（`p4.e2e.preserve.*` ×2、`selection.pg.*` ×4、
  `sforum.page-loader-reference`）现在卸不掉，但撞的是另一张表——这就是"只补 gc 不够"的证据。
- **9 个 builtin**（`is_deletable=false`）有 gc 行但卸不了，属潜在隐患：一旦将来走 missing-artifact 清理、
  或放开 builtin 卸载，同一条路径立刻复现。
- `sforum.storage-s3` 还有 `attachment_storage_instances` 1 行、`sforum.gamification` 有
  `extension_builtin_removals` 1 行——都在同一批 RESTRICT 里。

建议的验收断言（给老后写测试用）：

1. 一个"启用过、GC 过历史版本、再卸载"的扩展能收敛到 `finalized`，且 `extensions` 行消失。
2. 卸载后 `extension_version_gc_removals` 里该扩展的行数 = 0（或被归档到审计表，二者取一并在测试里钉死）。
3. `missing_artifact_removals` / `builtin_removals` / `attachment_storage_instances` /
   `notification_channel_selections` 四张表同样归零。
4. 负向：`extensions` 行仍存在（active_version_id 不匹配）时，purge 必须继续 fail-closed，不许先删台账。

### 4.4 1154 `qa.store.proof` —— actor/audit 证据缺失

链路：`service_lifecycle_v2.go:701` → `clearPluginProviderSelectionsWithAudit`：

`apps/api/app/Models/Extensions/lifecycle.go:552-568`
```go
if s.providerSlotSelections != nil {
    if actorUserID <= 0 || auditEventID <= 0 || reasonCode == "" {
        return fmt.Errorf("provider slot selection invalidation requires actor and audit evidence")
    }
    ...
```

actor/audit 取自 operation 行本身（`lifecycle_coordinator_drive.go:726-748`）：

```
 id   | extension_id   | requested_by_user_id | audit_event_id | recovery_actor | recovery_audit | authority_type
 703  | sforum.web-search  | 1    | 8643  | (null) | (null) | trust_grant
 908  | sforum.marketplace | 1    | 10249 | (null) | (null) | trust_grant
 909  | sforum.marketplace | 1    | 10251 | (null) | (null) | trust_grant
 1154 | qa.store.proof     | **NULL** | 11101 | (null) | (null) | trust_grant
```

`qa.store.proof` 是通过 dev-auto 无凭据通道装的（`AGENTS.md` 里的 zero-command dev 通道，
审计归属 `dev-auto` 系统账号），因此 `requested_by_user_id` 为空 → `actorUserID=0` → 直接失败。

关键坑：**免键收尾端点也救不了**。`FinalizeUninstallCleanupForExtension`（super_admin 调用）
最终同样走 `finalizeLifecycleUninstall(..., operation)`，actor 仍从 operation 行读，不读调用者身份
（`lifecycle.go:957-976`）。

而且这次失效化本身是**空操作**：`mail_provider_selection` 只有 `mail.provider → sforum.smtp` 一行，
`notification_channel_selections` 0 行，`qa.store.proof` 没有任何 provider 选择可清——守卫在"没事可做"
的情况下先炸了。

**安全解法（只给方案）**：
- 方案 A（推荐，代码修）：先查该扩展是否真有选择行，为空时直接返回成功，把 actor/audit 校验挪到
  "确实要改数据"的分支里。语义上仍然安全：无行可删就不需要 actor 证据。
- 方案 B（代码修）：`finalizeLifecycleUninstall` 接受"调用者 actor"作为 operation actor 的兜底，
  免键收尾时把 super_admin 的 user id + 一条新审计事件写进 operation 再执行。
- 方案 C（dev 数据修复）：给 1154 补 `requested_by_user_id` 与对应审计事件——但这是**伪造证据**，
  除非另写一条"人工恢复"审计事件并记录真实操作者，否则不建议。
- 注意：只做 disable 或重装都不会清墓碑；`qa.store.proof` 目前 disabled 且 0 表 schema，
  风险低，可以排到 P3。

---

### 4.5 703「作废墓碑」需要动的表和字段（只列清单，不动数据）

先说结论：**当前 schema 里没有"作废"这个状态，硬做只能靠迁移或撒谎**。三条约束把路堵死了。

**要动的表**

| 表 | 字段 | 现状 | 作废动作要做什么 |
|---|---|---|---|
| `extension_lifecycle_cleanup_records` | `status` | 枚举只有 `retained` / `pending` / `finalized`（`extension_lifecycle_cleanup_records_status_check`） | 需要一个"非 pending 且非 finalized"的终态，或复用 finalized 并放宽下面两条 |
| 同上 | `finalized_at` | 703 为 NULL | finalized 时必填，且 `>= created_at`（check10） |
| 同上 | `finalized_operation_revision` | NULL | finalized 时必填（check7） |
| 同上 | `finalized_operation_completed_at` | NULL | finalized 时必填（check7） |
| 同上 | `purge_receipt_id` | NULL | finalized 时必填且非空（check7） |
| 同上 | `purge_proof` / `purge_proof_digest` | NULL | finalized 时必填：jsonb 非空对象 + 64 位 hex（check7） |
| 同上 | `physical_identity_present` / `physical_package_present` / `physical_runtime_recovery_present` | 全为 `t` | **finalized 要求三者全为 false**（check8）——703 的扩展还在、`extensions` 行还在，改成 false 就是撒谎 |
| 同上 | `revision` / `updated_at` | 1 / 2026-10-19 | 变更需递增 revision 并刷新 updated_at（check9） |
| `extension_lifecycle_operations` | `terminal_result` / `completed_at` | 已是 `succeeded` / 有值 | **不需要动**；sweep 的输入条件正是"operation 已终态成功" |
| `audit_events` | `action` / `actor_user_id` / `metadata` | — | 作废动作必须留一条审计（谁、为什么、哪个 operation），metadata 里带 cleanup_id 与 retained/active 版本对比 |

**三条阻塞约束（原样摘录）**

```
extension_lifecycle_cleanup_records_status_check
  CHECK (status = ANY (ARRAY['retained','pending','finalized']))

extension_lifecycle_cleanup_records_check7   -- finalized 的必备字段
  CHECK (((status='finalized') AND finalized_at IS NOT NULL AND finalized_operation_revision IS NOT NULL
          AND finalized_operation_completed_at IS NOT NULL AND purge_receipt_id IS NOT NULL
          AND purge_receipt_id <> '' AND purge_proof IS NOT NULL AND jsonb_typeof(purge_proof)='object'
          AND purge_proof <> '{}' AND purge_proof_digest ~ '^[0-9a-f]{64}$')
      OR ((status<>'finalized') AND ... 全部 IS NULL))

extension_lifecycle_cleanup_records_check8   -- finalized 必须是"物理三件套都不在"
  CHECK (((status = ANY (ARRAY['retained','pending'])) AND physical_identity_present
          AND physical_package_present AND physical_runtime_recovery_present)
      OR ((status='finalized') AND NOT physical_identity_present
          AND NOT physical_package_present AND NOT physical_runtime_recovery_present))
```

**一个必须知道的事实矛盾**：703 的 `physical_package_present` 在库里是 `t`，但墓碑指向的包目录
`storage/extensions/sforum.web-search/1.0.0/9abf2558…/` **在磁盘上已经不存在**（`ls` 报 No such file）。
也就是说这张表的物理快照列**已经和磁盘不一致**了——任何"把 flag 改成 false 就凑成 finalized"的做法，
都是在一条本来就不可信的记录上再写一次假。

**三条可选路径（推荐第 1 条）**

1. **新增终态（迁移）**：`status` 加 `abandoned`，加 `abandoned_at` / `abandoned_reason`（或复用 metadata），
   放宽 check4/check7/check8 对 `abandoned` 的要求，保留 `pending` 的严格语义。
   sweep 的查询条件是 `status='pending'`（`lifecycle_repository_operations.go:474-475`），
   所以新状态**自动退出 sweep**，不需要改查询。管理端徽标同理自动消失。
   代价：一条迁移 + `PendingUninstallCleanupExtensions` 的语义说明 + 一条决策记录。
2. **只让 sweep 跳过**（代码修，不动 schema）：sweep 里判断"retained 版本已不是 active"就跳过并降级为
   debug 日志。代价：墓碑永远 `pending`，管理端"清理挂起"徽标永久挂着，日志噪声消失但状态仍是脏的。
   可作为第 1 条落地前的止血。
3. **删墓碑行**：`DELETE FROM extension_lifecycle_cleanup_records WHERE operation_id=703`。
   **不建议**——丢了"这次卸载做过什么"的唯一证据，且违反仓库对 lifecycle 账本 append-only 的一贯态度。

无论走哪条，都要回答老板一句话：**"扩展卸载后又重装"这种序列，旧墓碑该不该自动作废？**
这是产品语义，不是运维能替它决定的。

## 5. compose 改 external：diff 草案 + 回滚

### 5.1 现状（dry-run 证据）

```
$ docker compose -f compose.yaml -f compose.dev.yaml config | grep -A 3 '^  postgres_data:'
  postgres_data:
    name: sforum_postgres_data        # ← 孤儿卷（46M，10-18 09:49 后无写入）
```

### 5.2 草案：只改 `compose.dev.yaml`，不动 `compose.yaml`

**为什么不能改 `compose.yaml`**：`deploy.sh:294` 用
`docker compose --env-file .env.production -f compose.yaml -f compose.prod.yaml -f compose.release.yaml`
部署生产，`compose.prod.yaml` / `compose.release.yaml` 都**不重声明 `volumes:`**（已 grep 确认），
所以 `compose.yaml` 的 `postgres_data` 是生产也在用的定义。在那里写 `external: true` 会直接把生产部署打断。

`compose.dev.yaml` 末尾 `volumes:` 追加 `postgres_data` / `redis_data` 两个 external 别名。
**最终可应用的补丁正文见 §5.5**（此处不重复，避免两处版本漂移）；下面是当时的等价 dry-run 验证记录。

dry-run 验证（用 `/tmp` 里的等价覆盖文件，未改仓库文件）：

```
$ cat /tmp/ops-p2-volumes-external.yaml
volumes:
  postgres_data: {external: true, name: sforum-postgres-pgdata}
  redis_data:    {external: true, name: sforum-redis-data}

$ docker compose -f compose.yaml -f compose.dev.yaml -f /tmp/ops-p2-volumes-external.yaml config --quiet
CONFIG_OK

$ ... config | grep -A 3 -E '^  (postgres_data|redis_data):'
  postgres_data:
    name: sforum-postgres-pgdata
    external: true
  redis_data:
    name: sforum-redis-data
    external: true
```

附带收益：external 卷 **`docker compose down -v` 不会删**（compose 拒绝移除外部卷），误操作面变小。

### 5.3 只改卷不够：端口/容器归属必须一起定

`sforum` project 的 postgres 会抢 `127.0.0.1:15432`，而 `sforum-postgres` 容器正占着。两种可行路线：

- **路线 1（推荐）：compose 接管 postgres**
  1. 停并移除手动容器（**不删卷**）：`docker stop sforum-postgres && docker rm sforum-postgres`
     ——注意它的 compose project 是 `sforum-postgres`，**不要**在那个目录跑 `docker compose down -v`。
  2. 应用 §5.2 diff，`./scripts/dev.sh` 起 compose postgres（镜像 `postgres:17.11-alpine`，与现有
     `postgres:17-alpine` 同 major，pgdata 可直读；建议先确认现容器实际小版本）。
  3. redis 同理。
  风险：手动容器的重启策略/端口习惯被改；需同步 `/home/inkedus/server/docker/compose/postgres`
  的使用习惯。
- **路线 2（最小改动）：dev.sh 不再管 postgres/redis**
  只保留 mailpit，`DATABASE_URL` 继续指 `127.0.0.1:15432`。但 `dev.sh` 的 `migrate` 步骤是
  `docker compose run --rm migrate`，容器内解析 `postgres` 主机名会失败，需要把
  `MIGRATE_DATABASE_URL` 换成 `host.docker.internal:15432`（并加 `extra_hosts: host-gateway`），
  改动比路线 1 更碎。

### 5.4 回滚步骤（任一时刻可回）

1. 恢复 `compose.dev.yaml`：删掉新增的 `postgres_data`/`redis_data` 两段
   （或 `git checkout -- compose.dev.yaml`，前提是文件无其它未提交改动——**先 `git diff compose.dev.yaml` 确认**）。
2. `docker compose -f compose.yaml -f compose.dev.yaml config | grep -A 2 '^  postgres_data:'`
   确认又指回 `sforum_postgres_data`。
3. 若走了路线 1 且要回退到手动容器：按 `/home/inkedus/server/docker/compose/postgres/docker-compose.yml`
   `docker compose up -d`（该文件与卷 `sforum-postgres-pgdata` 未动过，数据无损）。
4. 卷本身全程只读挂载关系变化，**没有任何删除动作**，所以回滚不需要数据恢复；
   唯一需要确认的是停容器前没有未落盘事务（`docker stop` 会走 fast shutdown，PG 安全）。

---

### 5.5 最终待应用 diff（已就位，等通知）

裁决：走 compose 接管路线，**只改 `compose.dev.yaml`**。diff 已落成可直接应用的补丁：

- 补丁文件：`knowledge/sessions/2026-10-27-compose-dev-volumes-external.patch`
- 已验证：`git apply --check --verbose knowledge/sessions/2026-10-27-compose-dev-volumes-external.patch`
  → exit 0（"正在检查补丁 compose.dev.yaml..."）
- 已验证：用等价覆盖文件跑 `docker compose -f compose.yaml -f compose.dev.yaml config --quiet`
  → CONFIG_OK，且解析为 `postgres_data → sforum-postgres-pgdata (external)`、
  `redis_data → sforum-redis-data (external)`

补丁正文：

```diff
--- a/compose.dev.yaml
+++ b/compose.dev.yaml
@@ -123,3 +123,12 @@
   web_node_modules:
   attachment_uploads:
   extension_packages:
+  # dev 组合复用已在服务的手动容器卷，避免再建一套 sforum_postgres_data / sforum_redis_data。
+  # 只写在这里：compose.yaml 的 postgres_data / redis_data 仍被生产
+  # （compose.prod.yaml + compose.release.yaml）继承，在那里改会打断 deploy.sh 的部署链路。
+  postgres_data:
+    external: true
+    name: sforum-postgres-pgdata
+  redis_data:
+    external: true
+    name: sforum-redis-data
```

**应用前置条件（缺一不可，按顺序）**

1. 新做一次 `pg_dump -Fc` 落 `storage/backup/`（现有 `ops_bk_20261027` 不能当回滚点，见 §6.4）。
2. 停并移除手动容器：`docker stop sforum-postgres sforum-redis` + `docker rm sforum-postgres sforum-redis`
   （**不删卷**；不要在 `/home/inkedus/server/docker/compose/{postgres,redis}` 里跑 `down -v`）。
   不先做这步，compose postgres 会因 15432/16379 端口占用起不来（§1.5）。
3. `git apply knowledge/sessions/2026-10-27-compose-dev-volumes-external.patch`。
4. `./scripts/dev.sh`，确认 postgres 健康、`migrate` 通过、API 能连上活库（`sforum`，1085 MB）。

**验证通过后观察 24h 再动孤儿卷**（裁决 2）：`sforum_postgres_data`（46 M）先留，
它是 10-18 那次运行留下的唯一现场。

**回滚**：`git checkout -- compose.dev.yaml`（先 `git diff compose.dev.yaml` 确认无其它未提交改动）→
`docker compose -f compose.yaml -f compose.dev.yaml config | grep -A 2 '^  postgres_data:'` 应重新解析为
`sforum_postgres_data`；手动容器按各自的 `docker-compose.yml` `up -d` 拉回（卷未动，数据无损）。

## 6. 额外发现（都带证据，建议纳入 P2/P3 排期）

### 6.1 `storage/extensions` 已 22 GB —— 比整个数据库大 20 倍

```
$ du -sh storage/extensions
22G
$ du -sh storage/extensions/* | sort -hr | head -6
5.2G  sforum.store
3.9G  sforum.oauth2-provider
3.5G  sforum.web-search
2.9G  sforum.fetch-url
2.7G  wiki.harvester
1.4G  sforum.web-push
```

成因：dev zero-command watcher 每次源码变更就 `build → digest → stage` 一个新 digest 目录
（每个目录含完整 `backend/` 构建产物），`find storage/extensions -maxdepth 4 -name '.snapshot-*' -type d | wc -l`
= 38 个快照目录。这与"卷"无关，但在**宿主磁盘**上，且不在 `docker system df` 视野内。

### 6.2 真正的包目录残留是 38 个 `.snapshot-*` 暂存目录（1.6 GB）；另有一处精确制品丢失

先纠正一个容易误判的点：`sforum.marketplace/0.1.0/` 下 162 MB（`1605d6e…` 57M + `dd796dba…` 57M +
`efdfd483…` 48M）**不是 GC 残留**——这三个 digest 都还在 `extension_versions` 里
（4272/11339/11345，均为历史版本），属正常保留，回收它们是 version GC 的活。GC 台账那 3 行
（`f238891…`/`5541967…`/`a16126e…`）对应的目录**确实已经删掉了**，台账与磁盘一致。

而且其中两个目录按现行决策**永远不会被 GC 回收**：

```
$ select m.extension_version_id, left(m.package_digest,16), count(*) as revisions
  from plugin_runtime_publication_members m where m.extension_id='sforum.marketplace' group by 1,2;
4272 | efdfd483ae0d01ba |   5   ← 被发布历史钉住
4283 | d6c41b933f8c5793 | 417   ← active，同时被 417 条发布修订钉住
```

`knowledge/decisions/2026-10-17-version-gc-publication-restrict-cooperation.md` 第 1/3 条写明
"GC 永不删除发布历史……被发布历史引用的版本行判据为永久钉住"，并坦承"曾被启用的插件版本行被永久钉住，
版本 GC 对这类版本不释放磁盘"。`11345`（`1605d6e…`）不在 members 里，是保留窗口内的"最近历史版本"；
`11339`（`dd796dba…`）无发布引用、无开放 lifecycle 操作（只有一条 `cancelled` 的 upgrade，op 361 属于
`11345`），属可回收候选但当前没被任何一轮 GC 选中。**这 162 MB 要动，得先有"发布历史归档"的决策，
不是运维清目录能解决的。**

真正的残留是另一种：`ExtensionPackage/snapshot.go:105` 的
`os.MkdirTemp(versionRoot, ".snapshot-")` 暂存目录，被打断的暂存操作没清掉：

```
$ find storage/extensions -maxdepth 4 -name '.snapshot-*' -type d | wc -l
38
$ find storage/extensions -maxdepth 4 -name '.snapshot-*' -type d -print0 | xargs -0 du -sch | tail -1
1.6G   total
# 单个最大：sforum.oauth2-provider/1.6.0/.snapshot-4282540164  60M（同目录另有 4 个 60M 的）
```

注意 `.gc-staging` 是 GC 的暂存后缀（`postgres_store_version_gc.go:80`），和 `.snapshot-` 不是一回事，
清理时别混。

**另一处异常（更值得关注）**：`sforum.marketplace` 的 **active 版本目录不在磁盘上**：

```
$ ls -1 storage/extensions/sforum.marketplace/0.1.0/
1605d6e7…  dd796dba…  efdfd483…          # 只有三个历史版本
$ extensions.active_version_id = 4283 → package_path .../d6c41b933f8c5793…/   → 目录不存在
```

即 `active_version_id=4283` 指向的精确制品已丢失（`extension_versions` 行还在、被 417 条发布修订引用，
`package_path` 指向空）。这也解释了为什么 908/909 的 `inspectProductionLifecyclePackage` 拿到
`present=false`——目前因为 `identity.present=true` 没被拦，但如果将来有人想"按制品重装/校验 marketplace"，
这条路已经断了。丢失原因未查明（可能是 10-17 决策前的某轮 GC，也可能是暂存改名中途被打断），
**没有证据前不要归因**。

### 6.3 角色残留

```
$ select rolname from pg_roles where rolname like 'sforum%';
sforum_core_o_*                      23 个（每个核心迁移一批，未回收）
sforum_ext_o_/r_/l_*                 25 个
  其中 p4.e2e.preserve.* 相关 10 个（o/r 各 5）—— 对应 schema 已不存在，纯孤儿
  sforum_ext_o_/r_sforum_commerce_workflow / custom_content —— schema 在，extensions 表无对应行
```

`sforum_ext_s_sforum_commerce_workflow_*`（1 表 32 kB）与 `sforum_ext_s_sforum_custom_content_*`
（3 表 176 kB）是 preserve 模式留下的孤儿 schema（包目录已不在 `storage/extensions`），
extensions 表里没有对应行——清理前需人工确认里面是否还有想留的数据。

### 6.4 `ops_bk_20261027` 是备份，但不是"当前状态"的备份

```
$ select count(*) from ops_bk_20261027.extensions;              → 100
$ select id from ops_bk_20261027.extensions limit 3;
 test.version-gc.1793055815944708598 | plugin | disabled | 23127
 test.version-gc.1793055816020280572 | plugin | disabled | 23130
 test.version-gc.1793055816079950917 | plugin | disabled | 23133
$ select count(*) from ops_bk_20261027.extension_versions;      → 280
$ select count(*) from ops_bk_20261027.lifecycle_operations;    → 9
$ select count(*) from ops_bk_20261027.version_gc_removals;     → 20
```

它抓的是**某次 version-gc 测试运行**的快照（100 个 `test.version-gc.*` 扩展），
里面**没有** `sforum.marketplace` / `qa.store.proof` / `sforum.web-search` 任何一行。

所以：**在动 §4 的数据之前必须重新备份**（`pg_dump -Fc` 全库，或至少
`extensions` / `extension_versions` / `extension_lifecycle_*` / `extension_version_gc_removals`
四张表）。现有 `ops_bk_20261027` 不能当这次的后悔药，但它本身也别删（是历史证据）。

### 6.5 其他

- `docker system df`：Images 2.795 GB（可回收 1.954 GB，69%）、Build Cache **10.2 GB（可回收 10.08 GB）**、
  Local Volumes 5.725 GB（可回收 2.895 GB）。build cache 是除 `storage/extensions` 之外最大的
  可回收项，但属另一条线（不在这张单子里）。
- **卷数在盘点窗口内发生漂移**：07:56 首次 `docker volume ls` 是 31 个（4 个具名 + 27 个匿名），
  08:07 复查变 35 个——新增 4 个匿名卷的 `CreatedAt` 是 08:02:53 / 08:03:16 / 08:04:07 / 08:06:58，
  不是我建的（本轮全程只读）。同一时段 `apps/api/**/*postgres*_test.go` 有大批文件在改；按
  `AGENTS.md` 现行约定，Go 数据库测试走 `./scripts/test-api-isolated.sh`，它会为每次运行临时起
  一次性 PG 集群（并通过 `public.sforum_dev_guard` 哨兵拒绝连开发库），这些匿名卷就是它的产物。
  **这 4 个不在本报告统计内**，也不该和 `sforum_*` 那 4 个混为一谈；收尾时若已无 LINKS 可随
  `docker volume prune` 处理（需另开单，别和本次整治混在一起）。
- `sforum-postgres` 容器跑的是浮动 tag `postgres:17-alpine`，实际版本 `select version()` 报
  **PostgreSQL 17.11**，与 `compose.yaml` 钉的 `postgres:17.11-alpine` 同小版本 —— 路线 1 切过去
  不会触发 minor 回退，pgdata 可直接复用（同 major、同 minor）。
- `docker compose ls` 显示 `sforum` project `exited(1)`，网络 `sforum_default` 仍在；
  整治时若 `docker compose down`（不带 `-v`）会顺手删该网络，属预期。

---

## 7. 复现命令清单（全部只读）

```bash
cd /home/inkedus/Code/github/SForum
set -a; . ./.env; set +a

# 卷/容器归属
docker compose ls -a
docker ps -a --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}\t{{.Label "com.docker.compose.project"}}'
docker volume inspect sforum-postgres-pgdata sforum-redis-data sforum_postgres_data sforum_redis_data
docker system df -v
sudo -n stat -c '%y  %n' /var/lib/docker/volumes/*/_data

# 解析后的卷名（不启动任何东西）
docker compose -f compose.yaml -f compose.dev.yaml config | grep -A 3 '^  postgres_data:'

# schema / 扩展 / 墓碑
docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" sforum-postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -P pager=off -c "
select n.nspname, pg_get_userbyid(n.nspowner), count(c.oid) filter (where c.relkind in ('r','p'))
from pg_namespace n left join pg_class c on c.relnamespace=n.oid
where n.nspname not like 'pg\_%' and n.nspname <> 'information_schema'
group by 1,2 order by 1;"

# pending 墓碑（sweep 的输入集）
docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" sforum-postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -P pager=off -c "
select c.operation_id, c.target_extension_id, c.status, o.operation, o.terminal_result, o.removal_mode
from extension_lifecycle_cleanup_records c join extension_lifecycle_operations o on o.id=c.operation_id
where c.record_kind='uninstall_tombstone' and c.status='pending' order by 1;"

# FK 定义
docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" sforum-postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "
select conname, pg_get_constraintdef(oid) from pg_constraint
where conrelid='extension_version_gc_removals'::regclass;"

# 启动日志里的 sweep 失败
grep -c "uninstall cleanup sweep item failed" /tmp/ops-api-dev4.log   # 56
```

---

## 8. 决策清单（可直接转发，一屏读完）

背景一句话：dev 环境现在有**两套数据库**，脚本默认连的是 46 MB 的废库，而真正在用的是 2.2 GB 那个；
同时启动日志每 5 分钟刷 4 条清理失败告警。以下 6 件事需要拍板，前 4 件已定方向、只差执行批准。

| # | 一句话 | 推荐选项 | 代价 / 风险 |
|---|---|---|---|
| 1 | **两套库合成一套**：让 dev 脚本改用正在服务的 2.2 GB 库 | 只改 `compose.dev.yaml`（补丁已写好并验证），不动生产用的 `compose.yaml` | 需先停掉现在的手动 PG/Redis 容器，dev 环境**停机约 1–2 分钟**；卷不删，可回滚 |
| 2 | **46 MB 废库要不要删** | 先留，新配置跑满 24h 无异常后再删（删前先 dump） | 留着的成本只是 46 MB 磁盘；早删就永久失去 10-18 那次的现场 |
| 3 | **"扩展卸载后又重装"留下的旧清理记录怎么处理**（当前 4 条告警里有 1 条是它） | 数据库加一个"作废"状态（一条迁移），让它自动退出告警 | 一条迁移 + 一次产品语义确认；不改就只能一直挂着告警 |
| 4 | **卸载扩展会撞数据库外键、清不掉**（当前 4 条告警里有 2 条是它） | 改代码：删扩展前按顺序清 5 张关联表（不只清报错的那一张） | 影响 22 个扩展的卸载路径，需配回归测试；不改则**任何装过历史版本的扩展都卸不掉** |
| 5 | **49 个库里 39 个是测试垃圾**（约 12 MB） | P3 清，先逐个 dump 再 DROP，脚本可回滚 | 需先有新的全库备份；不清不影响功能，只影响可读性和后续排查 |
| 6 | **扩展包目录已 22 GB**（比数据库大 20 倍） | 单独开单：先清 1.6 GB 中断残留，162 MB 那部分需要"历史归档"决策才能动 | 清 1.6 GB 安全无风险；162 MB 动了会破坏发布证据链，不建议现在碰 |

补充两条已定、不用再拍的：`knowledge/index.md` 由 lead 统一收口（我不动）；
22 GB 那件事不混进本次整治，单独开单。

> 本文件只落在 `knowledge/sessions/`（task-2 的 write scope）。`knowledge/index.md` 的 Latest Handoff
> 我没有改——那不在我的写入范围，留给你决定要不要指过来。
