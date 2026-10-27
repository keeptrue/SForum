import { isIP } from 'node:net'

/**
 * web（Nuxt）层的客户端 IP 规范化。
 *
 * 为什么 web 层不判断「对端是否可信」：
 * Nuxt 4.4 / Nitro 2.13 的请求管线（dev 与生产 `bun` preset 相同）走
 * `localFetch → fetchNodeRequestHandler → node-mock-http`，h3 事件里的
 * `event.node.req.socket` 是 mock Socket，`remoteAddress` 恒为空串。
 * 也就是说 web 层拿不到 TCP 对端，任何基于 peer 的信任判定都是死代码；
 * 更糟的是它会把入口代理已经写好的真实 IP 当成「不可信」整组删掉，
 * 让 API 只能回退到 web 容器地址（2026-10-27 那版实现的实际后果）。
 *
 * 因此 web 层的职责收敛为一条：**信任入口代理写入的单值头，规范化后上行**。
 * 信任边界由部署保证：web 只在 loopback/内网发布（compose.prod 绑 127.0.0.1），
 * 公网唯一入口是 Caddy；Caddy 用 `header_up X-Real-IP {client_ip}` 覆盖该头，
 * 并删除 CF-Connecting-IP / True-Client-IP。若把 web 端口发布到公网，
 * 客户端即可自定审计 IP——这条硬约束写在部署文档里。
 *
 * 解析顺序：x-real-ip（入口覆盖，权威）→ x-forwarded-for 最右有效项（自定义反代兜底）→ 空。
 * 空值仍然 fail-closed：四个客户端 IP 头全部删除，宁可让 API 回退 TCP 对端，
 * 也不转发未经校验的声明。CDN 单值头在这里一律不采信：它们没有中间代理替我们覆盖，
 * 由 Caddy 的 `servers.trusted_proxies` + `{client_ip}` 负责解析。
 */

const X_REAL_IP = 'x-real-ip'
const X_FORWARDED_FOR = 'x-forwarded-for'
const CDN_CLIENT_HEADERS = ['cf-connecting-ip', 'true-client-ip'] as const
const IPV4_MAPPED_MARKER = 0xffffn
const IPV4_MAX = 0xffffffffn

interface ParsedAddress {
  /** 规范化文本：IPv4 点分十进制，IPv6 为 RFC 5952 压缩形式；`::ffff:` 一律归一成 IPv4。 */
  text: string
  family: 4 | 6
  value: bigint
}

let warnedMissingEdgeIP = false

/**
 * 解析入口代理给出的客户端 IP。
 *
 * 只做「取值 + 校验 + 规范化」，不判断来源可信度：运行时拿不到 TCP 对端，
 * 可信度由部署拓扑保证（web 不可从公网直达）。非法或缺失返回空串。
 */
export function resolveEdgeClientIP(headers: Headers): string {
  const realIp = parseAddress(headers.get(X_REAL_IP))
  if (realIp) return realIp.text

  const chain = headers.get(X_FORWARDED_FOR)
  if (!chain) return ''
  // 从右往左取第一个可解析条目：最近一跳代理追加的值，客户端无法把它顶掉。
  const entries = chain.split(',')
  for (let index = entries.length - 1; index >= 0; index--) {
    const candidate = parseAddress(entries[index])
    if (candidate) return candidate.text
  }
  return ''
}

/**
 * 把上行头覆盖成单值，并删掉其余可伪造的客户端 IP 头。
 * ip 为空时同样删除全部四个头：宁可让 API 回退到 TCP 对端，也不转发任何未经校验的声明。
 */
export function applyClientIPHeaders(headers: Headers, ip: string): void {
  for (const name of CDN_CLIENT_HEADERS) headers.delete(name)
  if (!ip) {
    headers.delete(X_FORWARDED_FOR)
    headers.delete(X_REAL_IP)
    return
  }
  headers.set(X_FORWARDED_FOR, ip)
  headers.set(X_REAL_IP, ip)
}

/**
 * 代理上行前的统一入口：解析入口头、覆盖上行头、必要时告警。
 * 生产环境解析为空只告警一次，避免入口漏配再次静默降级成容器地址。
 */
export function normalizeUpstreamClientIP(headers: Headers): void {
  const ip = resolveEdgeClientIP(headers)
  if (!ip) warnMissingEdgeIP()
  applyClientIPHeaders(headers, ip)
}

