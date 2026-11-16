import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { computed, defineComponent, h, ref, toRef } from 'vue'
import {
  createPostRiskVerification,
  isPostRiskVerificationReason,
  postRiskChallengeUrl,
  postRiskMessageKey,
  postRiskVerificationPayload,
  withHumanVerification
} from '~/utils/forum/postRiskVerification'
import { compileVueSfc, flushPromises, mount } from '../helpers/vueSfc'

/**
 * P3-3：发帖 / 回帖的 post_risk 人机验证（前端半边）。
 *
 * 契约（后端 70c1ab86f 已落地）：
 * - 请求体可选字段 `humanVerification: { provider: 'altcha', token }`；
 * - 开关关闭 → 前端不渲染 widget、请求体里不出现该字段；
 * - 开关开启 → 渲染 ALTCHA（challenge purpose=post_risk），解出的 payload 作为 token；
 * - 422 `human_verification.required` → 即使前端没提前渲染也要把 widget 拉起来（双保险）。
 *
 * 这里的断言分两层：控制器是真实实现（`createPostRiskVerification`），快捷回复组件是
 * 真实 SFC 渲染；只有「发帖页 / 高级回复抽屉」的模板位置用源码契约钉住（两者依赖
 * useAsyncData 与 USlideover，整页挂载代价过高）。
 */

const zhCN = JSON.parse(
  readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
) as Record<string, unknown>
const enUS = JSON.parse(
  readFileSync(new URL('../../i18n/locales/en-US.json', import.meta.url), 'utf8')
) as Record<string, unknown>

function lookup(messages: Record<string, unknown>, path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    messages
  )
}

function translator(messages: Record<string, unknown>) {
  return (key: string) => {
    const value = lookup(messages, key)
    return typeof value === 'string' ? value : key
  }
}

const translateZh = translator(zhCN)

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
    translate: translateZh
  })
}

/** ofetch 形态的错误信封：后端 MapHumanVerificationError 产出的形状。 */
function verificationError(reason: string, fieldCode = reason) {
  return {
    data: {
      code: 422,
      message: 'human verification failed',
      data: { reason, fields: { humanVerification: [fieldCode] } }
    }
  }
}

function verifiedEvent(payload: string) {
  return { detail: { state: 'verified', payload } } as unknown as Event
}

function stateEvent(state: string) {
  return { detail: { state } } as unknown as Event
}

