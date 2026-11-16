# 升级注意

[← 生产部署](../deployment.md)

这一页只收「跨版本会改变运维动作」的条目。每条都是同一套三栏：旧行为是什么、现在
变成什么、升级时要做什么。版本号以 Release 标签为准（`$SFORUM_VERSION`），本页随
发布线滚动维护，不钉死具体版本。

只读三行版：**升级前先配好备份口令**；自建脚本调 `install-from-url` **必须带
sha256**；持有 `raw_core` 授权的插件**要在升级后复验**。

## 速查

| # | 条目 | 谁受影响 | 什么时候做 |
| --- | --- | --- | --- |
| 1 | `install-from-url` 必须提供 sha256 | 自建脚本、SDK 调用方 | 升级前 |
| 2 | `raw_core` 收窄到显式白名单 | 持有该授权的插件 | 升级后复验 |
| 3 | 插件 `global_middleware` 作用域显式化 | 声明了 global middleware 的插件 | 升级后复验 |
| 4 | 插件子进程 cwd 钉在制品目录 | 第三方插件 | 升级后复验 |
| 5 | 备份加密口令成为升级硬前置 | 所有安装 | 升级前 |
| 6 | 匿名 `/ready` 只回摘要 | 解析 `/ready` 的脚本 | 按需改脚本 |
| 7 | 401 与 503 语义分家 | API 客户端、监控 | 按需重生成客户端 |
| 8 | 备份 / 恢复校验更严、更早失败 | 做备份与恢复的人 | 演练一次 |

## 1. `install-from-url` 必须提供 sha256

**旧行为**：`sha256` 是可选字段。不带它也能装，宿主不校验下载到的字节是否就是你要
的那份包。

**新行为**：`POST /api/v1/admin/extensions/install-from-url` 把 sha256 当作唯一的
来源性依据。缺字段直接 422 `extension.remote_digest_required`，而且**在任何出站请求
之前**就拒绝；不是 64 位小写十六进制 → 422 `validation.invalid`；下载后摘要不符 →
422 `extension.remote_digest_mismatch`。管理后台的 URL 安装表单本来就强制要求填。

**运维要做什么**：如果有脚本、CI 或 SDK 在调这条接口，先算摘要再提交：

```sh
sha256sum sforum-plugin.zip
```

请求体至少包含这两个字段（鉴权按你现有的方式补）：

```json
{"url": "https://example.com/sforum-plugin.zip", "sha256": "<sha256sum 输出的 64 位 hex>"}
```

升级后第一次调用如果收到 `extension.remote_digest_required`，就是这里没改。

## 2. `raw_core` 收窄到显式白名单

**旧行为**：签发 `raw_core` 租约时按授权时点的 `pg_class` 快照，把 public schema 里
全部 Core 表（165 张表 + 89 条序列）的 `SELECT` / `INSERT` / `UPDATE` / `DELETE` 授给
插件，其中包含 `users`、`user_credentials`、`user_sessions`、`api_tokens`、
`secret_store`、`extension_trust_grants`、`audit_events`。授了什么既不可枚举也不可复现。

**新行为**：只授三张可重建的派生投影表 `search_documents`、`search_index_state`、
`search_reindex_runs`，以及序列 `search_reindex_runs_id_seq`。白名单之外的 Core 表是
**零 ACL、零有效权限**，越界读写在首次使用时失败，错误是 PostgreSQL 的
`ERROR: permission denied for table <name>`（SQLSTATE `42501`）。白名单本体是代码
常量（`rawcore` 子包，签发与校验共读同一份只读副本），所以 Core 新增派生表要等一次
版本更新，且必须同批补测试与决策记录。`core_views` 档的视图读取与 `own_schema` 档的
自有表读写不受影响。

**运维要做什么**：升级后跑一遍带 `raw_core` 的第三方插件，看日志里有没有 `42501`。
要立刻自查它实际还能写哪些表：

```sql
SELECT n.nspname AS schema, c.relname AS relation
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname IN ('public', 'sforum_core_v1') AND c.relkind IN ('r','p','v')
  AND pg_catalog.has_table_privilege(current_user, c.oid, 'INSERT')
ORDER BY 1, 2;
```

有越界调用的插件按这个顺序改：读改走 `sforum_core_v1` 视图或 HostQuery；写内容改走
Host Command 或事件 + 自己在 `own_schema` 里的投影表；写配置走 options / admin API；
确实是可重建的派生投影，就提案把表加进白名单。没有「退回全库 DML」的开关，不用找。

## 3. 插件 `global_middleware` 的作用域改为显式声明

