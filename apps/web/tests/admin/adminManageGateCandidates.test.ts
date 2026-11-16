import { afterAll, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { ref, type Ref } from 'vue'

import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import { useAuthSession, type CurrentUser } from '~/composables/identity/useAuthSession'
import { usePermissions } from '~/composables/identity/usePermissions'
import { useAdminForumSettingsTab } from '~/composables/admin/settings/useAdminForumSettingsTab'
import { apiErrorMessage } from '~/composables/useApiClient'
import {
  createDefaultForumSettings,
  forumListSortOptions,
  recommendedListDefaultSort
} from '~/utils/admin/adminForum'

/**
 * task-24：后台「可管理」门只认细粒度子键。
 *
 * 父键（`settings.manage`）是后端 `ExpandEffectivePermissions` 的展开来源，持父权限的
 * 会话里**始终带着它**；把它写进 `can(子键) || can(父键)` 的候选，管理员 deny 掉的子键
 * 就会在页面上重新放行（控件可编辑、保存按钮可点，提交后 API 403）。
 *
 * 这里全部用**真实挂载**取证：允许路径（父权限展开后的子键在集合内）控件可用，denied
 * 路径（集合里没有该子键）控件不可用 / 入口不渲染。把父键加回候选，denied 用例必红。
 */

const globals = globalThis as Record<string, unknown>
const saved = {
  useI18n: globals.useI18n,
  useToast: globals.useToast,
  useApiClient: globals.useApiClient,
  useState: globals.useState
}
afterAll(() => {
  globals.useI18n = saved.useI18n
  globals.useToast = saved.useToast
  globals.useApiClient = saved.useApiClient
  globals.useState = saved.useState
})

const SETTINGS_PARENT = 'settings.manage'
const SETTINGS_SITE = 'settings.site.manage'
const FORUM_SETTINGS = 'forum.settings.manage'
const ENTITY_META = 'entity_meta.manage'
const ALL_SETTINGS_CHILDREN = [
  SETTINGS_SITE,
  'settings.mail.manage',
  'settings.notifications.manage',
  'settings.avatar.manage',
  'settings.appearance.manage',
  FORUM_SETTINGS,
  ENTITY_META
]

/**
 * 建模「后端下发什么」：API 侧 `ExpandEffectivePermissions(keys, denied)` 保留父键、
 * 展开子键，并跳过被显式 deny 的子键。不是前端判定逻辑。
 */
function sessionPermissions(denied: readonly string[] = []): string[] {
  return [SETTINGS_PARENT, ...ALL_SETTINGS_CHILDREN.filter(child => !denied.includes(child))]
}

function operator(permissions: string[]): CurrentUser {
  return {
    id: 7,
    username: 'operator',
    displayName: 'Operator',
    locale: 'zh-CN',
    appearance: null,
    status: 'active',
    emailVerified: true,
    isInitialSuperAdmin: false,
    avatar: { kind: 'initials', url: '', alt: 'Operator' },
    roleKeys: ['operator'],
    permissions
  }
}

function installGlobals(current: Ref<CurrentUser | null>, request: () => Promise<unknown>) {
  Object.assign(globals, {
    ref: testVue.ref,
    reactive: testVue.reactive,
    computed: testVue.computed,
    watch: testVue.watch,
    toRef: testVue.toRef,
    nextTick: testVue.nextTick,
    onMounted: testVue.onMounted,
    useI18n: () => ({
      locale: ref('zh-CN'),
      t: (key: string) => key,
      te: () => false
    }),
    useToast: () => ({ add: () => {} }),
    useApiClient: () => ({ request }),
    useRouter: () => ({ push: async () => {}, replace: async () => {} }),
    useSeoMeta: () => {},
    useState: <T>(key: string, init: () => T) => (
      key === 'auth:user' ? current as unknown as Ref<T> : ref(init())
    )
  })
}

const frameStub = {
  props: ['tab', 'dirty', 'validationKey', 'saving', 'restoring', 'canSave'],
  template: '<div data-frame :data-can-save="String(canSave)"><slot /></div>'
}
const passthrough = { template: '<div><slot /></div>' }
const SelectStub = {
  props: ['modelValue', 'disabled'],
  emits: ['update:modelValue'],
  template: '<select :disabled="disabled"><slot /></select>'
}
const adminRoutesStub = () => ({ path: (path: string) => `/control-panel${path}` })

/** tab 的 `<script setup>` 里只有 import 被剥掉；自动导入的组件/composable 走这里的注入。 */
const tabBindings = {
  usePermissions,
  useAdminForumSettingsTab,
  SFAdminForumTabFrame: frameStub,
  SFNativeSelect: SelectStub,
  useAdminRoutes: adminRoutesStub,
  forumListSortOptions,
  recommendedListDefaultSort
}

/* ── 后台设置 tab：真实 SFC + 真实 useAdminForumSettingsTab，只桩展示框架 ───────── */

const tabFiles = [
  'SFAdminForumTopicsTab.vue',
  'SFAdminForumBehaviorTab.vue',
  'SFAdminForumCommentsTab.vue',
  'SFAdminForumReadingTab.vue',
  'SFAdminForumGeneralTab.vue'
] as const

const tabComponents = await Promise.all(tabFiles.map(async (file, index) => ({
  file,
  component: await compileVueSfc(
    fileURLToPath(new URL(`../../app/components/admin/settings/forum/tabs/${file}`, import.meta.url)),
    `forum-tab-${index}`,
    tabBindings
  )
})))

async function tabCanSave(file: string, permissions: string[]): Promise<string | undefined> {
  installGlobals(ref(operator(permissions)), async () => ({}))
  const entry = tabComponents.find(candidate => candidate.file === file)
  if (!entry) throw new Error(`unknown tab: ${file}`)
  const wrapper = mount(entry.component, {
    props: {
      settings: createDefaultForumSettings(),
      categories: []
    }
  })
  await flushPromises()
  return wrapper.find('[data-frame]').attributes('data-can-save')
}

describe('后台设置 tab 的可管理门只认子键', () => {
  for (const file of tabFiles) {
    test(`${file}：父权限展开后子键在集合内 → 可保存`, async () => {
      expect(await tabCanSave(file, sessionPermissions())).toBe('true')
    })

    test(`${file}：deny ${FORUM_SETTINGS} → 不可保存`, async () => {
      expect(await tabCanSave(file, sessionPermissions([FORUM_SETTINGS]))).toBe('false')
    })
  }
})

/* ── 三个页面 shell：真实挂载，断言控件可用性 / 入口渲染 ─────────────────────── */

const SwitchStub = {
  props: ['modelValue', 'disabled'],
  emits: ['update:modelValue'],
  template: '<button type="button" data-switch :disabled="disabled" @click="$emit(\'update:modelValue\', !modelValue)" />'
}
const ButtonStub = {
  props: ['disabled', 'loading', 'icon', 'color', 'variant', 'size'],
  emits: ['click'],
  template: '<button type="button" :disabled="disabled" @click="$emit(\'click\')"><slot /></button>'
}
const InputStub = {
  props: ['modelValue', 'type', 'placeholder', 'disabled', 'size', 'icon'],
  emits: ['update:modelValue'],
  template: '<input :value="modelValue" :disabled="disabled" @input="$emit(\'update:modelValue\', $event.target.value)" />'
}
const FieldStub = { template: '<label><slot /></label>' }
const CardStub = { template: '<div><slot name="header" /><slot /><slot name="footer" /></div>' }
const BadgeStub = { template: '<span><slot /></span>' }
const AlertStub = { props: ['color', 'variant', 'icon', 'title', 'description'], template: '<div class="alert">{{ title }}</div>' }
const ModalStub = {
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: '<div v-if="modelValue" class="modal"><slot name="content" /></div>'
}
const pageStubs = {
  UAlert: AlertStub,
  UModal: ModalStub,
  UButton: ButtonStub,
  UInput: InputStub,
  UFormField: FieldStub,
  UBadge: BadgeStub,
  UCard: CardStub,
  USwitch: SwitchStub,
  UCheckbox: SwitchStub,
  UTextarea: InputStub
}

const adminPageStub = () => ({ id: '/stub', icon: 'i-lucide-stub', labelKey: 'admin.nav.stub' })

const FeaturesPage = await compileVueSfc(
  fileURLToPath(new URL('../../app/pages/admin/settings/features.vue', import.meta.url)),
  'admin-features-page',
  {
    useAuthSession,
    useAdminPage: adminPageStub,
    apiErrorMessage,
    announcePolite: () => {},
    useSeoMeta: () => {},
    SFAdminFormFooter: passthrough
  }
)

const EntityMetaPage = await compileVueSfc(
  fileURLToPath(new URL('../../app/pages/admin/entity-meta.vue', import.meta.url)),
  'admin-entity-meta-page',
  {
    useAuthSession,
    useAdminPage: adminPageStub,
    apiErrorMessage,
    useRoute: () => ({ path: '/control-panel/entity-meta', query: {} }),
    SFAdminFormFooter: passthrough,
    SFAdminFixedTabNav: passthrough,
    SFNativeSelect: SelectStub
  }
)

const WebhooksPage = await compileVueSfc(
  fileURLToPath(new URL('../../app/pages/admin/webhooks.vue', import.meta.url)),
  'admin-webhooks-page',
  {
    useAuthSession,
    useAdminPage: adminPageStub,
    apiErrorMessage
  }
)

const featureItems = [
  { name: 'demo.flag', value: 'enabled', public: true, secret: false, secretSet: false }
]

const entityMetaItem = {
  id: 1,
  entityType: 'topic',
  fieldKey: 'demo.extra_field',
  labelZh: '演示字段',
  labelEn: 'Demo field',
  valueType: 'string',
  visibility: 'public',
  required: false,
  enabled: true,
  ownerExtensionId: null
}

function pageRequest(path: string): Promise<unknown> {
  if (path.startsWith('/admin/features')) return Promise.resolve({ items: featureItems, catalog: [] })
  if (path.startsWith('/admin/entity-meta/definitions')) return Promise.resolve({ items: [entityMetaItem] })
  if (path.includes('/endpoints')) return Promise.resolve({ items: [] })
  if (path.includes('/deliveries')) return Promise.resolve({ items: [] })
  return Promise.resolve({ items: [] })
}

async function mountPage(
  component: unknown,
  permissions: string[]
) {
  installGlobals(ref(operator(permissions)), async (path: string) => pageRequest(path))
  const wrapper = mount(component as never, { global: { stubs: pageStubs } })
  await flushPromises()
  return wrapper
}

describe('产品开关页：父键不再是候选', () => {
  test('父权限展开后 settings.site.manage 在集合内 → 开关可用', async () => {
    const wrapper = await mountPage(FeaturesPage, sessionPermissions())
    expect(wrapper.find('[data-switch]').attributes('disabled')).toBeUndefined()
  })

  test(`deny ${SETTINGS_SITE} → 开关全部不可用`, async () => {
    const wrapper = await mountPage(FeaturesPage, sessionPermissions([SETTINGS_SITE]))
    expect(wrapper.find('[data-switch]').attributes('disabled')).toBe('')
  })
})

describe('实体元数据页：父键不再是候选', () => {
  test('父权限展开后 entity_meta.manage 在集合内 → 行操作可用', async () => {
    const wrapper = await mountPage(EntityMetaPage, sessionPermissions())
    const action = wrapper.findAll('button').find(button => button.text() === 'admin.entityMeta.delete')
    expect(action?.attributes('disabled')).toBeUndefined()
  })

  test(`deny ${ENTITY_META} → 行操作不可用`, async () => {
    const wrapper = await mountPage(EntityMetaPage, sessionPermissions([ENTITY_META]))
    const action = wrapper.findAll('button').find(button => button.text() === 'admin.entityMeta.delete')
    expect(action?.attributes('disabled')).toBe('')
  })
})

describe('Webhook 页：父键不再是候选', () => {
  test('父权限展开后 settings.site.manage 在集合内 → 新建入口渲染', async () => {
    const wrapper = await mountPage(WebhooksPage, sessionPermissions())
    const create = wrapper.findAll('button').find(button => button.text() === 'admin.webhooks.create')
    expect(create).toBeDefined()
  })

  test(`deny ${SETTINGS_SITE} → 新建入口不渲染`, async () => {
    const wrapper = await mountPage(WebhooksPage, sessionPermissions([SETTINGS_SITE]))
    const create = wrapper.findAll('button').find(button => button.text() === 'admin.webhooks.create')
    expect(create).toBeUndefined()
  })
})
