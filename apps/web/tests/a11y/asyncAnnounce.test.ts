import { afterEach, describe, expect, test } from 'bun:test'
import { nextTick } from 'vue'
import { announceAsyncResult } from '../../app/utils/a11y/asyncAnnounce'
import { resetLiveRegion, useLiveRegionState } from '../../app/utils/a11y/liveRegion'

/**
 * 列表异步面的播报决策（UX2-17 / 复审 M1）。
 *
 * 两件事必须钉死：失败**绝不能**播「已加载 0 个」；未完成时什么都不播。
 * `useAsyncData(..., { default: () => [] })` 在失败时保留空数组，所以只看条数就会撒谎。
 */

const { politeMessage, assertiveMessage } = useLiveRegionState()

afterEach(() => {
  resetLiveRegion()
})

describe('announceAsyncResult', () => {
  test('pending 时两条通道都不写', async () => {
    const state = announceAsyncResult({ pending: true, failed: false, success: '已加载 3 个任务。', failure: '任务列表加载失败。' })
    await nextTick()

    expect(state).toBe('pending')
    expect(politeMessage.value).toBe('')
    expect(assertiveMessage.value).toBe('')
  })

  test('失败时只播 assertive，绝不播成功文案', async () => {
    const state = announceAsyncResult({ pending: false, failed: true, success: '已加载 0 个任务。', failure: '任务列表加载失败。' })
    await nextTick()

    expect(state).toBe('failed')
    expect(politeMessage.value).toBe('')
    expect(assertiveMessage.value).toBe('任务列表加载失败。')
  })

  test('成功时只播 polite', async () => {
    const state = announceAsyncResult({ pending: false, failed: false, success: '已加载 3 个任务。', failure: '任务列表加载失败。' })
    await nextTick()

    expect(state).toBe('succeeded')
    expect(politeMessage.value).toBe('已加载 3 个任务。')
    expect(assertiveMessage.value).toBe('')
  })
})
