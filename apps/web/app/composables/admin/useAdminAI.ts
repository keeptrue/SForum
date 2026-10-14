import type { InjectionKey } from 'vue'

export type AdminAIProtocol = 'openai-chat' | 'anthropic-messages'
export type AdminAICostClass = 'economy' | 'standard' | 'premium'

export type AdminAIProfile = {
  id: string
  label: string
  protocol: AdminAIProtocol
  baseUrl: string
  apiKeyRef: string
  model: string
  costClass: AdminAICostClass
  enabled: boolean
  price: { inputPerMillionMicros: number, outputPerMillionMicros: number }
  defaults: { maxTokens: number, temperature?: number, timeoutMs: number }
  // 端点是否支持工具调用。缺省（undefined）视为支持；只有明确不支持时才写 false。
  supportsTools?: boolean
}

// AdminAIPurposeTools 是某个用途的工具白名单覆盖。字段存在（哪怕空列表）即表示
// 运营者接管了这一项；字段缺失表示沿用该用途声明的默认列表。
export type AdminAIPurposeTools = { allowed: string[] }

// AdminAIPurposeOverride 是某个用途的站点覆盖（与后端 ai.PurposeSettings 对应）。
export type AdminAIPurposeOverride = {
  tools?: AdminAIPurposeTools
  failurePosture?: string
}

// AdminAIValidatedPurpose 是控制台的用途目录条目：声明了什么，以及当前生效什么。
// declaredTools 与 toolsAllowed 并列，让「覆盖成了什么」与「原本是什么」都能看见。
export type AdminAIValidatedPurpose = {
  id: string
  label: { default: string, 'en-US'?: string }
  description: { default: string, 'en-US'?: string }
  costClass: AdminAICostClass
  resourceType: string
  promptVersion: string
  blastRadius: 'single' | 'batch' | 'site' | string
  failurePosture: string
  failurePostureOverridden: boolean
  declaredTools: string[]
  toolsAllowed: string[]
  toolsOverridden: boolean
}

export type AdminAISettings = {
  schemaVersion: string
  enabled: boolean
  profiles: AdminAIProfile[]
  costClassProfiles: Record<string, string>
  purposeFailurePosture: Record<string, string>
  // purposes 是按用途的覆盖。缺省即沿用各用途自己的声明；它是 reply.tools 特例的
  // 替代品（旧字段由服务端在归一化时迁移进来）。
  purposes?: Record<string, AdminAIPurposeOverride>
  // reply.systemPrompt 为空表示使用内置默认提示词；一键恢复就是把这里清空。
  // triggerMode 决定机器人何时回复：mention（默认）/ autonomous（AI 自主判定
  // 是否参与）/ off（停用自动触发）。旧文档可能没有这个字段。
  reply: { systemPrompt: string, triggerMode?: 'mention' | 'autonomous' | 'off' }
  autoAction: { enabled: boolean, confidenceThreshold: number, allowedActions: string[] }
  gates: { rateLimitPerMinute: number, extensionDailyQuota: number, userDailyQuota: number, monthlyBudgetMicros: number, toolCallsPerReply?: number }
  redaction: { enabled: boolean, redactEmail: boolean, redactPhone: boolean, redactIp: boolean }
  // multimodal 是图片理解开关。旧文档可能没有这个字段（引入前保存的）。
  multimodal?: { enabled: boolean }
  revision: number
  updatedAt?: string
}

// ReplyBotInfo 描述当前充当 AI 助手的账号。configured=false 表示站内还没有
// 机器人账号，回复功能不会生效。
export type AdminAIReplyBot = {
  userId?: number
  username?: string
  configured: boolean
}

export type AdminAISettingsPayload = {
  settings: AdminAISettings
  warnings: string[]
  encryptionEnabled: boolean
  credentialStatus: Record<string, boolean>
  replyBot: AdminAIReplyBot
  /** 站内已声明的 AI 用途清单（声明 + 当前生效参数）。 */
  purposes: AdminAIValidatedPurpose[]
  /** 内置回复提示词（不含安全尾注）。编辑器用它预填，让默认值可见。 */
  defaultSystemPrompt: string
  /** 始终附加在提示词末尾的防注入声明，只读展示。 */
  safetyAppendix: string
}

export type AdminAIUsageSnapshot = {
  minuteCalls: number
  dayCalls: number
  dayTokens: number
  monthSpendMicros: number
}

export type AdminAIUsagePayload = {
  usage: { site: AdminAIUsageSnapshot, extension: AdminAIUsageSnapshot, user: AdminAIUsageSnapshot }
  budgetMicros: number
  warnings: string[]
}

export type AdminAIExecution = {
  id: number
  createdAt: string
  purpose: string
  costClass: string
  callerExtensionId: string
  profileId: string
  protocol: string
  model: string
  status: 'succeeded' | 'failed' | 'denied'
  gateReason: string
  gateScope: string
  promptVersion: string
  configRevision: number
  latencyMs: number
  inputTokens: number
  outputTokens: number
  cachedTokens: number
  spendMicros: number
  cacheHit: boolean
  errorSummary: string
}

