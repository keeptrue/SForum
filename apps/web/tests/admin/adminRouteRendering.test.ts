import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { ref } from 'vue'

describe('admin route rendering', () => {
  test('does not configure any route as SPA-only to avoid empty-shell white screens', () => {
    const config = readFileSync(new URL('../../nuxt.config.ts', import.meta.url), 'utf8')
    // 全站不应有任何 ssr: false 路由——所有页面都 SSR，彻底杜绝空壳白屏。
    expect(config).not.toMatch(/['"`][^'"`]+['"`]\s*:\s*\{[^}]*\bssr\s*:\s*false/)
  })

  test('renders admin and component-preview pages via SSR', () => {
    const config = readFileSync(new URL('../../nuxt.config.ts', import.meta.url), 'utf8')
    // admin 路由（模板字符串 key）不再带 ssr: false。
    expect(config).toMatch(/\[`\$\{adminRoutePrefix\}\/\*\*`\]/)
    expect(config).not.toMatch(/adminRoutePrefix[^}]*ssr\s*:\s*false/)
    // 组件预览页不再带 ssr: false。
    expect(config).not.toMatch(/['"`]\/components['"`]\s*:\s*\{[^}]*ssr\s*:\s*false/)
  })

  test('disables route cache for admin pages', () => {
    const config = readFileSync(new URL('../../nuxt.config.ts', import.meta.url), 'utf8')
    // admin 路由是动态 key（模板字符串），验证其配置块包含 cache: false。
    expect(config).toMatch(/adminRoutePrefix.*\*\*.*\{[^}]*cache\s*:\s*false/)
  })

  test('redirects to login instead of rendering a Nuxt error when auth service is unavailable', async () => {
    const status = ref('unknown')
    const actions = {
      navigations: [] as unknown[],
      errors: [] as unknown[],
      aborts: [] as unknown[]
    }
    const middleware = loadAdminMiddleware({
      useAuthSession: () => ({
        user: ref(null),
        status,
        refresh: async () => {
          status.value = 'unavailable'
          return null
        },
        can: () => false
      }),
      navigateTo: async (route: unknown) => {
        actions.navigations.push(route)
        return { type: 'navigate', route }
      },
      createError: (error: unknown) => {
        actions.errors.push(error)
        return error
      },
      abortNavigation: (error: unknown) => {
        actions.aborts.push(error)
        return { type: 'abort', error }
      }
    })

    await middleware({ fullPath: '/control-panel/extensions?page=2' })

    expect(actions.errors).toEqual([])
    expect(actions.aborts).toEqual([])
    expect(actions.navigations).toEqual([{
      path: '/login',
      query: { redirect: '/control-panel/extensions?page=2' }
    }])
  })

  test('renders the shared 403 state instead of silently bouncing home without admin.access', async () => {
    const user = ref({ id: 1, roleKeys: ['member'], permissions: ['topic.create'] })
    const actions = {
      navigations: [] as unknown[],
      errors: [] as unknown[],
      aborts: [] as unknown[]
    }
    const middleware = loadAdminMiddleware({
      useAuthSession: () => ({
        user,
        status: ref('authenticated'),
        refresh: async () => user.value,
        can: () => false
      }),
      navigateTo: async (route: unknown) => {
        actions.navigations.push(route)
        return { type: 'navigate', route }
      },
      createError: (error: unknown) => {
        actions.errors.push(error)
        return error
      },
      abortNavigation: (error: unknown) => {
        actions.aborts.push(error)
        return { type: 'abort', error }
      }
    })

    // 权限不足要落到共享 403 态：静默弹回首页会让「没有后台权限」和「链接失效」长得一样。
    let thrown: unknown
    try {
      await middleware({ fullPath: '/control-panel/users' })
    } catch (error) {
      thrown = error
    }

    expect(thrown).toMatchObject({ statusCode: 403 })
    expect(actions.navigations).toEqual([])
    expect(actions.aborts).toEqual([])
  })
})

function loadAdminMiddleware(globals: {
  useAuthSession: () => unknown
  navigateTo: (route: unknown) => unknown
  createError: (error: unknown) => unknown
  abortNavigation: (error: unknown) => unknown
}) {
  const source = readFileSync(new URL('../../app/middleware/admin.ts', import.meta.url), 'utf8')
  const transpiler = new Bun.Transpiler({ loader: 'ts', target: 'browser' })
  const executable = transpiler.transformSync(
    source.replace(/^import(?:[\s\S]*?from\s+)?['"][^'"]+['"];?\s*$/gm, '')
  )
    .replaceAll('import.meta.dev', 'true')
    .replace(/export default /, 'return ')

  const factory = new Function(
    'defineNuxtRouteMiddleware',
    'useLocalePath',
    'useAuthSession',
    'navigateTo',
    'createError',
    'abortNavigation',
    executable
  )

  return factory(
    (middleware: (to: { fullPath: string }) => Promise<unknown>) => middleware,
    () => (path: string) => path,
    globals.useAuthSession,
    globals.navigateTo,
    globals.createError,
    globals.abortNavigation
  ) as (to: { fullPath: string }) => Promise<unknown>
}
