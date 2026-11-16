import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import {
  computed,
  defineComponent,
  h,
  inject,
  onBeforeUnmount,
  provide,
  ref,
  shallowRef,
  watch,
  type Component
} from 'vue'
import { createPostRiskVerification } from '~/utils/forum/postRiskVerification'
import { TOPIC_COMMENT_POST_RISK_KEY } from '~/composables/forum/useTopicCommentComposerDrawer'
import { compileSfcSource, compileVueSfc, flushPromises, mount } from '../helpers/vueSfc'

/**
 * P3-3 的**渲染级**证据：发帖页与高级回复抽屉的「卡片出现/消失」不能只靠源码字符串断言
 * （台账「审查发现」第 4 条：静态棘轮只证形状，不证语义）。
 *
 * 这里挂载真实 SFC：
 * - `SFTopicComposerPage.vue`（async setup，用 Suspense 包一层）；
 * - `SFTopicCommentComposerDrawer.vue`（含 provide/inject 缺失即抛错）。
 *
 * 只有 Nuxt 自动导入与第三方 UI 组件被替换成桩，`usePostRiskVerification` 注入的是
 * 真实控制器（`createPostRiskVerification`），所以「开关关闭不渲染 / 开关开启渲染 /
 * 解出 token 进请求体」全部由真实状态机驱动。
 */

const zhCN = JSON.parse(
  readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
) as Record<string, unknown>

function lookup(path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    zhCN
  )
}

function t(key: string, params: Record<string, unknown> = {}) {
  const template = lookup(key)
  const text = typeof template === 'string' ? template : key
  return text.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
}

const widgetSettings = {
  type: 'checkbox' as const,
  auto: 'off' as const,
  display: 'standard' as const,
  hideLogo: true,
  hideFooter: true,
  workers: 2,
  minDuration: 500
}

function createController(scenarioEnabled: boolean) {
  return createPostRiskVerification({
    scenarioEnabled,
    widgetSettings,
    apiBaseUrl: 'http://api.sforum.test',
    locale: 'zh-CN',
    translate: t
  })
}

const AltchaStub = defineComponent({
  name: 'altcha-widget',
  props: {
    challenge: { type: String, default: '' },
    configuration: { type: String, default: '' },
    language: { type: String, default: '' },
    display: { type: String, default: '' },
    type: { type: String, default: '' },
    workers: { type: Number, default: 0 },
    auto: { type: String, default: '' }
  },
  emits: ['verified', 'expired', 'statechange'],
  setup() {
    return () => h('div', { 'data-testid': 'altcha-widget' })
  }
})

const ClientOnlyStub = defineComponent({
  name: 'ClientOnly',
  setup: (_, { slots }) => () => slots.default?.()
})

/** 通用桩：渲染默认插槽，让「卡片标题在不在」这类断言仍然看真实 DOM 结构。 */
function stubComponent(name: string) {
  return defineComponent({
    name,
    inheritAttrs: false,
    setup: (_, { slots }) => () => h('div', { 'data-stub': name }, slots.default?.())
  })
}

/* ------------------------------------------------------------------ */
/* 发帖页                                                              */
/* ------------------------------------------------------------------ */

const composerPagePath = fileURLToPath(
  new URL('../../app/components/forum/SFTopicComposerPage.vue', import.meta.url)
)

const limits = {
  topicTitleMinRunes: 0,
  topicTitleMaxRunes: 120,
  topicContentMinRunes: 0,
  topicContentMaxRunes: 50000,
  tagMinPerTopic: 0,
  tagMaxPerTopic: 5,
  defaultCategorySlug: '',
  tagCreationMode: 'free',
  mentionsEnabled: true
}

type CreateTopicCall = Record<string, unknown>

const createdTopicCalls: CreateTopicCall[] = []
let currentController: ReturnType<typeof createController> | null = null

