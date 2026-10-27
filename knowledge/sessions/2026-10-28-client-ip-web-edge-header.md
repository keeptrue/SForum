# 2026-10-28 web 层客户端 IP：改为信任入口头

**起因**：生产 `user_sessions.ip_address`、登录审计、`moderation.view_ip` 与写限流键
仍记成容器地址。上一轮（2026-10-27，`4117f296a`）的 API 侧修复是对的，但 web 层的
「解析唯一客户端 IP」从未生效。

## 根因（已取证）

Nuxt 4.4 / Nitro 2.13 的请求管线（dev 与生产 `bun` preset 相同）经
`localFetch → fetchNodeRequestHandler → node-mock-http` 构造 h3 事件：mock
`IncomingMessage.socket` 是 mock `Socket`，`remoteAddress` 恒为 `""`。
`clientPeerAddress()` 因此返回空串，`resolveClientIP('')` 也是空串，
`applyClientIPHeaders(headers, '')` 把 `X-Real-IP` / `X-Forwarded-For` /
`CF-Connecting-IP` / `True-Client-IP` 全部删除，API 只能回退 TCP 对端（web 容器）。

活体证据（dev，2026-10-28）：

- 经 `:3000` 发 `X-Real-IP: 203.0.113.200` / `X-Forwarded-For: 203.0.113.201`，
  两次都落进同一个 Redis 限流键 `127.0.0.1`（29→28）；
- 直连 `:8080` 发 `203.0.113.91` / `.92`，各自独立成键；
- `user_sessions.user_agent_raw` 有真实 UA（45 条），证明头透传正常，被删的只有 IP 头；
- `node-mock-http@1.0.4` 的 mock `Socket.remoteAddress = ""`；h3 自己的
  `getRequestIP` 也为空 socket 加了守卫；
- 旧测试用 `createServer(toNodeListener(app))`（真实 socket），所以没抓到。

## Changed

- `apps/web/server/utils/clientIp.ts`：删掉 peer / `TRUSTED_PROXIES` 整套逻辑，新增
  `resolveEdgeClientIP(headers)`（`x-real-ip` → XFF 最右有效项 → 空）与
  `normalizeUpstreamClientIP(headers)`（解析 + 生产首次空值 warn + 覆盖单值）。
- `server/utils/pluginRouteProxy.ts`、`server/utils/notifications/notificationStreamProxy.ts`：
  改用 `normalizeUpstreamClientIP`，删除 `clientPeerAddress`。
- `compose.yaml`：web 服务不再接收 `TRUSTED_PROXIES`；注释写明 web 端口必须只发布到
  loopback。
- `.env.production.example`：`TRUSTED_PROXIES` 标注「只作用于 api」。
- `deploy/caddy/Caddyfile`：注释改为「Nuxt 转发本层写入的 X-Real-IP，不再解析」。
- `deploy/scripts/compose-production_test.sh`：断言反过来——web 不得收到
  `TRUSTED_PROXIES`。
- 文档（中英 `deployment/environment.md`）：新增「web 层如何传客户端 IP」与
  「症状：会话/审计里记成容器地址」两节。
- 测试：重写 `tests/framework/clientIp.test.ts`；`pluginRouteProxy.test.ts` 的客户端
  IP 用例固定 Nitro 形状（`socket.remoteAddress = ''`）；`notificationStreamProxy.test.ts`
  同步。

## Decisions

- web 层不再判断「对端是否可信」：运行时拿不到 peer，判定是死代码且会删掉真值。
- 信任边界改为部署约束：**web 端口只发布到 loopback/内网**；公网可达时客户端可自定
  审计 IP。需要更强保证时再上 HMAC 跳签名。
- `TRUSTED_PROXIES` 只服务 API 侧的「对端是显式代理」场景（CDN 单值头），web 不需要。
- 空值 fail-closed 保留，但生产首次触发要 warn，避免再次静默降级。

## 验证

- 单测：`bun test` 全量 **1798 pass / 213 files，0 fail**；`bun run typecheck` 0。
- dev 活体（2026-10-28，Nuxt dev + `sforum-api`）：
  - 经 `:3000` 发 `X-Real-IP: 203.0.113.200` / `203.0.113.201`，Redis 限流键分别为
    `203.0.113.200` / `203.0.113.201`（修复前是同一个 `127.0.0.1`，29→28）；
  - 经 `:3000` 用 `X-Real-IP: 203.0.113.77` 注册一次性用户 `qa_clientip_1028`
    （dev 库 id=456）：`user_sessions.ip_address = 203.0.113.77`、
    `ip_prefix = 203.0.113.*`、`audit_events.metadata.ipAddress = 203.0.113.77`；
  - 该用户只用于取证，留在 dev 库；要清理时按正常用户删除流程处理。
- 部署闸门：`deploy/scripts/compose-production_test.sh`、`node tests/validate-docs.mjs`、
  `node tests/validate-architecture-boundaries.mjs` 全绿；`go build ./...` 通过（Go 侧未改）。
- 生产 bun preset 实测（2026-10-28）：`bun run build` 后以
  `PORT=3100 NUXT_API_INTERNAL_BASE_URL=http://127.0.0.1:8080/api/v1 APP_ENV=production
  bun .output/server/index.mjs` 起预览，经 `:3100` 发 `X-Real-IP: 203.0.113.210` /
  `203.0.113.211`，Redis 限流键分别为这两个 IP；无头请求触发一次
  `[client-ip] inbound request carried no valid x-real-ip ...` 告警，符合预期。
- 待补：真机公网/内网两条路径取证。

## 发布

- v3.0.22（2026-10-28）：`scripts/release.sh 3.0.22 --type stable`，Release workflow
  全绿（Verify main CI 8m27s、六路镜像构建与合并、四平台资产、smoke、SDK 发布、
  promote、匿名拉取验证、GitHub Release）。
  https://github.com/keeptrue/SForum/releases/tag/v3.0.22

## Next

- 生产镜像验证（bun preset）与真机公网/内网两条路径取证。
- 历史错误 IP 不回溯。
- 可选：`GET /api/v1/admin/overview` 增 `clientIp` 字段，便于线上自检。
