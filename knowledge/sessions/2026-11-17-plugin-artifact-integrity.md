# 2026-11-17 Session Handoff

## Changed

- 事故：全站 `/api/v1/*` 503 `plugin_runtime_convergence_failed`。根因已定：
  `plugin_runtime_publications` revision 1540 钉住 `sforum-shortcodes@1.3.10` 的
  `b4f399bd…`，该目录的 `backend/plugin` 于 `2026-11-17 17:34:53` 被带外删除（28/29 文件），
  每次启动 stage fail-closed。失败原文：
  `apply plugin runtime revision 1540 attempt 1: stage sforum-shortcodes@1.3.10: backend entry backend/plugin is not available`
- 分支 `fix/plugin-runtime-artifact-integrity`，本刀只改文档（`AGENTS.md` + `knowledge/**`），
  未改 Go、未改 `.env`、未碰 `apps/web` 的未提交改动。
- `AGENTS.md`：在 Core Framework / Plugin-First 章节补五条条文（制品只读不变式、发布期
  fail-closed、错误必须可诊断、恢复通道完备性、诊断纪律），接在既有 exact-artifact 条目之后。
- 新增决策 `decisions/2026-11-17-artifact-integrity-at-publish-time.md`（D1 发布期 vs apply 期、
  D2 dev 自愈范围、D3 republish 不热恢复 + 六条撤销条件）。
- `modules/extensions.md` 新增 "Artifact Integrity And Recovery (2026-11-17)" 一节。
- 第二轮（同日）：按 #254 实现回填两个 CLI 的签名、退出码、输出字段与 dry-run 语义
  （`AGENTS.md`、模块笔记、本决策的 D3/Consequences/Evidence）。

## Decisions

- 完整性校验放**发布期**（写 publication 之前），apply 期校验保留作纵深防御：publication 是
  追加式不可变表，发布期是唯一还能「不写」的时刻；发布期失败可回退，apply 期失败是全站 503。
- dev 自愈只在 `APP_ENV=development`，与 `2026-10-17-dev-zero-command.md` 同一道闸门；
  生产侧等价能力是可审计的 CLI，不是启动时自动改写。
- `republish` 只写 durable 事实（剔除/重指向 → 追加新 revision），不热恢复：收敛失败时运行时
  已被判定不可信，热恢复会造出第二个无审计的分歧来源。
- **坏制品不能补文件**（决策的关键引理）：`b4f399bd…` 的 manifest 声明
  `backend.digest=b29b4775…`，该包 1.3.4–1.3.10 共 35 个 `backend/plugin` 无一同 sha256；
  拷一个二进制进去既对不上 manifest 摘要也对不上目录树摘要，stage 仍失败。
- 可诊断性口径：artifact 类失败命中 `plugin_artifact_incomplete`（`/api/v1/ready` 的
  `recovery.code` 与 503 body 的 `data.code`，P1 / #253）；旧 code 仍是兜底。

## Next

- **发布期完整性闸门 + dev 自愈仍未实现**（task-3 剩余部分）：`AGENTS.md` 的「Publication is
  fail-closed」与 D2 目前只是规范要求，代码里还没有写入前校验、也没有 `APP_ENV=development`
  自愈通道（`rg 'self-heal|autoRepair' apps/api` 无命中）。落地时按五条条文写测试。
- **已落地（可直接用）**：`ae2487303` + `5f857cc38`（#253 `plugin_artifact_incomplete` +
  四坐标 message）、`c7e80dd51`（#254 `sforum extension verify-artifacts` 与
  `sforum recovery republish-artifact`）。
- 事故现场恢复：先 dry-run
  `sforum recovery republish-artifact sforum-shortcodes --reason "<原因>" --root apps/api`
  看 `DEFECT` / `CANDIDATE`（1.3.10 现有另外三个 digest 都完整），确认候选后再加
  `--confirm-token <任意非空>` 执行，然后走正常启动闸门；**不要**手工往 `b4f399bd…` 补文件。
- 收尾时把 `knowledge/index.md` 的 Latest Handoff 换成最终状态（本文件已指过去）。

## Open Questions

- 发布期校验失败该不该也返回 `plugin_artifact_incomplete`（即「拒绝发布」与「recovery-only」
  是否共用同一 code），还是发布侧独立一个 code——未拍。
- dev 自愈形态未定：重建 stage，还是重写 publication 指向？两者对 `dev-auto` 审计记录的
  影响不同；若复用 republish 通道，还要决定 dev 自愈是否也要求 `--confirm-token`。
- `--confirm-token` 目前只判非空、不校验内容（显式确认闸门）。是否要绑到具体
  `(extensionId, digest, revision)` 防误用，未定。
- `republish` 只支持**同版本**重指向：若某版本在 `extension_versions` 里没有自洽候选，
  出口只剩「重建该版本制品」；跨版本回退不在该命令范围内。需确认这覆盖了运维的真实恢复路径。
- 取证残留：`storage/extensions/sforum-shortcodes/1.3.10/` 目录 mtime 为 `2026-11-17 19:05:16`
  （本会话开始前），说明有人在该时刻增删过子项；谁在动这个目录尚未查明，复盘时需排除干扰。
