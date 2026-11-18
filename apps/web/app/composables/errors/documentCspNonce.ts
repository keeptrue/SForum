/**
 * 文档 CSP 的 nonce 组装与 HTML 注入（纯函数，服务端专用）。
 *
 * 为什么存在：Host 下发的 documentPolicy 只包含可静态确定的源（`script-src 'self'`），
 * 而 Nuxt SSR 会把页面数据与引导代码写成内联 `<script>`。要在不放宽 `script-src`
 * 的前提下放行它们，只能按请求发一个 nonce：响应头里 `script-src` 追加且仅追加一个
 * `'nonce-…'`，渲染出的内联 `<script>` 打上同一个值。
 *
 * ⚠️ 只碰 `script-src`。`style-src` 一旦出现 nonce 或 hash，CSP3 会忽略同一指令里的
 * `'unsafe-inline'`，内联样式会再次全部被拦。Host 给什么就原样保留什么。
 *
 * 唯一例外是 `connect-src` 的 dev 工具链源：Vite HMR 的 socket 主机名是构建期硬编码的
 * （`localhost:5173`），Iconify 客户端兜底还会请求三个公共 API。这些源只在 dev 组装时
 * 追加，生产 header 必须逐字等于 Host 下发的策略。
 */

/** nonce 在 H3 event.context 上的键：header 与 HTML 注入靠它对齐。 */
export const DOCUMENT_CSP_NONCE_CONTEXT_KEY = 'sforumDocumentCspNonce'

/** 本响应已写下的文档策略（来源 + 策略本体）：双写时靠它判断「具体胜基线」。 */
export const DOCUMENT_CSP_APPLIED_CONTEXT_KEY = 'sforumDocumentCspApplied'

const NONCE_BYTES = 16
const NONCE_SOURCE_PATTERN = /^'nonce-.*'$/i
const SCRIPT_TAG_PATTERN = /<script\b([^>]*)>/gi
const SCRIPT_SRC_ATTRIBUTE_PATTERN = /(?:^|\s)src\s*=/i
const SCRIPT_NONCE_ATTRIBUTE_PATTERN = /(?:^|\s)nonce\s*=/i

export type DocumentCspDirective = {
  name: string
  sources: readonly string[]
}

export type DocumentCspBuildOptions = {
  /** per-request nonce；只有 Host 下发了 `script-src` 才传。 */
  nonce?: string
  /** dev 才追加工具链源；生产必须为 false。 */
  dev?: boolean
  /** 跨源部署时 API 的 origin；同源传空数组。 */
  extraConnectSrc?: readonly string[]
}

/**
 * dev 工具链需要的 `connect-src` 源。
 * - `ws:` / `wss:`：Vite HMR 的 socket 主机名由 `@vite/client` 在构建期写死
 *   （`serverHost = "localhost:5173/_nuxt/"`），与页面真实 origin 不一致，`'self'` 拦不住。
 * - `http://localhost:*` / `http://127.0.0.1:*`：dev 下同机 API、资源与 HMR 回退。
 * - 三个 iconify 主机：`@iconify/vue` 在图标未内置时从公共 API 兜底取图。
 */
export const DEV_CONNECT_SRC_SOURCES = [
  'ws:',
  'wss:',
  'http://localhost:*',
  'http://127.0.0.1:*',
  'https://api.iconify.design',
  'https://api.simplesvg.com',
  'https://api.unisvg.com'
] as const

