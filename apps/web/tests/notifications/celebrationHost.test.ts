import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, h, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount } from '../helpers/vueSfc'
import {
  celebrationDialogModel,
  celebrationPresentation,
  formatCelebrationTemplate,
  pickCelebrationText,
  type CelebrationItem
} from '../../app/utils/notifications/celebrationPresentation'
import { consumedCursorId, dedupeCelebrationItems, useCelebrations } from '../../app/composables/notifications/useCelebrations'
import { notificationPresentation } from '../../app/utils/notifications/notificationsPresentation'
import {
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle
} from '../../app/utils/forum/forumBadge'
import type { NotificationItem } from '../../app/composables/notifications/useNotifications'

function badgeNotification(payload: Record<string, unknown>): NotificationItem {
  return {
    id: 41,
    type: 'badge_granted',
    targetType: 'badge',
    targetId: 12,
    targetAvailable: true,
    targetPath: '/u/zhuchunshu?tab=badges',
    payload,
    createdAt: '2026-11-13T10:00:00.000Z'
  }
}

/**
 * 庆祝浮卡的契约（wire 已由 Lead 冻结）：
 *
 * - `titleKey` / `bodyKey` 命中才走 i18n，否则按 locale 选 `label` / `body`，选中为空回落另一种。
 * - 模板里缺的参数不许以 `{badgeName}` 字面量出现在界面上。
 * - `total` 大于条目数时用 `celebrations.dialog.aggregate`（数量取 total），弹窗列出全部条目。
 * - `initialized === false` 的首访只 POST 游标初始化，不渲染、不再补拉。
 * - 未登录 / 徽章功能关闭 / 管理端路由 / 无条目时**没有可见 DOM**。
 */
const zh = JSON.parse(readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')) as Record<string, unknown>
const en = JSON.parse(readFileSync(new URL('../../i18n/locales/en-US.json', import.meta.url), 'utf8')) as Record<string, unknown>

function lookup(messages: Record<string, unknown>, path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    messages
  )
}

/** 与 vue-i18n 同口径：缺参的占位符原样保留（此后由 formatCelebrationTemplate 负责收拾）。 */
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
const enI18n = makeI18n(en)

function badgeItem(overrides: Partial<CelebrationItem> = {}): CelebrationItem {
  return {
    id: 126,
    type: 'badge_granted',
    icon: 'i-lucide-award',
    label: { zh: '徽章', en: 'Badge' },
    body: { zh: '你获得了新徽章', en: 'You earned a new badge' },
    titleKey: 'celebrations.badge_granted.title',
    bodyKey: 'celebrations.badge_granted.body',
    params: { badgeName: '常客' },
    targetPath: '/u/zhuchunshu?tab=badges',
    createdAt: '2026-11-13T18:02:11+08:00',
    ...overrides
  }
}

