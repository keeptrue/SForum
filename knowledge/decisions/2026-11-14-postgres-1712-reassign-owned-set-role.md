# 决策：扩展数据库回收路径在 PG 17.12 下临时获取接收者的 SET ROLE 权限

日期：2026-11-14
状态：已采纳

## 背景

`CI / Go gate` 在 `TestPostgresExtensionDatabaseKernelCleanupWorksForNonSuperCreateRoleHost`
上反复失败：

```
retire non-super kernel lease: ERROR: must be able to SET ROLE "sforum_p5_kernel_ext_..." (SQLSTATE 42501)
```

根因不是测试偶发，而是 PostgreSQL 17.12 的行为变更。17.12 release notes 原文
（<https://www.postgresql.org/docs/release/17.12/>）：

> Align `REASSIGN OWNED`'s caller-privilege checks with `ALTER ... OWNER TO`'s checks (Zsolt Paragri).
> Since v16, giving an object to another role requires the ability to `SET ROLE` to that role.
> However, a role granted `INHERIT TRUE, SET FALSE` could bypass that via `REASSIGN OWNED`.
> ... Make `REASSIGN` follow the `ALTER OWNER` rules. (CVE-2026-120146)

也就是说：

- PG 16 起 `ALTER <object> ... OWNER TO <role>` 就要求调用者能 `SET ROLE` 到**接收者**；
- 但 `REASSIGN OWNED BY <old> TO <new>` 在此之前只检查**旧 role** 的成员关系，
  于是 `INHERIT TRUE, SET FALSE` 的成员关系可以绕过接收者一侧的检查（CVE-2026-120146）；
- 17.12 把这个缺口堵上：接收者也必须可 `SET ROLE`。

本地复现（同一条 SQL，两个镜像，非超级用户 host）：

| 镜像 | `REASSIGN OWNED BY old TO new`（new 为 `INHERIT TRUE, SET FALSE`） |
|---|---|
| `postgres:17.11-alpine` | 通过 |
| `postgres:17-alpine`（17.12） | `ERROR: must be able to SET ROLE "new"` |
| `postgres:17-alpine`（17.12，new 改 `SET TRUE`） | 通过 |

为什么一直没被发现：CI 的 `postgres:17-alpine` 在 2026-11-12 随 17.12 发布更新了镜像 digest，
而本机 dev 容器（`sforum-postgres`）仍是 17.11 —— 同一测试本地恒绿、CI 恒红。

## 裁决

**在回收事务内临时获取接收者的 `SET ROLE` 权限，用完立即收回；不做常驻授权。**

生产代码里 `REASSIGN OWNED BY <旧 role> TO <扩展 owner>` 共有三处，全部在事务内执行：

1. `app/Support/Extensions/extension_database_runtime_lease_postgres.go` —
   `cleanupExtensionDatabaseRuntimeLeaseRole`，runtime lease 回收（`own_schema` 权限时）；
2. `app/Support/Extensions/extension_database_disposition_postgres.go` —
   `reassignAndDropExtensionDatabaseRole`，扩展卸载时回收 runtime/migration 子 role；
3. `app/Support/Extensions/extension_database_migration_role.go` —
   `revokeExtensionDatabaseMigrationRole`，迁移 role 退役。

三处都新增成对调用：

```go
acquireExtensionDatabaseObjectRecipientAuthority(ctx, tx, ownerRoleName)
// ... REASSIGN OWNED BY <old> TO <owner> / DROP OWNED BY / DROP ROLE ...
releaseExtensionDatabaseObjectRecipientAuthority(ctx, tx, ownerRoleName)
```

helper 实现在 `extension_database_kernel_postgres.go`，与既有
`acquire/releaseExtensionDatabaseKernelOwnerAuthority` 同构：

- `acquire`：`GRANT <owner> TO <session_user> WITH ADMIN FALSE, INHERIT TRUE, SET TRUE`；
- `release`：`REVOKE <owner> FROM <session_user> GRANTED BY CURRENT_USER`；
- 旧 role 一侧继续用 `SET FALSE`。存活租约 role 是带密码的 LOGIN role，
  给它 `SET TRUE` 等于让会话可以冒充租约身份，属于提权。

### 为什么不用常驻授权

