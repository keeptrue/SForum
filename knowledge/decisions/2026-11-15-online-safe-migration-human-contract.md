# Decision: `OnlineSafe` 是人工兼容契约，自动侧只校验声明形态

## Status

Accepted（2026-11-15，审计 OPS2-13）

## Context

零停机升级允许「旧 API 仍在服务」的窗口内跑完 Core 迁移。这个窗口是否安全，取决于
**新 schema 是否真的对旧二进制向后兼容** —— 列语义、约束对旧写入路径的影响、视图/函数
签名、枚举取值、回填锁，全部是语义问题。

自动侧能证明的只有声明形态：`database/migrator/online_migrations.go` 的
`migrationDeclaresOnlineSafe` 会读 Up 段（到 `-- +goose Down` 为止），要求

- 出现 `-- +sforum OnlineSafe`；
- Up 段不含 `-- +goose NO TRANSACTION`（必须事务性）；
- Up 段含 `SET LOCAL lock_timeout` 与 `SET LOCAL statement_timeout`（必须有界）。

`CheckOnlineSafe` 另外要求 River 迁移版本与库内完全一致，并且待跑迁移必须有可读的 SQL 源。
除此之外**没有任何兼容性校验**：声明一条 `DROP COLUMN` 的迁移同样能通过上述检查。

审计 OPS2-13 的结论是：零停机窗口的安全性完全建立在人工注释之上。本轮不引入运行时兼容
校验器 —— 静态 SQL 分析判不准（假阴性会挡住合法发布，假阳性会给出虚假安全感），成本也
远超收益。取而代之：把「这是人工契约」写成显式决定，并给出一份提交前必查清单，让评审有
可执行的判据，而不是靠直觉。

## Decision

1. **`-- +sforum OnlineSafe` 是作者对兼容性的书面承诺，不是机器推导的结论。** 加这一行
   等于声明：本次迁移跑完后，当前在服务的旧二进制与目标二进制都能正常工作，且候选启动
   失败或路由回滚时旧槽仍可服务。发布评审审的就是这句承诺。
2. **提交前必查清单**（缺一条就不要声明 `OnlineSafe`，改走 `deploy.sh` 维护窗口）：

   1. **只做扩张**：只新增表、新增可空列、新增带默认值的新列、新增索引/约束（`CREATE INDEX
      CONCURRENTLY` 除外 —— 它不能进事务，与「必须事务性」冲突）、`CREATE OR REPLACE VIEW`
      保持既有列名/类型/顺序不变。改名、删列、收窄类型、加 `NOT NULL` 而不给默认值，一律
      不算 OnlineSafe。
   2. **旧代码的写入仍然合法**：新加的 `CHECK` / `NOT NULL` / 唯一约束 / 外键不能拒掉旧二进制
      当前会写出的行；`CREATE OR REPLACE VIEW` 不得减少列或改变列类型。
   3. **不改共享签名**：函数/触发器/视图的签名与返回列集合不变（旧代码按旧签名调用）。新增
      函数、或新增带默认参数的重载可以。
   4. **回填与索引有界**：数据回填必须分批或有界（`WHERE` 命中行数、`UPDATE ... LIMIT` 循环），
      且显式写 `SET LOCAL lock_timeout` / `SET LOCAL statement_timeout`；大表上的
      `ALTER TABLE ... ADD COLUMN ... DEFAULT` 在 PG 11+ 是元数据操作，可以，但
      `ADD CONSTRAINT ... CHECK` 默认会全表扫描，需要 `NOT VALID` + 后续 `VALIDATE`。
   5. **回滚窗口内可逆**：本次发布若回滚到旧槽，新 schema 必须仍能被旧代码读写；「先扩后缩」
      的收缩步骤（删列、删索引、收紧约束）必须放到旧二进制退出回滚窗口之后的维护窗口里。

3. **清单不是自动门禁。** 违反清单的迁移可以存在，但只能不声明 `OnlineSafe`，由
   `deploy.sh` 的维护窗口路径执行；`CheckOnlineSafe` 会 fail closed 到 `ErrOnlineMigrationUnsafe`，
   不会让它在蓝绿窗口里静默跑掉。

## Consequences

- 声明与实现之间没有自动一致性校验，评审必须真的读 SQL。清单把「读什么」固定下来，但无法
  替代人的判断。
- 未来若要加自动校验，可行方向是窄的形态检查（禁止 `DROP`/`ALTER COLUMN ... TYPE`、
  要求 `CHECK` 带 `NOT VALID`、要求超时语句存在且数值有上限），而不是「兼容性证明」。
  这类检查应该落成 `migrationDeclaresOnlineSafe` 的增量规则，不新增独立工具。
- **已落地其一（2026-11-17，G3）**：收缩型 DDL 的静态棘轮。`database/migrator/
  online_migrations_shrink.go` 在 `classifyOnlineSafeForm` 里加了一条增量规则 ——
  声明了 `OnlineSafe` 却在 Up 段出现 `DROP COLUMN` / `RENAME COLUMN` /
  `ALTER COLUMN ... TYPE` 时，必须逐条登记在 `registeredShrinkDDL`（迁移名 + 语句原文 +
  中文理由），否则形态判为 malformed，`migrate --check-online-safe-form` 以退出码 3 失败。
  匹配只发生在代码文本上（注释、单引号字符串、美元引用块被清空），所以说明文字与函数体
  里的字样不会误报；未声明 `OnlineSafe` 的迁移不参与这条规则（它们本来就走停机窗口）。
  当前登记表为空 —— 嵌入式迁移集里 36 条声明迁移的 Up 段全部只做扩张，这一点由
  `TestEmbeddedOnlineSafeFormsAreWellFormed` 与 `TestRegisteredShrinkDDLEntriesAreWellFormed`
  持续钉住。**这仍然不是兼容性证明**：它只回答「有没有人声明的收缩语句没人复核」，
  清单第 2/3/4 条（旧写入合法性、签名不变、回填有界）依旧靠人工评审。
- 与 `2026-08-02-declared-online-core-migrations.md` 的关系：那份记录决定「要不要有声明」，
  这份记录决定「声明之后靠什么保证」。两者不重复。
