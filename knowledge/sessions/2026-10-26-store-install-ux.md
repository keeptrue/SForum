# 2026-10-26 应用商城安装体验：宿主动作二连 + 弹窗层级 Handoff

## Changed

- **清单词表（`task-1`）**：新增 `extension.install.enable`
  （`AdminHostActionExtensionInstallEnable`）。`AdminHostActions()` 按「旧动作在前」
  返回两个 id，`knownAdminHostAction` 仍是唯一判定入口；未知 id、非 `view=component`
  页仍拒绝整份清单。`extension.install` 语义零改动（只暂存）。OpenAPI
  `hostActions.enum` 同步两个 id；内嵌 JSON Schema 仍 `items: string`，Go 词表才是
  拒绝权威。
- **前端 bridge（`task-2`）**：新增 `installAndEnableExtension(request)` → 安装身份 +
  三态 `outcome`（`enabled` / `upgraded` / `staged`）；每个已声明动作只注入自己的成员。
  状态机 `confirming → installing → trust-review → activating → success/error`，
  启用失败记 `failurePhase='activate'`，重试只重跑启用。启用走既有生命周期路由
  （`enable`；升级态 `upgrade`/`restart`）+ `Idempotency-Key`；可执行后端的信任审阅在
  弹窗内复用 `SFAdminExecutableTrustImpact` 与一次性 challenge，后端
  `extension.trust_challenge_required` 回落成人工审阅。回落 `staged` 三个原因：
  主题、非 super_admin、操作员关掉影响审阅。
- **披露**：`SFAdminExtensionHostActionsNotice` 增加第二行
  `admin.extensions.hostActions.enableBody`。
- **弹窗层级**：宿主弹窗 overlay/content 固定 `z-[80]`（插件遮罩 ≤ `z-40`，toast
  `z-100`）；安装中 `animate-spin` +「已用 N 秒 · 最长 120 秒」；结果面板区分已启用/
  已升级/已暂存，启用失败显示「已安装，尚未启用 + 重试启用」。
- **商店侧（`task-3`，已完成）**：`dialog.ts` 的 `setPending()` 不再改
  `overlay.style.zIndex`（原「安装期间降到 0、结束还原 40」两行已删）：生产代码与
  `dist` 里 0 处该赋值，只有 `catalog.test.ts` 断言 pending 前后
  `overlay.style.zIndex` 都是 `undefined`；层级交给宿主 `z-[80]`。
- **文档**：两份 decision（两个动作 + 死锁根因；自动启用只经新动作、默认惰性）、
  `docs/extensions/trusted-admin-components.md`（新 bridge 小节、outcome 表、层级契约、
  成员列表）、`modules/extensions.md`（两条现状）、SDK README 新增
  `## Install and enable (1.3.0)`。

## 根因（已核对，不是猜测）

点「安装」像整页卡死，三件事叠加：

1. Nuxt UI 4 的 `UModal` 渲染 reka `DialogOverlay`/`DialogContent` 时**没有
   z-index**（`#build/ui/modal`），只靠 DOM 顺序；
2. 商店遮罩是 `body` 下的定位元素且 `z-index: 40`（`.sfstore__overlay`），
   无论 DOM 顺序都在宿主弹窗之上；
3. reka `DismissableLayer` 在模态层打开时给 `body` 设 `pointer-events: none`，
   只在自己的层内容上恢复 `auto` —— 被商店遮罩盖住后，弹窗不可见、下面也没有
   任何可点区域。

## Evidence

- `go test ./app/Support/ExtensionManifest/ -run TestAdminHostActions` → ok（6 个测试函数）；
  `bun test tests/admin/{adminPageInstallDialog,extensionHostInstall}.test.ts` → 61 pass / 0 fail。
- 真机 QA（`task-5`）：图标真转（`[data-slot="icon"]` 含 `animate-spin`、`animationName=spin`；首轮
  「不转」是旧 bundle 误判已推翻留痕）；关闭后 `body.pointerEvents==='auto'` 多次一致 → 死锁三因
  移除；`packageFiles` 逐字节一致、`adminFrontendDigest` `e8e42cc7`→`90e4d6ec`；重启后确认框全中文（标题/按钮/能力代确认）、390×844 无溢出。
- **dev 经验**：改 i18n 后 dev 的 client 模块图可能是旧的（SSR 新、client 旧 → 裸 key），重启或等刷新即可，不是缺键。

## Decisions

- `decisions/2026-10-24-admin-page-host-action.md`（2026-10-26 修订）：启用为何必须在
  宿主弹窗内、三个 `staged` 回落、旧动作不被重载、层级契约。
- `decisions/2026-10-23-admin-extension-url-install.md`（2026-10-26 修订）：安装路由
  仍只暂存，自动启用只经页面级新动作，没有开关。

## Next

- **drain 已修（`task-11`）**：新增 `LifecycleRecovery`（exact-evidence + CAS + 审计 + 幂等，启动时
  先于 runtime 收敛执行）与 `sforum extension recovery activation-state` CLI；source 角色改
  fail-closed（`ErrLifecycleHostRuntimeDiverged`，不再 stage 替身）；op 926 半状态已修（审计 id
  11057），`sforum.fetch-url` 经 op 1147 升级到 1.0.3（11 step 全绿）。
- **仍留白**：「已启用/已升级」「已用 N 秒 · 最长 120 秒」无真机渲染证据——目录无可安装项（6 个
  商品全已安装、两主题与 3.0.16 不兼容、beta 为空），非环境/代码问题，由 core 与商店单测覆盖；
  op 926 的 job publication 仍是 target（空、按 operation_id 隔离、启动不读、不可达），归零需另开单。
- **发版阻塞 1 — 商店 CI pin**：`ci.yml:80` 仍 `ref: 10c2b21e8`，不认识 `extension.install.enable`；
  core 含该动作的 commit 落地后换 pin，与下一条同批（不换则 `validate-manifest` 必拒）。
- **发版阻塞 2 — 商店 `sforumVersion` 留 `>=1.0.0`**：本机 core `3.0.16`（`+14.g8ab9f246b.dirty`
  是构建元数据，不参与比较），抬到 `>=3.0.17` 会被 `extension.core_incompatible` 拒；等 core 出含该动作的 tag 再与 pin 同批改。
- 残项：商店仓 `2026-10-26-catalog-card-dialog` 仍写「安装期间把遮罩降到 0」需更正（本仓外）；10-24 真实公网包 staged 仍只有路由/服务层证明，等签名短链补真机。

## Open Questions

- 两个动作各自声明还是让 `.enable` 隐含 `.install`？当前选前者，代价是商店同时声明两个 id。
- 主题装完仍要操作员去主题页激活；是否给「安装并设为默认」的后续动作 —— **建议已出**
  （推荐不改动作词表，改结果面板深链预选），见
  `sessions/2026-10-26-release-v3.0.17-readiness.md` §10 Q1，仍待产品拍板。
- 非 super_admin 允许装可执行后端包、启用回落超管 —— **建议已出**（推荐保持现状），
  见同一份 §10 Q2，仍待产品拍板。
- 四条真机状态的判定标准见 `sessions/2026-10-26-install-evidence-acceptance.md`
  （含前置条件、锚点选择器、反例与夹具路径）。
