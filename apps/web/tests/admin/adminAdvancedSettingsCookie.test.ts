import { afterEach, beforeEach, describe, expect, test } from 'bun:test'
import { computed } from 'vue'

import { useAdminAdvancedSettings } from '../../app/composables/admin/useAdminAdvancedSettings'
import {
  adminPageDefinitions,
  shouldShowAdminPageInNav
} from '../../app/config/adminModules'

/**
 * FE2-02 回归：专业 / 运维模式开关必须扛得住刷新。
 *
 * 审计的怀疑是「cookie 写入与读取判据不一致」。实测结论是**现状已正确**：
 * Nuxt 的 `useCookie` 对字符串值做 JSON 编码，所以写入 `'1'` 落盘是 `%221%22`，
 * 读回再 JSON.parse 得到字符串 `'1'` —— 与 `parseFlagCookie` 的判据一致。
 * 这里把这条真实编解码契约钉死：谁把落盘格式改回裸 `1`、或者把读判据改成只认
 * `'true'`，这套用例都会红。
 *
 * 编解码照抄 nuxt@4.4.8 的真实实现
 * （node_modules/nuxt/dist/app/composables/cookie.js 的 `CookieDefaults` 与 `parseCookieValue`）。
 */

function parseCookieValue(value: string) {
  if (value === 'undefined') {
    return undefined
  }
  try {
    const parsed = JSON.parse(value)
    if (typeof parsed === 'number' && String(parsed) !== value) {
      return value
    }
    return parsed
  } catch {
    return value
  }
}

/** CookieDefaults.encode：非字符串或「JSON 解析后不是字符串」的值都按 JSON 序列化。 */
function nuxtEncode(value: unknown) {
  if (typeof value !== 'string' || value === 'undefined') {
    return encodeURIComponent(JSON.stringify(value))
  }
  try {
    if (typeof JSON.parse(value) !== 'string') {
      return encodeURIComponent(JSON.stringify(value))
    }
  } catch {
    // 不是 JSON：按原样编码（走下面的 return）。
  }
  return encodeURIComponent(value)
}

/** CookieDefaults.decode。 */
function nuxtDecode(raw: string) {
  return raw ? parseCookieValue(decodeURIComponent(raw)) : raw
}

const PROFESSIONAL = 'sforum-admin-professional-mode'
const OPERATIONS = 'sforum-admin-operations-mode'

function readRawCookie(name: string) {
  const entry = document.cookie.split('; ').find(part => part.startsWith(`${name}=`))
  return entry ? entry.slice(name.length + 1) : undefined
}

function writeRawCookie(name: string, encoded: string) {
  document.cookie = `${name}=${encoded}; Path=/; Max-Age=31536000`
}

function clearCookies() {
  for (const entry of document.cookie.split('; ')) {
    const name = entry.split('=')[0]
    if (name) {
      document.cookie = `${name}=; Path=/; Max-Age=0`
    }
  }
}

type CookieSource = 'client' | 'ssr'

/**
 * 最小 useCookie：读写都经过上面的真实编解码。
 * - `client`：值来自 `document.cookie`（浏览器水合）。
 * - `ssr`：值来自请求头 Cookie（服务端首帧），写入也只是记账不回写 document。
 */
function installCookieImplementation(source: CookieSource, requestCookieHeader = '') {
  const globals = globalThis as typeof globalThis & Record<string, unknown>
  const written: string[] = []

  globals.computed = computed
  globals.useCookie = (name: string, options: { default?: () => unknown } = {}) => {
    const raw = source === 'client'
      ? readRawCookie(name)
      : (() => {
          const entry = requestCookieHeader.split('; ').find(part => part.startsWith(`${name}=`))
          return entry ? entry.slice(name.length + 1) : undefined
        })()
    const initial = raw === undefined ? options.default?.() : nuxtDecode(raw)

    // 用可变对象模拟 cookie ref：computed 的 get 读取它，set 同时落盘并记住写出的原始串。
    const state = { value: initial as unknown }
    return computed({
      get: () => state.value,
      set: (value: unknown) => {
        state.value = value
        const encoded = nuxtEncode(value)
        written.push(encoded)
        if (source === 'client') {
          writeRawCookie(name, encoded)
        }
      }
    })
  }

  return { written }
}

afterEach(() => {
  clearCookies()
})

beforeEach(() => {
  clearCookies()
})

describe('FE2-02 admin advanced mode cookies', () => {
  test('writes the JSON-quoted flag Nuxt can decode, not a bare `1`', () => {
    const { written } = installCookieImplementation('client')
    const settings = useAdminAdvancedSettings()

    expect(settings.professionalMode.value).toBe(false)

    settings.professionalMode.value = true

    // 落盘格式是 Nuxt 编码后的 `%221%22`（decodeURIComponent = `"1"`）。
    expect(written).toEqual(['%221%22'])
    expect(readRawCookie(PROFESSIONAL)).toBe('%221%22')
    expect(decodeURIComponent(readRawCookie(PROFESSIONAL)!)).toBe('"1"')
    // 裸 `1` 会被 Nuxt 解成数字 1，正是这套判据真正怕的形态。
    expect(readRawCookie(PROFESSIONAL)).not.toBe('1')
  })

  test('survives a refresh: a fresh instance reads the same flag back as true', () => {
    installCookieImplementation('client')
    const beforeReload = useAdminAdvancedSettings()
    beforeReload.professionalMode.value = true
    beforeReload.operationsMode.value = true

    // 刷新 = 组件重装：新的 useCookie 实例从 document.cookie 重新解码。
    const afterReload = useAdminAdvancedSettings()
    expect(afterReload.professionalMode.value).toBe(true)
    expect(afterReload.operationsMode.value).toBe(true)

    afterReload.professionalMode.value = false
    expect(useAdminAdvancedSettings().professionalMode.value).toBe(false)
    expect(useAdminAdvancedSettings().operationsMode.value).toBe(true)
  })

  test('the SSR first frame decodes the same persisted value', () => {
    const { written } = installCookieImplementation('client')
    const client = useAdminAdvancedSettings()
    client.professionalMode.value = true
    const persisted = written[0]
    clearCookies()

    // 第二次请求：浏览器把 `%221%22` 放进 Cookie 头，服务端据此渲染首帧。
    installCookieImplementation('ssr', `${PROFESSIONAL}=${persisted}`)
    const ssr = useAdminAdvancedSettings()

    expect(ssr.professionalMode.value).toBe(true)
    expect(ssr.operationsMode.value).toBe(false)
  })

  test('the decoded flag is what the admin layout feeds into navigation visibility', () => {
    installCookieImplementation('client')
    const settings = useAdminAdvancedSettings()
    settings.professionalMode.value = true
    settings.operationsMode.value = true

    const reloaded = useAdminAdvancedSettings()
    const visibility = {
      professionalMode: reloaded.professionalMode.value,
      operationsMode: reloaded.operationsMode.value
    }
    const allowAll = () => true

    // admin.vue:209-217 的 navVisibility 就是这两个布尔；专业页 / 运维页必须真的出现。
    const professionalPage = adminPageDefinitions.find(page => page.id === '/settings/features')!
    const operationsPage = adminPageDefinitions.find(page => page.id === '/jobs')!
    expect(shouldShowAdminPageInNav(professionalPage, allowAll, visibility)).toBe(true)
    expect(shouldShowAdminPageInNav(operationsPage, allowAll, visibility)).toBe(true)
    expect(shouldShowAdminPageInNav(operationsPage, allowAll, { ...visibility, operationsMode: false })).toBe(false)
  })
})
