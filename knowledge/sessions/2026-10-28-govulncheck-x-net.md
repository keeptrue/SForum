# 2026-10-28 Security 门禁：x/net 可达漏洞与 v3.0.23

**起因**：main 上 Security 的 `Govulncheck / core-api` 红，CI 五路全绿、只有它红。
govulncheck 报 2 个**可达**漏洞，都来自 `golang.org/x/net@v0.60.0`，修于 `v0.62.0`：

- GO-2026-6866（渲染 raw text 时未检查全部祖先命名空间跃迁）
  命中 `ThemeCompiler.balanceHTMLSkeleton` → `html.Render`
- GO-2026-6860（合并文本节点时的平方复杂度）
  命中 `EditorDocument.htmlToPlain` → `html.Parse`、`ThemeCompiler` → `html.ParseFragment`

## Changed

- `apps/api`：`golang.org/x/net 0.60.0 → 0.62.0`，连带 `x/crypto 0.58.0`、
  `x/sync 0.24.0`、`x/text 0.43.0`；`go mod tidy` 清掉旧版本残留的 go.sum 行。
- `tests/compat`：同一批 x/ 版本。
- 16 个 `extensions/fixtures/plugins/*/backend` 与 8 个
  `extensions/builtin/plugins/*/backend` 逐个 `go mod tidy`。这些模块用
  `replace github.com/keeptrue/sforum/apps/api => ../../apps/api` 复用主模块依赖图，
  MVS 选中新版本后各自 go.sum 缺条目，`go build` 直接报
  `updates to go.mod needed`；CI 的 reference 包构建带 `-mod=mod`，缺条目会被写回
  工作区，随即被 `Reject generated module drift` 判红。
- 8 个内置插件 patch 抬版（`backend/go.sum` 属于 sourceDigest）：auth-github 1.0.10、
  content-policy 1.1.9、search-site 1.0.9、shortcodes 1.3.7、smtp 1.1.9、
  storage-fs 1.1.10、storage-s3 1.0.12、web-push 1.0.9；
  `tests/builtin-plugin-release-baseline.json` 随之 `--write`。

## Decisions

- 只升级依赖，不加忽略规则：两条 CVE 都真的可达（govulncheck 给出了符号级调用链），
  忽略表会顺手掩盖下一次真实命中。
- 内置插件抬版是发布契约的必然结果，不是顺手改版本号：`digestPlugin` 把
  `backend/**` 纳入 sourceDigest，依赖文件一变就必须抬版，否则 Release tooling 的
  `validate-builtin-plugin-versions.mjs` 拒绝。

## 验证

- 本地：`apps/api` 与 `tests/compat` 的 `go build ./...`、`go vet ./...` 通过；
  16 个 fixture 与 8 个内置插件 backend 全部 `go build` 通过；本地 govulncheck 不再
  命中 x/net 两项（剩余 13 条是本地 go1.27.1 的 stdlib 条目，DB 修于 go1.27.2，
  CI 走 `go-version-input: stable`，不在本次改动范围）。
- `SFORUM_BUILTIN_VERSION_BASE=origin/main node tests/validate-builtin-plugin-versions.mjs`
  通过；`./scripts/ci/release-tooling-gate.sh` 全绿；`node tests/validate-docs.mjs`、
  `node tests/validate-architecture-boundaries.mjs` 通过。
- CI run `40596275765` 五路全绿；Security run `40596275832` 四路全绿
  （`Govulncheck / core-api` 由红转绿）。
- Release run `40598005034` 全绿：Verify main CI、六路镜像构建与合并、四平台资产、
  smoke、SDK 发布、promote、匿名拉取验证、GitHub Release。

## 发布

- v3.0.23（2026-10-28）：`scripts/release.sh 3.0.23 --non-interactive --yes`，
  标签指向 `776eea788`。https://github.com/keeptrue/SForum/releases/tag/v3.0.23

## Next

- 下次 apps/api 依赖变动（含 dependabot 的 core-go 组）会再次要求这批 backend 模块
  `go mod tidy` 加内置插件抬版；`origin/dependabot/go_modules/apps/api/core-go-4c3f7bdadc`
  （PR #169）就是只改了 `apps/api` 两个文件的例子，按本 handoff 的清单补齐即可。
- 自查命令：
  `for d in extensions/builtin/plugins/*/backend extensions/fixtures/plugins/*/backend; do (cd "$d" && go build ./...) || echo "DRIFT $d"; done`
