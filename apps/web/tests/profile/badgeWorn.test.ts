import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, h, ref, watch, type Ref } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount } from '../helpers/vueSfc'
import {
  badgeWornErrorKey,
  badgeWornStateFrom,
  emptyBadgeWornState,
  useBadgeWorn,
  wornSlugOf
} from '~/composables/forum/badges/useBadgeWorn'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useBadgeWearToggle } from '~/composables/forum/badges/useBadgeWearToggle'
import {
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeRequirementText,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle,
  sortProfileBadgeEntries,
  sortProfileBadgeGroups
} from '~/utils/forum/forumBadge'

/**
 * 资料页佩戴 UI 的契约（`GET/PUT/DELETE /badges/worn`）：
 *
 * - 只有本人视角（`self`）且非目录页才出现佩戴控件；他人页与 `/badges` 一个控件都没有。
 * - 按钮**可见文字就是动作**：未佩戴「佩戴」，已佩戴「取消佩戴」；状态只由 `aria-pressed`
 *   表达。已佩戴时再点 = **不佩戴任何徽章**（`PUT {hidden:true}`），不是 `DELETE` 回自动展示。
 * - 底部状态条是两个常驻出口：「自动展示徽章」（`DELETE` 回 auto）与「不佩戴徽章」
 *   （`PUT {hidden:true}`）；当前态那颗点了不发请求（可见空操作）。
 * - 乐观更新：点下去本地立刻变；失败回滚到点击前的快照，并且**必须有错误 toast**。
 * - `upgradeSlug` 仍由数据层解析（服务端契约没变），但 UI 不再消费：浮起的「可升级」提示
 *   已按产品裁决删除，佩戴只由用户主动点选。
 */
const zh = JSON.parse(readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')) as Record<string, unknown>
const en = JSON.parse(readFileSync(new URL('../../i18n/locales/en-US.json', import.meta.url), 'utf8')) as Record<string, unknown>

function lookup(messages: Record<string, unknown>, path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    messages
  )
}

function makeI18n(messages: Record<string, unknown>) {
  const t = (key: string, params: Record<string, string | number> = {}) => {
    const template = lookup(messages, key)
    if (typeof template !== 'string') throw new Error(`missing locale key: ${key}`)
    return template.replace(/\{(\w+)\}/g, (match, name: string) => (
      params[name] === undefined ? match : String(params[name])
    ))
  }
  const te = (key: string) => typeof lookup(messages, key) === 'string'
  return { t, te }
}

const zhI18n = makeI18n(zh)

type Call = { path: string, method: string, body?: unknown }

function createStateStore() {
  const state = new Map<string, ReturnType<typeof ref>>()
  return {
    useState<T>(key: string, init: () => T) {
      if (!state.has(key)) state.set(key, ref(init()))
      return state.get(key) as ReturnType<typeof ref<T>>
    }
  }
}

type WornGlobalsOptions = {
  /** 每次请求依次消费：对象 = 正常响应，Error = 抛错，Promise = 手动控制回包时机。 */
  responses?: Array<Record<string, unknown> | Error | Promise<Record<string, unknown>>>
  /**
   * 当前登录用户（`useState('auth:user')` 那一格）：账号切换用例要在两次
   * `ensureLoaded()` 之间改它。缺省视为已登录的 user 1（归属判定需要非零 viewerId）。
   */
  viewer?: Ref<{ id: number } | null>
}

/**
 * 组合式函数与 tab 都走 Nuxt 自动导入（`useState` / `useApiClient` / `useToast`），
 * 所以这里只替换这三处边界，其余全是生产代码。
 */
async function withWornGlobals(
  options: WornGlobalsOptions,
  run: (context: { calls: Call[], toasts: Array<Record<string, unknown>> }) => Promise<void>
) {
  const globals = globalThis as Record<string, unknown>
  const originalState = globals.useState
  const originalApiClient = globals.useApiClient
  const originalToast = globals.useToast
  const store = createStateStore()
  const calls: Call[] = []
  const toasts: Array<Record<string, unknown>> = []
  const queue = [...(options.responses ?? [])]
  // `useAuthSession` 是显式 import（仓库约定），不能从全局桩替身；但它读的是
  // `useState('auth:user')`，所以把这一格换成用例可改的 ref 就能控制当前账号。
  const authUser = options.viewer ?? ref<{ id: number } | null>({ id: 1 })

  globals.useState = <T>(key: string, init: () => T) => (
    key === 'auth:user' ? authUser as unknown as Ref<T> : store.useState(key, init)
  )
  globals.useToast = () => ({ add: (toast: Record<string, unknown>) => toasts.push(toast) })
  globals.useApiClient = () => ({
    request: async (path: string, init: { method?: string, body?: unknown } = {}) => {
      calls.push({ path, method: init.method ?? 'GET', body: init.body })
      const next = queue.shift()
      if (next instanceof Error) throw next
      return next ?? {}
    }
  })

  try {
    await run({ calls, toasts })
  } finally {
    globals.useState = originalState
    globals.useApiClient = originalApiClient
    globals.useToast = originalToast
  }
}

