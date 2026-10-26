# 决策：dev 的「Core 版本未知」不等于「Core 版本更低」

Status: accepted
Date: 2026-10-27
Related: `2026-10-26-dev-core-version-from-git.md`、`2026-10-25-core-version-surface.md`

## 背景

`2026-10-26-dev-core-version-from-git.md` 让 dev 构建从最近可达的 `v*` tag 派生
`coreVersion`，无 tag 时回落哨兵 `developmentCoreCompatibilityVersion = "1.0.0"`。
回落本身是必要的：migrator 用 `semver.StrictNewVersion` 解析目标版本，空串会让迁移
直接失败。缺陷在于**回落值被当成了真实意图**。

2026-10-27 实测：仓库被 shallow 化后 `git describe --tags --match 'v[0-9]*' --long
--dirty --always` 退化成裸 sha `d1153068c-dirty`，`parseGitDescribe` 的正则不匹配 →
返回 `""` → `CoreCompatibilityVersion()` 静默给出 `1.0.0`。数据库
`public.sforum_core_runtime_state.current_version` 是
`3.0.18+4.ga4e91267b.dirty`，于是 `validateCoreMigrationTarget` 判
`1.0.0 < 3.0.18`，抛 `ErrCoreUpgradeIncompatible`，API 起不来。

一句话：**「不知道我是谁」被编码成了「我是 1.0.0」，再被当成真实降级意图**。运维用
`git fetch --unshallow` 恢复了现场，但代码缺陷还在。

## 决策

### 1. 版本与「是否声明」成对暴露，不新增第二权威

`version.CoreCompatibilityDeclared() (string, bool)`：

| 构建 | 返回值 | declared |
| --- | --- | --- |
| `Current != "dev"`（release，`-ldflags` 注入） | `Current` | `true` |
| `Current == "dev"` 且探针有值 | 派生值 | `true` |
| `Current == "dev"` 且探针为空 | `1.0.0` 哨兵 | `false` |

`CoreCompatibilityVersion()` 签名与行为逐字不变，内部改为取该函数的第一个返回值——
**字符串仍是唯一版本权威**，布尔只是它的出处，不是第二个版本来源。

### 2. 未声明的 target 不参与「比较版本」的否决，结构校验一条不减

| 闸门 | 未声明时 |
| --- | --- |
| `checkCoreUpgradeCompatibility`（ledger 兼容性） | 在任何 SQL 之前 `return nil` |
| `validateCoreMigrationTarget`（运行时状态降级围栏） | 只跳过 `target < current` 与 `target < pending` 两处比较；status 与存储版本可解析照旧报错 |
| `reconcileCoreKernelAuthority`（物理权限兼容性） | 照常 `semver.StrictNewVersion`、加载 blockers、跑 `validatePhysicalCoreCompatibilityDeclarations`（exactness 结构校验）；只有 `rejectCoreCompatibilityDeclarations`（版本比较）不执行 |
| `publishCoreRuntimeVersion` | 不写 `current_version`；`target_version` 收敛到 `current_version`；只有全新库（current 为空）才写入 target |

第一版实现给 kernel 闸门传空 target，理由是「空 = 不表达版本意见」。复核指出
`core_authority_kernel.go` 的 `if targetVersion != ""` 同时包住了 exactness 校验，
传空串等于把结构校验也跳过。改为显式 `declared` 参数，语义与另外三处一致。

`Up` 与 `CheckOnlineSafe` 共用 `resolveCoreTargetVersion(cfg)`：显式 `TargetCoreVersion`
非空即视为声明（**写了版本就是意图**，护栏不能因为调用方少设一个 bool 就被绕过），
否则取 `CoreCompatibilityDeclared()`。未声明时用 `logger.WarnContext` 打一条可操作告警
（`derived_version` / `caller_declared_target` / `usual_cause` / `fix`），说明
`git describe` 没找到可达 `v*` tag、常见原因是 shallow clone、建议 `git fetch --unshallow`，
并明说本次跳过降级护栏。

`Config` 不带「是否声明」字段：本轮曾加过 `TargetCoreVersionDeclared`，收尾时删除。
理由是 **pin 即声明，没有第二语义**——它既不参与判定，又会被后来者当成开关去用；
`AGENTS.md` 明确要求不为一次性行为加包装。告警里的 `caller_declared_target` 由
`strings.TrimSpace(cfg.TargetCoreVersion) != ""` 就地算出。

### 3. 生产必须显式声明版本（fail-closed）

初版的前提「生产 `Current` 恒非 dev」是错的：`apps/api/Dockerfile` 是
`ARG SFORUM_VERSION=dev`，`compose.yaml` 原先没有传 `args`，运行镜像无 git、无 `.git`。
所以「源码 docker build + `APP_ENV=production`」必然 `Current == "dev"` 且探针失败 →
`declared=false` → 两道版本闸门全跳，库里已有真实版本时改动前会启动失败、改动后只剩一条 Warn。

- `version.RequireDeclaredCoreVersion(appEnv, coreVersion, declared)` 是纯策略；
  `RequireDeclaredForProduction(appEnv)` 用真实构建身份调用它。错误文案自带修法
  （`docker build --build-arg SFORUM_VERSION=<semver>`、compose 设 `SFORUM_VERSION`、
  或改用官方发布镜像），并说明版本护栏是 safe-mode 同级的 fail-closed 面。
