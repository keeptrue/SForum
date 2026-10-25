import { notifyApiConnectionError } from './useApiConnectionError'
import { isApiConnectionError } from './errors/apiConnectionErrorClassify'

export type ApiEnvelope<T> = {
  code: number
  message: string
  data: T
}

export type ApiErrorData = {
  reason?: string
  fields?: Record<string, string[]>
  retryAfterSeconds?: number
  retryAt?: string
  details?: Record<string, unknown>
}

type ApiRequestBody = BodyInit | Record<string, unknown> | null

/**
 * 响应体解包模式。宿主自己的路由统一返回 `{ code, message, data }` 信封，
 * 但扩展（插件）路由返回的是它自己声明的裸 JSON，没有信封可解。
 * 用显式开关区分两条契约，不要按「body 里有没有 data 键」去猜。
 */
export type ApiResponseMode = 'data' | 'body'

type ApiFetchOptions = {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE'
  body?: ApiRequestBody
  credentials?: RequestCredentials
  headers?: Record<string, string>
  timeout?: number
  /** Nuxt 错误渲染等上下文必须绕过相对 URL，直连服务端 API。 */
  serverInternal?: boolean
  /**
   * `'data'`（默认）解包宿主信封后返回 `envelope.data`；
   * `'body'` 直接返回解析后的响应体本身（扩展路由的裸 JSON）。
   * 只影响成功响应的返回值，CSRF、超时、错误映射等传输行为完全一致。
   */
  responseMode?: ApiResponseMode
}

const UNSAFE_METHODS = new Set(['POST', 'PATCH', 'PUT', 'DELETE'])
// 未显式传 timeout 的兜底：面向慢站点放宽——慢而活的响应不再被短超时误杀，
// 也不会长时间悬挂后才报 timeout 触发误报（连接失败仍走缓冲弹窗逻辑）。
const DEFAULT_SAFE_TIMEOUT_MS = 15000
const DEFAULT_UNSAFE_TIMEOUT_MS = 30000
const CSRF_COOKIE_NAME = 'csrf_'
const CSRF_HEADER_NAME = 'X-Csrf-Token'
const CSRF_PRIME_PATH = '/health'

type ApiErrorEnvelopeLike = {
  code?: unknown
  message?: unknown
  data?: unknown
}

type ApiFetchErrorLike = {
  data?: unknown
  status?: unknown
  statusCode?: unknown
  response?: {
    status?: unknown
    _data?: unknown
  }
}

type RuntimeI18nLike = {
  locale?: unknown
}

export function useApiClient() {
  const runtimeConfig = useRuntimeConfig()
  const apiBaseUrl = runtimeConfig.public.apiBaseUrl as string
  const i18n = useNuxtApp().$i18n as RuntimeI18nLike | undefined
  // SSR 重试可能跨过 await；请求 cookie 必须在 composable 创建时从当前上下文捕获。
  const requestCookie = import.meta.server
    ? (useRequestHeaders(['cookie']).cookie || '')
    : ''

  function apiLocale() {
    const locale = localeString(i18n?.locale) || String(runtimeConfig.public.appLocale || 'zh-CN')
    return locale === 'en' ? 'en-US' : locale
  }

  function apiHeaders(extra?: Record<string, string>): Record<string, string> {
    const headers: Record<string, string> = {
      'Accept-Language': apiLocale()
    }
    if (requestCookie) {
      headers.cookie = requestCookie
    }
    return { ...headers, ...extra }
  }

  // csrfToken 读取后端在 GET 请求种下的 csrf_ cookie：
  // client 端用 useCookie 读浏览器 cookie；server 端（SSR）从透传的请求 cookie 头解析。
  // 返回空字符串表示无 token（首次访问尚未种下，unsafe 请求会被后端拒绝，正常流程先有 GET）。
  function csrfToken(): string {
    if (import.meta.server) {
      for (const part of requestCookie.split(';')) {
        const [k, ...rest] = part.trim().split('=')
        if (k === CSRF_COOKIE_NAME) {
          return decodeURIComponent(rest.join('='))
        }
      }
      return ''
    }
    return useCookie<string>(CSRF_COOKIE_NAME).value || ''
  }

  async function refreshCsrfToken() {
    if (import.meta.server) {
      return csrfToken()
    }

    await $fetch<ApiEnvelope<unknown>>(`${apiBaseUrl}${CSRF_PRIME_PATH}`, {
      credentials: 'include',
      headers: apiHeaders(),
      timeout: 2000
    })

    return csrfToken()
  }

  async function request<T>(path: string, options: ApiFetchOptions = {}) {
    const method = options.method?.toUpperCase()
    const unsafe = Boolean(method && UNSAFE_METHODS.has(method))
    const callerProvidedCsrf = Boolean(options.headers?.[CSRF_HEADER_NAME])
    const canRefreshCsrf = !import.meta.server

    async function requestHeaders(refreshToken: boolean) {
      // 首次进入页面或 token 过期时，先用安全 GET 让后端种/刷新 double-submit cookie。
      if (canRefreshCsrf && unsafe && !callerProvidedCsrf && (refreshToken || !csrfToken())) {
        await refreshCsrfToken()
      }

      const headers = apiHeaders(options.headers)
      // unsafe 方法必须携带 CSRF token（double-submit：cookie 值 == X-Csrf-Token header）。
      if (unsafe && !callerProvidedCsrf) {
        const token = csrfToken()
        if (token) {
          headers[CSRF_HEADER_NAME] = token
        }
      }
      return headers
    }

    async function send(refreshToken = false) {
      const headers = await requestHeaders(refreshToken)
      const requestBaseUrl = import.meta.server && options.serverInternal
        ? (process.env.NUXT_API_INTERNAL_BASE_URL || 'http://api:8080/api/v1')
        : apiBaseUrl
      const payload = await $fetch<ApiEnvelope<T> | T>(`${requestBaseUrl.replace(/\/+$/, '')}${path}`, {
        method: options.method,
        body: options.body,
        credentials: options.credentials ?? 'include',
        headers,
        timeout: options.timeout ?? (unsafe ? DEFAULT_UNSAFE_TIMEOUT_MS : DEFAULT_SAFE_TIMEOUT_MS)
      })

      if (options.responseMode === 'body') {
        // 扩展路由的裸 JSON：整个响应体就是调用方要的数据，没有信封可解。
        return payload as T
      }

      return (payload as ApiEnvelope<T>).data
    }

    try {
      return await send()
    } catch (error) {
      if (canRefreshCsrf && unsafe && !callerProvidedCsrf && apiErrorReason(error) === 'csrf.invalid') {
        try {
          return await send(true)
        } catch (retryError) {
          notifyApiConnectionError(retryError, path, apiBaseUrl)
          throw retryError
        }
      }
      // 安全方法（GET/未指定 method）连接类失败做一次原地重试：抖动在第二次
      // 即恢复时完全不进入弹窗缓冲。unsafe 方法保持单次，避免重复提交。
      if (!unsafe && canRetryInPlace(error)) {
        try {
          return await send()
        } catch (retryError) {
          notifyApiConnectionError(retryError, path, apiBaseUrl)
          throw retryError
        }
      }
      notifyApiConnectionError(error, path, apiBaseUrl)
      throw error
    }
  }

  return { apiBaseUrl, apiHeaders, request }
}

