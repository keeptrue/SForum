# Decision: 无清单明文恢复通道保留，但改为默认拒绝

## Status

Accepted（2026-11-17，安全审计 31 轮 F-2 判定）。判定已定；**实现待派（老运）** —— 本记录落盘时
生产脚本对无清单载荷仍是静默放行。

## Context

P3-22（`4b4352d48`）给备份加了 encrypt-then-MAC，并在恢复侧封住「清单声明 v2、载荷却是明文」这条
绕过：`deploy/scripts/postgres-backup-format.sh` 的 `sforum_check_payload_integrity` 在 v2 分支直接
`sforum_die`，只留 `SFORUM_RESTORE_ALLOW_UNVERIFIED=1` 一个显式开关。F-2 问的是残留：没有清单的
明文载荷。

先把边界说准。残留的**不是**「`.sql` 后缀」，而是「**无清单 / 清单缺 `encryption=`**」这一整类 ——
后者比前者更省事。

### 证据 A：无清单载荷零校验、零告警

`sforum_check_payload_integrity`（`postgres-backup-format.sh:271`）第二句就是
`[ -f "$manifest" ] || return 0`（`:276`）。清单不存在时它连「这份载荷来自哪里」都不问。

实测（临时目录，直接 `source` `postgres-common.sh` + `postgres-backup-format.sh` 后调
`sforum_detect_payload` / `sforum_prepare_payload`）：

| 输入 | detect | prepare_payload | stderr |
|---|---|---|---|
| `plain.sql` | `sql` | OK | 无 |
| `plain.sql` 改名 `plain.sql.enc` | `sql` | OK | 无 |
| 手搓 `tar.gz`（只含 `database.sql`） | `archive` | OK | 无 |

「改名成 `.enc` 也照样当 SQL」是 `sforum_detect_payload`（`:233-253`）认 magic 不认扩展名的必然
结果 —— 这一条是对的，问题在认完之后没有凭据可查。

### 证据 B：P3-22 的守卫可以用「删一行」绕过

`declared` 落进 `""|"$SFORUM_BACKUP_ENCRYPTION_SCHEME_V1"` 分支（`:279`）时，只对 `kind=encrypted`
打降级告警；`archive` / `sql` 一句都不打。所以把 `manifest.env` 里的 `encryption=` 行删掉，明文
tar.gz 照样通过、无任何 stderr。

实测同一份明文归档（`manifest.env` + `database.sql` 打成 `tar.gz`）：

- 保留 `encryption=openssl-aes-256-cbc-pbkdf2-iter200000+hmac-sha256` → `exit 1`，报「完整性无法
  校验」，并点名 `SFORUM_RESTORE_ALLOW_UNVERIFIED=1`；加该开关后 `exit 0`。**P3-22 如声明般生效。**
- 删掉 `encryption=` 行 → `prepare_payload: OK`，无 stderr。

即 P3-22 关掉的是**降级**（信封被剥离 / 清单还在），没关掉**替换**（清单一起换掉）。这不否证
P3-22：它的威胁模型写的是「有磁盘写权限者最省事的一条绕过」，而攻击者若只改现有归档，清单仍在。
但若把威胁模型读成「有 `backups/` 写权限者」，这个人可以直接写一份没有清单的载荷，P3-22 拦不到。

### 证据 C：脚本不遍历目录挑文件（本条最重要的判定）

**不存在自动扫描。** 三处：

1. `deploy/scripts/restore-postgres.sh:53-57`：`BACKUP_INPUT="$1"`，随后 `[ ! -f "$BACKUP_INPUT" ]`
   → `Backup file not found`。目录不是普通文件，直接拒。
2. 全脚本唯一的 `find` 是 `sforum_sweep_stale_restore_dirs`（`:90`），只清 `$TMPDIR/sforum-restore.*`
   陈旧工作目录，与备份目录无关。
3. `deploy.sh:961` 的 `restore_backup` 是 `read -r backup_file`（`:965`）交互输入，无 glob、无
   `ls -t`、无 `sort -r`；`upgrade.sh` 只调 `backup_database`，从不调恢复。

实测（临时根目录造 `.env.production` + `backups/` 里放两个假文件：旧 `.sql` 与「新」`.enc`）：

```
$ SFORUM_CONFIRM_RESTORE=RESTORE ./deploy/scripts/restore-postgres.sh "$T/backups"
Backup file not found: /tmp/.../backups
exit=1
```

结论：残留通道是**运维显式指定**的，不是脚本自己挑最新文件。它因此不是「静默自动绕过」；它的真实
缺口在别处 —— 运维显式指定之后，脚本对一个不可校验的载荷**保持沉默**，既不告警也不要求确认。

### 证据 D：明文 `.sql` 是真实历史格式，不是测试遗留

`git show 5889cd690^:deploy/scripts/backup-postgres.sh` 里旧备份写的是
`$BACKUP_DIR/$POSTGRES_DB-$STAMP.sql`：2026-11-15 归档格式落地之前，生产备份**就是**备份目录下的
明文全库转储。两件事随之成立：

- 老安装升级后，`backups/` 里可能真躺着必须能恢复的明文 `.sql`；
- retention（`sforum_apply_retention`，`postgres-backup-format.sh:647`）只按 `sforum-backup-*` 前缀
  清理，**这些历史明文转储永远不会被自动删掉**（顺带发现，见「需要谁做什么」第 4 条）。