- 闸门校验两件事：**声明了**，且**声明的值能过 `semver.StrictNewVersion`**。
- `v` 前缀在**兼容版本解析处**归一：`CoreCompatibilityDeclared()` 先 trim 再
  `TrimPrefix(…, "v")`，之后才判 `dev`。部署侧 `SFORUM_VERSION` 本来就是 v-tag 语义
  （`.env.production.example`、`configure-production.sh` 生成 `v3.0.18`），build arg
  沿用同一个名字而不是另起一个——强迫运维记两个拼写不同的变量更糟。归一之后
  `v3.0.18` 能过闸门；`StrictNewVersion` 检查保留为兜底（`not-semver`、空串、裸 `v`
  仍被拒）。`release.yml` 用 `${REQUESTED_TAG#v}` 正好绕开这条，CI 测不到，靠 `version`
  包单测钉住。
- **展示版本 vs 兼容版本**是既有分工，本轮不变：`BuildInfo.Version`（`Get()`）显示原始
  `Current`（带 v 就带 v），`CoreVersion`（`CoreCompatibilityVersion()` /
  `Identity().CoreVersion`）是去 v 的 strict semver，只有后者参与约束求值与迁移闸门。
- `compose.prod.yaml` 的 `migrate` 服务有自己的完整 build 块（不继承 api 的 args），
  同样补了 `SFORUM_VERSION`：源码构建的生产 migrate 镜像否则恒为 dev，被同一道闸门拒绝。
- 闸门位置：`bootstrap.runStartupMigrations` **在 `MIGRATE_ON_STARTUP` 早退之前**——
  生产模板写 `MIGRATE_ON_STARTUP=false`，所以这是启动闸门而不是迁移闸门；另加独立入口
  `cmd/migrate`，那才是生产迁移真正执行的地方。
- `compose.yaml` 的 api build 增加 `args: SFORUM_VERSION: ${SFORUM_VERSION:-dev}`：
  不传时与 Dockerfile 默认值一致（dev 行为不变），传了就注入。`compose.release.yaml`
  把 build 重置为 `null`（发布镜像由 CI 带 `--build-arg` 构建），`compose.prod.yaml`
  只覆盖 target、args 从 base 合并，`compose.dev.yaml` 是 `target: dev`（air，无 ldflags），
  都不需要额外 args。

### 4. 为什么护栏没有变弱

- release / 官方镜像：`Current` 由 `-ldflags` 注入 → `declared == true` → 四条闸门行为与
  改动前逐字相同；`TestReleaseBuildsNeverConsultTheGitProbe` 还保证 release 不执行 git 探针。
- 源码构建的生产：不再是「静默跳过闸门」，而是启动即失败（`runStartupMigrations` 与
  `cmd/migrate` 各一道），修法写在错误里。
- dev：`declared == false` 只跳过版本比较；结构校验、权限收敛、迁移执行全在。
- 反向证据（本轮固化）：同一输入 `current=3.0.18+4.ga4e91267b.dirty`、`target=1.0.0`，
  `declared=false` → `nil`，`declared=true` → `ErrCoreUpgradeIncompatible`；物理声明
  exactness 漂移在 `declared=false` 时仍报 `ErrCoreAuthorityConflict`。

## 已知代价与残留风险

- **生产源码构建必须显式给版本**：不传 `SFORUM_VERSION` 的 `docker compose build` +
  `APP_ENV=production` 会在启动/迁移时失败。这是有意的 fail-closed，不是配置噪声。
- **全新库 + 未声明运行仍会写入哨兵**：`current_version` 为空时没有更高记录可保，
  只可能发生在 dev。
- **未声明运行不刷新 `current_version`**：库里保留上一次声明的版本，而 schema 可能已经
  前进。这是有意的——宁可让记录偏高（下一次更旧的已声明构建会被拒），也不写一个更低的假版本。
- 告警仍是 `Warn` 而不是 `Error`：无 tag 的开发机是合法状态。
- 扩展侧 `app/Support/Extensions` 的 `coreCompatibility` 求值仍以
  `CoreCompatibilityVersion()` 的字符串为准，未接 `declared`。

## 重开条件

1. 需要支持「dev 机上跨发行线回退演练」→ 先修 git 探针；未声明运行现在不会写低
   `current_version`，但仍不做版本比较。
2. 扩展侧兼容性求值需要区分「未知 / 更低」→ 那是另一处判定面，需单独设计。
3. 生产需要跑「不带版本的镜像」（例如把 dev 镜像直接投生产）→ 那等于放弃版本护栏，
   必须做成运维显式开关，不能默认放行。

## 验证

在本仓（`git describe` = `v3.0.18-10-gd1153068c-dirty`）实跑：

| 命令 | 结果 |
| --- | --- |
| `cd apps/api && go build ./...` | 通过 |
| `cd apps/api && go vet ./...` | 通过（无输出） |
| `cd apps/api && go test ./version/... ./database/migrator/... ./bootstrap/...` | 通过（无 DB 时集成用例 skip） |
| `SFORUM_TEST_DATABASE_URL=… go test -count=1 ./database/migrator/`（本机 sforum-postgres） | 通过（含 44 条集成用例） |
| 一次性 Postgres 17 上跑 `TestUpTreatsUndeclaredTargetAsUnknownInsteadOfDowngrade` | 通过：pin `1.0.0` 仍被拒绝，未声明的 `1.0.0` 放行、告警，且 `current_version` 不变 |
| `node tests/validate-architecture-boundaries.mjs` | 通过 |
