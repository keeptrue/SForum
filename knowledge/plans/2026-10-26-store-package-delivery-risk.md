# 2026-10-26 商城包交付风险：6–7MB 包贴 90s 下载硬超时

**Status**: `draft`（决策建议，待拍板）。**本轮不改任何代码、不动超时常量**。
拍板后按 §7 拆单，拆出的实现任务再各自升为 `ready`。

**问题一句话**：真商城一个 6.77 MB 的包，从商城下载到宿主落盘，平均速率只要低于
约 77–79 KB/s 就必然超时；而当天实测就在 6.7–34 KB/s 之间 —— 用户看到的却是
「对方没有返回可用的压缩包」。

**为谁解决什么问题**：为「装了商城插件的站长」解决「点了安装，等 90 秒，看到一句
与事实不符的报错，重试还是失败」。不是为省带宽，也不是为跑分。

## 1 结论先行

1. **「改一行就能配」不成立。** `extensiondownload.Options.Timeout` 这个口子早就存在
   （`downloader.go:53`，零值回落 `DefaultTimeout`），但**生产装配处把它写死成
   `90 * time.Second` 字面量**（`api_assembly_domains.go:454`），不读 config。
   要「可配」至少三处：config 加字段、装配改读配置、两处 90s 收敛成单一来源。
2. **90s 不是一个点，是三段串联，而且是跨仓的。** core 90s 之外，商城服务自己还有
   一个 `AbortSignal.timeout(90_000)`（`service/src/lib/github/client.ts:531`）。
   **只放宽 core 会把失败点原样搬到商城侧**，用户看到的错误从「下载失败」变成
   「上游不可用」，问题没解决。
3. **当前失败是必然，不是边缘。** 6.77 MB 在 90s 内的临界平均速率约 77–79 KB/s；
   当天两次实测分别 34 KB/s（1 MB 用 30.5s）与 6.7 KB/s（90.9s 只到 607 KB）。
   同一天同一个 URL 波动 5 倍，说明「有时能过」只是运气。
4. **报错文案在超时场景是错的。** 超时被包成 `ErrFetchFailed` → 502
   `extension.remote_fetch_failed` → 前端文案「下载失败：对方没有返回可用的压缩包。」
   实际对方正常返回了字节，只是没在 90s 内传完。
5. **推荐顺序：A（收敛+可配，默认 180s）→ C（镜像源）→ B（断点续传）。**
   A 与商城侧超时收敛必须同批上线；C 是唯一能把 6–7 MB 包拉回「秒级」的方向；
   B 是长期正解但跨两仓且要改 proxy 语义，在 A+C 落地前不做。

## 2 事实：这条链上有三段超时（逐行核实）

调用链：浏览器 → core `POST /admin/extensions/install-from-url` → 商城
`GET /api/v1/download/<token>`（签名短链，默认 `downloadMode=proxy` 原样转发字节）
→ GitHub release asset。

| # | 位置 | 值 | 形态 | 覆盖范围 |
| --- | --- | --- | --- | --- |
| 1 | `apps/web/app/utils/admin/extensions/hostActions.ts:27` `ADMIN_HOST_INSTALL_TIMEOUT_MS` | 120s | 显式传给 `useApiClient`（`useAdminPageHostInstall.ts:399`） | 浏览器等待整个安装请求 |
| 2 | `apps/api/app/Support/ExtensionDownload/downloader.go:31` `DefaultTimeout` **+** `apps/api/bootstrap/api_assembly_domains.go:454` `Timeout: 90 * time.Second` | 90s | `http.Client.Timeout`（`OutboundHTTP/ssrf.go` 返回 `&http.Client{Timeout: timeout}`） | 连接 + 读 body 全程，不是空闲超时 |
| 3 | `sforum-app-store/service/src/lib/github/client.ts:531` `AbortSignal.timeout(90_000)` | 90s | fetch signal | 商城拉 GitHub asset 全程 |

关键细节：

- **core 出站 `Proxy: nil`**（`OutboundHTTP/ssrf.go` 的 transport），不走环境代理。
  所以「本机挂个代理加速」对宿主无效，弱网就是弱网。
- **商城默认 `downloadMode=proxy`**（`service/src/lib/settings/service.test.ts:360`
  `expect(runtime.downloadMode).toBe("proxy")`），字节由商城转发；`redirect` 模式
  则是 302 到 GitHub 签名地址，core 跟随重定向（`maxRedirects=3`）。两种模式下
  core 的 90s 都存在。
- core 的 90s 还包含商城侧「换 GitHub installation token + 302 换签名地址」的往返，
  真正留给传输的时间小于 90s。
- **失败语义在包装时丢失**：`downloader.go:136`
  `fmt.Errorf("%w: request failed", ErrFetchFailed)` 把超时、DNS 失败、连接重置
  压成同一个错误。

## 3 风险量化

