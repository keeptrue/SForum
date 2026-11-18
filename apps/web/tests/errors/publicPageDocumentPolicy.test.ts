import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import {
  PUBLIC_FRONTEND_DOCUMENT_POLICY_SCHEMA_VERSION,
  PUBLIC_FRONTEND_POLICY_SCHEMA_VERSION,
  PUBLIC_PAGE_CSP_HEADER,
  PUBLIC_PAGE_POLICY_PATH,
  collectPublicL2ComponentRefsFromRenderNodes,
  normalizePublicFrontendComponentRefs,
  parsePublicFrontendPolicy,
  publicPagePolicyPath,
  PublicPagePolicyError
} from '../../app/runtime/public-extensions/pagePolicy'
import {
  DOCUMENT_CSP_NONCE_CONTEXT_KEY,
  apiConnectSources,
  applyDocumentCspNonceToHtmlChunk,
  buildDocumentCspHeaderValue,
  createDocumentCspBaselineCache,
  createDocumentCspNonce,
  hasDocumentCspDirective,
  selectDocumentCspPolicy,
  shouldWriteDocumentCsp
} from '../../app/composables/errors/documentCspNonce'

function source(relative: string) {
  return readFileSync(new URL(relative, import.meta.url), 'utf8')
}

/** 按 CSP 序列化格式拆出某条指令的源列表，用于断言 header 内容。 */
function directiveSources(header: string, name: string): string[] {
  for (const part of header.split(';')) {
    const tokens = part.trim().split(/\s+/).filter(Boolean)
    if (tokens[0] === name) {
      return tokens.slice(1)
    }
  }
  return []
}

/** Host 基线（apps/api 的 publicPageHostBaselinePolicy）；style-src 保留 unsafe-inline。 */
const HOST_BASELINE_DIRECTIVES = [
  { name: 'base-uri', sources: ["'none'"] },
  { name: 'connect-src', sources: ["'self'"] },
  { name: 'default-src', sources: ["'none'"] },
  { name: 'font-src', sources: ["'self'"] },
  { name: 'form-action', sources: ["'self'"] },
  { name: 'frame-ancestors', sources: ["'none'"] },
  { name: 'img-src', sources: ["'self'", 'blob:', 'data:', 'https:'] },
  { name: 'media-src', sources: ["'self'"] },
  { name: 'object-src', sources: ["'none'"] },
  { name: 'script-src', sources: ["'self'"] },
  { name: 'style-src', sources: ["'self'", "'unsafe-inline'"] },
  { name: 'worker-src', sources: ["'self'"] }
]

