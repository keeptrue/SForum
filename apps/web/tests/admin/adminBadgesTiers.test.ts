import { describe, expect, mock, test } from 'bun:test'
import { computed, defineComponent, ref, watch } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount } from '../helpers/vueSfc'
import { tierColorVar } from '~/utils/theme/tierColor'

/**
 * 第四分区（等级）的接线与两个不可回退的约束：
 * 1. 壳层把 `tiers` 分区接上（tab 项、组件映射、props 只给该分区声明的字段）；
 * 2. 删除等级**必须先选迁移目标**（没有目标时按钮禁用），且是两步确认；
 * 3. 调序发的是**全量 keys**，不是「两个 key 的交换补丁」。
 */
const shellSource = readFileSync(new URL('../../app/pages/admin/badges.vue', import.meta.url), 'utf8')
const tiersTabSource = readFileSync(
  new URL('../../app/components/admin/badges/tabs/SFAdminBadgesTiersTab.vue', import.meta.url),
  'utf8'
)
const deleteDialogSource = readFileSync(
  new URL('../../app/components/admin/badges/SFAdminBadgeTierDeleteDialog.vue', import.meta.url),
  'utf8'
)
const storeSource = readFileSync(
  new URL('../../app/composables/admin/badges/useAdminBadges.ts', import.meta.url),
  'utf8'
)

describe('badges shell fourth tab', () => {
  test('registers the tiers tab with its own icon and component', () => {
    expect(shellSource).toContain("type BadgesTab = 'badges' | 'groups' | 'tiers' | 'grants'")
    expect(shellSource).toContain("const validTabs: BadgesTab[] = ['badges', 'groups', 'tiers', 'grants']")
    expect(shellSource).toContain("t('admin.badges.tabs.tiers')")
    expect(shellSource).toContain("icon: 'i-lucide-layers'")
    expect(shellSource).toContain('tiers: SFAdminBadgesTiersTab')
  })

  test('passes tiers + colorChoices only to the tiers partition', () => {
    expect(shellSource).toContain('colorChoices: store.colorChoices.value')
    expect(shellSource).toContain('tiers: store.tiers.value')
    // 分区 props 按 tab 分支下发：未声明的 props 会变成 fallthrough attr。
    expect(shellSource).toContain("if (activeTab.value === 'tiers')")
  })
})

describe('admin badges store tiers layer', () => {
  test('loads tiers in the same Promise.all as badges and groups', () => {
    expect(storeSource).toContain("request<{ items: AdminBadgeTierRow[], colorChoices: string[] }>('/admin/badges/tiers')")
    expect(storeSource).toContain('colorChoices.value = Array.isArray(tierList?.colorChoices)')
  })

  test('exposes the tier CRUD + order + batch grant methods', () => {
    for (const method of ['createTier', 'updateTier', 'deleteTier', 'reorderTiers', 'searchGrantCandidates', 'batchGrant']) {
      expect(storeSource).toContain(method)
    }
    // 删除必须带迁移目标（后端契约），不是可选参数。
    expect(storeSource).toContain('async function deleteTier(key: string, migrateTo: string)')
    expect(storeSource).toContain("'/admin/badges/tiers/order'")
  })
})

describe('tier delete dialog', () => {
  test('requires a migration target and a second confirmation', () => {
    expect(deleteDialogSource).toContain('const canDelete = computed(() => Boolean(props.tier) && props.targets.length > 0 && migrateTo.value !== \'\')')
    expect(deleteDialogSource).toContain('if (!confirming.value)')
    expect(deleteDialogSource).toContain('admin.badges.tiers.remove.noTarget')
  })

  test('the tiers tab only offers other enabled tiers as targets', () => {
    expect(tiersTabSource).toContain('tier.key !== deleting.value?.key && tier.enabled')
  })
})

describe('tiers tab reorder', () => {
  test('sends the full key list, not a swap patch', () => {
    expect(tiersTabSource).toContain('const keys = ordered.value.map(tier => tier.key)')
    expect(tiersTabSource).toContain('await props.store.reorderTiers(keys)')
  })
})

