# 决策：dev 构建的 coreVersion 从 git tag 派生

Status: accepted
Date: 2026-10-26
Related: `2026-10-25-core-version-surface.md`、`2026-10-24-app-store-out-of-tree.md`

## 背景

`2026-10-25-core-version-surface.md` 把 `coreVersion` 定为兼容判定的唯一权威，但
dev 构建里它被硬编码成 `1.0.0`。当时这是「本机没有发行身份」的合理兜底，代价是
**开发机上的兼容判定从此与真实发行线脱节**：

- 商城按「只看当前版本可安装」过滤时，声明 `>=3.0.12-alpha.7` 的
  `sforum.oauth2-provider` 在 dev 环境直接消失——插件本身没错，是宿主把自己报低了。
- 反过来，任何声明 `^1.0.0` 的老包在 dev 上看起来兼容，到发行版才炸。

本仓当时的真实状态（`git describe --tags --match 'v[0-9]*' --long --dirty --always`）：
`v3.0.16-13-g6f94488ad`，即最近 tag 为 `v3.0.16`、距它 13 个提交。也就是说开发机
明明知道自己站在哪条发行线上，只是没有把这个事实报出去。

## 决策

### 1. dev 从最近可达的 tag 派生，release 不变

`CoreCompatibilityVersion()` 的唯一变化在 `Current == "dev"` 分支：

- 取派生值，派生为空才回落 `developmentCoreCompatibilityVersion = "1.0.0"`；
- `Current` 非 `dev`（release 由 `-ldflags` 注入）原样返回，**不触发任何 git 调用**
  （`TestReleaseBuildsNeverConsultTheGitProbe` 守护：探针一旦被调用就让测试失败）。

### 2. 派生规则：tag 给发行线，git 状态进 build metadata

`git describe` 输出经纯函数 `parseGitDescribe` 转换：

| describe 输出 | coreVersion | 说明 |
| --- | --- | --- |
| `v3.0.16-0-gabc12` | `3.0.16` | 正好在干净 tag 上 |
| `v3.0.16-3-gabc12` | `3.0.16+3.gabc12` | tag 之后 3 个提交 |
| `v3.0.16-0-gabc12-dirty` | `3.0.16+0.gabc12.dirty` | 在 tag 上但有本地改动 |
| `v3.0.13-alpha.2-3-gabc12` | `3.0.13-alpha.2+3.gabc12` | 预发布 tag 之后 |
| 裸 sha / 空 / 无 `v*` tag / 解析失败 | `""` → 兜底 `1.0.0` | |

选择 **build metadata 而不是预发布段** 是这次决策的关键：构建元数据不参与 semver
比较，`3.0.16+13.g6f94488ad` 与 `3.0.16` 在约束求值上完全等价，所以 dev 构建能
满足发行线声明（`>=3.0.12-alpha.7`、`>=3.0.16`、`^3.0.0` 都成立，见
`TestDerivedVersionSatisfiesReleaseConstraints`）。若写成 `3.0.16-dev.13`，会落进
Masterminds 的预发布规则：`>=3.0.16` 直接判 false，等于把 dev 又踢出所有第三方包
的声明范围。

结果必须通过 `semver.StrictNewVersion` 才算派生成功，否则返回空串走兜底——派生
值非法比「报低了」更难排查。

### 3. 每个进程只探一次

`developmentVersionProbe = sync.OnceValue(probeDevelopmentCoreVersion)`：git 状态在
进程生命周期内不会变，而 `CoreCompatibilityVersion()` 会在启动、v2 握手、
`/api/v1/health`、公开 L2 描述符等路径被反复调用。单测直接替换这个变量，不碰 git。

### 3.1 没有「测试态」分支，改为让清单说真话

第一版曾用 `testing.Testing()` 让测试进程固定回落 `1.0.0`，因为 `apps/api` 里有 39 处
测试清单写着 `sforumVersion: ^1.0.0`，dev 一旦报 `3.0.16` 就会在安装/启用预检处集体
失败。那个分支能跑，但它把根因留在原地：**那些清单本身就是错的**——仓库已在 v3.0.16
发行线，声明 `^1.0.0` 的包在真实 dev 宿主上会被拒绝（`extensions/dev/plugins/wiki.harvester`
就装在 `storage/extensions/` 里）。测试看不见派生只是症状。

