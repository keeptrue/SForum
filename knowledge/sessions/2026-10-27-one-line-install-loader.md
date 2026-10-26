# 2026-10-27 Session Handoff

## Changed

- 新增 `sforum-install.sh`：一行安装/更新 loader，作为第 10 个固定名 Release
  资产。只做「解析标签 → 下载 bootstrap + SHA256SUMS → 精确校验 → 落盘 →
  exec」，不碰 `.env.production`、`.deployrc`、`deploy/runtime/` 与数据卷。
- 新增 `deploy/scripts/install-loader_test.sh`，挂进
  `scripts/ci/release-tooling-gate.sh`；覆盖 install / upgrade 自动判定、篡改与
  重复 checksum、`SFORUM_INSTALL_SHA256` pin、固定与预发布标签、半装状态、
  非交互降级。
- Release 管线：`build-deploy-asset.sh` 产出新资产，
  `finalize-release-assets.sh` 的矩阵 9→10，`release_assets_test.sh` 同步断言，
  `generate-release-notes.sh` 首屏换成 loader 一行。
- 门禁：`tests/validate-docs.mjs` 的 `validateStableInstallEntry` 改为按文件
  分级——README 与两份 `deployment.md` 只要求一行入口，`one-click.md` 必须保留
  完整审计序列；管道规则收窄为「只有 loader 可管道」。
  `tests/validate-docs_test.sh` 同步 fixture 与负例，
  `tests/validate-sforum-latest.sh` 不再要求 README 展示 raw 包装器。
- 文档：README 首屏与两份 `deployment.md` 换成一行；`one-click.md` 增加
  「一行 loader」小节并保留可审计等价命令；troubleshooting 与 environment
  补 loader 错误语义和 `SFORUM_INSTALL_DIR` / `SFORUM_INSTALL_SHA256`。
- 决策记录：`knowledge/decisions/2026-10-27-one-line-install-loader.md`。

## Decisions

- 只有 loader 允许 `curl | bash`；`sforum-bootstrap.sh` 仍然禁止管道执行。
  首层 loader 未校验，与 `get.docker.com` / rustup 同级，用可审计体积、
  `SFORUM_INSTALL_SHA256` pin、bootstrap 自校验、attestation 和
  `sforum-latest.sh` 补偿。
- 默认装到 `./sforum`；已有 `.env.production` + 带版本的 `.deployrc` 时自动转
  upgrade；半装状态不猜，指向 `sforum-latest.sh`。
- loader 用 `--version "$TAG"` 钉住标签，bootstrap 不再二次解析，避免两次解析
  漂移。
- 非交互执行优先把 stdin 接到 `/dev/tty`；没有终端时打印提示并追加 `--yes`。

## Next

- 下一个 Release 需要实际产出 `sforum-install.sh` 并验证
  `releases/latest/download/sforum-install.sh` 的滚动地址可用。
- 观察 `SFORUM_INSTALL_SHA256` 是否需要在 release notes 里自动带出，方便
  企业环境 pin。
- 短域名入口（例如 `get.sforum.dev`）仍是可选的后续项，需要仓库外 DNS 与
  托管。

## Open Questions

- 旧 Release（无 loader）是否需要回填一个静态 loader 资产？目前靠
  `one-click.md` 的审计命令兜底。
- `SFORUM_INSTALL_SHA256` 的哈希分发渠道：release notes、还是单独的
  `SHA256SUMS` 页面？
