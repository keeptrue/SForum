# 2026-11-24 CI/CD 与测试全链路二次提速任务书

**Status**: active（2026-11-25；M0–M4 landed，M5–M6 未批准）
**Owner**: 2026-11-24 session
**前置基础**: 接续 [2026-10-23 全量测试提速任务书](./2026-10-23-test-suite-speedup.md)（已完成集群分片、`--with-compat`、`test.sh` 三线并行与 `--fast`）。

## 1. 量化目标

| 场景 | 当前实测基线 | 优化后目标 | 主要贡献阶段 |
| --- | --- | --- | --- |
| 本地 `./scripts/test.sh`（全量冷跑） | `api` 391s / `static` 268s / `web` 214s（总壁钟 ~6.5 min） | **150-180s**（2.5-3 min） | M1 + M2 + M3 |
| 本地 `./scripts/test.sh`（命中缓存） | 因 `-count=1` 强制重跑，约 391s | **60-90s** | M1 + M2 |
| 本地 `./scripts/test.sh --fast` | 无跨轮 Go 缓存，仍受 `shard 1` 长尾拖累 | **45-75s** | M1 + M5 |
| GitHub Actions `CI`（无关改动的 PR） | 无路径跳过，`go`/`web`/`containers` 全跑 | 单门跳过 < 15s，`main` 仍产出 `success` 状态 | M0 + M4 |
| GitHub Actions `CI`（全量跑） | 偏斜分片 + 无 Bun/Proto/Build 缓存 + 死 Postgres service | **壁钟压缩 40%-65%** | M0 + M1 + M2 + M3 |
| `Release tooling`（`release-tooling-gate.sh`） | 串行跑 24 个脚本 + 200k KDF + 硬编码 sleep（约 200s+） | **40-65s** | M2 |
| `Release` 镜像（`sforum-migrate` / `sforum-api`） | `migrate` 重编 8 插件 + 17 次 `go run` + 废弃 `worker` | `migrate` 耗时降 **> 70%**，`api` 省去 16 次重编 | M3 + M6 |

## 2. 实测瓶颈诊断（基于 `tests/.logs/` 与源码核验）

### 2.1 Go 隔离测试（`api` 线 391s，占绝对长尾）

1. **分片严重偏斜（`scripts/test-api-isolated.sh:299-307`）**：
   当前按 `go list ./...` 序号 `i % total == index` 轮转分片。实测 177 个列举包（158 个含测试包）总耗时 **692.49s**（4 分片理论均衡值 **173.1s**），实际分配如下：
   - `shard 0`: **93.99s**（`Models/Forum` 36.80s、`Models/Badges` 21.41s、`app/Http` 15.67s、`Support/CompatFarm` 7.32s）
   - `shard 1`: **307.60s**（`Models/Extensions` 122.84s、`Models/Identity` 70.93s、`Extensions/IntegrationTests` 49.24s、`bootstrap` 28.65s、`cmd/sforum` 16.35s）
   - `shard 2`: **203.29s**（`database/migrator` 96.93s、`Support/HostAPI` 46.71s、`Support/IdentityRegistry` 32.89s、`Support/ThemeCompiler` 6.87s）
   - `shard 3`: **87.61s**（`Support/Extensions` 70.17s、`Models/Moderation` 2.9s、`Models/TrustLevels` 2.8s）
   最慢的 `shard 1` 是理想均值的 1.78 倍、是最快分片的 3.5 倍。
2. **硬编码 `-count=1` 废掉了固定缓存键（`scripts/test-api-isolated.sh:326,397`）**：
   脚本 `:19-26` 专门固定 `55432+i` 与 `56379+i` 作为 Go 测试缓存键，但下面执行时强制写死 `-count=1`，导致本地日常改单包时 158 个未改动包每次依然全跑。
3. **`--with-compat` 串行拉长尾部（`scripts/test-api-isolated.sh:391-403`）**：
   全量分片跑完并销毁 4 套容器后，才新起第 5 套 PG + Redis 容器、重跑全套 Goose 迁移，并在 `CompatFarm` 中现场编译 `sforum-smtp` 与 `sforum-search-site`（`apps/api/app/Support/CompatFarm/run.go:241,308`）。
4. **容器启动探测硬等 1s 步长（`scripts/test-api-isolated.sh:127-139,207-219`）**：
   每套 PG + Redis 需连续两次就绪探测，每次间隔 `sleep 1`，5 套容器叠加上下线耗时明显。
5. **头部重包内部串行重放迁移**：
   `app/Models/Extensions`（122.84s）、`database/migrator`（96.93s，34+ 文件仅 1 处 `t.Parallel()`）、`app/Models/Identity`（70.93s）、`app/Support/HostAPI`（46.71s）大量测试各自实例化 `goose.NewProvider` 并串行执行 178+ 条 SQL 迁移。

### 2.2 静态与发布工具链门禁（`static` 线 268s）