// --- 挂载行为：调序真的把整串 keys 发出去 ---
const UButtonStub = defineComponent({
  name: 'UButton',
  props: { icon: String, disabled: Boolean, ariaLabel: String, title: String, loading: Boolean, color: String, variant: String },
  emits: ['click'],
  template: '<button :aria-label="ariaLabel" :title="title" :disabled="disabled" @click="$emit(\'click\')"><slot /></button>'
})
const USwitchStub = defineComponent({
  name: 'USwitch',
  props: { modelValue: Boolean, disabled: Boolean, ariaLabel: String },
  emits: ['update:modelValue'],
  template: '<button :aria-label="ariaLabel" :data-enabled="modelValue" @click="$emit(\'update:modelValue\', !modelValue)" />'
})
const UIconStub = defineComponent({ name: 'UIcon', props: { name: String }, template: '<i />' })
const TierDialogStub = defineComponent({ name: 'SFAdminBadgeTierDialog', props: ['open', 'tier', 'colorChoices', 'store'], template: '<div />' })
const TierDeleteDialogStub = defineComponent({ name: 'SFAdminBadgeTierDeleteDialog', props: ['open', 'tier', 'targets', 'store'], template: '<div />' })

const tiersTabPath = fileURLToPath(new URL('../../app/components/admin/badges/tabs/SFAdminBadgesTiersTab.vue', import.meta.url))
const TiersTab = await compileVueSfc(tiersTabPath, 'tiers-tab', {
  computed,
  ref,
  useI18n: () => ({ t: (key: string) => key }),
  useToast: () => ({ add: () => {} }),
  tierColorVar,
  SFAdminBadgeTierDialog: TierDialogStub,
  SFAdminBadgeTierDeleteDialog: TierDeleteDialogStub
})

const tierRows = [
  { key: 'gold', name: { zh: '金牌', en: 'Gold' }, icon: 'i-lucide-crown', color: 'amber', position: 0, enabled: true, badgeCount: 3 },
  { key: 'silver', name: { zh: '银牌', en: 'Silver' }, icon: 'i-lucide-medal', color: 'slate', position: 1, enabled: true, badgeCount: 1 },
  { key: 'legendary', name: { zh: '传说', en: 'Legendary' }, icon: 'i-lucide-flame', color: 'violet', position: 2, enabled: false, badgeCount: 0 }
]

describe('tiers tab (mounted)', () => {
  test('renders one row per tier with its localized name and badge count', () => {
    const wrapper = mount(TiersTab, {
      props: { store: { load: async () => {}, reorderTiers: async () => {}, updateTier: async () => {} }, tiers: tierRows, colorChoices: ['amber', 'slate', 'violet'], canManage: true },
      global: { stubs: { UButton: UButtonStub, USwitch: USwitchStub, UIcon: UIconStub } }
    })
    try {
      const rows = wrapper.findAll('li[data-tier]')
      expect(rows).toHaveLength(3)
      expect(rows[0]!.text()).toContain('金牌')
      expect(rows[0]!.text()).toContain('admin.badges.tiers.badgeCount')
      expect(rows[2]!.attributes('data-enabled')).toBe('false')
    } finally {
      wrapper.unmount()
    }
  })

  test('moving a tier down reorders by sending every key in the new order', async () => {
    const reorderTiers = mock(async () => {})
    const wrapper = mount(TiersTab, {
      props: { store: { load: async () => {}, reorderTiers, updateTier: async () => {} }, tiers: tierRows, colorChoices: [], canManage: true },
      global: { stubs: { UButton: UButtonStub, USwitch: USwitchStub, UIcon: UIconStub } }
    })
    try {
      const firstRow = wrapper.findAll('li[data-tier]')[0]!
      const down = firstRow.findAll('button').find(button => button.attributes('title') === 'admin.badges.tiers.moveDown')
      expect(down).toBeDefined()
      await down!.trigger('click')
      await flushPromises()
      expect(reorderTiers).toHaveBeenCalledWith(['silver', 'gold', 'legendary'])
    } finally {
      wrapper.unmount()
    }
  })

  test('the first row cannot move up and the last cannot move down', () => {
    const wrapper = mount(TiersTab, {
      props: { store: { load: async () => {}, reorderTiers: async () => {}, updateTier: async () => {} }, tiers: tierRows, colorChoices: [], canManage: true },
      global: { stubs: { UButton: UButtonStub, USwitch: USwitchStub, UIcon: UIconStub } }
    })
    try {
      const rows = wrapper.findAll('li[data-tier]')
      const up = rows[0]!.findAll('button').find(button => button.attributes('title') === 'admin.badges.tiers.moveUp')
      const down = rows[2]!.findAll('button').find(button => button.attributes('title') === 'admin.badges.tiers.moveDown')
      expect(up!.attributes('disabled')).toBeDefined()
      expect(down!.attributes('disabled')).toBeDefined()
    } finally {
      wrapper.unmount()
    }
  })

  test('a read-only admin gets no create button and disabled row actions', () => {
    const wrapper = mount(TiersTab, {
      props: { store: { load: async () => {}, reorderTiers: async () => {}, updateTier: async () => {} }, tiers: tierRows, colorChoices: [], canManage: false },
      global: { stubs: { UButton: UButtonStub, USwitch: USwitchStub, UIcon: UIconStub } }
    })
    try {
      expect(wrapper.text()).not.toContain('admin.badges.tiers.create')
      const edit = wrapper.findAll('button').find(button => button.attributes('title') === 'admin.badges.tiers.edit')
      expect(edit!.attributes('disabled')).toBeDefined()
    } finally {
      wrapper.unmount()
    }
  })
})

