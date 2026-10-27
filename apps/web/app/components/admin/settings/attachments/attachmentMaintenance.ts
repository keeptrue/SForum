import type { AttachmentCleanupResult, AttachmentRecountResult } from './model'

/** 清理结果对应的 Toast 语义；文案 key 与参数保持纯数据，便于单测覆盖分支。 */
export type CleanupToastOutcome = {
  color: 'info' | 'success' | 'warning'
  icon: string
  messageKey: string
  params: Record<string, number>
}

/**
 * 清理结果 → Toast。
 * 全部落在宽限期内既不是失败也不是清理成功，用 info 说明「到期后自动清理」；
 * 宽限期天数取自清理响应本身，不再额外请求设置接口。
 */
export function cleanupOutcome(result: AttachmentCleanupResult): CleanupToastOutcome {
  const deleted = Math.max(0, Number(result.deleted) || 0)
  const failed = Math.max(0, Number(result.failed) || 0)
  const skipped = Math.max(0, Number(result.skipped) || 0)
  const retentionDays = Number(result.retentionDays)
  if (deleted === 0 && skipped > 0) {
    if (Number.isFinite(retentionDays) && retentionDays > 0) {
      return {
        color: 'info',
        icon: 'i-lucide-clock-3',
        messageKey: 'admin.attachments.cleanupNothingDue',
        params: { skipped, days: retentionDays }
      }
    }
    // 老后端不返回 retentionDays：降级为不带天数的文案。
    return {
      color: 'info',
      icon: 'i-lucide-clock-3',
      messageKey: 'admin.attachments.cleanupNothingDueNoDays',
      params: { skipped }
    }
  }
  return {
    color: failed > 0 ? 'warning' : 'success',
    icon: failed > 0 ? 'i-lucide-triangle-alert' : 'i-lucide-check',
    messageKey: 'admin.attachments.cleanupFinished',
    params: { deleted, failed }
  }
}

/**
 * 重算 dry-run 结果 → 直接提示「无需重算」，还是打开确认框。
 * 只有真的会有计数变化时才打扰运营确认。
 */
export function reconcilePreviewOutcome(preview: AttachmentRecountResult): 'noop' | 'confirm' {
  const lowered = Math.max(0, Number(preview.lowered) || 0)
  const raised = Math.max(0, Number(preview.raised) || 0)
  return lowered === 0 && raised === 0 ? 'noop' : 'confirm'
}

/** 确认框里的示例附件 id：契约上限 20 条，全部展示，不做隐性截断。 */
export function reconcileSamplesText(samples: number[] | undefined) {
  return (samples || []).map(id => String(id)).join(', ')
}