const SFTopicComposerPage = await compileVueSfc(composerPagePath, 'sf-topic-composer-page', {
  ref,
  computed,
  watch,
  onMounted: (hook: () => void) => hook(),
  onBeforeUnmount: () => undefined,
  useI18n: () => ({ t, locale: ref('zh-CN') }),
  useLocalePath: () => (path: string) => path,
  useWebOptions: () => ({ seoSettings: computed(() => ({ topicUrlMode: 'id' })) }),
  useToast: () => ({ add: () => undefined }),
  useState: <T>(_key: string, init: () => T) => ref(init()),
  navigateTo: async () => undefined,
  apiErrorMessage: () => '',
  apiErrorFields: () => ({}),
  useAsyncData: async (_key: string, fn: () => Promise<unknown>) => ({
    data: ref(await fn()),
    pending: ref(false)
  }),
  useSForumSeo: () => undefined,
  userDisplayName: () => 'alice',
  FORUM_PERMISSIONS: { topicCreate: 'forum.topic.create' },
  usePermissions: () => ({ can: () => true }),
  useAuthSession: () => ({ user: ref({ id: 1, username: 'alice' }) }),
  useForumContentLimits: () => ({
    limits: ref(limits),
    runeLength: (value: string) => value.length,
    validateTopicTitle: () => '',
    validateTopicBody: () => '',
    validateTagCount: () => ''
  }),
  useForumCooldownError: () => ({
    active: ref(false),
    message: ref(''),
    capture: () => false
  }),
  useForumApi: () => ({
    listCategoryGroups: async () => [],
    listTags: async () => [],
    listComposerToolbarActions: async () => [],
    applyComposerToolbarAction: async () => undefined,
    createTopic: async (input: CreateTopicCall) => {
      createdTopicCalls.push(input)
      return { id: 42, slug: 'topic-42', status: 'active' }
    }
  }),
  usePostRiskVerification: () => currentController!,
  usePublicSidebarDrawer: () => ({ closeDrawer: () => undefined }),
  forumContentFromEditorPayload: (payload: { markdown?: string }) => ({
    rawContent: payload.markdown ?? '',
    sourceFormat: 'markdown'
  }),
  forumTopicPath: () => '/t/42',
  isForumTagSlug: () => true,
  sforumDocumentPublishIssue: () => '',
  sforumPublishFailureKey: () => 'errors.unknown',
  onBeforeRouteLeave: () => undefined,
  SFTopicComposerRightRail: stubComponent('SFTopicComposerRightRail'),
  SFTopicComposerLeftRail: stubComponent('SFTopicComposerLeftRail'),
  SFTagInput: stubComponent('SFTagInput'),
  SFHomeNavigation: stubComponent('SFHomeNavigation'),
  SFCategorySelect: stubComponent('SFCategorySelect'),
  SFResponsivePublicSidebar: stubComponent('SFResponsivePublicSidebar'),
  SFPublicMobileUserMenu: stubComponent('SFPublicMobileUserMenu'),
  SFPublicMobileRightDrawerHeader: stubComponent('SFPublicMobileRightDrawerHeader')
})

// async setup 必须落在 Suspense 边界里；Page 走全局注册解析。
const ComposerHarness = await compileSfcSource(
  '<script setup lang="ts">\nconst harnessReady = true\n</script><template><Suspense><Page /></Suspense></template>',
  'composer-harness',
  {},
  'composer-harness.vue'
)

async function mountComposerPage(scenarioEnabled: boolean) {
  currentController = createController(scenarioEnabled)
  createdTopicCalls.length = 0
  const wrapper = mount(ComposerHarness as Component, {
    global: {
      components: {
        Page: SFTopicComposerPage as Component,
        'altcha-widget': AltchaStub,
        ClientOnly: ClientOnlyStub,
        LazySFEditor: stubComponent('LazySFEditor'),
        SFInput: stubComponent('SFInput'),
        SFButton: stubComponent('SFButton'),
        SFCard: stubComponent('SFCard'),
        SFAlert: stubComponent('SFAlert'),
        SFEmptyState: stubComponent('SFEmptyState'),
        SFRegionOutlet: stubComponent('SFRegionOutlet'),
        SFAvatar: stubComponent('SFAvatar'),
        UIcon: stubComponent('UIcon'),
        NuxtLink: stubComponent('NuxtLink')
      }
    }
  })
  await flushPromises()
  await flushPromises()
  return wrapper
}

describe('发帖页 post_risk 渲染（真实 SFC + Suspense）', () => {
  test('开关关闭：DOM 里没有 ALTCHA widget，也没有人机验证卡片标题', async () => {
    const wrapper = await mountComposerPage(false)

    expect(wrapper.find('[data-testid="altcha-widget"]').exists()).toBe(false)
    expect(wrapper.find('#topic-composer-human-verification-label').exists()).toBe(false)
    expect(wrapper.text()).not.toContain(t('composer.humanVerificationLabel'))
    wrapper.unmount()
  })

  test('开关开启：渲染 ALTCHA widget，challenge 指向 post_risk', async () => {
    const wrapper = await mountComposerPage(true)

    const widget = wrapper.findComponent(AltchaStub)
    expect(widget.exists()).toBe(true)
    expect(widget.props('challenge')).toBe('http://api.sforum.test/human-verification/challenge?purpose=post_risk')
    expect(wrapper.find('#topic-composer-human-verification-label').text()).toBe(t('composer.humanVerificationLabel'))
    wrapper.unmount()
  })

  test('解出 payload 后发帖请求体带 humanVerification；开关关闭时请求体连键都没有', async () => {
    // 开启：解题 → 提交 → 请求体带 token
    const enabledWrapper = await mountComposerPage(true)
    await enabledWrapper.findComponent(AltchaStub).vm.$emit('verified', { detail: { payload: 'solved-in-composer' } })
    await flushPromises()

    enabledWrapper.findComponent({ name: 'SFInput' }).vm.$emit('update:modelValue', '标题')
    await flushPromises()
    enabledWrapper.findComponent({ name: 'LazySFEditor' }).vm.$emit('submit', {
      markdown: '正文内容',
      text: '正文内容',
      attachmentIds: [],
      pendingUploadCount: 0
    })
    await flushPromises()

    expect(createdTopicCalls).toHaveLength(1)
    expect(createdTopicCalls[0]!.humanVerification).toEqual({ provider: 'altcha', token: 'solved-in-composer' })
    enabledWrapper.unmount()

    // 关闭：同样的操作 → 请求体里没有这个键
    const disabledWrapper = await mountComposerPage(false)
    disabledWrapper.findComponent({ name: 'SFInput' }).vm.$emit('update:modelValue', '标题')
    await flushPromises()
    disabledWrapper.findComponent({ name: 'LazySFEditor' }).vm.$emit('submit', {
      markdown: '正文内容',
      text: '正文内容',
      attachmentIds: [],
      pendingUploadCount: 0
    })
    await flushPromises()

    expect(createdTopicCalls).toHaveLength(1)
    expect('humanVerification' in createdTopicCalls[0]!).toBe(false)
    disabledWrapper.unmount()
  })
})

