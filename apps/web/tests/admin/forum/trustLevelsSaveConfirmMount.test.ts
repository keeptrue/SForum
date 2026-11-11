import { describe, expect, test, mock } from 'bun:test'
import { computed, reactive, ref, watch, onMounted } from 'vue'
import { fileURLToPath } from 'node:url'
import { readFileSync } from 'node:fs'
import { compileVueSfc, flushPromises, mount } from '../../helpers/vueSfc'

/**
 * 编排层的协作者（权限 / 设置分区 / web options / option 保存）用 `mock.module` 换掉：
 * 它们本身依赖 Nuxt 运行时上下文（`useState` 等），与本次要验证的行为无关。
 * 编排层本身是**真的**，所以「点击 → 事件 → 表单变更 → save()」整条链不被绕过。
 */
type WriteCall = { method: string, path: string, body?: unknown }
const writes: WriteCall[] = []
const toasts: unknown[] = []
/** 下一次写请求的响应；`reject` 用来构造真实 422。 */
let nextFailure: unknown = null

function adminPayload(config: string) {
  return {
    config,
    levels: [],
    eligiblePermissions: ['topic.create', 'post.create'],
    baselinePermissions: [],
    recommendedConfig: STORED_CONFIG,
    iconChoices: ['leaf', 'home'],
    colorChoices: ['slate', 'teal', 'emerald', 'sky', 'indigo', 'violet', 'fuchsia', 'rose', 'amber'],
    namePool: { zh: ['种子'], en: ['Seed'] },
    tierLimits: { min: 1, max: 8 },
    nextTierSeq: 5
  }
}

mock.module('~/composables/useApiClient', () => ({
  apiErrorReason: (error: unknown) => (error as { reason?: string })?.reason ?? '',
  apiErrorStatusCode: (error: unknown) => (error as { status?: number })?.status ?? 0,
  // 与真实实现同语义：从 `data.details` 取结构化诊断。
  apiErrorDetails: (error: unknown) => (error as { details?: unknown })?.details,
  useApiClient: () => ({
    request: async (path: string, options: { method?: string, body?: unknown } = {}) => {
      const method = (options.method ?? 'GET').toUpperCase()
      // 读面（GET）不计入「写请求」：本文件断言的是保存/恢复有没有真的发出去。
      if (method !== 'GET') writes.push({ method, path, body: options.body })
      if (method === 'GET') return adminPayload(STORED_CONFIG)
      if (nextFailure) {
        const failure = nextFailure
        nextFailure = null
        throw failure
      }
      const config = (options.body as { config?: string } | undefined)?.config ?? STORED_CONFIG
      return adminPayload(config)
    }
  })
}))
mock.module('~/composables/identity/useAuthSession', () => ({
  useAuthSession: () => ({ can: () => true })
}))
mock.module('~/composables/settings/useSettingsSection', () => ({
  useSettingsSection: () => ({
    saving: ref(false),
    // 与真实 `runSave` 同语义：吞掉异常并返回 false（错误由调用方转成字段级明细）。
    runSave: async (options: { prepare?: () => Promise<void>, save: () => Promise<void> }) => {
      try {
        await options.prepare?.()
        await options.save()
        return true
      } catch {
        return false
      }
    },
    runRestore: () => {}
  })
}))
mock.module('~/composables/useWebOptions', () => ({
  useWebOptions: () => ({
    options: ref<Record<string, string>>({}),
    trustLevelsEnabled: ref(true),
    fetchAdminEnvelope: async () => ({
      data: [{
        name: 'trust.levels',
        value: STORED_CONFIG,
        public: false,
        secret: false,
        managePermission: 'trust.manage'
      }]
    })
  })
}))
mock.module('~/composables/admin/settings/useAdminOptionTab', () => ({
  adminOptionMap: (items: Array<{ name: string, value: string }>) =>
    Object.fromEntries(items.map(item => [item.name, item]))
}))

