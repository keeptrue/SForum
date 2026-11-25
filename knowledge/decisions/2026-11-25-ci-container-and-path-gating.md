# 2026-11-25 CI 容器构建与路径门控（M3/M4）

**Status**: accepted
**Scope**: `apps/api/Dockerfile`、`apps/web/Dockerfile`、`apps/web/nuxt.config.ts`、
`.github/workflows/ci.yml`、`.github/workflows/security.yml`、
`scripts/ci/release_workflow_test.rb`

## Decisions

1. **API 镜像只编译一次 `sforum` CLI**。`base` 阶段先构建
   `/usr/local/bin/sforum`，8 个受保护内置插件的 `digest --write` / `validate` /
   `test` 直接调用该二进制，不再重复执行 17 次 `go run ./cmd/sforum`。
2. **内置插件使用受控并发且 fail-fast**。`SFORUM_BUILTIN_BUILD_JOBS` 默认 4；每个
   插件在独立子 shell 中完成 Linux 后端构建、digest、可选 validate 和 extension test。
   监视器每 0.2s 检查同批 PID，任一失败立即终止并回收其余任务，打印全部任务日志。
3. **`migrate` 使用独立 `migrate-build` 阶段**。该阶段只下载 API 模块、复制
   `apps/api/` 并编译 `sforum-migrate`，不触碰 8 个内置插件或 API 二进制。默认
   `build` 阶段只编译 `sforum-api`；已废弃的独立 worker 目标被删除。
4. **Web Nitro 预压缩保持现状**。同一 Bun 1.4.4 容器内的对照结果为：
   brotli+gzip 96.6s / 15,818,123 bytes / 1,089 files；仅 gzip 89.7s /
   13,353,408 bytes / 776 files；关闭预压缩 89.9s / 10,470,114 bytes / 463 files。
   最大节省约 6.9s，未达到任务书要求的 30s 阈值，因此
   `compressPublicAssets: { brotli: true, gzip: true }` 不变。
5. **BuildKit cache mount 不进入 Web Dockerfile**。同一环境无 cache mount 对照为
   109s；Bun/Vite/Nitro cache mount 冷 119s、热 116s，没有收益；缓存
   `.nuxt-build` 还会扩大热构建的模块转换量。保留现有 `type=gha` 层缓存。
6. **CI 路径门控由轻量 `changes` job 统一负责**。使用固定
   `dorny/paths-filter@ceb8a2b8f2d89434be7ff52d3de7ec3738c5cc9d`（v4.0.3），
   输出 `go` / `web` / `contracts` / `container_api` / `container_web` 布尔位，并派生
   container matrix JSON。`workflow_call` 与 `workflow_dispatch` 全部默认 true。
7. **`ci-summary` 是 main push 的成功锚点**。它使用 `if: always()`，依赖
   `changes`、`go`、`web`、`contracts`、`containers`，只接受 `success` 或 `skipped`；
   任一失败/取消都会让整个 `ci.yml` 失败，从而保持 `verify-main-ci.sh` 的 exact-commit
   成功记录契约，同时禁止 `on.push.paths`。
8. **Security 按语言/依赖面跳过重型扫描**。`codeql` 的语言矩阵由 Security
   `changes` job 派生；`govulncheck` 只随 Go 路径运行，`web-audit` 只随 Web
   依赖路径运行。`schedule` 与 `workflow_dispatch` 仍将三类扫描全部置 true。
9. **contracts 的重复 Web 单测只在 Web job 真跑时跳过**。CI env 绑定
   `needs.changes.outputs.web == 'true'`；Web job 被路径门控跳过时，contracts 自己执行
   这 8 个测试。

## Measurements

| 场景 | 优化前 | 优化后 |
| --- | --- | --- |
| `docker build --target api` 无缓存 | 343s | **149s** |
| `docker build --target migrate` 无缓存 | 268s | **62s** |
| `docker build --target prod` | 204s（首次冷网络） | **109s**（同 Dockerfile 复测） |
| 内置插件 build/digest/validate/test | 171s 串行 | **64s，4 路受控并发** |
| API 二进制构建阶段 | 62s | **2.7s**（CLI/插件构建预热 Go cache） |
| Nitro brotli+gzip / gzip / off | 96.6s / 89.7s / 89.9s | 生产配置不变 |
| BuildKit cache mount 冷 / 热 / control | 119s / 116s / 109s | 不采用 |

## Guardrails

- `apps/api/Dockerfile` 首行必须继续是
  `FROM golang:1.26.11-alpine AS base`；RV-18 三处同源校验不可放宽。
- `migrate-build` 不得重新引入内置插件构建或 `sforum-api` 构建。
- 独立 `sforum-worker` 目标已退役；服务端资产契约继续禁止该二进制。
- 路径门控不得改用 `on.push.paths`；所有 main push 必须仍产生成功 `ci.yml` 记录。
- `ci-summary` 的 `needs` 必须覆盖全部下游 gate；新增下游 job 时同步加入。
- `SFORUM_CONTRACTS_SKIP_WEB_UNIT` 不得重新变成无条件跳过。
