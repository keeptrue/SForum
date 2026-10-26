import type { H3Event } from 'h3'
import {
  createError,
  getProxyRequestHeaders,
  getRequestURL,
  getRequestWebStream,
  removeResponseHeader,
  sendProxy,
  setResponseHeader
} from 'h3'

import { applyClientIPHeaders, clientIPConfig, resolveClientIP } from './clientIp'

export const INTERNAL_ROUTE_PROBE_HEADER = 'x-sforum-internal-route-probe'
export const INTERNAL_ROUTE_METHOD_HEADER = 'x-sforum-internal-route-method'
export const INTERNAL_ROUTE_RESULT_HEADER = 'x-sforum-internal-route-result'
export const INTERNAL_ROUTE_PROBE_VERSION = 'v1'
export const INTERNAL_ROUTE_MATCH = 'plugin'
export const INTERNAL_ROUTE_MISS = 'miss'
const SAFE_PROXY_ATTEMPTS = 10
const SAFE_PROXY_RETRY_DELAY_MS = 500

/**
 * 公开只读资产的跨源策略。
 *
 * 上游 API 对扩展/主题资产发的是 `Cross-Origin-Resource-Policy: same-origin`
 * 且不带 ACAO；QQ 内置浏览器（X5 云加速）、CDN 或企业代理把文档或子资源换源
 * 后，浏览器按跨源校验并拒载入口脚本与样式。这里在 sendProxy 复制完上游头之后
 * 覆盖，保证换源链路仍可加载。私有资产代理不走这条路径。
 */
const PUBLIC_ASSET_CORS_HEADERS = {
  'access-control-allow-origin': '*',
  'cross-origin-resource-policy': 'cross-origin',
  'timing-allow-origin': '*'
} as const

export function applyPublicAssetCorsHeaders(event: H3Event) {
  for (const [name, value] of Object.entries(PUBLIC_ASSET_CORS_HEADERS)) {
    setResponseHeader(event, name, value)
  }
}

type ProbeFetch = (input: string | URL | Request, init?: RequestInit) => Promise<Response>

export function isCoreAPIPath(pathname: string) {
  return pathname === '/api/v1' || pathname.startsWith('/api/v1/')
}

export function isHostReservedPath(pathname: string) {
  return isCoreAPIPath(pathname)
    || pathname === '/_sforum' || pathname.startsWith('/_sforum/')
    || pathname === '/_nuxt' || pathname.startsWith('/_nuxt/')
    || pathname === '/media/avatars' || pathname.startsWith('/media/avatars/')
    || pathname === '/media/attachments' || pathname.startsWith('/media/attachments/')
}

export function buildPluginRouteTarget(apiBaseURL: string, requestURL: URL) {
  const target = new URL(apiBaseURL)
  if ((target.protocol !== 'http:' && target.protocol !== 'https:') || target.username || target.password) {
    throw new Error('NUXT_API_INTERNAL_BASE_URL must be an HTTP(S) URL without credentials')
  }
  target.pathname = requestURL.pathname
  target.search = requestURL.search
  target.hash = ''
  return target
}

export function pluginRouteProxyHeaders(input: HeadersInit, omitCredentials = false) {
  const headers = new Headers(input)
  headers.delete(INTERNAL_ROUTE_PROBE_HEADER)
  headers.delete(INTERNAL_ROUTE_METHOD_HEADER)
  headers.delete(INTERNAL_ROUTE_RESULT_HEADER)
  if (omitCredentials) {
    headers.delete('authorization')
    headers.delete('cookie')
    headers.delete('x-csrf-token')
  }
  return headers
}

export async function probePluginRoute(target: URL, method: string, fetcher: ProbeFetch = fetch) {
  const response = await fetcher(target, {
    method: 'HEAD',
    redirect: 'manual',
    headers: {
      [INTERNAL_ROUTE_PROBE_HEADER]: INTERNAL_ROUTE_PROBE_VERSION,
      [INTERNAL_ROUTE_METHOD_HEADER]: method
    }
  })
  const result = response.headers.get(INTERNAL_ROUTE_RESULT_HEADER)
  if (response.status === 204 && result === INTERNAL_ROUTE_MATCH) {
    return INTERNAL_ROUTE_MATCH
  }
  if (response.status === 404 && result === INTERNAL_ROUTE_MISS) {
    return INTERNAL_ROUTE_MISS
  }
  throw new Error('route probe returned invalid Host evidence')
}