// Nuxt 自动导入的最小替身：只提供编排层真正调用到的几个。
Object.assign(globalThis as Record<string, unknown>, {
  ref,
  computed,
  watch,
  onMounted,
  useI18n: () => ({ t, te, locale: ref('zh-CN') }),
  useToast: () => ({ add: (payload: unknown) => { toasts.push(payload) } }),
  // Nuxt 的 useAsyncData 是**同步返回**的（数据稍后落位），照抄这个时序才能复现真实首帧。
  useAsyncData: (_key: string, handler: () => Promise<unknown>) => {
    const data = ref<unknown>(null)
    const pending = ref(true)
    void handler().then((value) => { data.value = value }).finally(() => { pending.value = false })
    return { data, pending, error: ref(null), refresh: async () => {} }
  },
  // `useWebOptions` 在编排层是 Nuxt 自动导入（不是静态 import），所以必须挂在 globalThis 上。
  useWebOptions: () => ({
    options: ref<Record<string, string>>({}),
    trustLevelsEnabled: ref(true),
    fetchAdminEnvelope: async () => ({
      data: [{
        name: 'trust.levels',
        value: STORED_CONFIG,
        public: false,
        secret: false,
        managePermission: 'trust.manage'
      }]
    })
  })
})

const { useTrustLevelsAdminPage } = await import('~/composables/admin/forum/trustLevels/useTrustLevelsAdminPage')

/**
 * **保存确认的挂载级行为**（P1-4）：谓词单测只能证明「判据对不对」，
 * 证明不了「点了保存到底弹不弹、弹的时候有没有先写盘」——这正是 P1 卡片错位的同型教训。
 *
 * 断言落点只有两个：
 * 1. 确认对话框是否**存在于 DOM**；
 * 2. 写请求（`saveOptions`）是否**发出**。
 *
 * 叶子组件用最小桩（只发页面真正监听的事件），编排层是**真的** `useTrustLevelsAdminPage`，
 * 所以走的是「点击 → 事件 → 表单变更 → save() → 弹窗/写盘」整条链。
 */