// AdminAIModelList 是一次只读的模型列表探测结论。供应商层面的失败以 reason + error
// 返回，而不是抛出：地址能连上但密钥无效，是运营者要读到的信息。
export type AdminAIModelList = {
  profileId?: string
  protocol?: string
  models: string[]
  count: number
  /** 供应商还有更多条目未展示（超过上限，或供应商自身分页未取完）。 */
  truncated?: boolean
  reason?: string
  error?: string
}

// AdminAIModelProbe 是探测参数。三个可选字段都只在这一次请求里存在：带上它们，
// 运营者才能在保存之前就看到模型列表；缺省时服务端使用已保存的地址与密钥。
export type AdminAIModelProbe = {
  profileId: string
  baseUrl?: string
  protocol?: AdminAIProtocol
  apiKey?: string
}

export type AdminAIDiagnoseResult = {
  ok: boolean
  providerId?: string
  model?: string
  protocol?: string
  /** 实际探测的成本等级绑定。运营者可能只绑定了其中一档，展示它避免误判。 */
  costClass?: string
  /** 模型返回的正文。成功但为空时是空字符串（不是缺失），界面据此提示而非渲染空白条。 */
  reply: string
  latencyMs?: number
  inputTokens?: number
  outputTokens?: number
  /** 协议无关的停止原因。正文为空时它解释「为什么一个字都没有」。 */
  stopReason?: string
  reason?: string
  error?: string
}

// MICRO_PER_UNIT 与后端 ai.MicroPerUnit 保持一致：1 个货币单位 = 1_000_000 micro。
export const MICRO_PER_UNIT = 1_000_000

// ADMIN_AI_KEY 让页面壳成为唯一的数据所有者：三个页签共用一个上下文，
// 而不是各自再调一次 useAsyncData。同一个 key 被多个组件使用时，Nuxt 会在
// 组件卸载时清理数据，页签切换因此可能出现空数据与白屏。
export type AdminAIContext = ReturnType<typeof useAdminAI>
export const ADMIN_AI_KEY: InjectionKey<AdminAIContext> = Symbol('sforum.admin.ai')

export function emptyAdminAISettings(): AdminAISettings {
  return {
    schemaVersion: 'sforum.ai.settings@1',
    enabled: false,
    profiles: [],
    costClassProfiles: {},
    purposeFailurePosture: {},
    reply: { systemPrompt: '', triggerMode: 'mention' },
    autoAction: { enabled: false, confidenceThreshold: 0.9, allowedActions: [] },
    gates: { rateLimitPerMinute: 0, extensionDailyQuota: 0, userDailyQuota: 0, monthlyBudgetMicros: 0, toolCallsPerReply: 3 },
    redaction: { enabled: true, redactEmail: true, redactPhone: true, redactIp: true },
    revision: 0
  }
}

export function emptyAdminAIUsage(): AdminAIUsageSnapshot {
  return { minuteCalls: 0, dayCalls: 0, dayTokens: 0, monthSpendMicros: 0 }
}

