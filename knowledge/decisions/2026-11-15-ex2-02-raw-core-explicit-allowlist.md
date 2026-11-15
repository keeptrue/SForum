# Decision: raw_core 收敛到显式白名单（EX2-02）与 sforum_core_v1 的 PUBLIC 零权限约定（EX2-07）

## Status

Accepted（2026-11-15，簇 12-B）

## Context

扩展数据库授权有五档（`own_schema` → `core_views` → `host_commands` → `raw_core` →
`kernel`）。`raw_core` 是「已签名的高风险逃生口」，但它的实现是**按授权时点的
`pg_class` 快照枚举 public schema 的全部 Core 表**，然后逐表
`GRANT SELECT, INSERT, UPDATE, DELETE`：

- public 170 张表 − River 5 张 = **165 张表**、89 条序列全部拿到插件 DML；
- 其中包含身份、凭据、会话、令牌、扩展信任状态与审计表（`users`、
  `user_credentials`、`user_sessions`、`api_tokens`、`secret_store`、
  `extension_trust_grants`、`audit_events` …）；
- 快照是**授权时点**的：之后新建的表不自动纳入，所以「授了什么」既不可枚举也
  不可复现，爆炸半径只能靠现场查 ACL 才说得清。

`knowledge/decisions/2026-10-18-extension-data-surface-policy.md` 已经写明：
插件的跨域数据需求走四条受认可的增长面（stable view / HostQuery / Host Command /
事件投影），`raw_core` must never become the routine way to read or write core data。
按那条策略，上面这份「整库 DML」既没有产品依据，也没有被量化过。

EX2-07 是同一片数据面的另一半：`sforum_core_v1` 建 schema 时 REVOKE 过一次
PUBLIC，之后 8 条迁移新增视图却没有再 REVOKE。实测 PUBLIC 对这些视图当前零权限
（新对象默认不授予 PUBLIC），所以**不是现实暴露，而是 fail-closed 约定缺口**：
一旦默认权限被改动，新视图会带着 PUBLIC 可写面出厂而无人报错。

## Decision

### 1. raw_core 的 DML 范围 = 显式白名单 + 默认拒绝

`extension_database_raw_core_postgres.go` 里的两份常量是唯一来源：

| 常量 | 内容 | 说明 |
| --- | --- | --- |
| `extensionDatabaseRawCoreWritableTables` | `search_documents`、`search_index_state`、`search_reindex_runs` | 允许 DML 的表 |
| `extensionDatabaseRawCoreWritableSequences` | `search_reindex_runs_id_seq` | 允许 USAGE/SELECT/UPDATE 的序列 |

**准入标准只有一条**：表里的行是**派生数据**——可以由宿主或插件从其它 Core 数据
重新生成（重建索引即恢复），因此被写坏不会造成不可恢复的身份、凭据、会话、信任
或审计后果。内容表（`topics` / `posts` / `comments` / 反应 / 收藏 …）与配置表
（`web_options` / `extension_settings` / 导航 / 公告 …）**一律不在白名单里**：
内容变更走事件与 Host Command，配置走 options API，读取走 `sforum_core_v1` 视图。

授权与校验共用同一份白名单，但走两条独立路径：

- 授权侧 `loadExtensionDatabaseRawCoreGrantRelations` 按名字取对象，逐个核对
  relkind 与 owner（`sforum_core_o_*`）；名字缺失、类型不符、owner 不对一律
  fail closed —— 不能「授到几个算几个」。
- 校验侧仍然枚举 public 全量关系（这是安全网），期望值改为：白名单内 = 精确
  SELECT/INSERT/UPDATE/DELETE（序列 = USAGE/SELECT/UPDATE），白名单外 = **零
  ACL、零有效权限**；并额外断言白名单每一项都真实存在。因此「有人事后给
  `users` 补一条 INSERT」会直接让租约签发失败（`extension database physical
  resource conflict`），而不是悄悄生效。

### 2. 已装插件的行为变更与迁移路径（必读）

这是**对已安装 raw_core 插件的行为变更**，不是文档补充：

- **表现**：下一次运行时租约签发时，插件只拿到上表那几张派生投影表的 DML。任何
  对其它 Core 表的读写都会在首次使用时失败，错误是 PostgreSQL 的
  `ERROR: permission denied for table <name>`（SQLSTATE **42501**）。`core_views`
  档的视图读取、`own_schema` 档的自有表读写**不受影响**（累加式权限保持）。
