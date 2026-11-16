import { computed, ref, toValue, type MaybeRefOrGetter } from 'vue'
import type { AltchaWidgetElement } from 'altcha'
import type { AltchaWidgetSettings } from '~/composables/useWebOptions'
import { apiErrorFields, apiErrorReason } from '~/composables/useApiClient'
import type { ForumHumanVerificationInput } from '~/utils/forum/forumTaxonomy'

/**
 * post_risk（发帖/回帖）人机验证的前端状态机。
 *
 * 与登录 / 注册 / 邮箱验证共用同一套 ALTCHA 形态：challenge 端点带 `purpose=post_risk`，
 * 解出的 payload 原样作为 `humanVerification.token` 回传，失败 reason 也是同一组
 * `human_verification.*`（后端 `apphttp.MapHumanVerificationError` 是唯一映射点）。
 *
 * 这里只放与框架无关的状态与请求体构造，Nuxt 侧由 `usePostRiskVerification` 注入
 * `useI18n` / `useWebOptions` / `useApiClient` —— 这样 happy-dom 单测能跑真实逻辑，
 * 而不是匹配源码字符串。
 */

export const POST_RISK_SCENARIO = 'post_risk'
export const POST_RISK_PROVIDER = 'altcha'
export const POST_RISK_FIELD = 'humanVerification'

/** 后端 reason → 本地化文案键，与 `utils/identity/registerErrors.ts` 用同一组文案。 */
const POST_RISK_MESSAGE_KEYS: Record<string, string> = {
  'human_verification.required': 'errors.humanVerificationRequired',
  'human_verification.invalid': 'errors.humanVerificationInvalid',
  'human_verification.expired': 'errors.humanVerificationExpired',
  'human_verification.replayed': 'errors.humanVerificationReplayed',
  'rate_limit.exceeded': 'errors.rateLimited'
}

export function postRiskChallengeUrl(apiBaseUrl: string) {
  return `${apiBaseUrl}/human-verification/challenge?purpose=${POST_RISK_SCENARIO}`
}

export function postRiskAltchaConfiguration(settings: AltchaWidgetSettings) {
  return JSON.stringify({
    hideLogo: settings.hideLogo,
    hideFooter: settings.hideFooter,
    minDuration: settings.minDuration
  })
}

export function postRiskMessageKey(code: string) {
  return POST_RISK_MESSAGE_KEYS[code] || 'errors.humanVerificationInvalid'
}

/** 这些 reason 都意味着「人机验证这条链路拦下了请求」，必须由本模块呈现。 */
export function isPostRiskVerificationReason(reason: string) {
  return reason.startsWith('human_verification.') || reason === 'rate_limit.exceeded'
}

/**
 * 请求体片段：开关关闭时返回 `undefined`。
 *
 * 调用方必须让字段**整个不出现**，而不是发空对象或空字符串：后端对关闭场景零校验，
 * 但半截字段会污染审计日志与 OpenAPI 契约语义。
 */
export function postRiskVerificationPayload(
  enabled: boolean,
  token: string
): ForumHumanVerificationInput | undefined {
  return enabled ? { provider: POST_RISK_PROVIDER, token } : undefined
}

/** 只在真的拿到 payload 时挂字段，避免 `humanVerification: undefined` 这类半截键。 */
export function withHumanVerification<T extends Record<string, unknown>>(
  body: T,
  humanVerification?: ForumHumanVerificationInput
): T & { humanVerification?: ForumHumanVerificationInput } {
  return humanVerification ? { ...body, humanVerification } : body
}

/**
 * 可直接展开进请求体的字段片段：开关关闭时是**空对象**，键根本不存在。
 *
 * 与 `postRiskVerificationPayload` 的分工：那个返回 `undefined`（供 API 层判断要不要挂字段），
 * 这个给调用点用 `...postRiskField()` 展开——这样连中间 DTO 里都不会出现
 * `humanVerification: undefined`，请求体「没有这个键」在每一层都成立。
 */