1. **`upgrade.sh` 重试睡眠（`upgrade-version-selection_test.sh` 约 33.2s）**：
   `upgrade.sh:370`（`fetch_with_retry`）与 `:545`（`pull_with_retry`）写死 `sleep $((attempt * 2))`（每次失败多睡 2s+4s），`:897,958` 也写死 `sleep 2` / `sleep 1`。
2. **200,000 次 PBKDF2 循环测试（`postgres-safety_test.sh` 约 34.5s）**：
   `deploy/scripts/postgres-backup-format.sh:33,93,138,176` 硬编码 `200000` 次迭代，41 个加密/解密/篡改回归用例绝大多数都在反复跑高强度密钥派生。
3. **串行执行约 24 个发布工具测试（`scripts/ci/release-tooling-gate.sh:105-176`）**：
   `deploy_test.sh`（~23.4s）、`install-loader_test.sh`（~9.5s）等全串行；其中 `tests/validate-docs_test.sh`（584 行）通过子进程重启 `node tests/validate-docs.mjs` 约 27 次（单次全仓扫描约 2.7s）。
4. **契约验证重复全仓扫描与重复单测（`scripts/ci/contracts-gate.sh:18-60,118-131`）**：
   - 4 个 `validate-contract-*parity*.test.mjs` 测试共启动约 8 次全仓库 `validate-contract-status-parity.mjs`（单次 ~2.4s）；
   - `validate-contract-reachability.test.mjs:135` 同步串联 `spawnSync` 跑 10 套夹具；
   - `:120-128` 单独重跑 8 个 `bun test` 文件，而 `web-gate.sh:17`（与 `test.sh` 的 `web` 线）已全量覆盖它们。

### 2.3 Web 构建与容器镜像（`web` 线 214s + Docker 构建）

1. **`apps/api/Dockerfile:13-46` 重复编 CLI 与串行插件验证**：
   `base` 阶段先串行构建 8 个内置插件，紧接着执行 **17 次 `CGO_ENABLED=0 go run ./cmd/sforum`**，每次都要重复编译解析 `cmd/sforum` 及其依赖图。
2. **`apps/api/Dockerfile:53-67,94-102` 阶段耦合**：
   `build` 阶段强行依次编译 `sforum-api`、已废弃独立部署的 `sforum-worker`（`AGENTS.md` 与 `finalize-release-assets.sh:66` 已禁用独立 worker）和 `sforum-migrate`。导致 `release.yml` 中的 `sforum-migrate`（`amd64` + `arm64`，且 `cache_writer: false`）在只需求一个轻量迁移二进制时，也必须先把 `base` 的 8 个插件、17 次 `go run`、`sforum-api` 和 `sforum-worker` 全做一遍。
3. **Nitro 构建占比偏高（`apps/web/nuxt.config.ts:114`）**：
   实测 `nuxt build` 2m 28s 中，`client` 36.1s、`server` 35.0s、`nitro` 高达 **1m 09s**（含 `compressPublicAssets: { brotli: true, gzip: true }` 及 `.output/server` 63.2 MB 打包）。

### 2.4 GitHub Actions 工作流浪费

1. **死 Service 与缺失缓存（`.github/workflows/ci.yml` / `release-tooling.yml` / `security.yml`）**：
   - `ci.yml:35-48,66` 声明了未实际使用的 `services.postgres`（`:29-34` 注释已写明脚本会覆盖它）；
   - `ci.yml`（`web` / `contracts`）、`release-tooling.yml`、`security.yml`（`web-audit`）均未缓存 `~/.bun/install/cache`；
   - `ci.yml` `contracts` 未缓存 `tools/proto/.bin`（`scripts/proto.sh:12-20` 每次冷编译 `buf` 等三个二进制）及 `actionlint@v1.7.12`（`:145` 每次 `go run`）；
   - `ci.yml` `web` 未跨 run 缓存 `.nuxt-typecheck.tsbuildinfo` / Vite 产物；
   - `ci.yml:53,130` 默认 `fetch-depth: 0` 拉全仓历史。
2. **缺少按路径跳过重任务**：
   `ci.yml` 与 `security.yml` 对任意改动（如纯文档或单端修改）都全量运行全部重型检查与镜像构建。
3. **发版链路无依赖阻塞（`.github/workflows/release.yml:394-397`）**：
   `release-assets` 中的 `darwin/amd64`、`darwin/arm64` 仅交叉编译 CLI 二进制（不从 GHCR 拉镜像），目前却挂在 `needs: merge-candidate` 之后干等双平台容器编译与扫描完成。

## 3. 不可逾越的门禁与守护约束（实施红线）

