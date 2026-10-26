import { describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount } from '../helpers/vueSfc'

const globals = globalThis as typeof globalThis & {
  useI18n?: () => { t: (key: string, params?: Record<string, unknown>) => string }
}
globals.useI18n = () => ({
  t: (key, params) => params?.value ? `${key}:${String(params.value)}` : key
})

const componentPath = fileURLToPath(
  new URL('../../app/components/admin/identity/users/SFAdminUserListToolbar.vue', import.meta.url)
)
const Toolbar = await compileVueSfc(componentPath, 'admin-user-list-toolbar')
// 排序参数由列表 composable 负责；拆分后页面只做装配。
const list = await Bun.file(new URL('../../app/composables/admin/identity/useAdminUserList.ts', import.meta.url)).text()

describe('admin user sorting', () => {
  test('sends the selected stable server-side sorting parameters', () => {
    expect(list).toContain("params.set('sortBy', sortBy.value)")
    expect(list).toContain("params.set('sortOrder', sortOrder.value)")
    expect(list).toContain('watch([status, roleKey, sortBy, sortOrder]')
  })

  test('lets the operator select a field and direction', async () => {
    const wrapper = mount(Toolbar, {
      props: {
        search: '',
        status: '',
        roleKey: '',
        sortBy: 'createdAt',
        sortOrder: 'desc',
        roleOptions: [],
        pending: false,
        total: 12,
        perPage: 20
      },
      global: {
        stubs: {
          UDashboardToolbar: {
            template: '<div><slot name="left" /><slot name="right" /></div>'
          },
          UInput: true,
          UButton: { template: '<button><slot /></button>' },
          UBadge: { template: '<span><slot /></span>' }
        }
      }
    })
    try {
      await wrapper.get('[data-testid="admin-user-sort-field"]').setValue('username')
      await wrapper.get('[data-testid="admin-user-sort-order"]').setValue('asc')
      expect(wrapper.emitted('update:sortBy')).toEqual([['username']])
      expect(wrapper.emitted('update:sortOrder')).toEqual([['asc']])
    } finally {
      wrapper.unmount()
    }
  })
})