describe('celebrationPresentation · i18n 命中 / 回落 / 参数缺失', () => {
  test('titleKey and bodyKey hits use the locale copy, not the server label', () => {
    const card = celebrationPresentation(badgeItem(), { locale: 'zh-CN', ...zhI18n })
    expect(card.title).toBe('获得新徽章')
    // 徽章名是庆祝的核心信息：主文案必须把它带出来。
    expect(card.body).toBe('你获得了「常客」徽章。')
    expect(card.actionLabel).toBe('查看')
    expect(card.targetPath).toBe('/u/zhuchunshu?tab=badges')
    expect(card.icon).toBe('i-lucide-award')
  })

  test('a key the locale does not know falls back to the server label for the active locale', () => {
    const item = badgeItem({ titleKey: 'celebrations.unknown_type.title', bodyKey: 'celebrations.unknown_type.body' })
    expect(celebrationPresentation(item, { locale: 'zh-CN', ...zhI18n }).title).toBe('徽章')
    expect(celebrationPresentation(item, { locale: 'en-US', ...enI18n }).title).toBe('Badge')
    expect(celebrationPresentation(item, { locale: 'zh-CN', ...zhI18n }).body).toBe('你获得了新徽章')
  })

  test('an empty selected locale falls back to the other one before giving up', () => {
    expect(pickCelebrationText({ zh: '', en: 'Badge' }, 'zh-CN')).toBe('Badge')
    expect(pickCelebrationText({ zh: '徽章', en: '' }, 'en-US')).toBe('徽章')
    expect(pickCelebrationText(undefined, 'zh-CN')).toBe('')
    expect(pickCelebrationText('plain', 'en-US')).toBe('plain')
  })

  test('a satisfied template shows the badge name', () => {
    const card = celebrationPresentation(badgeItem({ params: { badgeName: '常客' } }), { locale: 'zh-CN', ...zhI18n })
    expect(card.body).toBe('你获得了「常客」徽章。')
  })

  test('an unsatisfied template falls back to the server plain text instead of a chopped sentence', () => {
    const card = celebrationPresentation(badgeItem({ params: {} }), { locale: 'zh-CN', ...zhI18n })
    expect(card.body).toBe('你获得了新徽章')
    expect(card.body).not.toContain('{badgeName}')
    // 断句兜底（「你获得了」这种残句）不能出现在主路径上。
    expect(card.body).not.toBe('你获得了')
    expect(card.title).not.toContain('{')

    const english = celebrationPresentation(
      badgeItem({ params: {}, body: { zh: '你获得了新徽章', en: 'You earned a new badge' } }),
      { locale: 'en-US', ...enI18n }
    )
    expect(english.body).toBe('You earned a new badge')
  })

  test('the eaten-placeholder formatter stays as the last line of defence', () => {
    // 服务端连兜底文本都没给：宁可不显示徽章名，也不能把 {badgeName} 画到页面上。
    const placeholderLabel = { zh: '你获得了「{badgeName}」', en: 'You earned "{badgeName}"' }
    const card = celebrationPresentation(
      badgeItem({
        titleKey: 'celebrations.missing.title',
        label: placeholderLabel,
        body: '',
        bodyKey: 'celebrations.missing.body',
        params: {}
      }),
      { locale: 'zh-CN', ...zhI18n }
    )
    expect(card.title).toBe('你获得了')
    expect(card.body).not.toContain('{badgeName}')
  })

  test('a key that resolves to an empty string never becomes an empty title', () => {
    const emptyMessages = {
      ...zh,
      celebrations: {
        ...(zh.celebrations as Record<string, unknown>),
        badge_granted: { title: '', body: '你获得了新徽章' }
      }
    }
    const blank = makeI18n(emptyMessages)
    const card = celebrationPresentation(badgeItem(), { locale: 'zh-CN', ...blank })
    expect(card.title).not.toBe('')
    // 空译文当未命中：回落到服务端 label（「徽章」）而不是渲染空标题。
    expect(card.title).toBe('徽章')
  })

  test('everything empty yields no dialog at all instead of a blank panel', () => {
    const emptyI18n = { t: (key: string) => '', te: () => false }
    expect(celebrationDialogModel([{ id: 1, type: 'x', label: '', body: '' }], 1, { locale: 'zh-CN', ...emptyI18n })).toBeNull()
  })

  test('formatCelebrationTemplate fills what it has and drops empty quoting', () => {
    expect(formatCelebrationTemplate('获得「{badgeName}」', { badgeName: '常客' })).toBe('获得「常客」')
    expect(formatCelebrationTemplate('获得「{badgeName}」', {})).toBe('获得')
    expect(formatCelebrationTemplate('获得 {badgeName} 枚', { badgeName: '  ' })).toBe('获得 枚')
    expect(formatCelebrationTemplate('获得 {count} 枚新徽章', { count: 3 })).toBe('获得 3 枚新徽章')
  })

  test('the shipped badge body copy interpolates badgeName in both locales', () => {
    expect(lookup(zh, 'celebrations.badge_granted.body')).toContain('{badgeName}')
    expect(lookup(en, 'celebrations.badge_granted.body')).toContain('{badgeName}')
  })

  test('the wire shape the backend actually emits (null keys on plugin types) stays readable', () => {
    // 后端 `Notifications/celebrations.go` 的真实形状：插件类型 titleKey/bodyKey 是 null，
    // label/body 是 {zh,en}，params 是空 map，targetPath 也可能是 null。
    const plugin = celebrationPresentation({
      id: 12,
      type: 'plugin_daily_streak',
      icon: 'i-tabler-flame',
      label: { zh: '连续打卡', en: 'Daily streak' },
      body: { zh: '你已连续打卡 30 天', en: 'You reached a 30-day streak' },
      titleKey: null,
      bodyKey: null,
      params: {},
      targetPath: null,
      createdAt: '2026-11-13T18:02:11+08:00'
    }, { locale: 'zh-CN', ...zhI18n })
    expect(plugin.title).toBe('连续打卡')
    expect(plugin.body).toBe('你已连续打卡 30 天')
    expect(plugin.targetPath).toBe('')

    // Core 徽章的 body 兜底句（后端为缺参场景准备的那句）会被用上。
    const coreFallback = celebrationPresentation({
      id: 13,
      type: 'badge_granted',
      icon: 'i-lucide-award',
      label: { zh: '徽章', en: 'Badge' },
      body: { zh: '你获得了新徽章', en: 'You earned a new badge' },
      titleKey: 'celebrations.badge_granted.title',
      bodyKey: 'celebrations.badge_granted.body',
      params: {},
      targetPath: '/u/zhuchunshu?tab=badges'
    }, { locale: 'zh-CN', ...zhI18n })
    expect(coreFallback.body).toBe('你获得了新徽章')
    expect(coreFallback.targetPath).toBe('/u/zhuchunshu?tab=badges')
  })

  test('a plugin type with no locale keys and no server copy still gets readable text', () => {
    const card = celebrationPresentation({
      id: 9,
      type: 'plugin_daily_streak',
      label: '',
      body: '',
      targetPath: ''
    }, { locale: 'zh-CN', ...zhI18n })
    expect(card.title).toBe('新成就')
    expect(card.body).toBe('有新的成就可以查看。')
    // 没有站内绝对路径就不渲染成链接：actionLabel 为空、targetPath 为空。
    expect(card.targetPath).toBe('')
    expect(card.actionLabel).toBe('')
  })

  test('a non-absolute targetPath is dropped instead of becoming a broken link', () => {
    expect(celebrationPresentation(badgeItem({ targetPath: 'javascript:alert(1)' }), { locale: 'zh-CN', ...zhI18n }).targetPath).toBe('')
    expect(celebrationPresentation(badgeItem({ targetPath: '' }), { locale: 'zh-CN', ...zhI18n }).actionLabel).toBe('')
  })
})

describe('notificationPresentation · badge_granted 不再画裸键', () => {
  test('the inbox label comes from payload.badgeName and all three keys exist', () => {
    const presented = notificationPresentation(badgeNotification({ badgeName: '常客', badgeId: 12 }))
    expect(presented.targetLabel).toBe('常客')
    expect(presented.typeLabelKey).toBe('notifications.filter.badge_granted')
    expect(presented.titleKey).toBe('notifications.types.badge_granted')
    expect(presented.bodyKey).toBe('notifications.body.badge_granted')
    expect(presented.icon).toBe('i-tabler-award')
    expect(presented.target.path).toBe('/u/zhuchunshu?tab=badges')
    // 三个键都必须真在 locale 里，否则页面会画 `notifications.types.badge_granted` 字面量。
    for (const key of [presented.typeLabelKey, presented.titleKey, presented.bodyKey]) {
      expect(typeof lookup(zh, key)).toBe('string')
      expect(typeof lookup(en, key)).toBe('string')
    }
  })

  test('a payload without badgeName falls back to the generic target label, not a bare key', () => {
    const presented = notificationPresentation(badgeNotification({ badgeId: 12 }))
    // 通用回落顺序不变（targetType #targetId），徽章分支只在有 badgeName 时抢先。
    expect(presented.targetLabel).toBe('badge #12')
    expect(presented.bodyKey).toBe('notifications.body.badge_granted')
    expect(presented.bodyParams.target).toBe('badge #12')
    expect(presented.targetLabel).not.toContain('notifications.')
  })

  test('other types keep the old label order (topic title wins)', () => {
    const presented = notificationPresentation({
      id: 42,
      type: 'reply',
      targetType: 'comment',
      targetId: 9,
      payload: { title: '主题标题', badgeName: '不该出现' },
      createdAt: '2026-11-13T10:00:00.000Z'
    })
    expect(presented.targetLabel).toBe('主题标题')
  })
})

