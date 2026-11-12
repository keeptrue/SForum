import { describe, expect, mock, test } from 'bun:test'
import { computed, defineComponent, onBeforeUnmount, ref, watch } from 'vue'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount } from '../helpers/vueSfc'
import type { AdminBadgeGrantCandidate } from '~/composables/admin/badges/useAdminBadges'

/**
 * 批量授予的用户选择器：搜索防抖、多选、已持有置灰、键盘可达。
 *
 * 这里刻意用**真实定时器**等待 300ms：用假定时器测防抖，测的是「有没有 setTimeout」，
 * 而不是「连续输入只发最后一次请求」——后者才是运营真正感受到的行为。
 */
const UInputStub = defineComponent({
  name: 'UInput',
  props: { modelValue: { type: String, default: '' }, placeholder: String, disabled: Boolean, icon: String },
  emits: ['update:modelValue', 'keydown'],
  template: '<input class="picker-input" :value="modelValue" :disabled="disabled" @input="$emit(\'update:modelValue\', $event.target.value)" @keydown="$emit(\'keydown\', $event)" />'
})
const UIconStub = defineComponent({ name: 'UIcon', props: { name: String }, template: '<i />' })
const UBadgeStub = defineComponent({ name: 'UBadge', template: '<span class="badge"><slot /></span>' })

const pickerPath = fileURLToPath(new URL('../../app/components/admin/badges/SFAdminBadgeUserPicker.vue', import.meta.url))
const UserPicker = await compileVueSfc(pickerPath, 'badge-user-picker', {
  computed,
  ref,
  watch,
  onBeforeUnmount,
  useI18n: () => ({ t: (key: string) => key })
})

const candidates: AdminBadgeGrantCandidate[] = [
  { userId: 7, username: 'alice', displayName: 'Alice', status: 'active', alreadyGranted: false },
  { userId: 8, username: 'alina', displayName: 'Alina', status: 'active', alreadyGranted: true },
  { userId: 9, username: 'albert', displayName: 'Albert', status: 'suspended', alreadyGranted: false }
]

function labels() {
  return {
    placeholder: 'admin.badges.grant.userPlaceholder',
    emptyLabel: 'admin.badges.grant.search.empty',
    noResultLabel: 'admin.badges.grant.search.noResult',
    alreadyLabel: 'admin.badges.grant.search.already',
    removeLabel: 'admin.badges.grant.search.remove',
    maxLabel: 'admin.badges.grant.search.max'
  }
}

function mountPicker(overrides: Record<string, unknown> = {}) {
  return mount(UserPicker, {
    props: {
      modelValue: [],
      badgeId: 1,
      search: async () => candidates,
      ...labels(),
      ...overrides
    },
    global: { stubs: { UInput: UInputStub, UIcon: UIconStub, UBadge: UBadgeStub } }
  })
}

const wait = (ms: number) => new Promise(resolve => setTimeout(resolve, ms))

