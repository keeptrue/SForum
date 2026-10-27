# 真实客户端 IP（登录 / 发帖 / 评论）

## Status

Accepted on 2026-07-12. Implemented. Revised 2026-10-27（见「2026-10-27 修订」）and 2026-10-28（见文末「2026-10-28 修订」，web 层模型纠正）。

## Context

登录设备目录 `user_sessions` 只存 `ip_prefix` 脱敏前缀；发帖/评论表无 IP 字段；
业务多处直接 `c.IP()`，且 Fiber 未配置 `TrustProxy` / `ProxyHeader`。在
CDN → Caddy/Nginx → Nuxt → API（Docker）链路上，API 的 TCP 对端几乎一定是
容器网桥或 Nuxt，而不是真实客户端。

## Decision

1. **统一解析**：`app/Support/ClientIP`（`clientip.FromCtx`）。
   - 仅当 TCP `RemoteIP` 属于信任代理时才读转发头。
   - 头优先级：`CF-Connecting-IP` → `True-Client-IP` → `X-Real-IP` →
     `X-Forwarded-For`（从右往左剥信任代理）。
   - 失败回退 `RemoteIP`。
2. **信任策略**：
   - 非 production：默认 `TRUST_PROXY=true`，信任私网 + loopback（Docker 友好）。
   - production：默认 `TRUST_PROXY=false`、不信任私网；须显式
     `TRUST_PROXY=true` + `TRUSTED_PROXIES`（或显式打开 private/loopback）。
3. **存储分层**：
   - 库内存全文 `ip_address`（会话、topics、comments）。
   - 用户端设备列表仍只返回 `ipPrefix` 脱敏。
   - 公开 topic/comment JSON **不**返回 IP。
4. **Fiber**：同步配置 `TrustProxy` / `ProxyHeader` / `EnableIPValidation`，
   让限流等内置组件与 `clientip` 策略一致。

## Rationale

- 信任边界在「谁可以声称客户端 IP」上：未信任对端时忽略一切可伪造头。
- 展示与审计分离：用户隐私（前缀）与运营风控（全文）可并存。
- IP 挂在 topic/comment 实体而非 posts 内容体：语义是「谁在什么 IP 创建了这条」。

## Consequences

- 生产部署文档必须写清 `TRUST_PROXY` / `TRUSTED_PROXIES`。
- 历史会话/主题在迁移后 `ip_address` 为空串，仅新写入有值。
- 管理端「查看发帖 IP」UI/权限可后续加（数据已落库）。

## Follow-ups (2026-07-12)

- 权限键定为 **`moderation.view_ip`**（与 review 分离；版主模板默认有；
  持有 review 的角色迁移时补权）。
- 编辑路径写 **`last_edit_ip`**；创建 `ip_address` 不变。
- 隐私清理：`ClearUserClientIPs` + admin API；审计 metadata 中的历史 IP
  暂保留。账号删除/封禁产品流落地后应在状态变更中自动调用。

## Open Questions

- 审计流水中的 `ipAddress` 是否随隐私清理一并脱敏/删除。

## 2026-10-27 修订

生产实测 `user_sessions.ip_address` 记录成 `192.168.*`（服务端跳数地址）后，上面
的信任模型有三处被推翻。原文保留为历史，以下是当前生效的语义。

1. **`TrustPrivate` 只作用于 TCP 对端。** 原实现把同一个 `isTrusted` 复用到
   `X-Forwarded-For` 链条目：打开私网信任后，内网客户端（如 `192.168.160.20`）
   会被当成代理剥掉，最后仍然回退对端地址。现在拆成两个判定：
   - `isTrustedPeer` = 显式 `Proxies` ∪ 私网 ∪ loopback ∪ link-local，只用于对端；
   - `isProxyAddr` = 显式 `Proxies` ∪ loopback ∪ link-local（后两者各受
     `TrustLoopback` / `TrustLinkLocal` 约束），**绝不含 RFC1918**，用于链条目。
   理由是链上的私网地址更可能是内网客户端本身，剥掉它只会把结果换成对端地址。
2. **`TRUST_PROXY` 成为 `clientip` 总开关。** 此前 `server.go` 只把三个信任集合
   传进 `clientip.Config`，`TRUST_PROXY=false` 关不掉头解析、单独设 `true` 又无效。
   现在 `Config` 带 `TrustProxy`，零值 fail-closed（`NewResolver(Config{})` 不读任何
   转发头），包级默认解析器显式补 `TrustProxy: true`；启动时打一行生效策略日志。