6.77 MB（商城 `release_assets.size`；按 MiB 计 7,098,475 B）在 90s 内的临界平均速率
是 **78,872 B/s ≈ 77.0 KiB/s（≈78.9 KB/s 十进制）**。

| 平均速率 | 6.77 MB 耗时 | 90s 结果 | 余量 |
| --- | --- | --- | --- |
| 6.7 KB/s（今日实测 2） | ≈1060s | 失败 | -970s |
| 15 KB/s（区间下沿） | ≈473s | 失败 | -383s |
| 34 KB/s（今日实测 1） | ≈206s | 失败 | -116s |
| 50 KB/s | ≈142s | 失败 | -52s |
| 77–79 KB/s | ≈90s | 临界 | 0 |
| 85 KB/s（区间上沿） | ≈83.5s | 险过 | 6.5s |

结论：**实测区间 6.7–85 KB/s 横跨临界点**，成功侧的最大余量只有 6.5s ——
一次 TLS 握手抖动、一次 token 往返、商城一次冷启动，都能把这 6.5s 吃掉。
而失败侧最多要等 17 分钟才可能成功（按 6.7 KB/s），远超用户耐心。

**今日实测（2026-10-26，core 同款直连姿势 `env -u *_proxy`，GitHub release asset
`Range: bytes=0-1048575`）**：

```text
run1: http=206 bytes=1048576 time=30.504857s speed=34374B/s   # 34.4 KB/s
run2: http=206 bytes=607174  time=90.937733s speed=6676B/s    # 超时截断，6.7 KB/s
```

同一 URL 两次差 5 倍 —— 这不是稳定带宽问题，是链路质量问题。

## 4 用户此刻看到什么（读代码，不推测）

- core 超时 → `ErrFetchFailed` → `remote_install.go:123` 映射
  **502 `extension.remote_fetch_failed`**。
- 前端 `hostActions.ts:225` `'extension.remote_fetch_failed': 'remoteFetchFailed'`，
  文案 `apps/web/i18n/locales/zh-CN.json:1320`
  **「下载失败：对方没有返回可用的压缩包。」**
  → 用户会判断成「包坏了」或「商城挂了」，而不是「网络慢」。
- **可重试，但从 0 重来**：对话框 error 态有 retry 按钮
  （`SFAdminPageInstallDialog.vue:427`）；`useAdminPageHostInstall.ts:460` 的
  `retry()` 在 `failurePhase === 'install'` 时只把状态推回 `confirming`，用户再点一次
  就**重新完整下载**。没有断点续传、没有自动重试、没有退避。
  （`failurePhase === 'activate'` 时只重试启用，不重下 —— 这部分是对的，保留。）
- **倒计时与真实预算不一致**：对话框按 `ADMIN_HOST_INSTALL_TIMEOUT_MS / 1000`
  显示 120s 上限（`SFAdminPageInstallDialog.vue:52`），但宿主 90s 就断 ——
  用户会在还剩 30s 的时候突然看到失败。

## 5 三个方向：做法、代价、验收口径

### A 放宽超时（可配置）

**做法**

1. `apps/api/config/config.go` 加 `ExtensionDownloadTimeout`，用既有 `envDuration`
   范式（`config.go:190` 的 `WorkerShutdownTimeout` 就是模板）：
   `envDuration("EXTENSION_DOWNLOAD_TIMEOUT", 180*time.Second)`，非法值回落默认。
2. `api_assembly_domains.go:452` 的 `Options{Timeout: 90 * time.Second}` 改读配置。
3. `downloader.go:31` 的 `DefaultTimeout` 保留为「零值兜底」，但注释里写明它必须与
   config 默认值一致，或干脆让装配处成为唯一来源、常量降级为测试用默认。
4. 商城侧 `client.ts:531` 的 `AbortSignal.timeout(90_000)` 同步改成可配
   （env 或运行时设置），**默认值不得小于 core 的配置值**。

**默认值取舍**：50 MB 上限下，要覆盖 20 KB/s 的 50 MB 需要 2560s（43 分钟），
这不现实。默认值只服务「常见包」，不服务「上限包」。建议 **180s**：覆盖
6.77 MB @ 39 KB/s，比今天多一倍余量，又不至于让一个死连接占用请求 5 分钟。

**代价**：慢请求占用连接与 goroutine；管理页有页面级单飞限制，不会无限并发；
反向代理层可能先切断（需确认部署侧 `proxy_read_timeout` / `read_timeout` 配置）。

**验收口径**：限速 40 KB/s 时 6.77 MB 包安装成功；`EXTENSION_DOWNLOAD_TIMEOUT`
缺失/非法时行为与今天一致（回落默认，不新增失败面）；两仓各自的 90s 收敛到单一来源。

### B 分片 / 断点续传（Range）

**前提已实测**：GitHub release asset **支持 Range** ——
`accept-ranges: bytes`，且 302 到 `release-assets.githubusercontent.com`（Azure 后端）。

**做法**：core 下载器按片请求（例如 4 MiB/片）、每片独立 `context` 超时（例如 60s）、
单片失败指数退避重试 N 次、拼装后校验整体 sha256（与商城声明的 `sha256` 比对）。

