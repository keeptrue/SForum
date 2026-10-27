# 2026-10-27 CI 转绿与 v3.0.20-beta.2 发布

## Changed

- **CI 红根因**（提交 `f93817434` 起连续多轮）：
  - 附件新增错误码 `attachments.*` 没有本地化文案 → 改用既有单数命名空间
    `attachment.reconcile_body_invalid` / `attachment.cleanup_body_invalid` /
    `attachment.reconcile_unsupported`，补齐 zh-CN / en-US。
  - `TestCoreRouteCatalogHasExactReviewedGuardParity` 仍按 379 计数；新增
    `core.route.attachments.reconcile_admin` 后应为 380，注释同步。
  - `tests/validate-docs.mjs` 不认识顶层 `newExtensionRecoveryCommand`；补
    `recovery` 映射，并把 `recovery_lifecycle_close.go` /
    `recovery_activation_state.go` / `recovery_recheck.go` 纳入 `extension recovery`
    子命令覆盖；`docs/{zh-CN,en-US}/development/cli.md` 补 `activation-state`、
    `recheck` 与顶层 `recovery` 说明。
- **两个被前一步掩盖的静态契约**（validate-docs 修好后才会执行到）：
  - `tests/validate-identity-ui.js`：用户管理已拆成路由壳 + `users/` 组件 +
    `admin/identity` composable，断言改为检查真实归属（`adminUsersSurface`）。
  - `tests/validate-theme-activation.js`：名称/简介由共享
    `SFAdminExtensionIdentity` 渲染，断言覆盖该组件。
- **Release tooling 红**（`ab5b5310f`）：`600fa68e6` 给插件 Host API 增加
  `SecretService.Put/Clear`（proto + 生成代码），属于共享插件运行时变更；按
  `tests/validate-builtin-plugin-versions.mjs` 契约，8 个内置插件全部 patch 抬版
  （auth-github 1.0.9 / content-policy 1.1.8 / search-site 1.0.8 / shortcodes
  1.3.6 / smtp 1.1.8 / storage-fs 1.1.9 / storage-s3 1.0.11 / web-push 1.0.8），
  基线 `tests/builtin-plugin-release-baseline.json` 同步。校验器漂移报错改为点名
  `shared plugin runtime` 与具体插件版本变化。
- **Release 红**（`v3.0.20-beta.1`）：账号改名 zhuchunshu → keeptrue 后，两个
  Web SDK 包的 repository/homepage/bugs 元数据变化，tarball 与已发布的
  `@sforum/admin-sdk@1.3.0` / `@sforum/plugin-ui@1.1.0` 内容不一致，精确制品
  校验拒绝覆盖 → patch 抬到 1.3.1 / 1.1.1，脚手架依赖范围、authoring-guide 与
  `bun.lock` 同步。
- **GHCR 读后写抖动**：`v3.0.20-beta.2` 的 `Merge candidate / sforum-migrate`
  在 `imagetools create` 成功后立刻 `inspect` 得到 404，重跑即过；`release.yml`
  的合并步骤加 5s 递增、约 60s 上限的 `inspect_with_retry`。

## 发布结果

- tag `v3.0.20-beta.2`（提交 `e3ab91e2e`）已发布为 GitHub prerelease：
  CLI/server 四平台包、`sforum-deploy.tar.gz`、`sforum-install.sh`、`SHA256SUMS`
  等资产齐全；GHCR 三镜像 promote 与匿名拉取校验通过；npm
  `@sforum/admin-sdk@1.3.1`、`@sforum/plugin-ui@1.1.1` 已发布。
- 该提交上 CI、Security、Release tooling 全绿；Release workflow 的
  `Verify main CI` 精确比对了同提交的 main CI run。

## Decisions

- 附件错误码沿用既有 `attachment.*` 单数命名空间，不新开 `attachments.*`。
- SDK 只差元数据时用 patch 抬版，不放宽「同版本必须同内容」的精确制品校验。
- 共享插件运行时变更按契约抬全部内置插件版本，不做基线特批。
- 注册表读抖动用有限重试处理，校验强度不变。

## Next

- 下次发布确认 `inspect_with_retry` 在真实流水线生效（本次 beta.2 用的还是旧
  workflow，重跑才过）。
- 若还要发 stable，从 `v3.0.20-beta.2` 之后的新提交重新走 tag 流程。

## Open Questions

- `v3.0.19` 的 Release 从未产出（verify-ci 撞上红色 main CI），编号保留不复用；
  是否需要补一条 release note 说明该版本无制品。
