# 开发者 CLI（`sforum`）

[← 开发指南](./README.md)

`sforum` 是 SForum 的开发者控制台（类似 Laravel Artisan 的定位），源码在
`apps/api/cmd/sforum`。用于脚手架、扩展校验、精确摘要、打包、契约测试、
带外恢复、账号/数据维护与假数据种子。

所有命令默认从 `apps/api` 目录运行：

```sh
cd apps/api
go run ./cmd/sforum --help
go run ./cmd/sforum --version
```

## 命令总览

| 命令 | 作用 |
| --- | --- |
| `version` | 打印构建信息（版本、提交、构建时间） |
| `make:plugin` | 生成插件脚手架 |
| `make:theme` | 生成主题脚手架 |
| `seed:forum` | 批量写入论坛假数据（small 模式） |
| `seed:perf` | 百万级读路径种子（`seed:forum --profile=perf-1m` 的别名） |
| `users:reset-password` | 重置用户密码（默认交互式；`--email/--password/--yes` 非交互，别名 `user:reset-password`） |
| `privacy:erase-user` | 擦除一个用户的隐私数据（`privacy.Registry.EraseUser` 的生产入口；`--actor-user-id` 须为 super_admin / `user.manage`；`--user-id/--user-email` 二选一，自动化须 `--yes`） |
| `revisions backfill` | 分批回填论坛内容修订台账 |
| `extension build` | 一键构建作者前端、刷新摘要并执行全部包门禁 |
| `extension validate` | 校验扩展包（含 includes / 模板预检） |
| `extension digest` | 查看或刷新 Manifest V3 `packageFiles` 摘要 |
| `extension test` | Host 契约检查（能力、事件、入口等） |
| `extension package` | 打 zip + SBOM stub |
| `extension brand check` | 校验品牌资产（brand/icon.svg、brand/icon.png、brand/social-preview.png 的尺寸与字节上限） |
| `extension docs generate` | 从 Go catalog 生成 host 文档 |
| `extension list` | 带外查看扩展恢复状态（不启插件代码） |
| `extension disable` / `disable-all` | 带外禁用第三方扩展 |
| `extension quarantine` | 带外隔离精确的内置/系统制品 |
| `extension recovery lifecycle-close` | 带外把一条开放 lifecycle operation 推进到 cancelled 终态（审计落库） |
| `extension recovery activation-state` | 把中断的 activation 发布恢复到精确源制品（只按 durable 证据修复，条件漂移即 no-op） |
| `extension recovery recheck` | 重建期望的插件运行时全集并与最新发布精确比对，一致时推进 `reason=recovery` 修订（不热恢复） |
| `recovery` | 顶层带外恢复入口，与 `extension recovery` 共享同一套子命令 |
| `extension gc` | 版本垃圾回收：默认 dry-run 清单 + 一次性 token 执行（先盘后账、审计落库） |
| `extension command list/run` | 列出 / 运行已信任插件命令 |
| `extension api-lts` | 打印 Host/Frontend API LTS 与 shim 遥测 |
| `extension system-tier list/upsert/disable` | 带外管理系统扩展层（不加载包代码） |
| `extension dev` | 开发模式编辑循环：监视包内源输入并自动重建（仅 `APP_ENV=development`） |
| `extension builtins` | 从清单树发现内置包清单（构建脚本的唯一来源） |
| `dev:cleanup-orphan-plugins` | 停止已改属的扩展后端进程（安全用于运行中的 sforum-api） |
| `ai:eval` | 在固定 fixture 上跑一次真实 AI 回复生成，产出可 diff 的基线快照（`--diff`/`--only`/`--confirm`；不发布评论） |
| `ai:reply` | 用真实帖子与真实配置跑一次回复生成并打印全部决定事实（`--topic-id`、`--trigger-comment-id`、`--confirm`；禁用结果缓存、不发布评论、不写结果表） |
| `ai:derived` | 手动触发 AI 派生内容（`run --topic-id --purpose digest\|tour`，产物落 `ai_derived_artifacts` 草稿；`list` 查看） |

> 本表是命令的**权威清单**；新增或删除命令时必须同步更新本表与
> `docs/en-US/development/cli.md`（`tests/validate-docs.mjs` 会校验覆盖）。

---

## 构建信息：`version`

```sh
go run ./cmd/sforum version
go run ./cmd/sforum --version
```

输出统一构建身份：SForum 版本、Git 提交、构建时间（发布镜像与 CLI 使用同一
构建参数注入）。

---

## 脚手架：`make:plugin` / `make:theme`

交互式（会提问 ID、名称、是否带后端等）：

