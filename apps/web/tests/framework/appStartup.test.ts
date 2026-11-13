import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import { computed, ref } from 'vue'

describe('app startup rendering', () => {
  test('initializes anonymous SSR as guest without requesting the session', async () => {
    const page = loadAppComponentForStartupTest({ server: true })

    await page.component.setup({}, { expose: () => {} })

    expect(page.loaderStarted()).toBe(true)
    expect(page.webOptionsRefreshStarted()).toBe(true)
    expect(page.authRefreshStarted()).toBe(false)
    expect(page.guestInitialized()).toBe(true)
    expect(page.themeSkinRefreshStarted()).toBe(true)
    expect(page.publicContentStylesRefreshStarted()).toBe(true)
  })

  test('restores auth during SSR when the request carries a session', async () => {
    const page = loadAppComponentForStartupTest({
      server: true,
      cookie: 'locale=zh-CN; sforum_session=test-session'
    })

    await page.component.setup({}, { expose: () => {} })

    expect(page.loaderStarted()).toBe(true)
    expect(page.webOptionsRefreshStarted()).toBe(true)
    expect(page.authRefreshStarted()).toBe(true)
    expect(page.guestInitialized()).toBe(false)
    expect(page.themeSkinRefreshStarted()).toBe(true)
    expect(page.publicContentStylesRefreshStarted()).toBe(true)
  })

  test('does not block client setup while startup refresh is still pending', async () => {
    const page = loadAppComponentForStartupTest({ server: false })
    const setupPromise = page.component.setup({}, { expose: () => {} })

    const result = await Promise.race([
      setupPromise.then(() => 'resolved'),
      new Promise(resolve => setTimeout(() => resolve('pending'), 25))
    ])

    expect(page.loaderStarted()).toBe(false)
    expect(page.webOptionsRefreshStarted()).toBe(false)
    expect(page.authRefreshStarted()).toBe(false)
    expect(result).toBe('resolved')

    await page.runMounted()

    expect(page.webOptionsRefreshStarted()).toBe(true)
    expect(page.authRefreshStarted()).toBe(true)
    expect(page.themeSkinRefreshStarted()).toBe(true)
    expect(page.themeSkinCleared()).toBe(false)
    expect(page.publicContentStylesRefreshStarted()).toBe(true)
    expect(page.publicContentStylesCleared()).toBe(false)
  })

  test('keeps public theme skin outside admin routes', async () => {
    const page = loadAppComponentForStartupTest({ server: false, routePath: '/control-panel' })

    await page.component.setup({}, { expose: () => {} })
    await page.runMounted()

    expect(page.themeSkinRefreshStarted()).toBe(false)
    expect(page.themeSkinCleared()).toBe(true)
    expect(page.publicContentStylesRefreshStarted()).toBe(false)
    expect(page.publicContentStylesCleared()).toBe(true)
  })

  test('preserves page-resolved titles and only supplies the empty-title fallback', async () => {
    const page = loadAppComponentForStartupTest({ server: true })

    await page.component.setup({}, { expose: () => {} })

    expect(page.resolveDocumentTitle('Deploy | SForum')).toBe('Deploy | SForum')
    expect(page.resolveDocumentTitle()).toBe('SForum')
  })
})