1. **R1（`verify-main-ci.sh` 主线契约）**：`release.yml:95` 调用 `scripts/ci/verify-main-ci.sh:25-61`，强制要求拟发版的 `main` commit 拥有一条 `workflow=ci.yml, event=push, branch=main, status=completed, conclusion=success` 记录。**禁止在 `ci.yml` 使用 `on.push.paths`**：必须改用轻量前置 `changes` job 配合下游 `if:` 跳过 + 终态汇总 job，使任何 `main` 提交都能产出一份 `success` 的 `ci.yml` 运行。
2. **R2（`release_workflow_test.rb` 结构守卫）**：`scripts/ci/release_workflow_test.rb` 对 `ci.yml` 与 `release.yml` 施加了严格断言（`containers` `needs` 为空、Web 镜像扫描仅报告不阻断、PR 不写 max 缓存、6 路 native runner 无 QEMU、Trivy 先于 digest 上传、`merge-candidate` 与 `promote` 覆盖三镜像等）。凡改动工作流 DAG，必须在同一 PR 显式评审并同步更新 `release_workflow_test.rb` 及其说明，严禁绕过测试。
3. **R3（RV-14 覆盖自检）**：`scripts/ci/release-tooling-gate.sh:48-54` 扫描自身 `^\./${script}([[:space:]]|$)` 以保证 `deploy/scripts/*_test.sh` 全量执行，`:33-41` 断言根 `*.sh` 全数登记在 `release-tooling.yml`。并行化重构必须保留或同步更新 RV-14 自检逻辑。
4. **R4（RV-18 工具链三处同源）**：`release-tooling-gate.sh:68-103` 校验 `apps/api/go.mod`、`apps/api/Dockerfile` 首行 `FROM golang:` 与 `security.yml` 的 `govulncheck` 完全一致。重写 `Dockerfile` 时不得破坏首行 `FROM golang:<ver>-alpine` 解析与同源校验。
5. **R5（Parity 与 Reachability 互相独立）**：`contracts-gate.sh:56-59` 与 `tests/validate-contract-reachability-equivalence.test.mjs` 要求两套检查不共享解析实现，以便互相复核盲区。优化只能合并同一门内的重复进程启动，禁止合并两门解析器。
6. **R6（证据与 CI 防假绿）**：CI（`CI=true`）及证据采集脚本中的 Go 测试必须保留 `-count=1` 禁止命中跨轮测试缓存；测试缓存仅限显式日常开发循环，并在输出中明示缓存模式。
7. **R7（固定缓存端口并发隔离）**：同一宿主机同时跑两轮全量 Go 测试会回退临时端口（损失缓存但不得抢占彼此容器，见 `scripts/test-api-isolated.sh:19-26`）。
8. **R8（安全与健康就绪底线）**：PG/Redis 缩短探测等待间隔后仍必须保持「连续两次真实认证 / 发布端口 PONG」判据；生产备份默认迭代次数 `200000` 不得下调，且必须有显式用例验证 `200000` 默认行为与信封格式版本。

## 4. 分期改造工作项（M0-M6）

### M0：CI 废耗清理与零风险缓存（优先落地）

- **改动范围**：
  - `.github/workflows/ci.yml`
  - `.github/workflows/release-tooling.yml`
  - `.github/workflows/security.yml`
  - `scripts/ci/contracts-gate.sh`
- **具体改法**：
  1. 删除 `ci.yml:35-48` 未被使用的 `services.postgres` 及其 `:66` 的 `SFORUM_COMPAT_DATABASE_URL` 环境变量（更新 `:29-34` 注释）。
  2. 为 `ci.yml`（`web` / `contracts`）、`release-tooling.yml`（`tooling`）、`security.yml`（`web-audit`）接入 `actions/cache` 缓存 `~/.bun/install/cache`（key 绑定 `${{ runner.os }}-bun-${{ hashFiles('apps/web/bun.lock') }}`）。
  3. `ci.yml` `web` 缓存 `apps/web/.nuxt-typecheck.tsbuildinfo`（位于 `.nuxt-typecheck/` 目录外，避免被 `nuxt prepare` 清除）及 `.nuxt-build` / `node_modules/.cache`。
  4. `ci.yml` `contracts` 缓存 `tools/proto/.bin`（key 绑定 `tools/proto/go.sum` + `scripts/proto.sh`），并将 `:145` 的 `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12` 改为编译到缓存工具目录或预置版本校验缓存，免去每次冷编译。
  5. 将 `ci.yml:53,130` 的 `fetch-depth: 0` 改为 `fetch-depth: 2`（对 PR / push 按需浅拉 `SFORUM_BUILTIN_VERSION_BASE` 对应 commit，如 `git fetch --no-tags --depth=1 origin "$BASE_SHA"`，确保 `validate-builtin-plugin-versions.mjs` 能读到基线即可）。
  6. `contracts-gate.sh:118-131` 增加条件控制（如 `SFORUM_CONTRACTS_SKIP_WEB_UNIT=1`）：当本地 `test.sh`（已全跑 `web` 线）或同一 CI run 中 `web` job 必然执行时跳过这 8 个重复 `bun test` 文件；单独触发 `contracts`（如在 M4 路径过滤下只改契约未改前端时）仍执行该 8 个用例。
- **验证**：`go-gate`、`web-gate`、`contracts-gate`、`release_workflow_test.rb` 全绿；二次命中缓存时依赖安装与工具准备阶段明显下降。

### M1：Go 隔离测试分片均衡与调度提速