function warnMissingEdgeIP() {
  if (warnedMissingEdgeIP || process.env.APP_ENV !== 'production') return
  warnedMissingEdgeIP = true
  console.warn(
    '[client-ip] inbound request carried no valid x-real-ip / x-forwarded-for; '
    + 'no client IP header was forwarded (check the ingress proxy configuration)'
  )
}

function parseAddress(raw: string | null | undefined): ParsedAddress | undefined {
  if (!raw) return undefined
  const text = stripPortAndZone(raw)
  if (!text) return undefined

  const version = isIP(text)
  if (version === 4) {
    const value = ipv4Value(text)
    return { text: formatIpv4(value), family: 4, value }
  }
  if (version !== 6) return undefined

  const groups = expandIpv6(text)
  if (!groups) return undefined
  const value = groupsToValue(groups)
  // IPv4-mapped IPv6（::ffff:a.b.c.d）归一成 IPv4，避免同一地址在两种写法下判定分裂。
  if ((value >> 32n) === IPV4_MAPPED_MARKER) {
    const mapped = value & IPV4_MAX
    return { text: formatIpv4(mapped), family: 4, value: mapped }
  }
  return { text: formatIpv6(groups), family: 6, value }
}

// 与 Go 的 net.SplitHostPort + Trim("[]") 对齐：只剥「host:port」与「[v6]:port」，
// 裸 IPv6 的多个冒号不能被误当端口分隔符。
function stripPortAndZone(raw: string) {
  let text = raw.trim()
  const bracketed = /^\[([^\]]+)\](?::[^:]*)?$/.exec(text)
  if (bracketed?.[1]) {
    text = bracketed[1]
  } else {
    const colon = text.indexOf(':')
    if (colon > 0 && text.indexOf(':', colon + 1) === -1 && isIP(text.slice(0, colon)) === 4) {
      text = text.slice(0, colon)
    }
  }
  const zone = text.indexOf('%')
  return zone >= 0 ? text.slice(0, zone) : text
}

function expandIpv6(text: string): number[] | undefined {
  const normalized = replaceTrailingIpv4(text)
  if (normalized === undefined) return undefined
  const parts = normalized.split('::')
  if (parts.length > 2) return undefined
  const head = parseHextets(parts[0] ?? '')
  if (!head) return undefined
  if (parts.length === 1) return head.length === 8 ? head : undefined
  const tail = parseHextets(parts[1] ?? '')
  if (!tail) return undefined
  const missing = 8 - head.length - tail.length
  if (missing < 1) return undefined
  return [...head, ...new Array<number>(missing).fill(0), ...tail]
}

function parseHextets(part: string): number[] | undefined {
  if (!part) return []
  const groups: number[] = []
  for (const piece of part.split(':')) {
    if (!/^[0-9a-f]{1,4}$/i.test(piece)) return undefined
    groups.push(Number.parseInt(piece, 16))
  }
  return groups
}

function replaceTrailingIpv4(text: string): string | undefined {
  if (!text.includes('.')) return text
  const colon = text.lastIndexOf(':')
  if (colon < 0) return undefined
  const tail = text.slice(colon + 1)
  if (isIP(tail) !== 4) return undefined
  const value = ipv4Value(tail)
  return `${text.slice(0, colon + 1)}${(value >> 16n).toString(16)}:${(value & 0xffffn).toString(16)}`
}

function groupsToValue(groups: number[]) {
  let value = 0n
  for (const group of groups) value = (value << 16n) | BigInt(group)
  return value
}

function ipv4Value(text: string) {
  let value = 0n
  for (const part of text.split('.')) value = (value << 8n) | BigInt(Number(part))
  return value
}

function formatIpv4(value: bigint) {
  return [24n, 16n, 8n, 0n].map(shift => Number((value >> shift) & 0xffn)).join('.')
}

// RFC 5952：最长的一段连续零组（长度 ≥ 2）压缩为 ::，十六进制小写且无前导零。
function formatIpv6(groups: number[]) {
  let bestStart = -1
  let bestLength = 0
  for (let index = 0; index < groups.length; index++) {
    if (groups[index] !== 0) continue
    let end = index
    while (end < groups.length && groups[end] === 0) end++
    if (end - index > bestLength) {
      bestStart = index
      bestLength = end - index
    }
    index = end - 1
  }
  const hex = groups.map(group => group.toString(16))
  if (bestLength < 2) return hex.join(':')
  return `${hex.slice(0, bestStart).join(':')}::${hex.slice(bestStart + bestLength).join(':')}`
}
