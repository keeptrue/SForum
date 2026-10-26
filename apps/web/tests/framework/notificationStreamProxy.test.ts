import { describe, expect, test } from 'bun:test'
import { EventEmitter } from 'node:events'
import { request as httpRequest, type ClientRequest, type IncomingMessage } from 'node:http'
import { PassThrough } from 'node:stream'
import type { H3Event } from 'h3'

import { proxyNotificationStream } from '../../server/utils/notifications/notificationStreamProxy'

interface FakeEventOptions {
  headers: Record<string, string>
  /** undefined 模拟拿不到 TCP 对端（例如 unix socket），用于验证 fail-closed。 */
  remoteAddress?: string
}

function fakeEvent(options: FakeEventOptions) {
  const downstream = new PassThrough() as PassThrough & {
    statusCode: number
    statusMessage: string
    setHeader: (name: string, value: string | string[]) => void
  }
  downstream.statusCode = 200
  downstream.statusMessage = ''
  downstream.setHeader = () => {}
  const request = Object.assign(new EventEmitter(), {
    headers: options.headers,
    socket: options.remoteAddress === undefined ? undefined : { remoteAddress: options.remoteAddress }
  })
  return { event: { node: { req: request, res: downstream } } as unknown as H3Event, downstream }
}

// 只捕获上游请求头；SSE 的流式与响应头行为由既有测试覆盖。
async function captureUpstreamHeaders(options: FakeEventOptions) {
  const { event, downstream } = fakeEvent(options)
  let captured: Record<string, string> = {}
  const upstream = new PassThrough() as IncomingMessage
  upstream.statusCode = 200
  upstream.statusMessage = 'OK'
  upstream.headers = { 'content-type': 'text/event-stream' }
  const upstreamRequest = new EventEmitter() as ClientRequest
  upstreamRequest.end = () => upstreamRequest
  upstreamRequest.destroy = () => upstreamRequest
  const request = ((
    _target: URL,
    init: { headers?: Record<string, string> },
    callback: (response: IncomingMessage) => void
  ) => {
    captured = init.headers ?? {}
    callback(upstream)
    return upstreamRequest
  }) as unknown as typeof httpRequest

  const proxy = proxyNotificationStream(event, new URL('http://api.test/stream'), request)
  upstream.end(': ready\n\n')
  await proxy
  downstream.destroy()
  return captured
}

describe('notification stream client IP normalization', () => {
  test('覆盖伪造的单值头与多值 XFF，只留解析结果', async () => {
    const headers = await captureUpstreamHeaders({
      remoteAddress: '127.0.0.1',
      headers: {
        authorization: 'Bearer sft_real',
        'cf-connecting-ip': '203.0.113.7',
        'true-client-ip': '203.0.113.8',
        'x-real-ip': '203.0.113.9',
        'x-forwarded-for': '198.51.100.7, 10.9.9.9'
      }
    })

    // 对端不是显式代理：伪造的 CDN 头被忽略，落回 X-Real-IP，且两个 CDN 头出站即删。
    expect(headers['x-forwarded-for']).toBe('203.0.113.9')
    expect(headers['x-real-ip']).toBe('203.0.113.9')
    expect(headers['cf-connecting-ip']).toBeUndefined()
    expect(headers['true-client-ip']).toBeUndefined()
    // 其余头照旧透传，SSE 代理本身行为不变。
    expect(headers.authorization).toBe('Bearer sft_real')
  })

  test('公网对端忽略一切转发头，只写对端地址', async () => {
    const headers = await captureUpstreamHeaders({
      remoteAddress: '198.51.100.20',
      headers: {
        'cf-connecting-ip': '203.0.113.7',
        'x-real-ip': '203.0.113.9',
        'x-forwarded-for': '198.51.100.7'
      }
    })

    expect(headers['x-forwarded-for']).toBe('198.51.100.20')
    expect(headers['x-real-ip']).toBe('198.51.100.20')
    expect(headers['cf-connecting-ip']).toBeUndefined()
  })

  test('只有多值 XFF 时取最右侧非代理地址', async () => {
    const headers = await captureUpstreamHeaders({
      remoteAddress: '172.18.0.5',
      headers: { 'x-forwarded-for': '198.51.100.7, 10.9.9.9' }
    })

    expect(headers['x-forwarded-for']).toBe('10.9.9.9')
    expect(headers['x-real-ip']).toBe('10.9.9.9')
  })

  test('拿不到对端时删除全部客户端 IP 头（fail-closed）', async () => {
    const headers = await captureUpstreamHeaders({
      headers: {
        'cf-connecting-ip': '203.0.113.7',
        'true-client-ip': '203.0.113.8',
        'x-real-ip': '203.0.113.9',
        'x-forwarded-for': '198.51.100.7'
      }
    })

    expect(headers['x-forwarded-for']).toBeUndefined()
    expect(headers['x-real-ip']).toBeUndefined()
    expect(headers['cf-connecting-ip']).toBeUndefined()
    expect(headers['true-client-ip']).toBeUndefined()
  })
})