```sh
go run ./cmd/sforum make:plugin
go run ./cmd/sforum make:theme
```

非交互示例：

```sh
# 本地实验（默认 → extensions/dev/，gitignored，不进后台列表）
go run ./cmd/sforum make:plugin \
  --id acme.demo \
  --name "Acme Demo" \
  --description "示例插件" \
  --backend \
  --no-interaction

# 受保护内置包（→ extensions/builtin/，SyncBuiltins 会扫入后台）
go run ./cmd/sforum make:plugin \
  --id sforum.foo \
  --name "Foo" \
  --description "…" \
  --backend \
  --builtin \
  --no-interaction

# 指定输出目录
go run ./cmd/sforum make:plugin \
  --id acme.demo --name "Acme Demo" --description "…" \
  --backend --no-interaction --out /tmp/acme.demo
```

### 常用 flags

| Flag | 适用 | 说明 |
| --- | --- | --- |
| `--id` | 两者 | 稳定扩展 ID，如 `acme.demo` |
| `--name` / `--description` | 两者 | 展示名与简介 |
| `--url` / `--author-*` | 两者 | 官网与作者信息 |
| `--out` | 两者 | 输出目录；省略则按 dev/builtin 规则落盘 |
| `--builtin` | 两者 | 写到 `extensions/builtin/` 而非 `dev/` |
| `--no-interaction` | 两者 | 关闭交互提问 |
| `--backend` | plugin | 生成 `backend/plugin` stub 与 README |
| `--complex` | plugin | 多文件 manifest（includes + langs + settings 分片） |
| `--prebuilt-settings` | 两者 | 预构建 Admin 设置组件 + Schema 回退 |
| `--vue-admin-page` | plugin | Vue/Vite 后台页面 + Plugin UI SDK 工作区 |
| `--provider-slot` | plugin | 声明 provider slot + `provider_probe`（需 `--backend`） |
| `--public-page` | 两者 | 生成一个公开 Page Registry 页（路径 `/<slug>`、L1 模板、精确声明）。slug 为小写字母数字与 `-` |
| `--public-page-layout` | 两者 | 公开页 layout：`site`（默认，主题拥有站点外壳）或 `bare`（页面自持 chrome） |
| `--schema-page` | 两者 | 生成一个声明式公开页（路径 `/<slug>`、`view: schema`、包内有界描述符 + JSON Schema 契约）。slug 规则同 `--public-page` |
| `--schema-page-kind` | 两者 | 描述符种类：`list`（默认）| `detail` | `form` | `notice`（需 `--schema-page`） |
| `--route-middleware` | plugin | 生成 before 请求补丁 + after 响应补丁路由（需 `--backend`），guard 固定为 `core.guard.inherit` |

`--public-page` 会写入 `theme.json` 的页面映射，并在清单里同时补上 `templates[]` 与
`packageFiles[]`（三方身份契约，摘要一致）。生成的是**静态**页：插件页数据源
（`dataSource`/`dataRoute`）当前没有可用传输，Loader Gateway 会回退，所以脚手架不生成它。
`--schema-page` 生成**声明式**公开页：`theme.json` 只给出描述符文件 id 与它必须满足的精确
schema 引用（`<schemaFileId>@1`），清单里两者都是 `kind: schema` 且带 `version`。正文由宿主渲染，
包内没有 Vue、没有 Go 模板、也没有构建步骤。宿主执行有界规则：`kind` 只能是
`list`/`detail`/`form`/`notice`，字段类型限 `text`/`textarea`/`number`/`boolean`/`select`，
字段 ≤ 24、条目 ≤ 64、选项 ≤ 64、cells ≤ 64。`form` 目前只读渲染：提交需要页面声明的
`dataRoute` 绑定，而该绑定仍受 loader 传输缺口阻塞（与 `dataSource` 同一阻塞项）。
与 `--public-page` 共用同一 slug 会被直接拒绝，避免同一路径两个声明。

`--route-middleware` 只声明清单里列出的可变字段，绝不请求 raw request authority，
也不允许声明 `access`/`permission`——补丁继承目标路由的授权。

### 脚手架之后

1. 实现后端逻辑，并把可执行文件编译到 Manifest 声明的 `backend.entry`（通常是 `backend/plugin`）。
2. 有 `frontend/admin/package.json` 时运行 `extension build`；它会构建前端、刷新摘要并执行全部包门禁。
3. 没有作者前端时，直接运行 `extension digest --write`、`extension validate` 与 `extension test`。
4. 需要分发时再 `extension package`。