describe('post_risk human verification controller', () => {
  test('开关关闭时不要求验证，请求体里不出现 humanVerification', () => {
    const postRisk = createController(false)

    // 渲染门：模板以 enabled 决定是否挂 altcha-widget。
    expect(postRisk.enabled.value).toBe(false)
    expect(postRisk.payload()).toBeUndefined()

    const body = withHumanVerification({ title: '标题', content: { rawContent: '正文' } }, postRisk.payload())
    expect('humanVerification' in body).toBe(false)
    expect(JSON.stringify(body)).not.toContain('humanVerification')
  })

  test('开关开启时渲染条件成立，解出的 payload 原样作为 token 进入请求体', () => {
    const postRisk = createController(true)
    expect(postRisk.enabled.value).toBe(true)
    expect(postRisk.challengeUrl.value).toBe('http://api.sforum.test/human-verification/challenge?purpose=post_risk')

    // 未解题时 token 为空串：仍带字段（服务端会以 required 拒绝并提示）。
    expect(postRisk.payload()).toEqual({ provider: 'altcha', token: '' })

    postRisk.handleVerified(verifiedEvent('altcha-solution'))
    expect(postRisk.token.value).toBe('altcha-solution')

    const body = withHumanVerification({ title: '标题' }, postRisk.payload())
    expect(body.humanVerification).toEqual({ provider: 'altcha', token: 'altcha-solution' })
  })

  test('statechange 的 verified 与 expired/unverified 分支不会留下过期 token', () => {
    const postRisk = createController(true)

    postRisk.handleStateChange(verifiedEvent('payload-from-statechange'))
    expect(postRisk.token.value).toBe('payload-from-statechange')

    postRisk.handleStateChange(stateEvent('expired'))
    expect(postRisk.token.value).toBe('')
  })

  test('422 human_verification.required 即使开关未下发也会把 widget 拉起来并本地化提示', () => {
    const postRisk = createController(false)
    expect(postRisk.enabled.value).toBe(false)

    const captured = postRisk.capture(verificationError('human_verification.required'))

    expect(captured).toBe(true)
    expect(postRisk.enabled.value).toBe(true)
    expect(postRisk.fieldMessage.value).toBe(translateZh('errors.humanVerificationRequired'))
  })

  test('已消费的 token 在 expired/replayed/invalid 后必须重置，不能带着旧 token 重试', () => {
    for (const reason of ['human_verification.expired', 'human_verification.replayed', 'human_verification.invalid']) {
      const postRisk = createController(true)
      postRisk.handleVerified(verifiedEvent('stale-token'))
      expect(postRisk.token.value).toBe('stale-token')

      expect(postRisk.capture(verificationError(reason))).toBe(true)
      expect(postRisk.token.value).toBe('')
      expect(postRisk.fieldMessage.value).not.toBe('')
      expect(postRisk.enabled.value).toBe(true)
    }
  })

  test('限流只提示、不改变渲染来源，普通业务错误不被本模块接管', () => {
    const postRisk = createController(false)

    expect(postRisk.capture(verificationError('rate_limit.exceeded'))).toBe(true)
    expect(postRisk.fieldMessage.value).toBe(translateZh('errors.rateLimited'))
    expect(postRisk.enabled.value).toBe(false)

    const businessError = verificationError('forum.topic_cooldown')
    expect(postRisk.capture(businessError)).toBe(false)
  })

  test('成功提交后 settle 清空 token 与提示，下一次发布重新取题', () => {
    const postRisk = createController(true)
    postRisk.handleVerified(verifiedEvent('once-only'))
    postRisk.capture(verificationError('human_verification.replayed'))

    postRisk.settle()

    expect(postRisk.token.value).toBe('')
    expect(postRisk.fieldMessage.value).toBe('')
    expect(postRisk.payload()).toEqual({ provider: 'altcha', token: '' })
  })

  test('开关是响应式的：后台打开场景后无需刷新页面即可开始要求验证', () => {
    const scenario = ref(false)
    const postRisk = createPostRiskVerification({
      scenarioEnabled: () => scenario.value,
      widgetSettings,
      apiBaseUrl: 'http://api.sforum.test',
      locale: 'en-US',
      translate: translator(enUS)
    })

    expect(postRisk.enabled.value).toBe(false)
    expect(postRisk.language.value).toBe('en')

    scenario.value = true
    expect(postRisk.enabled.value).toBe(true)
    expect(postRisk.payload()).toEqual({ provider: 'altcha', token: '' })
  })

  test('challenge 端点、文案键与 provider 口径与登录侧一致', () => {
    expect(postRiskChallengeUrl('http://127.0.0.1:8080')).toBe(
      'http://127.0.0.1:8080/human-verification/challenge?purpose=post_risk'
    )
    expect(postRiskMessageKey('human_verification.required')).toBe('errors.humanVerificationRequired')
    expect(postRiskMessageKey('human_verification.expired')).toBe('errors.humanVerificationExpired')
    expect(postRiskMessageKey('human_verification.replayed')).toBe('errors.humanVerificationReplayed')
    expect(postRiskMessageKey('rate_limit.exceeded')).toBe('errors.rateLimited')
    expect(postRiskMessageKey('human_verification.something_new')).toBe('errors.humanVerificationInvalid')
    expect(isPostRiskVerificationReason('human_verification.required')).toBe(true)
    expect(isPostRiskVerificationReason('rate_limit.exceeded')).toBe(true)
    expect(isPostRiskVerificationReason('forum.revision_conflict')).toBe(false)
    expect(postRiskVerificationPayload(false, 'ignored')).toBeUndefined()
    expect(postRiskVerificationPayload(true, 'kept')).toEqual({ provider: 'altcha', token: 'kept' })
  })

  test('中英文案键集一致（新增 key 不能只落一边）', () => {
    for (const key of ['composer.humanVerificationLabel', 'composer.humanVerificationHint']) {
      expect(typeof lookup(zhCN, key)).toBe('string')
      expect(typeof lookup(enUS, key)).toBe('string')
    }
  })
})

