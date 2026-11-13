import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import type { Component } from 'vue'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import { shouldShowUserHandle, userDisplayName } from '../../app/utils/forum/userDisplay'
import { resolveHeaderBadge, type HeaderBadgeInput } from '../../app/utils/profile/profileHeaderBadge'
import type { PublicProfile } from '../../app/composables/profile/useProfileApi'
import type { ForumBadgeRef } from '../../app/utils/forum/forumBadge'

/**
 * 佩戴中徽章的两处呈现：
 *
 * 1. 资料页头部昵称行（`name-row`）：本人页在徽章 tab 换过佩戴后立刻跟随本地佩戴态，
 *    他人页只认接口投影的 `topBadge`。页面组件依赖太重（useAsyncData/路由/权限），
 *    这里只锁源契约；渲染行为由 chip 自己的测试与浏览器 QA 覆盖。
 * 2. 用户预览卡：优先评论载荷里的 `topBadge`（与用户点开卡片的那颗 chip 同源），
 *    @提及 没有载荷时回落资料接口；chip 是纯展示，不许在名字链接里再套 `<a>`。
 *
 * 预览卡用真实编译 + 挂载验证，避免「模板写了但 props 没接上」这类只在运行时炸的问题。
 */

const pagePath = fileURLToPath(new URL('../../app/components/profile/SFProfileShowPage.vue', import.meta.url))
const previewPath = fileURLToPath(new URL('../../app/components/forum/SFCommentUserPreview.vue', import.meta.url))
const commentPath = fileURLToPath(new URL('../../app/components/forum/SFComment.vue', import.meta.url))
const identityPath = fileURLToPath(new URL('../../app/components/forum/SFUserIdentityName.vue', import.meta.url))
const cssPath = fileURLToPath(new URL('../../app/assets/css/sforum-profile.css', import.meta.url))

const source = (path: string) => readFileSync(path, 'utf8')

const payloadBadge: ForumBadgeRef = {
  slug: 'payload-badge',
  name: '常客',
  tier: 'bronze',
  icon: 'i-lucide-calendar'
}

const profileBadge: ForumBadgeRef = {
  slug: 'profile-badge',
  name: '资深',
  tier: 'silver',
  icon: 'i-lucide-medal'
}

const wallBadge: ForumBadgeRef = {
  slug: 'wall-badge',
  name: '常客',
  tier: 'bronze',
  icon: 'i-lucide-calendar'
}

function makeProfile(overrides: Partial<PublicProfile> = {}): PublicProfile {
  return {
    userId: 1,
    username: 'alice',
    displayName: 'Alice',
    profile: {
      userId: 1,
      bio: 'bio',
      signature: '',
      location: '',
      websiteUrl: '',
      avatar: { kind: 'initials', url: '', alt: 'Alice' },
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z'
    },
    topicCount: 3,
    commentCount: 2,
    recentTopics: [],
    activities: [],
    joinedAt: '2026-01-01T00:00:00Z',
    ...overrides
  }
}

const NuxtLinkStub = testVue.defineComponent({
  name: 'NuxtLink',
  props: { to: { type: [String, Object], default: '' } },
  template: '<a :data-to="to"><slot /></a>'
})

const AvatarStub = testVue.defineComponent({
  name: 'SFAvatar',
  props: {
    name: { type: String, default: '' },
    avatar: { type: Object, default: null },
    size: { type: String, default: '' }
  },
  template: '<span data-testid="avatar" />'
})

const SkeletonStub = testVue.defineComponent({
  name: 'SFSkeleton',
  props: {
    width: { type: String, default: '' },
    height: { type: String, default: '' },
    lines: { type: Number, default: 0 }
  },
  template: '<span data-testid="skeleton" />'
})

const IconStub = testVue.defineComponent({
  name: 'UIcon',
  props: { name: { type: String, default: '' } },
  template: '<i data-testid="icon" />'
})

/**
 * chip 存根：只暴露收到的 props，不渲染成链接。
 * `username` 用 `default: undefined` 而不是 null —— 测试要区分「没传」与「传了 null」，
 * 传了 username 的 chip 会在名字链接里再套一个 `<a>`，这是预览卡明确禁止的。
 */