第三方插件请走公开 SDK（`apps/api/sdk/plugin`），**不要** import `app/Models/*` 等宿主业务包。完整编写约定见 [插件编写指南](../../extensions/authoring-guide.md)。

---

## 扩展包工具：`extension …`

### 一键作者构建 — `build`

```sh
go run ./cmd/sforum extension build <package-root>
go run ./cmd/sforum extension build --allow-scaffold <package-root>
go run ./cmd/sforum extension build --skip-install <package-root>
go run ./cmd/sforum extension build --sdk-dir apps/web/packages <package-root>
```

当包内存在 `frontend/admin/package.json` 时，命令会先运行 `bun install` 和
`bun run build`；存在 `bun.lock` 或 `bun.lockb` 时安装会自动加
`--frozen-lockfile`。之后依次刷新精确摘要、完整校验 Manifest 与模板，并执行
Host 契约测试。没有作者前端时会跳过 Bun，仍执行后三项门禁。

预检只对「会进入发布制品的内容」计算摘要：`node_modules`、`.git`、`vendor`
与 `extension package` 的排除规则一致，因此作者本地装过依赖后照样能通过校验。
安装路径仍使用严格的整树摘要并拒绝任何符号链接。

- `--skip-install`：依赖已经安装时跳过 `bun install`，但仍执行构建。
- `--allow-scaffold`：只允许契约测试暂时缺少后端二进制，其他错误仍会失败。
- `--sdk-dir <dir>`（或环境变量 `SFORUM_SDK_DIR`）：**仅开发期**能力。把工作区里
  声明的 `@sforum/*` 依赖改写成指向该目录下的本地 SDK 包再构建，用于在 Release
  发布新 SDK 版本之前先行验证（例如 `@sforum/plugin-ui` 的新组件）。构建结束后
  `frontend/admin/package.json` 与 Bun 锁文件都会逐字节还原；构建失败或进程被强杀时
  会留下 `package.json.sforum-sdk-backup` 供手工恢复。发布与生产流程永远不使用它。

这是插件作者主动运行的本地命令，会执行插件自己的 `package.json` scripts。
上传、安装、启用和生产运行时都不会调用它；生产仍只加载 Manifest 精确绑定的
`.mjs` / `.css` 成品。

### 校验 — `validate`

```sh
go run ./cmd/sforum extension validate <package-root>
go run ./cmd/sforum extension validate <package-root> --json   # 打印合并后的 Manifest
```

加载包、解析 `includes`、校验 Manifest V3，并对显式 V3 包做页面模板运行时预检。

Page Registry 主题采用失败即关闭的三方一致性规则：每个
`theme.json.pages[].template` 路径都必须对应唯一的 Manifest V3
`templates[]` 声明，以及一个 `kind: "template"` 的 `packageFiles[]`
条目；路径与 SHA-256 摘要必须一致。`theme.json` 只定义页面映射，不能替代
精确制品声明。缺失或摘要过期会使校验失败，并在激活时返回
`extension.build_failed`。

### 精确摘要 — `digest`

Manifest V3 用 `packageFiles` 的 SHA-256 绑定可执行文件、前端、迁移等。**改过包内文件后必须刷新**：

```sh
go run ./cmd/sforum extension digest <package-root>           # 只检查
go run ./cmd/sforum extension digest --write <package-root>   # 写回根 manifest 并再校验
```

主题新增模板时，应先补齐 `templates[]` 的身份、契约和 ViewModel 声明，以及
对应的 `packageFiles[]` 文件条目，再运行 `digest --write`。该命令会刷新
`packageFiles[]` 及已声明的内联模板摘要，但不会从 `theme.json` 推断模板身份
或文件成员关系。

### 契约测试 — `test`

```sh
go run ./cmd/sforum extension test <package-root>
go run ./cmd/sforum extension test --allow-scaffold <package-root>  # 脚手架阶段可不要求后端二进制
go run ./cmd/sforum extension test --skip-backend-binary <package-root>
go run ./cmd/sforum extension test --json <package-root>
```

对照 host catalog 检查能力、事件、贡献点、provider、job、后端入口等。
`--allow-scaffold` 是 `--skip-backend-binary` 的别名。

内置主题修改后，从 `apps/api` 依次运行：

```sh
go run ./cmd/sforum extension digest --write ../../extensions/builtin/themes/<dir>
go run ./cmd/sforum extension validate ../../extensions/builtin/themes/<dir>
go run ./cmd/sforum extension test ../../extensions/builtin/themes/<dir>
```

然后在仓库根目录运行 `./scripts/build-builtin-plugins.sh`，重启 API 让
`SyncBuiltins` 暂存新摘要，并通过管理后台激活该版本。不要直接修改
`storage/builtin-dev/` 或 `storage/extensions/**`。