function apiError(reason: string, status = 422) {
  const error = new Error(reason) as Error & { status: number, data: Record<string, unknown> }
  error.status = status
  error.data = { reason }
  return error
}

describe('useBadgeWorn · 契约归一', () => {
  test('worn/automatic/upgrade accept both object and bare-slug shapes', () => {
    expect(wornSlugOf({ slug: 'helper', name: '优质解答' })).toBe('helper')
    expect(wornSlugOf('helper')).toBe('helper')
    expect(wornSlugOf(null)).toBe('')
    expect(wornSlugOf({})).toBe('')

    const fromObjects = badgeWornStateFrom({
      mode: 'manual',
      worn: { slug: 'helper' },
      automatic: { slug: 'liked' },
      upgrade: { slug: 'gold-writer' }
    })
    expect(fromObjects).toEqual({ mode: 'manual', wornSlug: 'helper', automaticSlug: 'liked', upgradeSlug: 'gold-writer' })

    const fromSlugs = badgeWornStateFrom({ mode: 'auto', worn: 'liked', automatic: 'liked', upgrade: null })
    expect(fromSlugs).toEqual({ mode: 'auto', wornSlug: 'liked', automaticSlug: 'liked', upgradeSlug: '' })
  })

  test('an unknown or missing mode degrades to auto instead of trusting the payload', () => {
    expect(badgeWornStateFrom({ mode: 'weird', worn: 'helper' }).mode).toBe('auto')
    expect(badgeWornStateFrom(null)).toEqual(emptyBadgeWornState())
  })

  test('failure keys separate "not owned" from generic save failures', () => {
    expect(badgeWornErrorKey(apiError('badge.not_owned'))).toBe('badges.worn.notOwned')
    expect(badgeWornErrorKey(apiError('badge.disabled'))).toBe('badges.worn.notOwned')
    expect(badgeWornErrorKey(new Error('network'))).toBe('badges.worn.saveFailed')
  })
})

