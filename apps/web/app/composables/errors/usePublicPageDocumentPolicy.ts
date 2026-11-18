import type { PublicFrontendComponentRef, PublicFrontendPolicy } from '~/runtime/public-extensions/pagePolicy'
import {
  PUBLIC_PAGE_CSP_HEADER,
  PUBLIC_PAGE_POLICY_TIMEOUT_MS,
  parsePublicFrontendPolicy,
  publicPagePolicyPath
} from '~/runtime/public-extensions/pagePolicy'
import {
  DOCUMENT_CSP_APPLIED_CONTEXT_KEY,
  DOCUMENT_CSP_NONCE_CONTEXT_KEY,
  apiConnectSources,
  buildDocumentCspHeaderValue,
  createDocumentCspBaselineCache,
  createDocumentCspNonce,
  hasDocumentCspDirective,
  selectDocumentCspPolicy,
  shouldWriteDocumentCsp,
  type DocumentCspPolicySource
} from './documentCspNonce'

type ApiRequest = ReturnType<typeof useApiClient>['request']

/**
 * 用已捕获的 API client 取策略。
 * `useApiClient()` 会把 runtimeConfig / cookie 在创建时钉住，所以跨 await 复用它是安全的；
 * 反过来在 await 之后才调用 composable 会丢 Nuxt 实例（NUXT_E1001）。
 */
async function requestPublicPagePolicy(
  request: ApiRequest,
  refs: readonly PublicFrontendComponentRef[]
): Promise<PublicFrontendPolicy | null> {
  try {
    const raw = await request<unknown>(publicPagePolicyPath(refs), {
      timeout: PUBLIC_PAGE_POLICY_TIMEOUT_MS
    })
    return parsePublicFrontendPolicy(raw)
  } catch {
    // 制品/信任不可用或 Safe Mode 生效时，调用方应对 L2 失败关闭。
    return null
  }
}

/**
 * 取页面 L2 聚合策略。服务端用于写文档 CSP 头；客户端用于同帧预注入
 * `<link rel="stylesheet">`（消除样式/h1 首帧闪变）。策略端点是 no-store 的
 * 权威读取；客户端换页与 widget 描述符读取同级成本，可接受。
 */
export async function fetchPublicPagePolicy(
  refs: readonly PublicFrontendComponentRef[] = []
): Promise<PublicFrontendPolicy | null> {
  const { request } = useApiClient()
  return requestPublicPagePolicy(request, refs)
}

/**
 * Host 基线的模块级缓存。
 *
 * 零 ref 的 page-policy 就是 Host 自己的文档策略常量（后端对空 component 列表不做
 * 逐组件查询），所以只该拉一次：首屏之后每个 pageview 都不再为它付一次 RTT。
 * 失败不缓存（清掉 in-flight 句柄，下个请求重试）；成功就一直复用。
 *
 * 失效路径：`import.meta.hot` 的 dispose 会清空缓存，dev 下改完 web 代码由 Vite
 * 重新求值本模块即可；若只改了 API 基线而没有动 web 文件，需要重启 `bun run dev`
 * 才会拿到新基线。生产按部署整体重启，不需要额外失效。
 */
const hostBaselineCache = createDocumentCspBaselineCache<PublicFrontendPolicy>()

async function loadHostBaselinePolicy(request: ApiRequest): Promise<PublicFrontendPolicy | null> {
  // 首个请求的 client 闭包被并发请求共享：策略端点是公开的 no-store 读取，
  // 不带任何身份语义，所以共享安全；换来的是一次 API 往返而不是 N 次。
  return hostBaselineCache.load(() => requestPublicPagePolicy(request, []))
}

/** 显式失效：dev 热更新与测试用。 */
export function resetHostBaselinePolicyCache(): void {
  hostBaselineCache.reset()
}

if (import.meta.hot) {
  import.meta.hot.dispose(() => resetHostBaselinePolicyCache())
}