3. **标准 Compose 默认信任私网对端。** `api` 只发布在
   `127.0.0.1:${API_PORT:-18080}`，普通请求的 TCP 对端必然是 compose 网络里的
   `web`（Nuxt）容器，所以 `TRUST_PROXY=true`、`TRUST_PROXY_PRIVATE=true`、
   `TRUST_PROXY_LOOPBACK=false`、`TRUSTED_PROXIES` 空成为默认值，
   `compose.yaml`、`deploy/scripts/configure-production.sh` 与
   `.env.production.example` 三处一致。安全边界=宿主机 + compose 网络；把 API
   端口发布到公网即失效，必须关掉 `TRUST_PROXY`。零停机栈的 `api-blue/green`
   通过 `extends` 继承同一组默认值。
4. **限流键统一走 `clientip.FromCtx`。** Fiber 的 `c.IP()` 在「链条全部判为可信」
   时会回退最左条目（`fiber@v3.4.0/req.go`），与 `clientip` 判定分歧，且客户端自带
   的 `X-Forwarded-For` 可以分裂限流桶。写限流 `KeyGenerator` 改为
   `clientip.FromCtx`，与审计 IP 同源。
5. **web 层做规范化。** Nuxt 在转发 `/api/v1/*` 之前解析出唯一客户端 IP，把
   `X-Forwarded-For` 覆盖为单值、`X-Real-IP` 覆盖为该值，并删除
   `CF-Connecting-IP` / `True-Client-IP`，使 API 侧链条只有一个条目。
   边界要说清（2026-10-27 复核修正）：`web` 只发布在 `127.0.0.1:${WEB_PORT}`，
   公网不可达；但私网与 loopback 对端恒可信，宿主机进程、同 compose 网络的容器、
   能连 loopback 的本地用户仍可指定这个值。「信任边界内的直连」等价于宿主机权限，
   不是新增攻击面，但也**不是**「消除了直连伪造面」。
6. **CDN 的真实客户端由 Caddy 负责，应用侧不再解析。** 官方 Caddyfile 用
   `{client_ip}` 写 `X-Real-IP`，并删除 `CF-Connecting-IP` / `True-Client-IP`；
   CDN 出口段配在 Caddy 的 `servers.trusted_proxies`（Caddy 2.10 不接受顶层同名
   选项），Cloudflare 另需显式 `client_ip_headers CF-Connecting-IP`（默认只认
   `X-Forwarded-For`）。应用侧 `TRUSTED_PROXIES` 在 CDN 场景保持为空：Caddy 已给出
   唯一值，再信任边缘段会让 Nuxt 把这条唯一记录当代理剥掉、回退容器网关地址。

原决策第 2 条「production 默认 `TRUST_PROXY=false`」仍然准确描述 `config` 的**内置
默认值**（裸机手写环境变量时成立），但不再是标准 Compose 部署的行为：Compose 与
向导生成的环境文件都显式写入了上面的新默认值。计划与验收见
`plans/2026-10-27-client-real-ip-production.md`。

## 2026-10-28 修订：web 层不再解析对端

**发现**：2026-10-27 的第 5 条在真实运行时从未生效，而且是净负面。Nuxt 4.4 /
Nitro 2.13 的请求管线（dev 与生产 `bun` preset 相同）经
`localFetch → fetchNodeRequestHandler → node-mock-http` 构造 h3 事件，mock
`IncomingMessage.socket.remoteAddress` 恒为空串。于是 `resolveClientIP('')`
返回空串，`applyClientIPHeaders(headers, '')` 把 `X-Real-IP` / `X-Forwarded-For` /
`CF-Connecting-IP` / `True-Client-IP` **全部删除**，API 只能回退 TCP 对端
（web 容器）——这正是「会话/审计记成容器地址」的直接原因。单测用
`createServer(toNodeListener(app))`（真实 socket）所以没抓到。

**修订**：

1. web 层不再判断「对端是否可信」（运行时拿不到），改为信任入口代理写入的单值头：
   `X-Real-IP` → `X-Forwarded-For` 最右有效项 → 空；规范化后写回单值并删 CDN 头。
2. 空值仍 fail-closed（四个头全删），生产环境首次触发打一条 warn，避免入口漏配
   再次静默降级。
3. `TRUSTED_PROXIES` 只作用于 `api`；`web` 不再接收该变量。
4. 信任边界改为部署约束：**web 端口只发布到 loopback/内网**。公网可达时客户端
   可自定审计 IP——这是本模型的唯一取舍，写进部署文档；需要更强保证时再上
   HMAC 跳签名。
5. 回归测试固定 Nitro 形状（`socket.remoteAddress === ''`），防止实现退回 peer 判定。

API 侧（第 1–4 条）保持不变：`clientip.FromCtx` 仍是唯一解析入口，限流键同源。
