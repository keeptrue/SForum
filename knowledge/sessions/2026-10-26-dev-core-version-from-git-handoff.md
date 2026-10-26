# 2026-10-26 dev coreVersion 从 git tag 派生 Handoff

## Changed

- `apps/api/version`：dev 构建（`Current == "dev"`）的 `coreVersion` 改为从最近可达的
  `v*` tag 派生严格 semver；release 仍由 `-ldflags` 注入 `Current`，**不触发 git 调用**。
  - `v3.0.16-0-gabc12` → `3.0.16`；`v3.0.16-13-gabc12` → `3.0.16+13.gabc12`；
    `-dirty` → `+13.gabc12.dirty`；预发布 tag 保留 prerelease（`3.0.13-alpha.2+3.gabc12`）。
  - 裸 sha / 无 tag / 无 git / 解析失败 → 回落 `1.0.0`：`database/migrator` 要
    `semver.StrictNewVersion`，空串会让迁移直接失败。
  - 探针 `sync.OnceValue(probeDevelopmentCoreVersion)` 每进程一次；解析是纯函数
    `parseGitDescribe`，结果必须过 `StrictNewVersion`，否则按空处理。
- 清单说真话：`apps/api` 测试清单 39 处 `sforumVersion: ^1.0.0` → `>=0.1.0`（24 个文件），
  另有 4 个自带包清单同步改（`page-registry-demo`、`sforum-page-{schema,layout}-reference`、
  `wiki.harvester`）。生产代码里的 `testing.Testing()` 测试态分支已删除。
- 契约与文档：`contracts/openapi/schemas/system.yaml`、`schemas/extensions.yaml` 的
  `coreVersion` 描述，`docs/extensions/authoring-guide.md` 的 dev 段落，
  `knowledge/modules/extensions.md` 的 host build 段。

## 为什么测试清单要改

`^1.0.0` 是 1.0.0 时代的夹具惯例。dev 一旦如实报 `3.0.16`，安装/启用预检会把这些夹具
全判 `extension.core_incompatible`（一次全量跑 80 条同源失败）。它们不是「故意的不兼容
用例」，只是陈旧。统一改成 `>=0.1.0`：在 `1.0.0` 与 `3.x` 两条线上都成立，于是测试结论
与本机有没有 tag 无关——这正是删掉测试态分支的前提。

真正的负例保留并注明（全仓 `_test.go` 里 `^1.0.0` 就这三处，
`grep -rn '\^1\.0\.0' apps/api --include=*_test.go` 可复核）：

- `apps/api/app/Support/ExtensionManifest/compatibility_test.go:10`：`^1.0.0` 是被测约束
  本身，判定对象是显式传入的 core，与运行中的宿主无关（同一用例第 20 行还断言
  `Required == "^1.0.0"`）。
- `apps/api/app/Models/Extensions/core_compatibility_test.go:109`：内置包豁免用例，
  `item.Manifest.SForumVersion = "^1.0.0"` 是**赋值形式**——小写 `grep sforumVersion`
  匹配不到 `SForumVersion`，这是本清单最容易被漏掉的第三处。
- `apps/api/app/Models/Extensions/builtin_plugin_runtime_publication_postgres_integration_test.go:163`：
  内置包夹具，同样靠 `source == builtin` 豁免。

注意区分：`extensions/builtin/themes/sforum-default/sforum.extension.json:235` 也写着
`^1.0.0`，但它是**内置包清单**而不是测试负例，按 `source == builtin` 豁免，本轮按约定
不动。

原 `TestInstallAcceptsTheSameManifestOnAMatchingReleaseLine` 断言的是「dev=1.0.0 通过、
3.x 拒绝」——这个前提正是本轮要消灭的行为。改为同一份 `^3.0.0` 归档在 `3.0.16` 通过、
在 `1.0.0` 拒绝，保留「字节相同、结论由运行中的 core 决定」的原意。

## Decisions

- 构建元数据（`+13.gSHA`）而不是预发布段：元数据不参与 semver 比较，`3.0.16+13.g…`
  与 `3.0.16` 等价，dev 因此能满足 `>=3.0.12-alpha.7`、`>=3.0.16`、`^3.0.0`；写成
  `3.0.16-dev.13` 会被 Masterminds 的预发布规则判 false。
- 不新增 `sforum.json` 之类运行时可读版本文件：会成为第二版本权威。
- 内置默认主题的 `^1.0.0` 不动：内置包按 `source == builtin` 豁免，留到下次版本 bump。
- 决策记录：`decisions/2026-10-26-dev-core-version-from-git.md`（含代价与重开条件）。

## Next

- **运行中的 API 需要重启才生效**：本次观察到 air 没有重建（进程仍是改动前的二进制），
  `GET /api/v1/health` 会继续返回旧值。重启 `./scripts/api-dev.sh` 后再确认
  `coreVersion` 是 `3.0.16+N.gSHA`（工作树脏时带 `.dirty`）。
- `wiki.harvester` 等 4 个包的清单变了 → 包摘要变化：dev watcher 会重新 stage 并要求
  一次重新授权，`storage/extensions/` 里已装的旧摘要版本要等 watcher 完成或手工重装。
- 脚手架已修（2026-10-26）：`cmd/sforum/generator.go` 两处模板（`buildManifest` 与
  `writeComplexPluginScaffold`）改为 `sforumVersion: >=0.1.0`，`make:plugin` 的 simple /
  complex 两条路径都生成真实产物并由 `TestMakePluginScaffoldDeclaresCompatibleReleaseLine`
  跑一遍 `extension validate` 兜底。
- `app/Support/ExtensionManifest/testdata/v3/*.json` 与 docs 示例仍写 `^1.0.0`：它们只做
  格式校验、从不与运行中的 core 求值，本轮按「只改测试清单与自带包」的边界留着。

## 已知代价

- dev 现在有真实降级护栏，但**只对更早的发行线（tag）生效**：`validateCoreMigrationTarget`
  用 `target.LessThan(current)`，而 semver 的 precedence 不含 build metadata。切到更早的
  tag（`v3.0.16` → `v3.0.15`）启动会得到 `ErrCoreUpgradeIncompatible`
  （`target=3.0.15 is older than migrated Core=3.0.16`）；**同一 tag 内的回退不受护栏保护**
  ——`3.0.16+13.gabc` → `3.0.16+12.gdef`，或 dirty → clean，比较结果都是相等，护栏放行。
  恒为 `1.0.0` 时这条护栏在 dev 上完全失效；现在降发行线需要重置 dev 数据库或手工改
  `core_runtime_state`。
- **已知噪声，不是故障**：`markCoreMigrationStarted` 的免迁移快路径用
  `state.CurrentVersion == target.String()` 做字符串相等，而 build metadata 参与这个字符串。
  工作树在 dirty 与 clean 之间来回（`…g6f94488ad` ↔ `…g6f94488ad.dirty`）会让状态每次被
  置回 `migrating` 再 `ready`；提交距离变化同理。goose 幂等、没有数据变更，代价只是几行
  UPDATE 与一次 `revision` 自增，下次看到别当故障查。
- dev 的公开面（`/api/v1/health`、公开 L2 `coreVersion`）会带短 SHA 与 `.dirty`；
  release 面不变。
