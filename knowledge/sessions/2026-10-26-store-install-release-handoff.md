# 2026-10-26 商城安装发版 Handoff

## Changed

- **core `v3.0.16` → `v3.0.18` 共 30 个提交**（`v3.0.16..v3.0.17` 25 个 +
  `v3.0.17..v3.0.18` 5 个），HEAD `26d808403` = annotated tag `v3.0.18`
  （`git rev-parse v3.0.18^{commit}` = `26d808403843ffd03445d31c5d72e672d261ebf7`）。
  发行内容见 tag message：宿主动作 `extension.install.enable`、`CoreCompatibilityVersion`
  从 tag 派生、`LifecycleRecovery` + 运行时分歧守卫、平台闸门、dev watcher 与
  `build.sh` 产物对齐、`LiveGrant` NULL 修复（`c6d6d53ba`）。
- **store `18a9a07` → `6b3885f` 共 6 个提交**（HEAD
  `6b3885f588a900d3611f12823881bef549d9eae5`）。
- **发版三处齐发**：Release「SForum v3.0.18」11 个 asset（非 draft/非 prerelease，
  `11:21:55Z`）；npm `@sforum/admin-sdk@1.3.0`（`11:21:56Z`，同一 run）；
  GHCR `latest` 与 `3.0.18` 同 digest
  `sha256:6f77d1f3f199e7a469700f7750cf6a14e03cd2bcaf288a7f4853a021d168c781`。
- **商店跟随**：pin `26d808403843ffd03445d31c5d72e672d261ebf7`（行内注释 `# v3.0.18`，
  `ci.yml:83`）、`sforumVersion` `>=3.0.17`（`sforum.extension.json:223`）、
  `backend.digest 82156831…`（`:43`，`packageFiles` 同值 `:113`）；HEAD 的 ci run
  `40316472412` 两 job（plugin / service）全 success。

## v3.0.17 为什么零发布，v3.0.18 补了什么

**后人最容易误读的一段：`v3.0.17` 的 tag 存在且已推送，但这一版什么都没发出去。**

- tag `v3.0.17`（指向 `998098cee`）推送后 release run `40264545071` 失败：
  18 success / 1 failure / 3 skipped。失败 job 是 `Publish versioned Web SDKs`，日志原文
  `Error: web SDK validation: Vue scaffold must depend on "@sforum/admin-sdk": "^1.3.0"`
  （`scripts/ci/web-sdk-packages.mjs:32` ← `publish-web-sdks.mjs:91`）。
- 它卡在 `promote` 之前，`Promote / *`、`Verify anonymous image pulls`、
  `Create GitHub release` 全 skipped ⇒ npm 无包、GHCR 无 `v3.0.17` 也无 `latest` 变更、
  无 GitHub Release。四处实测互证：`gh release list` 无 v3.0.17；
  `npm view @sforum/admin-sdk versions` 只有 `1.0.0`/`1.1.0`（**`1.2.0` 从未发布**，
  脚手架当时却依赖 `^1.2.0`）；GHCR `manifests/3.0.17` 取不到；`latest` digest 未变。
- 处置按仓库惯例：**不移动已推送的 tag**，升版本补发。`v3.0.18` tag message 首行即
  「SForum v3.0.18（补发 v3.0.17）」，并列出三项修复：Vue 脚手架跟上 `1.3.0`
  （`1e75be1e5`）；把 Web SDK 包校验挂进主门禁 `ci.yml`（`d5cb3d3e2`，原本只在
  `release.yml`，所以拖到发版最后一步才炸）；dev 后端构建钉住工具链与 workspace
  （`1906f971f`，`GOTOOLCHAIN` 从 go.mod 派生、`replace` 改由临时 `go.work` 承载
  ⇒ `backend.digest` 跨环境可复现）。另 `4cbdb64f2`/`26d808403` 让脚手架断言从 SDK
  清单派生、在 CI 父环境下也成立。
- `v3.0.18` 的 release run `40312622813` **24 job 全 success**；`v3.0.17` 的发行内容
  原样保留。

## Evidence

- **动作词表**：旧 pin `10c2b21e8` 与 HEAD `8ab9f246b` 的 `AdminHostActions()` 都只有
  `extension.install`；当时 `git log --all -S 'extension.install.enable'` 为空
  → 必须先提交，pin 才有东西可指。
- **`>=3.0.17` 运行期 E2E**：钉 host identity=`3.0.16`，`POST /api/v1/admin/extensions`
  → 422 `extension.core_incompatible`，`fields` 带 `required/coreVersion/buildVersion`；
  改成 `>=3.0.16` 则越过版本闸门、停在 403 `backend_execution_restricted`。
- **`sforumVersion` 的真假只有一处求值**：CI 的 `manifest OK` 只做
  `semver.NewConstraint` 语法解析（`v3_validate.go:31`），唯一求值点是
  `Models/Extensions/lifecycle.go:720`；`ensureCoreCompatible` 另有两个静默放行口
  （`!identity.Valid → nil`、`ErrSForumVersionUnverifiable → nil`）。