describe('useBadgeWorn · 乐观更新与回滚', () => {
  test('badgeWornStateFrom parses mode=hidden with an empty worn slug', () => {
    expect(badgeWornStateFrom({ mode: 'hidden', worn: null, automatic: { slug: 'liked' }, upgrade: null }))
      .toEqual({ mode: 'hidden', wornSlug: '', automaticSlug: 'liked', upgradeSlug: '' })
  })

  test('wear flips local state before the response and keeps the server answer afterwards', async () => {
    await withWornGlobals({ responses: [{ mode: 'manual', worn: { slug: 'helper' }, automatic: { slug: 'liked' }, upgrade: null }] }, async ({ calls }) => {
      const worn = useBadgeWorn()
      await worn.wear('helper')
      expect(calls).toEqual([{ path: '/badges/worn', method: 'PUT', body: { slug: 'helper' } }])
      expect(worn.mode.value).toBe('manual')
      expect(worn.wornSlug.value).toBe('helper')
      expect(worn.isWorn('helper')).toBe(true)
    })
  })

  describe('hidden 展示态', () => {
    test('hide sends PUT {hidden:true} optimistically and keeps the server answer', async () => {
      await withWornGlobals({
        responses: [
          { mode: 'manual', worn: { slug: 'helper' }, automatic: { slug: 'liked' }, upgrade: null },
          { mode: 'hidden', worn: null, automatic: { slug: 'liked' }, upgrade: null }
        ]
      }, async ({ calls }) => {
        const worn = useBadgeWorn()
        await worn.load()
        await worn.hide()
        expect(calls.at(-1)).toEqual({ path: '/badges/worn', method: 'PUT', body: { hidden: true } })
        expect(worn.mode.value).toBe('hidden')
        expect(worn.isHidden.value).toBe(true)
        expect(worn.wornSlug.value).toBe('')
      })
    })

    test('a failed hide rolls back to the previous snapshot and rethrows', async () => {
      await withWornGlobals({
        responses: [
          { mode: 'manual', worn: { slug: 'helper' }, automatic: { slug: 'liked' }, upgrade: null },
          new Error('network')
        ]
      }, async () => {
        const worn = useBadgeWorn()
        await worn.load()
        let caught: unknown
        try {
          await worn.hide()
        } catch (error) {
          caught = error
        }
        expect(caught).toBeDefined()
        expect(worn.mode.value).toBe('manual')
        expect(worn.wornSlug.value).toBe('helper')
        expect(worn.isHidden.value).toBe(false)
      })
    })

    test('wear lifts the hidden state back to manual', async () => {
      await withWornGlobals({
        responses: [
          { mode: 'hidden', worn: null, automatic: { slug: 'liked' }, upgrade: null },
          { mode: 'manual', worn: { slug: 'helper' }, automatic: { slug: 'liked' }, upgrade: null }
        ]
      }, async ({ calls }) => {
        const worn = useBadgeWorn()
        await worn.load()
        expect(worn.isHidden.value).toBe(true)
        await worn.wear('helper')
        expect(worn.mode.value).toBe('manual')
        expect(worn.wornSlug.value).toBe('helper')
        expect(calls.at(-1)?.body).toEqual({ slug: 'helper' })
      })
    })
  })

  test('a failed wear rolls back to the previous snapshot and rethrows', async () => {
    await withWornGlobals({ responses: [apiError('badge.not_owned')] }, async () => {
      const worn = useBadgeWorn()
      let caught: unknown
      try {
        await worn.wear('helper')
      } catch (error) {
        caught = error
      }
      expect(caught).toBeDefined()
      expect(worn.wornSlug.value).toBe('')
      expect(worn.mode.value).toBe('auto')
      expect(badgeWornErrorKey(caught)).toBe('badges.worn.notOwned')
    })
  })

  test('clear optimistically falls back to the automatic badge and reverts on failure', async () => {
    await withWornGlobals({
      responses: [
        { mode: 'auto', worn: { slug: 'liked' }, automatic: { slug: 'liked' }, upgrade: null },
        { mode: 'manual', worn: { slug: 'helper' }, automatic: { slug: 'liked' }, upgrade: null },
        new Error('network')
      ]
    }, async ({ calls }) => {
      const worn = useBadgeWorn()
      await worn.load()
      expect(worn.mode.value).toBe('auto')
      expect(worn.wornSlug.value).toBe('liked')

      await worn.wear('helper')
      expect(worn.wornSlug.value).toBe('helper')

      let caught: unknown
      try {
        await worn.clear()
      } catch (error) {
        caught = error
      }
      expect(caught).toBeDefined()
      // 回滚到点击前（manual + helper），不是回滚成空。
      expect(worn.mode.value).toBe('manual')
      expect(worn.wornSlug.value).toBe('helper')
      expect(calls.filter(call => call.method === 'DELETE')).toHaveLength(1)
    })
  })

  test('upgrade is only exposed in manual mode', async () => {
    await withWornGlobals({ responses: [{ mode: 'auto', worn: 'liked', automatic: 'liked', upgrade: 'helper' }] }, async () => {
      const worn = useBadgeWorn()
      await worn.load()
      expect(worn.upgradeSlug.value).toBe('')
    })
    await withWornGlobals({ responses: [{ mode: 'manual', worn: 'liked', automatic: 'helper', upgrade: 'helper' }] }, async () => {
      const worn = useBadgeWorn()
      await worn.load()
      expect(worn.upgradeSlug.value).toBe('helper')
    })
  })

  test('a failed load keeps the previous state and flags the failure instead of throwing', async () => {
    await withWornGlobals({ responses: [new Error('offline')] }, async () => {
      const worn = useBadgeWorn()
      await worn.load()
      expect(worn.failed.value).toBe(true)
      expect(worn.wornSlug.value).toBe('')
      expect(worn.loaded.value).toBe(false)
    })
  })

  test('switching accounts refetches the worn state instead of leaking the previous owner', async () => {
    // 登录/登出都是客户端跳转、不整页刷新：全局佩戴态会带着上一个账号的 loaded/wornSlug 活下来。
    const viewer = ref<{ id: number } | null>({ id: 1 })
    await withWornGlobals({
      viewer,
      responses: [
        { mode: 'manual', worn: { slug: 'helper' }, automatic: { slug: 'liked' }, upgrade: null },
        { mode: 'hidden', worn: null, automatic: { slug: 'liked' }, upgrade: null }
      ]
    }, async ({ calls }) => {
      const worn = useBadgeWorn()
      await worn.ensureLoaded()
      expect(worn.loadedFor(1)).toBe(true)
      expect(worn.loadedFor(2)).toBe(false)
      expect(worn.wornSlug.value).toBe('helper')

      // 同一页面里换成 user 2：loaded 仍为 true，但归属不是当前账号，必须重取。
      viewer.value = { id: 2 }
      await worn.ensureLoaded()

      expect(calls.filter(call => call.method === 'GET')).toHaveLength(2)
      expect(worn.loadedFor(1)).toBe(false)
      expect(worn.loadedFor(2)).toBe(true)
      // 新账号的 hidden 态立刻生效：不会继承上一个账号的 helper。
      expect(worn.isHidden.value).toBe(true)
      expect(worn.wornSlug.value).toBe('')
    })
  })

  test('a response that lands after an account switch is discarded', async () => {
    const viewer = ref<{ id: number } | null>({ id: 1 })
    let resolveResponse!: (value: Record<string, unknown>) => void
    const pending = new Promise<Record<string, unknown>>((resolve) => {
      resolveResponse = resolve
    })

    await withWornGlobals({ viewer, responses: [pending] }, async ({ calls }) => {
      const worn = useBadgeWorn()
      // 以 user 1 发起 GET，回包被手动挂起。
      const inFlight = worn.ensureLoaded()
      expect(calls).toHaveLength(1)

      // 回包前换账号：旧账号的响应迟到，不能记到 user 2 名下，也不能污染共享 state。
      viewer.value = { id: 2 }
      resolveResponse({ mode: 'manual', worn: { slug: 'helper' }, automatic: { slug: 'liked' }, upgrade: null })
      await inFlight

      expect(worn.loadedFor(1)).toBe(false)
      expect(worn.loadedFor(2)).toBe(false)
      expect(worn.ownerId.value).toBe(0)
      expect(worn.state.value.wornSlug).toBe('')
      expect(worn.mode.value).toBe('auto')
    })
  })
})