describe('celebrationDialogModel · 聚合口径', () => {
  test('one item and total 1 keeps its own title, adds no aggregate copy and lists one row', () => {
    const dialog = celebrationDialogModel([badgeItem()], 1, { locale: 'zh-CN', ...zhI18n })
    expect(dialog?.count).toBe(1)
    expect(dialog?.title).toBe('获得新徽章')
    expect(dialog?.title).not.toContain('枚新徽章')
    // 单条不重复：正文只在行里出现一次，不额外加聚合说明。
    expect(dialog?.description).toBe('')
    expect(dialog?.rows).toHaveLength(1)
    expect(dialog?.rows[0]?.text).toBe('你获得了「常客」徽章。')
    expect(dialog?.actionLabel).toBe('查看我的徽章')
    expect(dialog?.targetPath).toBe('/u/zhuchunshu?tab=badges')
  })

  test('total greater than the visible items aggregates the title and lists every loaded item', () => {
    const dialog = celebrationDialogModel([badgeItem()], 3, { locale: 'zh-CN', ...zhI18n })
    expect(dialog?.title).toBe('获得 3 枚新徽章')
    expect(dialog?.description).toBe('这 3 枚徽章已经加入你的徽章墙。')
    expect(dialog?.count).toBe(3)
    expect(dialog?.rows).toHaveLength(1)
  })

  test('several loaded items also aggregate, using their own count when total is missing', () => {
    const items = [badgeItem({ id: 1, params: { badgeName: '常客' } }), badgeItem({ id: 2, params: { badgeName: '笔耕不辍' } })]
    const zhDialog = celebrationDialogModel(items, 0, { locale: 'zh-CN', ...zhI18n })
    expect(zhDialog?.count).toBe(2)
    expect(zhDialog?.title).toBe('获得 2 枚新徽章')
    expect(zhDialog?.rows.map(row => row.text)).toEqual([
      '你获得了「常客」徽章。',
      '你获得了「笔耕不辍」徽章。'
    ])
    expect(zhDialog?.rows.map(row => row.id)).toEqual([1, 2])

    expect(celebrationDialogModel(items, undefined, { locale: 'en-US', ...enI18n })?.title)
      .toBe('You earned 2 new badges')
  })

  test('no items yields null so the host renders nothing at all', () => {
    expect(celebrationDialogModel([], 3, { locale: 'zh-CN', ...zhI18n })).toBeNull()
    expect(celebrationDialogModel(undefined as unknown as CelebrationItem[], 0, { locale: 'zh-CN', ...zhI18n })).toBeNull()
  })

  test('a target path is only offered when some item really has one', () => {
    const noTarget = celebrationDialogModel([badgeItem({ targetPath: '' })], 1, { locale: 'zh-CN', ...zhI18n })
    expect(noTarget?.targetPath).toBe('')
    expect(noTarget?.actionLabel).toBe('')

    const mixed = celebrationDialogModel(
      [badgeItem({ id: 1, targetPath: '' }), badgeItem({ id: 2, targetPath: '/badges' })],
      2,
      { locale: 'zh-CN', ...zhI18n }
    )
    expect(mixed?.targetPath).toBe('/badges')
  })
})

describe('celebrations i18n keys · zh/en 双向齐全', () => {
  const required = [
    'notifications.types.badge_granted',
    'notifications.filter.badge_granted',
    'notifications.body.badge_granted',
    'celebrations.types.badge_granted',
    'celebrations.badge_granted.title',
    'celebrations.badge_granted.body',
    'celebrations.plugin.title',
    'celebrations.plugin.body',
    'celebrations.dialog.title',
    'celebrations.dialog.aggregate',
    'celebrations.dialog.aggregateBody',
    'celebrations.dialog.view',
    'celebrations.dismiss',
    'celebrations.view'
  ]

  for (const key of required) {
    test(`${key} exists in both locales and is not empty`, () => {
      for (const messages of [zh, en]) {
        const value = lookup(messages, key)
        expect(typeof value).toBe('string')
        expect((value as string).trim().length).toBeGreaterThan(0)
      }
    })
  }

  test('the celebrations subtree has no key on one side only', () => {
    const flatten = (node: unknown, prefix: string): string[] => {
      if (!node || typeof node !== 'object') return [prefix]
      return Object.entries(node as Record<string, unknown>)
        .flatMap(([key, value]) => flatten(value, prefix ? `${prefix}.${key}` : key))
    }
    const zhKeys = flatten(lookup(zh, 'celebrations'), '').sort()
    const enKeys = flatten(lookup(en, 'celebrations'), '').sort()
    expect(zhKeys).toEqual(enKeys)
  })

  test('dialog aggregate copy interpolates count', () => {
    expect(lookup(zh, 'celebrations.dialog.aggregate')).toContain('{count}')
    expect(lookup(en, 'celebrations.dialog.aggregate')).toContain('{count}')
    expect(lookup(zh, 'celebrations.dialog.aggregateBody')).toContain('{count}')
    expect(lookup(en, 'celebrations.dialog.aggregateBody')).toContain('{count}')
  })
})

/* ------------------------------------------------------------------------- */
/* 组件门禁：宿主在四种情况下必须完全不产出可见 DOM                            */
/* ------------------------------------------------------------------------- */