export function canFallbackAfterRouteProbeFailure(method: string) {
  return method === 'GET' || method === 'HEAD'
}

// TCP 对端（宿主机 Caddy / 同网络容器）。IPv4-mapped IPv6 由 resolveClientIP 归一化。
function clientPeerAddress(event: H3Event) {
  return event.node.req.socket?.remoteAddress ?? ''
}

function proxyRequestAbortSignal(event: H3Event) {
  const controller = new AbortController()
  const webSignal = event.web?.request?.signal
  if (webSignal?.aborted) {
    controller.abort(webSignal.reason)
  } else {
    webSignal?.addEventListener('abort', () => controller.abort(webSignal.reason), { once: true })
  }
  event.node.res.once('close', () => controller.abort())
  return controller.signal
}

export async function retrySafeProxyRequest<T>(
  method: string,
  request: () => Promise<T>,
  sleep: (ms: number) => Promise<void> = (ms) => new Promise(resolve => setTimeout(resolve, ms)),
  signal?: AbortSignal
) {
  const attempts = canFallbackAfterRouteProbeFailure(method) ? SAFE_PROXY_ATTEMPTS : 1
  let lastError: unknown

  for (let attempt = 1; attempt <= attempts; attempt++) {
    if (signal?.aborted) throw lastError || signal.reason || new Error('proxy request aborted')
    try {
      return await request()
    } catch (error) {
      lastError = error
      if (signal?.aborted) throw error
      if (attempt < attempts) {
        await sleep(SAFE_PROXY_RETRY_DELAY_MS)
      }
    }
  }

  throw lastError
}

export async function proxyDeclaredPluginRoute(event: H3Event) {
  const requestURL = getRequestURL(event)
  if (isHostReservedPath(requestURL.pathname)) {
    return
  }

  const apiBaseURL = process.env.NUXT_API_INTERNAL_BASE_URL || 'http://api:8080/api/v1'
  const target = buildPluginRouteTarget(apiBaseURL, requestURL)
  try {
    if (await probePluginRoute(target, event.method) === INTERNAL_ROUTE_MISS) {
      return
    }
  } catch {
    // 无法证明 miss 时，unsafe 请求不能落到另一个潜在 writer。
    if (canFallbackAfterRouteProbeFailure(event.method)) {
      return
    }
    throw createError({ statusCode: 503, statusMessage: 'Route registry unavailable' })
  }

  return proxyRouteRequest(event, target)
}

export async function proxyRouteRequest(
  event: H3Event,
  target: URL,
  options: { omitCredentials?: boolean, requestHeaders?: HeadersInit } = {}
) {
  const inboundHeaders = getProxyRequestHeaders(event)
  const headers = pluginRouteProxyHeaders(inboundHeaders, options.omitCredentials)
  for (const [name, value] of new Headers(options.requestHeaders)) {
    headers.set(name, value)
  }
  // web 是客户端 IP 的唯一权威出口：解析出唯一 IP 后覆盖上行头，API 侧链条只剩一个条目。
  // 必须放在合并 requestHeaders 之后，调用方无法再把伪造值写回来。
  applyClientIPHeaders(
    headers,
    resolveClientIP(clientPeerAddress(event), new Headers(inboundHeaders), clientIPConfig())
  )
  const hasRequestBody = event.method !== 'GET' && event.method !== 'HEAD'
  const signal = proxyRequestAbortSignal(event)
  return retrySafeProxyRequest(event.method, () => sendProxy(event, target.toString(), {
    headers,
    sendStream: true,
    onResponse: options.omitCredentials
      ? (proxyEvent, response) => {
          removeResponseHeader(proxyEvent, 'set-cookie')
          applyPublicAssetCorsHeaders(proxyEvent)
          setResponseHeader(
            proxyEvent,
            'cache-control',
            response.status === 200 ? 'public, max-age=31536000, immutable' : 'no-store'
          )
          setResponseHeader(proxyEvent, 'vary', 'Accept-Encoding')
        }
      : undefined,
    fetchOptions: {
      method: event.method,
      redirect: 'manual',
      signal,
      body: hasRequestBody ? getRequestWebStream(event) : undefined,
      duplex: hasRequestBody ? 'half' : undefined
    }
  }), undefined, signal)
}
