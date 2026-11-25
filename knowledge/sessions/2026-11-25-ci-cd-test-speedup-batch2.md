# 2026-11-25 CI/CD 与测试提速 M3+M4 Handoff

## Changed

- M3：`apps/api/Dockerfile` 先构建一次 `sforum` CLI，8 个内置插件按 4 路受控并发完成
  build/digest/validate/test；默认 `build` 只编译 `sforum-api`；新增独立
  `migrate-build`；删除已退役的独立 worker 目标。
- M3 对照：Nitro brotli+gzip / gzip-only / off 分别为 96.6s / 89.7s / 89.9s，
  `.output/public` 为 15.82MB / 13.35MB / 10.47MB。最大节省约 6.9s，未达 30s
  阈值，生产配置未改。Bun/Vite/Nitro BuildKit cache mount 冷/热/control 为
  119s / 116s / 109s，不采用。
- M4：`ci.yml` 新增 `changes` 与 `ci-summary`，路径输出驱动
  go/web/contracts/containers；containers matrix 按镜像布尔位精确派生；
  `security.yml` 的 CodeQL/Govulncheck/Web audit 按路径跳过，schedule/dispatch 全量；
  contracts 仅在 `changes.web == true` 时跳过重复 Web 单测。

## Evidence

- Docker 无缓存：`api` 343s → **149s**；`migrate` 268s → **62s**；
  `prod` 首次冷网络 204s，同 Dockerfile 复测 **109s**。
- 内置插件阶段 171s → **64s**；API 二进制阶段 62s → **2.7s**。
- `./scripts/test.sh --fast`：**45–49s**（最终 api 20 / web 49 / static 22）。
- `./scripts/test.sh`：**259s**（api 259 / web 222 / static 83），三线 exit 0。
- actionlint v1.7.12、`release_workflow_test.rb`、`verify-main-ci_test.sh`、
  `release-tooling-gate.sh`、`contracts-gate.sh` 全绿。
- RV-18：`go 1.26.11 across 28 go.mod, Dockerfile, and govulncheck`。
- `release_workflow_test.rb` 已执行 docs-only / Go-only / Web-only /
  workflow_call / workflow_dispatch 的 change-output 场景，以及
  success+skipped / failed 的 ci-summary 场景；Security 的 docs / Go / Web /
  web-audit / schedule / workflow_dispatch 与 CodeQL 语言矩阵也已覆盖。
- 日志：`tests/.logs/ci-speedup-2026-11-25-m3m4/`。

## Decisions

- 见 `decisions/2026-11-25-ci-container-and-path-gating.md`。
- 旧决策 `decisions/2026-11-24-ci-test-throughput-batch1.md` 的 contracts
  条件跳过 guardrail 已标记为 M4 已改绑。

## Next

1. M5 未开始：Go 模板库/重包内部并行需要另行批准。
2. M6 未开始：`release.yml` 的 darwin 资产关键路径拆分需要另行批准。
3. 下一次 GitHub Actions 运行确认 path-filter 命中率、GHA cache 命中率和
   main push 的 `ci-summary` success 记录。

## Open Questions

- 全量静态线中 document-CSP 真实服务探针因 `127.0.0.1:3000` 无响应按设计 SKIP；
  self-test 已通过。若需要在本地关闭该残余，需要 web + API 同时在线后重跑
  `node tests/validate-document-csp.mjs`。
