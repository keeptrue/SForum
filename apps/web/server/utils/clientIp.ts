import { isIP } from 'node:net'

/**
 * web（Nuxt）层的客户端 IP 规范化。
 *
 * 为什么 web 层必须做这一步：
 * 1. 生产链路是「浏览器 → Caddy → web(Nuxt) → api(Go)」。Caddy 写入 X-Real-IP / XFF 后，
 *    Nuxt 用 h3 的 getProxyRequestHeaders 原样复制，API 只看到 web 容器这个 TCP 对端；
 *    在零信任默认下，审计、限流、会话记录全部把这个跳数地址当成客户端。
 * 2. 转发头本身可伪造：直连 :3000 的请求可以随便写 XFF / CF-Connecting-IP。
 * 所以 web 是客户端 IP 的唯一权威出口：先按信任规则解析出唯一 IP，再把上行头覆盖成单值，
 * 让 API 侧看到的链条永远只有一个条目。
 *
 * 规则与 apps/api/app/Support/ClientIP/clientip.go 一一对应：
 * - 对端（TCP peer）：loopback 与 RFC1918/ULA 私网恒可信（web 容器只可能被宿主机或同网络
 *   容器访问）；公网对端一律不可信，此时直接返回对端，转发头一概不采信。
 * - 链条目：只认显式代理（TRUSTED_PROXIES）与 loopback。私网不是代理，因此私网客户端
 *   不会被当成跳数剥掉（Go 侧 D2 修复后的语义）。
 * - 门控通过后的解析顺序：CDN 单值头（仅当对端是显式代理）→ x-real-ip → x-forwarded-for（右→左）→ 对端。
 *
 * CDN 单值头（cf-connecting-ip / true-client-ip）有一道额外门控：**只有对端本身落在显式
 * cfg.proxies 里才采信**。标准拓扑是 Caddy 在前，它只用 header_up 覆盖 X-Real-IP，不会
 * 剥掉这两个头——任何人都能往请求里写一个假 CF 头，若只看「对端可信」就采信，公网客户端
 * 可以凭空指定自己的审计 IP 并打散写限流桶。真正接 Cloudflare 时，让 CDN 边缘直连 web
 * 并把 CDN 出口段写进 TRUSTED_PROXIES，这两个头才重新可信。
 */

const CDN_CLIENT_HEADERS = ['cf-connecting-ip', 'true-client-ip'] as const
const X_REAL_IP = 'x-real-ip'
const X_FORWARDED_FOR = 'x-forwarded-for'
const IPV4_MAPPED_MARKER = 0xffffn
const IPV4_MAX = 0xffffffffn

export interface ClientIPConfig {
  /** 显式信任的代理 IP 或 CIDR，来自 TRUSTED_PROXIES（逗号分隔）；非法条目静默忽略。 */
  proxies: string[]
}

interface ParsedAddress {
  /** 规范化文本：IPv4 点分十进制，IPv6 为 RFC 5952 压缩形式；`::ffff:` 一律归一成 IPv4。 */
  text: string
  family: 4 | 6
  value: bigint
}

interface ParsedProxy extends ParsedAddress {
  prefix: number
}

/**
 * 从环境变量读取代理信任列表。默认空表示不信任任何显式代理，
 * 与 API 侧 TRUSTED_PROXIES 的默认值保持一致。
 */
export function clientIPConfig(raw = process.env.TRUSTED_PROXIES ?? ''): ClientIPConfig {
  return {
    proxies: raw.split(',').map(entry => entry.trim()).filter(Boolean)
  }
}

/**
 * 解析唯一客户端 IP。对端非法时返回空串（调用方按 fail-closed 处理）。
 */
