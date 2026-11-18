import { _api, addAPIProvider } from '@iconify/vue'

/**
 * 让客户端图标走同源 `/api/_nuxt_icon`，而不是公共 Iconify API。
 *
 * 为什么需要这一步（实测定位，不是推演）：
 * - `@nuxt/icon` 2.6.2 的 `applyIconifyConfig()` 只在「默认 provider 仍是默认值」时
 *   才把 resources 换成 `localApiEndpoint`，它判断默认值的基准是
 *   `DEFAULT_RESOURCES = ['https://api.iconify.design']`（**1** 个主机）。
 * - `@iconify/vue` 5.x 的默认 provider 实际是 **3** 个主机
 *   （`api.iconify.design` + `api.simplesvg.com` + `api.unisvg.com`，见其 dist 的
 *   `fallBackAPISources`），长度不等 → 判定失败 → resources 永远不被替换。
 * - 于是生产页面的动态图标（如 `i-lucide-book-marked`）会请求那三个公共主机，
 *   被文档 CSP `connect-src 'self'` 拦掉，图标静默消失；dev 分支放行了这三个主机，
 *   所以开发期看不见。
 * - `icon.fallbackToApi: false` 管的是另一条分支（是否把公共 API 追加进 resources），
 *   盖不住这里的判定失败。
 *
 * 这里直接把默认 provider 指向同源端点：所有图标（含运行时拼接的动态名字）都从本站
 * `/api/_nuxt_icon` 取，生产 CSP 不需要为第三方主机开口子。
 */
const LOCAL_ICON_ENDPOINT = '/api/_nuxt_icon'

/** 幂等：已经把默认 provider 指到同源端点时返回 false。 */
export function applyLocalIconProvider(endpoint = LOCAL_ICON_ENDPOINT): boolean {
  const resources = _api.getAPIConfig('')?.resources || []
  if (resources.length === 1 && resources[0] === endpoint) {
    return false
  }
  return addAPIProvider('', { resources: [endpoint] })
}

export default defineNuxtPlugin(() => {
  applyLocalIconProvider()
})