扩展 owner 的成员关系是被校验的：
`validateExtensionDatabaseRoleIsolation(..., identifiers.OwnerRole, ..., allowedMembership: "")`
（`extension_database_registry_postgres.go:38-41`）要求 owner role **没有任何成员**，
只有 runtime role 在 `configureExtensionDatabaseOwnership` 里被显式授予。
给宿主一条常驻成员关系会让下一次 provisioning/校验直接报
`ErrExtensionDatabaseResourceConflict`；而回收只是瞬态需求，没有必要把权限常驻化。

### grantor 维度：为什么成对调用是安全的

`pg_auth_members` 的主键是 `(roleid, member, grantor)`，所以：

- `CREATEROLE` 会话创建 role 时，PostgreSQL 自动留下一条
  `grantor=<creator>, admin=true, inherit=false, set=false` 的行；
- 我们的 `GRANT` 不覆盖它，而是**新增**一条 `grantor=<session_user>, admin=false, inherit=true, set=true`；
- `REVOKE ... GRANTED BY CURRENT_USER` 只删自己那条，creator 的 `ADMIN` 仍在
  （所以随后的 `DROP ROLE <旧 role>` 依旧成立），也不会残留 `SET`。

实测（PG 17.12，逐步打印 `pg_auth_members`）：`CREATE ROLE` 后只有 creator 行；
`acquire` 后多出 `grantor=<session>, admin=f, inherit=t, set=t`；
`release` 后只剩 creator 行，`SET ROLE owner` 报 `permission denied`，`DROP ROLE owner` 仍成功。

## 后果

- 非超级用户宿主在 PG 17.11 与 17.12 上都能完成租约回收；超级用户路径不受影响
  （`GRANT`/`REVOKE` 对超级用户同样成立）。
- 回归测试 `TestPostgresExtensionDatabaseKernelCleanupWorksForNonSuperCreateRoleHost`
  保留原有严格前提（回收前宿主不得继承扩展 owner），改为调用生产同款 helper，
  并把「不得残留权限」的断言从只查 `pg_has_role(..., 'USAGE')` 扩到同时检查 `USAGE` 与 `SET`。
  机制说明（此前一版写反了，以此为准）：creator 行是 `admin=true, inherit=false, set=false`，
  只有那一行时 `USAGE` 为 false；但 helper 的临时授权是 `INHERIT TRUE, SET TRUE`，
  泄漏时 `USAGE` 与 `SET` 会同时翻成 true —— 反证实测 `usage=true set=true`，
  所以单查 `USAGE` 并不是空断言，它能抓住这次回归。加查 `SET` 是为了另一种将来变体：
  若临时授权改成 `INHERIT FALSE, SET TRUE`，`USAGE` 会漏，而宿主仍可 `SET ROLE` 到扩展 owner。
- 风险与后续：如果将来某条回收路径在 `REASSIGN OWNED` 之外再引入「把对象交给其他 role」的语句，
  同样需要这对 helper；`rg -n "REASSIGN OWNED" apps/api --glob '!*_test.go'` 是当前的全部调用点。
- 同一权限窗口在另外两处同样必要，本次一并修复（reviewer 复核发现）：
  - provisioning：`ensureExtensionDatabaseSchema` 的 `CREATE SCHEMA ... AUTHORIZATION <owner>`
    在 PG 16 起就要求接收者 `SET ROLE`，与 17.12 无关 —— 非超级用户宿主在 17.11/17.12 上同样失败；
    此前该缺口被测试里手工补的 `GRANT ... SET TRUE` 盖住。
  - 卸载：`DROP SCHEMA ... CASCADE` 与 `DROP OWNED BY <owner>` 都要以 owner 身份执行（`INHERIT` 即可），
    而 `DROP ROLE <owner>` 只依赖 creator 行的 `ADMIN`。窗口因此卡在两者之间：acquire 在
    `DROP SCHEMA` 之前，release 在 `DROP ROLE` 之前 —— 否则 REVOKE 会打在已删除的角色上。
  - 回归覆盖：新增 `TestPostgresExtensionDatabaseSchemaProvisioningWorksForNonSuperCreateRoleHost`，
    宿主全程不手工补任何 GRANT；把 provisioning 的 acquire 注释掉即报
    `must be able to SET ROLE ... (SQLSTATE 42501)`。