// --- 删除弹窗：没有迁移目标时不能删 ---
const UModalStub = defineComponent({ name: 'UModal', props: ['open'], template: '<div><slot name="content" /></div>' })
const UFormFieldStub = defineComponent({
  name: 'UFormField',
  props: ['label'],
  template: '<div><span>{{ label }}</span><slot /><slot name="hint" /></div>'
})
const USelectStub = defineComponent({
  name: 'USelect',
  props: ['modelValue', 'items'],
  emits: ['update:modelValue'],
  template: '<select />'
})

const deleteDialogPath = fileURLToPath(new URL('../../app/components/admin/badges/SFAdminBadgeTierDeleteDialog.vue', import.meta.url))
const DeleteDialog = await compileVueSfc(deleteDialogPath, 'tier-delete-dialog', {
  computed,
  ref,
  watch,
  useI18n: () => ({ t: (key: string) => key }),
  useToast: () => ({ add: () => {} }),
  apiErrorMessage: () => ''
})

const deleteTargets = [
  { key: 'gold', name: { zh: '金牌', en: 'Gold' }, icon: 'i-lucide-crown', color: 'amber', position: 0, enabled: true, badgeCount: 3 }
]

function mountDeleteDialog(store: Record<string, unknown>) {
  return mount(DeleteDialog, {
    props: {
      tier: { key: 'silver', name: { zh: '银牌', en: 'Silver' }, icon: 'i-lucide-medal', color: 'slate', position: 1, enabled: true, badgeCount: 2 },
      targets: deleteTargets,
      store,
      open: true
    },
    global: { stubs: { UModal: UModalStub, UFormField: UFormFieldStub, USelect: USelectStub, UButton: UButtonStub } }
  })
}

describe('tier delete dialog (mounted)', () => {
  test('delete stays disabled until a migration target is picked', async () => {
    const deleteTier = mock(async () => true)
    const wrapper = mountDeleteDialog({ load: async () => {}, deleteTier })
    try {
      const submit = () => wrapper.findAll('button').find(button => button.text() === 'admin.badges.actions.delete')!
      expect(submit().attributes('disabled')).toBeDefined()
      await submit().trigger('click')
      expect(deleteTier).not.toHaveBeenCalled()

      await wrapper.findComponent(USelectStub).vm.$emit('update:modelValue', 'gold')
      await flushPromises()
      expect(submit().attributes('disabled')).toBeUndefined()
      // 第一次点击只进确认态。
      await submit().trigger('click')
      expect(deleteTier).not.toHaveBeenCalled()
      expect(wrapper.text()).toContain('admin.badges.tiers.remove.confirmBody')
      // 第二次才真删：确认态的按钮文案已经换成了「确认删除等级」。
      const confirm = wrapper.findAll('button').find(button => button.text() === 'admin.badges.tiers.remove.confirm')!
      await confirm.trigger('click')
      await flushPromises()
      expect(deleteTier).toHaveBeenCalledWith('silver', 'gold')
    } finally {
      wrapper.unmount()
    }
  })

  test('with no other enabled tier the dialog explains why and blocks deletion', () => {
    const wrapper = mount(DeleteDialog, {
      props: {
        tier: { key: 'silver', name: { zh: '银牌', en: 'Silver' }, icon: '', color: 'slate', position: 1, enabled: true, badgeCount: 2 },
        targets: [],
        store: { load: async () => {}, deleteTier: async () => true },
        open: true
      },
      global: { stubs: { UModal: UModalStub, UFormField: UFormFieldStub, USelect: USelectStub, UButton: UButtonStub } }
    })
    try {
      expect(wrapper.text()).toContain('admin.badges.tiers.remove.noTarget')
      const submit = wrapper.findAll('button').find(button => button.text() === 'admin.badges.actions.delete')!
      expect(submit.attributes('disabled')).toBeDefined()
    } finally {
      wrapper.unmount()
    }
  })
})