### 打包 — `package`

把扩展根目录打成 zip，并生成 SBOM stub。

```sh
# 默认：根目录几乎全部文件都进 zip
go run ./cmd/sforum extension package <package-root>

# 发布包：跳过常见源码 / 开发文件
go run ./cmd/sforum extension package <package-root> --exclude-source

# 指定输出路径
go run ./cmd/sforum extension package <package-root> \
  --exclude-source \
  -o /tmp/acme.demo.sforum.zip
```

| 行为 | 说明 |
| --- | --- |
| 默认包含 | 包根下几乎所有文件 |
| 始终跳过 | `.git/`、`node_modules/`、`vendor/`、已有 `*.sforum.zip` |
| `--exclude-source` 额外跳过 | `*.go`、`go.mod`/`go.sum`、`*.vue`/`*.ts`/`*.tsx`、Sass、source map、`package.json`/`tsconfig` 等配置、`testdata/` / `__tests__/` 等 |
| 发布时通常保留 | `sforum.extension.json`、manifest 分片、`backend/plugin`、预构建 `.mjs`/`.css`、`README.md` |
| 默认输出 | `<package-root>/<目录名>.sforum.zip` + 旁路 `.sbom.json` |

打包前会先做 package validation。输出示例：

```text
package	…/acme.demo.sforum.zip
digest	…
sbom	…/acme.demo.sforum.zip.sbom.json
files	12
skipped	8	(source/dev files)   # 仅 --exclude-source 且有跳过时
```

**重要：**

- 运行时安装**不需要**源码，但默认 `package` **不会自动剥源码**；分发请加 `--exclude-source`，或先拷到干净的 release 目录再打包。
- `--exclude-source` 是启发式过滤，不是「只打 `packageFiles` 清单」。
- 上传 zip 只做惰性校验与存储；首次启用可执行逻辑需要基于精确摘要的信任确认。运营侧说明见 [扩展与主题](../usage/extensions.md)。

推荐发布循环：

```sh
# 1. 编译后端到 backend/plugin
# 2. 构建前端（如有）+ 刷新摘要 + 校验 + 契约测试
go run ./cmd/sforum extension build <package-root>
# 3. 打发布 zip
go run ./cmd/sforum extension package <package-root> --exclude-source -o /tmp/my-plugin.sforum.zip
```

### Host 文档 — `docs generate`

Host 表面（事件、能力、contribution、provider slot、schedule 等）变更后：

```sh
go run ./cmd/sforum extension docs generate
go run ./cmd/sforum extension docs generate --check   # CI：与已提交文档漂移则失败
```

默认写入 `docs/extensions/catalogs/`（可用 `--out` 覆盖）。

### 插件命令 — `command`

```sh
go run ./cmd/sforum extension command list
go run ./cmd/sforum extension command run <command-id>
go run ./cmd/sforum extension command run <command-id> --input '{"key":"value"}'
go run ./cmd/sforum extension command run <command-id> --input-file /tmp/input.json
```

需要可用的 `DATABASE_URL`（或 `--database-url`）。可加 `--safe-mode`
（在 `SFORUM_SAFE_MODE` 之外强制 Safe Mode）。`list --json` 输出机器可读结果。

### 带外恢复

不启动 SForum 主进程、不执行插件代码：

```sh
go run ./cmd/sforum extension list
go run ./cmd/sforum extension list --json
go run ./cmd/sforum extension disable <extension-id>
go run ./cmd/sforum extension disable-all
go run ./cmd/sforum extension quarantine <extension-id> \
  --expect-version <精确版本> \
  --expect-digest <64位hex摘要>
```

`quarantine` 只隔离**精确匹配**的内置/系统制品：`--expect-version` 与
`--expect-digest` 都必须与实际活动版本一致，避免误隔离。

`extension recovery lifecycle-close --operation-id <id> --reason "<原因>"`
把一条 `completed_at IS NULL` 的 lifecycle operation 在单事务内推进到
`cancelled` 终态（开放 step 置为 failed、写 `extension.cli_recovery`
审计）。它 fail-closed：仍有未过期 step lease 的操作会被拒绝，除非显式加
`--force`（运维明确负责）。适用于执行进程在操作中途崩溃、durable 行永远
无人推进终态的场景；关闭后重启 API 恢复正常收敛。启动路径另有自动回收
：所有 step lease 均已过期的孤儿操作会在 genesis 重试与 coordinator 轮询
时被宿主推进到 cancelled（`extension.lifecycle_orphan_reclaim` 审计），
无需人工干预。

