import { computed, ref } from 'vue'
import { apiErrorMessage } from '~/composables/useApiClient'
import { useModerationApi } from '~/composables/moderation/useModerationApi'

export type ModerationReportTarget = { type: 'topic' | 'comment', id: number }
export type ModerationReportReason = 'spam' | 'abuse' | 'illegal' | 'off_topic' | 'other'

/**
 * 举报对话框状态机：目标选择、原因/补充说明、提交与错误。
 * 抽成 composable 让「公开页面里的举报入口」共用同一套行为，
 * 页面只负责在模板里绑定返回的响应式字段（字段名与原页面保持一致）。
 *
 * 契约（本 composable 返回的是 **ref**，不是 reactive 对象）：
 * - 调用方可以安全地解构返回值（`const { target, reason, body } = useModerationReportDialog()`），
 *   拿到的仍然是 ref，模板里读写都保持响应式。
 * - **禁止再包 `reactive()`**：reactive 在属性读取时会把 ref 解包成一次性快照，
 *   解构后的 target/reason/body 等会永远停在初始值（target 恒为 null），
 *   模板再也读不到后续变更——公开页「举报」按钮点了没反应就是这个原因。
 *   回归守卫：apps/web/tests/moderation/moderationReportDialog.test.ts
 */
export function useModerationReportDialog(options: { canReport?: () => boolean } = {}) {
  const moderationApi = useModerationApi()
  const { t } = useI18n()

  const target = ref<ModerationReportTarget | null>(null)
  const reason = ref<ModerationReportReason | ''>('')
  const body = ref('')
  const submitting = ref(false)
  const error = ref('')
  const success = ref(false)

  const reasonOptions = computed(() => [
    { label: t('moderation.reason.spam'), value: 'spam' },
    { label: t('moderation.reason.abuse'), value: 'abuse' },
    { label: t('moderation.reason.illegal'), value: 'illegal' },
    { label: t('moderation.reason.off_topic'), value: 'off_topic' },
    { label: t('moderation.reason.other'), value: 'other' }
  ])

  function open(next: ModerationReportTarget) {
    // 未登录不打开：举报提交需要身份，前端只做 UX 过滤，策略仍由后端裁决。
    if (options.canReport && !options.canReport()) {
      return
    }
    target.value = next
    reason.value = ''
    body.value = ''
    error.value = ''
    success.value = false
  }

  function close() {
    target.value = null
  }

  const reportReasonValues: ModerationReportReason[] = ['spam', 'abuse', 'illegal', 'off_topic', 'other']

  // 对话框 emit 的 reason 是 string；按白名单收窄后再写入，避免模板把任意字符串
  // 塞进联合类型。写入的是 ref 本身，调用方解构后模板依然能读到最新值。
  function setReason(value: string) {
    reason.value = reportReasonValues.includes(value as ModerationReportReason) ? (value as ModerationReportReason) : ''
  }

  async function submit() {
    if (!target.value || !reason.value || submitting.value) {
      return
    }
    submitting.value = true
    error.value = ''
    try {
      await moderationApi.createReport({
        targetType: target.value.type,
        targetId: target.value.id,
        reasonCode: reason.value,
        body: body.value
      })
      success.value = true
      setTimeout(() => close(), 2000)
    } catch (submitError) {
      error.value = apiErrorMessage(submitError) || t('moderation.reportFailed')
    } finally {
      submitting.value = false
    }
  }

  // 直接返回 ref 契约：调用方解构出来的仍然是 ref（详见文件头的契约注释）。
  // 不要在这里再包一层 reactive()，否则解构会把 ref 解包成快照。
  return {
    target,
    reason,
    body,
    submitting,
    error,
    success,
    reasonOptions,
    open,
    close,
    setReason,
    submit
  }
}
