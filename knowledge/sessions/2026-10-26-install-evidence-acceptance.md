# 2026-10-26 商城安装真机取证：可判定验收标准

**读者**：执行取证的人（小设）。目标是「看到什么才算过」，不需要回来问产品。
**范围**：core 管理页里商城（`sforum.store`）的「安装并启用」链路，四条状态：
已启用 / 已升级 / 已用 N 秒 / 有后端包影响面。
**不在范围**：主题激活路径、平台不兼容 409、旧服务端 `latestPlatforms` 回落（见 §6）。

## 0 三条通用规则

1. **一条证据 = DOM 证据 + 独立复核**。只截弹窗、只截 toast 都不算过；复核必须来自弹窗之外的
   宿主事实（插件页状态、active 版本、插件自身可用性）。
2. **文案必须来自当前产物**。locale 改动后 dev 的 client 模块图可能是旧的，表现为裸 key
   （`admin.extensionInstall.hostAction.*`）。看到裸 key 记为「产物陈旧，需重取」，
   **既不记失败也不记通过**。
3. **时间类证据只能现场采样**。截图无法证明计时在走；后台标签页会被节流，必须保持前台可见。

固定锚点（后续判定都用它）：

| 锚点 | 选择器 |
| --- | --- |
| 弹窗根 | `[data-testid="admin-page-install-dialog"]` |
| 结果标题 | `[data-role="result-headline"]` |
| 旋转图标 | 弹窗内 `[data-slot="icon"]`（class 含 `animate-spin`） |
| 影响面本体 | `SFAdminExecutableTrustImpact` 渲染块（含 `data-testid="extension-database-risk"` / `extension-guard-authorization`） |

**这些锚点来自源码，不是实测 DOM**：`data-testid="admin-page-install-dialog"` 在
`SFAdminPageInstallDialog.vue:161`、`data-role="result-headline"` 在同文件 `:202`。
取证者第一步必须在真实 DOM 里确认它们存在；不存在就以实际元素为准，并把偏差记进证据
（选择器、实际元素、为什么不同）。不要把本文的选择器当既成事实。

## 1 前置条件（不满足就别开始计时）

| 编号 | 条件 | 判据 | 当前实测 |
| --- | --- | --- | --- |
| P0-1 | 前端产物新鲜 | 确认态标题是「安装并启用这个扩展包？」，页面无裸 key | 未验证（需重启 web dev server 后确认） |
| P0-2 | 商城取包可用 | 签名下载的 zip 的 sha256 == 商城 `release_assets.sha256`，连续 3 次一致 | **不稳定**：同一 token 一次 200 但截断在 2.0 MB（应为 6.77 MB），一次 503 `upstream_unavailable`（1.3 s） |
| P0-3 | 目录里有「可安装 + 兼容 + 非内置」的商品 | 卡片按钮可点、弹窗能打开 | **当前 0 个**，需按 §5 造夹具 |
| P0-4 | 账号权限 | `extension.plugin.manage`；影响面要求 super_admin | 待确认 |

P0-2 的验证命令：

```bash
for i in 1 2 3; do
  tok=$(curl -s -o /dev/null -w '%{redirect_url}' \
    "http://127.0.0.1:4000/api/v1/products/sforum-ai-url-fetch/download/linux/amd64" \
    | sed 's#.*/download/##')
  curl -s -o /tmp/pkg$i.zip -w "try$i=%{http_code} size=%{size_download}\n" \
    --max-time 300 "http://127.0.0.1:4000/api/v1/download/$tok"
  sha256sum /tmp/pkg$i.zip
done
```

过：三次都 200、size 一致、sha256 一致且等于 `release_assets.sha256`
（fetch-url 1.0.3 linux/amd64 = `3dfa876b4eb8…`，6 768 059 B）。
为什么要这一条：`install-from-url` 按 sha256 校验，包被截断时安装会死在下载/摘要阶段，
四条状态一条都取不到。

## 2 判定表

### 2.1 已启用（`outcome = 'enabled'`）