/* ------------------------------------------------------------------------- */
/* 资料页徽章 tab 的佩戴 UI                                                    */
/* ------------------------------------------------------------------------- */

const UIconStub = defineComponent({
  name: 'UIcon',
  props: { name: { type: String, default: '' } },
  template: '<i :data-icon="name" />'
})

const NuxtLinkStub = defineComponent({
  name: 'NuxtLink',
  props: { to: { type: [String, Object], default: '' } },
  setup(props, { slots }) {
    const href = typeof props.to === 'string' ? props.to : (props.to as { path?: string }).path
    return () => h('a', { href }, slots.default?.())
  }
})

const EmptyStateStub = defineComponent({
  name: 'SFEmptyState',
  props: { title: { type: String, default: '' }, description: { type: String, default: '' } },
  template: '<div class="sf-empty-state-stub">{{ title }}</div>'
})

const cardPath = fileURLToPath(new URL('../../app/components/forum/badges/SFBadgeCard.vue', import.meta.url))
const SFBadgeCard = await compileVueSfc(cardPath, 'sf-badge-card-worn', {
  computed,
  useI18n: () => ({ t: zhI18n.t, te: zhI18n.te, locale: ref('zh-CN') }),
  useSiteDateTime: () => ({ formatDateOnly: (value: string) => value.slice(0, 10) }),
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle
})

/** 详情弹窗在本文件只作为占位：佩戴交互的断言全在卡片的动作位上。 */
const BadgeDetailModalStub = defineComponent({
  name: 'SFBadgeDetailModal',
  props: {
    open: { type: Boolean, default: false },
    badge: { type: Object, default: null },
    groupName: { type: String, default: '' },
    variant: { type: String, default: 'profile' },
    self: { type: Boolean, default: false },
    worn: { type: Boolean, default: false }
  },
  emits: ['update:open', 'toggle-wear'],
  template: '<div v-if="open" class="badge-detail-stub" />'
})