- **改动范围**：
  - `scripts/test-api-isolated.sh`
  - `scripts/test.sh`
  - `AGENTS.md`（仅补充缓存环境变量说明）
- **具体改法**：
  1. **LPT 加权分片替代轮转（`scripts/test-api-isolated.sh:299-307`）**：
     在脚本内用 `awk` / 关联数组维护重点慢包的静态耗时权重表（例如 `app/Models/Extensions=123`、`database/migrator=97`、`app/Models/Identity=71`、`app/Support/Extensions=70`、`app/Support/Extensions/IntegrationTests=49`、`app/Support/HostAPI=47`、`app/Models/Forum=37`、`app/Support/IdentityRegistry=33`、`bootstrap=29`、`app/Models/Badges=21`、`cmd/sforum=16`、`app/Http=16`，其余包默认权重 `1`），先按权重降序排序再用 LPT 贪心分配到当前累计权重最小的分片。目标 4 分片最大片由 **307.6s** 压至 **~175-185s**（总和 692s 均值 173s）；本地多核可将 `SFORUM_TEST_SHARDS` 上限评估放到 `min(nproc, 6)`。
  2. **可控缓存策略（`scripts/test-api-isolated.sh:326,397`）**：
     引入 `SFORUM_TEST_CACHE`（默认：当 `CI=true` 时 `0`，强制 `-count=1`；本地交互/日常 `test.sh` 可设为 `1` 或通过 `--cache` / `--no-cache` 显式控制；关键验收与基线取证脚本显式传入 `SFORUM_TEST_NO_CACHE=1` 强制 `-count=1`）。脚本首行日志打印缓存状态，避免误拿缓存结果当门禁证据。
  3. **`--with-compat` 与分片协同（`scripts/test-api-isolated.sh:391-403`）**：
     全量分片模式下将 `compat`（使用端口 `preferred_port + total` 与 `redis_preferred_port + total`）作为并发后台任务与分片同时运行，或接在最轻分片内复用已迁移的同一集群，消除第 5 套容器冷启与迁移对总壁钟的纯串行追加。
  4. **就绪探测微步长（`scripts/test-api-isolated.sh:127-139,207-219`）**：
     把 `sleep 1` 改为 `sleep 0.2`（循环上限改为 `150`，总超时仍为 30s），保留「连续两次真实认证/发布端口 PONG」条件不变（R8）。
- **验证**：本地 `SFORUM_TEST_NO_CACHE=1 ./scripts/test-api-isolated.sh --with-compat` 连跑 2 轮零 flaky，记录 4 分片各自耗时与均衡度（目标 `max_shard / avg_shard <= 1.15`）。

### M2：静态门禁与部署 Shell 测试提速

- **改动范围**：
  - `upgrade.sh`、`deploy/scripts/upgrade-version-selection_test.sh`
  - `deploy/scripts/postgres-backup-format.sh`、`deploy/scripts/postgres-safety_test.sh`
  - `scripts/ci/release-tooling-gate.sh`
  - `tests/validate-docs.mjs`、`tests/validate-docs_test.sh`
  - `scripts/ci/contracts-gate.sh`、`tests/validate-contract-status-parity*.mjs`、`tests/validate-contract-reachability.test.mjs`
- **具体改法**：
  1. **`upgrade.sh` 重试退避可配置**：
     在 `upgrade.sh:370,545` 使用 `retry_sleep="${SFORUM_UPGRADE_RETRY_SLEEP_SECONDS:-2}"`，仅在测试脚本 `upgrade-version-selection_test.sh` 中设 `0`（并保留 1 条断言检查默认退避计算正确），预计 `33.2s -> < 2s`。
  2. **备份 PBKDF2 测试可配覆盖（严守 R8）**：
     在 `deploy/scripts/postgres-backup-format.sh:33,138,176` 将加解密 `-iter` 统一读取 `${SFORUM_BACKUP_KDF_ITER:-200000}`；`postgres-safety_test.sh` 先用 1 个加解密闭环用例以默认 `200000` 验证兼容性与 `iter200000` 信封契约，其余状态机/篡改/轮转测试启用 `SFORUM_BACKUP_KDF_ITER=1000`，预计 `34.5s -> ~2-3s`。
  3. **`release-tooling-gate.sh` 并行调度 + 同步更新 RV-14（严守 R3）**：
     将 `:105-176` 的 ~24 个独立脚本改为受控并发跑（默认 `min(nproc, 4)`，每个测试输出落到独立缓冲并在失败时完整打印）；同时将 `:48-54` 的 RV-14 正则自检改造为直接针对并行任务登记列表校验，确保漏登记任何 `deploy/scripts/*_test.sh` 依然必红。
  4. **`tests/validate-docs_test.sh` 提速**：
     保留 2 条子进程黑盒用例验证 CLI 参数/退出码，对其余 25 个夹具启用并发执行（`xargs -P 4` 或后台 job pool）或由 `validate-docs.mjs` 提供单进程批跑入口，预计 `40s+ -> 4-6s`。
  5. **契约验证去重并发（严守 R5）**：
     - `validate-contract-status-parity*.test.mjs`：四个文件内的真实仓库全仓比对保留各跑一次（或由独立 `--report-json` 参数在 gate 首步输出只读 JSON 快照供同门其余断言消费，各自仍独立启动并跑完合成失败树测试），消除约 6-7 次重复全仓扫描；
     - `validate-contract-reachability.test.mjs:135`：将 10 套隔离临时夹具的 `spawnSync` 改成有限并发 `spawn`（并发度 4-6）；
     - `contracts-gate.sh`：将互相无状态依赖的纯静态脚本（如 `validate-docs`、`validate-architecture-boundaries`、`validate-homepage`、`validate-identity-ui`、SEO/Theme/SF 校验）分组并发。