describe('SFAdminBadgeUserPicker', () => {
  test('debounces typing into a single search call with the last term', async () => {
    const search = mock(async () => candidates)
    const wrapper = mountPicker({ search })
    try {
      const input = wrapper.get('input.picker-input')
      await input.setValue('a')
      await input.setValue('ali')
      await input.setValue('alic')
      // 防抖窗口内不该发请求。
      expect(search).toHaveBeenCalledTimes(0)
      await wait(340)
      await flushPromises()
      expect(search).toHaveBeenCalledTimes(1)
      expect(search).toHaveBeenCalledWith('alic', 1)
    } finally {
      wrapper.unmount()
    }
  })

  test('an empty query never hits the backend and shows the guidance copy', async () => {
    const search = mock(async () => candidates)
    const wrapper = mountPicker({ search })
    try {
      const input = wrapper.get('input.picker-input')
      await input.setValue(' ')
      await wait(340)
      await flushPromises()
      expect(search).not.toHaveBeenCalled()
      expect(wrapper.text()).toContain('admin.badges.grant.search.empty')
    } finally {
      wrapper.unmount()
    }
  })

  test('results show identity, status and the already-granted marker', async () => {
    const wrapper = mountPicker()
    try {
      await wrapper.get('input.picker-input').setValue('al')
      await wait(340)
      await flushPromises()
      const options = wrapper.findAll('[role="option"]')
      expect(options).toHaveLength(3)
      expect(options[0]!.text()).toContain('Alice')
      expect(options[0]!.text()).toContain('@alice')
      expect(options[0]!.text()).toContain('active')
      expect(options[1]!.text()).toContain('admin.badges.grant.search.already')
      expect(options[1]!.attributes('aria-disabled')).toBe('true')
    } finally {
      wrapper.unmount()
    }
  })

  test('clicking an already-granted row does nothing; a selectable row becomes a chip', async () => {
    const wrapper = mountPicker()
    try {
      await wrapper.get('input.picker-input').setValue('al')
      await wait(340)
      await flushPromises()
      const options = wrapper.findAll('[role="option"]')
      await options[1]!.trigger('mousedown')
      expect(wrapper.emitted('update:modelValue')).toBeUndefined()
      await options[0]!.trigger('mousedown')
      const emitted = wrapper.emitted('update:modelValue')!
      expect(emitted[0]![0]).toEqual([candidates[0]!])
    } finally {
      wrapper.unmount()
    }
  })

  test('Enter picks the first selectable result, skipping already-granted rows', async () => {
    const wrapper = mountPicker({
      search: async () => [candidates[1]!, candidates[2]!]
    })
    try {
      const input = wrapper.get('input.picker-input')
      await input.setValue('al')
      await wait(340)
      await flushPromises()
      await input.trigger('keydown', { key: 'Enter' })
      const emitted = wrapper.emitted('update:modelValue')!
      expect(emitted[0]![0]).toEqual([candidates[2]!])
    } finally {
      wrapper.unmount()
    }
  })

  test('Backspace on an empty query removes the last chip', async () => {
    const wrapper = mountPicker({ modelValue: [candidates[0]!, candidates[2]!] })
    try {
      await wrapper.get('input.picker-input').trigger('keydown', { key: 'Backspace' })
      const emitted = wrapper.emitted('update:modelValue')!
      expect(emitted[0]![0]).toEqual([candidates[0]!])
    } finally {
      wrapper.unmount()
    }
  })

  test('chips expose a remove control with the display name in its label', () => {
    const wrapper = mountPicker({ modelValue: [candidates[0]!] })
    try {
      const remove = wrapper.get('button[aria-label="admin.badges.grant.search.remove"]')
      expect(remove.exists()).toBe(true)
      expect(wrapper.text()).toContain('Alice')
    } finally {
      wrapper.unmount()
    }
  })

  test('reaching the selection cap disables the input and explains the limit', () => {
    const wrapper = mountPicker({ modelValue: [candidates[0]!], max: 1 })
    try {
      expect(wrapper.get('input.picker-input').attributes('disabled')).toBeDefined()
      expect(wrapper.text()).toContain('admin.badges.grant.search.max')
    } finally {
      wrapper.unmount()
    }
  })
})

// --- 授予弹窗的结果区：三态分别计数 + 只重试失败项 ---
const UModalStub = defineComponent({ name: 'UModal', props: ['open'], template: '<div><slot name="content" /></div>' })
const UFormFieldStub = defineComponent({ name: 'UFormField', props: ['label', 'error'], template: '<div><span>{{ label }}</span><slot /></div>' })
const UTextareaStub = defineComponent({
  name: 'UTextarea',
  props: { modelValue: String },
  emits: ['update:modelValue'],
  template: '<textarea />'
})
const UButtonStub = defineComponent({
  name: 'UButton',
  props: { color: String, variant: String, loading: Boolean, disabled: Boolean, icon: String },
  emits: ['click'],
  template: '<button :disabled="disabled" @click="$emit(\'click\')"><slot /></button>'
})
const UserPickerStub = defineComponent({
  name: 'SFAdminBadgeUserPicker',
  props: ['modelValue', 'badgeId', 'search', 'placeholder', 'emptyLabel', 'noResultLabel', 'alreadyLabel', 'removeLabel', 'maxLabel', 'disabled'],
  emits: ['update:modelValue'],
  template: '<div class="picker-stub" />'
})