const tabPath = fileURLToPath(new URL('../../app/components/profile/SFProfileBadgeTab.vue', import.meta.url))
const SFProfileBadgeTab = await compileVueSfc(tabPath, 'sf-profile-badge-tab-worn', {
  computed,
  watch,
  useI18n: () => ({ t: zhI18n.t, te: zhI18n.te, locale: ref('zh-CN') }),
  useLocalePath: () => (path: string) => path,
  useToast: () => ({ add: (toast: Record<string, unknown>) => toastSink.push(toast) }),
  useBadgeWorn,
  useBadgeWearToggle,
  // tab 现在按账号归属判断佩戴状态条；真实组合式函数读的就是被桩掉的 useState。
  useAuthSession,
  badgeWornErrorKey,
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeRequirementText,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle,
  sortProfileBadgeEntries,
  sortProfileBadgeGroups,
  SFBadgeCard,
  SFBadgeDetailModal: BadgeDetailModalStub
})

/** toast 接收器：每次挂载前重置，断言「哪个动作弹了哪条」而不是「弹过 toast」。 */
let toastSink: Array<Record<string, unknown>> = []

const groups = [
  {
    key: 'content',
    name: '内容',
    position: 1,
    badges: [
      { slug: 'helper', name: '优质解答', tier: 'silver', icon: '', tierPosition: 1, position: 1, earned: true, grantedAt: '2026-11-03T08:00:00Z' },
      { slug: 'first-post', name: '初次发帖', tier: 'bronze', icon: '', tierPosition: 2, position: 2, earned: false, current: 3, threshold: 10 }
    ]
  },
  {
    key: 'honor',
    name: '荣誉',
    position: 2,
    badges: [
      { slug: 'liked', name: '广受好评', tier: 'gold', icon: '', tierPosition: 0, position: 1, earned: true, grantedAt: '2026-11-01T08:00:00Z' }
    ]
  }
]

async function mountTab(
  props: Record<string, unknown>,
  options: WornGlobalsOptions = { responses: [{ mode: 'auto', worn: null, automatic: null, upgrade: null }] }
) {
  // tab 与组合式函数共用同一套全局桩：控件行为与请求必须一起断言。
  const globals = globalThis as Record<string, unknown>
  toastSink = []
  const originalState = globals.useState
  const originalApiClient = globals.useApiClient
  const originalI18n = globals.useI18n
  const originalToast = globals.useToast
  const store = createStateStore()
  const calls: Call[] = []
  const queue = [...(options.responses ?? [])]
  globals.useState = store.useState
  // 真实 useBadgeWearToggle 走 Nuxt 自动导入：tab 的 toast/i18n 与它必须共用同一份桩。
  globals.useI18n = () => ({ t: zhI18n.t, te: zhI18n.te, locale: ref('zh-CN') })
  globals.useToast = () => ({ add: (toast: Record<string, unknown>) => toastSink.push(toast) })
  globals.useApiClient = () => ({
    request: async (path: string, init: { method?: string, body?: unknown } = {}) => {
      calls.push({ path, method: init.method ?? 'GET', body: init.body })
      const next = queue.shift()
      if (next instanceof Error) throw next
      return next ?? {}
    }
  })

  const wrapper = mount(SFProfileBadgeTab, {
    props,
    global: { components: { UIcon: UIconStub, NuxtLink: NuxtLinkStub, SFEmptyState: EmptyStateStub } }
  })
  await wrapper.vm.$nextTick()

  return {
    wrapper,
    calls,
    toasts: () => toastSink,
    restore: () => {
      wrapper.unmount()
      globals.useState = originalState
      globals.useApiClient = originalApiClient
      globals.useI18n = originalI18n
      globals.useToast = originalToast
    }
  }
}

describe('SFProfileBadgeTab · 佩戴控件门禁', () => {
  test('a visitor view (self=false) renders no wear control and never asks for worn state', async () => {
    const mounted = await mountTab({ groups })
    try {
      expect(mounted.wrapper.find('.sf-profile-badges__wear').exists()).toBe(false)
      expect(mounted.calls).toEqual([])
    } finally {
      mounted.restore()
    }
  })

  test('the catalog variant has no wear control and opens no detail modal even when self=true', async () => {
    const mounted = await mountTab({
      groups,
      variant: 'catalog',
      self: true
    }, { responses: [{ mode: 'manual', worn: 'helper', automatic: 'liked', upgrade: 'liked' }] })
    try {
      expect(mounted.wrapper.find('.sf-profile-badges__wear').exists()).toBe(false)
      expect(mounted.wrapper.find('.badge-detail-stub').exists()).toBe(false)
      expect(mounted.calls).toEqual([])
    } finally {
      mounted.restore()
    }
  })

  test('self=true shows a wear button on earned cards only', async () => {
    const mounted = await mountTab({ groups, self: true })
    try {
      const earned = mounted.wrapper.get('[data-badge-slug="helper"]')
      const button = earned.get('.sf-profile-badges__wear')
      expect(button.attributes('aria-pressed')).toBe('false')
      expect(button.text()).toBe('佩戴')
      // WCAG 2.5.3：可访问名必须包含可见文字（语音控制念「佩戴」要能点中）。
      // 动作语义放 title，状态放 aria-pressed，不再用 aria-label 顶替可见文字。
      expect(button.attributes('aria-label')).toBeUndefined()
      expect(button.attributes('title')).toBe('佩戴')

      const unearned = mounted.wrapper.get('[data-badge-slug="first-post"]')
      expect(unearned.find('.sf-profile-badges__wear').exists()).toBe(false)
      // 本人视角会拉一次佩戴态。
      expect(mounted.calls).toEqual([{ path: '/badges/worn', method: 'GET', body: undefined }])
    } finally {
      mounted.restore()
    }
  })
})