- **验证**：`./scripts/ci/release-tooling-gate.sh` 与 `./scripts/ci/contracts-gate.sh` 全绿；故意删掉一个 `deploy/scripts/*_test.sh` 注册行或故意改坏等价门验证门禁仍能准确报红。

### M3：容器构建与 Web 构建提速

- **改动范围**：
  - `apps/api/Dockerfile`
  - `apps/web/nuxt.config.ts`、`apps/web/Dockerfile`
  - `scripts/build-builtin-plugins.sh`（可选：同步受益于单次编 `sforum` CLI）
- **具体改法**：
  1. **`apps/api/Dockerfile` 优化（严守 R4）**：
     - 首行保持 `FROM golang:1.26.11-alpine AS base`（满足 RV-18 正则）；
     - 先执行一次 `go build -o /usr/local/bin/sforum ./cmd/sforum`，再让后续 17 条 `digest --write` / `validate` / `test` 直接调用 `/usr/local/bin/sforum`，消灭 16 次完整重编译；
     - 8 个内置插件的 `go build` 与 `sforum extension test` 改成受控并发（如 4 并发），失败立即中断；
     - 新增独立 `FROM golang:1.26.11-alpine AS migrate-build`，仅执行 `go mod download`、`COPY apps/api/` 与 `go build -o /out/sforum-migrate ./cmd/migrate`，供 `AS migrate` 直接 `COPY --from=migrate-build`，彻底甩掉 `base` 里的 8 插件编译与 `sforum-api` 构建；
     - 清理默认 `build` 阶段里的 `sforum-worker` 编译（确认 `compose*.yaml`、`release-smoke.sh`、`finalize-release-assets.sh` 均已不使用并禁止独立 worker；如有兼容需求可单独拆成按 `--target worker` 才触发的 `worker-build` 阶段）。
  2. **`apps/web` Nitro 压缩与构建缓存**：
     - 对 `apps/web/nuxt.config.ts:114` 的 `compressPublicAssets: { brotli: true, gzip: true }` 做一次可度量对照实验：记录开启 `brotli+gzip`、仅 `gzip`、及关闭预压缩（由前置 Caddy 处理）三组对 `nitro` 阶段耗时（当前 **1m 09s**）与 `.output/public` 体积的影响，如仅保留 `gzip` 或交由 Caddy 能省下 30s+ 且符合生产代理拓扑，先立决策再改。
     - 在 `apps/web/Dockerfile` 中对比验证 BuildKit `--mount=type=cache` 对 Bun 缓存与 Vite/Nitro 缓存的效果（不破坏现有 `type=gha` 命中）。
- **验证**：本地分别 `docker build --target api`、`--target migrate`、`--target prod` 计时对比；`ruby ./scripts/ci/release_workflow_test.rb` 与 `./scripts/ci/release-tooling-gate.sh` 的 RV-18 校验全绿。

### M4：GitHub Actions 路径门控（按变更精确跳过）

- **改动范围**：
  - `.github/workflows/ci.yml`
  - `.github/workflows/security.yml`
  - `scripts/ci/release_workflow_test.rb`（按 R2 同步评审更新）
- **具体改法**：
  1. **`ci.yml` 引入秒级 `changes` 探测 job（严守 R1）**：
     - 输出 `go`、`web`、`contracts`、`container_api`、`container_web` 布尔位（`workflow_call` / `workflow_dispatch` 默认全 `true`）；
     - `go` / `web` / `contracts` / `containers` 按对应输出位设置 `if:` 条件，并在 `containers` matrix 中按 `container_api` / `container_web` 精准决定构建哪几个镜像；
     - 末尾增加极轻量的 `ci-summary` job（`if: always()`，`needs: [changes, go, web, contracts, containers]`），校验所有依赖 job 结果必须为 `success` 或 `skipped`，确保即使跳过全部重任务，该次 `main` push 的 `ci.yml` 状态也严格以 `success` 落库供 `verify-main-ci.sh` 检查。
  2. **同步更新 `scripts/ci/release_workflow_test.rb:160-163`（严守 R2）**：
     允许 `ci.yml` `containers` 依赖 `changes`（`container_needs == ["changes"]`），但严禁依赖 `go` / `web` / `contracts` 等重 gate；保持其余全部发版、扫描与缓存规则不变。
  3. **`security.yml` 路径过滤**：
     在 `pull_request` / `push` 上对 `codeql`（Go vs JS/TS）、`govulncheck`、`web-audit` 接入对应路径条件（无匹配时跳过），同时保留每周一 `schedule` 与 `workflow_dispatch` 的全量兜底扫描。
