# 2026-11-15 审计簇 14 收口（供应链与 CI）

簇 14 全簇 **9 条**（RV-14 / RV-15 / RV-18 / RV-41 / RV-42 / RV-43 / FE2-01 /
OPS2-07 / OPS2-09）+ 同域遗留 **5 项**，代码与文档一次落地。总表口径：
有效待修池 **102**，已修 **69**，剩余 **33 = P2 21 / P3 12**（P1 0）。

## 升级前必读：四项行为变更

1. **镜像身份 = 内容 digest**：`upgrade.sh` 拉取前用 `docker buildx imagetools
   inspect` 把 tag 解析成 manifest digest，拉取后核对 `RepoDigests`，不一致直接
   die；`SFORUM_CADDY_IMAGE` 覆盖值必须带 `@sha256:`，裸 tag 会在一次性入口转换
   核对时失败（应用镜像此时可能已拉完）。
2. **升级机需要 `buildx` 插件**：缺插件在拉取前 fail closed 并给出明确报错；
   `deploy.sh` 与直连拓扑不受影响。
3. **`api` / `web` 有 healthcheck**：`api` 打 `/api/v1/ready`（`start_period`
   120s）、`web` 打 `/health`（180s）；`compose.prod.yaml` 与
   `compose.zero-downtime.yaml` 的 `web → api` 依赖升为 `service_healthy`，冷启动
   首次 `compose up` 比以前多等一会儿。
4. **`.env.production.example` 的 `MIGRATE_ON_STARTUP` 改为 `false`**：与
   `configure-production.sh` 一致；`compose.yaml` 缺省仍是 `true`，`false` 只在
   `--env-file` 加载模板时生效。

## Changed

- RV-14：`release-tooling.yml` 的 `paths` 补根级 `deploy.sh` / `upgrade.sh` /
  `sforum-*.sh` 与 `deploy/**`；新增 `scripts/ci/lint-shell.sh`（shellcheck 门，
  baseline 棘轮，缺二进制 fail closed）并接进 tooling job；「仓库根 `*.sh` ⊆
  paths」由 `release-tooling-gate.sh` 逐条断言。
- RV-15：`sforum-latest.sh` + `.sha256` 进 Release 资产矩阵
  （`build-deploy-asset.sh` 复制、`finalize-release-assets.sh` 逐字节校验、
  `release.yml` 发布前断言），不再从 `main` 取。
- RV-18 / OPS2-07：28 个 `go.mod` 抬到 `go 1.26.11`；`apps/api/Dockerfile` 改
  `golang:1.26.11-alpine`；govulncheck 的 `go-version-input` 由 `stable` 改
  `1.26.11`；三处同源由 `release-tooling-gate.sh` 断言。
- OPS2-09：`security.yml` 加 `bun audit --prod --registry
  https://registry.npmjs.org --json`，`SEVERITY_GATE: report`（扫描本身即门，
  退出码 1 区分「有 advisory」与「扫描失败」）。
- RV-43：`upgrade.sh` 的 `resolve_image_digest` / `verify_target_images` /
  `verify_caddy_image`；caddy 统一到
  `caddy:2.11-alpine@sha256:003a97808e781aa9f7e75cc2f43d34d461099031faf5e20faefb9f61126836f0`
  （与 `compose.zero-downtime.yaml` 逐字一致，测试钉住）。
- RV-41：`compose.yaml` 给 `api` / `web` 加 healthcheck；`compose.prod.yaml` 与
  `compose.zero-downtime.yaml` 的 `web → api` 改 `service_healthy`。
- RV-42：`deploy/runtime/` 入库 `.gitkeep` + `.gitignore`，生成的 `Caddyfile*`
  仍不入库；`finalize-release-assets.sh` 的期望清单同步两个占位文件。
- FE2-01：新增 `apps/web/app/runtime/public-extensions/l2StyleLinks.ts`，SSR 预注入
  与客户端换页共用同一份 link 描述（含 `integrity`），预注入 CSS 字节受同一条
  digest 约束。
- 同域 5 项：锁判定对齐（`ps -p` 兜底 + `lstart` 复用检测）、
  `deploy/scripts/deploy-lock.sh` 抽取（`build-deploy-asset.sh` 的 `SOURCE_FILES`
  同步）、`MIGRATE_ON_STARTUP` 对齐、shellcheck 接 CI、路由目录行号契约（PRD2 系列
  改符号引用）。
- 文档面（本轮 writer 收口）：1.26.11 口径（`AGENTS.md`、两份 getting-started、
  两份 development/setup、`modules/backend.md`、`modules/trust-levels.md`）；两份
  `one-click.md` 的 RV-15 命令块与校验链、RV-43 digest 小节；两份
  `deployment.md` 的 buildx 前提与 healthcheck；两份 `manual.md` /
  `environment.md` / `troubleshooting.md` 的 `MIGRATE_ON_STARTUP`、健康检查与三条
  新报错；新增 `reports/2026-11-15-release-upgrade-notes.md`。
  `node tests/validate-docs.mjs` 0 failure。

## Decisions

- **28 个 `go.mod` 一起抬**：`ExtensionPackage/dev_build.go` 的
  `newBackendBuildPlan` 用插件自己 `go.mod` 的 `go` 行钉 `GOTOOLCHAIN`，只抬
  `apps/api` 会让零命令插件回路继续拿旧工具链。
- **三处同源断言**（`go.mod` == Dockerfile 基础镜像 == govulncheck 工具链）落在
  `release-tooling-gate.sh`，任一处单独漂移直接红；不用 `stable`，否则扫描器永远
  比发布物新，发布物带可达漏洞也全绿。
- **镜像身份用 digest，标签降级为发布线自检**：标签可被重推，digest 不能；`edge`
  也必须纳入核对，否则 Caddy 成为唯一不被校验的入口镜像。
- **`sforum-latest.sh` 走 Release 资产而非新增签名清单**：`.sha256` 锚脚本字节、
  脚本锚同标签部署包、attestation 锚构建来源，三层各管一段，不引入第二套信任体系。

## Next

- 前端四簇（簇 9 → 17 → 8 → 16）整批一次过，避免反复动同一批 `admin settings`
  页与 `pages/**` 壳。
- 契约侧单开一批：2 条 302（附件内容直连交付）+ 12 条公开读 401
  （`requireGuestRead`）+ 3 条通配路径形状；先修「状态码集合为空 211/435」的退化
  区，否则新缺口继续隐形，且 `validate-contract-status-parity.mjs` 会要求清基线。
- 审计总表 §11 新轮次与顶部进度表（42 → 33）待提交后补，需要 commit hash。

## Open Questions

- `SEVERITY_GATE: report` 翻 `fail` 的前提是前端清完 5 critical / 44 high；清完后
  是否同时给 `bun audit` 的 advisory 计数加棘轮？
- `extensions/dev/`（gitignored）不在 `go.mod` 一致性断言内，dev 脚手架的 `go` 行
  漂移目前无人守；要不要在 `make:plugin` 模板生成时断言？
- shellcheck 版本未在 workflow 钉（`ubuntu-latest` 预装，随 runner 镜像漂移）；
  要不要固定到 `SFORUM_SHELLCHECK_BIN` + 下载版本？`README.md` 仍写
  “repository-side `sforum-latest.sh` wrapper”，不在本轮写入范围，下次动 README
  时改。