- **怎么自查**：插件启动后跑一句只读探测即可列出自己实际还能写的表：

  ```sql
  SELECT n.nspname AS schema, c.relname AS relation
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname IN ('public', 'sforum_core_v1') AND c.relkind IN ('r','p','v')
    AND pg_catalog.has_table_privilege(current_user, c.oid, 'INSERT')
  ORDER BY 1, 2;
  ```

  生产侧也可以直接看日志里的 42501 与表名 —— 错误信息本来就带对象名。
- **迁移路径**（按数据面策略的优先级）：
  1. **读**：改用 `sforum_core_v1` 稳定视图（或对应 HostQuery），不要读 Core 表；
  2. **写内容**：改走 Host Command 或事件 + 自己在 `own_schema` 里的投影表；
  3. **写配置**：走 options / admin API，不要直写 `web_options` 这类表；
  4. **确实是可重建的派生投影**：提案把表加进白名单——同一提交里补 allowed /
     denied 测试与决策记录，评审标准就是上面那条准入标准。

不建议、也没有提供的路径：环境变量或配置项打开「退回全库 DML」。一个能被默认
打开、忘记关闭的开关，等于把审计项原样搬回生产默认值。

### 3. EX2-07：新迁移补 REVOKE + 扫全表的静态棘轮

- 新增 `202611150002_stable_core_views_public_revoke.sql`：
  `REVOKE ALL ON ALL TABLES IN SCHEMA sforum_core_v1 FROM PUBLIC` +
  `REVOKE ALL ON SCHEMA sforum_core_v1 FROM PUBLIC`。**不改任何历史迁移**：
  goose 按 version 记账、没有 checksum，改历史文件对已升级库完全无效。
  Down 段是显式 no-op（`SELECT 1`），回滚这条迁移不该重新打开 PUBLIC 写面。
- `stable_core_views_public_revoke_test.go` 里两条断言：
  1. **静态棘轮**扫全部迁移的 Up 段：每个 `CREATE [OR REPLACE] VIEW
     sforum_core_v1.*` 之后，必须存在 schema 级 REVOKE（同迁移更靠后，或版本号
     更大的迁移）。只盯本次补的 8 张视图，第 9、10 张会重新破防。
  2. **实库断言**：对 `sforum_core_v1` 里每张视图用 `has_table_privilege('public',
     …)` 逐项检查 SELECT/INSERT/UPDATE/DELETE/TRUNCATE/REFERENCES/TRIGGER 全为
     false，并要求 `pg_relation_is_updatable(...) = 0`（结构上只读）。

## Consequences

- `raw_core` 的 DML 面从 165 张表 + 89 条序列收敛到 3 张派生投影表 + 1 条序列；
  身份、凭据、会话、令牌、扩展信任、审计、内容与配置表全部零权限。
- 新建 Core 表**默认不授予**任何插件 DML —— 这正是期望行为：加表要显式改白名单
  并补测试，而不是靠「建在 public 就自动可写」。
- kernel 在 public 里创建的对象（历史兼容路径）同样不再对 raw_core 开放；相关
  集成测试从「能写」改为断言 42501。
- 代价：`raw_core` 的白名单是代码常量，Core 新增派生表时插件要等一次版本更新。
  这是有意的：让扩大爆炸半径变成一次显式、可审计的代码变更。
- EX2-07 之后，「新视图对 PUBLIC 零权限」从人工纪律变成会红的门：迁移漏 REVOKE
  会在静态棘轮上失败，ACL 被改动会在实库断言上失败。

## References

- 审计项：EX2-02、EX2-07（簇 12-B）
- 策略依据：`knowledge/decisions/2026-10-18-extension-data-surface-policy.md`
- 迁移契约：`knowledge/decisions/2026-11-15-online-safe-migration-human-contract.md`
- 授权模型：`knowledge/decisions/2026-10-12-extension-database-grant-authority.md`
- 实现：`apps/api/app/Support/Extensions/extension_database_raw_core_postgres.go`、
  `apps/api/database/migrations/202611150002_stable_core_views_public_revoke.sql`