function loadAppComponentForStartupTest(options: { server: boolean, routePath?: string, cookie?: string }) {
  const source = readFileSync(new URL('../../app/app.vue', import.meta.url), 'utf8')
  const { descriptor } = parse(source, { filename: 'app.vue' })
  const compiled = compileScript(descriptor, { id: 'app-startup-test' }).content
  const transpiler = new Bun.Transpiler({ loader: 'ts', target: 'browser' })
  const executable = transpiler.transformSync(
    compiled.replace(
      /import\s+\{\s*withAsyncContext\s+as\s+_withAsyncContext,\s*defineComponent\s+as\s+_defineComponent\s*\}\s+from\s+['"]vue['"];?/,
      'const { withAsyncContext: _withAsyncContext, defineComponent: _defineComponent } = __vue;'
    )
      .replace(/^import(?:[\s\S]*?from\s+)?['"][^'"]+['"];?\s*$/gm, '')
  )
    .replaceAll('import.meta.dev', 'true')
    .replaceAll('import.meta.server', options.server ? 'true' : 'false')
    .replace(/export default /, 'return ')

  let loaderStarted = false
  let webOptionsRefreshStarted = false
  let authRefreshStarted = false
  let guestInitialized = false
  let themeSkinRefreshStarted = false
  let themeSkinCleared = false
  let publicContentStylesRefreshStarted = false
  let publicContentStylesCleared = false
  let appHeadFactory: (() => { titleTemplate?: (title?: string) => string }) | undefined
  const mountedCallbacks: Array<() => void | Promise<void>> = []
  const never = new Promise(() => {})

  const factory = new Function(
    '__vue',
    'useLocaleHead',
    'useWebOptions',
    'useAuthSession',
    'useUserLanguage',
    'useRoute',
    'useAdminRoutes',
    'useAdminTabs',
    'useAsyncData',
    'useHead',
    'onMounted',
    'watch',
    'useActiveThemeSkin',
    'useRequestHeaders',
    'useExternalAuthFeedback',
    'useAdminAppearancePreview',
    'useAppliedAppearance',
    'usePublicContentStyles',
    'computed',
    'SFApiConnectionModal',
    'SFCelebrationHost',
    'SFBadgeDetailDialogHost',
    executable
  )

  const component = factory(
    {
      withAsyncContext: (fn: () => Promise<unknown>) => [fn(), () => {}],
      defineComponent: (options: unknown) => options
    },
    () => ({ value: { htmlAttrs: {}, link: [], meta: [] } }),
    () => ({
      siteName: ref('SForum'),
      resolvedAppearanceTheme: ref({ dataTheme: 'pine_teal', style: '' }),
      lightBackground: ref('pure_white'),
      siteFaviconUrl: ref(''),
      siteAppleTouchIconUrl: ref(''),
      refresh: () => {
        webOptionsRefreshStarted = true
        return options.server ? Promise.resolve(true) : never
      }
    }),
    () => ({
      refresh: () => {
        authRefreshStarted = true
        return options.server ? Promise.resolve(true) : never
      },
      setUser: (user: unknown) => {
        guestInitialized = user === null
      }
    }),
    () => ({ applyStoredLanguage: async () => {} }),
    () => ({ path: options.routePath || '/' }),
    () => ({ routeId: (path: string) => path.startsWith('/control-panel') ? '/' : null }),
    () => ({ cachedTabNames: ref([]) }),
    (_key: string, loader: () => Promise<unknown>) => {
      loaderStarted = true
      return loader()
    },
    (factory: () => { titleTemplate?: (title?: string) => string }) => {
      appHeadFactory = factory
    },
    (callback: () => void | Promise<void>) => {
      mountedCallbacks.push(callback)
    },
    () => {},
    () => ({
      links: ref([]),
      restoreLastPublic: () => false,
      refresh: async () => {
        themeSkinRefreshStarted = true
      },
      clear: () => {
        themeSkinCleared = true
      }
    }),
    () => ({ cookie: options.cookie || '' }),
    () => ({
      consumeFromRoute: () => {}
    }),
    () => ({ preview: ref(null) }),
    () => ({
      appliedAppearanceTheme: ref({ dataTheme: 'pine_teal', style: '' }),
      appliedLightBackground: ref('pure_white'),
      savedUserAppearance: ref(null),
      userAppearancePreview: ref(null)
    }),
    () => ({
      links: ref([]),
      refresh: async () => {
        publicContentStylesRefreshStarted = true
      },
      clear: () => {
        publicContentStylesCleared = true
      }
    }),
    computed,
    {},
    // 庆祝浮卡/徽章弹窗宿主不是启动流程的一部分：给空壳，断言只看启动动作是否发生。
    {},
    {}
  )

  return {
    component,
    loaderStarted: () => loaderStarted,
    webOptionsRefreshStarted: () => webOptionsRefreshStarted,
    authRefreshStarted: () => authRefreshStarted,
    guestInitialized: () => guestInitialized,
    themeSkinRefreshStarted: () => themeSkinRefreshStarted,
    themeSkinCleared: () => themeSkinCleared,
    publicContentStylesRefreshStarted: () => publicContentStylesRefreshStarted,
    publicContentStylesCleared: () => publicContentStylesCleared,
    resolveDocumentTitle: (title?: string) => {
      const template = appHeadFactory?.().titleTemplate
      if (!template) throw new Error('app title template was not registered')
      return template(title)
    },
    runMounted: async () => {
      for (const callback of mountedCallbacks) {
        void callback()
      }
      await Promise.resolve()
    }
  }
}
