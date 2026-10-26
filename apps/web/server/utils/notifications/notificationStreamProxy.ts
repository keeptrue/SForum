import type { H3Event } from 'h3'
import { getProxyRequestHeaders } from 'h3'
import { request as httpRequest, type IncomingMessage } from 'node:http'
import { request as httpsRequest } from 'node:https'

import { applyClientIPHeaders, clientIPConfig, resolveClientIP } from '../clientIp'
import { pluginRouteProxyHeaders } from '../pluginRouteProxy'

const HOP_BY_HOP_RESPONSE_HEADERS = new Set([
  'connection',
  'content-length',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'te',
  'trailer',
  'transfer-encoding',
  'upgrade'
])

// Host SSE 裸管道代理：通知修订流与主题评论修订流共用（见 utils/apiStreamPath.ts）。
// 与 sendProxy 的差别是不做任何缓冲/压缩改写，并把客户端断开直接映射为上游断开。
export function proxyNotificationStream(event: H3Event, target: URL, requestOverride?: typeof httpRequest) {
  const request = requestOverride || (target.protocol === 'https:' ? httpsRequest : httpRequest)
  const inboundHeaders = getProxyRequestHeaders(event)
  const headers = pluginRouteProxyHeaders(inboundHeaders)
  // 裸管道同样要覆盖客户端 IP 头：否则这两条 SSE 通道会把客户端自带的
  // XFF / CF-Connecting-IP 直接透传给 API，成为绕过 web 出口的伪造面。
  // 与 proxyRouteRequest 一致：只写解析出的单值，不追加 Nuxt 自身地址。
  applyClientIPHeaders(
    headers,
    resolveClientIP(event.node.req.socket?.remoteAddress ?? '', new Headers(inboundHeaders), clientIPConfig())
  )

  return new Promise<void>((resolve, reject) => {
    let settled = false
    let upstreamResponse: IncomingMessage | undefined
    const settle = (error?: Error) => {
      if (settled) return
      settled = true
      if (error) reject(error)
      else resolve()
    }
    const upstream = request(target, {
      method: 'GET',
      headers: Object.fromEntries(headers.entries())
    }, (response) => {
      upstreamResponse = response
      event.node.res.statusCode = response.statusCode || 502
      if (response.statusMessage) event.node.res.statusMessage = response.statusMessage
      for (const [name, value] of Object.entries(response.headers)) {
        if (value === undefined || HOP_BY_HOP_RESPONSE_HEADERS.has(name)) continue
        event.node.res.setHeader(name, value)
      }
      response.once('error', settle)
      response.once('end', () => settle())
      response.pipe(event.node.res)
    })

    const abort = () => {
      upstreamResponse?.destroy()
      upstream.destroy()
      settle()
    }
    event.node.req.once('aborted', abort)
    event.node.res.once('close', abort)
    upstream.once('error', (error) => {
      if (event.node.res.destroyed) settle()
      else settle(error)
    })
    upstream.end()
  })
}
