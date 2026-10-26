# 2026-10-26 插件/主题架构兼容（商城预判 + Core 硬闸门）Handoff

## Changed

**Core（SForum）**
- 新增 `Support/ExtensionPackage/platform.go`：`Target{OS,Arch}`、`DetectTarget`、
  `HostTarget`、`CompatibleWith`。顺序 fat Mach-O → `debug/buildinfo` → ELF →
  薄 Mach-O → PE；`ELFOSABI_NONE` 视为「不知道」而非 linux。
- `Models/Extensions`：`CodePlatformIncompatible`、`ExecutablePlatformMismatchError`；
  `InstallOrUpgradeArchive` 在 `ensureCoreCompatible` 之后、类型权限 switch 之前
  拒绝错架构包（读已解进内存的 `packageFiles`，**落盘前**）；`requireEnablePreflight`
  追加 `requireExecutablePlatform`（内置包不豁免）。
- 新建 `Http/Controllers/Extensions/errors_compatibility.go`：搬出
  `mapCoreIncompatibleError` 并新增 `mapExecutablePlatformError`；安装 422 /
  运行 409，`data.fields={required,host,entry}`（字符串数组）。`controller.go`
  999 → 985 行。
- 本地化 zh/en 各一条；`contracts/openapi/paths/extensions.yaml` 的 install 422 与
  enable/activate/upgrade/rollback/restart 409 补 reason 与字段。
- Web：`hostActions.ts` 的 `INSTALL_ERROR_KEYS` 加 `extension.platform_incompatible`，
  `adminExtensionInstallErrorMessage` 用 `apiErrorFields` 取 `required`/`host` 插值，
  fields 缺失时落到 `platformIncompatibleGeneric`（不把 `{required}` 印给运维）。

**商城（`sforum-app-store`）**
- 服务端：`ProductSummary.latestPlatforms`（必填，可为空=不知道）、目录查询
  `platform=strict|all`（默认 strict）、`buildSummary` 用 `latestRow` 算
  latestPlatforms、`queryFingerprint` 加入 `platform`、`api-types.ts` 重新生成。
- 插件后端：`platform` 参数解析与转发、`latestPlatforms` 映射（上游缺字段回落
  `platforms`）。
- 管理页：新建 `platform.ts`（`supportsTarget`/`releaseSupportsTarget`/
  `preferredRelease`）、`foot.ts`（节点构造）、`installGate` 新增
  `platformUnsupported`（平台判定先于版本判定）、卡片徽标、弹窗默认选最新可运行
  版本、平台下拉只留宿主/any、「只看支持当前架构」开关（默认开）。

## Evidence（实测，非推断）

- Core：`go build ./...` 绿；`go test ./...` 05:48 全量 0 FAIL；
  `platform_test.go`（真实 `os.Executable()` + 合成 ELF/Mach-O/fat/PE/脚本/空）全 PASS；
  `platform_gate_test.go` 8 项 PASS（含「拒绝且未落盘」「内置包不豁免」）；
  `errors_compatibility_test.go` 422/409 分流 PASS；
  `node tests/validate-architecture-boundaries.mjs` PASS；`ruby scripts/validate-openapi-refs.rb` OK。
- Web：typecheck 绿；`bun test` **1633 pass / 0 fail**。
- 商城：typecheck 绿；`bun run test` **790 pass / 0 fail**；`contract:generate` **NO_DIFF**；
  插件后端 `go vet` + `go test -count=1` 绿；插件前端 **162 pass / 0 fail**；
  `build.sh` 绿（digests 3 updated、manifest OK，三份产物 sha256 与 manifest 逐项一致）。
- 真机（1633×810 与 390×844，均无横向溢出）：
  - UI 上传 darwin/arm64 包 → **422** `extension.platform_incompatible`，
    `fields={entry:[backend/plugin], host:[linux/amd64], required:[darwin/arm64]}`；
    同路径上传 linux/amd64 包 → **201 installed**，随后 UI 卸载，extensions 30→29、
    versions 1155→1154、行与版本归零。
  - 商城：strict=4 / all=5；`platform=bogus`→400；卡片「最新版不支持当前架构」与
    「不支持当前架构」两种徽标；弹窗默认选 1.0.0 并说明；选 2.0.0 按钮
    `install-blocked`+disabled+原因；平台下拉只剩 `linux/amd64`。
  - `latestPlatforms` 缺失的回落：CDP `Fetch` 拦截响应删掉该字段 → 页面不误判、
    不加徽标、不锁安装。

## Residual / 未验证

1. **运行阶段 409 无真机证据**：要在已安装制品上替换后端二进制才能触发，会先撞
   摘要校验，证据不纯。现由 `platform_gate_test.go` 覆盖。
2. **旧服务端回落只有响应注入 + 单测**：没有真的旧服务端可指。
3. **Core 前端插值文案未真机验证**：真机提示显示 raw key
   `admin.extensionInstall.errors.platformIncompatible` —— 映射本身生效（选对了 key
   与参数），但用户 web dev server 启动于 10-25 22:44，locale JSON 改于 05:18，
   Nuxt i18n 的产物是旧的（`$te('...platformIncompatible')===false`）。**未重启用户的
   dev server**；需重启或生产构建才能看到文案。
4. **全仓 `./scripts/test.sh --fast` 未全绿**，两条红都属工作区里**另外两批未提交
   改动**，不是本功能：
   - `tests/validate-admin-framework.ts:371`（已提交）仍按旧契约要求扩展目录对
     `/extensions/<id>/pages/...` 为 active，而未提交的 `adminModules.ts` 删掉了该
     分支（新测试 `adminSidebarExtensionNav.test.ts:75` 断言 `false`）→ admin 侧栏
     批次自相矛盾。
   - `app/Support/Extensions` 15 项 DB 集成测试硬编码 `>=1.0.0 <2.0.0`，而未提交的
     `version.go` 批次把 dev core 改成从 git 推导（现 `3.0.16+14.g8ab9f246b.dirty`）。
5. 观测（未定性）：插件路由 `/api/v1/extensions/sforum.store/api/store/catalog`
   返回 `total=3`，而同参数直接请求本机 :4000 与隧道都返回 4（含当时的 fixture），
   隧道 `cf-cache-status: DYNAMIC`，等 45s 仍未变。不影响验收结论（回落验证用的是
   真实响应），但排查「插件路由少一条产品」时从这里入手。

## Next

1. 用户决定是否修上面两条 pre-existing 红（都在别人未提交的批次里）。
2. 需要看到 Core 平台文案时：重启 web dev server 或走生产构建后复验。
3. 若要让运行阶段 409 有真机证据：在**安装前**把错架构二进制放进包（会被安装闸门
   拦），或在测试夹具里直接构造已安装扩展 + 错架构制品（等价于现有 Go 测试）。