必须**同时**满足：

| # | 看到什么 | 判据 |
| --- | --- | --- |
| 1 | 弹窗仍开着（state=success） | `[data-testid="admin-page-install-dialog"]` 存在 |
| 2 | 结果标题 | `[data-role="result-headline"]` 文本 === **「已安装并启用」** |
| 3 | 身份行 | 标题下第二行 === `<extensionId> · <version>`，version 等于本次安装版本 |
| 4 | 成功徽标 | 标题左侧图标 class 含 `i-lucide-check`（不是 `i-lucide-package-check`），底色含 `bg-emerald-500/15` |
| 5 | 说明句 | success 色 alert 的描述 === **「插件已经在运行。要停用或改设置，去插件页。」** |
| 6 | 制品摘要 | 「制品摘要」的 dd 非空、64 位 hex，且与确认态那屏显示的 SHA-256 逐字相同（同一弹窗前后两屏对比；不要求等于商城页面上的 sha256，两者口径可能不同） |
| 7 | 底部动作 | 两个按钮：**「关闭」** + **「去插件页完成信任与启用」** |
| 8 | **独立复核** | 点「去插件页完成信任与启用」→ 落到「当前管理前缀 + `/extensions/plugins`」，该扩展行状态为**已启用**，版本列 === 本次版本。**前缀是实例配置**：本机实测 `/control-panel`，代码默认 `/n`（`adminRoutePrefix.ts`），不要写死 |

更硬的复核（能取到就用）：
- `GET /api/v1/admin/extensions` 里该 id `status === 'enabled'` 且 active version === 本次版本；
- 插件自身能力可用（如 fetch-url 的抓取探测返回 200）。

**不算过**：
- 只有 toast「扩展已启用。」（toast 在 settle 前发，不证明运行态）；
- 标题是「安装完成」「只是暂存，尚未启用」「已安装，尚未启用」「已安装，主题要到主题页激活」
  「已安装，启用需要超级管理员」；
- 弹窗说已启用但插件页显示未启用 / 版本对不上；
- 文本是裸 key。

### 2.2 已升级（`outcome = 'upgraded'`）

| # | 看到什么 | 判据 |
| --- | --- | --- |
| 1 | 结果标题 | === **「已安装并升级」** |
| 2 | 身份行 | `<extensionId> · <新版本>`，且新版本 ≠ 安装前插件页记录的版本 |
| 3 | 说明句 | success alert 描述 === **「新版本已经生效。要回滚或改设置，去插件页。」** |
| 4 | toast | 标题 === **「扩展已升级。」** |
| 5 | **独立复核** | 插件页 active version 从旧版本变成新版本，旧版本不再是 active |

**不算过**：
- 标题是「升级候选已保存」（那是 `upgraded + staged`，只暂存未生效）；
- 弹窗版本与插件页 active version 不一致。

### 2.3 已用 N 秒

| # | 看到什么 | 判据 |
| --- | --- | --- |
| 1 | 位置 | `state=installing` 的 info alert 描述 |
| 2 | 文本形状 | 严格匹配 `^安装期间不要关闭页面；关闭会中止本次安装。 已用 (\d+) 秒 · 最长 120 秒$` |
| 3 | 上限 | 必须出现「最长 120 秒」（`ADMIN_HOST_INSTALL_TIMEOUT_MS/1000`） |
| 4 | 首值 | 进入 installing 的首次渲染是「已用 0 秒」 |
| 5 | 递增 | 以 200 ms 间隔采样 ≥3 点：N 单调不减，且至少一个样本 N ≥ 1 |
| 6 | 节奏 | 相邻两次 N 变化的墙钟差 ∈ [900 ms, 1300 ms]（`ELAPSED_TICK_MS = 1000`） |
| 7 | 停止 | 离开 installing（转 activating / success / error）后该行消失且不再增长 |
| 8 | 图标 | 同一 alert 内 `[data-slot="icon"]` class 同时含 `animate-spin` 与 `motion-reduce:animate-none`；计算样式 `animationName === 'spin'` |