describe('SFProfileBadgeTab · 佩戴交互与 toast', () => {
  test('hidden state shows 未佩戴徽章, keeps every card unlit, and 自动展示 goes through DELETE', async () => {
    const mounted = await mountTab({ groups, self: true }, {
      responses: [
        { mode: 'hidden', worn: null, automatic: { slug: 'liked' }, upgrade: null },
        { mode: 'auto', worn: { slug: 'liked' }, automatic: { slug: 'liked' }, upgrade: null }
      ]
    })
    try {
      expect(mounted.wrapper.get('.sf-profile-badges__worn-text').text()).toBe('未佩戴徽章')
      // hidden 态不回落：自动徽章不亮佩戴标识。
      expect(mounted.wrapper.find('[data-badge-slug="liked"] .sf-profile-badges__wear').attributes('aria-pressed')).toBe('false')
      // 两个常驻出口：当前态是「不佩戴徽章」。
      expect(mounted.wrapper.get('.sf-profile-badges__worn-auto').text()).toBe('自动展示徽章')
      expect(mounted.wrapper.get('.sf-profile-badges__worn-auto').attributes('aria-pressed')).toBe('false')
      expect(mounted.wrapper.get('.sf-profile-badges__worn-hide').text()).toBe('不佩戴徽章')
      expect(mounted.wrapper.get('.sf-profile-badges__worn-hide').attributes('aria-pressed')).toBe('true')

      await mounted.wrapper.get('.sf-profile-badges__worn-auto').trigger('click')
      await mounted.wrapper.vm.$nextTick()
      expect(mounted.calls.at(-1)).toEqual({ path: '/badges/worn', method: 'DELETE', body: undefined })
      expect(mounted.toasts().at(-1)?.title).toBe('已恢复自动展示徽章')
      // 回到 auto：状态行不再说「未佩戴」，auto 出口按下、自动徽章亮起。
      expect(mounted.wrapper.find('.sf-profile-badges__worn-text').exists()).toBe(false)
      expect(mounted.wrapper.get('.sf-profile-badges__worn-auto').attributes('aria-pressed')).toBe('true')
      expect(mounted.wrapper.get('.sf-profile-badges__worn-hide').attributes('aria-pressed')).toBe('false')
      expect(mounted.wrapper.get('[data-badge-slug="liked"] .sf-profile-badges__wear').attributes('aria-pressed')).toBe('true')
    } finally {
      mounted.restore()
    }
  })

  test('不佩戴徽章 sends PUT {hidden:true} and flips the status line to 未佩戴徽章', async () => {
    const mounted = await mountTab({ groups, self: true }, {
      responses: [
        { mode: 'manual', worn: { slug: 'helper' }, automatic: { slug: 'liked' }, upgrade: null },
        { mode: 'hidden', worn: null, automatic: { slug: 'liked' }, upgrade: null }
      ]
    })
    try {
      expect(mounted.wrapper.get('.sf-profile-badges__worn-text').text()).toBe('当前佩戴：优质解答')
      // manual 不是任何常驻出口的当前态：两颗都不按下。
      expect(mounted.wrapper.get('.sf-profile-badges__worn-auto').attributes('aria-pressed')).toBe('false')
      expect(mounted.wrapper.get('.sf-profile-badges__worn-hide').attributes('aria-pressed')).toBe('false')

      await mounted.wrapper.get('.sf-profile-badges__worn-hide').trigger('click')
      await mounted.wrapper.vm.$nextTick()
      expect(mounted.calls.at(-1)).toEqual({ path: '/badges/worn', method: 'PUT', body: { hidden: true } })
      expect(mounted.toasts().at(-1)?.title).toBe('已不佩戴任何徽章')
      expect(mounted.wrapper.get('.sf-profile-badges__worn-text').text()).toBe('未佩戴徽章')
      // hidden 态：不佩戴徽章那颗按下，已佩戴的卡片全部熄灭。
      expect(mounted.wrapper.get('.sf-profile-badges__worn-hide').attributes('aria-pressed')).toBe('true')
      expect(mounted.wrapper.get('[data-badge-slug="helper"] .sf-profile-badges__wear').attributes('aria-pressed')).toBe('false')
    } finally {
      mounted.restore()
    }
  })

  test('clicking 佩戴 sends PUT, flips the button to 取消佩戴 and toasts the badge name', async () => {
    const mounted = await mountTab({ groups, self: true }, {
      responses: [
        { mode: 'auto', worn: null, automatic: null, upgrade: null },
        { mode: 'manual', worn: { slug: 'helper' }, automatic: null, upgrade: null }
      ]
    })
    try {
      await mounted.wrapper.get('[data-badge-slug="helper"] .sf-profile-badges__wear').trigger('click')
      await mounted.wrapper.vm.$nextTick()

      expect(mounted.calls.at(-1)).toEqual({ path: '/badges/worn', method: 'PUT', body: { slug: 'helper' } })
      const button = mounted.wrapper.get('[data-badge-slug="helper"] .sf-profile-badges__wear')
      expect(button.attributes('aria-pressed')).toBe('true')
      // 可见文字是动作「取消佩戴」，不是状态词「佩戴中」。
      expect(button.text()).toBe('取消佩戴')
      expect(button.attributes('aria-label')).toBeUndefined()
      expect(button.attributes('title')).toBe('取消佩戴')
      expect(mounted.toasts().at(-1)?.title).toBe('已佩戴「优质解答」')
      expect(mounted.toasts().at(-1)?.color).toBe('success')
      // manual 态：底部两颗出口都不按下。
      expect(mounted.wrapper.get('.sf-profile-badges__worn-auto').attributes('aria-pressed')).toBe('false')
      expect(mounted.wrapper.get('.sf-profile-badges__worn-hide').attributes('aria-pressed')).toBe('false')
    } finally {
      mounted.restore()
    }
  })

  test('clicking 取消佩戴 hides every badge with PUT {hidden:true}, never DELETE', async () => {
    const mounted = await mountTab({ groups, self: true }, {
      responses: [
        { mode: 'manual', worn: { slug: 'helper' }, automatic: { slug: 'liked' }, upgrade: null },
        { mode: 'hidden', worn: null, automatic: { slug: 'liked' }, upgrade: null }
      ]
    })
    try {
      await mounted.wrapper.get('[data-badge-slug="helper"] .sf-profile-badges__wear').trigger('click')
      await mounted.wrapper.vm.$nextTick()

      expect(mounted.calls.at(-1)).toEqual({ path: '/badges/worn', method: 'PUT', body: { hidden: true } })
      // 关键回归：取消佩戴不是「回到自动展示」，整轮不许出现 DELETE。
      expect(mounted.calls.filter(call => call.method === 'DELETE')).toHaveLength(0)
      const button = mounted.wrapper.get('[data-badge-slug="helper"] .sf-profile-badges__wear')
      expect(button.attributes('aria-pressed')).toBe('false')
      expect(button.text()).toBe('佩戴')
      expect(mounted.toasts().at(-1)?.title).toBe('已不佩戴任何徽章')
      // hidden 后没有任何一枚卡片亮着：自动徽章也不亮。
      expect(mounted.wrapper.findAll('[data-badge-slug] .sf-profile-badges__wear--active')).toHaveLength(0)
      expect(mounted.wrapper.get('.sf-profile-badges__worn-text').text()).toBe('未佩戴徽章')
    } finally {
      mounted.restore()
    }
  })

  test('a rejected hide rolls the cards back to the previous state and keeps a persistent error toast', async () => {
    const mounted = await mountTab({ groups, self: true }, {
      responses: [
        { mode: 'manual', worn: { slug: 'helper' }, automatic: { slug: 'liked' }, upgrade: null },
        apiError('badge.worn_unavailable', 500)
      ]
    })
    try {
      await mounted.wrapper.get('.sf-profile-badges__worn-hide').trigger('click')
      await mounted.wrapper.vm.$nextTick()

      // 乐观更新已回滚：状态行与卡片都回到点击前（manual + helper），没有任何一枚熄灭。
      expect(mounted.wrapper.get('.sf-profile-badges__worn-text').text()).toBe('当前佩戴：优质解答')
      expect(mounted.wrapper.get('[data-badge-slug="helper"] .sf-profile-badges__wear').attributes('aria-pressed')).toBe('true')
      expect(mounted.wrapper.get('[data-badge-slug="helper"] .sf-profile-badges__wear').text()).toBe('取消佩戴')
      expect(mounted.wrapper.get('.sf-profile-badges__worn-hide').attributes('aria-pressed')).toBe('false')
      // 失败不静默：错误 toast 常驻（duration 0），文案按「保存失败」而不是「未持有」。
      const toast = mounted.toasts().at(-1)
      expect(toast?.title).toBe('徽章佩戴设置保存失败')
      expect(toast?.color).toBe('error')
      expect(toast?.duration).toBe(0)
    } finally {
      mounted.restore()
    }
  })

  test('a rejected wear rolls the button back and surfaces a persistent error toast', async () => {
    const mounted = await mountTab({ groups, self: true }, {
      responses: [
        { mode: 'auto', worn: null, automatic: null, upgrade: null },
        apiError('badge.not_owned')
      ]
    })
    try {
      await mounted.wrapper.get('[data-badge-slug="helper"] .sf-profile-badges__wear').trigger('click')
      await mounted.wrapper.vm.$nextTick()

      const button = mounted.wrapper.get('[data-badge-slug="helper"] .sf-profile-badges__wear')
      expect(button.attributes('aria-pressed')).toBe('false')
      expect(button.text()).toBe('佩戴')
      const toast = mounted.toasts().at(-1)
      expect(toast?.title).toBe('这枚徽章不在你的徽章墙里，无法佩戴')
      expect(toast?.color).toBe('error')
      // 错误 toast 不自动关闭。
      expect(toast?.duration).toBe(0)
    } finally {
      mounted.restore()
    }
  })

  test('the already-active exit is a visible no-op: no request and no toast', async () => {
    const cases = [
      {
        state: { mode: 'auto', worn: { slug: 'liked' }, automatic: { slug: 'liked' }, upgrade: null },
        selector: '.sf-profile-badges__worn-auto'
      },
      {
        state: { mode: 'hidden', worn: null, automatic: { slug: 'liked' }, upgrade: null },
        selector: '.sf-profile-badges__worn-hide'
      }
    ]
    for (const entry of cases) {
      const mounted = await mountTab({ groups, self: true }, { responses: [entry.state] })
      try {
        const button = mounted.wrapper.get(entry.selector)
        // 当前态那颗仍然可聚焦（不是 disabled），只是点了不发请求。
        expect(button.attributes('aria-pressed')).toBe('true')
        expect(button.attributes('disabled')).toBeUndefined()
        const before = mounted.calls.length

        await button.trigger('click')
        await mounted.wrapper.vm.$nextTick()

        expect(mounted.calls).toHaveLength(before)
        expect(mounted.toasts()).toHaveLength(0)
      } finally {
        mounted.restore()
      }
    }
  })

})

