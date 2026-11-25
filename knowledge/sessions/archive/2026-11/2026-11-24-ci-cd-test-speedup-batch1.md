# 2026-11-24 CI/CD 与测试提速第一批 Handoff

## Changed

- M0：删除 CI 死 PostgreSQL service；增加 Bun、Nuxt/typecheck、Proto、actionlint
  跨 run 缓存；checkout 改浅拉并补取内置插件基线；contracts 入口可跳过已由 web
  线覆盖的 8 个重复单测。
- M1：隔离测试改为实测 LPT 分片，父进程只生成一次计划；`--with-compat` 复用最轻分片；
  PG/Redis 就绪探测改 0.2s、仍要求连续两次真实认证/PONG；新增显式缓存模式与分片耗时。
- M2：升级重试退避可配；备份 KDF 增加 test-only 覆盖；release/contracts 门禁改动态
  worker pool；parity 真实仓库扫描从约 9 次降到 1 次；reachability 夹具并发；
  validate-docs 失败夹具并行；修复 Chrome 154 无头导航卡死。

## Evidence

- 最终 `./scripts/test.sh`：**287.5s**，api **288s** / web **244s** / static **80s**。
  优化前：api 391s / web 214s / static 268s。
- 无缓存隔离两轮：
  - round 1：209 / 215 / 188 / 197s，壁钟 **216.1s**；
  - round 2：205 / 210 / 185 / 195s，壁钟 **212.0s**；
  - max/avg 分别 **1.063 / 1.039**。
- `--fast`：冷缓存 295.5s；稳态热缓存 **69.2s**（api 29 / web 69 / static 30）。
- `release-tooling-gate.sh`：**45.3s**；RV-14 删除登记项反向自检按预期报红并恢复。
- `contracts-gate.sh`：独立入口 **17.1s**，skip 重复 web 单测 **12.5s**。
- `upgrade-version-selection_test.sh`：33.9s → 3.5–6.1s。
- `postgres-safety_test.sh`：32.3s → 18.4–29.5s；默认 200000 闭环、信封格式、
  生产测试口拒绝均保留。
- `validate-docs_test.sh`：5.9s → 4.3–8.0s。
- actionlint、ShellCheck 0.11.0（27 scripts clean）、release workflow、validate-docs、
  architecture boundaries 全绿。

日志：`tests/.logs/ci-speedup-2026-11-24/`。

## Decisions

- 见 `decisions/2026-11-24-ci-test-throughput-batch1.md`。
- M0–M2 只改第一批范围；Dockerfile、`release.yml`、Go 模板库和 `ci.yml` 路径门控未动。

## Next

1. M3：Dockerfile 单次 CLI 构建、独立 migrate-build、Nitro 压缩/构建缓存。
2. M4：`changes` + `ci-summary` 路径门控；同步 release workflow 守卫，并重新绑定
   `SFORUM_CONTRACTS_SKIP_WEB_UNIT`。
3. M5：模板库/包内并行深挖；M6：release asset 关键路径拆分。

## Open Questions

- 当前完整全量仍受 Nuxt build 与 Go/Web 资源竞争限制；需要 M3/M5 的实测对照。
- CI 缓存命中率只能由下一次 GitHub Actions 运行确认，本地已完成 workflow lint/结构守卫。