**旧行为**：`global_middleware` 无条件进入每一次匹配的贡献列表，于是任意插件的
global middleware 都会包住 Core 的每一条 `/api/v1` terminal。Core 侧唯一的隔离手段
是「这是 Core 路由」这个事实，没有任何声明、审计或测试约束。

**新行为**：作用域由 `targetId` 声明，只有两种：留空 = 只覆盖插件自己的 terminal；
写明 = 精确覆盖那一条 terminal（Core 路由要靠它显式 opt-in）。仍然禁止 `path` 与
`methods`（global 不是前缀匹配）；`core.route.system.health`、
`core.route.system.ready`、`core.route.notifications.stream` 不可被指向，manifest
校验直接拒绝。

**运维要做什么**：靠「包住 Core 路由」实现功能的插件，升级后会**静默不再生效**
（方向是收窄，不会放宽权限）。逐个检查插件 manifest 里的 `global_middleware` 条目：
需要覆盖 Core 路由就补 `targetId`，否则它只作用于该插件自己的 terminal。升级后跑一次
插件自带的用例，或观察它注入的响应头/字段，确认中间件真的在跑。

## 4. 插件子进程的工作目录钉在制品目录

**旧行为**：插件子进程继承宿主进程的 cwd，插件里的相对路径以「宿主启动目录」为基准。

**新行为**：cwd 固定为后端入口所在目录（惯例是 `<package-root>/backend`），exec 目标
来自同一次解析出的绝对路径；入口落在包内容根之外、或宿主工作树落进插件 cwd 时拒绝
启动。文件系统沙箱下，写自己二进制旁边的目录是 `EACCES`。

**运维要做什么**：第三方插件里 `./x`、`os.ReadFile("config.json")` 这类写法现在落在
包目录内，不再是启动目录。要写文件就改用绝对路径（`os.UserCacheDir()`、Host API、
运维配置项），不要写在自己二进制旁边。内置 8 个插件已核对：没有一处依赖相对路径读
宿主工作目录。作者契约见[构建与加载](../extensions/build-and-load.md)的「运行时进程
契约」一节。

## 5. 备份加密口令成为升级硬前置

**旧行为**：`.env.production` 模板里没有备份口令段，`deploy.sh` 只读进程环境。装完
直接 update 会停在备份步，而安装向导和模板都没有提示要先配它。

**新行为**：模板里有注释形式的 `SFORUM_BACKUP_KEY_FILE` / `SFORUM_BACKUP_KEY`
（注释形式，不会被 Compose 读进容器），`configure-production.sh` 收尾会打印生成命令，
`deploy.sh` 的备份动作从 `.env.production` 取值（shell 环境优先）。缺口令仍然硬失败，
**绝不产出明文备份**。`upgrade.sh` 直接调备份脚本，仍然只读进程环境。

**运维要做什么**：升级前建好口令文件，并两处都配：

```sh
install -m 600 /dev/null /etc/sforum/backup.key
openssl rand -base64 32 > /etc/sforum/backup.key
```

在 `.env.production` 里取消注释并指向它（覆盖 `deploy.sh` 的备份与 update）：

```sh
SFORUM_BACKUP_KEY_FILE=/etc/sforum/backup.key
```

执行 `upgrade.sh` 前再 `export` 同名变量——它读不到模板里那一行。口令文件不要和备份
归档放在同一块盘上。换过口令以后，旧归档只能用旧口令解，留一份旧口令到确认新备份
可恢复为止。

## 6. 匿名 `/ready` 只回摘要

**旧行为**：匿名请求 `/api/v1/ready` 会拿到完整就绪报告：恢复态带
`recovery.artifacts[]`（每个已装插件的 extensionId / version / package digest）与
`publicationRevision`；`components[].error` 直接回显插件进程启动失败的原文，可能带
制品路径、版本与 digest。

**新行为**：只回 `status`、`ready`、`checkedAt`、`components`、
`recovery{code,component,message}`。`artifacts` 与 `publicationRevision` 不再出现，
`components[].error` 收敛为固定摘要，契约给这几份 schema 加了
`additionalProperties: false`。恢复态仍然是 503 + `ready:false`，只是不再附带指纹。

**运维要做什么**：只吃状态码的消费方（Compose healthcheck、`release-smoke.sh`）完全
不受影响；`wait-for-health.sh` 与 `upgrade.sh` 除了状态码还要读 body 里的 `recovery`
块，而那块仍在白名单里，判定逻辑不变。从 `/ready` 抓制品清单的脚本改用带外通道：

```sh
sforum extension recovery list --json
sforum extension recovery recheck --reason "why it failed"
```