/* ------------------------------------------------------------------ */
/* 真实 SFC 渲染：快捷回复（SFTopicReplyComposer）                      */
/* ------------------------------------------------------------------ */

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

const EditorStub = defineComponent({
  name: 'LazySFEditor',
  emits: ['submit', 'cancel', 'content-change', 'update:modelValue'],
  setup() {
    return () => h('div', { 'data-testid': 'editor-stub' })
  }
})

const AvatarStub = defineComponent({ name: 'SFAvatar', setup: () => () => h('span') })
const AlertStub = defineComponent({
  name: 'SFAlert',
  props: { title: { type: String, default: '' }, variant: { type: String, default: '' } },
  setup: props => () => h('div', { 'data-testid': 'alert-stub' }, props.title)
})
/** Nuxt 的 ClientOnly：单测里直接渲染默认插槽（DOM 已由 happy-dom 提供）。 */
const ClientOnlyStub = defineComponent({
  name: 'ClientOnly',
  setup: (_, { slots }) => () => slots.default?.()
})

const replyComposerPath = fileURLToPath(
  new URL('../../app/components/forum/SFTopicReplyComposer.vue', import.meta.url)
)

// 控制器本身已是 ref 集合，这里用普通变量持有：再套一层 ref 会把内部 ref 深度解包，
// 与生产里 props 传递的实际形态不一致。
let currentController: ReturnType<typeof createController> | null = null

const SFTopicReplyComposer = await compileVueSfc(replyComposerPath, 'sf-topic-reply-composer', {
  ref,
  computed,
  toRef,
  useI18n: () => ({ t: translateZh }),
  useForumContentLimits: () => ({ limits: ref({ mentionsEnabled: true }) }),
  useTopicCommentSubmission: () => {
    const postRisk = currentController!
    return {
      replyMarkdown: ref(''),
      replySubmitting: ref(false),
      replyError: ref(''),
      showReplyError: ref(false),
      commentCooldownActive: ref(false),
      replyDisplayError: ref(''),
      postRisk,
      submitReply: async () => null
    }
  },
  LazySFEditor: EditorStub,
  SFAvatar: AvatarStub,
  SFAlert: AlertStub
})

function mountReplyComposer(scenarioEnabled: boolean) {
  currentController = createController(scenarioEnabled)
  return mount(SFTopicReplyComposer, {
    props: {
      topic: { id: 1, title: '主题', status: 'active' },
      refreshComments: async () => undefined
    },
    global: {
      components: {
        'altcha-widget': AltchaStub,
        LazySFEditor: EditorStub,
        SFAvatar: AvatarStub,
        SFAlert: AlertStub,
        ClientOnly: ClientOnlyStub
      }
    }
  })
}

describe('post_risk 渲染契约（快捷回复真实组件）', () => {
  test('开关关闭：DOM 里没有 ALTCHA widget', async () => {
    const wrapper = mountReplyComposer(false)
    await flushPromises()

    expect(wrapper.find('[data-testid="altcha-widget"]').exists()).toBe(false)
    expect(wrapper.find('#topic-reply-human-verification-label').exists()).toBe(false)
    wrapper.unmount()
  })

  test('开关开启：渲染 ALTCHA widget，challenge 指向 post_risk', async () => {
    const wrapper = mountReplyComposer(true)
    await flushPromises()

    const widget = wrapper.findComponent(AltchaStub)
    expect(widget.exists()).toBe(true)
    expect(widget.props('challenge')).toBe('http://api.sforum.test/human-verification/challenge?purpose=post_risk')
    expect(widget.props('language')).toBe('zh-cn')
    // 可访问性：label 与 widget 通过 aria-labelledby 关联，键盘用户能看到同一个名字。
    const label = wrapper.find('#topic-reply-human-verification-label')
    expect(label.exists()).toBe(true)
    expect(widget.attributes('aria-labelledby')).toBe('topic-reply-human-verification-label')
    wrapper.unmount()
  })

  test('422 human_verification.required 后 widget 出现并显示提示', async () => {
    const wrapper = mountReplyComposer(false)
    await flushPromises()
    expect(wrapper.find('[data-testid="altcha-widget"]').exists()).toBe(false)

    // 模拟后端拒绝：控制器捕获后必须把 widget 拉起来。
    currentController!.capture(verificationError('human_verification.required'))
    await flushPromises()

    expect(wrapper.findComponent(AltchaStub).exists()).toBe(true)
    const error = wrapper.find('#topic-reply-human-verification-error')
    expect(error.exists()).toBe(true)
    expect(error.attributes('role')).toBe('alert')
    expect(error.text()).toBe(translateZh('errors.humanVerificationRequired'))
    // 失败提示同时挂在 widget 上，读屏软件从控件就能知道「这次要重做验证」。
    expect(wrapper.findComponent(AltchaStub).attributes('aria-invalid')).toBe('true')
    wrapper.unmount()
  })

  test('widget 解出 payload 后 token 进入回帖请求体', async () => {
    const wrapper = mountReplyComposer(true)
    await flushPromises()

    await wrapper.findComponent(AltchaStub).vm.$emit('verified', { detail: { payload: 'solved-by-user' } })
    await flushPromises()

    const payload = currentController!.payload()
    expect(payload).toEqual({ provider: 'altcha', token: 'solved-by-user' })
    const body = withHumanVerification({ parentId: null, content: {} }, payload)
    expect(body.humanVerification).toEqual({ provider: 'altcha', token: 'solved-by-user' })
    wrapper.unmount()
  })
})