/* ------------------------------------------------------------------ */
/* 高级回复抽屉                                                        */
/* ------------------------------------------------------------------ */

const drawerPath = fileURLToPath(
  new URL('../../app/components/forum/SFTopicCommentComposerDrawer.vue', import.meta.url)
)

const SlideoverStub = defineComponent({
  name: 'USlideover',
  setup: (_, { slots }) => () =>
    h('div', { 'data-testid': 'slideover' }, [
      slots.title?.(),
      slots.body?.(),
      slots.footer?.()
    ])
})

const SFTopicCommentComposerDrawer = await compileVueSfc(drawerPath, 'sf-topic-comment-composer-drawer', {
  ref,
  shallowRef,
  computed,
  watch,
  // inject/toValue 由 SFC 自己从 'vue' 显式导入（helper 会把 import 改写成 Vue 解构），
  // 这里不能再以同名参数注入，否则重复声明。
  onBeforeUnmount,
  TOPIC_COMMENT_POST_RISK_KEY,
  useI18n: () => ({ t }),
  useToast: () => ({ add: () => undefined }),
  useForumContentLimits: () => ({ limits: ref(limits) })
})

const drawerGlobals = {
  components: {
    'altcha-widget': AltchaStub,
    ClientOnly: ClientOnlyStub,
    USlideover: SlideoverStub,
    LazySFEditor: stubComponent('LazySFEditor'),
    SFAlert: stubComponent('SFAlert'),
    SFAvatar: stubComponent('SFAvatar'),
    SFButton: stubComponent('SFButton'),
    UIcon: stubComponent('UIcon')
  }
}

function drawerProps(mode: 'advanced' | 'reply' | 'edit' = 'reply') {
  return {
    open: true,
    mode,
    editorKey: 'k1',
    topicTitle: '主题标题',
    modelValue: '',
    initialContent: undefined
  }
}

function mountDrawerWithProvider(scenarioEnabled: boolean, mode: 'advanced' | 'reply' | 'edit' = 'reply') {
  const controller = createController(scenarioEnabled)
  const Host = defineComponent({
    name: 'DrawerProviderHost',
    setup() {
      provide(TOPIC_COMMENT_POST_RISK_KEY, controller)
      return () => h(SFTopicCommentComposerDrawer as Component, drawerProps(mode))
    }
  })
  const wrapper = mount(Host, { global: drawerGlobals })
  return { wrapper, controller }
}

describe('高级回复抽屉 post_risk 渲染（真实 SFC + 注入）', () => {
  test('开关关闭：DOM 里没有 ALTCHA widget', () => {
    const { wrapper } = mountDrawerWithProvider(false)

    expect(wrapper.find('[data-testid="altcha-widget"]').exists()).toBe(false)
    expect(wrapper.find('#drawer-human-verification-label').exists()).toBe(false)
    wrapper.unmount()
  })

  test('开关开启：渲染 ALTCHA widget，challenge 指向 post_risk', () => {
    const { wrapper } = mountDrawerWithProvider(true)

    const widget = wrapper.findComponent(AltchaStub)
    expect(widget.exists()).toBe(true)
    expect(widget.props('challenge')).toBe('http://api.sforum.test/human-verification/challenge?purpose=post_risk')
    expect(wrapper.find('#drawer-human-verification-label').exists()).toBe(true)
    wrapper.unmount()
  })

  test('编辑模式即使开关开启也不渲染 widget（PATCH 不需要人机验证）', () => {
    const { wrapper } = mountDrawerWithProvider(true, 'edit')

    expect(wrapper.find('[data-testid="altcha-widget"]').exists()).toBe(false)
    expect(wrapper.find('#drawer-human-verification-label').exists()).toBe(false)
    wrapper.unmount()
  })

  test('缺少 provider 时显式抛错，而不是静默丢掉人机验证', () => {
    // 不带 provide 直接挂载：注入缺失必须立刻炸出来（而不是渲染一个没有人机验证的抽屉）。
    expect(() => mount(SFTopicCommentComposerDrawer as Component, {
      props: drawerProps('reply'),
      global: drawerGlobals
    })).toThrow('post_risk')
  })
})