- **验证**：构造纯 `docs/` 改动与单端改动测试 `changes` 输出；运行 `ruby ./scripts/ci/release_workflow_test.rb` 与 `actionlint` 全绿；验证模拟 `verify-main-ci_test.sh`。

### M5：Go 数据库长尾包内部提速（二次深挖）

- **改动范围**：
  - `apps/api/internal/testdb/`（及迁移辅助）
  - `apps/api/database/migrator/`（96.93s）
  - `apps/api/app/Models/Extensions/`（122.84s）
  - `apps/api/app/Models/Identity/`（70.93s）
  - `apps/api/app/Support/HostAPI/`（46.71s）
  - `apps/api/app/Support/CompatFarm/run.go`
- **具体改法**：
  1. **Template DB / 共享迁移基线**：在测试辅助中为高频重包提供 `TestMain` / `sync.Once` 预先跑通一次 178+ 条 Goose 迁移的只读模板库，用例通过 `CREATE DATABASE ... TEMPLATE ...` 或轻量化 schema 拷贝获得干净环境，避免成百上千次重复执行全量 DDL。
  2. **按包渐进开启 `t.Parallel()`**：先隔离修改包级全局变量 / `os.Setenv` / 固定租约 ID 的测试，再给其余独立 Postgres 测试加 `t.Parallel()`，每包用 `go test -race -count=5` 核验稳定性。
  3. **`CompatFarm` 复用预编译内置插件**：允许通过选项或环境变量传入已验证 digest 的预编内置插件目录，在源码干净时跳过 `CompatFarm` 中的现场 `go build`。

### M6：Release 发版关键路径并行化（可选）

- **改动范围**：
  - `.github/workflows/release.yml`
  - `scripts/ci/release_workflow_test.rb`
- **具体改法**：
  将 `release-assets`（`:394-415`）中的 `darwin/amd64`、`darwin/arm64` CLI 打包拆出为独立 job（仅依赖 `[prepare, verify-ci]`），不再白等 `merge-candidate` 的容器编译与合并完成；`linux/amd64`、`linux/arm64` 因需从扫描通过的 GHCR 镜像提取 `sforum-api` / `sforum-migrate` 二进制，仍保持依赖 `merge-candidate`。`verify-release-assets` 聚合两组产物后再统一做校验和与 attestation，并同步更新 `release_workflow_test.rb:128-132`。

## 5. 建议实施顺序与验收矩阵

1. **第一批（低风险高回报，1-2 个 PR）**：**M0 + M1 + M2**（不改镜像拓扑与 `release.yml`，可立刻把本地与 PR 跑测压下来）。
2. **第二批（工作流与构建大头）**：**M3 + M4**（需要同步修改并评审 `release_workflow_test.rb` 与 `ci.yml`）。
3. **第三批（长尾包深优与发版压缩）**：**M5 + M6**（按包逐个放开并做 5 轮稳定性验证）。

每完成一个 Milestone 必须过齐对应门禁：
- 本地全量：`./scripts/test.sh`（观察 `tests/.logs/*.elapsed`）
- 门禁单项：`./scripts/ci/go-gate.sh`、`./scripts/ci/web-gate.sh`、`./scripts/ci/contracts-gate.sh`、`./scripts/ci/release-tooling-gate.sh`
- 守护套件：`ruby ./scripts/ci/release_workflow_test.rb`、`./scripts/ci/verify-main-ci_test.sh`、`node tests/validate-docs.mjs`、`node tests/validate-architecture-boundaries.mjs`

## 6. 执行记录（2026-11-24）

### 6.1 范围与结论

- 以下 6.1–6.5 保留为 M0–M2 批次的历史记录；M3/M4 的 2026-11-25 实施证据见 6.6。
- **M0–M2 已完成**；M3（Docker/Web 构建）、M4（workflow 路径门控）、M5（Go 重包内部
  模板库/并行）、M6（`release.yml` 关键路径）均未修改，等待明确批准。
- 用户现有 `.codegraph/`、工作区分支与 3000 端口服务未改动；未运行 pnpm。
- 额外修复：Chrome 154 在部分宿主默认无头参数下会卡住 loopback `Page.navigate`，
  CSP 自检增加后台网络/同步/扩展隔离参数；CSP 断言未变化。

### 6.2 实测耗时

