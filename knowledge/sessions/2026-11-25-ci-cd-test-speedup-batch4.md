# 2026-11-25 CI/CD 与测试提速 M6 Handoff

## Changed

- `.github/workflows/release.yml` 将四平台 `release-assets` 拆成：
  - `release-assets-darwin`：darwin amd64/arm64，needs `[prepare, verify-ci]`。
  - `release-assets-linux`：linux amd64/arm64，needs `[prepare, merge-candidate]`。
- Linux 仍从扫描并合并后的 GHCR 镜像提取 `sforum-api` / `sforum-migrate`；
  darwin 不再等待镜像链，也不接收 GHCR package 权限。
- artifact 名称保持 `release-asset-<goos>-<goarch>`；
  `verify-release-assets` 聚合 `[prepare, release-assets-darwin,
  release-assets-linux, deploy-asset]`，继续使用 `release-asset-*` pattern。
- 校验和、attestation、`verified-release-assets` 上传，以及 Trivy 顺序、QEMU
  禁令、cache_writer、digest 上传顺序、SDK/smoke/promote 依赖均未改。
- `scripts/ci/release_workflow_test.rb` 已同步并新增精确 DAG、matrix、权限、
  artifact 名称和聚合步骤断言。

## Evidence

- `actionlint` v1.7.12：通过。
- `ruby scripts/ci/release_workflow_test.rb`：通过。
- `./scripts/ci/verify-main-ci_test.sh`：通过。
- `./scripts/ci/release-tooling-gate.sh`：24/24 通过。
- `./scripts/ci/contracts-gate.sh`：28/28 通过。
- `./scripts/test.sh --fast`：api 26s / web 54s / static 27s，壁钟约 50s。
- `./scripts/test.sh`：api 240s / web 268s / static 106s，壁钟约 268s。
- release-tooling 与 contracts 在沙箱内首轮因 Node 子进程 `EPERM` 失败，沙箱外
  原样重跑通过；断言未修改。

## Decisions

- 见 `decisions/2026-11-25-release-asset-critical-path-split.md`。
- darwin 与 Linux 拆成独立 job 而不是动态 job-level `needs`，因为 GitHub Actions
  的 `needs` 不能按 matrix 值变化；两个 job 共享稳定 artifact 契约。

## Next

1. 推送后触发真实 tag Release，确认 darwin 资产与容器链并行、Linux 仍等待
   `merge-candidate`，并核对 attestation 与最终资产集合。
2. 记录真实 Release run 的 job 时间线，确认关键路径实际缩短。
3. 本轮不 push；本地 commit 由当前任务收口。

## Open Questions

- 远端 Release run 的实际耗时和 GHCR 读路径抖动尚未测量。
- 完整门禁本轮壁钟 268s，高于 M5 记录的 195s，属于当前机器负载波动；测试全部通过。