export function resolveClientIP(peer: string, headers: Headers, cfg: ClientIPConfig): string {
  const peerAddress = parseAddress(peer)
  if (!peerAddress) return ''

  const proxies = compileProxies(cfg.proxies)
  // 对端不是可信代理：绝不采信可伪造的转发头。
  if (!isTrustedPeer(peerAddress, proxies)) return peerAddress.text

  // 1) CDN 单值头：没有中间代理替我们覆盖这两个头，因此只有「对端本身就是显式配置的
  //    CDN 边缘」才采信；标准拓扑里对端是 Caddy，这里的伪造值必须被忽略。
  const peerIsExplicitProxy = proxies.some(proxy => matchesPrefix(peerAddress, proxy))
  if (peerIsExplicitProxy) {
    for (const name of CDN_CLIENT_HEADERS) {
      const candidate = parseAddress(headers.get(name))
      if (candidate && !isProxyAddress(candidate, proxies)) return candidate.text
    }
  }

  // 2) X-Real-IP：官方 Caddyfile 用 header_up 覆盖它，对端可信即可采信。
  const realIp = parseAddress(headers.get(X_REAL_IP))
  if (realIp && !isProxyAddress(realIp, proxies)) return realIp.text

  // 3) X-Forwarded-For：从右往左剥代理，取最右侧的非代理地址。
  const chain = headers.get(X_FORWARDED_FOR)
  if (chain) {
    const entries = chain.split(',')
    for (let index = entries.length - 1; index >= 0; index--) {
      const candidate = parseAddress(entries[index])
      if (!candidate || isProxyAddress(candidate, proxies)) continue
      return candidate.text
    }
  }

  return peerAddress.text
}

/**
 * 把上行头覆盖成单值，并删掉其余可伪造的客户端 IP 头。
 * ip 为空时同样删除全部四个头：宁可让 API 回退到 TCP 对端，也不转发任何未经校验的声明。
 */
export function applyClientIPHeaders(headers: Headers, ip: string): void {
  headers.delete('cf-connecting-ip')
  headers.delete('true-client-ip')
  if (!ip) {
    headers.delete(X_FORWARDED_FOR)
    headers.delete('x-real-ip')
    return
  }
  headers.set(X_FORWARDED_FOR, ip)
  headers.set('x-real-ip', ip)
}

function isTrustedPeer(address: ParsedAddress, proxies: ParsedProxy[]) {
  return isProxyAddress(address, proxies) || isPrivate(address)
}

// 链条目只认显式代理与 loopback；私网地址是客户端，不是跳数。
function isProxyAddress(address: ParsedAddress, proxies: ParsedProxy[]) {
  return isLoopback(address) || proxies.some(proxy => matchesPrefix(address, proxy))
}

function isLoopback(address: ParsedAddress) {
  if (address.family === 4) return (address.value >> 24n) === 0x7fn
  return address.value === 1n
}

function isPrivate(address: ParsedAddress) {
  if (address.family === 6) return (address.value >> 121n) === 0x7en // fc00::/7
  const first = Number((address.value >> 24n) & 0xffn)
  const second = Number((address.value >> 16n) & 0xffn)
  return first === 10
    || (first === 172 && second >= 16 && second <= 31)
    || (first === 192 && second === 168)
}

function matchesPrefix(address: ParsedAddress, proxy: ParsedProxy) {
  if (address.family !== proxy.family) return false
  const shift = BigInt(address.family === 4 ? 32 : 128) - BigInt(proxy.prefix)
  if (shift === 0n) return address.value === proxy.value
  return (address.value >> shift) === (proxy.value >> shift)
}

const proxyCache = new Map<string, ParsedProxy[]>()

function compileProxies(proxies: readonly string[]) {
  const key = proxies.join(',')
  const cached = proxyCache.get(key)
  if (cached) return cached
  const compiled = proxies.map(parseProxy).filter((proxy): proxy is ParsedProxy => Boolean(proxy))
  // 配置在进程内基本不变，只保留少量键即可，避免测试或热重载时无界增长。
  if (proxyCache.size > 8) proxyCache.clear()
  proxyCache.set(key, compiled)
  return compiled
}

function parseProxy(raw: string): ParsedProxy | undefined {
  const [addressPart, prefixPart, extra] = raw.split('/')
  if (extra !== undefined) return undefined
  const address = parseAddress(addressPart)
  if (!address) return undefined
  if (prefixPart === undefined) return { ...address, prefix: address.family === 4 ? 32 : 128 }
  if (!/^\d{1,3}$/.test(prefixPart)) return undefined
  const prefix = Number(prefixPart)
  if (prefix > (address.family === 4 ? 32 : 128)) return undefined
  return { ...address, prefix }
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