const messages = JSON.parse(
  readFileSync(new URL('../../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
) as Record<string, unknown>

function lookup(path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    messages
  )
}

function t(key: string, params: Record<string, unknown> = {}) {
  const template = lookup(key)
  if (typeof template !== 'string') throw new Error(`missing locale key: ${key}`)
  return template.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
}

const te = (key: string) => typeof lookup(key) === 'string'

const STORED_CONFIG = JSON.stringify({
  schemaVersion: 2,
  tiers: ['member', 'resident', 'veteran', 'leader'].map((id, index) => ({
    id,
    name: { zh: '', en: '' },
    icon: null,
    requirements: {
      days_visited: (index + 1) * 3,
      topic_count: 0,
      comment_count: 0,
      like_received_count: 0,
      like_given_count: 0,
      bookmark_count: 0
    },
    limits: null,
    earnable: [],
    retired: false
  }))
})

/** 每个用例一个干净场景：路由 tab 决定哪个分区可见，写请求计数从 0 开始。 */
function createHarness(tab: 'tiers' | 'thresholds') {
  writes.length = 0
  toasts.length = 0
  nextFailure = null
  const route = reactive({ query: { tab } as Record<string, string> })
  return { writeCalls: writes, route }
}

const TiersStub = {
  name: 'SFAdminTrustLevelTiersTab',
  props: {
    savedRetiredIds: { type: Array, default: () => [] },
    deletedTierIds: { type: Array, default: () => [] }
  },
  emits: ['add', 'retire', 'restore', 'delete', 'setColor'],
  template: `<div>
    <button type="button" data-stub-add-tier @click="$emit('add')">add</button>
    <button type="button" data-stub-retire @click="$emit('retire', 'resident')">retire</button>
    <button type="button" data-stub-restore @click="$emit('restore', 'resident')">restore</button>
    <button type="button" data-stub-delete @click="$emit('delete', 'resident')">delete</button>
    <button type="button" data-stub-set-color @click="$emit('setColor', 'member', 'rose')">color</button>
    <span data-stub-saved-retired>{{ JSON.stringify(savedRetiredIds) }}</span>
    <span data-stub-tombstones>{{ JSON.stringify(deletedTierIds) }}</span>
  </div>`
}
const ThresholdsStub = {
  name: 'SFAdminTrustLevelThresholdsTab',
  emits: ['threshold', 'restore'],
  template: `<div>
    <button type="button" data-stub-threshold @click="$emit('threshold', 'member', 'days_visited', 42)">threshold</button>
    <button type="button" data-stub-restore @click="$emit('restore')">restore</button>
  </div>`
}
const EmptyStub = { name: 'EmptyStub', template: '<div />' }

const UModalStub = {
  name: 'UModal',
  props: { open: { type: Boolean, default: false } },
  emits: ['update:open'],
  template: '<div v-if="open" data-testid="structure-modal"><slot name="content" /></div>'
}
const UButtonStub = { name: 'UButton', template: '<button type="button" v-bind="$attrs"><slot /></button>' }
const UAlertStub = {
  name: 'UAlert',
  props: { title: String, description: String },
  template: '<div class="u-alert" :data-title="title"><slot name="description" /></div>'
}
const UCardStub = {
  name: 'UCard',
  template: '<section><slot name="header" /><slot /><slot name="footer" /></section>'
}
const UIconStub = { name: 'UIcon', template: '<i />' }
const UBadgeStub = { name: 'UBadge', template: '<span><slot /></span>' }
const UDashboardToolbarStub = {
  name: 'UDashboardToolbar',
  template: '<div><slot name="left" /><slot name="right" /></div>'
}
const SFAdminFixedTabNavStub = { name: 'SFAdminFixedTabNav', template: '<div />' }
const SFAdminFormFooterStub = {
  name: 'SFAdminFormFooter',
  template: '<div><slot name="left" /><slot name="actions" /></div>'
}

const routeRef = ref<{ query: Record<string, string> }>({ query: {} })

const pagePath = fileURLToPath(new URL('../../../app/pages/admin/forum/trust-levels.vue', import.meta.url))

async function mountPage(tab: 'tiers' | 'thresholds') {
  // 页面 shell 的宏与路由也要有替身（编译后是普通调用）。
  Object.assign(globalThis as Record<string, unknown>, {
    useRoute: () => routeRef.value,
    useRouter: () => ({ replace: async () => {} }),
    useSeoMeta: () => {},
    definePageMeta: () => {}
  })
  const harness = createHarness(tab)
  routeRef.value = harness.route
  // 每次挂载重新编译：bindings 是编译期闭包，场景不同就要重新绑定。
  const dialogPath = fileURLToPath(
    new URL('../../../app/components/admin/forum/trust-levels/SFAdminTrustLevelTierConfirmDialog.vue', import.meta.url)
  )
  const SFAdminTrustLevelTierConfirmDialog = await compileVueSfc(dialogPath, 'sf-trust-tier-confirm-dialog', {
    computed,
    useI18n: () => ({ t, te, locale: ref('zh-CN') })
  })
  const Page = await compileVueSfc(pagePath, `sf-trust-levels-page-${tab}`, {
    computed,
    ref,
    watch,
    onMounted,
    useAdminPage: () => ({ icon: 'i-lucide-gauge' }),
    useTrustLevelsAdminPage,
    SFAdminFixedTabNav: SFAdminFixedTabNavStub,
    SFAdminFormFooter: SFAdminFormFooterStub,
    SFAdminTrustLevelsOverviewTab: EmptyStub,
    SFAdminTrustLevelTiersTab: TiersStub,
    SFAdminTrustLevelTierConfirmDialog,
    SFAdminTrustLevelThresholdsTab: ThresholdsStub,
    SFAdminTrustLevelEarnableTab: EmptyStub,
    SFAdminTrustLevelLimitsTab: EmptyStub
  })
  const wrapper = mount(Page, {
    global: {
      components: {
        UModal: UModalStub,
        UButton: UButtonStub,
        UAlert: UAlertStub,
        UCard: UCardStub,
        UIcon: UIconStub,
        UBadge: UBadgeStub,
        UDashboardToolbar: UDashboardToolbarStub
      }
    }
  })
  await flushPromises()
  return { wrapper, ...harness }
}

describe('save confirmation is a real gate, not just a predicate (P1-4)', () => {
  test('a structural change opens the blocking dialog and sends no write request', async () => {
    const { wrapper, writeCalls: saveCalls } = await mountPage('tiers')
    try {
      expect(wrapper.find('[data-testid="structure-modal"]').exists()).toBe(false)
      await wrapper.find('[data-stub-add-tier]').trigger('click')
      await flushPromises()
      await wrapper.find('form').trigger('submit')
      await flushPromises()

      // ① 对话框真的渲染出来了。
      expect(wrapper.find('[data-testid="structure-modal"]').exists()).toBe(true)
      expect(wrapper.find('[data-sf-trust-levels-structure-accept]').exists()).toBe(true)
      // ② 而且它挡在写之前：此刻一次写请求都没发。
      expect(saveCalls).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('confirming the dialog is what actually writes', async () => {
    const { wrapper, writeCalls: saveCalls } = await mountPage('tiers')
    try {
      await wrapper.find('[data-stub-add-tier]').trigger('click')
      await flushPromises()
      await wrapper.find('form').trigger('submit')
      await flushPromises()
      expect(saveCalls).toHaveLength(0)

      await wrapper.find('[data-sf-trust-levels-structure-accept]').trigger('click')
      await flushPromises()

      expect(wrapper.find('[data-testid="structure-modal"]').exists()).toBe(false)
      expect(saveCalls).toHaveLength(1)
      const call = saveCalls[0]!
      // 专用写端点，不再落到通用 web-options。
      expect(call.method).toBe('PUT')
      expect(call.path).toBe('/admin/trust-levels')
      expect(call.path).not.toContain('web-options')
      const config = JSON.parse((call.body as { config: string }).config) as {
        schemaVersion: number
        tiers: Array<Record<string, unknown>>
      }
      expect(config.schemaVersion).toBe(2)
      expect(config.tiers).toHaveLength(5)
      // 未选颜色时**不含** `color` 键（omitempty）：空串/显式默认值都会把「跟随位次」钉死。
      expect(Object.keys(config.tiers[0]!).sort()).toEqual(['earnable', 'icon', 'id', 'limits', 'name', 'requirements', 'retired'])
      expect(config.tiers.some(tier => 'color' in tier)).toBe(false)
      // 墓碑同理：没有删除时整份载荷不出现 `deletedTierIds`。
      expect('deletedTierIds' in (config as unknown as Record<string, unknown>)).toBe(false)
      // 新档 id 必须是后端认的 `tier_<数字>`（下划线），且与其它档不重复。
      const ids = config.tiers.map(tier => String(tier.id))
      const created = ids.filter(id => id !== 'member' && id !== 'resident' && id !== 'veteran' && id !== 'leader')
      expect(created).toHaveLength(1)
      expect(created[0]).toMatch(/^tier_[0-9]+$/)
      expect(new Set(ids).size).toBe(ids.length)
    } finally {
      wrapper.unmount()
    }
  })

  test('cancelling the dialog writes nothing', async () => {
    const { wrapper, writeCalls: saveCalls } = await mountPage('tiers')
    try {
      await wrapper.find('[data-stub-add-tier]').trigger('click')
      await flushPromises()
      await wrapper.find('form').trigger('submit')
      await flushPromises()
      await wrapper.find('[data-sf-trust-levels-structure-cancel]').trigger('click')
      await flushPromises()

      expect(wrapper.find('[data-testid="structure-modal"]').exists()).toBe(false)
      expect(saveCalls).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('two inserts in a row get two distinct tier_<n> ids', async () => {
    const { wrapper, writeCalls } = await mountPage('tiers')
    try {
      await wrapper.find('[data-stub-add-tier]').trigger('click')
      await flushPromises()
      await wrapper.find('[data-stub-add-tier]').trigger('click')
      await flushPromises()
      await wrapper.find('form').trigger('submit')
      await flushPromises()
      await wrapper.find('[data-sf-trust-levels-structure-accept]').trigger('click')
      await flushPromises()

      const config = JSON.parse((writeCalls[0]!.body as { config: string }).config) as {
        tiers: Array<{ id: string }>
      }
      const ids = config.tiers.map(tier => tier.id)
      expect(config.tiers).toHaveLength(6)
      expect(new Set(ids).size).toBe(6)
      const created = ids.filter(id => !['member', 'resident', 'veteran', 'leader'].includes(id))
      expect(created).toHaveLength(2)
      for (const id of created) expect(id).toMatch(/^tier_[0-9]+$/)
    } finally {
      wrapper.unmount()
    }
  })

  test('both dialogs declare themselves as modal dialogs (a11y)', async () => {
    const { wrapper } = await mountPage('tiers')
    try {
      await wrapper.find('[data-stub-add-tier]').trigger('click')
      await flushPromises()
      await wrapper.find('form').trigger('submit')
      await flushPromises()
      const structure = wrapper.find('[data-sf-trust-levels-structure-dialog]')
      expect(structure.attributes('role')).toBe('dialog')
      expect(structure.attributes('aria-modal')).toBe('true')
      expect(structure.attributes('aria-labelledby')).toBe('sf-trust-levels-structure-title')
      // 属性字符串本身不构成无障碍名：被指向的 id 必须真的在 DOM 里。
      expect(wrapper.find('#sf-trust-levels-structure-title').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }

    const { wrapper: second } = await mountPage('thresholds')
    try {
      await second.find('[data-stub-restore]').trigger('click')
      await flushPromises()
      const restore = second.find('[data-sf-trust-levels-restore-dialog]')
      expect(restore.attributes('role')).toBe('dialog')
      expect(restore.attributes('aria-modal')).toBe('true')
      expect(restore.attributes('aria-labelledby')).toBe('sf-trust-levels-restore-title')
      expect(second.find('#sf-trust-levels-restore-title').exists()).toBe(true)
    } finally {
      second.unmount()
    }
  })

  test('a 422 renders the page-level detail block instead of a silent failure', async () => {
    const { wrapper, writeCalls } = await mountPage('thresholds')
    try {
      nextFailure = {
        status: 422,
        reason: 'trust_levels.invalid',
        details: { errors: [{ rank: 0, tierId: '', field: 'schemaVersion', reason: '版本不受支持', reasonCode: 'schema_version_unsupported', params: {} }] }
      }
      await wrapper.find('[data-stub-threshold]').trigger('click')
      await flushPromises()
      await wrapper.find('form').trigger('submit')
      await flushPromises()

      expect(writeCalls).toHaveLength(1)
      const block = wrapper.find('[data-sf-trust-levels-save-errors]')
      expect(block.exists()).toBe(true)
      expect(block.attributes('data-title')).toBe(t('admin.forum.trustLevels.errors.pageTitle'))
      expect(block.text()).toContain('版本不受支持')
    } finally {
      wrapper.unmount()
    }
  })

  test('restore defaults asks first, then POSTs the dedicated endpoint', async () => {
    const { wrapper, writeCalls } = await mountPage('thresholds')
    try {
      await wrapper.find('[data-stub-restore]').trigger('click')
      await flushPromises()
      // 先确认：对话框出现，且此时没有请求。
      expect(wrapper.find('[data-sf-trust-levels-restore-accept]').exists()).toBe(true)
      expect(writeCalls).toHaveLength(0)
      // 逐项列出会丢的东西：档位/阈值/限额/解锁/名称与图标，五条一条不少。
      const items = wrapper.findAll('[data-sf-trust-levels-restore-items] li')
      expect(items).toHaveLength(5)
      expect(items.map(item => item.text())).toEqual([
        t('admin.forum.trustLevels.restoreConfirm.items.tiers'),
        t('admin.forum.trustLevels.restoreConfirm.items.thresholds'),
        t('admin.forum.trustLevels.restoreConfirm.items.limits'),
        t('admin.forum.trustLevels.restoreConfirm.items.earnable'),
        t('admin.forum.trustLevels.restoreConfirm.items.appearance')
      ])

      await wrapper.find('[data-sf-trust-levels-restore-accept]').trigger('click')
      await flushPromises()
      expect(writeCalls).toHaveLength(1)
      expect(writeCalls[0]!.method).toBe('POST')
      expect(writeCalls[0]!.path).toBe('/admin/trust-levels/restore-defaults')
      // 不叠加结构变更弹窗。
      expect(wrapper.find('[data-testid="structure-modal"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('a threshold-only edit saves straight away with no dialog (reverse case)', async () => {
    const { wrapper, writeCalls: saveCalls } = await mountPage('thresholds')
    try {
      await wrapper.find('[data-stub-threshold]').trigger('click')
      await flushPromises()
      await wrapper.find('form').trigger('submit')
      await flushPromises()

      // ① 没有对话框。② 写请求真的发出去了，且走专用端点。
      expect(wrapper.find('[data-testid="structure-modal"]').exists()).toBe(false)
      expect(saveCalls).toHaveLength(1)
      const call = saveCalls[0]!
      expect(call.method).toBe('PUT')
      expect(call.path).toBe('/admin/trust-levels')
      const written = JSON.parse((call.body as { config: string }).config) as {
        schemaVersion: number
        tiers: Array<{ requirements: Record<string, number> }>
      }
      expect(written.schemaVersion).toBe(2)
      // 只改阈值：档位数不变（4），证明没把结构变化混进来。
      expect(written.tiers).toHaveLength(4)
      expect(written.tiers[0]!.requirements.days_visited).toBe(42)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('tier color and deletion gates (D-C1 / D-C2 / D-C3)', () => {
  async function submit(wrapper: Awaited<ReturnType<typeof mountPage>>['wrapper']) {
    await wrapper.find('form').trigger('submit')
    await flushPromises()
  }

  function lastConfig(writeCalls: WriteCall[]) {
    return JSON.parse((writeCalls[writeCalls.length - 1]!.body as { config: string }).config) as {
      schemaVersion: number
      tiers: Array<Record<string, unknown>>
      deletedTierIds?: string[]
    }
  }

  test('an explicit color is the only thing that writes the color field', async () => {
    const { wrapper, writeCalls } = await mountPage('tiers')
    try {
      // 只改颜色 = 不改结构：直接写，不弹确认。
      await wrapper.find('[data-stub-set-color]').trigger('click')
      await flushPromises()
      await submit(wrapper)
      expect(wrapper.find('[data-testid="structure-modal"]').exists()).toBe(false)
      expect(writeCalls).toHaveLength(1)
      const config = lastConfig(writeCalls)
      expect(config.tiers[0]!.color).toBe('rose')
      // 其它档位没动过颜色 → 仍然不写这个键。
      expect('color' in config.tiers[1]!).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('retire opens a modal, cancel touches nothing, confirm only edits the form', async () => {
    const { wrapper, writeCalls } = await mountPage('tiers')
    try {
      await wrapper.find('[data-stub-retire]').trigger('click')
      await flushPromises()
      const dialog = wrapper.find('[data-sf-trust-levels-tier-dialog-root="retire"]')
      expect(dialog.exists()).toBe(true)
      expect(dialog.attributes('role')).toBe('dialog')
      expect(dialog.attributes('aria-modal')).toBe('true')
      expect(dialog.attributes('aria-labelledby')).toBe('sf-trust-levels-tier-retire-title')
      expect(wrapper.find('#sf-trust-levels-tier-retire-title').exists()).toBe(true)
      // 弹窗挡在写之前：此刻一次写请求都没有。
      expect(writeCalls).toHaveLength(0)

      // 取消 → 表单逐字不变（写出来的还是「全员未停用」）。
      await wrapper.find('[data-sf-tier-dialog-cancel="retire"]').trigger('click')
      await flushPromises()
      expect(wrapper.find('[data-sf-trust-levels-tier-dialog-root="retire"]').exists()).toBe(false)
      await submit(wrapper)
      expect(writeCalls).toHaveLength(1)
      expect(lastConfig(writeCalls).tiers.every(tier => tier.retired === false)).toBe(true)

      // 确认 → 只改表单，仍然不写（保存入口永远只有 footer）。
      await wrapper.find('[data-stub-retire]').trigger('click')
      await flushPromises()
      await wrapper.find('[data-sf-tier-dialog-accept="retire"]').trigger('click')
      await flushPromises()
      expect(wrapper.find('[data-sf-trust-levels-tier-dialog-root="retire"]').exists()).toBe(false)
      expect(writeCalls).toHaveLength(1)
      // 结构变了 → 保存要过结构确认，落盘的配置里 resident 已停用。
      await submit(wrapper)
      await wrapper.find('[data-sf-trust-levels-structure-accept]').trigger('click')
      await flushPromises()
      const config = lastConfig(writeCalls)
      expect(config.tiers.find(tier => tier.id === 'resident')?.retired).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('delete is locked until the retirement is saved, then leaves an id tombstone', async () => {
    const { wrapper, writeCalls } = await mountPage('tiers')
    try {
      // 未保存为停用 → 按钮拿不到意图，弹窗不出现。
      await wrapper.find('[data-stub-delete]').trigger('click')
      await flushPromises()
      expect(wrapper.find('[data-sf-trust-levels-tier-dialog-root="delete"]').exists()).toBe(false)

      // 停用并保存，把「已保存为停用」这一步走实。
      await wrapper.find('[data-stub-retire]').trigger('click')
      await flushPromises()
      await wrapper.find('[data-sf-tier-dialog-accept="retire"]').trigger('click')
      await flushPromises()
      await submit(wrapper)
      await wrapper.find('[data-sf-trust-levels-structure-accept]').trigger('click')
      await flushPromises()
      expect(wrapper.find('[data-stub-saved-retired]').text()).toContain('resident')

      // 现在删除可用了：走 modal，确认后只改表单 + 记墓碑。
      await wrapper.find('[data-stub-delete]').trigger('click')
      await flushPromises()
      const dialog = wrapper.find('[data-sf-trust-levels-tier-dialog-root="delete"]')
      expect(dialog.exists()).toBe(true)
      expect(dialog.attributes('aria-labelledby')).toBe('sf-trust-levels-tier-delete-title')
      expect(wrapper.find('#sf-trust-levels-tier-delete-title').exists()).toBe(true)
      // 删除文案必须说明「不改等级」与「id 永不复用」，且不承诺可恢复。
      expect(dialog.text()).toContain(t('admin.forum.trustLevels.tierDelete.rankSafe'))
      expect(dialog.text()).toContain(t('admin.forum.trustLevels.tierDelete.idNote'))

      await wrapper.find('[data-sf-tier-dialog-accept="delete"]').trigger('click')
      await flushPromises()
      expect(wrapper.find('[data-stub-tombstones]').text()).toContain('resident')
      // 删除的是已停用档 → active 序列不变 → 不弹结构确认。
      await submit(wrapper)
      expect(wrapper.find('[data-testid="structure-modal"]').exists()).toBe(false)
      const config = lastConfig(writeCalls)
      expect(config.tiers.some(tier => tier.id === 'resident')).toBe(false)
      expect(config.deletedTierIds).toEqual(['resident'])

      // 墓碑非空之后的读面回填：再保存一次仍然带着它（不会被 applyTiers 静默抹掉）。
      await wrapper.find('[data-stub-set-color]').trigger('click')
      await flushPromises()
      await submit(wrapper)
      expect(lastConfig(writeCalls).deletedTierIds).toEqual(['resident'])
    } finally {
      wrapper.unmount()
    }
  })
})
