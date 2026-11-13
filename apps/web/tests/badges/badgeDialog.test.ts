import { describe, expect, test } from 'bun:test'
import { nextTick, ref } from 'vue'
import { useForumBadgeDialog } from '~/composables/forum/badges/useForumBadgeDialog'

/**
 * 全局徽章说明弹窗的数据层：
 *
 * - 打开时按 username 拉一次 `GET /profiles/{username}/badges`，按 slug 从墙里解析条目；
 * - 同一用户第二次打开不再请求（缓存按 username 分桶）；
 * - 失败只置 `failed`，不伪造条目、不清已缓存的墙；重开会重试；
 * - 分组名只认服务端，调用方传的 groupName 只在墙未到时当占位。
 *
 * `useState` / `useApiClient` 是 Nuxt 自动导入，这里按仓库既有桩法替换全局边界，
 * 其余全是生产代码。
 */
type Call = { path: string, method: string }

function createStateStore() {
  const state = new Map<string, ReturnType<typeof ref>>()
  return {
    useState<T>(key: string, init: () => T) {
      if (!state.has(key)) state.set(key, ref(init()))
      return state.get(key) as ReturnType<typeof ref<T>>
    }
  }
}

async function withDialogGlobals(
  responses: Array<Record<string, unknown> | Error>,
  run: (context: { calls: Call[] }) => Promise<void>
) {
  const globals = globalThis as Record<string, unknown>
  const originalState = globals.useState
  const originalApiClient = globals.useApiClient
  const store = createStateStore()
  const calls: Call[] = []
  const queue = [...responses]

  globals.useState = store.useState
  globals.useApiClient = () => ({
    request: async (path: string, init: { method?: string } = {}) => {
      calls.push({ path, method: init.method ?? 'GET' })
      const next = queue.shift()
      if (next instanceof Error) throw next
      return next ?? {}
    }
  })

  try {
    await run({ calls })
  } finally {
    globals.useState = originalState
    globals.useApiClient = originalApiClient
  }
}

/** 让 `openBadgeDialog` 里 fire-and-forget 的请求跑完。 */
const flush = () => new Promise(resolve => setTimeout(resolve, 0))

const helperEntry = {
  slug: 'helper',
  name: '优质解答',
  tier: 'silver',
  icon: '',
  position: 1,
  earned: true,
  description: '回答被采纳很多次。',
  requirementText: { zh: '被采纳 10 次', en: 'Accepted 10 times' },
  grantedAt: '2026-11-03T08:00:00Z'
}

const wall = {
  groups: [
    {
      key: 'content',
      name: '内容',
      position: 1,
      badges: [helperEntry]
    }
  ]
}

const chipRef = { slug: 'helper', name: '优质解答', tier: 'silver', icon: '' }

describe('useForumBadgeDialog', () => {
  test('opens, loads the wall once, and resolves the entry plus the server group name', async () => {
    await withDialogGlobals([wall], async ({ calls }) => {
      const dialog = useForumBadgeDialog()
      dialog.openBadgeDialog({ username: 'alice', badge: chipRef, groupName: '调用方提示' })
      await flush()

      expect(calls).toEqual([{ path: '/profiles/alice/badges', method: 'GET' }])
      expect(dialog.state.value).toMatchObject({ open: true, username: 'alice', slug: 'helper' })
      expect(dialog.entry.value?.slug).toBe('helper')
      expect(dialog.entry.value?.description).toBe('回答被采纳很多次。')
      expect(dialog.entry.value?.requirementText).toEqual({ zh: '被采纳 10 次', en: 'Accepted 10 times' })
      // 服务端分组名覆盖加载期提示。
      expect(dialog.groupName.value).toBe('内容')
      expect(dialog.loading.value).toBe(false)
      expect(dialog.failed.value).toBe(false)
    })
  })

  test('a second open for the same user reuses the cached wall and sends no new request', async () => {
    await withDialogGlobals([wall], async ({ calls }) => {
      const dialog = useForumBadgeDialog()
      dialog.openBadgeDialog({ username: 'alice', badge: chipRef })
      await flush()
      expect(calls).toHaveLength(1)

      dialog.closeBadgeDialog()
      expect(dialog.state.value.open).toBe(false)

      dialog.openBadgeDialog({ username: 'alice', badge: chipRef })
      await flush()
      expect(calls).toHaveLength(1)
      expect(dialog.entry.value?.slug).toBe('helper')
    })
  })

  test('a failed load flags failed without faking an entry, and reopening retries', async () => {
    await withDialogGlobals([new Error('offline'), wall], async ({ calls }) => {
      const dialog = useForumBadgeDialog()
      dialog.openBadgeDialog({ username: 'alice', badge: chipRef })
      await flush()

      expect(dialog.failed.value).toBe(true)
      expect(dialog.entry.value).toBeNull()
      expect(dialog.loading.value).toBe(false)

      dialog.closeBadgeDialog()
      dialog.openBadgeDialog({ username: 'alice', badge: chipRef })
      await flush()

      // 失败不写 loaded：重开必须重试，而不是拿空墙当「没有这枚徽章」。
      expect(calls).toHaveLength(2)
      expect(dialog.failed.value).toBe(false)
      expect(dialog.entry.value?.slug).toBe('helper')
    })
  })

  test('an unknown slug keeps entry null and only falls back to the hint group name', async () => {
    await withDialogGlobals([wall], async () => {
      const dialog = useForumBadgeDialog()
      dialog.openBadgeDialog({
        username: 'alice',
        badge: { ...chipRef, slug: 'retired', name: '已停用' },
        groupName: '旧分组提示'
      })
      await flush()

      expect(dialog.entry.value).toBeNull()
      // 墙里没有这枚：分组名退回调用方提示，绝不编一个条目出来。
      expect(dialog.groupName.value).toBe('旧分组提示')
      expect(dialog.failed.value).toBe(false)
    })
  })

  test('walls are cached per username instead of globally', async () => {
    const bobWall = {
      groups: [{ key: 'honor', name: '荣誉', position: 1, badges: [{ ...helperEntry, slug: 'bob-badge', name: 'Bob 的徽章' }] }]
    }
    await withDialogGlobals([wall, bobWall], async ({ calls }) => {
      const dialog = useForumBadgeDialog()
      dialog.openBadgeDialog({ username: 'alice', badge: chipRef })
      await flush()
      dialog.openBadgeDialog({ username: 'bob', badge: { ...chipRef, slug: 'bob-badge' } })
      await flush()

      expect(calls.map(call => call.path)).toEqual(['/profiles/alice/badges', '/profiles/bob/badges'])
      expect(dialog.entry.value?.name).toBe('Bob 的徽章')
      expect(dialog.groupName.value).toBe('荣誉')
    })
  })

  test('closing restores focus to the chip that opened the dialog', async () => {
    await withDialogGlobals([wall], async () => {
      const trigger = document.createElement('button')
      document.body.appendChild(trigger)
      trigger.focus()
      expect(document.activeElement).toBe(trigger)

      const dialog = useForumBadgeDialog()
      dialog.openBadgeDialog({ username: 'alice', badge: chipRef })
      await flush()
      dialog.closeBadgeDialog()
      await nextTick()

      // 关闭后焦点回到触发 chip，键盘用户不会掉到 body。
      expect(document.activeElement).toBe(trigger)
      trigger.remove()
    })
  })
})
