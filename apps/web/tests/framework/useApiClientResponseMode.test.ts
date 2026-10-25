import { describe, expect, test } from 'bun:test'
import { ref } from 'vue'

import {
  apiErrorMessage,
  apiErrorReason,
  useApiClient
} from '../../app/composables/useApiClient'

// 宿主路由返回 { code, message, data } 信封；扩展路由返回自己声明的裸 JSON。
// 两个分支都必须是显式选择，不能靠「body 里有没有 data 键」去猜。
describe('useApiClient response modes', () => {
  test('unwraps the Host envelope by default', async () => {
    const csrfCookie = ref('token')

    await withApiClientGlobals(csrfCookie, async () => {
      globalThis.$fetch = async () => ({
        code: 200,
        message: 'ok',
        data: { items: [{ id: 'host-item' }], total: 1 }
      })

      const { request } = useApiClient()

      await expect(request('/admin/extensions')).resolves.toEqual({
        items: [{ id: 'host-item' }],
        total: 1
      })
    })
  })

  test("returns the parsed response body itself in 'body' mode", async () => {
    const csrfCookie = ref('token')
    // 形状取自 sforum.store 的 catalog.response.json：顶层没有 data 键。
    const catalogBody = {
      compatibility: { coreVersion: '1.0.0', compatible: true },
      fetchedAt: '2026-10-25T12:00:00Z',
      items: [{ id: 'acme.plugin' }, { id: 'acme.theme' }],
      nextCursor: '',
      stale: false,
      target: 'core',
      total: 2
    }

    await withApiClientGlobals(csrfCookie, async () => {
      globalThis.$fetch = async () => catalogBody

      const { request } = useApiClient()
      const body = await request('/extensions/acme.store/api/store/catalog', { responseMode: 'body' })

      // 同一个对象：不是 envelope.data（那会是 undefined），也不是浅拷贝。
      expect(body).toBe(catalogBody)
      expect(body).not.toBeUndefined()
    })
  })

  test("keeps CSRF, timeout, and error mapping unchanged in 'body' mode", async () => {
    const csrfCookie = ref('token')
    const calls: Array<{ url: string, headers?: Record<string, string>, timeout?: number, method?: string }> = []
    const rawBody = { staged: true, extensionId: 'acme.plugin' }

    await withApiClientGlobals(csrfCookie, async () => {
      globalThis.$fetch = async (url: string, options?: {
        method?: string
        headers?: Record<string, string>
        timeout?: number
      }) => {
        calls.push({ url, headers: options?.headers, timeout: options?.timeout, method: options?.method })
        return rawBody
      }

      const { request } = useApiClient()

      await expect(request('/extensions/acme.store/api/store/install-url', {
        method: 'POST',
        body: { url: 'https://example.test/acme.zip' },
        responseMode: 'body'
      })).resolves.toBe(rawBody)
    })

    expect(calls).toHaveLength(1)
    expect(calls[0]?.url).toBe('/api/v1/extensions/acme.store/api/store/install-url')
    expect(calls[0]?.method).toBe('POST')
    // double-submit：cookie 值 == X-Csrf-Token header，body 模式不得绕过。
    expect(calls[0]?.headers?.['X-Csrf-Token']).toBe('token')
    // unsafe 方法仍是 30s 兜底超时。
    expect(calls[0]?.timeout).toBe(30000)
  })

  test('still rejects and maps non-2xx errors in body mode', async () => {
    const csrfCookie = ref('token')

    await withApiClientGlobals(csrfCookie, async () => {
      globalThis.$fetch = async () => {
        throw {
          data: {
            code: 403,
            message: '插件未授权。',
            data: { reason: 'extension.trust_required' }
          }
        }
      }

      const { request } = useApiClient()
      const error = await request('/extensions/acme.store/api/store/catalog', { responseMode: 'body' })
        .then(() => undefined, (rejection: unknown) => rejection)

      // 失败语义不变：原样抛出，错误信封仍可被宿主 helper 读出 reason。
      expect(error).toBeDefined()
      expect(apiErrorReason(error)).toBe('extension.trust_required')
      expect(apiErrorMessage(error)).toBe('插件未授权。')
    })
  })
})

async function withApiClientGlobals(csrfCookie: { value: string }, run: () => Promise<void>) {
  const originalFetch = globalThis.$fetch
  const originalUseRuntimeConfig = globalThis.useRuntimeConfig
  const originalUseNuxtApp = globalThis.useNuxtApp
  const originalUseCookie = globalThis.useCookie
  const originalUseState = globalThis.useState

  globalThis.useRuntimeConfig = () => ({
    public: {
      apiBaseUrl: '/api/v1',
      appLocale: 'zh-CN'
    }
  })
  globalThis.useNuxtApp = () => ({
    $i18n: {
      locale: ref('zh-CN')
    }
  })
  globalThis.useCookie = (name: string) => {
    if (name !== 'csrf_') {
      throw new Error(`unexpected cookie ${name}`)
    }
    return csrfCookie
  }

  try {
    await run()
  } finally {
    globalThis.$fetch = originalFetch
    globalThis.useRuntimeConfig = originalUseRuntimeConfig
    globalThis.useNuxtApp = originalUseNuxtApp
    globalThis.useCookie = originalUseCookie
    globalThis.useState = originalUseState
  }
}