API 启动日志里的 `publication_revision` 与制品计数也还在。同批的探针变化：健康等待从
两态变三态（`0` 就绪 / `3` 恢复态 / `1` 超时），`deploy.sh` 在恢复态写
`reason=recovery_only` 并提示跑 recovery recheck，`upgrade.sh` 首次探到恢复态就
abort 并先回滚，不再白等满超时。

## 7. 401 与 503 语义分家

**旧行为**：同一个读端点对「拿不到登录主体」有五种答案：任何错误一律 401（Redis
抖动被答成「你的登录无效」）、吞错后匿名 200、原样上抛成 500、刻意匿名、跳过 viewer
分支。必登面里 19 处直连 `LoadActor` 的端点，依赖故障同样落到 500。

**新行为**：按原因分五类——A 无凭据 → 必登面 401、公开读匿名 200（行为不变）；B 凭据
存在但不可解析/失效 → 与 A 不可区分（会话层把它折成 `(0,false,nil)`）；C 主体
不存在 / disabled / banned → 401 `auth.required`（公开读；写路径仍由域层答 403
`permission.denied`）；D 依赖故障 → 503 `service.not_ready` + `Retry-After: 1`；
E 未识别 → 503（fail-closed，不放宽成匿名）。必登面 18 个控制器包、19 处直连调用点
全部换成同一个门，契约按可达性门补齐 188 条 503（另有 5 条门看不见的手工补）。

**运维要做什么**：把 503 + `Retry-After` 当**可重试**，不要据此清 cookie，也不要在
监控里报成「登录失效」；401 才是要重新登录。从 `contracts/openapi.yaml` 生成客户端的
项目要重跑生成器（状态码集合变了）。已知边界：B 在 HTTP 层不可观测，坏 token 与匿名
请求目前同答匿名；`Retry-After` 的字段级声明还没进 parity 自动比对。

## 8. 备份 / 恢复校验更严、更早失败

**旧行为**：卷恢复只比「归档里的条目在卷里存在」，写一半或截断都算通过；拓扑守卫只防
「声明 single 却在跑蓝绿槽」一个方向；蓝绿多槽共用一个 deadline；SIGKILL 留下的临时
工作目录（含 `600` 的明文 `database.sql`）没人清；冷库首启把所有 `up -d --wait` 失败
都当慢启动。

**新行为**：四条一起变——卷校验逐条比**大小**（缺条目、大小不符都红；列表解析不出任何
条目也算失败，不是通过）；拓扑守卫双向覆盖（声明 blue-green 而实跑单拓扑 `api` /
`worker` 同样在动手之前停手）；每个槽位各自计时（第二个槽不再拿到 0 秒预算）；备份
工作目录 `.sforum-backup-*` 超 24 小时（`SFORUM_BACKUP_STALE_WORKDIR_HOURS`，最小 1）
与恢复工作目录 `$TMPDIR/sforum-restore.*` 超 1 天自动清理。另外冷库首启现在区分
「慢启动」（降级成 warning，交给 `pg_isready` 重试）与「容器根本没在跑」（立刻失败并
点名容器状态）。

**运维要做什么**：升级后第一次恢复可能比以前**更早失败**——这是目的，不要绕过。失败
时重跑一次备份，不要用旧归档硬恢复。恢复前确认 `.deployrc` 的 `topology` 与实际在跑
的服务一致，否则脚本会在停服务之前就停手（这正是它该做的）。空核心表的警告现在每次
恢复只打一遍，第二次校验安静通过。

## 如果必须回退

回退到旧版本会把旧行为一起带回来：`/ready` 重新回显制品清单与
`publicationRevision`、恢复脚本不再比大小、`global_middleware` 重新无条件包住 Core
路由、401/503 重新合并成 401/500。回退前先确认这一点，尤其是 `/ready` 的可见面。

## 验证

```sh
./deploy.sh --action status
curl -fsS "http://127.0.0.1:${API_PORT:-18080}/api/v1/ready" | jq '{status, ready, recovery}'
sforum extension recovery list --json
```

第一条看部署状态；第二条确认 `/ready` 只回摘要（响应里不应出现 `artifacts` 与
`publicationRevision`）；第三条确认恢复态的制品清单还能带外取到。

## 相关

- [一键安装与更新](./one-click.md)：`deploy.sh` / `upgrade.sh` 的完整执行链与失败语义。
- [环境变量与密钥](./environment.md)：`.env.production` 的加载方式与必填项。
- [故障排查](./troubleshooting.md)：`recovery_required` 各 `reason` 的处置。
