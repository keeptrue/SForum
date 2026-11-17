# 2026-11-17 Session Handoff — 插件进程识别 + 默认主题审批自愈

两个用户报告的线上级缺陷（均已修复、已独立审查 approve、已带运行时证据）：

1. 后台「插件占用 **0 个进程**」（v3.1.0 正常）
2. 网站默认主题**不生效**，且后台没有「恢复默认主题 / 重新激活」入口

## Changed

- `apps/api/app/Support/Extensions/plugin_command_transport.go` —
  `pluginSandboxArgv0` 返回**绝对制品入口路径**（`cmd.Args[0]` → `cmd.Dir+base(entry)`
  → `extension.ID`）。EX2-01 沙箱启动器此前把清单相对入口 `backend/plugin` 当
  `argv[0]`，`processmemory.IsBackendPluginCommand` 要求 cmdline 含
  `storage/extensions/…` 路径标记 → 采样器在 `List()` 阶段丢掉全部插件子进程。
  `pluginprocess/sandbox_types.go`、`sandbox_linux.go` 的 `Argv0` 契约注释同步改为
  「进程表识别唯一依据」。
- `apps/api/app/Models/Extensions/theme_publication_apply.go` —
  `promoteStagedBuiltinThemeIfHealthy` 迁入（原在 `service_package.go`，受 807 行
  legacy cap），受保护默认主题晋升时置 `HostApproveProtectedDefault` +
  `Reason=startup_repair`；新增 `defaultThemeApprovalRepair.EnsureDefaultThemeApproved`
  （启动自愈，健康时零写入）；apply `switch` 增兜底分支
  `未批准 + 受保护默认 + 来源已批准 + ActorUserID == 0` → 按 host 批准落地 + `slog.Warn`。
- `apps/api/app/Models/Extensions/{types.go,postgres_store.go,service.go,service_package.go}`
  — `ThemeActivationInput` 增两个 `json:"-"` 字段；`ActivateThemeExact` 透传 reason；
  `SyncBuiltins` 在活动主题判定后调用自愈；`enable_failed` 事件追加 `api cwd=…` 与启动目录提示。
- `tests/architecture-boundaries-baseline.json` — `service_package.go` 807 → **784**
  （ratchet 下调，与当前行数逐字相等）。
- 前端：`useExtensionRowMenu.ts` 增可选 `activate` handler；已启用主题行追加
  「恢复默认主题 / 重新激活」菜单项；`adminExtensions.ts` 新增唯一判定源
  `isProtectedDefaultTheme`；`themes.vue`、`index.vue` 传入 `activateTheme`。
- 测试：`plugin_sandbox_argv0_test.go`、`theme_publication_apply_test.go`、
  `theme_publication_postgres_integration_test.go`（+2 用例）、
  `apps/web/tests/admin/extensionRowMenu.test.ts`。

## Decisions

- **自愈不得降权**：受保护内置默认主题的 staged 精确晋升必须继续带 host 批准；
  只有「无人类 actor + 受保护默认」才放行，`lockProtectedBuiltinDefault` 门未放宽。
  apply 兜底额外要求 `ActorUserID == 0`，防止人类「主动不批准」被自动升格。
- **不扩 God object**：`Service` / `ThemeService` 的 receiver 数被 ratchet 顶格，
  自愈做成 focused collaborator `defaultThemeApprovalRepair`（唯一调用点 `SyncBuiltins`）。
- **不给 60 方法的 `Store` 接口加方法**：账本读取用 `ThemeRuntimePublicationRepository`
  类型断言，断言失败视为无可判定 → 零写入。
- 前端只加入口，不改接口/契约/i18n key，不动 `activateTheme` 的审批链。

## Evidence

- `go build ./...` exit 0；架构闸门 passed（2336 files / 224 >500）。
- `./scripts/test-api-isolated.sh`：作者轮 `EXIT=0`；审查者复跑最终树 `EXIT=1`，
  唯一红是**既有 flake** `TestRecoveryOnlyModeExposesOnlyHostHealthAndReadiness`
  （`app/Http/server_test.go` 里裸字面量 `"935"` 命中 `/ready` 的 `checkedAt`
  纳秒串，该文件零 diff，单测 `-count=15` 全绿）→ 见 Open Questions。
- `bun test tests/admin/` 1532 pass；`bun test` 全量 3518 pass / 1 既有 skip；`typecheck` exit 0。
- 现网：16 条插件子进程 cmdline 为绝对路径（dashboard 实测「插件占用 16 个进程 394 MiB」）；
  `theme_runtime_publications` 最新 `24 | t | host | NULL | startup_repair`（`23` 是故障态）；
  `page_provider_bindings = 35`；`extension_events` 一条 `%re-approved%`；
  `pages/resolve?id=forum.home` → `provider=sforum.default-theme / fallback=false`；
  `:3000` 渲染 `data-provider="sforum.default-theme"`、`data-template="1"`；
  重启后账本仍停 24（自愈幂等）。
- 变异：argv0 回退、promote 去 host 批准、去 `ActorUserID==0`、兜底置死 —— 4 条全部被抓，
  逐字节还原。

## Next

1. `package_path` 仍是 cwd 相对（`../../storage/extensions/…`）：从非 `apps/api` 目录启动
   仍会让制品不可解析。本轮只保证「不再因此静默降权」+ 事件带 cwd。根治需存储根锚点
   /绝对路径 + 迁移，**单开一刀**，不要叠在主题修复上。
2. 主题「未生效」状态在扩展列表里仍不可见：想做成徽章需要新字段（如 `themeApplied`，
   含 OpenAPI 契约改动 + parity 门），本轮先用「恢复默认主题」入口覆盖。
3. 清 13 条修复前遗留的 `backend/plugin` 孤儿（PPID=`systemd --user`，cmdline 只有
   `backend/plugin`）：`scripts/cleanup-orphan-extension-plugins.sh` 只认 PPID=1，
   对这批量级无效。修复后新产生的孤儿已可被路径识别。
4. 默认主题菜单项的 `title` 复用 `reactivateThemeHint`，语义略偏（P3-5），下轮改文案。

## Open Questions

- `app/Http/server_test.go` 的哨兵 `"935"` 是既有 flake（非本轮引入）：删掉裸字面量、
  只保留键不存在断言，或换成不会出现在 RFC3339 里的 64-hex 哨兵。要不要单开一刀由运营定。
- `defaultThemeApprovalHealthy` 只看 exact tuple + 批准位，不看目标是否 host-approvable
  （P3-2；`SaveBuiltin` 固定写受保护位，实际不可达）。若未来允许默认主题行改可删/可停用，
  需把它加进健康判定。
