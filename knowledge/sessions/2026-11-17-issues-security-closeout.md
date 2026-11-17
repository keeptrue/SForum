# 2026-11-17 Session Handoff — GitHub Issues + Security 收口

## Changed

- GitHub `keeptrue/SForum` 上 4 条 open issue + security 面板全部处置完毕：#232 **已关闭**、
  #238 **已关闭**、#235 **取 b 已实现**、#237 **已实现**（后两条并入 PR #249）。
- **#232 Nuxt 4.4.8→4.6.4**：代码早在 PR #233（2026-11-17T07:17 合并）落地，本轮只补验收 ——
  干净 clone 上 `typecheck` exit 0 / `bun test` **3498 pass** / `build` exit 0 /
  `bun pm ls --all` 断言 `@unhead/vue@3.4.3` 与 `vue@3.5.45` 各单版本 /
  `bun-audit-gate` 不带 `--refresh` exit 0（基线 44 条，3 条 nuxt GHSA 0 命中）。
- **#235 `NoData` 登记 204 / 实现 200**（PR #249）：契约 8 处 `204`→`200` + `ApiResponseNull`，
  门表两处 `codes:[204]`→`[200]`，新增 `checkResponseHelperCodes()` 守卫（登记值 vs 函数体实测，
  变异验证红绿留档），8 端点表驱动测试。
- **#237 契约门看不见映射器业务码**（PR #249）：新增
  `tests/helpers/contract-parity/mapper-attribution.mjs`（379 分支 / 335 哨兵，按 handler 调用的
  领域方法收窄归因，盲区 206 个）；**119/433 → 214/433**；反向缺口 **537 → 310**；
  反向检查 + 反例测试（forward / declared / narrow 三棵合成树）；已接入 CI。
- **#238 `@nuxt/ui` 4.12.2 管理端视觉回归 QA**：226 条断言、198 pass、**真实视觉回归 0 条**、
  真实 a11y 缺陷 1 条（`aria-modal` 缺失）→ 转出 #250。
- **Security 面板**：advisories 0 / dependabot open 0 / secret scanning 0；CodeQL alert 49 由
  PR #248 修复（待合并后 CI 重扫确认）。
- 本轮开的 PR：**#248** `post_risk` 真机探针改走 CDP arguments 传参（修法 A：
  `Runtime.callFunctionOn` 参数化、表达式零动态插值，10 处插值点收进 8 个静态探针 +
  1 个 `callInPage`）；**#249** #235 + #237 合并收口，**已通过独立审查**（含「摘掉反向检查 →
  CI 立刻红」的回归证明）；**#251** `tests/browser-admin-visual-regression.mjs`，约 1100 行
  零依赖 CDP harness。
- 本轮新开 issue：**#250** `[a11y] 管理端 UModal 的 role="dialog" 缺 aria-modal="true"`
  （由 #238 转出，不阻塞）。

## Decisions

- **#235 取 b 而非 a**：与 2026-07-04 原始设计（`NoData` = 200 + `data:null`）一致；前端
  `useApiClient.ts:167-170` 对 null/undefined 都容忍，零改动；代价是 DELETE 类端点返 200 带
  body，与 REST 惯例不合，已在契约描述里改准措辞。
- **#237 不做全量并入**：把 `mapXxxError` 的 379 个分支全塞给每个 handler 是过近似，会让门为
  假缺陷变红。按「handler 调用的领域方法在同包直接 return 的哨兵」收窄；`narrow` 合成树是
  「收窄」与「全量并入」的分界线。新暴露的 31 条真实缺口只登记不修（本批只做可见性）。
- **口径修正**：#237 正文的 `119/434` 分母是旧口径，正确是 `119/433`（本门只统计已映射
  operation：440 契约 / 433 已映射 / 436 实现侧路由）；直方图起点用本批开工实测 **537**，
  不是 issue 里更早快照的 `403=234`。
- **QA 判据必须按实测校准**：照 issue 字面写会得到假断言 —— 「无营销式大标题」不能判 `h2` 计数
  （活动 tab 的卡片标题本来就是 h2 / 16px）；「唯一 tab 系统」不能判 `tablist` 总数（后台 shell
  自带 `SFAdminTabStrip`）；「面板无残留」必须先剔除 `[role=tablist]` 子树（设置页 8 个 tab
  按钮就在 `#sforum-admin-main` 内部）。

## Next

- PR #248 待合并后确认 CodeQL alert 49 消失（**本地无 CodeQL CLI**，只能靠合并后 CI 重扫）。
- PR #249 / #251 待合并；#249 合并后 #235 自动关闭，#237 建议手动关闭。
- 31 条新暴露的契约缺口待补声明（不挂在 #237 上）。
- #250 `aria-modal` 待修。
- QA 脚本的 5/6/8 组只覆盖 4 页，其余 10 页暗色与弹窗待补；全量一次跑完在 `roles` 页会卡住，
  需 `SFORUM_ONLY_PHASES` 分段。
- reviewer 列的纯注释级 P3 措辞残留：`validate-contract-status-parity.mjs:183`、
  `status-sources.mjs:34/46/411/489`、`go-source.mjs:294/644`、`contracts-gate.sh:27`、
  call-edges fixture 注释、`nodata_envelope_test.go:214-217` 重复一句。
- 本机 API recovery 根因（见下）未修；修法建议：在 admin 里激活 `staged_version_id=40794`
  （digest `70e02fb2` 是自洽的，`backend/plugin` 与 manifest digest 一致）。

## Open Questions

- **`apps/web/node_modules` 曾与 lockfile 不一致**：`package.json`/`bun.lock` 已是
  `@nuxt/ui@4.12.2`，磁盘上是 `4.9.0`。后果：主仓 `bun run typecheck` exit 2
  （`migrations.vue:334` 假错）、`bun test` 报 `Cannot find package 'jsdom'`。main 的 CI 一直是
  绿的（跑 `scripts/ci/web-gate.sh`），问题只在本地安装树。已 `bun install --frozen-lockfile`
  修正。**教训：改完 `package.json` 没跑 `bun install`，本机验的就是旧栈。**
- **本机 API 长期 `recovery_required`**：真因不是那 5 条 publication mismatch，而是
  `stage sforum-shortcodes@1.3.10: backend entry backend/plugin is not available` ——
  `storage/extensions/sforum-shortcodes/1.3.10/b4f399bd…/backend/` 里只有 `backend`（24MB,
  not stripped），没有 manifest 声明的 `plugin`。尝试补文件后错误变为
  `frontend package changed: digest expected b4f399bd…, got 760e55ba…`（DigestTree 会校验），
  证明**不能改 artifact 内容**。`recovery` CLI 只有 `activation-state` / `lifecycle-close` /
  `recheck`，其中 `recheck` 只读比对不写库、`disable` 族只做减法、builtin sync 在 recovery 下
  跳过 —— 构成死锁。本轮以 `SFORUM_SAFE_MODE=1` 启动取证（`/api/v1/web-options` 返 200），
  **未修复根因**。
