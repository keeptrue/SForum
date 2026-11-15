# 2026-11-15 插件子进程 exec 用相对路径导致启动失败（全站 503）

## 现象

`/api/v1/ready` 503、`/api/v1/health` `status=recovery_required`，所有 `/api/v1/*` 返回
`{"code":503,"message":"service.recovery_required","data":{"code":"plugin_runtime_convergence_failed","component":"plugin_runtime","message":"plugin process failed to start"}}`。启动日志原文：

```
WARN  plugin runtime convergence degraded
      apply plugin runtime revision 1532 attempt 1: stage sforum-shortcodes@1.3.9: plugin process failed to start:
      fork/exec ../../storage/extensions/sforum-shortcodes/1.3.9/1c66b642.../backend/plugin: no such file or directory
ERROR plugin runtime convergence failed; entering Host recovery-only mode
```

`ps` 里 16/16 插件子进程全部缺席；制品本身完好（入口在、可执行、manifest digest == 实际
sha256、16/16 直跑都输出 "This binary is a plugin"），lifecycle 栅栏开放行 0 条，架构/依赖/权限/资源上限/env 体积均已排除。

## 根因链

1. `config.go` 的 `EXTENSION_ROOT` 在非生产缺省为**相对路径** `../../storage/extensions`
   （本机 `.env:62` 显式写死同一个值），所以 dev 的 `extension.PackagePath` 是相对的。
2. `Models/Extensions/service_package.go` 的 `installedFilePath` 返回
   `filepath.Join(PackageContentRoot, entry)` → **相对入口路径**。
3. `Support/Extensions/protocol.go` 把它交给 `preparePluginCommand`。
4. `plugin_command_transport.go` 里 `exec.Command(entryPath)` 用相对路径，而 **`cmd.Dir` 是绝对制品目录**。
5. `execve` 对相对路径以**子进程 cwd**（`cmd.Dir`）为基准解析 → 去找 `<制品目录>/../../storage/.../backend/plugin` → ENOENT。

回归来源 `a288a8a0a`（EX2-01 进程级加固）：加固前是 `exec.Command(path)` 且**没有**
`cmd.Dir`，相对路径按宿主 cwd 正确解析；加固钉了 cwd 却没绝对化 exec。**两者叠加**才是
故障，任何一侧单独存在都没事。

## 修复

`apps/api/app/Support/Extensions/plugin_command_transport.go`：

- 新增 `pluginCommandPaths{WorkDir, Entry}`：把「cwd 与 exec 目标」收成一次解析的产物，
  两个同类型 `string` 分开返回有对调风险，结构体把「同源」写进类型。
- `pluginWorkingDirectory` → `resolvePluginCommandPaths`，内部仍只做一次
  `filepath.Abs(entryPath)`，返回 `{WorkDir: filepath.Dir(entryAbs), Entry: entryAbs}`；
  原有 5 条校验（包内容根非空 / 根内 / 拒绝 `/` 与 `.` / 拒绝宿主工作树）一字未改。
- `preparePluginCommand` 改为 `exec.Command(paths.Entry)` + `cmd.Dir = paths.WorkDir`。
- **不动** `installedFilePath` / `PackageContentRoot` 的相对语义；不动 lifecycle 栅栏、
  genesis 预算；不新增 `extensionsruntime` Manager/Service 方法。

## 回归测试

- **守卫**：`plugin_working_directory_test.go` /
  `TestPreparePluginCommandExecsAbsoluteEntryForRelativePackage` —— 相对 `PackagePath` → 相对
  入口 → 断言 `cmd.Path` 绝对、`cmd.Dir` 仍是制品目录、`os.SameFile`。overlay 还原 exec 一行
  时的原文：`plugin exec path "../../../../../../../../../../var/tmp/..." is relative`。
- **正向冒烟，不是守卫**：`plugin_process_unix_test.go` /
  `TestProtocolStartAcceptsRelativePackagePath` —— 真实探针插件以相对 `PackagePath` 冷启动。
  首版误当 guard：那次 worktree 复现的 cwd 只有 7 层（`..` 7 < 制品目录 8 层 → 真 ENOENT），
  而交付树 cwd 有 10 层，`..` 段数恒等于 cwd 层数，POSIX 在 `/` 处钳制多余 `..` 后尾部绝对段
  又拼回真实路径 → 照样找得到。`go test -overlay` 只还原 exec 一行即验证：单元用例 **FAIL**、
  本用例 **PASS**。判据是「制品目录层数 > `..` 段数」才 ENOENT（8 → RESOLVED，18 → ENOENT），
  红绿取决于 checkout 深度。RV-53 接线后 `protocol.go` 走 `pinVerifiedExecutable` +
  `/proc/self/fd/3`，该端点结构上不可再红，别再当守卫。
- 顺带在 `TestPluginWorkingDirectoryPinsArtifactDirectory` 补 `paths.Entry == entry`，旧断言未删。

## 证据

`go build ./...` exit 0；隔离集群 `./scripts/test-api-isolated.sh ./app/Support/Extensions/`
→ `ok 57.609s`；修复后在线复核 `/api/v1/ready` 200 `ready:true`、`/api/v1/health`
`status=ok`、16/16 插件子进程存活。

架构门：本刀落地当时是绿的（基线零改动）；RV-53 的并行改动一度把它转红（`protocol.go`
906>897 等三行），RV-53 收口后又恢复绿，当前 `2278 production files` 通过。本刀只改既有
文件，无一进入 cap 计数（`_test.go` 不计入 production 扫描）。

## Next

- 无未完成项。这是**契约层读漏**不是设计缺陷：cwd 注释当初只写了「钉 cwd」，没写「exec
  因而必须绝对」。后续若改用 `O_PATH` + `fexecve` 收敛 TOCTOU，仍要维持「cwd 与 exec 同源」。

## Open Questions

- 生产是否也会踩到？**不会**：`config.go` 的启动闸门对相对 `EXTENSION_ROOT` 直接 `panic`
  （「must be an absolute path in production」）。故障面是 dev 专有的。
