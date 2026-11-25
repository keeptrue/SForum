# 2026-11-25 CI/CD 与测试提速 M5 Handoff

## Changed

- `internal/testdb` 新增迁移模板库与包级模板克隆；隔离脚本在完整迁移后冻结
  `sforum_test_template`，模板缺失 fail closed。
- Extensions、Identity、HostAPI 的高频 Postgres fixture 改为克隆包级模板；
  三个包按隔离边界开启 `t.Parallel()`。
- Identity 的既有并发测试从普通整数计数改为 `atomic.Int64`。
- CompatFarm 支持 `SFORUM_COMPAT_PREBUILT_ROOT`；源码干净且 manifest/tree/executable
  digest 全部通过时复用预编译内置包，显式 `BuiltinPluginPath` 优先。
- LPT 权重按实测更新，默认分片上限改为 `min(nproc,6)`。

## Evidence

- 单轮无缓存：Extensions 71.1→26.1s、Identity 55.9→13.3s、
  HostAPI 30.0→10.2s；migrator 保持 77.6s。
- `-race -count=5`：Identity 124.4s、Extensions 234.4s、HostAPI 73.6s、
  `internal/testdb` 53.6s、CompatFarm 42.2s，均通过。
- 无缓存全量 4 分片：api 200s；6 分片：**api 172s / web 195s / static 83s**。
- `test.sh --fast` 稳态：**43s（api 20 / web 43 / static 19）**。
- `test.sh --fast`、`test.sh --no-cache`、`go-gate`、`contracts-gate`、
  architecture boundaries 全绿。
- 日志：`tests/.logs/ci-speedup-2026-11-25-m5/`。

## Decisions

- 见 `decisions/2026-11-25-go-template-db-and-package-parallelism.md`。
- migrator 不开启额外 `t.Parallel()`：Goose 并发解析的 4 MiB 缓冲在 race + count=5
  下造成 10 分钟超时，保持原语义。
- 不自动信任 `storage/builtin-dev`；预编译根必须由调用方显式提供。

## Next

1. M6 未开始；`release.yml` 的 darwin 资产关键路径拆分仍需另行批准。
2. 若继续压缩 API，优先研究 Goose 解析结果缓存/共享，再考虑 migrator 包内并行。
3. 下一次 GitHub Actions 运行确认 6 分片在 CI runner 上的实际收益与容器资源占用。

## Open Questions

- 本机无 web/API 联机，document-CSP 真实服务探针按设计 SKIP；self-test 已通过。
- 完整壁钟仍受 Nuxt production build 限制，M5 未承诺整体 150–180s。