`recovery` 是同一套带外恢复子命令的顶层入口：`sforum recovery <subcommand>`
与 `sforum extension recovery <subcommand>` 完全等价，事故现场可以少敲一段。
两条路径都只读 durable 行，不启动主进程、不加载插件代码。

`extension recovery activation-state` 处理「state 事务已提交 target、
publication 未提交」的中断窗口：它和 API 启动修复调用同一个 collaborator，
只按 durable 证据把发布恢复到精确源制品，条件一旦漂移就 no-op。`--limit`
限制单次检查的候选数量（0 表示默认），`--json` 输出恢复明细（operation、
扩展、state publication、from/to 版本与审计事件 ID）。

`extension recovery recheck --reason "<原因>"` 是 recovery-only 的显式复核
通道：从 durable 行重建期望的插件运行时全集，与最新不可变发布精确比对；完全
一致时推进一条 `reason=recovery` 的不可变 revision，不一致时原样列出每个不符
制品并非零退出，绝不「尽力修」。它只把「该重新收敛了」写成 durable 事实，
**不热恢复**：运行中的进程仍是 recovery-only，生效点在进程下一次启动。
`--reason` 必填并落审计（≤4096 字节）。

### 版本垃圾回收 — `gc`

```bash
# 默认 dry-run：打印候选与保留/阻塞原因，并签发一次性确认 token（15 分钟有效）
go run ./cmd/sforum extension gc --json
# 执行（token 与 dry-run 的精确计划绑定；必须重复相同的策略选项）
go run ./cmd/sforum extension gc --confirm-token <dry-run 输出的 token>
# 策略旋钮：每扩展保留的历史版本数 / 按安装时间保留 / 限定扩展
go run ./cmd/sforum extension gc --retain 3 --retain-days 30 --extension-id <id> --json
```

`extension gc` 回收扩展的**历史版本行与对应磁盘目录**（默认策略：每扩展保留
active + 1 个最近历史版本；`--retain` / `--retain-days` 可调）。fail-closed
三道防线：候选必须同时满足"非 active/staged、无开放 lifecycle operation、
无发布历史引用、超出保留窗口、磁盘存在"；执行先删盘（原子改名暂存）后删
台账，任一步失败该条回滚并恢复现场；被 append-only 发布账本
（`plugin_runtime_publication_members`）引用的版本行在 SQL 层永久不可删
（`blocked=publication_history`），因此**曾被启用的插件版本永久保留**——
GC 的实际回收面是主题历史版本与从未进入发布的插件版本。
`blocked=disk_missing` 的行不在 GC 语义内，请走管理端"缺失制品清理"。
token 一次性且与计划摘要绑定：dry-run 与执行之间判据/清单/选项漂移即失效。
一切删除写 `extension.version_gc` 审计并留 `extension_version_gc_removals`
锚点。判据与边界详见
[版本 GC 运维参考](../../zh-CN/extensions/version-gc.md) 与决策记录
`knowledge/decisions/2026-10-17-version-gc-publication-restrict-cooperation.md`。

### 开发编辑循环 — `dev`

```bash
cd apps/api
APP_ENV=development go run ./cmd/sforum extension dev ../../extensions/dev/acme.demo
```

只允许在 `APP_ENV=development` 下运行；生产、预发与测试环境在入口即拒绝（与
`dev:cleanup-orphan-plugins` 的孤儿回收器用同一条判据）。包必须位于
`extensions/dev`、`extensions/optional`、`extensions/fixtures` 或
`EXTERNAL_EXTENSION_ROOTS` 配置的外部插件仓库根（含子目录）之下——独立插件仓库
无需拷进主程序目录；受保护的 `extensions/builtin` 源码树被拒绝，因为内置包必须走
`scripts/build-builtin-plugins.sh` 与管理员激活流程。

循环只监视**包内源输入**：`dist/`、`node_modules/`、`.git/`、归档、编辑器交换文件与
临时文件都会被忽略；构建事务自身重写的清单也不算作者编辑（靠内容戳快照区分），
否则循环会自己触发自己。

| flag | 作用 |
| --- | --- |
| `--once` | 只跑一次构建事务后退出（无监视器，适合 CI） |
| `--json` | 每个阶段输出一行 JSON，供编辑器集成消费 |
| `--debounce` | 事件合并窗口（默认 `400ms`） |
| `--skip-install` | 跳过 `bun install`，复用已有的前端依赖 |
| `--auto-trust` | 每轮构建成功后自动调用 dev trust 会话续信任（见下节） |
| `--auto-upgrade` | 信任续期成功后继续调用既有 upgrade 路由，把 staged 候选升为活动版本（隐含 `--auto-trust`）。配合 dev 会话即可做到「保存 → 自动构建 → 自动暂存 → 自动信任 → 自动升级」的保存即生效链；扩展处于 disabled 时该轮报 `auto_upgrade_failed`，在管理端启用一次后恢复 |