**代价（这是它排最后的原因）**

1. **当前实现是全量进内存**：`downloader.go:146`
   `io.ReadAll(io.LimitReader(response.Body, d.maxBytes+1))`。分片必须改成流式写临时
   文件，否则 50 MB 上限下内存模型不成立。
2. **`http.Client.Timeout` 是整请求超时**：不改这个模型，发多少个 Range 请求都还是
   被同一个 90s 卡住 —— 分片的前提是把超时改成每片自己的 `context`。
3. **跨仓**：proxy 模式要透传 `Range` 与 `206`（`download/[token]/route.ts` 目前
   是「原样转发、不缓冲」，加 Range 语义是改代理契约）；否则 Range 只在 redirect
   模式可用。
4. 服务器忽略 Range 返回 `200` 时必须回退整包，要有明确分支。

**验收口径**：限速 30 KB/s 时 6.77 MB 包能完成；中途 kill 一片后重试成功；
服务器返回 200 时正确回退；最终 sha256 等于商城声明值。

### C 官方镜像源

**口径**：core 只保留通用原语 —— 现成的 `install-from-url` + `sourceLabel` 审计标签
就够，**镜像属于部署侧/插件**。这与 AGENTS.md 的 plugin-first 默认
（外部集成默认 out-of-tree）和 `decisions/2026-10-24-app-store-out-of-tree.md` 一致。

**做法**：镜像作为商城侧能力（同一套签名 token，换 base URL）或部署侧 CDN 回源；
core 零改动。

**代价**：镜像要先有包 —— 要么商城把 asset 复制到自己的对象存储，要么反向代理缓存；
新鲜度与运维成本落在部署方；对「官方商城」是产品承诺（镜像过期谁负责）。

**验收口径**：镜像命中时 core 下载耗时 < 10s（同区域）；镜像 miss 时回退原链；
镜像内容 sha256 与商城声明一致。

## 6 推荐组合与优先级

| 优先级 | 方向 | 理由 | 是否跨仓 |
| --- | --- | --- | --- |
| **P0** | A 的收敛版 + 文案修正 | 改动最小、可逆，把「必失败」变「大概率成功」 | 是（core + 商城同批） |
| **P1** | C 镜像源 | 唯一能把 6–7 MB 包拉回秒级，且不动 core 语义 | 否（部署侧/商城侧） |
| **P2** | B 分片续传 | 长期正解，但要改内存模型与代理契约 | 是 |

**反优先级（明确不做）**：

- 只放宽 core 的 90s 而不动商城侧 90s —— 失败点只是搬家。
- 把 `DefaultTimeout` 单独调大而不接到配置 —— 下次再有人调还是同一个坑。
- 为了「看起来修了」而把前端 120s 调大 —— 前端从来不是瓶颈。

**附带必做（P0 内，成本一行级）**：超时语义要能区分出来。建议 core 在
`downloader.go:134` 之后判定 `errors.Is(err, context.DeadlineExceeded)` 或
`os.IsTimeout(err)`，返回独立 reason（例如 `extension.remote_fetch_timeout`），
前端映射成「下载超时，可重试」。今天的「对方没有返回可用的压缩包」必须消失。

## 7 下轮怎么开单（本轮只写这份文档）

三张单，写清依赖：

1. **core：下载超时可配**
   文件：`apps/api/config/config.go`、`apps/api/bootstrap/api_assembly_domains.go`、
   `apps/api/app/Support/ExtensionDownload/downloader.go`（+ 对应 `config_test.go`）。
   验收：见 §5-A。
2. **core+web：超时错误码与文案**
   文件：`apps/api/app/Http/Controllers/Extensions/remote_install.go`、
   `apps/web/app/utils/admin/extensions/hostActions.ts`、
   `apps/web/i18n/locales/{zh-CN,en-US}.json`。
   验收：超时显示「超时」且可重试；非超时失败文案不变。
3. **商城：超时可配 + Range 透传评估**
   文件：`service/src/lib/github/client.ts`、
   `service/src/app/api/v1/download/[token]/route.ts`。
   验收：商城侧超时 ≥ core 配置值；proxy 模式能否透传 Range 给出结论（做或不做都行，
   但要有结论，不能悬着）。

**依赖关系**：单 1 与单 3 必须同一批上线（否则 core 放宽被商城 90s 吃掉）；
单 2 可独立先上。

## 8 与既有记录的关系

- 现象记录：`sessions/2026-10-26-install-evidence-acceptance.md` 的 P0-2
  （同一 token 一次 200 但截断在 2.0 MB、一次 503 `upstream_unavailable`）。
- 发版侧残项：`sessions/2026-10-26-store-install-release-handoff.md` 的 Next 第 3、4 条。
- 本轮**不产生**新的架构决策记录：A/C 都是既有口径的延伸（配置项 + plugin-first），
  B 若真做再补 decision。
