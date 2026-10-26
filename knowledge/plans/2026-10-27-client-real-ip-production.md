# 生产真实客户端 IP 修复计划书

**Status**: ready（2026-10-27 批准，实施中）
**Scope**: `apps/api` 真实 IP 解析、`apps/web` 同域 API 代理、`compose*.yaml`、`deploy/scripts`、部署文档
**Relates**: 修订 `knowledge/decisions/2026-07-12-client-real-ip.md` 的信任模型

## Context

生产部署后，管理端「用户信息预览 / 登录设备」显示 `192.168.160.5`（脱敏 `192.168.160.*`），与访问者真实地址不符。

影响面不止展示：

- 审计与风控失真：`user_sessions.ip_address`、登录审计 `metadata.ipAddress`、`moderation.view_ip` 的 IP 全部来自同一解析入口。
- 论坛内容溯源失真：`topics.ip_address` / `comments.ip_address` / `last_edit_ip` 同源。
- 限流与审计键不一致：写入限流按 IP 计数，与审计记录不是同一个值。

## 根因

链路：

```
浏览器 ──TLS──> 宿主机 Caddy(443) ──HTTP──> web(Nuxt, 127.0.0.1:3000) ──HTTP──> api(Go, 8080)
                    设置 X-Real-IP/XFF        原样复制这两个头              取 TCP 对端
```

普通 `/api/v1/*` 一律经 Nuxt 同域转发（`apps/web/server/routes/api/v1/[...path].ts` →
`apps/web/server/utils/pluginRouteProxy.ts`）。只有 WebSocket Upgrade 由 Caddy 直打
`127.0.0.1:18080`（`deploy/caddy/Caddyfile`）。

三个事实叠加：

1. Caddy 写入真实客户端头 `header_up X-Real-IP {remote_host}`，并按默认行为追加
   `X-Forwarded-For`。
2. Nuxt 用 h3 `getProxyRequestHeaders` 原样复制这两个头（`h3@1.15.11` 的 `ignoredHeaders`
   只过滤 hop-by-hop 与 `host`），既不追加自己的跳，也不做任何校验。
3. API 的 `clientip` 只在对端可信时才读这些头；而生产默认不信任任何对端
   （`TRUST_PROXY=false`、`TRUST_PROXY_PRIVATE=false`、`TRUST_PROXY_LOOPBACK=false`、
   `TRUSTED_PROXIES` 为空）。

于是 `Resolve` 回退 `RemoteIP()`，把 **Nuxt 容器（或宿主机）地址**当成客户端 IP。
按标准 Compose 部署，该路径 100% 复现。

### 次生缺陷

| 编号 | 缺陷 | 证据 | 后果 |
| --- | --- | --- | --- |
| D1 | 生产默认零信任，部署流程从不写这四个变量 | `apps/api/config/config.go`；`compose.yaml` 传空值；`configure-production.sh` 无相关写入 | 开箱即错 |
| D2 | `TrustPrivate` 同时作用于「对端」和「XFF 链条目」 | `clientip.go` 的 `isTrusted` 被三处复用 | 打开私网信任后内网客户端被当代理剥掉，仍回退对端 |
| D3 | `TRUST_PROXY` 不是 `clientip` 的总开关 | `server.go` 只传三个信任集合 | `false` 关不掉头解析，`true` 单独设置又无效 |
| D4 | 文档配方在 Compose 拓扑下不成立 | `docs/zh-CN/deployment/environment.md` 建议「同机 Caddy 用 127.0.0.1/32」 | API 对端是 `web` 容器，配了无效 |
| D5 | 限流键与 `clientip` 判定分歧 | `fiber@v3.4.0/req.go` 在链条全可信时回退最左条目 | 客户端自带 XFF 可分裂限流桶 |

## Decision

采用「语义修正 + 正确默认值」（方案 B）：

- `TrustPrivate` 只作用于 TCP 对端；链条目只认显式代理与 loopback。
- `TRUST_PROXY` 成为 `clientip` 总开关，`Config` 零值 fail-closed。
- Compose 拓扑默认信任私网对端（API 仅 loopback 发布，信任边界=宿主机与 compose 网络）。
- web 层解析出唯一客户端 IP 后覆盖上行头，使 API 侧链条只有一个条目。

否决方案：

- **A 固定子网 + CIDR**：精确零代码，但存量部署必须 `docker compose down` 重建网络，
  且与宿主机既有路由冲突时启动失败、裸机不适用。作为严格模式写进文档。