**它不绕过信任。** 每轮只打印新的包摘要与下一步提示：精确制品仍要在管理端扩展流程里
由操作员确认后才会暂存与激活。构建或预检失败时保留最后一个健康制品，并打印失败阶段与原因。

### 开发信任会话 — `dev-trust`（仅 `APP_ENV=development`）

每个开发会话只需 super_admin 在管理端确认**一次**，之后同一 watch 循环内每个新摘要
由后端自动写入 exact live grant（逐条审计，可随时撤销）。生产环境没有 dev 包根、
对应路由 fail-closed，信任模型一行不改。

```bash
cd apps/api
APP_ENV=development go run ./cmd/sforum extension dev-trust create ../../extensions/dev/acme.demo
APP_ENV=development go run ./cmd/sforum extension dev-trust list
APP_ENV=development go run ./cmd/sforum extension dev-trust revoke <sessionID>
```

- `create`：为开发源码根下的包建立会话（默认 TTL 8 小时，一个包同时最多一个活跃会话）；
  本身就是那次人工确认，请求必须显式携带 `confirm: true`。
  允许的源码根 = `EXTERNAL_EXTENSION_ROOTS` 配置的外部插件仓库（含子目录）∪
  脚手架默认 `extensions/dev` 树；服务端会读取该目录下的 manifest 验证扩展 ID
  一致后才建立会话，因此外部仓库无需（也不能用）symlink。
  会话唯一键是扩展 ID——源码根与落库快照路径解耦，`SyncExternalSources`
  暂存（staged）的新摘要同样会被自动授权。
- `--ensure`：幂等确认——服务端返回 `409 extension.dev_trust_session_exists`
  （已有活跃会话）时视为成功。

> **遗留说明**：本节描述的 CLI dev 循环（`extension dev --auto-trust/--auto-upgrade`）
> 与 `dev-trust` 是**带凭据的旧路径**。日常插件开发已改为**零命令 host 通道**——
> 开发装配下 host 自己监视 `EXTERNAL_EXTENSION_ROOTS`，保存文件即自动
> 构建→暂存→授予→升级，无需登录/凭据/会话，也无需 wrapper 脚本（旧的
> `scripts/dev-plugin-loop.sh` 已删除）。详见
> `docs/zh-CN/extensions/build-and-load.md` 的「零命令 dev live-reload」。
- 带 `--auto-trust` 的 `extension dev`：每轮构建成功后调用
  `POST /admin/extensions/{id}/dev-trust-refresh`，输出 `dev_trusted digest=…`；
  无会话/凭据时回落 `reload_required`，与人工路径一致。开发装配下该端点会**先重扫
  `EXTERNAL_EXTENSION_ROOTS` 外部源**（`SyncExternalSources`），把刚构建出的新摘要
  直接暂存进运行中的 API——不再需要为看到新制品而重启 dev API。
- 带 `--auto-upgrade` 的 `extension dev`：信任续期成功后自动调用既有
  `POST /admin/extensions/{id}/upgrade`（exact-artifact 信任授予已就位），staged 候选
  升为活动版本，插件后端子进程按新摘要重启。失败输出 `auto_upgrade_failed` 与原因，
  循环不中断；保存即生效链因此完整：保存 → 构建/摘要/校验/测试 → 暂存 → 信任 → 升级。
- 凭据解析优先级：`SFORUM_SESSION_COOKIE` / `SFORUM_DEV_AGENT_PAT` →
  **agent 凭据缓存**（`$XDG_CACHE_HOME/sforum/dev/agent-credentials.json`，
  0600，可用 `SFORUM_DEV_AGENT_CACHE` 覆盖；不存密码）→
  `SFORUM_ADMIN_EMAIL`+`SFORUM_ADMIN_PASSWORD`（与
  `tests/admin-extension-activation.sh` 同一约定）登录并自动入缓存 →
  交互终端里一次性提示输入（密码不回显）并自动入缓存。
- 首次在他处（如 agent 会话）运行前，操作者在终端里跑一次
  `extension dev-trust create --ensure <包根>` 完成提示登录（或直接跑
  dev 循环），此后所有 wrapper / agent 会话零凭据、零交互。
