# 2026-11-17 Session Handoff

## Changed

- main `6643af3f0`；今日三个 squash 依次是 `c1bdb978d`（#260 / #258，app.config 移入
  `app/`）→ `0bf151841`（#261 / #254，发布期闸门）→ `6643af3f0`（#263 / #254，dev 自愈），
  0 个 open PR，工作树干净。三份文档（`AGENTS.md`、决策、模块笔记）都已随 PR 同步，本轮收尾
  只写 knowledge。
- **#258（已关，`c1bdb978d`）**：`apps/web/app.config.ts`（仓库根）从未被 Nuxt 加载
  （`srcDir=app/`），里面的 alert 覆盖是死代码；现已并入 `apps/web/app/app.config.ts`。
  **关键事实**：`tv({extend})` 走 tailwind-merge 同组合并，槽位字符串是**合并**不是替换 ——
  只写 `shrink-0` 不写 `overflow-visible`，默认的 `overflow-hidden` 会继续留在 class 里，
  覆盖等于没生效，所以 root 两个类都要写。新增
  `apps/web/tests/framework/appConfigLocation.test.ts`（5 条）钉住文件位置、导出内容与真实
  合并结果。QA：4 页 × 双视口 8/8。
- **#254 发布期闸门（已关，`0bf151841`）**：`insertPluginRuntimePublication`
  （`apps/api/app/Models/Extensions/plugin_runtime_publication_postgres.go`）在两条 INSERT
  （publications / members）之前逐成员跑 `artifactintegrity.Verify`；9 个生产调用点全部继承，
  无开关、无 reason 豁免；空集（genesis）放行；失败带四坐标 `extensionId` / `version` /
  `digest` / 相对 `path`。
- **#254 dev 自愈（已关，`6643af3f0`）**：只在 `APP_ENV=development` 且 `!SafeMode` 时启用
  （与既有 dev 通道同一判据），候选源含 `storage/builtin-dev`，经
  `PublishPluginRuntimeArtifactRepairTx` 发布 —— 自愈出的 revision 仍要过同一道闸门；
  零写盘（不往制品目录补/改文件）；构建窗口（`backend/plugin` 尚未产出）fail-closed。
  代码：`apps/api/bootstrap/plugin_runtime_dev_heal.go`、
  `apps/api/bootstrap/api_assembly_extension_restore.go`。

## Decisions

- 规范与取舍正文不在本文件，去这三处：`AGENTS.md`（「Publication is fail-closed」段已从
  target spec 改为 wired，并写入 dev 自愈范围）、
  `knowledge/decisions/2026-11-17-artifact-integrity-at-publish-time.md`、
  `knowledge/modules/extensions.md`。
- **已知限制**（已写进决策记录）：dev 自愈**一次启动最多修复一个成员**；多成员同坏、只有一个
  有候选时，第一次 repair 会被闸门拒（`healed=0`，仍 fail-closed）。#262 跟踪三条 P3：多成员
  批量、补登记 manifest 未展开 `includes`、repair 多一条无 actor 审计。
- 上一份热交接 `sessions/2026-11-17-plugin-artifact-integrity.md` 的 Next 两条（「发布期校验
  闸门与 dev 自愈尚未实现」）已被 `0bf151841` / `6643af3f0` 兑现，那份文件的 Next 段落作废；
  #256 的取证与 CLI 用法仍然有效（坏制品目录**仍不许补文件**：manifest `backend.digest` 与
  目录树摘要都对不上）。

## Environment And Verification

- 交刀时 `:3000` / `:8080` 都是关的（不是 agent kill 的）；接刀先起 compose 依赖与 dev 服务。
- dev 库最新 publication revision 是 **1548**（旧交接写的 1541 已过期）；
  `sforum extension verify-artifacts` 输出 `checked 16 artifact(s)` /
  `all artifacts are loadable`。
- 验证纪律新增两条：本地 Go 验证必须带上 `./internal/testdb/`（CI 抓到过
  `serial_insert_guard` 红）；web 测试必须用 bun 跑 —— 新测试用
  `createRequire('@nuxt/ui/package.json')` 定位包，Node/tsx 会
  `ERR_PACKAGE_PATH_NOT_EXPORTED`。

## Next

- 待老板拍板：`/control-panel/forum/settings` 在 390×844 下 toolbar 左裁 +「刷新」竖排；
  根因是 `UDashboardToolbar` 的 `left` / `right` 槽位没有收缩权限（只改 `span.truncate`
  无效）。开不开 issue 等决定。
- #262 三条 P3 排期处理；多成员批量自愈会改启动期行为，动之前先过决策记录的撤销条件。
- 下次接手时重跑 Security 面板：本轮**未重新核对**，沿用 2026-11-17 日间读数
  advisories 0 / dependabot 0 / secret scanning 0 / code scanning 0。

## Open Questions

- 无新增未决项；唯一待确认即 Next 第一条（settings 页 390 宽缺陷是否开 issue）。