- **store pin 提交自身红过一次**：`60c3b34`（pin to v3.0.18）的 run `40315378941`
  service job 失败 —— 测试间 settings 行串扰，`GET /api/health` 期望
  `store.example.com` 实收 `127.0.0.1:3000`；`6b3885f` 隔离后转绿。
- **商店侧 z-index hack 已移除**：`dialog.ts:283` 注释「不再碰遮罩的 z-index」，
  `catalog.test.ts:1297` 有「pending never rewrites the overlay z-index」用例（`f02d98c`）。
- **真机取证**（`/tmp/store-qa-2026-10-26/evidence/report.md`，临时 super_admin
  `qa-agent` 用完即删）：spin + `0→0→0→1→1`（250ms×5）；`已安装并启用` /
  `已安装并升级` 且插件页独立复核（启动 1 / 2 次）；trust-review 先审阅后确认；无 P0。

## Decisions

- pin 用**完整 40 位 SHA**，不用 tag：短 SHA 不是合法 refspec，tag 可被 force-move。
- 顺序硬约束：commit → push main → CI 绿 → 推 tag → **才**改 `sforumVersion`。
  提前改会让 dev watcher 把 store 插件判成 `core_incompatible`，商城页当场消失。
  本轮符合：`6bf3a60`（抬 `>=3.0.17`）在 `v3.0.17` tag 之后。
- **零发布的版本按「升版本补发」处理**，不移动、不删除已推送的 tag；补发版 tag
  message 必须写明补的是哪一版，避免后人把 `v3.0.17` 当已发布基线。
- **只在 release workflow 里跑的校验等于没有校验**：发版门禁必须同时挂进主 CI。
- `core_authority_kernel_test_helpers_test.go:147` 保持字面量：那条约束校验的是用例
  自选的 1.x 迁移目标，不是宿主版本。

## Next

- **待人工推送：`origin/main..HEAD` 全部未 push、未打 tag**（`git tag --points-at HEAD`
  为空）：本轮 5 个代码提交 `2cac877c9` guard 4 处 →
  `ae3913ec6` grant exact-artifact → `11a8f4948` 用例加固 + fence 注释 →
  `a4e91267b` 第 5 处 `TopicAuthorID` → `86993dd3b` stale-grant 用例全新库确定性复现，
  外加本条 knowledge 提交。**不要当成已发布**。
- **卸载 tombstone 只修到第二层（grant 层），不能写成「已修好」**：第三层
  identity purge 与 append-only 发布账本的 `ON DELETE RESTRICT` 冲突，需要新决策，
  归 `plans/2026-10-26-uninstall-identity-purge.md`。三条负向断言成立：703 仍 pending、
  1154 只剩 actor 守卫错、908/909 停在
  `plugin_runtime_publication_me_extension_version_id_extensi_fkey`（SQLSTATE 23503）。
- **703 不是 bug**：`active_version_id` 已变仍永久 pending 是 fail-closed 的正确行为，
  缺的是 `superseded` 终态出口。不要按「修 703」开单。
- **「record id 30 与 34」不是「30/34 条成功」**：那是 cleanup record 的编号。这两条能
  finalized 恰恰因为它们从未进过发布账本（`extension_versions` /
  `plugin_runtime_publication_members` 均 0 行），是**负证据**；全库仍只有这 2 条
  finalized。诊断全文见 `sessions/2026-10-26-uninstall-tombstone-diagnosis.md`。
- **勘误（`2cac877c9` 提交正文，不 amend，事实以此为准）**：正文写「redact 路由没有前置
  route guard」不准确 —— catalog 确实声明了 `core.route.forum.redact_comment_revision` /
  `redact_topic_revision` 的 `super_admin` guard
  （`app/Support/Routes/core_catalog_gen.go:271/362`），只是 core-only 直连路径不评估它，
  真正权威是 `revisions_mutation.go:83-85` 的 `IsSuperAdmin`。可达性结论不变。
- store `ci.yml:75` 注释文本仍写 `# full 40-char SHA to the v3.0.17 commit`，与第 83 行
  ref 的 `# v3.0.18` 不一致 → 改注释（纯注释，不影响 digest；本轮不动 store 仓，下批带上）。
- 真商城 6–7MB 包处在宿主 90s 超时边缘（直连 15–85 KB/s，6.77MB 用 87.2s；宿主 90s、
  页面 120s）→ 决策建议见 `plans/2026-10-26-store-package-delivery-risk.md`。
- 真机取证 P0-2/P0-3 未过：同一 token 一次 200 但截断在 2.0 MB、一次 503
  `upstream_unavailable` → 与上一条同源，先定交付策略再补证。
- op 926 job publication 停在 `target/pending` 是**读取侧缺线**（`marker=true` 且未对账
  的行没有启动期重放，`pending` 把「待重放」与「已作废」混成一种），本机 40 行未对账
  全为 `marker=false`、River 无 `extension.plugin_job` 行 ⇒ 当前零用户可见后果 →
  `plans/2026-10-26-op926-job-publication.md`。
- `tun2.html` 是他人调试残留，未提交未删除（不要动）。

## Open Questions

- 无新问题。两条产品口径（主题装完是否加「安装并设为默认」、非超管是否拒绝装可执行包）
  的建议见 `2026-10-26-release-v3.0.17-readiness.md` §10，仍待拍板。