## Decision

**保留通道，改为默认拒绝。**

通道有真实需求（证据 D），且不能被「先打包再加密」替代：打包路径要求现场具备完整 compose 栈、
备份密钥文件与脚本自身，而灾难恢复现场往往只剩一个 dump 文件。把它封死等于在 DR 场景里先要求
运维重建一个健康实例。但「保留」不等于「沉默放行」：一个无法校验来源的载荷经
`psql --single-transaction` 进入目标库是一次不可逆的全库替换，它必须是一次**显式**决定。

口径（与 P3-22 同风格，复用同一个开关，不新增变量）：

1. `sforum_prepare_payload` 在 `kind=sql`、或 `kind=archive|sql` 而清单缺失时默认 `sforum_die`，
   文案点名 `SFORUM_RESTORE_ALLOW_UNVERIFIED=1`。**无清单的 `envelope` / `encrypted` 仍然放行，
   这不是绕过**：`encrypted` 在解包之前必须过 `sforum_require_backup_key` 解密，`envelope` 还要
   先验 HMAC 再解密；没有密钥的人伪造不出可解密的载荷，所以「没有清单」对这两类不构成免校验通道。
   封死它只会误伤合法的 v1 无清单归档（早期手工 `openssl enc` 产物），在 DR 现场多一道没有收益的墙。
2. 清单存在但缺 `encryption=` 行同样默认拒绝。仓库产出的每一份归档都写了这一行
   （`sforum_write_manifest` 自格式落地起就 `printf 'encryption=%s\n'`，
   `git show 5889cd690:deploy/scripts/postgres-backup-format.sh` 可证），所以「缺这行」只可能是
   手搓或篡改。
3. `SFORUM_RESTORE_ALLOW_UNVERIFIED=1` 时打印警告，内容含备份路径与「该载荷无完整性凭据，恢复后
   无法证明来源」。
4. v1 归档不受影响：它带非空 `encryption=`（v1 方案串）且 `kind=encrypted`，仍按现有降级告警继续。

不做的部分：不要求恢复源必须在 `backups/` 内（路径限制不增加安全性，运维本来就只能指定一个已存在
的文件），不新增环境变量，不引入审计表。

## 需要谁做什么

老运（`deploy/` 归属）：

1. `deploy/scripts/postgres-backup-format.sh` 的 `sforum_check_payload_integrity`：把早退改成按
   `kind` 分流 —— `envelope`/`encrypted` 之外的 kind 在无清单时走 `sforum_die`，受
   `SFORUM_RESTORE_ALLOW_UNVERIFIED=1` 放行。**注意 `kind=sql` 时 `$out_dir/manifest.env` 永远
   不存在**（`sql` 分支只 `cp` 不解包），所以这一条对 `.sql` 输入实际等于「一律默认拒绝」；
   清单可缺可有的只有 `kind=archive`。
2. 同文件 `:279` 的 `""|v1` 分支：把「空」与「v1 方案串」拆成两个 case；`declared` 为空且
   `kind=archive|sql` 时 fail closed，v1 方案串保持现行为。
3. `deploy/scripts/postgres-safety_test.sh`：
   - C13（`:985-1001`，`legacy .sql` 成功用例）补 `SFORUM_RESTORE_ALLOW_UNVERIFIED=1`；
   - 新增 C13b：同一份 `legacy.sql` **不带**开关 → 必须失败，报错含开关名，且 `MOCK_LOG` 里没有
     `exec restore` / `exec swap`；
   - 新增 C13c：手搓无清单 `tar.gz` 同上（现有 C6e2 在 `:658-685` 只覆盖「清单还在」那一类）。
4. 顺带（同一次改动，一行）：`sforum_apply_retention` 的 `find` 只匹配 `sforum-backup-*`，历史明文
   `<db>-<ts>.sql` 永不被清理。建议**不**自动删（删运维的历史备份比留着更危险），改在
   `docs/zh-CN/deployment/one-click.md` 的 `backups/` 说明里写明「历史明文转储需人工清理」。

文档（同批）：`docs/zh-CN/deployment/one-click.md` 与 `docs/en-US/deployment/one-click.md` 的
`backup` / `restore` 两行补一句「恢复源必须由运维显式指定；无完整性凭据的载荷需
`SFORUM_RESTORE_ALLOW_UNVERIFIED=1`，脚本不会遍历备份目录自动挑选」。

## Consequences

- **F-2 的关闭条件**（台账原文：「决策落 `knowledge/decisions/` 且实现与之一致」）在本记录落盘后
  只差第 1–3 条实现；实现不合入，F-2 仍算未闭。
- **旧安装升级后的第一次恢复会多一步**。这是有意的：让运维在不可逆操作前读一遍「这份载荷没有凭据」。
- **P3-22 的注释口径要跟着改准**。现在那句只写「有磁盘写权限者最省事的一条绕过」被堵住，实际堵的
  是「改动现有归档」，不是「替换成自己的载荷」。实现落地时把注释改成准确表述，否则下一个人会以为
  这条通道已经关了。
- **行号会漂**。本记录行号取样于 `6f577659e`；判据一律以「文件 + 符号名」为准，行号只作导航。