export function postRiskVerificationField(
  enabled: boolean,
  token: string
): { humanVerification?: ForumHumanVerificationInput } {
  const payload = postRiskVerificationPayload(enabled, token)
  return payload ? { humanVerification: payload } : {}
}

export type PostRiskVerificationDeps = {
  /** 运营是否开启该场景（provider=altcha 且 `human_verification.scenarios.post_risk`）。 */
  scenarioEnabled: MaybeRefOrGetter<boolean>
  widgetSettings: MaybeRefOrGetter<AltchaWidgetSettings>
  apiBaseUrl: MaybeRefOrGetter<string>
  locale: MaybeRefOrGetter<string>
  translate: (key: string) => string
}

export type PostRiskVerificationController = ReturnType<typeof createPostRiskVerification>

export function createPostRiskVerification(deps: PostRiskVerificationDeps) {
  // 渲染 widget 的两个来源：运营开关，或服务端明确要求（422 human_verification.required）。
  // 后者是双保险——即使开关在页面打开后才下发/前端没提前渲染，也不能让用户卡在无解的死路。
  const serverRequired = ref(false)
  const token = ref('')
  const widget = ref<AltchaWidgetElement | null>(null)
  const fieldMessage = ref('')

  const scenarioEnabled = computed(() => toValue(deps.scenarioEnabled))
  const enabled = computed(() => scenarioEnabled.value || serverRequired.value)
  const widgetSettings = computed(() => toValue(deps.widgetSettings))
  const challengeUrl = computed(() => postRiskChallengeUrl(toValue(deps.apiBaseUrl)))
  const configuration = computed(() => postRiskAltchaConfiguration(widgetSettings.value))
  const language = computed(() => toValue(deps.locale) === 'zh-CN' ? 'zh-cn' : 'en')

  function handleVerified(event: Event) {
    const detail = (event as CustomEvent<{ payload?: string }>).detail
    token.value = detail?.payload || ''
  }

  function handleStateChange(event: Event) {
    const detail = (event as CustomEvent<{ state?: string, payload?: string }>).detail
    if (detail?.state === 'verified' && detail.payload) {
      token.value = detail.payload
      return
    }
    if (detail?.state === 'expired' || detail?.state === 'error' || detail?.state === 'unverified') {
      token.value = ''
    }
  }

  function reset() {
    token.value = ''
    widget.value?.reset()
  }

  function payload() {
    return postRiskVerificationPayload(enabled.value, token.value)
  }

  /** 请求体片段（可展开）：开关关闭时是空对象，调用点不会留下半截键。 */
  function field() {
    return postRiskVerificationField(enabled.value, token.value)
  }

  /**
   * 接管人机验证失败：返回 true 表示错误已由本模块呈现（调用方不要再叠加通用提示）。
   *
   * token 是单次使用的：只要本次请求带过 token 且被拒，就必须重置 widget 重新取题，
   * 否则用户会在一个已消费的 token 上反复失败。
   */
  function capture(error: unknown) {
    const reason = apiErrorReason(error)
    if (!isPostRiskVerificationReason(reason)) {
      return false
    }
    const code = apiErrorFields(error)[POST_RISK_FIELD]?.[0] || reason
    fieldMessage.value = deps.translate(postRiskMessageKey(code))
    if (reason.startsWith('human_verification.')) {
      serverRequired.value = true
    }
    if (token.value) {
      reset()
    }
    return true
  }

  /** 成功提交后清账：widget 需要重新取题，下一次发布不会复用已消费的 token。 */
  function settle() {
    reset()
    fieldMessage.value = ''
    serverRequired.value = false
  }

  function dismissMessage() {
    fieldMessage.value = ''
  }

  return {
    enabled,
    scenarioEnabled,
    token,
    widget,
    fieldMessage,
    widgetSettings,
    challengeUrl,
    configuration,
    language,
    handleVerified,
    handleStateChange,
    reset,
    payload,
    field,
    capture,
    settle,
    dismissMessage
  }
}