describe('public page document CSP policy', () => {
  it('builds deterministic soft-ref page-policy paths', () => {
    expect(publicPagePolicyPath([])).toBe(PUBLIC_PAGE_POLICY_PATH)
    expect(publicPagePolicyPath([
      { extensionId: 'b.plugin', componentId: 'b.card' },
      { extensionId: 'a.plugin', componentId: 'a.card' },
      { extensionId: 'a.plugin', componentId: 'a.card' }
    ])).toBe(
      `${PUBLIC_PAGE_POLICY_PATH}?component=${encodeURIComponent('a.plugin/a.card')}&component=${encodeURIComponent('b.plugin/b.card')}`
    )
    expect(() => normalizePublicFrontendComponentRefs([
      { extensionId: 'Bad', componentId: 'x' }
    ])).toThrow(PublicPagePolicyError)
  })

  it('parses Host document policy and rejects unsafe header values', () => {
    const policy = parsePublicFrontendPolicy({
      schemaVersion: PUBLIC_FRONTEND_POLICY_SCHEMA_VERSION,
      graphDigest: 'a'.repeat(64),
      extensionPolicyDigest: 'b'.repeat(64),
      directives: [{ name: 'img-src', sources: ["'self'", 'https://cdn.example'] }],
      admittedComponents: [{
        extensionId: 'demo.plugin',
        extensionVersion: '1.0.0',
        packageDigest: 'c'.repeat(64),
        impactDigest: 'd'.repeat(64),
        componentId: 'demo.plugin.component.card',
        contractVersion: 'demo.plugin.component.card@1'
      }],
      documentPolicy: {
        schemaVersion: PUBLIC_FRONTEND_DOCUMENT_POLICY_SCHEMA_VERSION,
        digest: 'e'.repeat(64),
        directives: [
          { name: 'default-src', sources: ["'none'"] },
          { name: 'script-src', sources: ["'self'"] }
        ],
        headerValue: "default-src 'none'; script-src 'self'"
      }
    })
    expect(policy.documentPolicy.headerValue).toContain("script-src 'self'")
    expect(() => parsePublicFrontendPolicy({
      ...policy,
      documentPolicy: {
        ...policy.documentPolicy,
        headerValue: "default-src 'none'\nscript-src 'self'"
      }
    })).toThrow(PublicPagePolicyError)
  })

  it('collects L2 soft refs from theme render islands', () => {
    const refs = collectPublicL2ComponentRefsFromRenderNodes([
      {
        kind: 'island',
        descriptor: { componentId: 'core.component.shared.sfextension_widget' },
        props: {
          'extension-id': 'sforum.public-l2-e2e-theme',
          'component-id': 'sforum.public-l2-e2e-theme.component.card'
        },
        children: []
      },
      {
        kind: 'island',
        descriptor: { componentId: 'forum.component.home_page' },
        props: {},
        children: []
      }
    ])
    expect(refs).toEqual([{
      extensionId: 'sforum.public-l2-e2e-theme',
      componentId: 'sforum.public-l2-e2e-theme.component.card'
    }])
  })

  it('wires Nuxt SSR to apply Host DocumentPolicy.HeaderValue', () => {
    const composable = source('../../app/composables/errors/usePublicPageDocumentPolicy.ts')
    const template = source('../../app/components/SFThemeTemplate.vue')
    // 实现刻意直接写 nitro 响应对象：Nuxt 4 没有导出 setResponseHeader 组合式，
    // 而把 h3 的 setHeader 引进一个纯 runtime 组合式只为了同样的 res.setHeader。
    // 断言「CSP 头确实落在 SSR 响应上」而不是钉某一个函数名。
    expect(composable).toMatch(/(event\.node\.res\.setHeader|setHeader\()\(?\s*(event,\s*)?PUBLIC_PAGE_CSP_HEADER/)
    expect(composable).toContain(PUBLIC_PAGE_CSP_HEADER)
    expect(composable).toContain('publicPagePolicyPath')
    expect(composable).toContain('X-SForum-Document-Policy-Digest')
    expect(composable).toContain('buildDocumentCspHeaderValue')
    expect(composable).toContain('createDocumentCspNonce')
    expect(composable).toContain('DOCUMENT_CSP_NONCE_CONTEXT_KEY')
    expect(template).toContain('applyPublicPageDocumentPolicy')
    expect(template).toContain('collectPublicL2ComponentRefsFromRenderNodes')
    expect(template).toContain('data-document-policy-digest')
  })

  it('writes the same document CSP in dev as in production', () => {
    const composable = source('../../app/composables/errors/usePublicPageDocumentPolicy.ts')
    // dev 短路（`if (import.meta.dev) return policy`）会让「内联脚本被 CSP 拦」这类
    // 缺陷只在生产暴露，必须没有它；dev 差异只允许通过 header 组装的显式开关表达。
    expect(composable).not.toMatch(/if\s*\(\s*import\.meta\.dev\s*\)[\s\S]{0,160}?return policy/)
    expect(composable).toContain('dev: import.meta.dev')
  })

  it('appends exactly one script-src nonce and never touches style-src', () => {
    const nonce = createDocumentCspNonce()
    const header = buildDocumentCspHeaderValue(HOST_BASELINE_DIRECTIVES, { nonce })

    const scriptSources = directiveSources(header, 'script-src')
    expect(scriptSources).toContain("'self'")
    expect(scriptSources.filter(source => /^'nonce-/.test(source))).toEqual([`'nonce-${nonce}'`])
    expect(scriptSources).not.toContain("'unsafe-inline'")

    // CSP3：style-src 里出现 nonce/hash 会让 'unsafe-inline' 立即失效，样式会全挂。
    const styleSources = directiveSources(header, 'style-src')
    expect(styleSources).toEqual(["'self'", "'unsafe-inline'"])
    expect(styleSources.some(source => source.includes('nonce-'))).toBe(false)
    expect(styleSources.some(source => /^'sha(256|384|512)-/.test(source))).toBe(false)
    expect(header).not.toMatch(/style-src[^;]*(nonce-|'sha)/)

    // 其余指令逐字保留，顺序不变。
    expect(header.startsWith("base-uri 'none'; connect-src 'self'; default-src 'none'")).toBe(true)
    // 后端基线已放开 https:（全站下发后外链图会被拦），逐字对齐。
    expect(header).toContain("img-src 'self' blob: data: https:")
    expect(directiveSources(header, 'default-src')).toEqual(["'none'"])
    expect(directiveSources(header, 'worker-src')).toEqual(["'self'"])
  })

  it('adds the dev toolchain sources to connect-src only when dev is on', () => {
    const nonce = createDocumentCspNonce()
    const devHeader = buildDocumentCspHeaderValue(HOST_BASELINE_DIRECTIVES, { nonce, dev: true })
    const devSources = directiveSources(devHeader, 'connect-src')
    // Vite HMR 的 socket 主机名构建期写死成 localhost:5173，'self' 拦不住。
    expect(devSources).toEqual(expect.arrayContaining([
      'ws:',
      'wss:',
      'http://localhost:*',
      'http://127.0.0.1:*',
      'https://api.iconify.design',
      'https://api.simplesvg.com',
      'https://api.unisvg.com'
    ]))
    expect(devSources[0]).toBe("'self'")

    // 生产必须逐字等于 Host 下发的策略（只多一个 nonce），dev 源一个都不许漏出去。
    const prodHeader = buildDocumentCspHeaderValue(HOST_BASELINE_DIRECTIVES, { nonce, dev: false })
    expect(directiveSources(prodHeader, 'connect-src')).toEqual(["'self'"])
    for (const source of ['ws:', 'wss:', 'http://localhost:*', 'http://127.0.0.1:*', 'api.iconify.design']) {
      expect(prodHeader).not.toContain(source)
    }
    // 没有 dev 选项时也必须按生产组装。
    expect(buildDocumentCspHeaderValue(HOST_BASELINE_DIRECTIVES, { nonce }))
      .toBe(prodHeader)
  })

  it('keeps a zero-source directive in the header instead of dropping it', () => {
    const header = buildDocumentCspHeaderValue([
      { name: 'default-src', sources: ["'none'"] },
      { name: 'frame-ancestors', sources: [] },
      { name: 'form-action', sources: ["'self'"] }
    ])
    // 裸指令名与 `'none'` 在浏览器里等效（Chrome 154 实测，且都能压过宽松的
    // default-src），但契约要显式 `'none'`：指令绝不静默消失，也不留歧义写法。
    expect(header).toBe("default-src 'none'; frame-ancestors 'none'; form-action 'self'")
  })

  it('adds the API origin to connect-src only for a cross-origin absolute apiBaseUrl', () => {
    // 默认 `/api/v1` 是相对路径：同源，`'self'` 已经覆盖，什么都不加。
    expect(apiConnectSources('/api/v1', 'https://forum.example')).toEqual([])
    // 绝对地址但同源：同样不加。
    expect(apiConnectSources('https://api.example.com/api/v1', 'https://api.example.com')).toEqual([])
    // 跨源绝对地址：补 origin（含非默认端口），否则全站请求会被 connect-src 拦掉。
    expect(apiConnectSources('https://api.example.com/api/v1', 'https://forum.example'))
      .toEqual(['https://api.example.com'])
    expect(apiConnectSources('https://api.example.com:8443/api/v1', 'https://forum.example'))
      .toEqual(['https://api.example.com:8443'])
    expect(apiConnectSources('', 'https://forum.example')).toEqual([])
    expect(apiConnectSources('not a url', 'https://forum.example')).toEqual([])

    const header = buildDocumentCspHeaderValue(HOST_BASELINE_DIRECTIVES, {
      nonce: 'n1',
      extraConnectSrc: ['https://api.example.com']
    })
    expect(directiveSources(header, 'connect-src')).toEqual(["'self'", 'https://api.example.com'])
    // 跨源 API 不许把 dev 源带进生产，也不许碰 style-src。
    expect(header).not.toContain('ws:')
    expect(header).not.toContain('api.iconify.design')
    expect(directiveSources(header, 'style-src')).toEqual(["'self'", "'unsafe-inline'"])
  })

  it('falls back to the cached host baseline when the per-page policy is unavailable', () => {
    type TestPolicy = {
      documentPolicy: { digest: string, directives: { name: string, sources: string[] }[] }
    }
    const baseline: TestPolicy = {
      documentPolicy: { digest: 'b'.repeat(64), directives: [{ name: 'script-src', sources: ["'self'"] }] }
    }
    const perPage: TestPolicy = {
      documentPolicy: { digest: 'p'.repeat(64), directives: [{ name: 'script-src', sources: ["'self'"] }] }
    }
    // 逐页失败（信任撤销/制品不可用/Safe Mode）→ 用基线，绝不丢头。
    expect(selectDocumentCspPolicy(null, baseline)).toEqual({ policy: baseline, source: 'baseline' })
    // 逐页成功 → 逐页优先（styles 预注入与逐页 digest 都来自它）。
    expect(selectDocumentCspPolicy(perPage, baseline)).toEqual({ policy: perPage, source: 'page' })
    // 基线也拿不到（API 不可用）才是唯一允许的无头路径。
    expect(selectDocumentCspPolicy(null, null)).toBeNull()
  })

  it('writes the document policy on pages without any L2 ref', () => {
    const composable = source('../../app/composables/errors/usePublicPageDocumentPolicy.ts')
    const template = source('../../app/components/SFThemeTemplate.vue')
    const resolver = source('../../app/components/SFPageOutletResolver.vue')

    // 两个组件都不再以 ref 长度为前提写头。
    expect(template).not.toMatch(/publicL2Refs\.value\.length > 0[\s\S]{0,80}?applyPublicPageDocumentPolicy/)
    expect(resolver).not.toMatch(/regionWidgetRefs\.value\.length > 0/)
    // 两条路径仍然互斥：resolver 只在不会走主题模板时写头。
    expect(resolver).toContain('if (import.meta.server && !willUseThemeTemplate.value)')
    expect(template.match(/applyPublicPageDocumentPolicy\(/g)).toHaveLength(1)
    expect(resolver.match(/applyPublicPageDocumentPolicy\(/g)).toHaveLength(1)

    // 零 ref 只拉一次基线并缓存；有 ref 才拉逐页策略。
    expect(composable).toContain('loadHostBaselinePolicy')
    expect(composable).toContain('createDocumentCspBaselineCache')
    expect(composable).toContain('hostBaselineCache.load')
    expect(composable).toContain('requestPublicPagePolicy(request, [])')
    expect(composable).toContain('refs.length ? await requestPublicPagePolicy(request, refs) : null')
    expect(composable).toContain('selectDocumentCspPolicy(pagePolicy, baselinePolicy)')
    expect(composable).toContain('apiConnectSources(')
    // 双写头规则必须走共享判定，不能在组合式里另写一套。
    expect(composable).toContain('shouldWriteDocumentCsp(applied?.source ?? null, selection.source)')
    expect(composable).toContain('DOCUMENT_CSP_APPLIED_CONTEXT_KEY')
  })

  it('never calls a composable after the first await', () => {
    const composable = source('../../app/composables/errors/usePublicPageDocumentPolicy.ts')
    const body = composable
      .slice(composable.indexOf('export async function applyPublicPageDocumentPolicy'))
      // 注释里也会出现 "await" 这个词，先剥掉注释再定位真正的 await。
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .replace(/\/\/[^\n]*/g, '')
    const firstAwait = body.indexOf('await ')
    expect(firstAwait).toBeGreaterThan(-1)
    // 正向锚点：await 之前确实捕获过上下文，避免「一个都没调」让下面的断言空过。
    expect(body.slice(0, firstAwait)).toMatch(/\buseRequestEvent\s*\(/)
    // 任何 composable（useXxx(）出现在 await 之后都会丢 Nuxt 实例（NUXT_E1001 → 整页 500）。
    // 用通用正则而不是列举名字：以后加 useHead()/useI18n()/useRoute() 一样会被抓住。
    expect(body.slice(firstAwait)).not.toMatch(/\buse[A-Z]\w*\s*\(/)
  })

  it('caches the host baseline: repeated loads hit the API once', async () => {
    const cache = createDocumentCspBaselineCache<{ id: string }>()
    let calls = 0
    const fetchPolicy = async () => {
      calls += 1
      return { id: 'baseline' }
    }
    expect(await cache.load(fetchPolicy)).toEqual({ id: 'baseline' })
    expect(await cache.load(fetchPolicy)).toEqual({ id: 'baseline' })
    expect(calls).toBe(1)
  })

  it('does not cache a failed baseline load', async () => {
    const cache = createDocumentCspBaselineCache<{ id: string }>()
    let calls = 0
    const failing = async () => {
      calls += 1
      return null
    }
    expect(await cache.load(failing)).toBeNull()
    // 失败不缓存：下个请求必须重试，不能「永久缓存 null → 全站长期无头」。
    expect(await cache.load(failing)).toBeNull()
    expect(calls).toBe(2)
    const succeeding = async () => {
      calls += 1
      return { id: 'baseline' }
    }
    expect(await cache.load(succeeding)).toEqual({ id: 'baseline' })
    expect(calls).toBe(3)
  })

  it('shares one in-flight baseline request across concurrent loads', async () => {
    const cache = createDocumentCspBaselineCache<{ id: string }>()
    let firstCalls = 0
    let secondCalls = 0
    let release: (policy: { id: string } | null) => void = () => {}
    const pending = new Promise<{ id: string } | null>((resolve) => {
      release = resolve
    })
    const first = cache.load(() => {
      firstCalls += 1
      return pending
    })
    const second = cache.load(() => {
      secondCalls += 1
      return Promise.resolve({ id: 'second' })
    })
    release({ id: 'baseline' })
    expect(await Promise.all([first, second])).toEqual([{ id: 'baseline' }, { id: 'baseline' }])
    expect(firstCalls).toBe(1)
    expect(secondCalls).toBe(0)
  })

  it('applies the specific-over-baseline rule when a response writes twice', () => {
    // 外壳先写逐页策略，正文岛里再渲染主题模板时只有基线 → 不得覆盖。
    expect(shouldWriteDocumentCsp('page', 'baseline')).toBe(false)
    // 反向顺序：逐页策略覆盖已经写下的基线。
    expect(shouldWriteDocumentCsp('baseline', 'page')).toBe(true)
    // 首次写头：任何来源都写。
    expect(shouldWriteDocumentCsp(null, 'page')).toBe(true)
    expect(shouldWriteDocumentCsp(null, 'baseline')).toBe(true)
    // 基线重写没有意义。
    expect(shouldWriteDocumentCsp('baseline', 'baseline')).toBe(false)
  })

  it('replaces a Host-supplied script-src nonce instead of stacking one', () => {
    const header = buildDocumentCspHeaderValue(
      [{ name: 'script-src', sources: ["'self'", "'nonce-stale'"] }],
      { nonce: 'fresh' }
    )
    expect(directiveSources(header, 'script-src')).toEqual(["'self'", "'nonce-fresh'"])
  })

  it('issues a fresh base64url nonce per request', () => {
    const first = createDocumentCspNonce()
    const second = createDocumentCspNonce()
    expect(first).toMatch(/^[A-Za-z0-9_-]{22}$/)
    expect(second).not.toBe(first)
  })

  it('detects whether the Host sent script-src before minting a nonce', () => {
    expect(hasDocumentCspDirective(HOST_BASELINE_DIRECTIVES, 'script-src')).toBe(true)
    expect(hasDocumentCspDirective([{ name: 'default-src', sources: ["'none'"] }], 'script-src')).toBe(false)
  })

  it('stamps the nonce onto inline scripts and leaves external ones alone', () => {
    const nonce = 'abc123'
    const html = [
      '<script>window.__NUXT__={}</script>',
      '<script type="module" src="/_nuxt/entry.js"></script>',
      '<script nonce="keep-me">already()</script>',
      '<script type="application/json" id="__NUXT_DATA__">[]</script>'
    ].join('')
    const injected = applyDocumentCspNonceToHtmlChunk(html, nonce)
    expect(injected).toContain(`<script nonce="${nonce}">window.__NUXT__={}</script>`)
    expect(injected).toContain('<script type="module" src="/_nuxt/entry.js"></script>')
    expect(injected).toContain('<script nonce="keep-me">already()</script>')
    expect(injected).toContain(`<script nonce="${nonce}" type="application/json" id="__NUXT_DATA__">[]</script>`)
    // 每个可注入脚本恰好出现一次 nonce 属性。
    expect(injected.match(/<script nonce="abc123"/g)).toHaveLength(2)
  })

  it('leaves chunks untouched without a nonce or without inline scripts', () => {
    const html = '<script>inline()</script>'
    expect(applyDocumentCspNonceToHtmlChunk(html, '')).toBe(html)
    expect(applyDocumentCspNonceToHtmlChunk('<div>no script</div>', 'abc123')).toBe('<div>no script</div>')
  })

  it('escapes the nonce before putting it in an attribute', () => {
    expect(applyDocumentCspNonceToHtmlChunk('<script>x()</script>', 'a"b'))
      .toBe('<script nonce="a&quot;b">x()</script>')
  })

  it('wires the render:html plugin to the per-request nonce', () => {
    const plugin = source('../../server/plugins/document-csp.ts')
    expect(plugin).toContain("hook('render:html'")
    expect(plugin).toContain('DOCUMENT_CSP_NONCE_CONTEXT_KEY')
    // composable 写、plugin 读，键名是两边的契约。
    expect(DOCUMENT_CSP_NONCE_CONTEXT_KEY).toBe('sforumDocumentCspNonce')
    expect(plugin).toContain('applyDocumentCspNonceToHtmlChunk')
    for (const chunk of ['head', 'bodyAppend']) {
      expect(plugin).toContain(`'${chunk}'`)
    }
    // body 装的是页面 SSR 内容（可能含 v-html 注入的脚本）：给它打 nonce 等于
    // 把内容层注入合法化，必须留在 script-src 'self' 的拦截范围内。
    expect(plugin).not.toContain("'body'")
    expect(plugin).not.toContain("'bodyPrepend'")
  })

  it('injects the nonce through the render:html hook and skips responses without one', async () => {
    const globals = globalThis as unknown as Record<string, unknown>
    const previousDefineNitroPlugin = globals.defineNitroPlugin
    // nitro 的 auto-import 在 bun test 里不存在；用最小桩拿到插件注册函数。
    globals.defineNitroPlugin = (plugin: unknown) => plugin
    try {
      const { default: registerPlugin } = await import('../../server/plugins/document-csp')
      const hooks = new Map<string, ((html: unknown, context: unknown) => void)[]>()
      const nitroApp = {
        hooks: {
          hook(name: string, handler: (html: unknown, context: unknown) => void) {
            hooks.set(name, [...(hooks.get(name) || []), handler])
          }
        }
      }
      ;(registerPlugin as unknown as (app: unknown) => void)(nitroApp)
      const renderHooks = hooks.get('render:html') || []
      expect(renderHooks).toHaveLength(1)

      const html = {
        htmlAttrs: [],
        head: ['<script>window.__NUXT__={}</script>'],
        bodyAttrs: [],
        bodyPrepend: ['<script>teleport()</script>'],
        body: ['<div id="__nuxt"><script>injectedByVHtml()</script></div>'],
        bodyAppend: ['<script type="application/json">[]</script>']
      }
      renderHooks[0](html, { event: { context: { [DOCUMENT_CSP_NONCE_CONTEXT_KEY]: 'n0nce' } } })
      expect(html.head[0]).toBe('<script nonce="n0nce">window.__NUXT__={}</script>')
      // bodyPrepend / body 不在 Nuxt 自己拥有的 chunk 里，必须原样返回。
      expect(html.bodyPrepend[0]).toBe('<script>teleport()</script>')
      expect(html.body[0]).toBe('<div id="__nuxt"><script>injectedByVHtml()</script></div>')
      expect(html.bodyAppend[0]).toBe('<script nonce="n0nce" type="application/json">[]</script>')

      // 没写 CSP 的响应（没有 nonce）必须原样返回。
      const untouched = {
        htmlAttrs: [],
        head: ['<script>keep()</script>'],
        bodyAttrs: [],
        bodyPrepend: [],
        body: [],
        bodyAppend: []
      }
      renderHooks[0](untouched, { event: { context: {} } })
      expect(untouched.head[0]).toBe('<script>keep()</script>')
    } finally {
      if (previousDefineNitroPlugin === undefined) {
        delete globals.defineNitroPlugin
      } else {
        globals.defineNitroPlugin = previousDefineNitroPlugin
      }
    }
  })
})
