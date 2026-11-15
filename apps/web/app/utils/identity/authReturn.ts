const AUTH_RETURN_BASE = 'http://sforum.local'
const AUTH_PAGE_PATTERN = /\/(?:login|register)\/?$/

// 非 Nuxt 页面：API 端点与插件 HTTP 路由由服务端直接响应。
// 客户端路由会把它们交给 `[...sfRegistryPage]` catch-all（渲染成
// “Failed to load extension page”），所以认证回跳必须整页导航。
const DOCUMENT_NAVIGATION_PREFIXES = ['/api/', '/plugin/']

export function normalizeAuthReturnPath(candidate: unknown): string | null {
  if (typeof candidate !== 'string' || !candidate.startsWith('/') || candidate.startsWith('//')) {
    return null
  }

  try {
    const url = new URL(candidate, AUTH_RETURN_BASE)
    if (url.origin !== AUTH_RETURN_BASE) {
      return null
    }

    // URL 归一化会把 `/..//host`、`/a/../..//host` 这类输入塌陷成协议相对地址
    // `//host`：原始输入并没有以 `//` 开头，但归一化后的 pathname 会。这种值交给
    // navigateTo 会被判成外链直接抛错，登录成功后落到错误页，所以这里再判一次。
    if (url.pathname.startsWith('//')) {
      return null
    }

    // 非法百分号转义在 pathname/search/hash 任一处都视为不可信输入。
    const pathname = decodePathPart(url.pathname)
    if (pathname === null
      || decodePathPart(url.search) === null
      || decodePathPart(url.hash) === null) {
      return null
    }
    if (AUTH_PAGE_PATTERN.test(pathname)) {
      return null
    }

    return `${url.pathname}${url.search}${url.hash}`
  } catch {
    return null
  }
}

function decodePathPart(value: string): string | null {
  try {
    return decodeURI(value)
  } catch {
    return null
  }
}

export function resolveAuthReturnPath(
  explicit: unknown,
  referrer: unknown,
  fallback: string
): string {
  return normalizeAuthReturnPath(explicit)
    ?? normalizeAuthReturnPath(referrer)
    ?? normalizeAuthReturnPath(fallback)
    ?? '/'
}

export function buildAuthPageLink(path: string, explicitRedirect: unknown) {
  const redirect = normalizeAuthReturnPath(explicitRedirect)
  if (!redirect) {
    return path
  }

  return {
    path,
    query: { redirect }
  }
}

// 认证完成后的回跳目标是否需要整页导航（而不是 Nuxt 客户端路由）。
// 插件路由（如 OAuth2 授权端点 `/api/oauth/authorize`）只存在于服务端，
// 内部导航会被 catch-all 页面接住，永远到不了端点。
export function requiresDocumentNavigation(path: string): boolean {
  if (typeof path !== 'string' || !path.startsWith('/')) {
    return false
  }
  return DOCUMENT_NAVIGATION_PREFIXES.some(prefix => path.startsWith(prefix))
}