- 两种可撤销形态：PAT（`{"kind":"pat",...}`，管理端 api_token 审计可凭证撤销）
  或会话对（`{"kind":"session",...}`，管理端会话页可撤销/登出即失效）。
  缓存文件只属于本机 dev 环境；生产从不读它（dev trust 服务生产不注入）。
- Safe Mode 启动时全部会话立即吊销；Safe Mode 结束后旧会话不会复活。
- 管理端可查看活跃会话列表并一键撤销（`/admin/extensions/dev-trust-sessions`）。

### 内置包发现 — `builtins`

```bash
cd apps/api
go run ./cmd/sforum extension builtins
go run ./cmd/sforum extension builtins --json
```

递归扫描内置根（默认 `extensions/builtin`）下所有 Manifest V3 包，输出
`id`、相对路径、后端入口与「是否含 Go 后端」。`scripts/build-builtin-plugins.sh`
消费这份输出，因此新增或改名内置包不再需要在 shell 里同步一份 id 列表。

### API LTS 状态

```sh
go run ./cmd/sforum extension api-lts
go run ./cmd/sforum extension api-lts --json
```

### 系统扩展层 — `system-tier`

不加载包代码，管理 SystemTier 成员（影响启动顺序与恢复语义）：

```sh
go run ./cmd/sforum extension system-tier list
go run ./cmd/sforum extension system-tier upsert <extension-id> \
  --role infra --priority 100 --enabled true
go run ./cmd/sforum extension system-tier disable <extension-id>
```

`upsert` 的 `--role` 取值为 `auth|cache|storage|infra`，`--priority` 越低越先
加载。所有子命令支持 `--database-url` 与 `--json`。

---

## 假数据：`seed:forum` 与 `seed:perf`

```sh
# config.Load 不读 .env，需先导出环境变量
set -a; . ../../.env; set +a   # 若在 apps/api 下，按你的 .env 实际路径调整

go run ./cmd/sforum seed:forum
go run ./cmd/sforum seed:forum --count=100 --users=20 --comments-max=3
go run ./cmd/sforum seed:forum --count=400 --users=60 --comments-max=8 --tags=16 --pinned=4
go run ./cmd/sforum seed:forum --categories=0 --tags=0 --pinned=0   # 只造用户/主题/评论
go run ./cmd/sforum seed:forum --profile=perf-1m --dry-run
go run ./cmd/sforum seed:forum --database-url 'postgres://…'
```

| 特性 | 说明 |
| --- | --- |
| 生成内容 | 用户与公开资料、分类分组/分类、标签、主题（标签/置顶/浏览数）、嵌套评论 |
| 写入方式 | 追加；可重复跑（已存在的分类/标签会跳过） |
| 事件 | 不触发领域事件 |
| 环境 | **仅开发/测试**，勿对生产库使用 |
| 依赖 | `DATABASE_URL` 或 `--database-url` |
| 权限 | 分类/标签需要 `super_admin`；空库首次运行第一个种子用户即超管 |

分类与标签来自内置目录（站务 → 技术 → 生活），只用于假数据，运营可在后台改名或删除。

`seed:forum` 主要 flags（完整列表以 `--help` 为准）：

| Flag | 默认 | 说明 |
| --- | --- | --- |
| `--profile` | `small` | `small` 或 `perf-1m` |
| `--count` | 1000 | 主题数（perf-1m 默认 1,000,000） |
| `--users` | 50 | 假用户数（perf-1m 默认 200） |
| `--comments-max` | 5 | 普通主题最多评论数（perf-1m 默认 0） |
| `--categories` | 0 | small：内置分类目录前 N 个（默认 6，含分组）；perf-1m：分类数（默认 20） |
| `--tags` | 0 | small：内置标签数（默认 12） |
| `--pinned` | 0 | small：置顶主题数（默认 3，落在公告分类） |
| `--views-max` | 0 | small：普通主题浏览数上限（默认 500；置顶取 `[max, 2*max)`） |
| `--hot-comments` | 0 | perf-1m 热帖评论数（默认 50000） |
| `--hot-slug` | 空 | perf-1m 热帖 slug（默认 `perf-hot-thread`） |
| `--category-slug` | 空 | small：主题全部发到该分类（默认按目录分布） |
| `--batch` | 20 | 日志/批大小（perf-1m 默认 5000） |
| `--dry-run` | false | 只打印计划 |
| `--confirm-perf-db` | false | perf-1m 非 dry-run 必填 |
| `--database-url` | 环境变量 | 覆盖 `DATABASE_URL` |

small 模式下 `--categories/--tags/--pinned/--views-max` 显式传 `0` 表示关闭该项。

