import { afterEach, describe, expect, test } from 'bun:test'
import { nextTick } from 'vue'
import { announceAssertive, announcePolite, resetLiveRegion, useLiveRegionState } from '../../app/utils/a11y/liveRegion'

/**
 * 共享 live region（UX2-17）。
 *
 * 钉住：两条通道各写各的；同一句话连续播报必须先清空再写回（读屏不会重复读相同的值）。
 */

const { politeMessage, assertiveMessage } = useLiveRegionState()

afterEach(() => {
  resetLiveRegion()
})

describe('live region announcer', () => {
  test('polite / assertive 是两条独立通道', async () => {
    announcePolite('已加载 3 个任务。')
    announceAssertive('保存失败。')
    await nextTick()

    expect(politeMessage.value).toBe('已加载 3 个任务。')
    expect(assertiveMessage.value).toBe('保存失败。')
  })

  test('连续两次同一句话会先清空再写回，保证读屏重播', async () => {
    announcePolite('已加载 3 个任务。')
    await nextTick()
    expect(politeMessage.value).toBe('已加载 3 个任务。')

    announcePolite('已加载 3 个任务。')
    // 清空发生在写入前的 nextTick 里
    expect(politeMessage.value).toBe('')
    await nextTick()
    expect(politeMessage.value).toBe('已加载 3 个任务。')
  })

  test('空白文案不写通道', async () => {
    announcePolite('   ')
    await nextTick()
    expect(politeMessage.value).toBe('')
  })
})