/**
 * 在 Nuxt SSR 上应用 Host 聚合的 Content-Security-Policy。
 * 仅在服务端写入响应头；客户端换页复用 fetchPublicPagePolicy 做样式预注入，
 * 但文档 CSP 只能由 SSR 响应头下发（浏览器无法在换页时改文档头）。
 *
 * 每个公开 SSR 页都写头：零 ref 页面用缓存的 Host 基线，有 L2 ref 的页面额外拉一次
 * 逐页策略（styles 预注入 + 逐页 digest）。逐页拉取失败回退到基线，而不是丢头。
 * 唯一允许「无头」的路径是基线本身拿不到（API 不可用/策略解析失败）——那时返回
 * null，调用方保留 L1/SSR 主内容，宁可不发策略也不编造一份没有 Host 背书的头。
 *
 * dev 与生产写同一份文档 CSP：只有开发期也下发，缺陷才会在开发期暴露，
 * 而不是等上线后才发现内联脚本被拦。
 */
export async function applyPublicPageDocumentPolicy(
  refs: readonly PublicFrontendComponentRef[] = []
): Promise<PublicFrontendPolicy | null> {
  if (!import.meta.server) {
    return null
  }
  const event = useRequestEvent()
  if (!event) {
    return null
  }
  // Nuxt 实例只在同步段可用：所有 composable 必须在第一个 await 之前取完，
  // 否则跨过 await 再调用会抛 NUXT_E1001（整页 500）。
  const { request } = useApiClient()
  const apiBaseUrl = String(useRuntimeConfig().public.apiBaseUrl || '')
  const pageOrigin = useRequestURL().origin
  // 有 L2 ref 才多付一次逐页策略；零 ref 页面只用缓存的 Host 基线，不逐页请求 API。
  const pagePolicy = refs.length ? await requestPublicPagePolicy(request, refs) : null
  const baselinePolicy = pagePolicy ? null : await loadHostBaselinePolicy(request)
  const selection = selectDocumentCspPolicy(pagePolicy, baselinePolicy)
  if (!selection) {
    return null
  }
  // 同一响应可能被写两次头（主题外壳 + 正文岛里的主题模板）。规则是「具体胜基线」：
  // 已经写过逐页策略时，内层的基线不得覆盖它；逐页策略永远可以覆盖基线。
  const applied = event.context[DOCUMENT_CSP_APPLIED_CONTEXT_KEY] as
    { source: DocumentCspPolicySource, policy: PublicFrontendPolicy } | undefined
  if (!shouldWriteDocumentCsp(applied?.source ?? null, selection.source)) {
    // 跳过写头时返回「已在生效」的那份策略：DOM 上的 digest 必须与响应头一致。
    return applied?.policy ?? selection.policy
  }
  const policy = selection.policy
  const directives = policy.documentPolicy.directives
  // Host 真的下发 script-src 时才发 nonce；凭空补一条 script-src 等于替 Host 放宽
  // default-src 'none'，不在前端的权限范围内。
  const nonce = hasDocumentCspDirective(directives, 'script-src') ? createDocumentCspNonce() : ''
  // dev 才追加 Vite HMR / Iconify 兜底的 connect-src 源；跨源部署再补 API origin。
  // 生产除此之外逐字保留 Host 策略。
  const headerValue = buildDocumentCspHeaderValue(directives, {
    nonce,
    dev: import.meta.dev,
    extraConnectSrc: apiConnectSources(apiBaseUrl, pageOrigin)
  })
  if (!headerValue) {
    return null
  }
  // 先把 nonce 放进请求上下文，server/plugins/document-csp.ts 才能给内联 <script> 打同一个值。
  if (nonce) {
    event.context[DOCUMENT_CSP_NONCE_CONTEXT_KEY] = nonce
  } else {
    delete event.context[DOCUMENT_CSP_NONCE_CONTEXT_KEY]
  }
  // Nuxt 4 未导出 setResponseHeader 组合式，直接写 nitro 响应对象。
  event.node.res.setHeader(PUBLIC_PAGE_CSP_HEADER, headerValue)
  event.node.res.setHeader('X-SForum-Document-Policy-Digest', policy.documentPolicy.digest)
  event.context[DOCUMENT_CSP_APPLIED_CONTEXT_KEY] = { source: selection.source, policy }
  return policy
}
