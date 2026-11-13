import { afterEach, beforeEach, describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { computed, reactive, ref } from 'vue'
import { contributionCatalogViewState } from '../../app/utils/admin/adminContributions'
import { useAdminContributionCatalog } from '../../app/composables/admin/useAdminContributionCatalog'
import { useApiClient } from '../../app/composables/useApiClient'

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
const page = () => source('../../app/pages/admin/extensions/contributions.vue')
const catalog = () => source('../../app/composables/admin/useAdminContributionCatalog.ts')
const zhCN = () => JSON.parse(source('../../i18n/locales/zh-CN.json'))
const enUS = () => JSON.parse(source('../../i18n/locales/en-US.json'))

type FetchCall = { url: string, options?: Record<string, unknown> }

function installNuxtGlobals(fetcher: (url: string) => Promise<unknown>) {
  const globals = globalThis as unknown as Record<string, unknown>
  const saved = {
    $fetch: globals.$fetch,
    useRuntimeConfig: globals.useRuntimeConfig,
    useNuxtApp: globals.useNuxtApp,
    useCookie: globals.useCookie,
    useState: globals.useState,
    ref: globals.ref,
    computed: globals.computed,
    reactive: globals.reactive,
    useApiClient: globals.useApiClient
  }
  globals.ref = ref
  globals.computed = computed
  globals.reactive = reactive
  globals.useRuntimeConfig = () => ({ public: { apiBaseUrl: '/api/v1', appLocale: 'zh-CN' } })
  globals.useNuxtApp = () => ({ $i18n: { locale: ref('zh-CN') } })
  // Nuxt 的自动导入在 bun 里不存在：把真实实现挂到全局，测的才是生产代码路径。
  globals.useApiClient = useApiClient
  globals.useCookie = () => ref('token')
  globals.useState = (_key: string, init: () => unknown) => ref(init())
  globals.$fetch = async (url: string, options?: Record<string, unknown>) => fetcher(url)
  return () => {
    for (const [key, value] of Object.entries(saved)) {
      globals[key] = value
    }
  }
}

function envelopeError(message: string) {
  return Object.assign(new Error(message), {
    data: { code: 500, message, data: { reason: 'internal.error' } }
  })
}

describe('contribution catalog view state', () => {
  test('error and empty are different states — a failed load never looks like an empty list', () => {
    expect(contributionCatalogViewState('loading', 0)).toBe('loading')
    expect(contributionCatalogViewState('error', 0)).toBe('error')
    expect(contributionCatalogViewState('error', 5)).toBe('error')
    expect(contributionCatalogViewState('ready', 0)).toBe('empty')
    expect(contributionCatalogViewState('ready', 3)).toBe('list')
    expect(contributionCatalogViewState('idle', 0)).toBe('idle')
  })
})

describe('useAdminContributionCatalog', () => {
  let restore: (() => void) | undefined

  beforeEach(() => {
    restore = undefined
  })

  afterEach(() => {
    restore?.()
  })

  test('a failing load surfaces errorMessage and keeps the previous rows', async () => {
    let fail = false
    const points = [{ id: 'admin.settings.tabs', owner: 'admin', kind: 'descriptor', description: '', payloadType: 'settingsSection' }]
    const contributions = [{ extensionId: 'demo', extensionName: 'Demo', extensionType: 'plugin', point: 'admin.settings.tabs', id: 'a', order: 10 }]
    restore = installNuxtGlobals(async (url) => {
      if (fail) throw envelopeError('后端挂了')
      return { code: 200, message: 'ok', data: url.includes('contribution-points') ? points : contributions }
    })

    const state = useAdminContributionCatalog()
    await state.load()
    expect(state.status.value).toBe('ready')
    expect(state.viewState.value).toBe('list')
    expect(state.contributions.value).toHaveLength(1)

    fail = true
    await state.load()
    expect(state.status.value).toBe('error')
    expect(state.failed.value).toBe(true)
    expect(state.errorMessage.value).toBe('后端挂了')
    expect(state.viewState.value).toBe('error')
    // 失败不清空：旧数据仍可读，运营据错误提示重试即可。
    expect(state.contributions.value).toHaveLength(1)

    fail = false
    await state.load()
    expect(state.status.value).toBe('ready')
    expect(state.errorMessage.value).toBe('')
  })
})

describe('contributions page renders the failure, not a fake empty state', () => {
  test('page consumes the explicit catalog state', () => {
    const src = page()
    expect(src).toContain('useAdminContributionCatalog()')
    // 不再走「catch 成空列表」的管理器加载器
    expect(src).not.toContain('loadContributions()')
    expect(src).not.toContain('loadContributionPoints()')
    expect(src).toContain('v-if="failed"')
    expect(src).toContain('data-testid="contributions-load-error"')
    expect(src).toContain("t('admin.extensions.contributions.retry')")
    expect(src).toContain(':loading="pending"')
    // 空态只在 ready 且真的为空时出现
    expect(src.match(/v-if="viewState === 'empty'"/g)?.length).toBe(2)
    expect(catalog()).toContain("status.value = 'error'")
    expect(catalog()).toContain('status.value = \'ready\'')
  })

  test('the loader no longer swallows failures into an empty array', () => {
    const src = catalog()
    expect(src).toContain('catch (error)')
    expect(src).toContain('errorMessage.value = apiErrorMessage(error)')
    // 没有「catch → []」这种把失败伪装成空列表的写法
    expect(src).not.toMatch(/catch\s*\{\s*$/m)
  })

  test('failure copy exists in both locales', () => {
    for (const locale of [zhCN(), enUS()]) {
      const copy = locale.admin.extensions.contributions
      expect(copy.loadErrorTitle).toBeTruthy()
      expect(copy.loadErrorHint).toBeTruthy()
      expect(copy.retry).toBeTruthy()
    }
  })
})