describe('badges.worn i18n keys', () => {
  const required = [
    'wear', 'unwear', 'auto', 'hide', 'hiddenEmpty', 'hideSuccess',
    'currentlyWearing', 'notOwned', 'saveFailed', 'wearSuccess', 'autoSuccess'
  ]

  test('every required key exists in both locales', () => {
    for (const key of required) {
      for (const messages of [zh, en]) {
        const value = lookup(messages, `badges.worn.${key}`)
        expect(typeof value).toBe('string')
        expect((value as string).trim().length).toBeGreaterThan(0)
      }
    }
  })

  test('the worn subtree has no key on one side only', () => {
    const zhKeys = Object.keys(lookup(zh, 'badges.worn') as Record<string, unknown>).sort()
    const enKeys = Object.keys(lookup(en, 'badges.worn') as Record<string, unknown>).sort()
    expect(zhKeys).toEqual(enKeys)
  })

  test('the retired state-word keys stay deleted in both locales', () => {
    // 「佩戴中」与「恢复自动展示」是被动作文案取代的旧键：留在 locale 里就会被再次引用，
    // 于是按钮又开始写状态。这条让「删掉实现」真的红。
    for (const key of ['wearing', 'restoreAuto', 'unwearSuccess']) {
      for (const messages of [zh, en]) {
        expect(lookup(messages, `badges.worn.${key}`)).toBeUndefined()
      }
    }
  })
})
