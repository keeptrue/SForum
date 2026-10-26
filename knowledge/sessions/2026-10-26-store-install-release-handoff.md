# 2026-10-26 商城安装发版 Handoff

## Changed

- **core 10 个提交到 `e658aedf0`（含 P0 修复 `c6d6d53ba`）**：C1 dev `coreVersion` 从 git tag 派生（含 16 个 DB
  集成测试改为从 `CoreCompatibilityVersion()` 派生）；C2 清单词表新增
  `extension.install.enable`；C3 安装并启用二连（bridge 三态 + 弹窗 + 披露）；
  C4 扩展页归属交给 manifest `group`；C5 平台闸门（`ExtensionPackage.DetectTarget`）；
  C6 `LifecycleRecovery` 与运行时分歧守卫；C8 dev watcher 与 `build.sh` 产物对齐；
  C7 knowledge 汇总。工作树干净。
- **store 5 个提交到 `18a9a07`**：既有 1（`97348b9`）+ pin（完整 40 位 SHA
  `5a4ce5eaf341ba0825fdb6ffca87e63be88d3250`，行内注释 `# v3.0.17`）+
  service 35 文件 + 插件 30 文件 + 文档 20 文件。未跟踪残留：`tun2.html`。
- **发版**：annotated tag `v3.0.17` 指向含本批与 P0 修复的提交（最终 SHA 以
  `git rev-parse v3.0.17^{commit}` 为准）；推送状态以老运为准。

## P0 拦截

- **症状**：删除一个曾授予可执行信任的管理员账号后，API 启动直接崩：
  `load executable trust grant: can't scan into dest[6] (col: granted_by_user_id):
  cannot scan NULL into *int64`。
- **根因**：`app/Models/Extensions/trust_postgres.go` 的 `LiveGrant` 读
  `granted_by_user_id` 时漏写 `COALESCE(…,0)`；该列 `ON DELETE SET NULL` 可空；
  同表 6 处读该列的查询里只有这一处漏。
- **发现方式**：真机取证删临时管理员 `qa-agent` 时意外触发 —— 取证流程本身撞出一个既有 P0。
- **处置**：修 1 行 + 回归用例（修前必失败已留证）。改动已随 `c6d6d53ba` 提交，
  `v3.0.17` 已重打到含修复的提交；旧 tag 从未推送，因此不是 force-move 已发布 tag。
- **同类残留**（本次不发，另开单）：`Forum/resource_guard.go` 两处、
  `EntityMeta/guard_subject.go`、`Forum/revisions_redaction.go` —— 作者账号带外删除后
  编辑/删除鉴权 500，不在启动路径。

## Evidence

- **动作词表**：旧 pin `10c2b21e8` 与 HEAD `8ab9f246b` 的 `AdminHostActions()` 都只有
  `extension.install`；当时 `git log --all -S 'extension.install.enable'` 为空
  → 必须先提交，pin 才有东西可指。
- **`>=3.0.17` 运行期 E2E**：钉 host identity=`3.0.16`，`POST /api/v1/admin/extensions`
  → 422 `extension.core_incompatible`，`fields` 带 `required/coreVersion/buildVersion`；
  改成 `>=3.0.16` 则越过版本闸门、停在 403 `backend_execution_restricted`
  （证明版本闸门在权限闸门之前）。
- **CI 的 `manifest OK` 对 `sforumVersion` 真假无信息量**：`v3_validate.go:31` 只做
  `semver.NewConstraint` 语法解析；唯一求值点是 `Models/Extensions/lifecycle.go:720`。
- **`ensureCoreCompatible` 两个静默放行口**：`!identity.Valid → nil`、
  `ErrSForumVersionUnverifiable → nil`。
- **真机取证**（`/tmp/store-qa-2026-10-26/evidence/report.md`；临时 super_admin
  `qa-agent` 用完即删）：spin + `0→0→0→1→1`（250ms×5，两次安装同形）；
  `已安装并启用` / `已安装并升级` 且插件页独立复核（启动 1 次 / 2 次，运行时运行中）；
  trust-review 先审阅后确认（折叠块「请求的主机权限 7」「后端二进制 1」）；无 P0。

## Decisions

- pin 用**完整 40 位 SHA**，不用 tag：短 SHA 不是合法 refspec，tag 可被 force-move。
- 顺序硬约束：commit → push main → CI 绿 → 推 tag → **才**改 `sforumVersion`。
  提前改会让 dev watcher 把 store 插件判成 `core_incompatible`，商城页当场消失。
- `core_authority_kernel_test_helpers_test.go:147` 保持字面量：那条约束校验的是用例
  自选的 1.x 迁移目标，不是宿主版本。

## Next

- **`sforumVersion` 仍是 `>=1.0.0`**（`18a9a07` 实测）。**等 tag 重打 + 重启确认
  `coreVersion` 是精确 `3.0.17` 之后**才可改到 `>=3.0.17`；manifest 必须由 `build.sh`
  落盘，不手改。
- 卸载 tombstone pending：`qa.store.proof` / `sforum.web-search`（op 703）/
  `sforum.marketplace`（ops 908/909）同形态 → 另开单。
- 真商城 6–7MB 包处在宿主 90s 超时边缘（GitHub release asset 直连实测 15–85 KB/s，
  6.77MB 用 87.2s；宿主 90s、页面 120s）→ 后续评估包体积 / 超时可配 / 重试策略。
- `tun2.html` 是他人调试残留，未提交未删除。

## Open Questions

- 无。两条产品口径（主题装完是否加「安装并设为默认」、非超管是否拒绝装可执行包）
  的建议见 `2026-10-26-release-v3.0.17-readiness.md` §10，仍待拍板。
