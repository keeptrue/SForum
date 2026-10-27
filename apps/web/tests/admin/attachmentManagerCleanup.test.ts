import { describe, expect, test } from 'bun:test'

import {
  cleanupOutcome,
  reconcilePreviewOutcome,
  reconcileSamplesText
} from '../../app/components/admin/settings/attachments/attachmentMaintenance'
import type { AttachmentRecountResult } from '../../app/components/admin/settings/attachments/model'

describe('attachment cleanup feedback', () => {
  test('reports the grace period instead of a success when nothing was due', () => {
    const outcome = cleanupOutcome({ deleted: 0, failed: 0, skipped: 12, retentionDays: 30 })
    expect(outcome.color).toBe('info')
    expect(outcome.icon).toBe('i-lucide-clock-3')
    expect(outcome.messageKey).toBe('admin.attachments.cleanupNothingDue')
    expect(outcome.params).toEqual({ skipped: 12, days: 30 })
  })

  test('falls back to the copy without days when the API omits retentionDays', () => {
    const outcome = cleanupOutcome({ deleted: 0, failed: 0, skipped: 5 })
    expect(outcome.color).toBe('info')
    expect(outcome.messageKey).toBe('admin.attachments.cleanupNothingDueNoDays')
    expect(outcome.params).toEqual({ skipped: 5 })
    expect(outcome.params.days).toBeUndefined()
  })

  test('keeps the ordinary result copy when nothing was skipped', () => {
    const outcome = cleanupOutcome({ deleted: 0, failed: 0, skipped: 0, retentionDays: 30 })
    expect(outcome.color).toBe('success')
    expect(outcome.icon).toBe('i-lucide-check')
    expect(outcome.messageKey).toBe('admin.attachments.cleanupFinished')
    expect(outcome.params).toEqual({ deleted: 0, failed: 0 })
  })

  test('still reports real deletions and failures through the ordinary copy', () => {
    const cleaned = cleanupOutcome({ deleted: 3, failed: 0, skipped: 4, retentionDays: 7 })
    expect(cleaned.color).toBe('success')
    expect(cleaned.messageKey).toBe('admin.attachments.cleanupFinished')
    expect(cleaned.params).toEqual({ deleted: 3, failed: 0 })

    const partial = cleanupOutcome({ deleted: 2, failed: 1 })
    expect(partial.color).toBe('warning')
    expect(partial.icon).toBe('i-lucide-triangle-alert')
    expect(partial.params).toEqual({ deleted: 2, failed: 1 })
  })
})

describe('attachment recount preview', () => {
  function preview(patch: Partial<AttachmentRecountResult>): AttachmentRecountResult {
    return { lowered: 0, raised: 0, unchanged: 0, applied: false, samples: [], ...patch }
  }

  test('skips the confirm dialog when the dry run finds no drift', () => {
    expect(reconcilePreviewOutcome(preview({ unchanged: 49 }))).toBe('noop')
  })

  test('asks for confirmation when counts would change', () => {
    expect(reconcilePreviewOutcome(preview({ lowered: 2, unchanged: 47 }))).toBe('confirm')
    expect(reconcilePreviewOutcome(preview({ raised: 1 }))).toBe('confirm')
  })

  test('shows every sample id the contract returns', () => {
    const samples = Array.from({ length: 20 }, (_, index) => 101 + index)
    const text = reconcileSamplesText(samples)
    expect(text.split(', ')).toHaveLength(20)
    expect(text.startsWith('101, 102')).toBe(true)
    expect(text.endsWith('120')).toBe(true)
    expect(reconcileSamplesText(undefined)).toBe('')
  })
})