const grantDialogPath = fileURLToPath(new URL('../../app/components/admin/badges/SFAdminBadgeGrantDialog.vue', import.meta.url))
const GrantDialog = await compileVueSfc(grantDialogPath, 'badge-grant-dialog', {
  computed,
  ref,
  watch,
  useI18n: () => ({ t: (key: string) => key }),
  useToast: () => ({ add: () => {} }),
  apiErrorFields: () => ({}),
  apiErrorMessage: () => '',
  SFAdminBadgeUserPicker: UserPickerStub
})

const grantBadge = {
  id: 5, slug: 'pioneer', name: 'Pioneer', description: '', icon: 'i-lucide-medal', tier: 'gold',
  groupId: null, groupName: null, grantMode: 'manual', metric: null, threshold: null,
  grantedCount: 0, enabled: true, position: 0
}

function mountGrantDialog(store: Record<string, unknown>, badge: Record<string, unknown> = grantBadge) {
  return mount(GrantDialog, {
    props: { badge, store, open: true },
    global: {
      stubs: {
        UModal: UModalStub,
        UFormField: UFormFieldStub,
        UTextarea: UTextareaStub,
        UButton: UButtonStub,
        SFAdminBadgeUserPicker: UserPickerStub
      }
    }
  })
}

describe('SFAdminBadgeGrantDialog batch result', () => {
  test('renders the three result counts and the failed user list', async () => {
    const batchGrant = mock(async () => ({
      granted: 2,
      skipped: 1,
      failed: [{ userId: 9, username: 'albert', reason: 'user_disabled' }]
    }))
    const wrapper = mountGrantDialog({ searchGrantCandidates: async () => [], batchGrant })
    try {
      await wrapper.findComponent(UserPickerStub).vm.$emit('update:modelValue', [candidates[0]!, candidates[2]!])
      await flushPromises()
      const submit = wrapper.findAll('button').find(button => button.text() === 'admin.badges.grant.submit')!
      await submit.trigger('click')
      await flushPromises()
      expect(batchGrant).toHaveBeenCalledWith(5, { userIds: [7, 9], reason: '' })
      const counts = wrapper.findAll('dd').map(node => node.text())
      expect(counts).toEqual(['2', '1', '1'])
      expect(wrapper.text()).toContain('albert')
      expect(wrapper.text()).toContain('user_disabled')
    } finally {
      wrapper.unmount()
    }
  })

  test('retry sends only the failed user ids and accumulates the counts', async () => {
    const batchGrant = mock(async () => ({
      granted: 0,
      skipped: 0,
      failed: [{ userId: 9, username: 'albert', reason: 'user_disabled' }]
    }))
    const wrapper = mountGrantDialog({ searchGrantCandidates: async () => [], batchGrant })
    try {
      await wrapper.findComponent(UserPickerStub).vm.$emit('update:modelValue', [candidates[0]!])
      await flushPromises()
      await wrapper.findAll('button').find(button => button.text() === 'admin.badges.grant.submit')!.trigger('click')
      await flushPromises()
      const retry = wrapper.findAll('button').find(button => button.text() === 'admin.badges.grant.result.retry')!
      await retry.trigger('click')
      await flushPromises()
      expect(batchGrant).toHaveBeenLastCalledWith(5, { userIds: [9], reason: '' })
      expect(batchGrant).toHaveBeenCalledTimes(2)
    } finally {
      wrapper.unmount()
    }
  })

  test('an automatic badge cannot be granted manually', async () => {
    const wrapper = mountGrantDialog({ searchGrantCandidates: async () => [], batchGrant: async () => ({}) }, { ...grantBadge, grantMode: 'automatic' })
    try {
      expect(wrapper.text()).toContain('admin.badges.grant.automaticNotGrantable')
      // 自动徽章不给「看得见点不动」的禁用控件：选择器与提交按钮都不渲染，只留说明 + 取消。
      expect(wrapper.findComponent(UserPickerStub).exists()).toBe(false)
      expect(wrapper.findAll('button').some(button => button.text() === 'admin.badges.grant.submit')).toBe(false)
      expect(wrapper.findAll('button').some(button => button.text() === 'admin.badges.actions.cancel')).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })
})