export function useAdminAI() {
  const { request } = useApiClient()
  const toast = useToast()
  const { t } = useI18n()
  const busy = ref('')

  const payload = useAsyncData<AdminAISettingsPayload>('admin-ai-settings', () => request('/admin/ai/settings'))
  const usage = useAsyncData<AdminAIUsagePayload>('admin-ai-usage', () => request('/admin/ai/usage'))
  const executions = useAsyncData<{ items: AdminAIExecution[] }>('admin-ai-executions', () => request('/admin/ai/executions?limit=50'))
  // 页面壳必须在 SSR 渲染前等这三个请求完成：useAdminAI 是同步组合式函数，若不
  // await，SSR 首渲染拿到的 settings/purposes/usage 全是空兜底，而客户端水合时
  // payload 已从 __NUXT_DATA__ 同步恢复——两边的分支（v-if/v-else、v-for 数量）
  // 全部错位，hydration mismatch 由此而来（所有 AI 页签都受影响，不只是用途页签）。
  const ready = Promise.all([payload, usage, executions]).then(() => undefined)

  // 请求失败时 data 是 undefined，而不是 default 的返回值：所有下游读取都必须
  // 经由这些经过保护的 computed，否则一次 401/500 就会让整页渲染抛错。
  const settings = computed(() => payload.data.value?.settings ?? emptyAdminAISettings())
  const credentialStatus = computed(() => payload.data.value?.credentialStatus ?? {})
  const warnings = computed(() => payload.data.value?.warnings ?? [])
  const encryptionEnabled = computed(() => payload.data.value?.encryptionEnabled ?? false)
  const replyBot = computed<AdminAIReplyBot>(() => payload.data.value?.replyBot ?? { configured: false })
  const purposes = computed<AdminAIValidatedPurpose[]>(() => payload.data.value?.purposes ?? [])
  const defaultSystemPrompt = computed(() => payload.data.value?.defaultSystemPrompt ?? '')
  const safetyAppendix = computed(() => payload.data.value?.safetyAppendix ?? '')
  const loadFailed = computed(() => Boolean(payload.error.value))
  const loading = computed(() => payload.pending.value)

  const siteUsage = computed(() => usage.data.value?.usage?.site ?? emptyAdminAIUsage())
  const budgetMicros = computed(() => usage.data.value?.budgetMicros ?? 0)
  const executions_ = computed(() => executions.data.value?.items ?? [])
  const executionsPending = computed(() => executions.pending.value)

  async function refresh() {
    await Promise.all([payload.refresh(), usage.refresh(), executions.refresh()])
  }

  async function saveSettings(next: AdminAISettings) {
    busy.value = 'settings'
    try {
      await request('/admin/ai/settings', { method: 'PUT', body: next })
      toast.add({ color: 'success', icon: 'i-lucide-save', title: t('admin.ai.saved'), duration: 10000 })
      await refresh()
      return true
    } catch (error) {
      toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: `${error}`, duration: 0 })
      return false
    } finally {
      busy.value = ''
    }
  }

  async function resetSettings() {
    busy.value = 'reset'
    try {
      await request('/admin/ai/settings/reset', { method: 'POST', body: {} })
      toast.add({ color: 'success', icon: 'i-lucide-rotate-ccw', title: t('admin.ai.resetDone'), duration: 10000 })
      await refresh()
    } catch (error) {
      toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: `${error}`, duration: 0 })
    } finally {
      busy.value = ''
    }
  }

  // 密钥只在此处提交一次；提交后界面只显示「已配置」，永远读不回明文。
  async function saveCredential(profileId: string, apiKey: string) {
    busy.value = `credential:${profileId}`
    try {
      const result = await request<{ profileId: string, version: number }>(`/admin/ai/profiles/${encodeURIComponent(profileId)}/credential`, {
        method: 'PUT',
        body: { apiKey }
      })
      toast.add({ color: 'success', icon: 'i-lucide-key-round', title: t('admin.ai.credentialSaved', { version: result.version }), duration: 10000 })
      await refresh()
      return true
    } catch (error) {
      toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: `${error}`, duration: 0 })
      return false
    } finally {
      busy.value = ''
    }
  }

  // 诊断结果只保留在内存：它是一次性的探测，不需要持久化，也不该在刷新后
  // 让人误以为是最新结论。
  const diagnoseResult = ref<AdminAIDiagnoseResult | null>(null)

  // fetchModels 用当前的地址与密钥探测供应商的模型列表。它是一次只读探测：
  // 不产生补全调用、不计费、不写执行记录。请求里携带的密钥明文只用于这一次探测，
  // 不经过任何持久化路径（写入密钥仍然只走 saveCredential）。
  //
  // HTTP 层失败（401/403/422/网络）在这里提示并返回 null；供应商层面的失败会作为
  // 结果返回，交给调用方就地在字段旁解释——那是运营者要读的信息，不是一个弹窗。
  async function fetchModels(probe: AdminAIModelProbe): Promise<AdminAIModelList | null> {
    busy.value = `models:${probe.profileId}`
    try {
      return await request<AdminAIModelList>(`/admin/ai/profiles/${encodeURIComponent(probe.profileId)}/models`, {
        method: 'POST',
        body: { baseUrl: probe.baseUrl, protocol: probe.protocol, apiKey: probe.apiKey }
      })
    } catch (error) {
      toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: `${error}`, duration: 0 })
      return null
    } finally {
      busy.value = ''
    }
  }

  async function diagnose(message: string) {
    busy.value = 'diagnose'
    diagnoseResult.value = null
    try {
      const result = await request<AdminAIDiagnoseResult>('/admin/ai/diagnose', {
        method: 'POST',
        body: { message }
      })
      diagnoseResult.value = result
      // 调用成功但正文为空不是「连接正常」：推理模型的预算被思考耗尽时就会这样。
      // 提示语必须区分，否则运营者看到成功提示却找不到回复内容。
      if (result.ok && !result.reason) {
        toast.add({ color: 'success', icon: 'i-lucide-circle-check', title: t('admin.ai.diagnose.okToast'), duration: 10000 })
      } else if (result.reason === 'empty_reply') {
        toast.add({ color: 'warning', icon: 'i-lucide-message-square-warning', title: t('admin.ai.diagnose.emptyReplyToast'), duration: 10000 })
      }
      await executions.refresh()
      return result
    } catch (error) {
      toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: `${error}`, duration: 0 })
      return null
    } finally {
      busy.value = ''
    }
  }

  return {
    ready,
    settings,
    credentialStatus,
    warnings,
    encryptionEnabled,
    replyBot,
    purposes,
    defaultSystemPrompt,
    safetyAppendix,
    loadFailed,
    loading,
    siteUsage,
    budgetMicros,
    executions: executions_,
    executionsPending,
    diagnoseResult,
    diagnose,
    fetchModels,
    busy,
    refresh,
    saveSettings,
    resetSettings,
    saveCredential
  }
}