| 场景 | 优化前 | 优化后 |
| --- | --- | --- |
| `./scripts/test.sh` 全量 | api 391s / web 214s / static 268s | **api 288s / web 244s / static 80s，壁钟 287.5s** |
| 无缓存隔离 round 1 | 最大分片 307.6s，总包耗时 692.5s | **209 / 215 / 188 / 197s，壁钟 216.1s** |
| 无缓存隔离 round 2 | — | **205 / 210 / 185 / 195s，壁钟 212.0s** |
| `./scripts/test.sh --fast` | 历史约 41–75s，受分片长尾影响 | 冷缓存 295.5s；稳态热缓存 **69.2s**（api 29 / web 69 / static 30） |
| `release-tooling-gate.sh` | 本机 npm 子进程受限前已 155.3s；任务书约 200s+ | **45.3s** |
| `contracts-gate.sh` | 重复 parity 全仓扫描约 9 次 | 独立入口 **17.1s**；skip 重复 web 单测 **12.5s** |
| `upgrade-version-selection_test.sh` | 33.9s | **3.5–6.1s** |
| `postgres-safety_test.sh` | 32.3s | **18.4–29.5s**（并发负载下取上限） |
| `validate-docs_test.sh` | 5.9s | **4.3–8.0s**（并发负载下取上限） |

两轮最终分片 `max/avg` 分别为 **1.063 / 1.039**，均低于 1.15。全量壁钟下降约
**26.5%**，但 web build 仍是主要长尾；150–180s 目标需要 M3/M5 继续推进。

### 6.3 门禁与证据

- `SFORUM_TEST_NO_CACHE=1 ./scripts/test-api-isolated.sh --with-compat` 两轮均 exit 0，
  四分片完整输出耗时；compat 在最轻分片复用同集群，单独耗时 3–5s。
- `./scripts/ci/release-tooling-gate.sh`、`./scripts/ci/contracts-gate.sh`、
  `./scripts/test.sh`、`./scripts/test.sh --fast` 全绿。
- 临时删除 `deploy/scripts/backup-key_test.sh` 的并行任务登记后，RV-14 在任务启动前
  准确报红；登记已恢复。
- actionlint v1.7.12、`release_workflow_test.rb`、`validate-docs.mjs`、
  `validate-architecture-boundaries.mjs`、ShellCheck 0.11.0（27 scripts clean）全绿。
- 证据日志：`tests/.logs/ci-speedup-2026-11-24/`；最终完整三线日志与 `.elapsed`
  已在该目录单独归档，避免后续 `--fast` 覆盖。

### 6.4 改动文件

- Workflow/契约入口：`.github/workflows/ci.yml`、`.github/workflows/release-tooling.yml`、
  `.github/workflows/security.yml`、`scripts/ci/contracts-gate.sh`。
- Go 测试：`scripts/test-api-isolated.sh`、`scripts/test.sh`、`scripts/ci/go-gate.sh`、
  `AGENTS.md`。
- Shell/发布门禁：`upgrade.sh`、`deploy/scripts/upgrade-version-selection_test.sh`、
  `deploy/scripts/postgres-backup-format.sh`、`deploy/scripts/postgres-safety_test.sh`、
  `scripts/ci/release-tooling-gate.sh`、`tests/validate-docs_test.sh`。
- 契约测试：`tests/validate-contract-parity-call-edges.test.mjs`、
  `tests/validate-contract-parity-sample-equality.test.mjs`、
  `tests/validate-contract-reachability-equivalence.test.mjs`、
  `tests/validate-contract-reachability.test.mjs`、
  `tests/validate-contract-status-parity-helpers.test.mjs`、
  `tests/validate-contract-status-parity-reverse.test.mjs`、
  `tests/helpers/contract-parity/report.mjs`。
- CSP：`tests/validate-document-csp.mjs`。
- 知识库：本任务书、`knowledge/plans/README.md`、`knowledge/index.md`、
  `knowledge/decisions/2026-11-24-ci-test-throughput-batch1.md`、
  `knowledge/sessions/archive/2026-11/2026-11-24-ci-cd-test-speedup-batch1.md`。

### 6.5 偏离与残余

- 任务书示例 LPT 权重低估了首轮实测长尾；已按真实包耗时重算为约
  222/221/221/221，并重新完成两轮无 flaky 验收。
- `release-tooling-gate.sh` 批次调度升级为动态 worker pool，避免同批最慢任务空等。
- CI 跨 run 缓存命中率只能在下一次 GitHub Actions 运行确认；本地已验证 actionlint、
  workflow 结构、Bun/Proto/actionlint 缓存键与浅拉基线逻辑。
- `SFORUM_CONTRACTS_SKIP_WEB_UNIT=1` 依赖当前 web job 无条件执行；实施 M4 时必须改绑
  `changes` 输出，否则会漏测。
- 全量仍受 Nuxt production build 与 Go/Web 并发资源竞争限制；M3/M5 是后续主要收益。

### 6.6 M3+M4 执行记录（2026-11-25）

#### 范围与结论

- **M3 已完成**：API Dockerfile 只编译一次 `sforum` CLI；8 个内置插件按
  `SFORUM_BUILTIN_BUILD_JOBS`（默认 4）执行 build/digest/validate/test，失败即终止同批
  任务；`build` 只保留 `sforum-api`；新增独立 `migrate-build`；删除独立 worker 目标。