const ChipStub = testVue.defineComponent({
  name: 'SFBadgeChip',
  props: {
    badge: { type: Object, required: true },
    username: { type: [String, Object], default: undefined }
  },
  template: '<i data-testid="chip" :data-slug="badge.slug" />'
})

const SFUserIdentityName = await compileVueSfc(identityPath, 'sf-user-identity-name-preview-badge', {
  computed: testVue.computed,
  userDisplayName,
  shouldShowUserHandle
})

/** 每个用例一份独立的 `useState` 存储：预览卡的资料缓存不能跨用例泄漏。 */
function makeUseState() {
  const store = new Map<string, { value: unknown }>()
  return (key: string, init: () => unknown) => {
    if (!store.has(key)) store.set(key, testVue.ref(init()) as { value: unknown })
    return store.get(key)!
  }
}

async function compilePreview(profile: PublicProfile | null) {
  return compileVueSfc(previewPath, 'sf-comment-user-preview-badge', {
    // SFC 走 Nuxt 自动导入：ref/computed/onMounted 在这里必须显式注入。
    ref: testVue.ref,
    computed: testVue.computed,
    onMounted: testVue.onMounted,
    useState: makeUseState(),
    useI18n: () => ({ t: (key: string) => key, locale: testVue.ref('zh-CN') }),
    useProfileApi: () => ({
      getPublicProfile: async () => {
        if (!profile) throw new Error('profile unavailable')
        return profile
      }
    }),
    apiErrorStatusCode: () => 404,
    userDisplayName,
    SFUserIdentityName,
    SFBadgeChip: ChipStub
  })
}

function mountPreview(component: Component, props: Record<string, unknown>) {
  return mount(component, {
    props,
    global: {
      components: {
        SFAvatar: AvatarStub,
        SFSkeleton: SkeletonStub,
        UIcon: IconStub,
        NuxtLink: NuxtLinkStub
      }
    }
  })
}

