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

## 下一版新增条目（第 15 轮，2026-11-16）

> **版本号留空，定版时按实际 tag 填**：`v3.1.0` 这个标签已经存在
> （`1126c8802`，2026-11-14），本轮的全部提交都在它之后，所以本段条目属于**它之后的
> 那条发布线**，不是 v3.1.0 本身。派单里写的「v3.1.0 升级注意」按「下一版」理解；
> 如果 v3.1.0 实际没有对外发布，再回头把这段并进去。

上面 9 条是第 14 轮及以前的素材。第 15 轮再并入 8 条。**正文以
`docs/zh-CN/deployment/upgrade-notes.md` 与 `docs/en-US/deployment/upgrade-notes.md`
为准**——那一对页面是双语、随发布线滚动维护的正式页，每条都是「旧行为 / 新行为 /
运维要做什么」三栏。这里只留发布 body 需要的条目索引与「必须先做」判断，不复制正文，
避免两处各自漂移。

| # | 条目 | 什么时候做 |
| --- | --- | --- |
| 1 | `install-from-url` 必须提供 sha256（`extension.remote_digest_required`，出站前拒绝） | 升级前（自建脚本 / SDK） |
| 2 | `raw_core` 收窄到显式白名单（3 张派生表 + 1 条序列，越界 `42501`） | 升级后复验插件 |
| 3 | 插件 `global_middleware` 作用域改为显式 `targetId` | 升级后复验插件 |
| 4 | 插件子进程 cwd 钉在制品目录 | 升级后复验插件 |
| 5 | 备份加密口令成为升级硬前置（模板注释块 + 向导提示 + `deploy.sh` 读 `.env.production`） | 升级前 |
| 6 | 匿名 `/ready` 只回摘要（无 `artifacts` / `publicationRevision`） | 按需改脚本 |
| 7 | 401 与 503 分家（C→401、D/E→503 + `Retry-After`） | 按需重生成客户端 |
| 8 | 备份 / 恢复：卷比大小、拓扑守卫双向、多槽各自 deadline、SIGKILL 残留清理 | 演练一次 |

发布 body 里保留的「必须先做」三条：**备份口令**、**`install-from-url` 的 sha256**、
**`raw_core` 插件复验**。第 6 条另带一个同批变化：健康等待三态（`0`/`3`/`1`）与
`deploy.sh` 的 `reason=recovery_only`，已同步进 `one-click.md` 与 `troubleshooting.md`
的 `reason` 表（三种 → 四种）。

## 相关裁决（第 15 轮）

- `GO-2026-5932`（`x/crypto/openpgp`）：**不升级**，见
  `knowledge/decisions/2026-11-16-go-2026-5932-xcrypto-openpgp.md`（三条实证 + 四条
  复位条件）。运营侧文档不提这条内部裁决。