/** per-request nonce：16 字节随机数的 base64url 形式（22 字符，无填充）。 */
export function createDocumentCspNonce(): string {
  const bytes = new Uint8Array(NONCE_BYTES)
  globalThis.crypto.getRandomValues(bytes)
  let binary = ''
  for (const byte of bytes) {
    binary += String.fromCharCode(byte)
  }
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/**
 * 以 Host 下发的 directives 为源组装最终 header。
 * `script-src` 追加且仅追加一个 `'nonce-…'`（Host 若已带 nonce 则替换，不叠加）；
 * dev 下 `connect-src` 追加工具链源；其它指令逐字保留。
 */
export function buildDocumentCspHeaderValue(
  directives: readonly DocumentCspDirective[],
  options: DocumentCspBuildOptions = {}
): string {
  const nonce = options.nonce || ''
  const dev = options.dev === true
  const extraConnectSrc = options.extraConnectSrc || []
  const parts: string[] = []
  for (const directive of directives) {
    const name = String(directive?.name || '').trim()
    if (!name) {
      continue
    }
    let sources = directive.sources.map(source => String(source).trim()).filter(Boolean)
    if (name === 'script-src' && nonce) {
      sources = sources.filter(source => !NONCE_SOURCE_PATTERN.test(source))
      sources.push(`'nonce-${nonce}'`)
    }
    if (name === 'connect-src') {
      const appended = dev ? [...DEV_CONNECT_SRC_SOURCES, ...extraConnectSrc] : extraConnectSrc
      for (const source of appended) {
        if (!sources.includes(source)) {
          sources.push(source)
        }
      }
    }
    // 零来源指令显式写 'none'：裸指令名与 `'none'` 在浏览器里等效（都能压过宽松的
    // default-src），但显式 `'none'` 才是可读、可审计的契约；指令绝不静默消失。
    parts.push(sources.length ? `${name} ${sources.join(' ')}` : `${name} 'none'`)
  }
  return parts.join('; ')
}

/** Host 是否下发了某条指令；只有真的存在 `script-src` 时才值得发 nonce。 */
export function hasDocumentCspDirective(
  directives: readonly DocumentCspDirective[],
  name: string
): boolean {
  return directives.some(directive => String(directive?.name || '').trim() === name)
}

/**
 * 跨源部署时 `connect-src` 需要补的 API origin。
 *
 * `'self'` 只覆盖页面自己的 origin；`NUXT_PUBLIC_API_BASE_URL` 配成绝对地址时
 * （例如 `https://api.example.com/api/v1`），浏览器会把所有 API 请求判成跨源并
 * 全部拦掉。默认的 `/api/v1` 是相对路径，同源，什么都不加。
 */
export function apiConnectSources(apiBaseUrl: string, pageOrigin: string): string[] {
  const raw = String(apiBaseUrl || '').trim()
  if (!/^https?:\/\//i.test(raw)) {
    return []
  }
  let url: URL
  try {
    url = new URL(raw)
  } catch {
    return []
  }
  const origin = `${url.protocol}//${url.host}`
  // 无法判定页面 origin 时按跨源处理：宁可多一条站点自己配置的 API 源，
  // 也不要让跨源部署的站点一上线就发不出请求。
  if (pageOrigin && origin === pageOrigin) {
    return []
  }
  return [origin]
}

export type DocumentCspPolicyLike = {
  documentPolicy: {
    digest: string
    directives: readonly DocumentCspDirective[]
  }
}

export type DocumentCspPolicySource = 'page' | 'baseline'

/**
 * 「具体胜基线」：同一响应可能被写两次头（主题外壳 + 正文岛里再渲染一次主题模板）。
 * - 还没写过 → 写；
 * - 本次是逐页策略 → 覆盖（更具体者胜）；
 * - 本次只有基线、而响应已经写过 → 跳过，绝不用基线盖掉外层已经写下的逐页策略
 *   （那会丢掉扩展贡献的非可执行指令）。
 */
export function shouldWriteDocumentCsp(
  appliedSource: DocumentCspPolicySource | null,
  nextSource: DocumentCspPolicySource
): boolean {
  if (!appliedSource) {
    return true
  }
  return nextSource === 'page'
}

/**
 * Host 基线的模块级缓存：首次成功拉取后一直复用，失败不缓存（下个请求重试），
 * 并发首请求共用同一个 in-flight。
 *
 * 代价说明：缓存住的是基线本体，所以 `X-SForum-Document-Policy-Digest` 在扩展启停
 * 导致 graphDigest 变化后会保持旧值，直到进程重启。基线只随部署与扩展图变化，
 * 换来的是每个 pageview 少一次 API 往返。
 */
export function createDocumentCspBaselineCache<TPolicy>() {
  let cached: TPolicy | null = null
  let inFlight: Promise<TPolicy | null> | null = null
  return {
    load(fetchPolicy: () => Promise<TPolicy | null>): Promise<TPolicy | null> {
      if (cached) {
        return Promise.resolve(cached)
      }
      if (!inFlight) {
        inFlight = fetchPolicy()
          .then((policy) => {
            if (policy) {
              cached = policy
            }
            return policy
          })
          .finally(() => {
            inFlight = null
          })
      }
      return inFlight
    },
    reset(): void {
      cached = null
      inFlight = null
    }
  }
}

/**
 * 逐页策略优先，失败回退到缓存的 Host 基线；两者都没有才返回 null。
 *
 * 基线是 Host 自己的策略，可以安全下发；「没头」才是要避免的回归。
 */
export function selectDocumentCspPolicy<T extends DocumentCspPolicyLike>(
  pagePolicy: T | null,
  baselinePolicy: T | null
): { policy: T, source: DocumentCspPolicySource } | null {
  if (pagePolicy) {
    return { policy: pagePolicy, source: 'page' }
  }
  if (baselinePolicy) {
    return { policy: baselinePolicy, source: 'baseline' }
  }
  return null
}

/**
 * 给一个 HTML chunk 里没有 `src` 也没有 `nonce` 的 `<script>` 打上 nonce。
 * 带 `src` 的外链脚本由 `'self'` 放行，不需要也不应该重复打标；
 * `type="application/json"` 的载荷脚本不执行，打 nonce 无害，省掉特判。
 */
export function applyDocumentCspNonceToHtmlChunk(chunk: string, nonce: string): string {
  if (!chunk || !nonce) {
    return chunk
  }
  const attribute = nonce.replace(/&/g, '&amp;').replace(/"/g, '&quot;')
  return chunk.replace(SCRIPT_TAG_PATTERN, (match, attributes: string) => {
    if (SCRIPT_SRC_ATTRIBUTE_PATTERN.test(attributes) || SCRIPT_NONCE_ATTRIBUTE_PATTERN.test(attributes)) {
      return match
    }
    return `<script nonce="${attribute}"${attributes}>`
  })
}
