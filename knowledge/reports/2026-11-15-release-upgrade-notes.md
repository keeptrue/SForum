# 下一版发布说明 ·「升级注意」（草稿）

本文件是下一版 release body 的「升级注意」段素材，定版时整段并入
`YYYY-MM-DD-vX.Y.Z-release-notes.md` 的 `## 升级注意`。这里不写版本号，避免与
tag 冲突；条目按「必须动作 / 行为变更 / 回滚风险」排列。

## 升级注意

1. **必须动作：升级机装 `buildx` 插件** —— `upgrade.sh` 在拉取前把每个目标
   tag 解析成 manifest digest，用的命令是 `docker buildx imagetools inspect`。
   缺插件时脚本在**任何拉取之前** fail closed：
   `Could not resolve the manifest digest of <image>. Docker with the buildx
   plugin (docker buildx imagetools) is required to verify image identity.`
   先跑一次 `docker buildx version` 确认有输出。`deploy.sh` 与直连拓扑不受影响。

2. **必须动作：`SFORUM_CADDY_IMAGE` 覆盖值带 `@sha256:`** —— 只写 tag 时，一次性
   入口转换会 die 在
   `SFORUM_CADDY_IMAGE must pin a digest (name@sha256:...), got '<value>'.`
   （此时三个应用镜像可能已拉完）；在线更新不重建 `edge`，不会重新校验。不需要
   自定义就删掉这一行，用默认钉版
   `caddy:2.11-alpine@sha256:003a97808e781aa9f7e75cc2f43d34d461099031faf5e20faefb9f61126836f0`。

3. **行为变更：镜像身份是内容 digest，不是标签** —— 拉取后核对本地
   `RepoDigests` 是否仍是解析出的 digest；标签被重推会 die 在
   `<image> image content identity mismatch: ...`，不会静默跑另一个构建。版本
   label 仍然检查，但只作为发布线自检。受影响面：所有走 `upgrade.sh` 的升级，
   包括蓝绿在线更新与一次性入口转换。

4. **行为变更：Caddy 统一到 `caddy:2.11-alpine@sha256:003a97808e781aa9f7e75cc2f43d34d461099031faf5e20faefb9f61126836f0`**
   —— 修掉旧 `upgrade.sh` 拉 `caddy:2.10.2-alpine`、Compose 用 `2.11-alpine`
   的漂移。`upgrade.sh` 与 `compose.zero-downtime.yaml` 两处钉同一个 digest，
   由测试钉住一致。

5. **行为变更：`api` / `web` 有 healthcheck，web 等 API 就绪** —— `api` 探
   `/api/v1/ready`（`start_period` 120 秒），`web` 探 `/health`（180 秒）；
   `compose.prod.yaml` 与 `compose.zero-downtime.yaml` 的 `web → api` 依赖是
   `service_healthy`。API 没就绪时 web 不再提前接流量，代价是全新 `compose up`
   首次冷启动比以前多等一会儿，`docker compose ps` 会先 `starting` 再 `healthy`。

6. **行为变更：`.env.production.example` 的 `MIGRATE_ON_STARTUP` 改为 `false`** ——
   与 `configure-production.sh` 一致。口径要分清：`compose.yaml` 的缺省仍是
   `true`，`false` 只在 `--env-file` 加载模板时生效；完全不加载模板时 API 仍会
   自迁移。手工部署同样要先跑 `migrate`。

7. **行为变更：`sforum-latest.sh` 改为不可变 Release 资产** —— 脚本与
   `sforum-latest.sh.sha256` 现在是同一标签的两个资产：`.sha256` 锚定脚本字节 →
   脚本从**同一个 tag** 下载 `SHA256SUMS` 与 `sforum-deploy.tar.gz` 并校验 →
   `gh attestation verify` 可验构建来源。更早的 Release 没有这两个资产，那些
   版本只能继续按 `main` 分支的 raw 地址取脚本，校验不构成锚点。

8. **必须动作（自建镜像）：工具链抬到 Go 1.26.11** —— `apps/api/go.mod` 与另外
   27 个模块（8 个内置插件、17 个 fixture 插件、`tests/compat`、`tools/proto`）
   的 `go` 指令、`apps/api/Dockerfile` 的 `golang:1.26.11-alpine`、CI 的
   govulncheck 扫描工具链三处同源，由 `scripts/ci/release-tooling-gate.sh` 逐条
   断言。用官方镜像的运营方无需动作。

9. **无操作项：`deploy/runtime/` 入库两个占位文件** —— `.gitkeep` 与
   `.gitignore`，生成的 `Caddyfile*` 仍不入库。fresh clone 不再缺目录。

**回滚风险**：回退到旧 Release 的 `upgrade.sh` 时，它不核对 digest、仍按 tag
拉取，这是旧脚本的既有行为，不是本次引入的回归；但那时 `SFORUM_CADDY_IMAGE`
的 digest 写法不会被校验，请自行确认拉到的镜像内容。