`seed:perf` 是 `seed:forum --profile=perf-1m` 的别名，拥有相同的 perf 参数
（`--count` 默认 1,000,000、`--users` 200、`--categories` 20、
`--hot-comments` 50000、`--batch` 5000 等），并同样要求
`--confirm-perf-db` 才能写入。

---

## 账号维护：`users:reset-password`

重置任意用户密码（只读 `DATABASE_URL`/`--database-url`，不加载其他应用配置）。
默认是交互式流程；自动化（CI、发布镜像 smoke）使用非交互模式：

```sh
# 交互式
go run ./cmd/sforum users:reset-password
go run ./cmd/sforum users:reset-password --database-url 'postgres://…'
# 别名：
go run ./cmd/sforum user:reset-password

# 非交互（必须显式 --yes；缺少任一 flag 会在连接数据库之前失败）
go run ./cmd/sforum users:reset-password --database-url 'postgres://…' \
  --email admin@example.com --password '<符合站点策略的密码>' --yes
```

非交互模式与交互式走同一套站点密码策略与同一 `AdminSetUserPassword` 路径：
弱密码同样被拒，重置同样递增 token version 并撤销该用户的活跃会话。

---

## 隐私擦除：`privacy:erase-user`

`privacy.Registry.EraseUser` 的生产入口：遍历全部已注册 Privacy contribution 的
擦除钩子（含宿主侧插件命名空间会话状态），擦除一个用户的可擦除数据。

```sh
set -a; . ./.env; set +a
# 交互式（会展示目标摘要并要求确认）
go run ./cmd/sforum privacy:erase-user --actor-user-id 1 --user-email user@example.com

# 自动化（必须显式 --yes）
go run ./cmd/sforum privacy:erase-user --database-url 'postgres://…' \
  --actor-user-id 1 --user-id 42 --yes
```

- `--actor-user-id` 指向的账号必须真实存在且持有 `super_admin` 或 `user.manage`
  （权限由真实 RBAC 判定，不认「actor 非空」）；执行者与目标都会写入 privacy 审计台账。
- 报告只含扩展 id、成败与外部保留警告——任何数据值或命名空间键都不会被打印。
- Redis（`--redis-addr/--redis-password/--redis-db`，默认取 `REDIS_ADDR`/`REDIS_PASSWORD`/`REDIS_DB`）
  是插件会话状态擦除的必需依赖：不可达时以命名错误 fail closed，不会静默跳过该 contribution。
- 部分失败（`partial`）会打印逐项失败原因并以非零退出；完全失败没有报告。

---

## 内容修订台账：`revisions backfill`

分批回填论坛内容修订台账：

```sh
go run ./cmd/sforum revisions backfill
go run ./cmd/sforum revisions backfill --batch 1000 --loop   # 持续执行到 pending=0
go run ./cmd/sforum revisions backfill --database-url 'postgres://…'
```

`--batch` 每批处理的 posts 数（默认 100）；`--loop` 持续执行直到无待回填
记录。

---

## 开发进程清理：`dev:cleanup-orphan-plugins`

停止已被改属/孤儿化的 SForum 扩展后端插件进程（对运行中的 `sforum-api`
子进程安全，按 PID 白名单筛选）：

```sh
go run ./cmd/sforum dev:cleanup-orphan-plugins
go run ./cmd/sforum dev:cleanup-orphan-plugins --dry-run   # 只列出 PID，不发信号
```

---

## 包目录约定（速查）

| 目录 | 用途 | 启动扫描进后台 |
| --- | --- | --- |
| `extensions/dev/` | 本地实验（脚手架默认） | 否 |
| `extensions/builtin/` | 随产品内置、受保护 | 是（`SyncBuiltins`） |
| `extensions/optional/` | 仓库可选，需安装 | 否 |
| `extensions/fixtures/` | CI / 契约夹具 | 否 |
| 运行时 `EXTENSION_ROOT` | 运营上传安装后的存储 | 否 |
| `EXTERNAL_EXTENSION_ROOTS` | 独立插件/主题源码集合 | 是（惰性快照，不自动启用） |

完整地图：[extensions/README.md](../../../extensions/README.md)。
机制与信任模型：[插件编写指南](../../extensions/authoring-guide.md)、[运营侧扩展说明](../usage/extensions.md)。

---

## 相关文档

- [API 使用](./api.md)
- [日常工作流](./workflow.md)
- [环境搭建](./setup.md)
- [测试与质量门禁](./testing.md)
- [插件编写指南](../../extensions/authoring-guide.md)
- [Host API v2](../../extensions/host-api-v2.md)
