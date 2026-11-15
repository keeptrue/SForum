# 2026-11-15 审计第 13 轮：EX2-01 沙箱 / RV-03 裁决 / 契约 4 类 / RV-28 utils

## Changed

- **EX2-01（部分 → 完成）**：插件子进程改为经「内核级沙箱启动器」启动。
  `apps/api/app/Support/Extensions/pluginprocess/` 新增 `sandbox_types` / `sandbox_linux` /
  `landlock_linux` / `cgroup_linux` / `sandbox_other`；`cmd/api` 与 `cmd/worker` 的 `main`
  第一条语句接管 `__sforum_plugin_sandbox_exec` 并 `EnableLauncher()`；接线在
  `plugin_command_transport.go` 的 `attachPluginSandbox`。
  - Landlock ABI 8：文件系统默认拒绝（只读制品目录/包根/运行时目录，写面 = 制品目录 +
    per-plugin TMPDIR + `SFORUM_SETTING_*` 里运维配置的绝对路径 + `EXTRA_RW`）；
    网络策略可配（默认 `off`，`bind` / `all` 显式开启，`all` 下 TCP connect 实测 EACCES）。
  - cgroup v2：per-plugin `memory.max` / `pids.max`（委派可用时），`PR_SET_NO_NEW_PRIVS`；
    能力探测失败走结构化降级报告，不拒启动。
  - 报告管道：`abortPluginStart` 覆盖全部失败分支，`TakeLaunchReport` 幂等且不再空等 2s。
  - `SkipHostEnv: true` + 独立 transport 白名单（`SSL_CERT_*`、代理变量），env 白名单终于
    名副其实；`storage-fs` 的 `root_path` 经设置路径规则默认自动可写。
- **RV-03（部分 → 摘出分母）**：`knowledge/decisions/2026-11-15-core-guard-authority-boundary.md`
  把「core 授权权威在 handler/service 层、Route Registry 的 core guard 目录不参与运行时裁决」
  定为显式设计，含 4 条不接管理由、5 项替代保障、3 条残余、5 条复位条件。
- **契约 4 类盲区**：12 条公开读补条件性 401；附件 2 条内容 operation 补 302（含
  `Location` / `Cache-Control` 与请求头参数 `X-SForum-Media-Delivery: redirect-v1`）；
  3 条通配路径形状用描述披露（不假装对齐）；parity 门新增显式 guard 助手白名单
  （`requireGuestRead` / `requireAdmin`），空集路由 211 → **194**，新增 12 条定向 401 棘轮。
- **RV-28 扩到 utils 层**：`utils/editor/shortcodes.ts` 的 fallback 文案改 `fallbackKey` +
  唯一解析器（translator 由 `SFEditor.vue` 注入），`utils/sfEditor.ts` 的 emoji 只存
  `composer.emoji.*` 键；zh/en 各补 11 键（6931 = 6931），渲染路径裸中文只剩无 translator
  时的最后兜底字面量。

## Decisions

- **EX2-01 不做 uid 映射**：本机 `unshare -m/-n/-p` 与写 `uid_map` 均 EPERM（AppArmor），
  生产容器能力集不同且缺制品属主模型；同 uid 互发信号仍是残余。
- **seccomp 不上**：第三方二进制没有「声明自己需要哪些 syscall」的清单，白名单会大面积
  打死现有插件；网络策略因此默认 `off`（四个内置插件要出站），与 seccomp 同一前置条件。
- **行数闸门零余量**：`protocol.go` 897/897、`protocol_v2_client.go` 1003/1003，下一行
  增量必须先抽文件（干净边界：`newPluginClientConfig` 的版本开关、
  `startProtocolInstanceLocked` 的失败分支）。

## Verification

- `go build ./...` exit 0；`pluginprocess` 六探针真跑：`/etc/shadow` 与 host-canary EACCES、
  artifact/TMPDIR/unix socket OK、`policy=all` connect EACCES（`off` 对照 ok）、
  `memory.max=100663296 pids.max=48`、注入 ABI 0 走 degraded、设置数据目录可写而
  `/etc/hosts`/`$HOME` EACCES。
- 变异：摘 `landlock_restrict_self`、摘 cgroup 写入、摘 `PR_SET_NO_NEW_PRIVS`、摘设置路径
  规则、摘 `SkipHostEnv`、摘 `SSL_CERT_FILE` 白名单，六次全红并 `sha256sum -c` 复原。
- `./scripts/test-api-isolated.sh` exit 0（153 包）；`node tests/validate-architecture-boundaries.mjs`
  passed（2295 files）；`ruby scripts/validate-openapi-refs.rb` 3518 refs / 65 files；
  `node tests/validate-contract-status-parity.mjs` 0 未登记例外；
  `bash scripts/ci/contracts-gate.sh` exit 0；`node tests/validate-docs.mjs` pass；
  `bun test` 3309 pass / 0 fail；`bun run typecheck` exit 0。

## Next

- 契约侧：空集路由仍有 194/435（名字级 guard 跟随，`Mail.authorize` 这类不产码的转调链
  看不见）；3 条通配路径永远不可静态对齐。
- 沙箱侧：TMPDIR 回收只看 24h mtime（长跑插件可能被删）、cgroup 落 `app.slice` 而非服务
  scope、同插件两实例建组竞态、`GUARD_HELPERS` 同名取最后一个。
- 依赖/工具链：GO-2026-5932（`x/crypto`，module 级未调用）、bun audit 严重度门仍是
  `report`、shellcheck 版本未钉。

## Open Questions

- `EXTRA_RW` 与设置项共用「深度 ≥ 2」谓词后，`/data` 这类单组件数据根两个入口都不接受，
  过渡期需下钻一层（`/data/sforum`）；要不要给 env 通道单独放宽，等有显式数据目录声明再说。