const userRef = ref<{ id: number, username: string } | null>(null)
const badgesEnabledRef = ref(true)
const routePathRef = ref('/')
const celebrationsState = ref<{ initialized: boolean, cursorId: number, total: number, items: CelebrationItem[] }>({
  initialized: true,
  // 生产形状：cursorId 是读取前游标，条目 id 恒大于它。
  cursorId: 120,
  total: 0,
  items: []
})
const refreshCalls: number[] = []
const dismissCalls: number[] = []
const navigations: string[] = []

/** 徽章墙请求的替身：按顺序消费响应（对象 = 成功，Error = 抛错），并记录路径。 */
type WallCall = { path: string }
const wallCalls: WallCall[] = []
/** 对象 = 成功响应；Error = 抛错；Promise = 在途（换号场景要手工 resolve）。 */
const wallResponses: Array<Record<string, unknown> | Error | Promise<Record<string, unknown>>> = []

const clientOnlyStub = defineComponent({
  name: 'ClientOnly',
  setup(_props, { slots }) {
    return () => h('div', { class: 'client-only-stub' }, slots.default?.())
  }
})

const UIconStub = defineComponent({
  name: 'UIcon',
  props: { name: { type: String, default: '' } },
  template: '<i :data-icon="name" />'
})

/**
 * UModal 桩：`open=false` 时**不产出 DOM**（与真实 DialogRoot 关闭即卸载一致），
 * 并且把 `update:open` 透出来 —— Esc / 遮罩 / 关闭按钮在真实实现里都收敛到这一个事件。
 */
const UModalStub = defineComponent({
  name: 'UModal',
  props: {
    open: { type: Boolean, default: false },
    title: { type: String, default: '' },
    ui: { type: Object, default: () => ({}) }
  },
  emits: ['update:open'],
  setup(props, { slots }) {
    return () => (props.open
      ? h('div', { class: 'umodal-stub', 'data-title': props.title }, slots.content?.())
      : null)
  }
})

/**
 * 信息块编译真组件：庆祝弹窗「命中时」渲染的就是它，用桩就证明不了两边同源。
 */