最终做法：39 处测试清单改成 `>=0.1.0`（在 `1.0.0` 与 `3.x` 两条线上都成立，于是结论
与本机有没有 tag 无关），4 个自带包清单同步订正，生产代码不留任何测试态分支。
真正的负例保留并注明，全仓 `_test.go` 里只剩这三处
（`grep -rn '\^1\.0\.0' apps/api --include=*_test.go` 可复核）：

- `app/Support/ExtensionManifest/compatibility_test.go:10`：`^1.0.0` 是被测约束本身，
  判定对象是显式传入的 core。
- `app/Models/Extensions/core_compatibility_test.go:109`：内置包豁免用例，
  `item.Manifest.SForumVersion = "^1.0.0"` 是赋值形式，小写 `grep sforumVersion` 会漏。
- `app/Models/Extensions/builtin_plugin_runtime_publication_postgres_integration_test.go:163`：
  内置包夹具，靠 `source == builtin` 豁免。

`extensions/builtin/themes/sforum-default/sforum.extension.json:235` 的 `^1.0.0` 是内置
**包清单**、不是测试负例，本轮按约定不动。
原 `TestInstallAcceptsTheSameManifestOnAMatchingReleaseLine` 断言的是「dev=1.0.0 通过、
3.x 拒绝」，前提已被移除，改为同一份 `^3.0.0` 归档在 `3.0.16` 通过、在 `1.0.0` 拒绝。

关心兼容结论的用例用 `withCoreVersion(t, "3.0.16")` 显式钉住版本，不再依赖宿主环境。

### 4. `1.0.0` 兜底不能删

`database/migrator` 把目标版本喂给 `semver.StrictNewVersion`
（`core_runtime_state.go:27`、`core_compatibility.go:23`、`core_authority_kernel.go:123`），
空串会让迁移直接失败。派生失败时的兜底不是「兼容性判断」，是「让 dev 迁移还能跑」。
同理，不新增 `sforum.json` 之类的运行时可读版本文件：那会成为第二版本权威，还要在
打包、发布、容器链路里维护同步。

## 代价与已知限制

- **dev 公开面多了一点指纹**：公开 L2 描述符与 `/api/v1/health` 在 dev 上会出现
  短 SHA 与 `.dirty`。release 构建仍然只有发行号（`-ldflags` 注入，探针不执行），
  与 `2026-10-25` 记录里「公开面只给 release 级 coreVersion」的结论不冲突——它约束
  的是发行版。dev 实例本来就只跑在本机。
- **工作树一改就带 `.dirty`**：值会从 `3.0.16+13.g6f94488ad` 变成
  `3.0.16+13.g6f94488ad.dirty`。这是有意的（能区分「干净的 tag 构建」与「边改边跑」），
  比较结果不变。
- **tag 名自带 `+` 元数据时不派生**（如 `v3.0.16+ci.5`）：正则只接受
  `v<X.Y.Z[-pre]>`，这类 tag 回落 `1.0.0`。本仓 tag 命名不含 `+`，需要时再扩。
- **dev 现在有真实的降级护栏，但只覆盖更早的发行线（tag）**：`validateCoreMigrationTarget`
  用 `target.LessThan(current)`，而 Masterminds 的 `Compare` 只比 major/minor/patch 与
  prerelease，**build metadata 不参与 precedence**。切到更早的 tag（`v3.0.16` →
  `v3.0.15`）启动会得到 `ErrCoreUpgradeIncompatible`（`target=3.0.15 is older than
  migrated Core=3.0.16`）；**同一 tag 内的回退判不出来**——`3.0.16+13.gabc` →
  `3.0.16+12.gdef`、或 dirty → clean，比较结果都是相等，护栏放行。恒为 `1.0.0` 时这条
  护栏在 dev 上完全失效；现在降发行线需要重置 dev 数据库或手工改 `core_runtime_state`。
  这是「dev 报告自己的真实发行线」的应有代价，不是回归。
