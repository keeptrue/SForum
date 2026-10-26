# 2026-10-27 生产真实客户端 IP Handoff

## Changed

- `apps/api/app/Support/ClientIP/clientip.go`：`Config` 新增 `TrustProxy`（总开关，
  零值 fail-closed）；`isTrustedPeer`（对端：显式 ∪ 私网 ∪ loopback ∪ link-local）与
  `isProxyAddr`（链条目：显式 ∪ loopback ∪ link-local，**不含 RFC1918**）拆开；
  `Resolve` 在总开关关闭或对端不可信时直接返回对端；包级默认解析器补
  `TrustProxy: true`。
- `apps/api/app/Http/server.go`：`clientip.Configure` 传 `TrustProxy`；启动打一行
  生效策略日志；warning 条件改为「开了信任但三种信任来源全空」；写限流
  `KeyGenerator` 改为 `clientip.FromCtx`（与审计 IP 同源）。
- `apps/web/server/utils/clientIp.ts`（新）+ `pluginRouteProxy.ts`：`proxyRouteRequest`
  在合并 `requestHeaders` **之后**覆盖 `x-forwarded-for`（单值）/`x-real-ip`，删除
  `cf-connecting-ip` / `true-client-ip`，使 API 侧链条只剩一个条目。
- `compose.yaml`：api 的 `TRUST_PROXY` / `TRUST_PROXY_PRIVATE` 默认 `true`，
  `TRUST_PROXY_LOOPBACK` 默认 `false`，`TRUSTED_PROXIES` 默认空；web 也拿到
  `TRUSTED_PROXIES`。零停机栈 `api-blue/green` 经 `extends` 继承（`compose config` 实测）。
- `deploy/scripts/configure-production.sh`：生成的 `.env.production` 写入四项并回显
  一行生效策略；两个测试脚本断言具体期望值、compose.yaml 自身默认值、`TRUST_PROXY=false`
  一键回退。
- `.env.production.example`：四项改为生效默认值；`TRUSTED_PROXIES` 注释说明 CDN 场景
  同样留空（由 Caddy 处理）。
- `deploy/caddy/Caddyfile`：两个 `reverse_proxy` 块改为 `header_up X-Real-IP
  {client_ip}` + `header_up -CF-Connecting-IP` + `header_up -True-Client-IP`。
- 文档：中英 `deployment/environment.md` 重写「CSRF 与反向代理」（总开关语义 + 每跳
  对端拓扑表），`manual.md` / `deployment.md` 改掉「同机 Caddy 用 `127.0.0.1/32`」配方。

## 复核返工（同日第二轮，P0/P1）

- **P0**：原 Caddyfile 只覆盖 `X-Real-IP`，`CF-Connecting-IP` / `True-Client-IP` 原样
  透传——任意公网客户端自带这两个头就能指定落库 IP 并分裂写限流桶。改为 `{client_ip}`
  重算 + 删除两个 CDN 头。用 `caddy:2.10.2-alpine`（host 网络 + 头回显服务）实测两个块：
  伪造头到上游时已消失，`X-Real-IP` 是真实对端，`X-Forwarded-For` 被 Caddy 重写。
- **P1**：文档原「CDN 段写进应用 `TRUSTED_PROXIES`」照做反而更错（Nuxt 会把唯一一条
  边缘 IP 当代理剥空、回退容器网关）。改为写进 Caddy 的 `servers.trusted_proxies`，
  应用侧留空。实测：`trusted_proxies static 127.0.0.1` + `client_ip_headers
  CF-Connecting-IP ...` 后，`CF-Connecting-IP` 成为 `{client_ip}` 并在转发前被删除；
  Caddy 2.10 不接受顶层 `trusted_proxies`（adapt 报 unrecognized global option）。
- **P1**：裸机 LAN 行自相矛盾已改（显式 `TRUST_PROXY=true` + `TRUST_PROXY_PRIVATE=true`，
  注明 config 内置默认关闭）；补 WebSocket 直连 `:18080` 一行（对端 loopback，默认只记
  `127.0.0.1`，WS 不写会话 IP）。
- **P1**：决策记录删掉「消除直连 `:3000` 伪造面」——私网/loopback 对端恒可信，边界内
  直连等价于宿主机权限。

## Decisions

- 「语义修正 + 正确默认值」：私网信任只作用于 TCP 对端，链上私网条目不被当代理剥掉；
  `TRUST_PROXY` 是唯一总开关；Compose 默认信任私网对端。信任边界 = 宿主机 + compose
  网络（api 只发布 loopback），端口一旦发布到公网必须 `TRUST_PROXY=false`。
- CDN 解析职责上移到 Caddy：`{client_ip}` + `servers.trusted_proxies`（Cloudflare 另加
  `client_ip_headers CF-Connecting-IP`）。应用侧在 CDN 场景保持空，不再承担 CDN 解析。
- `config` 内置默认值不变（生产 fail-closed）：裸机手写环境变量仍要自己写
  `TRUST_PROXY=true`，文档已点明。
- 未采用固定子网（需重建网络、与宿主机路由冲突）与 HMAC 跳签名（跨语言密钥分发），
  理由见 `plans/2026-10-27-client-real-ip-production.md`。

## Next

- 真机验证两条路径：公网客户端记录公网 IP；内网客户端（`192.168.160.20`）记录内网 IP
  而不是容器地址。当前只有配置、单元与容器内实测证据，没有真实拓扑取证。
- 存量部署升级时 `.env.production` 不会自动补齐四项，`compose.yaml` 默认值会兜住。
- 可选：`GET /api/v1/admin/system/client-ip` 诊断端点；历史错误 IP 的清理说明。

## Open Questions

- 审计流水里的历史 `ipAddress` 是否随隐私清理一起处理（沿用 2026-07-12 的开放项）。
- 多级代理 / 自定义边缘是否需要升级到 HMAC 跳签名。

## 验证

- `caddy validate` 接受官方 Caddyfile 与文档给出的 CDN 变体（`caddy:2.10.2-alpine`）；
  `caddy adapt` 显示两块都是 `delete: [CF-Connecting-IP, True-Client-IP]` +
  `set X-Real-IP: {http.vars.client_ip}`。
- `docker compose ... config --quiet`、`deploy_test.sh`、`zero-downtime-compose_test.sh`、
  `validate-docs.mjs`、两个部署测试脚本全绿。
- 代码侧测试结果由 task-1 / task-2 / task-4 负责，本交接不代述。
