import { describe, expect, mock, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import {
  badgeFiltersActive,
  filterBadges,
  grantRevocable,
  grantSourceOf,
  sortBadges,
  toAdminBadgeInput
} from '~/utils/admin/adminBadges'

/**
 * 三个 tab 组件与壳层的关键行为锚点（源码断言）。
 *
 * 复杂逻辑（筛选/排序/来源判别）已抽成 utils 纯函数、由 adminBadgesTable.test.ts
 * 覆盖；这里钉住「结构选择」：懒加载边界、两步确认、收回按钮的行级可见性、
 * URL query 归属，防止重构时悄悄丢掉。
 */
const shellSource = readFileSync(new URL('../../app/pages/admin/badges.vue', import.meta.url), 'utf8')
const badgesTabSource = readFileSync(
  new URL('../../app/components/admin/badges/tabs/SFAdminBadgesBadgesTab.vue', import.meta.url),
  'utf8'
)
const groupsTabSource = readFileSync(
  new URL('../../app/components/admin/badges/tabs/SFAdminBadgesGroupsTab.vue', import.meta.url),
  'utf8'
)
const grantsTabSource = readFileSync(
  new URL('../../app/components/admin/badges/tabs/SFAdminBadgesGrantsTab.vue', import.meta.url),
  'utf8'
)
const editDialogSource = readFileSync(
  new URL('../../app/components/admin/badges/SFAdminBadgeEditDialog.vue', import.meta.url),
  'utf8'
)
const tableSource = readFileSync(
  new URL('../../app/components/admin/badges/SFAdminBadgeTable.vue', import.meta.url),
  'utf8'
)

describe('badges shell (pages/admin/badges.vue)', () => {
  test('keeps the settings geometry: text-xl title, toolbar, fixed tab nav, KeepAlive', () => {
    expect(shellSource).toContain('text-xl font-bold')
    expect(shellSource).toContain('<UDashboardToolbar')
    expect(shellSource).toContain('SFAdminFixedTabNav')
    expect(shellSource).toContain('<KeepAlive>')
    // 壳层不再有 intro 段落：说明文案移入徽章 tab 空态。
    expect(shellSource).not.toContain('admin.badges.intro')
  })

  test('wraps the tab content in a UCard container', () => {
    // 内容区必须有承载容器（与 permissions.vue 同款），tab nav 留在卡外。
    expect(shellSource).toContain('<UCard')
  })

  test('normalizes ?tab= with the same fallback pattern as settings', () => {
    expect(shellSource).toContain('normalizeTab(route.query.tab)')
    expect(shellSource).toContain("validTabs.includes(candidate as BadgesTab)")
    expect(shellSource).toContain("router.replace({ query: { ...route.query, tab } })")
  })

  test('renders compact stats derived from the list response, not a new route', () => {
    for (const key of ['admin.badges.stats.badges', 'admin.badges.stats.enabled', 'admin.badges.stats.automatic', 'admin.badges.stats.granted']) {
      expect(shellSource).toContain(key)
    }
    expect(shellSource).toContain('reduce((sum, badge) => sum + badge.grantedCount, 0)')
  })

  test('shares one store instance across the three tabs', () => {
    // 壳层改成按 tab 下发 props 后，store 由 tabProps 统一携带（仍是同一个实例）。
    expect(shellSource).toContain('v-bind="tabProps"')
    expect(shellSource).toContain('const tabProps = computed')
    expect(shellSource).toContain('const store = useAdminBadges()')
    expect(shellSource).toContain('store,')
  })

  test('keeps blocked alerts in the shell and disables tabs while blocked', () => {
    expect(shellSource).toContain(`store.blocked.value === 'disabled'`)
    expect(shellSource).toContain(`store.blocked.value === 'forbidden'`)
    expect(shellSource).toContain('disabled: blockedActive.value')
  })
})

describe('badges tab', () => {
  test('keeps filter and sort state in URL query keys', () => {
    for (const key of ['q', 'group', 'tier', 'grantMode', 'enabled', 'sort', 'dir']) {
      expect(badgesTabSource).toContain(`'${key}'`)
    }
    // 写 query 前必须过 normalizeQuery（数组/空值归一），不再是裸 `{ query }`。
    expect(badgesTabSource).toMatch(/router\.replace\(\{\s*query:\s*normalizeQuery\(/)
  })

  test('separates an empty directory from a filtered-out result', () => {
    expect(badgesTabSource).toContain('directoryEmpty')
    expect(badgesTabSource).toContain('admin.badges.filters.noMatch')
    // intro 说明文案在空态出现（壳层已删）。
    expect(badgesTabSource).toContain('admin.badges.intro')
    expect(badgesTabSource).toContain('admin.badges.empty')
  })

  test('never feeds an empty string to a filter SelectItem', () => {
    // 格式无关的 tripwire：单引号 / 双引号 / 模板字面量的空串都不许当 item value。
    // 真正的行为兜底在文件末尾的挂载用例（items 实际值 + 哨兵不泄漏进 URL）。
    expect(badgesTabSource).not.toMatch(/value:\s*(?:''|""|``)/)
  })

  test('owns the toggle action the shell used to hold', () => {
    expect(badgesTabSource).toContain('async function toggleEnabled(badge: AdminBadgeRow)')
    expect(badgesTabSource).toContain('props.store.updateBadge')
    expect(badgesTabSource).not.toContain('useAdminBadges()')
  })
})

describe('groups tab', () => {
  test('explains the SET NULL consequence before confirming deletion', () => {
    expect(groupsTabSource).toContain('badgeCount > 0')
    expect(groupsTabSource).toContain('admin.badges.groups.confirmDeleteBodyWithBadges')
    expect(groupsTabSource).toContain('admin.badges.groups.confirmDeleteBody')
  })

  test('uses a per-row two-step delete, not a second modal', () => {
    expect(groupsTabSource).toContain('confirmDeleteId')
    expect(groupsTabSource).toContain('deletingId')
  })

  test('routes CRUD through the shared store', () => {
    expect(groupsTabSource).toContain('props.store.createBadgeGroup')
    expect(groupsTabSource).toContain('props.store.updateBadgeGroup')
    expect(groupsTabSource).toContain('props.store.deleteBadgeGroup')
  })
})

describe('grants tab', () => {
  test('loads grants lazily: only after a badge is selected', () => {
    expect(grantsTabSource).toContain('watch(selectedBadgeId')
    expect(grantsTabSource).toContain('props.store.listGrants')
    expect(grantsTabSource).toContain('perPage: PER_PAGE')
  })

  test('renders the revoke button only for manual rows', () => {
    expect(grantsTabSource).toContain('grantRevocable(row.original)')
  })

  test('splits the idempotent revoke result into two toasts', () => {
    expect(grantsTabSource).toContain('const revoked = await props.store.revokeGrant')
    expect(grantsTabSource).toContain('admin.badges.grants.toast.revoked')
    expect(grantsTabSource).toContain('admin.badges.grants.toast.notRevoked')
  })

  test('shows a three-state source badge per row', () => {
    expect(grantsTabSource).toContain('admin.badges.grants.source.manual')
    expect(grantsTabSource).toContain('admin.badges.grants.source.recompute')
    expect(grantsTabSource).toContain('admin.badges.grants.source.automatic')
  })
})

describe('edit dialog and table wiring', () => {
  test('delete confirmation appends the top_badge distortion hint', () => {
    expect(editDialogSource).toContain('admin.badges.confirmDeleteTopBadgeHint')
    expect(editDialogSource).toContain('admin.badges.toast.deletedRecomputeHint')
  })

  test('the table stays URL-agnostic and receives sort state via props', () => {
    expect(tableSource).toContain('sortKey?: AdminBadgeSortKey | \'\'')
    expect(tableSource).toContain("emit('sort'")
    expect(tableSource).not.toContain('useRoute()')
    expect(tableSource).not.toContain('router.replace')
  })
})


// --- 挂载行为断言（不是源码字符串匹配）：防「实现被换掉但关键字还在」的假绿 ---
const grantsTabBadge = {
  id: 1, slug: 'pioneer', name: 'Pioneer', description: '', icon: 'i-lucide-medal', tier: 'gold',
  groupId: null, groupName: null, grantMode: 'manual', metric: null, threshold: null,
  grantedCount: 2, enabled: true, position: 0
}
const grantsTabRows = [
  { userId: 7, username: 'alice', displayName: 'Alice', badgeId: 1, badgeSlug: 'pioneer', grantedAt: '2026-11-11T00:00:00Z', grantedBy: 3, grantReason: 'event' },
  { userId: 8, username: 'bob', displayName: 'Bob', badgeId: 1, badgeSlug: 'pioneer', grantedAt: '2026-11-11T00:00:00Z', grantedBy: null, grantReason: '' }
]

const GrantsTab = await compileVueSfc(
  fileURLToPath(new URL('../../app/components/admin/badges/tabs/SFAdminBadgesGrantsTab.vue', import.meta.url)),
  'grants-tab',
  {
    ref: testVue.ref,
    computed: testVue.computed,
    watch: testVue.watch,
    useI18n: () => ({ t: (key: string) => key }),
    useToast: () => ({ add: () => {} }),
    useAuthSession: () => ({ can: () => true }),
    SFAdminBadgeGrantDialog: { props: ['open', 'badge'], template: '<div />' },
    grantRevocable,
    grantSourceOf
  }
)

describe('grants tab behavior (mounted)', () => {
  test('loads page 1 on mount; automatic rows have no revoke button; revoke needs two clicks', async () => {
    const listGrants = mock(async () => ({ items: grantsTabRows, total: grantsTabRows.length }))
    const revokeGrant = mock(async () => true)
    const wrapper = mount(GrantsTab, {
      props: { store: { listGrants, revokeGrant, load: async () => {} }, badges: [grantsTabBadge], canManage: true },
      global: {
        stubs: {
          UIcon: true,
          UAlert: true,
          UBadge: { props: ['color', 'variant'], template: '<span><slot /></span>' },
          USelect: { props: ['modelValue', 'items'], template: '<select />' },
          UTable: {
            props: ['data', 'columns', 'loading', 'caption'],
            inheritAttrs: false,
            template: '<table><tr v-for="row in data" :key="row.userId"><td><slot name="actions-cell" :row="{ original: row }" /></td></tr></table>'
          },
          UButton: {
            props: ['icon', 'title', 'ariaLabel'],
            emits: ['click'],
            template: '<button :aria-label="ariaLabel" :title="title" @click="$emit(\'click\')"><slot /></button>'
          }
        }
      }
    })
    await flushPromises()
    // 护栏 #1：目录已加载时切进本分区也必须自动拉取当前徽章的第一页。
    expect(listGrants).toHaveBeenCalledTimes(1)
    // 护栏 #2：自动授予行（grantedBy=null）不渲染收回入口。
    const revokeButtons = wrapper.findAll('button[aria-label="admin.badges.grants.revokeLabel"]')
    expect(revokeButtons).toHaveLength(1)
    // 护栏 #3：第一次点击只进确认态，不发 DELETE；第二次才发。
    await revokeButtons[0].trigger('click')
    expect(revokeGrant).not.toHaveBeenCalled()
    await revokeButtons[0].trigger('click')
    await flushPromises()
    expect(revokeGrant).toHaveBeenCalledWith(1, 7)
    wrapper.unmount()
  })
})

// --- badges tab 筛选下拉：行为断言（挂载真实 SFC，不靠源码字符串） ---
const USelectStub = {
  name: 'USelect',
  props: ['modelValue', 'items', 'ariaLabel'],
  emits: ['update:modelValue'],
  template: '<div class="uselect-stub" />'
}
const UInputStub = {
  name: 'UInput',
  props: ['modelValue', 'placeholder', 'ariaLabel'],
  emits: ['update:modelValue'],
  template: '<input />'
}
const UButtonStub = {
  name: 'UButton',
  props: ['icon', 'color', 'variant'],
  template: '<button><slot /></button>'
}
const BadgeTableStub = {
  name: 'SFAdminBadgeTable',
  props: ['badges', 'pending', 'canManage', 'sortKey', 'sortDir'],
  template: '<table />'
}
const EditDialogStub = { name: 'SFAdminBadgeEditDialog', props: ['open', 'badge', 'groups'], template: '<div />' }
const GrantDialogStub = { name: 'SFAdminBadgeGrantDialog', props: ['open', 'badge'], template: '<div />' }

// setup 时才读 query，所以每次挂载前替换这个对象即可模拟不同 URL。
let currentQuery: Record<string, unknown> = {}
const replaceCalls: Array<{ query: Record<string, string> }> = []

const BadgesTab = await compileVueSfc(
  fileURLToPath(new URL('../../app/components/admin/badges/tabs/SFAdminBadgesBadgesTab.vue', import.meta.url)),
  'badges-tab',
  {
    ref: testVue.ref,
    computed: testVue.computed,
    useRoute: () => ({ query: currentQuery }),
    useRouter: () => ({ replace: async (to: { query: Record<string, string> }) => { replaceCalls.push(to) } }),
    useI18n: () => ({ t: (key: string) => key }),
    useToast: () => ({ add: () => {} }),
    useAuthSession: () => ({ can: () => true }),
    badgeFiltersActive,
    filterBadges,
    sortBadges,
    toAdminBadgeInput,
    SFAdminBadgeTable: BadgeTableStub,
    SFAdminBadgeEditDialog: EditDialogStub,
    SFAdminBadgeGrantDialog: GrantDialogStub
  }
)

const badgesTabBadges = [
  {
    id: 1, slug: 'pioneer', name: 'Pioneer', description: '', icon: 'i-lucide-medal', tier: 'gold',
    groupId: 3, groupName: '活跃', grantMode: 'manual', metric: null, threshold: null,
    grantedCount: 2, enabled: true, position: 0
  },
  {
    id: 2, slug: 'resident', name: 'Resident', description: '', icon: 'i-lucide-medal', tier: 'silver',
    groupId: null, groupName: null, grantMode: 'automatic', metric: 'topic_count', threshold: 10,
    grantedCount: 0, enabled: false, position: 1
  }
]
const badgesTabGroups = [{ id: 3, name: '活跃', slug: 'active', description: '', position: 0, badgeCount: 1 }]

function mountBadgesTab(query: Record<string, unknown> = {}) {
  currentQuery = query
  replaceCalls.length = 0
  return mount(BadgesTab, {
    props: {
      store: { load: async () => {}, updateBadge: async () => {} },
      badges: badgesTabBadges,
      groups: badgesTabGroups,
      pending: false,
      canManage: true
    },
    global: {
      stubs: {
        UInput: UInputStub,
        USelect: USelectStub,
        UButton: UButtonStub,
        UIcon: true,
        SFAdminBadgeTable: BadgeTableStub,
        SFAdminBadgeEditDialog: EditDialogStub,
        SFAdminBadgeGrantDialog: GrantDialogStub
      }
    }
  })
}

function lastQuery() {
  const call = replaceCalls.at(-1)
  if (!call) throw new Error('router.replace was not called')
  return call.query
}

describe('badges tab filter selects (mounted)', () => {
  test('no SelectItem ever carries an empty-string value, and empty query maps to the sentinel', () => {
    const wrapper = mountBadgesTab()
    const selects = wrapper.findAllComponents(USelectStub)
    expect(selects).toHaveLength(4)
    for (const select of selects) {
      const items = select.props('items') as Array<{ value: string }>
      expect(items.length).toBeGreaterThan(1)
      // Reka 的硬约束：item value 必须是非空字符串，否则整个 SelectContent 抛错。
      for (const item of items) {
        expect(typeof item.value).toBe('string')
        expect(item.value).not.toBe('')
      }
      // 空 query 时下拉显示「全部」哨兵，而不是空串（空串是清空/placeholder 语义）。
      expect(select.props('modelValue')).toBe('all')
    }
    wrapper.unmount()
  })

  test('picking 「全部」 deletes the query key instead of leaking the sentinel', async () => {
    const wrapper = mountBadgesTab({ group: '3', tier: 'gold' })
    const selects = wrapper.findAllComponents(USelectStub)
    await selects[0].vm.$emit('update:modelValue', 'all')
    await flushPromises()
    const query = lastQuery()
    expect(query).not.toHaveProperty('group')
    expect(Object.values(query)).not.toContain('all')
    // 其它筛选不受影响
    expect(query.tier).toBe('gold')
    wrapper.unmount()
  })

  test('picking 「未分组」 writes group=none verbatim (sentinel must not eat it)', async () => {
    const wrapper = mountBadgesTab()
    await wrapper.findAllComponents(USelectStub)[0].vm.$emit('update:modelValue', 'none')
    await flushPromises()
    expect(lastQuery().group).toBe('none')
    wrapper.unmount()
  })

  // 挂载时先给满四个键：spy 不回写 query，所以「别人还活着」必须由初始状态提供，
  // 否则空 query 下任何断言都恒真（这条曾经就是这么空转的）。
  const SEEDED_QUERY = { group: '3', tier: 'gold', grantMode: 'manual', enabled: 'true' }

  test('a filter change writes only its own key and leaves the other filters alive', async () => {
    for (const [index, key, next, others] of [
      [1, 'tier', 'silver', { group: '3', grantMode: 'manual', enabled: 'true' }],
      [2, 'grantMode', 'automatic', { group: '3', tier: 'gold', enabled: 'true' }],
      [3, 'enabled', 'false', { group: '3', tier: 'gold', grantMode: 'manual' }]
    ] as const) {
      const wrapper = mountBadgesTab(SEEDED_QUERY)
      await wrapper.findAllComponents(USelectStub)[index].vm.$emit('update:modelValue', next)
      await flushPromises()
      const query = lastQuery()
      expect(query[key]).toBe(next)
      expect(query).toMatchObject(others)
      wrapper.unmount()
    }
  })

  test('picking 「全部」 clears only that key and leaves the other filters alive', async () => {
    for (const [index, key, others] of [
      [1, 'tier', { group: '3', grantMode: 'manual', enabled: 'true' }],
      [2, 'grantMode', { group: '3', tier: 'gold', enabled: 'true' }],
      [3, 'enabled', { group: '3', tier: 'gold', grantMode: 'manual' }]
    ] as const) {
      const wrapper = mountBadgesTab(SEEDED_QUERY)
      await wrapper.findAllComponents(USelectStub)[index].vm.$emit('update:modelValue', 'all')
      await flushPromises()
      const query = lastQuery()
      expect(query).not.toHaveProperty(key)
      expect(query).toMatchObject(others)
      wrapper.unmount()
    }
  })

  test('read side normalizes non-whitelisted query values instead of filtering everything out', () => {
    // ?group=all / ?enabled=all 是手改 URL 才会出现的值：不能当分组 id 比对，
    // 也不能让 badgeFiltersActive 误报「有筛选」。
    const wrapper = mountBadgesTab({ group: 'all', enabled: 'all', tier: 'platinum', grantMode: 'auto' })
    const selects = wrapper.findAllComponents(USelectStub)
    for (const select of selects) expect(select.props('modelValue')).toBe('all')
    const table = wrapper.findComponent(BadgeTableStub)
    expect(table.props('badges')).toHaveLength(badgesTabBadges.length)
    expect(wrapper.text()).not.toContain('admin.badges.filters.clear')
    wrapper.unmount()
  })

  test('read side keeps enabled=false and narrows to the disabled badge', () => {
    const wrapper = mountBadgesTab({ enabled: 'false' })
    expect(wrapper.findAllComponents(USelectStub)[3].props('modelValue')).toBe('false')
    const rendered = wrapper.findComponent(BadgeTableStub).props('badges') as Array<{ slug: string }>
    expect(rendered).toHaveLength(1)
    expect(rendered[0].slug).toBe('resident')
    wrapper.unmount()
  })

  test('read side keeps valid values and accepts numeric group ids', () => {
    const wrapper = mountBadgesTab({ group: '3', tier: 'gold', grantMode: 'manual', enabled: 'true' })
    const selects = wrapper.findAllComponents(USelectStub)
    expect(selects.map(select => select.props('modelValue'))).toEqual(['3', 'gold', 'manual', 'true'])
    // group=3 只留下分组 3 的那一枚
    expect(wrapper.findComponent(BadgeTableStub).props('badges')).toHaveLength(1)
    wrapper.unmount()
  })

  test('array query values collapse to a single value instead of bouncing back as duplicates', async () => {
    const wrapper = mountBadgesTab({ q: ['alpha', 'beta'], group: '3' })
    await wrapper.findAllComponents(USelectStub)[1].vm.$emit('update:modelValue', 'gold')
    await flushPromises()
    const query = lastQuery()
    expect(query.q).toBe('alpha')
    expect(query.group).toBe('3')
    expect(query.tier).toBe('gold')
    wrapper.unmount()
  })
})