/* ------------------------------------------------------------------ */
/* 发帖页 / 高级回复抽屉：模板位置与请求体接线（源码契约）              */
/* ------------------------------------------------------------------ */

function source(relative: string) {
  return readFileSync(new URL(relative, import.meta.url), 'utf8')
}

describe('post_risk 接线契约（发帖页与高级回复抽屉）', () => {
  const composerPage = source('../../app/components/forum/SFTopicComposerPage.vue')
  const commentDrawer = source('../../app/components/forum/SFTopicCommentComposerDrawer.vue')
  const submission = source('../../app/composables/forum/useTopicCommentSubmission.ts')
  const commentDrawerProvider = source('../../app/composables/forum/useTopicCommentComposerDrawer.ts')
  const forumApi = source('../../app/composables/forum/useForumApi.ts')

  test('发帖页只在 postRiskEnabled 时渲染 widget，并把 payload 放进 createTopic', () => {
    expect(composerPage).toContain('usePostRiskVerification()')
    expect(composerPage).toContain('v-if="postRiskEnabled"')
    expect(composerPage).toContain('aria-labelledby="topic-composer-human-verification-label"')
    expect(composerPage).toContain('...postRiskField()')
    expect(composerPage).toContain('if (capturePostRisk(error))')
    expect(composerPage).toContain('settlePostRisk()')
  })

  test('高级回复抽屉通过注入复用同一控制器，编辑模式不渲染 widget', () => {
    expect(commentDrawer).toContain('inject(TOPIC_COMMENT_POST_RISK_KEY)')
    expect(commentDrawer).toContain("visible: toValue(postRisk.enabled) && props.mode !== 'edit'")
    expect(commentDrawer).toContain('@verified="postRisk.handleVerified"')
    // 提供方与提交方同源：注入键与控制器都在 useTopicCommentComposerDrawer 里。
    expect(commentDrawerProvider).toContain('provide(TOPIC_COMMENT_POST_RISK_KEY, postRisk)')
    expect(commentDrawerProvider).toContain('postRisk,')
  })

  test('话题详情页保持 legacy 行数上限内（不为这次改动提基线）', () => {
    const page = source('../../app/components/forum/SFTopicShowPage.vue')
    expect(page).not.toContain('composerPostRisk')
    // 与 `node tests/validate-architecture-boundaries.mjs` 同口径：去掉结尾换行后数行。
    expect(page.replace(/\n$/, '').split('\n').length).toBeLessThanOrEqual(1118)
  })

  test('回帖请求体与发帖同口径：payload 只在有值时挂字段', () => {
    expect(submission).toContain('postRisk.payload()')
    expect(submission).toContain('if (postRisk.capture(error))')
    expect(submission).toContain('postRisk.settle()')
    expect(forumApi).toContain('withHumanVerification({ parentId: parentId ?? null, content }, humanVerification)')
    expect(forumApi).toContain('}, humanVerification)')
  })
})
