import { describe, expect, test } from 'bun:test'
import { EventEmitter, once } from 'node:events'
import { readFileSync } from 'node:fs'
import { createServer, type ClientRequest, type IncomingMessage, type Server } from 'node:http'
import { PassThrough } from 'node:stream'
import { createApp, eventHandler, getRequestURL, toNodeListener, type H3Event } from 'h3'

import {
  buildPluginRouteTarget,
  canFallbackAfterRouteProbeFailure,
  INTERNAL_ROUTE_METHOD_HEADER,
  INTERNAL_ROUTE_MISS,
  INTERNAL_ROUTE_MATCH,
  INTERNAL_ROUTE_PROBE_HEADER,
  INTERNAL_ROUTE_PROBE_VERSION,
  INTERNAL_ROUTE_RESULT_HEADER,
  isCoreAPIPath,
  isHostReservedPath,
  pluginRouteProxyHeaders,
  probePluginRoute,
  proxyDeclaredPluginRoute,
  proxyRouteRequest,
  retrySafeProxyRequest
} from '../../server/utils/pluginRouteProxy'
import { proxyNotificationStream } from '../../server/utils/notifications/notificationStreamProxy'

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')

describe('trusted plugin arbitrary-route proxy', () => {
  test('keeps core API paths on the existing proxy and admits root public/admin paths', () => {
    expect(isCoreAPIPath('/api/v1/topics')).toBe(true)
    expect(isCoreAPIPath('/api/v10/topics')).toBe(false)
    expect(isCoreAPIPath('/plugin/docs')).toBe(false)
    expect(isCoreAPIPath('/admin/plugin/rebuild')).toBe(false)
    expect(isHostReservedPath('/_sforum/assets/themes/demo/digest/theme.css')).toBe(true)
    expect(isHostReservedPath('/_sforum/private-assets/extensions/demo/digest/entry')).toBe(true)
    expect(isHostReservedPath('/_nuxt/app.js')).toBe(true)
    expect(isHostReservedPath('/media/avatars/3cfb087097f8cb1a3fec5bc63b1d85cb')).toBe(true)
    expect(isHostReservedPath('/media/attachments/3cfb087097f8cb1a3fec5bc63b1d85cb')).toBe(true)
  })

  test('builds only same configured API-origin targets without path-based SSRF', () => {
    const target = buildPluginRouteTarget(
      'http://api:8080/api/v1',
      new URL('https://forum.example.com//evil.example/path?q=1')
    )
    expect(target.origin).toBe('http://api:8080')
    expect(target.pathname).toBe('//evil.example/path')
    expect(target.search).toBe('?q=1')
    expect(() => buildPluginRouteTarget('file:///tmp/api', new URL('https://forum.example.com/x'))).toThrow()
    expect(() => buildPluginRouteTarget('http://user:secret@api:8080', new URL('https://forum.example.com/x'))).toThrow()
  })

  test('uses a body-free HEAD probe bound to the original method', async () => {
    let requestInit: RequestInit | undefined
    const matched = await probePluginRoute(new URL('http://api:8080/admin/plugin'), 'PATCH', async (_input, init) => {
      requestInit = init
      return new Response(null, {
        status: 204,
        headers: { [INTERNAL_ROUTE_RESULT_HEADER]: 'plugin' }
      })
    })
    expect(matched).toBe(INTERNAL_ROUTE_MATCH)
    expect(requestInit?.method).toBe('HEAD')
    expect(new Headers(requestInit?.headers).get(INTERNAL_ROUTE_PROBE_HEADER)).toBe(INTERNAL_ROUTE_PROBE_VERSION)
    expect(new Headers(requestInit?.headers).get(INTERNAL_ROUTE_METHOD_HEADER)).toBe('PATCH')
  })

  test('requires Host match evidence and strips all reserved authority headers', async () => {
    expect(await probePluginRoute(new URL('http://api:8080/x'), 'GET', async () => new Response(null, {
      status: 404,
      headers: { [INTERNAL_ROUTE_RESULT_HEADER]: INTERNAL_ROUTE_MISS }
    }))).toBe(INTERNAL_ROUTE_MISS)
    await expect(probePluginRoute(new URL('http://api:8080/x'), 'GET', async () => new Response(null, { status: 404 }))).rejects.toThrow()
    await expect(probePluginRoute(new URL('http://api:8080/x'), 'GET', async () => new Response(null, { status: 204 }))).rejects.toThrow()

    expect(canFallbackAfterRouteProbeFailure('GET')).toBe(true)
    expect(canFallbackAfterRouteProbeFailure('HEAD')).toBe(true)
    expect(canFallbackAfterRouteProbeFailure('POST')).toBe(false)

    const headers = pluginRouteProxyHeaders({
      authorization: 'Bearer sft_real',
      [INTERNAL_ROUTE_PROBE_HEADER]: 'forged',
      [INTERNAL_ROUTE_METHOD_HEADER]: 'DELETE',
      [INTERNAL_ROUTE_RESULT_HEADER]: 'plugin'
    })
    expect(headers.get('authorization')).toBe('Bearer sft_real')
    expect(headers.get(INTERNAL_ROUTE_PROBE_HEADER)).toBeNull()
    expect(headers.get(INTERNAL_ROUTE_METHOD_HEADER)).toBeNull()
    expect(headers.get(INTERNAL_ROUTE_RESULT_HEADER)).toBeNull()

    const publicHeaders = pluginRouteProxyHeaders({
      authorization: 'Bearer sft_private',
      cookie: 'sforum_session=private',
      'x-csrf-token': 'private',
      'x-sforum-public-package-digest': 'exact-public-digest'
    }, true)
    expect(publicHeaders.get('authorization')).toBeNull()
    expect(publicHeaders.get('cookie')).toBeNull()
    expect(publicHeaders.get('x-csrf-token')).toBeNull()
    expect(publicHeaders.get('x-sforum-public-package-digest')).toBe('exact-public-digest')
  })

  test('retries only safe proxy reads through a short API restart window', async () => {
    let attempts = 0
    const waits: number[] = []
    const response = await retrySafeProxyRequest('GET', async () => {
      attempts++
      if (attempts < 3) {
        throw new Error('connection refused')
      }
      return 'ready'
    }, async (ms) => {
      waits.push(ms)
    })
    expect(response).toBe('ready')
    expect(attempts).toBe(3)
    expect(waits).toEqual([500, 500])

    attempts = 0
    await expect(retrySafeProxyRequest('POST', async () => {
      attempts++
      throw new Error('write failed')
    })).rejects.toThrow('write failed')
    expect(attempts).toBe(1)
  })

  test('stops retrying when the downstream request is aborted', async () => {
    const controller = new AbortController()
    let attempts = 0
    let waits = 0
    const failure = new Error('downstream closed')

    await expect(retrySafeProxyRequest('GET', async () => {
      attempts++
      controller.abort()
      throw failure
    }, async () => {
      waits++
    }, controller.signal)).rejects.toBe(failure)

    expect(attempts).toBe(1)
    expect(waits).toBe(0)
  })

  test('middleware probes before proxying while ordinary API proxy remains streaming', () => {
    const middleware = source('../../server/middleware/plugin-route-proxy.ts')
    const apiProxy = source('../../server/routes/api/v1/[...path].ts')
    const avatarProxy = source('../../server/routes/media/avatars/[publicId].ts')
    const attachmentProxy = source('../../server/routes/media/attachments/[publicId].ts')
    const attachmentOriginalProxy = source('../../server/routes/media/attachments/[publicId]/original.ts')
    const proxyUtility = source('../../server/utils/pluginRouteProxy.ts')
    expect(middleware).toContain('proxyDeclaredPluginRoute(event)')
    expect(apiProxy).toContain('proxyRouteRequest(event, target)')
    expect(apiProxy).toContain('proxyNotificationStream(event, target)')
    expect(avatarProxy).toContain('/attachments/${encodeURIComponent(publicId)}/content')
    expect(avatarProxy).toContain('proxyRouteRequest(event, target, { omitCredentials: true })')
    expect(attachmentProxy).toContain('/attachments/${encodeURIComponent(publicId)}/variants/display/content')
    expect(attachmentProxy).toContain('proxyRouteRequest(event, target, {')
    expect(attachmentProxy).not.toContain('omitCredentials')
    expect(attachmentProxy).toContain("'x-sforum-media-delivery': 'redirect-v1'")
    expect(attachmentOriginalProxy).toContain('/attachments/${encodeURIComponent(publicId)}/content')
    expect(attachmentOriginalProxy).toContain("'x-sforum-media-delivery': 'redirect-v1'")
    expect(proxyUtility).toContain('getRequestWebStream(event)')
    expect(proxyUtility).toContain('new Headers(options.requestHeaders)')
    expect(proxyUtility).toContain('sendStream: true')
    expect(proxyUtility).toContain('const signal = proxyRequestAbortSignal(event)')
    expect(proxyUtility).toContain("duplex: hasRequestBody ? 'half' : undefined")

    const caddy = source('../../../../deploy/caddy/Caddyfile')
    const websocketProxy = 'reverse_proxy @host_api_websocket 127.0.0.1:{$API_PORT:18080}'
    const webProxy = 'reverse_proxy 127.0.0.1:{$WEB_PORT:3000}'
    expect(caddy).toContain('header_regexp connection_upgrade Connection (?i)(^|.*,\\s*)upgrade(\\s*,.*|$)')
    expect(caddy).toContain('header_regexp websocket_upgrade Upgrade (?i)^websocket$')
    expect(caddy).toContain('not header Sec-WebSocket-Protocol *vite-hmr*')
    expect(caddy).toContain(websocketProxy)
    expect(caddy).toContain(webProxy)
    expect(caddy.indexOf(websocketProxy)).toBeLessThan(caddy.indexOf(webProxy))
    expect(caddy).not.toMatch(/header_up\s+(?:Host|Origin|Cookie|Authorization|Sec-WebSocket-\S+)/i)

    const productionCompose = source('../../../../compose.prod.yaml')
    expect(productionCompose).toContain('- "127.0.0.1:${API_PORT:-18080}:8080"')
    expect(source('../../../../.env.production.example')).toContain('API_PORT=18080')
  })

  test('aborts the upstream stream after the downstream client disconnects', async () => {
    const downstream = new PassThrough() as PassThrough & {
      statusCode: number
      statusMessage: string
      setHeader: (name: string, value: string | string[]) => void
    }
    downstream.statusCode = 200
    downstream.statusMessage = ''
    downstream.setHeader = () => {}
    const downstreamRequest = new EventEmitter()
    const event = {
      node: { req: downstreamRequest, res: downstream }
    } as unknown as H3Event
    let requestDestroyed = false
    let responseDestroyed = false
    const upstreamRequest = new EventEmitter() as ClientRequest
    upstreamRequest.end = () => upstreamRequest
    upstreamRequest.destroy = () => {
      requestDestroyed = true
      return upstreamRequest
    }
    const upstreamResponse = new PassThrough() as IncomingMessage
    upstreamResponse.statusCode = 200
    upstreamResponse.statusMessage = 'OK'
    upstreamResponse.headers = { 'content-type': 'text/event-stream' }
    const destroyResponse = upstreamResponse.destroy.bind(upstreamResponse)
    upstreamResponse.destroy = () => {
      responseDestroyed = true
      return destroyResponse()
    }
    const request = ((_target: URL, _options: unknown, callback: (response: IncomingMessage) => void) => {
      callback(upstreamResponse)
      return upstreamRequest
    }) as typeof import('node:http').request

    const proxy = proxyNotificationStream(event, new URL('http://api.test/stream'), request)
    upstreamResponse.write(': ready\n\n')
    await once(downstream, 'data')
    downstream.emit('close')
    await proxy
    expect(requestDestroyed).toBe(true)
    expect(responseDestroyed).toBe(true)
  })

  test('proxies matched unsafe bodies and leaves explicit misses to Nuxt', async () => {
    const actualRequests: Array<{ method?: string, search: string, body: string, authorization?: string, probe?: string }> = []
    let publicAssetCredentials: { authorization?: string, cookie?: string } = {}
    let directDeliveryHeader: string | undefined
    let retryReadRequests = 0
    const api = createServer(async (request, response) => {
      const url = new URL(request.url || '/', 'http://api.test')
      if (url.pathname === '/public-asset' || url.pathname === '/public-asset-missing') {
        publicAssetCredentials = {
          authorization: request.headers.authorization,
          cookie: request.headers.cookie
        }
        response.statusCode = url.pathname.endsWith('-missing') ? 404 : 200
        response.setHeader('Cache-Control', 'public, max-age=300')
        response.setHeader('ETag', '"asset"')
        response.setHeader('Set-Cookie', 'csrf_=private')
        response.setHeader('Vary', 'Cookie, Accept-Encoding')
        // 上游 API 对公开资产发的是 same-origin；代理必须覆盖它，否则换源链路拒载。
        response.setHeader('Cross-Origin-Resource-Policy', 'same-origin')
        response.end('immutable-body')
        return
      }
      if (url.pathname === '/direct-delivery') {
        directDeliveryHeader = request.headers['x-sforum-media-delivery'] as string | undefined
        response.statusCode = 302
        response.setHeader('Location', 'https://objects.example.test/signed?token=abc')
        response.setHeader('Cache-Control', 'private, no-store')
        response.end()
        return
      }
      if (request.method === 'HEAD' && request.headers[INTERNAL_ROUTE_PROBE_HEADER] === INTERNAL_ROUTE_PROBE_VERSION) {
        if (url.pathname === '/probe-invalid') {
          response.statusCode = 500
          response.end()
          return
        }
        const matched = ['/plugin/echo', '/plugin/retry-read', '/plugin/redirect301', '/plugin/redirect308'].includes(url.pathname)
        response.statusCode = matched ? 204 : 404
        response.setHeader(INTERNAL_ROUTE_RESULT_HEADER, matched ? INTERNAL_ROUTE_MATCH : INTERNAL_ROUTE_MISS)
        response.end()
        return
      }

      if (url.pathname === '/plugin/redirect301' || url.pathname === '/plugin/redirect308') {
        response.statusCode = url.pathname.endsWith('301') ? 301 : 308
        response.setHeader('Location', '/plugin/canonical-target')
        response.setHeader('Link', '</plugin/canonical-target>; rel="canonical"')
        response.end()
        return
      }

      if (url.pathname === '/plugin/retry-read') {
        retryReadRequests++
        if (retryReadRequests < 3) {
          request.socket.destroy()
          return
        }
        response.statusCode = 200
        response.end('recovered')
        return
      }

      const chunks: Buffer[] = []
      for await (const chunk of request) {
        chunks.push(Buffer.from(chunk))
      }
      const body = Buffer.concat(chunks).toString()
      actualRequests.push({
        method: request.method,
        search: url.search,
        body,
        authorization: request.headers.authorization,
        probe: request.headers[INTERNAL_ROUTE_PROBE_HEADER] as string | undefined
      })
      response.statusCode = 200
      response.setHeader('Content-Type', 'text/plain; charset=utf-8')
      response.setHeader('X-Content-Type-Options', 'nosniff')
      response.end('plugin-response')
    })
    const apiURL = await listen(api)

    const app = createApp()
    app.use('/asset-proxy-missing', eventHandler(event => proxyRouteRequest(
      event, new URL(`${apiURL}/public-asset-missing`), { omitCredentials: true }
    )))
    app.use('/asset-proxy', eventHandler(event => proxyRouteRequest(
      event, new URL(`${apiURL}/public-asset`), { omitCredentials: true }
    )))
    app.use('/direct-delivery', eventHandler(event => proxyRouteRequest(
      event,
      new URL(`${apiURL}/direct-delivery`),
      { requestHeaders: { 'x-sforum-media-delivery': 'redirect-v1' } }
    )))
    app.use(eventHandler(event => proxyDeclaredPluginRoute(event)))
    app.use(eventHandler(event => `nuxt:${event.method}:${getRequestURL(event).pathname}`))
    const web = createServer(toNodeListener(app))
    const webURL = await listen(web)
    const oldBaseURL = process.env.NUXT_API_INTERNAL_BASE_URL
    process.env.NUXT_API_INTERNAL_BASE_URL = `${apiURL}/api/v1`

    try {
      const publicAsset = await fetch(`${webURL}/asset-proxy`, {
        headers: {
          authorization: 'Bearer sft_private',
          cookie: 'sforum_session=private',
          'x-csrf-token': 'private'
        }
      })
      expect(publicAsset.status).toBe(200)
      expect(await publicAsset.text()).toBe('immutable-body')
      expect(publicAssetCredentials).toEqual({ authorization: undefined, cookie: undefined })
      expect(publicAsset.headers.get('cache-control')).toBe('public, max-age=31536000, immutable')
      expect(publicAsset.headers.get('set-cookie')).toBeNull()
      expect(publicAsset.headers.get('vary')).toBe('Accept-Encoding')
      expect(publicAsset.headers.get('etag')).toBe('"asset"')
      // 换源链路（QQ X5 云加速、CDN）必须能读：上游 same-origin 被覆盖成 cross-origin。
      expect(publicAsset.headers.get('access-control-allow-origin')).toBe('*')
      expect(publicAsset.headers.get('cross-origin-resource-policy')).toBe('cross-origin')
      expect(publicAsset.headers.get('timing-allow-origin')).toBe('*')

      const missingAsset = await fetch(`${webURL}/asset-proxy-missing`)
      expect(missingAsset.status).toBe(404)
      expect(missingAsset.headers.get('cache-control')).toBe('no-store')
      expect(missingAsset.headers.get('set-cookie')).toBeNull()
      expect(missingAsset.headers.get('access-control-allow-origin')).toBe('*')

      const directDelivery = await fetch(`${webURL}/direct-delivery`, { redirect: 'manual' })
      expect(directDelivery.status).toBe(302)
      expect(directDelivery.headers.get('location')).toBe('https://objects.example.test/signed?token=abc')
      expect(directDelivery.headers.get('cache-control')).toBe('private, no-store')
      expect(directDeliveryHeader).toBe('redirect-v1')

      const pluginSearch = '?q=%3Cscript%3EglobalThis.compromised%3Dtrue%3C%2Fscript%3E'
      const pluginBody = '<img src=x onerror="globalThis.compromised=true">'
      const pluginResponse = await fetch(`${webURL}/plugin/echo${pluginSearch}`, {
        method: 'POST',
        headers: {
          authorization: 'Bearer sft_real',
          'content-type': 'text/plain',
          [INTERNAL_ROUTE_PROBE_HEADER]: INTERNAL_ROUTE_PROBE_VERSION,
          [INTERNAL_ROUTE_METHOD_HEADER]: 'DELETE'
        },
        body: pluginBody
      })
      expect(pluginResponse.status).toBe(200)
      expect(pluginResponse.headers.get('content-type')).toBe('text/plain; charset=utf-8')
      expect(pluginResponse.headers.get('x-content-type-options')).toBe('nosniff')
      expect(await pluginResponse.text()).toBe('plugin-response')
      expect(actualRequests).toEqual([{
        method: 'POST', search: pluginSearch, body: pluginBody, authorization: 'Bearer sft_real', probe: undefined
      }])

      const retryReadResponse = await fetch(`${webURL}/plugin/retry-read`)
      expect(retryReadResponse.status).toBe(200)
      expect(await retryReadResponse.text()).toBe('recovered')
      expect(retryReadRequests).toBe(3)

      for (const status of [301, 308]) {
        const redirectResponse = await fetch(`${webURL}/plugin/redirect${status}`, { redirect: 'manual' })
        expect(redirectResponse.status).toBe(status)
        expect(redirectResponse.headers.get('location')).toBe('/plugin/canonical-target')
        expect(redirectResponse.headers.get('link')).toBe('</plugin/canonical-target>; rel="canonical"')
      }
      expect(actualRequests).toHaveLength(1)

      const hostResponse = await fetch(`${webURL}/host-page`)
      expect(hostResponse.status).toBe(200)
      expect(await hostResponse.text()).toBe('nuxt:GET:/host-page')

      const unsafeUnavailable = await fetch(`${webURL}/probe-invalid`, { method: 'POST', body: 'must-not-fallback' })
      expect(unsafeUnavailable.status).toBe(503)
      expect(actualRequests).toHaveLength(1)

      const safeUnavailable = await fetch(`${webURL}/probe-invalid`)
      expect(safeUnavailable.status).toBe(200)
      expect(await safeUnavailable.text()).toBe('nuxt:GET:/probe-invalid')
    } finally {
      if (oldBaseURL === undefined) {
        delete process.env.NUXT_API_INTERNAL_BASE_URL
      } else {
        process.env.NUXT_API_INTERNAL_BASE_URL = oldBaseURL
      }
      await close(web)
      await close(api)
    }
  })

  test('overwrites forged client IP headers with one resolved value', async () => {
    const seen: Array<Record<string, string | string[] | undefined>> = []
    const api = createServer((request, response) => {
      seen.push({ ...request.headers })
      response.statusCode = 200
      response.end('ok')
    })
    const apiURL = await listen(api)
    const app = createApp()
    app.use(eventHandler(event => proxyRouteRequest(event, new URL(`${apiURL}/plugin/ip`))))
    const web = createServer(toNodeListener(app))
    const webURL = await listen(web)
    const oldTrustedProxies = process.env.TRUSTED_PROXIES
    delete process.env.TRUSTED_PROXIES

    try {
      const forged = await fetch(`${webURL}/plugin/ip`, {
        headers: {
          'cf-connecting-ip': '203.0.113.7',
          'true-client-ip': '203.0.113.8',
          'x-real-ip': '203.0.113.9',
          'x-forwarded-for': '198.51.100.7, 10.9.9.9'
        }
      })
      expect(forged.status).toBe(200)
      expect(seen).toHaveLength(1)
      // 对端是 loopback（可信但不是显式代理）：伪造的 CDN 头必须被忽略，落回 X-Real-IP，
      // 且两个 CDN 头在出站时删除——否则公网客户端可以凭空指定自己的审计 IP。
      expect(seen[0]?.['x-forwarded-for']).toBe('203.0.113.9')
      expect(seen[0]?.['x-real-ip']).toBe('203.0.113.9')
      expect(seen[0]?.['cf-connecting-ip']).toBeUndefined()
      expect(seen[0]?.['true-client-ip']).toBeUndefined()

      // 只有 XFF 时从右往左取最右侧非代理地址；私网客户端不被当代理剥掉。
      const chained = await fetch(`${webURL}/plugin/ip`, {
        headers: { 'x-forwarded-for': '198.51.100.7, 10.9.9.9' }
      })
      expect(chained.status).toBe(200)
      expect(seen[1]?.['x-forwarded-for']).toBe('10.9.9.9')
      expect(seen[1]?.['x-real-ip']).toBe('10.9.9.9')

      // TRUSTED_PROXIES 生效后，同一链条里的代理跳被剥掉。
      process.env.TRUSTED_PROXIES = '10.9.9.0/24'
      const stripped = await fetch(`${webURL}/plugin/ip`, {
        headers: { 'x-forwarded-for': '198.51.100.7, 10.9.9.9' }
      })
      expect(stripped.status).toBe(200)
      expect(seen[2]?.['x-forwarded-for']).toBe('198.51.100.7')
      expect(seen[2]?.['x-real-ip']).toBe('198.51.100.7')

      // 对端本身列进 TRUSTED_PROXIES（这里用测试服务器的 127.0.0.1）后，CDN 头才被采信。
      process.env.TRUSTED_PROXIES = '127.0.0.1/32'
      const cdn = await fetch(`${webURL}/plugin/ip`, {
        headers: {
          'cf-connecting-ip': '203.0.113.7',
          'true-client-ip': '203.0.113.8',
          'x-real-ip': '203.0.113.9'
        }
      })
      expect(cdn.status).toBe(200)
      expect(seen[3]?.['x-forwarded-for']).toBe('203.0.113.7')
      expect(seen[3]?.['x-real-ip']).toBe('203.0.113.7')
      expect(seen[3]?.['cf-connecting-ip']).toBeUndefined()
      expect(seen[3]?.['true-client-ip']).toBeUndefined()
    } finally {
      if (oldTrustedProxies === undefined) {
        delete process.env.TRUSTED_PROXIES
      } else {
        process.env.TRUSTED_PROXIES = oldTrustedProxies
      }
      await close(web)
      await close(api)
    }
  })
})

async function listen(server: Server) {
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('test server did not expose a TCP address')
  }
  return `http://127.0.0.1:${address.port}`
}

async function close(server: Server) {
  server.closeAllConnections()
  if (server.listening) {
    server.close()
  }
}