function localeString(value: unknown) {
  if (typeof value === 'string') {
    return value
  }
  if (value && typeof value === 'object' && 'value' in value) {
    const refValue = (value as { value?: unknown }).value
    return typeof refValue === 'string' ? refValue : ''
  }
  return ''
}

function isApiEnvelope(value: unknown): value is ApiEnvelope<ApiErrorData> {
  if (!value || typeof value !== 'object') {
    return false
  }

  const envelope = value as ApiErrorEnvelopeLike
  return typeof envelope.code === 'number' && typeof envelope.message === 'string'
}

function apiErrorEnvelope(error: unknown) {
  if (isApiEnvelope(error)) {
    return error
  }

  // ofetch/Nuxt 在不同链路里可能把后端 envelope 放在 data 或 response._data。
  const fetchError = error && typeof error === 'object'
    ? error as ApiFetchErrorLike
    : {}
  if (isApiEnvelope(fetchError.data)) {
    return fetchError.data
  }
  if (isApiEnvelope(fetchError.response?._data)) {
    return fetchError.response._data
  }

  return undefined
}

export function apiErrorMessage(error: unknown) {
  return apiErrorEnvelope(error)?.message || ''
}

export function apiErrorReason(error: unknown) {
  const reason = apiErrorEnvelope(error)?.data?.reason
  return typeof reason === 'string' ? reason : ''
}

/** 读取错误信封里的结构化诊断载荷（如卸载清理失败的 operationId/failedStep）。 */
export function apiErrorDetails(error: unknown): Record<string, unknown> | undefined {
  const details = apiErrorEnvelope(error)?.data?.details
  return details && typeof details === 'object' && !Array.isArray(details)
    ? details as Record<string, unknown>
    : undefined
}

export function apiErrorRetryAfterSeconds(error: unknown) {
  const value = Number(apiErrorEnvelope(error)?.data?.retryAfterSeconds)
  return Number.isFinite(value) && value > 0 ? Math.ceil(value) : 0
}

export function apiErrorRetryAt(error: unknown) {
  const value = apiErrorEnvelope(error)?.data?.retryAt
  return typeof value === 'string' ? value : ''
}

/** 统一读取 ofetch 与 Host envelope 的 HTTP 状态，供只读请求策略复用。 */
export function apiErrorStatusCode(error: unknown) {
  const fetchError = error && typeof error === 'object'
    ? error as ApiFetchErrorLike
    : {}
  const candidates = [
    apiErrorEnvelope(error)?.code,
    fetchError.statusCode,
    fetchError.status,
    fetchError.response?.status
  ]
  for (const value of candidates) {
    const status = Number(value)
    if (Number.isInteger(status) && status >= 400 && status <= 599) {
      return status
    }
  }
  return 0
}

export function apiErrorFields(error: unknown) {
  const fields = apiErrorEnvelope(error)?.data?.fields
  if (!fields || typeof fields !== 'object') {
    return {}
  }

  return Object.fromEntries(
    Object.entries(fields).filter(([, messages]) => Array.isArray(messages))
  ) as Record<string, string[]>
}

/** 连接类失败且非调用方主动 abort 才值得原地重试；当前调用方尚未接入 AbortSignal，该分支为契约预留。 */
function canRetryInPlace(error: unknown) {
  if (!isApiConnectionError(error)) {
    return false
  }
  const name = (error as { name?: unknown } | null)?.name
  return name !== 'AbortError'
}