describe('SFCommentUserPreview badge', () => {
  test('renders the comment payload badge first and keeps the chip link-free', async () => {
    const Preview = await compilePreview(makeProfile({ topBadge: profileBadge }))
    const wrapper = mountPreview(Preview, {
      user: { username: 'alice', displayName: 'Alice' },
      profilePath: '/u/alice',
      topBadge: payloadBadge
    })
    try {
      // 载荷优先：资料接口给的是 profile-badge，但评论载荷才是用户刚看到的那枚。
      expect(wrapper.get('[data-testid="chip"]').attributes('data-slug')).toBe('payload-badge')
      expect(wrapper.getComponent(ChipStub).props('badge')).toMatchObject({ slug: 'payload-badge' })
      // 不传 username：名字本身已是链接，chip 不许再造一个嵌套 `<a>`。
      expect(wrapper.getComponent(ChipStub).props('username')).toBeUndefined()
      expect(wrapper.get('a [data-testid="chip"]').exists()).toBe(true)
      expect(wrapper.findAll('a')).toHaveLength(2)

      await flushPromises()
    } finally {
      wrapper.unmount()
    }
  })

  test('falls back to the profile payload when the comment carries no badge', async () => {
    const Preview = await compilePreview(makeProfile({ topBadge: profileBadge }))
    const wrapper = mountPreview(Preview, {
      user: { username: 'alice', displayName: 'Alice' },
      profilePath: '/u/alice'
    })
    try {
      // 资料还没回来时没有可展示的徽章：先不渲染空壳 chip。
      expect(wrapper.find('[data-testid="chip"]').exists()).toBe(false)

      await flushPromises()
      await testVue.nextTick()

      expect(wrapper.get('[data-testid="chip"]').attributes('data-slug')).toBe('profile-badge')
      expect(wrapper.getComponent(ChipStub).props('username')).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })

  test('renders no chip when neither the comment nor the profile has one', async () => {
    const Preview = await compilePreview(makeProfile())
    const wrapper = mountPreview(Preview, {
      user: { username: 'alice', displayName: 'Alice' },
      profilePath: '/u/alice'
    })
    try {
      await flushPromises()
      await testVue.nextTick()

      expect(wrapper.find('[data-testid="chip"]').exists()).toBe(false)
      // 身份行仍在：chip 缺失只影响徽章，不影响名字。
      expect(wrapper.get('.sf-user-identity-name__name').text()).toBe('Alice')
    } finally {
      wrapper.unmount()
    }
  })
})

/**
 * 头部徽章优先级表。每一行对应 `resolveHeaderBadge` 的一条规则；默认输入是
 * 「本人页 + 本地态已加载且归属当前账号 + 佩戴的 slug 在徽章墙里」，
 * 也就是唯一会显示本地佩戴徽章的那一档。
 */
const headerBase: HeaderBadgeInput = {
  isSelf: true,
  viewerId: 1,
  wornOwnerId: 1,
  wornLoaded: true,
  wornHidden: false,
  wornSlug: 'wall-badge',
  wallBadges: [wallBadge],
  fallback: profileBadge
}

const headerBadgeCases: Array<{
  name: string
  input: Partial<HeaderBadgeInput>
  expected: ForumBadgeRef | null
}> = [
  { name: '他人页只认接口字段，绝不借用本地佩戴态', input: { isSelf: false }, expected: profileBadge },
  { name: '本地佩戴态还没加载过时回落接口字段', input: { wornLoaded: false }, expected: profileBadge },
  { name: '本地态属于上一个账号时回落接口字段', input: { wornOwnerId: 2 }, expected: profileBadge },
  // hidden 只对「归属匹配的本账号」有效：上一个账号的 hidden 不能把新账号的头部抹掉。
  {
    name: '上一个账号的 hidden 在归属不匹配时不生效，仍回落接口字段',
    input: { wornOwnerId: 2, wornHidden: true, wornSlug: '' },
    expected: profileBadge
  },
  { name: '本人选择了不佩戴时头部立刻消失', input: { wornHidden: true, wornSlug: '' }, expected: null },
  { name: '自动态但服务端没给徽章（slug 为空）时回落接口字段', input: { wornSlug: '' }, expected: profileBadge },
  { name: '佩戴的 slug 命中徽章墙时用墙上的条目', input: {}, expected: wallBadge },
  { name: '佩戴的 slug 已不在墙上（停用/删除）时回落接口字段', input: { wornSlug: 'retired' }, expected: profileBadge },
  { name: '没有可回落的接口字段时整枚 chip 不渲染', input: { wornSlug: 'retired', fallback: null }, expected: null }
]

describe('resolveHeaderBadge', () => {
  for (const entry of headerBadgeCases) {
    test(entry.name, () => {
      expect(resolveHeaderBadge({ ...headerBase, ...entry.input })).toEqual(entry.expected)
    })
  }
})

describe('profile header badge source contract', () => {
  test('the nickname row mounts the chip with the profile username', () => {
    const page = source(pagePath)

    expect(page).toContain('<div class="sf-profile-intro__name-row">')
    expect(page).toContain('<h1 id="profile-name">{{ displayName }}</h1>')
    expect(page).toContain(':badge="headerBadge"')
    // 页面头部是资料页自己的身份行：chip 可以指向本页徽章 tab。
    expect(page).toContain(':username="profile.username"')
    expect(page).toContain('const headerBadge = computed')
    // 优先级不在页面里手写：页面只把来源凑齐，裁决交给纯函数（下面表驱动覆盖）。
    expect(page).toContain('resolveHeaderBadge({')
  })

  test('the name row is a wrapping flex row so the chip never adds header height', () => {
    const css = source(cssPath)

    expect(css).toContain('.sf-profile-intro__name-row {')
    expect(css).toMatch(/\.sf-profile-intro__name-row \{[^}]*display: flex/)
    expect(css).toMatch(/\.sf-profile-intro__name-row \{[^}]*flex-wrap: wrap/)
    expect(css).toContain('.sf-profile-intro__name-row > h1 {')
  })

  test('the comment passes its payload badge into the preview card', () => {
    expect(source(commentPath)).toContain(':top-badge="comment?.topBadge || null"')
  })
})
