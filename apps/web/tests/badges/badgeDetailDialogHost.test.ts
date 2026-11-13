import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, ref, watch } from 'vue'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 全局徽章弹窗宿主的挂载契约：
 *
 * - 详情就绪时渲染的是 `SFBadgeDetailModal`，`self` 由「页主 username === 当前登录用户」决定，
 *   `worn` 由 `useBadgeWorn` 的归属判定决定；
 * - 本人但未获得（`earned=false`）时详情仍展示，但佩戴动作不成立（宿主不调 `toggleWear`）；
 * - 数据未就绪/失败时只渲染轻量 UModal 的 loading/failed 文案 + 关闭出口，
 *   绝不用 chip 的 fallback 拼一颗缺说明的假徽章。
 *
 * 这里桩掉两个组合式函数而不是驱动真实实现：宿主的职责是「把状态接进哪个组件」，
 * 取数/缓存与佩戴动作分别由 `badgeDialog.test.ts` / `badgeWorn.test.ts` 覆盖。
 */
const DetailModalStub = defineComponent({
  name: 'SFBadgeDetailModal',
  props: {
    open: { type: Boolean, default: false },
    badge: { type: Object, default: null },
    groupName: { type: String, default: '' },
    variant: { type: String, default: '' },
    self: { type: Boolean, default: false },
    worn: { type: Boolean, default: false }
  },
  emits: ['update:open', 'toggle-wear'],
  template: `<div
    data-testid="detail-modal"
    :data-self="String(self)"
    :data-worn="String(worn)"
    :data-slug="badge && badge.slug"
    :data-group="groupName"
    :data-variant="variant"
  >
    <button type="button" data-testid="modal-wear" @click="$emit('toggle-wear')" />
  </div>`
})

const UModalStub = defineComponent({
  name: 'UModal',
  props: {
    open: { type: Boolean, default: false },
    title: { type: String, default: '' },
    ui: { type: Object, default: () => ({}) }
  },
  template: '<div v-if="open" data-testid="state-modal"><slot name="content" /></div>'
})

const SkeletonStub = defineComponent({
  name: 'SFSkeleton',
  template: '<div data-testid="skeleton" />'
})

const hostPath = fileURLToPath(new URL('../../app/components/forum/badges/SFBadgeDetailDialogHost.vue', import.meta.url))

// 编译时绑定的是闭包；用例在 mount 前改这些 holder，等于给宿主换一套依赖。
let authUser = ref<{ id: number, username: string } | null>({ id: 1, username: 'alice' })
let dialogStub: Record<string, unknown> = {}
let wornStub: Record<string, unknown> = {}
let wearStub: Record<string, unknown> = {}

const SFBadgeDetailDialogHost = await compileVueSfc(hostPath, 'sf-badge-detail-dialog-host', {
  computed,
  watch,
  useI18n: () => ({ t: (key: string) => key }),
  useAuthSession: () => ({ user: authUser }),
  useBadgeWorn: () => wornStub,
  useBadgeWearToggle: () => wearStub,
  useForumBadgeDialog: () => dialogStub,
  SFBadgeDetailModal: DetailModalStub
})

const earnedBadge = {
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

function mountHost(options: {
  username?: string
  viewer?: { id: number, username: string } | null
  entry?: Record<string, unknown> | null
  loading?: boolean
  worn?: boolean
}) {
  authUser = ref(options.viewer === undefined ? { id: 1, username: 'alice' } : options.viewer)
  const closeCalls: number[] = []
  const wearCalls: string[] = []
  dialogStub = {
    state: ref({
      open: true,
      username: options.username ?? 'alice',
      slug: 'helper',
      groupName: '',
      fallback: { slug: 'helper', name: '优质解答', tier: 'silver', icon: '' }
    }),
    entry: ref(options.entry ?? null),
    groupName: ref('内容'),
    loading: ref(Boolean(options.loading)),
    closeBadgeDialog: () => {
      closeCalls.push(1)
    }
  }
  wornStub = {
    loadedFor: () => true,
    isWorn: () => Boolean(options.worn),
    ensureLoaded: async () => {}
  }
  wearStub = {
    toggleWear: async (badge: { slug: string }) => {
      wearCalls.push(badge.slug)
    }
  }

  const wrapper = mount(SFBadgeDetailDialogHost, {
    global: { components: { UModal: UModalStub, SFSkeleton: SkeletonStub } }
  })
  return { wrapper, closeCalls, wearCalls }
}

describe('SFBadgeDetailDialogHost', () => {
  test('a visitor sees the detail modal with self=false', () => {
    const { wrapper } = mountHost({
      username: 'alice',
      viewer: { id: 2, username: 'bob' },
      entry: earnedBadge
    })
    try {
      const modal = wrapper.get('[data-testid="detail-modal"]')
      expect(modal.attributes('data-self')).toBe('false')
      expect(modal.attributes('data-slug')).toBe('helper')
      expect(modal.attributes('data-group')).toBe('内容')
      expect(modal.attributes('data-variant')).toBe('profile')
      expect(wrapper.find('[data-testid="state-modal"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('the owner sees self=true and the worn state of the resolved badge', () => {
    const { wrapper } = mountHost({ username: 'alice', entry: earnedBadge, worn: true })
    try {
      const modal = wrapper.get('[data-testid="detail-modal"]')
      expect(modal.attributes('data-self')).toBe('true')
      expect(modal.attributes('data-worn')).toBe('true')
    } finally {
      wrapper.unmount()
    }
  })

  test('an unearned badge still opens the detail but cannot be worn', async () => {
    const { wrapper, wearCalls } = mountHost({
      username: 'alice',
      entry: { ...earnedBadge, earned: false },
      worn: false
    })
    try {
      const modal = wrapper.get('[data-testid="detail-modal"]')
      expect(modal.attributes('data-self')).toBe('true')
      expect(modal.attributes('data-worn')).toBe('false')

      // 未获得：即便弹窗回发 toggle-wear，宿主也必须拒绝（canWear 不成立）。
      await wrapper.get('[data-testid="modal-wear"]').trigger('click')
      expect(wearCalls).toEqual([])
    } finally {
      wrapper.unmount()
    }
  })

  test('loading renders a skeleton state modal with a visible close button, never a fallback badge', async () => {
    const { wrapper, closeCalls } = mountHost({ entry: null, loading: true })
    try {
      expect(wrapper.find('[data-testid="detail-modal"]').exists()).toBe(false)
      const state = wrapper.get('[data-testid="state-modal"]')
      expect(state.text()).toContain('badges.dialog.loading')
      expect(wrapper.find('[data-testid="skeleton"]').exists()).toBe(true)

      const close = state.get('.sf-badge-dialog-state__close')
      expect(close.text()).toBe('common.close')
      await close.trigger('click')
      expect(closeCalls).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })

  test('failure renders the failed copy and a close button instead of faking an entry', async () => {
    const { wrapper, closeCalls } = mountHost({ entry: null, loading: false })
    try {
      expect(wrapper.find('[data-testid="detail-modal"]').exists()).toBe(false)
      const state = wrapper.get('[data-testid="state-modal"]')
      expect(state.text()).toContain('badges.dialog.failed')
      expect(wrapper.find('[data-testid="skeleton"]').exists()).toBe(false)

      await state.get('.sf-badge-dialog-state__close').trigger('click')
      expect(closeCalls).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })
})