采样片段（弹窗出现「正在下载并暂存」后粘进控制台）：

```js
const t0 = performance.now(), out = []
const id = setInterval(() => {
  const el = document.querySelector('[data-testid="admin-page-install-dialog"]')
  const m = el?.innerText.match(/已用 (\d+) 秒 · 最长 (\d+) 秒/)
  out.push({ t: Math.round(performance.now() - t0), n: m ? +m[1] : null })
  if (!m && out.length > 3) { clearInterval(id); console.log(JSON.stringify(out)) }
}, 200)
```

**不算过**：
- 只截到一个数字（没有时间差，无法证明在走）；
- 后台标签页采样（`setInterval` 被节流会跳号）；
- 安装 < 1 s、N 始终为 0：记「未取到」，换更大包或限速重来，**不得记通过**。

### 2.4 有后端包影响面（`state = 'trust-review'`）

触发条件三条同时成立：包声明可执行后端 + 该**精确制品**未受信 + 操作员是 super_admin。
（非超管会直接判 `staged / super_admin`，不进审阅。）

| # | 看到什么 | 判据 |
| --- | --- | --- |
| 1 | 弹窗变宽 | content 容器 class 含 `sm:max-w-4xl`（确认态是 `sm:max-w-lg`）；1633 视口下实测宽 ≥ 700 px |
| 2 | 顶部 alert | 标题 === **「启用前请核对影响」**，描述以 **「下面是宿主按这个包生成的影响清单。」** 开头 |
| 3 | 影响面本体 | `SFAdminExecutableTrustImpact` 已渲染（不是「正在读取影响清单…」） |
| 4 | 身份行（常显） | 「身份」的 dd === `<extensionId>@<version> / <type> / <source>`，id 与 version 等于本次请求 |
| 5 | 摘要行（常显） | 「制品摘要」dd 64 位 hex；「影响摘要」dd 非空，两者都不得为 `—` |
| 6 | 数据库段（条件常显） | 包声明了迁移时出现 `[data-testid="extension-database-risk"]`，内含「数据库变更与恢复计划」与「数据库权限集合」 |
| 7 | 守卫段（条件常显） | 包声明了守卫时出现 `[data-testid="extension-guard-authorization"]`，每行含 kind / owner / digest |
| 8 | 折叠区 | 其余事实在 `<details>` 折叠块里：每个 `summary` 有标题 + 计数徽标（`itemCount`）。至少「请求的主机权限」「后端二进制」「数据库契约」三块的计数 **> 0**；点开 `summary` 后 `<pre>` 里是可读 JSON（不是 `null`） |
| 9 | 底部动作 | 两个按钮：**「取消」** + **「确认并启用」**，确认按钮不是 disabled |
| 10 | 一次性语义 | 点「确认并启用」后若启用失败回到 trust-review，再次确认必须能重新签发（不是复用同一 token 的报错） |
| 11 | **独立复核** | 确认成功后回到 2.1 判定；`extension_trust_grants` 里该 `(extension_id, version, package_digest)` 出现 `revoked_at is null` 的新行 |

**不算过**：
- 只看到「可执行插件还需要超级管理员」——那是确认态的常规提示，不是影响面；
- 非超管账号看到 trust-review（出现即 bug，单独记缺陷）；
- 折叠块计数为 0 却当作「已披露」（计数 0 = 该项没声明，不能当证据）；
- 身份行版本不是本次安装的版本（说明弹窗还挂着上一次的结果）。

## 3 反例总表（快速判否）

| 你看到的 | 结论 |
| --- | --- |
| toast 说启用成功，弹窗已经关了 | 不算，缺弹窗证据 |
| 弹窗「已安装并启用」，插件页显示未启用 | 缺陷：弹窗说谎，记 P0 |
| 弹窗「已安装并升级」，插件页版本没变 | 不算，说明只暂存 |
| 弹窗文案是 `admin.extensionInstall.hostAction.*` | 产物陈旧，重取 |
| 影响面只有标题 | 缺陷：影响面空壳 |
| 非超管进了 trust-review | 缺陷：越权入口 |
| 计时从 3 秒直接跳到 9 秒 | 采样在后台标签页，重采 |