- **已知噪声，不是故障**：`markCoreMigrationStarted` 的免迁移快路径用
  `state.CurrentVersion == target.String()` 做字符串相等，而 build metadata 参与这个字符串。
  工作树在 dirty 与 clean 之间来回（`…g6f94488ad` ↔ `…g6f94488ad.dirty`）会让状态每次
  被置回 `migrating` 再 `ready`，提交距离变化同理。Goose 幂等、没有数据变更，代价只是几行
  UPDATE 与一次 `revision` 自增；下一个人看到它重复出现不要当故障查。
- **无 tag 的浅克隆 / 源码包**仍然报 `1.0.0`：没有发行身份可派生，兜底是唯一诚实答案。
- 派生依赖运行目录在 git 工作树内。`go run`、`air`、`go test` 都在仓内，正常；
  把二进制拷到仓外运行会回落。
- **四个自带包清单已订正为 `>=0.1.0`**（`page-registry-demo`、
  `sforum-page-layout-reference`、`sforum-page-schema-reference`、`wiki.harvester`）。
  代价是包摘要变化：dev watcher 会重新 stage 并要求重新授权，`storage/extensions/`
  里已装的旧摘要版本需要等 watcher 完成或手工重装。内置默认主题的 `^1.0.0` 不动
  （`source == builtin` 豁免），留到它下次版本 bump。
- **脚手架模板已跟上**：`cmd/sforum/generator.go` 的 `buildManifest` 与
  `writeComplexPluginScaffold` 都改成 `sforumVersion: >=0.1.0`。不改会让 `make:plugin`
  产出的包在 v3 宿主上装不上——那是本次改动的直接回归。回归测试
  `TestMakePluginScaffoldDeclaresCompatibleReleaseLine` 会真的生成 simple / complex 两种
  产物并跑 `extension validate`，不是只断言模板字符串。
- **测试清单不再是 1.0.0 时代的样子**：39 处 `sforumVersion: ^1.0.0` 改为 `>=0.1.0`。
  关心兼容结论的用例必须用 `withCoreVersion` 显式钉住版本，不能依赖宿主环境；
  新写清单时也别再写 `^1.0.0`。

## 验证

在本仓（`git describe` = `v3.0.16-13-g6f94488ad`）实跑：

| 命令 | 结果 |
| --- | --- |
| `cd apps/api && go test ./version/...` | ok；`TestProbeDevelopmentCoreVersionIsStrictOrEmpty` 日志出 `3.0.16+13.g6f94488ad.dirty`（dirty 来自未提交改动） |
| 真实非测试二进制（临时 main 调 `version.Identity()`） | `coreVersion=3.0.16+13.g6f94488ad.dirty buildVersion=dev-6f944 valid=true` |
| `cd apps/api && go test -count=1 ./...` | exit=0，134 个包 ok，0 FAIL（无缓存） |
| `GIT_DIR=/tmp/empty-git go test -count=1 ./version/... ./app/Models/Extensions/... ./app/Http/Controllers/Extensions/... ./app/Support/Extensions/ ./database/migrator/...` | 全部 ok；`TestProbeDevelopmentCoreVersionIsStrictOrEmpty` 打印 `derived development coreVersion = ""` 并 skip——证明同一批用例在「无 tag → 1.0.0」与「v3.0.16 → 3.0.16+…」两种环境下结论一致 |
| `cd apps/api && go build ./...` | ok |
| `node tests/validate-architecture-boundaries.mjs` | passed（1933 production files） |
| `ruby scripts/validate-openapi-refs.rb` | OK，2907 refs / 61 files |

`TestDerivedVersionSatisfiesReleaseConstraints` 额外固定了「构建元数据不参与比较」：
`3.0.16`、`3.0.16+13.gabc12`、`3.0.16+13.gabc12.dirty` 都满足 `>=3.0.12-alpha.7`、
`>=3.0.16`、`^3.0.0`——即商城原先被藏掉的那个插件，现在在 dev 上可见。

## 重开条件

- 需要在 CI 里把「本次构建对应的发行号」冻结进二进制（不依赖运行期 git）：改由
  构建脚本注入 `Current`，探针保持兜底即可。
- 需要区分同一条发行线上的多个 dev 构建做缓存/缓存失效：先论证指纹风险，再考虑
  把短 SHA 放进 `buildVersion`（展示面）而不是 `coreVersion`。
- tag 命名引入 `+` 元数据：扩 `gitDescribePattern`，同时补对应表驱动用例。