const bodyPath = fileURLToPath(new URL('../../app/components/forum/badges/SFBadgeDetailBody.vue', import.meta.url))
const SFBadgeDetailBody = await compileVueSfc(bodyPath, 'sf-badge-detail-body-celebration', {
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

const hostPath = fileURLToPath(new URL('../../app/components/notifications/SFCelebrationHost.vue', import.meta.url))
const SFCelebrationHost = await compileVueSfc(hostPath, 'sf-celebration-host', {
  computed,
  ref,
  watch,
  onMounted,
  onBeforeUnmount,
  useI18n: () => ({ t: zhI18n.t, te: zhI18n.te, locale: ref('zh-CN') }),
  useAuthSession: () => ({ user: userRef }),
  useWebOptions: () => ({ badgesEnabled: badgesEnabledRef }),
  useAdminRoutes: () => ({ routeId: (path: string) => (path.startsWith('/control-panel') ? 'site' : null) }),
  useRoute: () => ({ path: routePathRef.value }),
  useCelebrations: () => ({
    state: celebrationsState,
    refresh: async () => {
      refreshCalls.push(1)
      return celebrationsState.value
    },
    dismiss: async () => {
      dismissCalls.push(1)
    }
  }),
  celebrationDialogModel,
  navigateTo: (path: string) => {
    navigations.push(path)
  },
  useApiClient: () => ({
    request: async (path: string) => {
      wallCalls.push({ path })
      const next = wallResponses.shift()
      if (next instanceof Error) throw next
      if (next instanceof Promise) return await next
      return next ?? { groups: [] }
    }
  }),
  SFBadgeDetailBody
})

function mountHost() {
  return mount(SFCelebrationHost, {
    global: {
      components: {
        UIcon: UIconStub,
        ClientOnly: clientOnlyStub,
        UModal: UModalStub
      }
    }
  })
}

function resetHostState() {
  userRef.value = { id: 7, username: 'zhuchunshu' }
  badgesEnabledRef.value = true
  routePathRef.value = '/'
  celebrationsState.value = { initialized: true, cursorId: 120, total: 0, items: [] }
  dismissCalls.length = 0
  navigations.length = 0
  wallCalls.length = 0
  wallResponses.length = 0
}

describe('SFCelebrationHost · 渲染门禁', () => {
  test('no item renders no celebration DOM at all', () => {
    resetHostState()
    const wrapper = mountHost()
    try {
      expect(wrapper.find('.sf-celebration').exists()).toBe(false)
      expect(wrapper.find('.umodal-stub').exists()).toBe(false)
      expect(wrapper.find('[role="status"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('a guest never mounts the dialog', () => {
    resetHostState()
    userRef.value = null
    celebrationsState.value = { initialized: true, cursorId: 120, total: 1, items: [badgeItem()] }
    const wrapper = mountHost()
    try {
      expect(wrapper.find('.sf-celebration').exists()).toBe(false)
      expect(wrapper.find('.umodal-stub').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('badges disabled hides the dialog even with items in state', () => {
    resetHostState()
    badgesEnabledRef.value = false
    celebrationsState.value = { initialized: true, cursorId: 120, total: 1, items: [badgeItem()] }
    const wrapper = mountHost()
    try {
      expect(wrapper.find('.sf-celebration').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('an admin route hides the dialog', () => {
    resetHostState()
    routePathRef.value = '/control-panel/settings'
    celebrationsState.value = { initialized: true, cursorId: 120, total: 1, items: [badgeItem()] }
    const wrapper = mountHost()
    try {
      expect(wrapper.find('.sf-celebration').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('a hidden document hides the dialog until it is visible again', async () => {
    resetHostState()
    celebrationsState.value = { initialized: true, cursorId: 120, total: 1, items: [badgeItem()] }
    const descriptor = Object.getOwnPropertyDescriptor(Document.prototype, 'visibilityState')
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
    const wrapper = mountHost()
    try {
      // onMounted 里的同步读取会触发一次重算，DOM 更新在 nextTick 才落地。
      await wrapper.vm.$nextTick()
      expect(wrapper.find('.sf-celebration').exists()).toBe(false)
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
      document.dispatchEvent(new Event('visibilitychange'))
      await wrapper.vm.$nextTick()
      expect(wrapper.find('.sf-celebration').exists()).toBe(true)
    } finally {
      wrapper.unmount()
      if (descriptor) Object.defineProperty(Document.prototype, 'visibilityState', descriptor)
      delete (document as unknown as Record<string, unknown>).visibilityState
    }
  })
})

describe('SFCelebrationHost · 弹窗行为', () => {
  test('one dialog with the aggregate title, the rows and a localized close label', async () => {
    resetHostState()
    celebrationsState.value = { initialized: true, cursorId: 120, total: 3, items: [badgeItem()] }
    refreshCalls.length = 0
    const wrapper = mountHost()
    try {
      expect(wrapper.findAll('.umodal-stub')).toHaveLength(1)
      // 标题同时交给 UModal（可访问名字）与可见标题，两处必须一致。
      expect(wrapper.get('.umodal-stub').attributes('data-title')).toBe('获得 3 枚新徽章')
      expect(wrapper.get('.sf-celebration__title').text()).toBe('获得 3 枚新徽章')
      expect(wrapper.get('.sf-celebration__description').text()).toBe('这 3 枚徽章已经加入你的徽章墙。')
      expect(wrapper.findAll('.sf-celebration__row')).toHaveLength(1)
      expect(wrapper.get('.sf-celebration__row-text').text()).toBe('你获得了「常客」徽章。')
      expect(wrapper.get('.sf-celebration__close').attributes('aria-label')).toBe('关闭庆祝提示')
      expect(wrapper.get('.sf-celebration__primary').text()).toBe('查看我的徽章')
      // 挂载时拉一次：登录态成立才拉。
      expect(refreshCalls).toHaveLength(1)

      await wrapper.get('.sf-celebration__close').trigger('click')
      expect(dismissCalls).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })

  test('Esc and the overlay close through the same dismiss path', async () => {
    resetHostState()
    celebrationsState.value = { initialized: true, cursorId: 120, total: 1, items: [badgeItem()] }
    const wrapper = mountHost()
    try {
      const modal = wrapper.getComponent(UModalStub)
      modal.vm.$emit('update:open', false)
      await wrapper.vm.$nextTick()
      expect(dismissCalls).toHaveLength(1)

      // 打开事件不 ack：只有「关」才推进游标。
      modal.vm.$emit('update:open', true)
      await wrapper.vm.$nextTick()
      expect(dismissCalls).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })

  test('the primary action dismisses first and then navigates to the target', async () => {
    resetHostState()
    celebrationsState.value = { initialized: true, cursorId: 120, total: 1, items: [badgeItem()] }
    const wrapper = mountHost()
    try {
      await wrapper.get('.sf-celebration__primary').trigger('click')
      expect(dismissCalls).toHaveLength(1)
      expect(navigations).toEqual(['/u/zhuchunshu?tab=badges'])
    } finally {
      wrapper.unmount()
    }
  })

  test('an item without a target path renders no primary action', () => {
    resetHostState()
    celebrationsState.value = { initialized: true, cursorId: 120, total: 1, items: [badgeItem({ targetPath: '' })] }
    const wrapper = mountHost()
    try {
      expect(wrapper.find('.sf-celebration__primary').exists()).toBe(false)
      expect(wrapper.get('.sf-celebration__title').text()).toBe('获得新徽章')
    } finally {
      wrapper.unmount()
    }
  })
})

/* ------------------------------------------------------------------------- */
/* 数据层：首访初始化、游标 ack、去重                                          */
/* ------------------------------------------------------------------------- */

type RequestCall = { path: string, method: string, body?: unknown }

function createStateStore() {
  const state = new Map<string, ReturnType<typeof ref>>()
  return {
    useState<T>(key: string, init: () => T) {
      if (!state.has(key)) state.set(key, ref(init()))
      return state.get(key) as ReturnType<typeof ref<T>>
    }
  }
}

async function withCelebrationGlobals(
  options: { user?: { id: number } | null, pages?: Array<Record<string, unknown>> },
  run: (calls: RequestCall[]) => Promise<void>
) {
  // 组合式函数走 Nuxt 自动导入：测试里只能打到 globalThis 上的同名桩。
  const globals = globalThis as Record<string, unknown>
  const originalState = globals.useState
  const originalApiClient = globals.useApiClient
  const store = createStateStore()
  const calls: RequestCall[] = []
  const pages = [...(options.pages ?? [])]

  // 真实的 useAuthSession / useNotifications 都从 useState 取状态：
  // 给同一个 store 播种，就等于给整条生产链路装上登录态，不用替换 composable。
  const authUser = store.useState<{ id: number } | null>('auth:user', () => null)
  authUser.value = options.user === undefined ? { id: 7 } : options.user

  globals.useState = store.useState
  globals.useApiClient = () => ({
    request: async (path: string, init: { method?: string, body?: unknown } = {}) => {
      calls.push({ path, method: init.method ?? 'GET', body: init.body })
      if (path === '/notifications/celebrations') {
        const page = pages.shift()
        if (!page) throw new Error('unexpected celebrations GET')
        return page
      }
      return { cursorId: (init.body as { cursorId?: number } | undefined)?.cursorId ?? 0 }
    }
  })

  try {
    await run(calls)
  } finally {
    globals.useState = originalState
    globals.useApiClient = originalApiClient
  }
}

describe('useCelebrations', () => {
  test('first visit (initialized=false) only initializes the cursor: no card, no second fetch', async () => {
    await withCelebrationGlobals({
      pages: [{ initialized: false, cursorId: 128, total: 0, items: [] }]
    }, async (calls) => {
      const celebrations = useCelebrations()
      await celebrations.refresh()
      expect(calls.map(call => `${call.method} ${call.path}`)).toEqual([
        'GET /notifications/celebrations',
        'POST /notifications/celebrations/seen'
      ])
      expect(calls[1]?.body).toEqual({ cursorId: 128 })
      expect(celebrations.items.value).toEqual([])
      expect(celebrations.state.value.initialized).toBe(true)
    })
  })

  test('a normal page stores deduped items and the pre-read server cursor', async () => {
    // 生产形状：`cursorId` 是**读取前**的游标，条目按 `id > cursorId` 取出，所以 id 恒大于它。
    await withCelebrationGlobals({
      pages: [{
        initialized: true,
        cursorId: 120,
        total: 3,
        items: [badgeItem({ id: 130 }), badgeItem({ id: 130 }), badgeItem({ id: 129 })]
      }]
    }, async () => {
      const celebrations = useCelebrations()
      await celebrations.refresh()
      expect(celebrations.items.value.map(item => item.id)).toEqual([130, 129])
      expect(celebrations.state.value.total).toBe(3)
      expect(celebrations.cursorId.value).toBe(120)
    })
  })

  test('dismiss acks the largest consumed item id, not the pre-read cursor', async () => {
    await withCelebrationGlobals({
      pages: [{ initialized: true, cursorId: 70, total: 1, items: [badgeItem({ id: 77 })] }]
    }, async (calls) => {
      const celebrations = useCelebrations()
      await celebrations.refresh()
      await celebrations.dismiss()
      expect(celebrations.items.value).toEqual([])
      // ack 70（= cursorId）在 GREATEST upsert 下是恒等 no-op：必须 ack 已消费的 77。
      expect(calls.at(-1)).toEqual({
        path: '/notifications/celebrations/seen',
        method: 'POST',
        body: { cursorId: 77 }
      })
    })
  })

  test('the next refresh after dismiss no longer returns the same batch', async () => {
    // 服务端按 `id > ack 游标` 取条目：第一次读到 77，ack 77 之后第二次就该是空的。
    // 用 fake 响应模拟这两次读，确保「关闭 → 实时推送 → 又弹同一批」不再复现。
    await withCelebrationGlobals({
      pages: [
        { initialized: true, cursorId: 70, total: 1, items: [badgeItem({ id: 77 })] },
        { initialized: true, cursorId: 77, total: 0, items: [] }
      ]
    }, async (calls) => {
      const celebrations = useCelebrations()
      await celebrations.refresh()
      expect(celebrations.items.value.map(item => item.id)).toEqual([77])

      await celebrations.dismiss()
      const ackedCursor = (calls.at(-1)?.body as { cursorId?: number } | undefined)?.cursorId
      expect(ackedCursor).toBe(77)

      await celebrations.refresh()
      expect(celebrations.items.value).toEqual([])
      expect(celebrations.state.value.total).toBe(0)
      // 第二次 GET 的游标已经是 ack 之后的值，服务端不会再给出同一批。
      expect(celebrations.cursorId.value).toBe(77)
    })
  })

  test('a guest never calls the API and keeps an empty queue', async () => {
    await withCelebrationGlobals({ user: null }, async (calls) => {
      const celebrations = useCelebrations()
      await celebrations.refresh()
      expect(calls).toEqual([])
      expect(celebrations.items.value).toEqual([])
    })
  })

  test('a failed fetch stays silent and keeps the previous queue', async () => {
    await withCelebrationGlobals({ pages: [] }, async () => {
      const celebrations = useCelebrations()
      // 没有排队响应 → 请求抛错；结果必须是空队列而不是抛出。
      await celebrations.refresh()
      expect(celebrations.items.value).toEqual([])
      expect(celebrations.state.value.initialized).toBe(true)
    })
  })

  test('consumedCursorId takes the max of the pre-read cursor and the queued item ids', () => {
    expect(consumedCursorId({ cursorId: 70, items: [badgeItem({ id: 77 }), badgeItem({ id: 75 })] })).toBe(77)
    expect(consumedCursorId({ cursorId: 70, items: [] })).toBe(70)
    expect(consumedCursorId({ cursorId: 0, items: [] })).toBe(0)
    // 脏数据不能把游标推成 NaN / 负数，也不能因为一条坏条目丢掉其余条目。
    expect(consumedCursorId({ cursorId: 70, items: [badgeItem({ id: 0 }), badgeItem({ id: 81 })] })).toBe(81)
    expect(consumedCursorId({ cursorId: 70, items: [{ id: Number.NaN } as CelebrationItem] })).toBe(70)
  })

  test('dedupeCelebrationItems drops entries without a usable id', () => {
    expect(dedupeCelebrationItems([
      badgeItem({ id: 0 }),
      badgeItem({ id: 5 }),
      badgeItem({ id: 5 })
    ]).map(item => item.id)).toEqual([5])
    expect(dedupeCelebrationItems(undefined)).toEqual([])
  })
})

/* ------------------------------------------------------------------------- */
/* 徽章信息块：命中 → 与详情弹窗同源；命中不了 → 回落纯文本                     */
/* ------------------------------------------------------------------------- */

/** `/profiles/{username}/badges` 的同形响应（字段与 `ProfileBadgeGroup` 一致）。 */
function badgeWall(overrides: Record<string, unknown> = {}) {
  return {
    groups: [{
      key: 'content',
      name: '内容',
      position: 1,
      badges: [{
        slug: 'regular',
        name: '常客',
        tier: 'gold',
        tierName: { zh: '金牌', en: 'Gold' },
        tierColor: 'amber',
        icon: 'i-lucide-crown',
        position: 0,
        earned: true,
        grantedAt: '2026-11-13T10:00:00Z',
        description: '连续 7 天在站内发帖。',
        requirementText: { zh: '连续 7 天访问站点', en: 'Visit the site 7 days in a row' },
        ...overrides
      }]
    }]
  }
}

/** 带 `params.badgeSlug` 的庆祝条目（后端 task-10 的字段）。 */
function badgeItemWithSlug(
  slug = 'regular',
  overrides: Partial<CelebrationItem> = {},
  badgeName = '常客'
): CelebrationItem {
  return badgeItem({ params: { badgeName, badgeSlug: slug }, ...overrides })
}

/**
 * 生产形状的徽章墙条目（`/profiles/{me}/badges` 的 `ProfileBadgeItem`）。
 *
 * 这个端点的语义是「**全部启用徽章**」，不是「我获得了哪些」：未获得的条目
 * `earned:false` + `current/threshold` + 没有 `grantedAt`，而且**同样带 slug**。
 * 夹具必须保留这个形状，否则「第二枚徽章不补拉」的 bug 根本测不出来。
 */
const REGULAR_EARNED = {
  slug: 'regular',
  name: '常客',
  tier: 'gold',
  tierName: { zh: '金牌', en: 'Gold' },
  tierColor: 'amber',
  icon: 'i-lucide-crown',
  position: 0,
  earned: true,
  grantedAt: '2026-11-13T10:00:00Z',
  description: '连续 7 天在站内发帖。',
  requirementText: { zh: '连续 7 天访问站点', en: 'Visit the site 7 days in a row' }
}

/** 同一枚徽章在「还没拿到」时的样子：有进度阈值、没有获得时间。 */
const VETERAN_UNEARNED = {
  slug: 'veteran',
  name: '资深会员',
  tier: 'silver',
  tierName: { zh: '银牌', en: 'Silver' },
  tierColor: 'slate',
  icon: 'i-lucide-medal',
  position: 1,
  earned: false,
  current: 3,
  threshold: 10,
  description: '加入站点满一年。',
  requirementText: { zh: '注册满 365 天', en: 'Be a member for 365 days' }
}

/** 同一枚徽章被授予之后：earned 翻真、进度字段消失、出现获得时间。 */
const VETERAN_EARNED = {
  ...VETERAN_UNEARNED,
  earned: true,
  current: undefined,
  threshold: undefined,
  grantedAt: '2026-11-13T11:00:00Z'
}

function wallOf(...badges: Array<Record<string, unknown>>) {
  return { groups: [{ key: 'content', name: '内容', position: 1, badges }] }
}

describe('SFCelebrationHost · 徽章信息块', () => {
  test('a hit renders the shared info block instead of the plain prose row', async () => {
    resetHostState()
    wallResponses.push(badgeWall())
    celebrationsState.value = { initialized: true, cursorId: 120, total: 1, items: [badgeItemWithSlug()] }
    const wrapper = mountHost()
    try {
      await flushPromises()

      // 只在真的有带 slug 的条目时才查徽章墙，且只查一次。
      expect(wallCalls.map(call => call.path)).toEqual(['/profiles/zhuchunshu/badges'])

      const block = wrapper.get('.sf-badge-detail-body')
      expect(block.attributes('data-badge-slug')).toBe('regular')
      expect(wrapper.get('.sf-badge-detail-body__name').text()).toBe('常客')
      expect(wrapper.get('.sf-badge-detail-body__tier').text()).toBe('金牌')
      expect(wrapper.get('.sf-badge-detail-body__group').text()).toBe('内容')
      expect(wrapper.text()).toContain('连续 7 天在站内发帖。')
      expect(wrapper.text()).toContain('连续 7 天访问站点')
      expect(wrapper.text()).toContain('获得于 2026-11-13')

      // 标题层级不跳级：弹窗标题 h2 → 条目徽章名 h3 → 字段标签 h4。
      expect(wrapper.get('.sf-celebration__title').element.tagName).toBe('H2')
      expect(wrapper.get('.sf-badge-detail-body__name').element.tagName).toBe('H3')
      for (const label of wrapper.findAll('.sf-badge-detail-body__label')) {
        expect(label.element.tagName).toBe('H4')
      }

      // 信息块里已经有名称：不再重复「你获得了「常客」徽章。」那句。
      expect(wrapper.text()).not.toContain('你获得了「常客」徽章。')
      expect(wrapper.find('.sf-celebration__row--text').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('several hits render one block each, separated as list items', async () => {
    resetHostState()
    wallResponses.push(badgeWall())
    celebrationsState.value = {
      initialized: true,
      cursorId: 120,
      total: 2,
      items: [badgeItemWithSlug('regular', { id: 126 }), badgeItemWithSlug('regular', { id: 127 })]
    }
    const wrapper = mountHost()
    try {
      await flushPromises()
      const rows = wrapper.findAll('.sf-celebration__row')
      expect(rows).toHaveLength(2)
      expect(rows.every(row => row.classes().includes('sf-celebration__row--badge'))).toBe(true)
      expect(wrapper.findAll('.sf-badge-detail-body')).toHaveLength(2)
      // 两条命中：正文聚合说明保留，逐条的那句「你获得了…」不重复。
      expect(wrapper.text()).not.toContain('你获得了「常客」徽章。')
    } finally {
      wrapper.unmount()
    }
  })

  test('a slug the wall does not know falls back to the plain prose row', async () => {
    resetHostState()
    // 徽章墙里只有别的徽章：这个 slug 已停用/删除。
    wallResponses.push(badgeWall({ slug: 'retired' }))
    celebrationsState.value = { initialized: true, cursorId: 120, total: 1, items: [badgeItemWithSlug()] }
    const wrapper = mountHost()
    try {
      await flushPromises()
      expect(wrapper.find('.sf-badge-detail-body').exists()).toBe(false)
      expect(wrapper.get('.sf-celebration__row-text').text()).toBe('你获得了「常客」徽章。')
    } finally {
      wrapper.unmount()
    }
  })

  test('an old notification without params.badgeSlug never asks for the wall', async () => {
    resetHostState()
    celebrationsState.value = { initialized: true, cursorId: 120, total: 1, items: [badgeItem()] }
    const wrapper = mountHost()
    try {
      await flushPromises()
      expect(wallCalls).toEqual([])
      expect(wrapper.find('.sf-badge-detail-body').exists()).toBe(false)
      expect(wrapper.get('.sf-celebration__row-text').text()).toBe('你获得了「常客」徽章。')
    } finally {
      wrapper.unmount()
    }
  })

  test('a failed wall request stays silent and still renders the fallback row', async () => {
    resetHostState()
    wallResponses.push(new Error('offline'))
    celebrationsState.value = { initialized: true, cursorId: 120, total: 1, items: [badgeItemWithSlug()] }
    const wrapper = mountHost()
    try {
      await flushPromises()
      expect(wallCalls).toHaveLength(1)
      expect(wrapper.find('.sf-badge-detail-body').exists()).toBe(false)
      expect(wrapper.get('.sf-celebration__row-text').text()).toBe('你获得了「常客」徽章。')
    } finally {
      wrapper.unmount()
    }
  })

  test('the second badge is refetched even though the first snapshot already lists its slug', async () => {
    resetHostState()
    // 生产形状：第一份快照里就有 `veteran`，但它是 earned:false（带阈值、无获得时间）。
    // 「有 slug」不等于「有可信资料」—— 把未获得条目当命中，第二枚徽章到手时就不会补拉，
    // 信息块会按未获得渲染：冒出进度条、丢掉获得时间（用户明确要求的六项之一）。
    wallResponses.push(wallOf(REGULAR_EARNED, VETERAN_UNEARNED))
    celebrationsState.value = {
      initialized: true,
      cursorId: 120,
      total: 1,
      items: [badgeItemWithSlug('regular', { id: 126 })]
    }
    const wrapper = mountHost()
    try {
      await flushPromises()
      expect(wallCalls).toHaveLength(1)
      // 未获得条目不进索引：此刻只有 regular 命中。
      expect(wrapper.findAll('.sf-badge-detail-body')).toHaveLength(1)

      // 第二枚到手：veteran 仍在 missing 里 → 必须补拉一次。
      wallResponses.push(wallOf(REGULAR_EARNED, VETERAN_EARNED))
      celebrationsState.value = {
        initialized: true,
        cursorId: 127,
        total: 2,
        items: [
          badgeItemWithSlug('regular', { id: 126 }),
          badgeItemWithSlug('veteran', { id: 127 }, '资深会员')
        ]
      }
      await flushPromises()

      expect(wallCalls).toHaveLength(2)
      expect(wrapper.findAll('.sf-badge-detail-body')).toHaveLength(2)

      const veteran = wrapper.get('[data-badge-slug="veteran"]')
      // 补拉之后按「已获得」渲染：有获得时间，没有进度条。
      expect(veteran.text()).toContain('获得于 2026-11-13')
      expect(veteran.find('[role="progressbar"]').exists()).toBe(false)

      // 解析完成后再等两拍：不该因为「每次 fetch 都产出新对象」而自激重发。
      await flushPromises()
      await wrapper.vm.$nextTick()
      expect(wallCalls).toHaveLength(2)
    } finally {
      wrapper.unmount()
    }
  })

  test('a slug that stays unknown after the refetch falls back to the plain row', async () => {
    resetHostState()
    wallResponses.push(wallOf(REGULAR_EARNED, VETERAN_UNEARNED))
    celebrationsState.value = {
      initialized: true,
      cursorId: 120,
      total: 1,
      items: [badgeItemWithSlug('regular', { id: 126 })]
    }
    const wrapper = mountHost()
    try {
      await flushPromises()

      // 第二枚到手，但服务端已经查不到它（徽章被停用/删除）。
      wallResponses.push(wallOf(REGULAR_EARNED))
      celebrationsState.value = {
        initialized: true,
        cursorId: 127,
        total: 2,
        items: [
          badgeItemWithSlug('regular', { id: 126 }),
          badgeItemWithSlug('veteran', { id: 127 }, '资深会员')
        ]
      }
      await flushPromises()

      expect(wallCalls).toHaveLength(2)
      expect(wrapper.find('[data-badge-slug="veteran"]').exists()).toBe(false)
      expect(wrapper.get('.sf-celebration__row--text').text()).toBe('你获得了「资深会员」徽章。')

      // 键不再变化：不会变成「每次渲染都重试」的风暴。
      await flushPromises()
      await wrapper.vm.$nextTick()
      expect(wallCalls).toHaveLength(2)
    } finally {
      wrapper.unmount()
    }
  })

  test('an in-flight wall response for the previous account is discarded', async () => {
    resetHostState()
    let resolveWall: (value: Record<string, unknown>) => void = () => {}
    wallResponses.push(new Promise((resolve) => {
      resolveWall = resolve
    }))
    celebrationsState.value = {
      initialized: true,
      cursorId: 120,
      total: 1,
      items: [badgeItemWithSlug('regular', { id: 126 })]
    }
    const wrapper = mountHost()
    try {
      await flushPromises()
      expect(wallCalls).toHaveLength(1)

      // 请求还在途时换账号：A 的响应回来不能落到 B 的界面上。
      userRef.value = { id: 8, username: 'bob' }
      await flushPromises()
      resolveWall(wallOf(REGULAR_EARNED))
      await flushPromises()

      expect(wrapper.find('.sf-badge-detail-body').exists()).toBe(false)
      expect(wrapper.get('.sf-celebration__row-text').text()).toBe('你获得了「常客」徽章。')
    } finally {
      wrapper.unmount()
    }
  })

  test('closing a dialog with info blocks still acks exactly once', async () => {
    resetHostState()
    wallResponses.push(badgeWall())
    celebrationsState.value = { initialized: true, cursorId: 120, total: 1, items: [badgeItemWithSlug()] }
    const wrapper = mountHost()
    try {
      await flushPromises()
      expect(wrapper.find('.sf-badge-detail-body').exists()).toBe(true)

      await wrapper.get('.sf-celebration__close').trigger('click')
      expect(dismissCalls).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })
})
