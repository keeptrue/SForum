import { isProtectedSEOPath } from '../utils/seoProtectedPaths'

export default defineEventHandler(async (event) => {
  if (event.method !== 'GET') {
    return
  }

  const url = getRequestURL(event)
  if (url.pathname.startsWith('/_nuxt/') || url.pathname.startsWith('/_sforum/assets/')) {
    // 仅内容寻址资源允许永久缓存；普通带扩展名页面/文件不能据此推断不可变。
    setHeader(event, 'cache-control', 'public, max-age=31536000, immutable')
    return
  }
  if (url.pathname.startsWith('/_sforum/private-assets/')) return

  const settings = await loadServerSEOSettings()
  const adminPrefix = (process.env.NUXT_PUBLIC_ADMIN_ROUTE_PREFIX || process.env.ADMIN_ROUTE_PREFIX || '/control-panel').replace(/\/+$/, '')
  const protectedPath = isProtectedSEOPath(url.pathname, adminPrefix)
  const rule = protectedPath || !serverSEOIndexable(settings)
    ? 'noindex, nofollow'
    : 'index, follow, max-image-preview:large, max-snippet:-1, max-video-preview:-1'

  setHeader(event, 'X-Robots-Tag', rule)
})
