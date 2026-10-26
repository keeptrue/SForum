# 2026-10-27 dev「Core 版本未知」不再等于「更低」Handoff

## Changed

- `apps/api/version`：新增 `CoreCompatibilityDeclared() (string, bool)`，把
  「版本」和「是不是真实声明」成对暴露；`CoreCompatibilityVersion()` 签名与行为不变，
  内部改为取它的第一个返回值。三种分支：release 恒 `true`；dev 探针有值 `true`；
  dev 探针为空 → `("1.0.0", false)`。
- `apps/api/database/migrator`：
  - `resolveCoreTargetVersion(cfg)` 统一解析：显式 `TargetCoreVersion` 非空即声明，
    否则走 `CoreCompatibilityDeclared()`。`Up` 与 `CheckOnlineSafe` 共用。
  - `checkCoreUpgradeCompatibility(ctx, db, target, declared)`：`!declared` 在任何 SQL
    之前返回 nil。
  - `validateCoreMigrationTarget(state, target, declared)`：`!declared` 跳过
    `target < current` 与 `target < pending`，结构校验（status、存储版本可解析）照旧。
  - `prepareCoreMigrationAuthorityForVersion(ctx, db, target, declared)` /
    `reconcileCoreKernelAuthority(..., declared)`：未声明时只跳过
    `rejectCoreCompatibilityDeclarations`（版本比较），`StrictNewVersion`、blockers 加载、
    `validatePhysicalCoreCompatibilityDeclarations`（exactness）照跑。
  - `publishCoreRuntimeVersion(ctx, conn, target, declared)`：未声明时不写
    `current_version`，`target_version` 收敛到 `current_version`；全新库（current 为空）才写 target。
  - `WarnContext` 一条可操作告警：`derived_version` / `caller_declared_target` /
    `usual_cause`（shallow clone）/ `fix`（`git fetch --unshallow`）。
- `apps/api/version`：新增 `RequireDeclaredCoreVersion(appEnv, coreVersion, declared)`（纯策略）
  与 `RequireDeclaredForProduction(appEnv)`（真实构建身份）。
- `apps/api/bootstrap/migrations.go`：`runStartupMigrations` 在 `MIGRATE_ON_STARTUP`
  早退**之前**调用生产闸门（生产模板写 `MIGRATE_ON_STARTUP=false`，这是启动闸门）。
  仍有意不传 `TargetCoreVersion`。
- `apps/api/cmd/migrate/main.go`：同一闸门加在独立迁移入口（生产迁移真正执行的地方）。
- `compose.yaml`：api build 增加 `args: SFORUM_VERSION: ${SFORUM_VERSION:-dev}`；
  `compose.prod.yaml` 的 `migrate`（独立 build 块，不继承 api 的 args）同步补上。
  源码构建的生产镜像从此有正经入口，发布镜像走 `compose.release.yaml`（build 重置为 null）。
- `apps/api/version`：`v` 前缀在兼容版本解析处归一（trim → TrimPrefix "v" → 判 dev），
  部署侧沿用同一个 `SFORUM_VERSION`（v-tag 语义）；`BuildInfo.Version` 仍显示原始标签。
- 文档：`docs/{zh-CN,en-US}/deployment/troubleshooting.md` 在既有
  `core upgrade is incompatible ...` 行下方补了 dev 场景的识别与修复。
- 测试：`version_test.go` 三分支；新增 `core_target_version_test.go`（解析 + 围栏对照）；
  `core_compatibility_test.go` 补未声明/已声明对照与「nil 查询器也不触发 SQL」；
  新增 `core_target_version_postgres_integration_test.go` 复刻事故现场。

## Decisions

- 决策记录：`decisions/2026-10-27-dev-core-version-unknown-vs-lower.md`。
- 字符串仍是唯一版本权威，布尔只描述出处，不是第二版本源。
- 生产护栏：release 镜像 `declared` 恒真、四条闸门逐字不变；源码构建的生产镜像不再
  静默跳过闸门，而是在 `runStartupMigrations` / `cmd/migrate` 启动即失败（fail-closed，
  修法写在错误里）。
- 反向证据固化为用例：同一输入（`current=3.0.18+4.ga4e91267b.dirty`、`target=1.0.0`）
  `declared=false` → nil，`declared=true` → `ErrCoreUpgradeIncompatible`。

## Next

- **新约束：源码构建的生产镜像必须显式声明版本**（`--build-arg SFORUM_VERSION=<semver>`
  或 compose 的 `SFORUM_VERSION`），否则启动即失败。发布流水线不受影响。
- 未声明运行不再刷新 `current_version`（只收敛 `target_version`）；全新库仍会写入哨兵。
- 扩展侧 `app/Support/Extensions` 的 `coreCompatibility` 求值仍以
  `CoreCompatibilityVersion()` 字符串为准，未接 `declared`；如需一致语义另开一轮。
- 本地 dev API 需要重启才会带上新告警与新语义。

## Open Questions

- `Config.TargetCoreVersionDeclared` 已删除：pin 即声明，没有第二语义，零行为的公开字段
  会被后来者当成开关。若将来真需要「pin 时声明来源」，先改决策与验收
  （「显式 pin `1.0.0` 仍失败」那条必须同步改写），不要复活一个不参与判定的字段。
