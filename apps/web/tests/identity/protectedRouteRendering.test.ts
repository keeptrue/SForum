import { describe, expect, test } from 'bun:test'
import { existsSync, readFileSync } from 'node:fs'

describe('protected route rendering', () => {
  test('avoids the nuxt-i18n Nitro context startup race', () => {
    const config = readFileSync(new URL('../../nuxt.config.ts', import.meta.url), 'utf8')

    expect(config).toMatch(/experimental\s*:\s*\{[^}]*nitroContextDetection\s*:\s*false/s)
    expect(config).toContain("strategy: 'no_prefix'")
    expect(config).toContain('detectBrowserLanguage: {')
    expect(config).not.toContain("strategy: 'prefix_except_default'")
  })

  test('does not serve protected user workflows as empty SPA shells', () => {
    const config = readFileSync(new URL('../../nuxt.config.ts', import.meta.url), 'utf8')
    const protectedRoutes = [
      '/settings/**',
      '/notifications',
      '/notifications/**',
      '/topics/new',
      '/t/**'
    ]

    for (const route of protectedRoutes) {
      const escapedRoute = route.replaceAll('/', '\\/').replaceAll('*', '\\*')
      expect(config).not.toMatch(new RegExp(`['"]${escapedRoute}['"]\\s*:\\s*\\{[^}]*ssr\\s*:\\s*false`))
    }
  })

  test('disables route cache for protected user workflows', () => {
    const config = readFileSync(new URL('../../nuxt.config.ts', import.meta.url), 'utf8')
    const protectedRoutes = [
      '/settings/**',
      '/notifications',
      '/notifications/**',
      '/topics/new',
      '/t/**'
    ]

    for (const route of protectedRoutes) {
      const escapedRoute = route.replaceAll('/', '\\/').replaceAll('*', '\\*')
      expect(config).toMatch(new RegExp(`['"]${escapedRoute}['"]\\s*:\\s*\\{[^}]*cache\\s*:\\s*false`))
    }
  })

  test('keeps the session-owned notification inbox out of search indexes', () => {
    const config = readFileSync(new URL('../../nuxt.config.ts', import.meta.url), 'utf8')
    const protectedRoutes = [
      '/notifications',
      '/notifications/**'
    ]

    for (const route of protectedRoutes) {
      const escapedRoute = route.replaceAll('/', '\\/').replaceAll('*', '\\*')
      expect(config).toMatch(new RegExp(`['"]${escapedRoute}['"]\\s*:\\s*\\{[^}]*robots\\s*:\\s*\\{\\s*index\\s*:\\s*false`))
    }
  })

  test('topic detail disables whole-page caching and applies explicit response policies', () => {
    const config = readFileSync(new URL('../../nuxt.config.ts', import.meta.url), 'utf8')
    expect(config).toMatch(/['"]\/t\/\*\*['"]\s*:\s*\{\s*cache\s*:\s*false\s*\}/)
    expect(config).not.toContain('x-sforum-public-surface-revision')
    expect(config).not.toContain("'/en/t/**'")

    const middlewarePath = new URL('../../server/middleware/topic-page-cache.ts', import.meta.url)
    expect(existsSync(middlewarePath)).toBe(true)
    const source = readFileSync(middlewarePath, 'utf8')
    expect(source).toContain("setHeader(event, 'cache-control', 'private, no-store')")
    expect(source).toContain("appendHeader(event, 'vary', 'Cookie')")
    expect(source).toContain("appendHeader(event, 'vary', 'Authorization')")
    expect(source).toContain("appendHeader(event, 'vary', 'Accept-Language')")
    expect(source).not.toContain("setHeader(event, 'cache-control', 'public, no-cache')")
    expect(source).not.toContain('routeRules')
    expect(source).not.toContain('loadPublicSurfaceRevision')
  })

  test('public profiles keep SSR but disable whole-page caching', () => {
    const config = readFileSync(new URL('../../nuxt.config.ts', import.meta.url), 'utf8')
    expect(config).toMatch(/['"]\/u\/\*\*['"]\s*:\s*\{\s*cache\s*:\s*false\s*\}/)
    expect(config).not.toMatch(/['"]\/u\/\*\*['"]\s*:\s*\{[^}]*swr\s*:/)
    expect(config).not.toMatch(/['"]\/u\/\*\*['"]\s*:\s*\{[^}]*ssr\s*:\s*false/)
    expect(config).not.toMatch(/varies\s*:\s*\[['"]cookie['"]\]/)
  })

  test('homepage publication state is never retained by shared HTML caches', () => {
    const config = readFileSync(new URL('../../nuxt.config.ts', import.meta.url), 'utf8')
    const start = config.indexOf('const publicHomepageRouteRule =')
    const end = config.indexOf('} as const', start)
    const homepagePolicy = config.slice(start, end)

    expect(start).toBeGreaterThanOrEqual(0)
    expect(end).toBeGreaterThan(start)
    expect(homepagePolicy).toContain('cache: false')
    expect(homepagePolicy).toContain("'cache-control': 'no-store'")
    expect(homepagePolicy).not.toContain('s-maxage')
  })

  test('bypasses shared SWR when non-default locale cookie is present', () => {
    const middlewarePath = new URL('../../server/middleware/locale-cache.ts', import.meta.url)
    expect(existsSync(middlewarePath)).toBe(true)
    const source = readFileSync(middlewarePath, 'utf8')
    expect(source).toContain('sforum_locale')
    expect(source).toContain('zh-CN')
    expect(source).toContain('routeRules.cache = false')
    expect(source).toContain('routeRules.swr = false')
  })

  test('301-strips legacy /en locale prefixes', () => {
    const middlewarePath = new URL('../../server/middleware/locale-prefix-compat.ts', import.meta.url)
    expect(existsSync(middlewarePath)).toBe(true)
    const source = readFileSync(middlewarePath, 'utf8')
    expect(source).toContain("path.startsWith('/en/')")
    expect(source).toContain('sendRedirect')
    expect(source).toContain('301')
  })

  test('marks every session-bearing SSR response private without mutating route rules', () => {
    const middlewarePath = new URL('../../server/middleware/public-session-cache.ts', import.meta.url)
    expect(existsSync(middlewarePath)).toBe(true)

    const source = readFileSync(middlewarePath, 'utf8')
    expect(source).toContain('sforum_session')
    expect(source).toContain("accept.includes('text/html')")
    expect(source).toContain("path.endsWith('/_payload.json')")
    expect(source).toContain("setHeader(event, 'cache-control', 'private, no-store')")
    expect(source).not.toContain('routeRules')
    expect(source).not.toContain('varies')
  })

  test('has a global middleware that enforces requiresAuth page metadata', () => {
    const middlewarePath = new URL('../../app/middleware/auth.global.ts', import.meta.url)

    expect(existsSync(middlewarePath)).toBe(true)

    const source = readFileSync(middlewarePath, 'utf8')
    expect(source).toContain('to.meta.requiresAuth')
    expect(source).toContain("localePath('/login')")
  })

  test('redirects ordinary protected routes to login instead of rendering a 503 shell', () => {
    const middlewarePath = new URL('../../app/middleware/auth.global.ts', import.meta.url)
    const source = readFileSync(middlewarePath, 'utf8')

    expect(source).not.toContain("status.value === 'unavailable'")
    expect(source).not.toContain('abortNavigation(createError')
    expect(source).toContain("path: localePath('/login')")
    expect(source).toContain('query: { redirect: to.fullPath }')
  })
})
