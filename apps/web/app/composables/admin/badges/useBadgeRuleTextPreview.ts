import { getCurrentScope, onScopeDispose, ref, watch } from 'vue'
import type { BadgeRule } from '~/utils/admin/badgeRule'

/**
 * 公开说明的**只读预览**：规则草稿一变，就去问服务端「这句话会怎么生成」。
 *
 * 口径唯一来源是服务端（`POST /admin/badges/rule-text` → `BuildRuleRequirementText`），
 * 前端不拼句子——保存后公开面看到的就是这里预览的同一条实现。
 *
 * 三条容易踩的行为：
 * 1. **防抖**：连续敲条件时只在停顿 300ms 后发一次请求，不做逐字符往返。
 * 2. **丢弃过期响应**：先发的慢请求不能覆盖后发的快请求（序号 + AbortController 双保险）。
 * 3. **失败保留旧文案**：派生数据断了不该把用户已经看到的句子擦掉，只加一行提示；
 *    也绝不进 `fieldErrors`——预览失败不阻断保存。
 */
export type BadgeRuleTextStatus = 'idle' | 'pending' | 'ready' | 'error'

/** 双语公开说明；与服务端 `requirementText{zh,en}` 同形。 */
export interface BadgeRuleText {
  zh: string
  en: string
}

export type BadgeRuleTextLoader = (rule: BadgeRule | null) => Promise<BadgeRuleText | null | undefined>

export interface BadgeRuleTextPreviewOptions {
  /** 当前草稿规则；`null` = 还没有规则（手动徽章 / 规则缺失），此时不发请求。 */
  rule: () => BadgeRule | null
  /** 额外前置条件（例如弹窗没打开时不打网络）。缺省恒真。 */
  enabled?: () => boolean
  /** 测试注入点：默认走 `useApiClient()`。 */
  loader?: BadgeRuleTextLoader
  debounceMs?: number
}

const DEFAULT_DEBOUNCE_MS = 300

export function useBadgeRuleTextPreview(options: BadgeRuleTextPreviewOptions) {
  const text = ref<BadgeRuleText | null>(null)
  const status = ref<BadgeRuleTextStatus>('idle')
  const error = ref<unknown>(null)

  const debounceMs = options.debounceMs ?? DEFAULT_DEBOUNCE_MS
  const loader: BadgeRuleTextLoader = options.loader
    ?? (rule => useApiClient().request<BadgeRuleText>('/admin/badges/rule-text', {
      method: 'POST',
      body: { rule }
    }))

  let timer: ReturnType<typeof setTimeout> | null = null
  let controller: AbortController | null = null
  /** 请求序号：只有最后一次发出的请求能写结果。 */
  let sequence = 0

  function cancelPending() {
    if (timer) {
      clearTimeout(timer)
      timer = null
    }
    controller?.abort()
    controller = null
    // 让所有在途响应立刻过期。
    sequence += 1
  }

  function reset() {
    cancelPending()
    text.value = null
    status.value = 'idle'
    error.value = null
  }

  async function run(rule: BadgeRule) {
    const ticket = sequence
    const local = new AbortController()
    controller = local
    status.value = 'pending'
    error.value = null
    try {
      const next = await loader(rule)
      if (ticket !== sequence || local.signal.aborted) return
      text.value = { zh: next?.zh ?? '', en: next?.en ?? '' }
      status.value = 'ready'
    } catch (cause) {
      if (ticket !== sequence || local.signal.aborted) return
      error.value = cause
      status.value = 'error'
    } finally {
      if (controller === local) controller = null
    }
  }

  function schedule() {
    if (options.enabled && !options.enabled()) {
      // 暂停（例如弹窗关闭）：不发请求，但**保留**上一次结果，
      // 否则「保存 → 关闭」会立刻把刚写回的文案擦成空态。
      cancelPending()
      return
    }
    const rule = options.rule()
    if (!rule) {
      reset()
      return
    }
    cancelPending()
    status.value = 'pending'
    timer = setTimeout(() => {
      timer = null
      void run(rule)
    }, debounceMs)
  }

  /** 保存成功后以服务端返回的文案为准：预览只是草稿的近似，落库结果优先。 */
  function setText(next: BadgeRuleText | null | undefined) {
    cancelPending()
    if (!next) {
      text.value = null
      status.value = 'idle'
      error.value = null
      return
    }
    text.value = { zh: next.zh ?? '', en: next.en ?? '' }
    status.value = 'ready'
    error.value = null
  }

  watch(() => [options.rule(), options.enabled?.() ?? true] as const, schedule, { immediate: true })

  if (getCurrentScope()) {
    onScopeDispose(() => {
      cancelPending()
    })
  }

  return { text, status, error, refresh: schedule, setText }
}