## 4 每条证据的记录格式

```
时间：
URL：<当前管理前缀>/extensions/sforum.store/pages/catalog（本机实测前缀 `/control-panel`）
账号角色：super_admin?（是/否）
扩展：<id> <旧版本> → <新版本>
DOM：<选择器> = "<文本>"
复核：<插件页/接口 的实际状态与版本>
截图：<路径>
```

## 5 夹具：当前环境唯一可行路径

目录现状（`GET :4000/api/v1/catalog`，6 个商品）：

| slug | 扩展 id | 类型 | sforumVersion | 3.0.16 兼容 | 宿主已装 |
| --- | --- | --- | --- | --- | --- |
| sforum-ai-url-fetch | sforum.fetch-url | plugin | `>=0.1.0` | ✓ | ✓ 1.0.3 |
| sforum-ai-web-search | sforum.web-search | plugin | `>=0.1.0` | ✓ | ✓ 1.1.0 |
| oauth2-provider | sforum.oauth2-provider | plugin | `>=3.0.12-alpha.7` | ✓ | ✓ 1.6.0 |
| search-site | sforum.search-site | plugin（builtin） | `>=1.4.0 <2.0.0` | ✗ | ✓（builtin） |
| markdown-plus | sforum.markdown-plus | plugin | `>=1.2.0 <2.0.0` | ✗ | ✗ |
| theme-quiet | sforum.theme-quiet | theme | `>=1.3.0 <2.0.0` | ✗ | ✗ |

三个兼容的都已安装 → 目录没有可安装项；三个未装的约束是 `<2.0.0`，3.0.16 与 3.0.17 都不满足。

**建议夹具：`sforum.fetch-url`，1.0.2 → 1.0.3 两跳**

1. 先在插件页抄下 fetch-url 的 7 项设置（`allow_http` / `allowed_domains` / `enabled` /
   `max_bytes` / `probe_url` / `proxy_url` / `timeout_ms`）。卸载会 CASCADE 掉
   `extension_settings`（契约原文：settings still CASCADE with the extension row；
   `retainSettings` 只是 V1 兼容位，不保设置）。
2. 卸载：在已登录的管理页里同源 fetch
   `POST /api/v1/admin/extensions/sforum.fetch-url/uninstall`，body `{"removalMode":"preserve"}`。
3. 刷新目录：卡片应重新出现「安装」。仍显示已安装 → 再刷一次；还不对就记「待查」
   （可能是插件后端缓存），不要改代码绕过。
4. 选 **1.0.2**（linux/amd64）安装并启用 → 走 trust-review → 确认 →
   同时取「影响面」与「已启用」两条证据。
   为什么 1.0.2 一定进审阅：本机 `extension_trust_grants` 里 1.0.2 的 12 条 live 授权都是
   dev watcher 本地构建的摘要，与商城包不是同一制品。
5. 再选 **1.0.3** 安装 → 取「已升级」证据。
6. 收尾：插件页确认 active = 1.0.3、status = enabled；把第 1 步的 7 项设置填回去。

**取证期间不要做**：
- 不要改 `sforum-fetch-url` 源码、不要重启 API：`EXTERNAL_EXTENSION_ROOTS` 的 dev watcher
  会重新构建并覆盖成它自己的摘要，active 版本会跳。
- 不要在取证中途切账号（权限不同会走 `staged` 分支，状态对不上）。

## 6 仍未覆盖（别当成已验收）

- 运行阶段 409 `extension.platform_incompatible`：要替换已安装二进制，会先撞摘要校验，
  现只有 Go 单测。
- 旧服务端无 `latestPlatforms` 的回落：只有响应注入验证，没有真的旧服务端可指。
- 主题路径：theme 永远 `staged / theme`，不在本次四条内。