- **M4 已完成**：`ci.yml` 新增 `changes` + `ci-summary`，go/web/contracts/containers
  按布尔输出跳过，container matrix 由 `container_images` 精确派生；`security.yml`
  的 CodeQL/Govulncheck/Web audit 按路径跳过；contracts 的重复 Web 单测改绑
  `changes.web == true`。
- M5/M6 未开始；未修改 `release.yml`、Go 重包模板库或 `apps/web/nuxt.config.ts` 的
  生产压缩配置。

#### 实测耗时与体积

| 场景 | 优化前 | 优化后 |
| --- | --- | --- |
| `docker build --target api` 无缓存 | 343s | **149s（-56.6%）** |
| `docker build --target migrate` 无缓存 | 268s | **62s（-76.9%）** |
| `docker build --target prod` | 204s（首次冷网络） | **109s（同 Dockerfile 复测）** |
| 内置插件 build/digest/validate/test | 171s 串行 | **64s，4 路受控并发** |
| API 二进制构建阶段 | 62s | **2.7s** |
| Nitro brotli+gzip | 96.6s / 15,818,123 bytes / 1,089 files | 生产配置不变 |
| Nitro gzip-only | 89.7s / 13,353,408 bytes / 776 files | 仅实验 |
| Nitro 关闭预压缩 | 89.9s / 10,470,114 bytes / 463 files | 仅实验 |
| BuildKit cache mount 冷 / 热 / control | 119s / 116s / 109s | 不采用 |
| `./scripts/test.sh --fast` | 上一批稳态 69.2s | **45–49s（最终 api 20 / web 49 / static 22）** |
| `./scripts/test.sh` 全量 | 上一批 287.5s | **259s（api 259 / web 222 / static 83）** |

Nitro 最大收益约 6.9s，未达到任务书要求的 30s 阈值；Caddy 当前
`encode zstd gzip` 可兜底，但生产配置保持不变。BuildKit cache mount 没有收益且
`.nuxt-build` 复用扩大转换量，因此 Web Dockerfile 未改。

#### 门禁与证据

- `docker build --target api|migrate` 均 exit 0；API/migrate 镜像内二进制
  `--version` 均输出 `SForum dev`，API 镜像确认不存在 `/usr/local/bin/sforum-worker`。
- `release-tooling-gate.sh` 全绿；RV-18 输出
  `Toolchain parity: go 1.26.11 across 28 go.mod, Dockerfile, and govulncheck.`。
- `contracts-gate.sh` 独立入口全绿；`actionlint` v1.7.12、`release_workflow_test.rb`、
  `verify-main-ci_test.sh` 全绿。
- `release_workflow_test.rb` 现在直接执行 workflow 的 change-output 与 ci-summary shell，
  覆盖 docs-only / Go-only / Web-only / workflow_call / workflow_dispatch，以及
  success+skipped / failed 汇总结果；Security 的 docs / Go / Web / web-audit /
  schedule / workflow_dispatch 路径与 CodeQL 语言矩阵也由同一测试执行。
- `./scripts/test.sh --fast` 重跑 exit 0；`./scripts/test.sh` 全量 exit 0。
- 日志：`tests/.logs/ci-speedup-2026-11-25-m3m4/`。其中 Docker 前后测日志、
  Nitro 三组日志、Web cache 三组日志和全量三线日志均已保留。

#### 改动文件

- 构建：`apps/api/Dockerfile`、`apps/api/cmd/sforum/test_extension_test.go`。
- Workflow：`.github/workflows/ci.yml`、`.github/workflows/security.yml`、
  `scripts/ci/release_workflow_test.rb`。
- 知识库：本任务书、`knowledge/plans/README.md`、`knowledge/index.md`、
  `knowledge/modules/backend.md`、`knowledge/modules/frontend.md`、
  `knowledge/decisions/2026-11-24-ci-test-throughput-batch1.md`、
  `knowledge/decisions/2026-11-25-ci-container-and-path-gating.md`、
  `knowledge/sessions/2026-11-25-ci-cd-test-speedup-batch2.md`、
  `knowledge/sessions/archive/2026-11/2026-11-24-ci-cd-test-speedup-batch1.md`。

#### 偏离与残余

- 首轮 `--fast` 命中旧的 Dockerfile 契约测试（仍期待逐插件 `cd` 与 api+worker 两处
  copy）；测试已同步为新的单次 CLI、受控并发清单、单一 API 最终镜像和独立
  `migrate-build`，随后 `--fast` 与全量均通过。
- 首轮 `release-tooling-gate.sh` / `contracts-gate.sh` 在沙箱内被
  `spawnSync ... EPERM` 阻断；在沙箱外重跑均 exit 0，未改测试断言。
- 全量静态线中 document-CSP 自检通过；真实服务探针因 `127.0.0.1:3000` 无响应而按
  设计记为 SKIP。未触碰用户手动运行的 web dev 进程。
- GitHub Actions 的 `paths-filter` 实际命中率、GHA cache 命中率和 `ci-summary`
  远端落库只能在推送后确认；本地已完成 actionlint、结构守卫和 shell 级场景执行。