- **C HMAC 跳签名**：最强（不依赖网段），但新增跨语言密钥分发与 TS/Go 双实现。
  留作未来多级代理/自定义边缘的升级路径。

## Plan

### P0-1 `apps/api/app/Support/ClientIP/clientip.go`

`Config` 新增 `TrustProxy`；拆分 `isTrustedPeer`（对端：显式 ∪ 私网 ∪ loopback ∪
link-local）与 `isProxyAddr`（链条目：显式 ∪ loopback ∪ link-local，绝不含 RFC1918）。
`Resolve` 在 `!TrustProxy` 时直接返回对端。包级默认值补 `TrustProxy: true`。

### P0-2 `apps/api/app/Http/server.go`

`Configure` 传 `TrustProxy`；启动打一行生效策略日志；warning 条件改为「trust 打开但三种
信任来源全空」；写限流 `KeyGenerator` 统一走 `clientip.FromCtx`。

### P0-3 `compose.yaml`

api：`TRUST_PROXY`/`TRUST_PROXY_PRIVATE` 默认 `true`，`TRUST_PROXY_LOOPBACK` 默认
`false`，`TRUSTED_PROXIES` 默认空。web：追加 `TRUSTED_PROXIES`。注释写清安全前提。

### P0-4 `deploy/scripts/configure-production.sh` + `.env.production.example`

生成 `.env.production` 时写入四项并在摘要回显；示例文件改成生效默认值。

### P1-1 `apps/web` 客户端 IP 规范化

新增 `apps/web/server/utils/clientIp.ts`，规则与 Go 侧一一对应；在 `proxyRouteRequest`
中覆盖 `x-forwarded-for`（单值）与 `x-real-ip`，删除 `cf-connecting-ip` / `true-client-ip`。

### P2 收尾

文档纠错（中英双语）、决策记录修订、模块笔记与热交接。诊断端点与历史数据清理为可选项，
未纳入本批。

## Acceptance

1. 全新生产部署（compose + Caddy），内网登录记录内网 IP，公网登录记录公网 IP，不再出现
   服务端跳数地址。
2. 经 Caddy 的任意伪造头（`X-Forwarded-For` / `X-Real-IP` / `CF-Connecting-IP` /
   `True-Client-IP`）不改变记录值，也不能打散写限流桶；直连 `:3000` / `:18080` 的伪造
   只在宿主机与 compose 网络信任边界内可能（等价宿主机权限），公网不可达。
3. `TRUST_PROXY=false` 时所有转发头失效，可回退。
4. 启动日志可见生效策略一行。
5. Go/TS 测试、架构边界闸门全绿。

## Risks

- 打开私网对端信任后，同网段的恶意容器/进程可伪造客户端 IP（写进审计）。缓解：API 仅
  loopback 发布、web 层成为唯一出口、诊断能力可后续补。
- CDN 的真实客户端由 Caddy 全局 `trusted_proxies` + `header_up X-Real-IP {client_ip}`
  负责；应用侧 `TRUSTED_PROXIES` 在标准拓扑留空。把 CDN 段写进应用侧反而会把唯一一条
  边缘 IP 当代理剥掉、回退容器网关（复核实测）。
- WebSocket Upgrade 由 Caddy 直送 `127.0.0.1:18080`，该路径的 TCP 对端是 loopback；
  默认 `TRUST_PROXY_LOOPBACK=false` 时只记录 `127.0.0.1`。WS 不写会话 IP，故无用户可见
  影响，需要时单独打开 loopback 信任。
- 无真机验证的生产拓扑分支（裸机 + LAN IP）仅靠文档覆盖。

## 修订（2026-10-27 复核轮）

第一轮独立复核判定不通过，已返工三处：

- **P0**：`CF-Connecting-IP` / `True-Client-IP` 在默认拓扑下可被任意公网客户端伪造穿透
  （Caddy 只覆盖 `X-Real-IP`）。修法三层：Caddy 删除这两个头、web 与 API 两侧都要求
  「对端是显式代理」才采信 CDN 单值头。
- **P1**：文档的 CDN 配方会把客户端解析回容器网关；改为由 Caddy `trusted_proxies` 承担。
- **P1**：决策记录里「消除直连 `:3000` 伪造面」的表述与实测不符，改为边界描述。

## Open Questions

- 是否补 `GET /api/v1/admin/system/client-ip` 诊断端点。
- 是否清理历史错误 IP（当前不清理，仅提供运维说明）。
