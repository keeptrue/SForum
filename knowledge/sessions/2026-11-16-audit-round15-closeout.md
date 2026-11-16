# 2026-11-16 · 审计后备池清零（第 15 轮）handoff

## Changed

- **8 条流全部收口，8 个提交**：`23bd1e11a`（W2 契约可达性常设门）→ `2c84e4205`（W5 沙箱 /
  架构残余）→ `ba212257b`（W6 运维 / 供应链）→ `a4c2d12cd`（W1 必登面 401/503）→
  `9a1588a3f`（W3 契约残余）+ `ae6c14d2b`（W3 返工）→ `739a282c5`（W4 前端遗留）→
  `0b6bbcdfd`（W7 授权 / 端点细节）。`96c2769ad..HEAD` **228 files / +17816 −734**。
- **W8（文档 / 裁决）是第 10 个提交**：`docs/{zh-CN,en-US}/deployment/upgrade-notes.md`
  双语 8 条（旧行为 / 新行为 / 运维要做什么）+ `decisions/2026-11-16-go-2026-5932-xcrypto-openpgp.md`
  + 4 处文档一致性（`one-click.md` 的 `reason` 三种→四种、`troubleshooting.md` 决策树、
  `routes.md` 的 `global_middleware` 口径、两个 README 与 `deployment.md` 的入口）。
- 契约：退化区 **194 → 30/435**（29 已映射），必登面补 **193** 条 503（188 门 + 5 手工）。
- 前端：axe 四类 **72/60/27/63 → 0**（9 后台页 × 2 视口），软徽标 **4.49 → 5.48:1**，
  后台壳唯一 `<main>` **18/18**，85 页三态盘点。
- 门禁（Lead 独立重跑）：`./scripts/test.sh --fast` PASS（api 255s / web 56s / static 46s）；
  contracts-gate 44 pass / 0 fail；parity 0 未登记例外；openapi refs **3785 / 65**；
  架构闸门 passed（2299 / 221）；v3 catalogs current（18）；deploy 套件 12/12；
  `bun test` **3363 pass / 0 fail**；typecheck EXIT 0；`validate-docs.mjs` pass。

## Decisions

- **版本事实**：`v3.1.0` tag **已存在**（`1126c8802`，2026-11-14），本轮全部提交在它之后 →
  这批「升级注意」属于**下一版**。docs 页保持版本中立（`$SFORUM_VERSION`），版本号只留在
  `reports/2026-11-15-release-upgrade-notes.md`，注明「定版时按实际 tag 填」。
- **GO-2026-5932 不升级**：`go list -deps ./...` 844 包 openpgp 零命中、全仓直接 import 只有
  `argon2`、advisory 无修复版本；4 条复位条件写进决策记录（依赖图出现 openpgp / 上游出修复版 /
  扫描口径改 fail-closed / 产品需要 OpenPGP）。
- **必登面 `disabled`/`banned` 归域层 403**（不在 `RequireActor` 门上判），不对称由表驱动测试钉住。
- **`[header-global]` 是全局存在性，不是逐 operation**；`Retry-After` 未逐 op 绑定，
  over-declaration 不可门检。复位条件：把 header 名绑到每个 503 出口。
- **公开页 205 个对比度节点归主题域**（`extensions/builtin/themes/**`，2.33–2.59:1，
  改法一行 `#a0a0aa`→`#6b6b76`），W4 写域外，不并入本轮。
- **契约退化区 30/435 的复位条件**：handler 改用登记助手，或 parity 扩成按分支调用图。
- **环境阻塞**：lint-shell 需 shellcheck 0.10.0（本机无二进制 / 无网，机制与 stub 反证已交付）；
  bun audit 基线未播种（CI fail-closed 到联网播种）。

## Next

- **主题轮**：205 个对比度节点 + 侧栏嵌套 `<nav>` 一起收（全仓唯一「一行改动、影响全站公开页」）。
- **契约**：`Retry-After` 绑到每个 503 出口并让 parity 做字段级比对；退化区 30 条优先改 handler。
- **依赖**：任何 x/crypto 升级先跑决策记录的复核命令，再决定是否重开 GO-2026-5932。
- **发布**：W8 提交后共 **10 个未 push 提交**；push 前确认 `origin/main` 仍是 `e12325aad`。
- **清理**：审计轨道已关闭，`sessions/2026-11-15-audit-*` 的 6 个中间 handoff 可在下次触碰
  本轨道时一并归档到 `sessions/archive/2026-11/`（本轮不动，避免打断正在引用的链接）。

## Open Questions

- `deploy_test.sh` 曾偶发红一次，未复现；下次跑 deploy 套件时留意。
- 公开页对比度若开主题轮，需先确认写域（`extensions/builtin/themes/**` 属谁）。
