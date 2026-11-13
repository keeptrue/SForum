import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount, testVue } from '../helpers/vueSfc'
import { shouldShowUserHandle, userDisplayName } from '../../app/utils/forum/userDisplay'
import type { PublicProfile } from '../../app/composables/profile/useProfileApi'

/**
 * 主页身份行不重复 @username。
 *
 * 后端在用户留空昵称时把 username 回填成 `displayName`，用户也可能把昵称设成与
 * username 完全相同的字符串——两种情况下「大佬 / @zhuchunshu」都退化成同一个词写两遍。
 * 判定收在 `shouldShowUserHandle`，主页头部、右栏、SFUserIdentityName 三处共用；
 * 本文件锁的是：helper 三态、两处主页组件的渲染结果、以及身份组件原有行为不变。
 */

const pagePath = fileURLToPath(new URL('../../app/components/profile/SFProfileShowPage.vue', import.meta.url))
const railPath = fileURLToPath(new URL('../../app/components/profile/SFProfileRightRail.vue', import.meta.url))
const identityPath = fileURLToPath(new URL('../../app/components/forum/SFUserIdentityName.vue', import.meta.url))
const source = (path: string) => readFileSync(path, 'utf8')

describe('shouldShowUserHandle', () => {
  test('hides the handle when the resolved name already is the username (nickname left empty)', () => {
    // 空昵称：userDisplayName 回落到 username，两者同形。
    const user = { displayName: '', username: 'zhuchunshu' }
    expect(userDisplayName(user)).toBe('zhuchunshu')
    expect(shouldShowUserHandle(userDisplayName(user), user.username)).toBe(false)
  })

  test('hides the handle when the nickname is literally the username, whitespace included', () => {
    expect(shouldShowUserHandle('zhuchunshu', 'zhuchunshu')).toBe(false)
    // 两端空白必须被吃掉再比较，否则「 zhuchunshu 」这类输入会被误判成不同的名字。
    expect(shouldShowUserHandle(' zhuchunshu ', 'zhuchunshu')).toBe(false)
    expect(shouldShowUserHandle('zhuchunshu', '  zhuchunshu  ')).toBe(false)
  })

  test('shows the handle when the nickname differs from the username', () => {
    expect(shouldShowUserHandle('大佬', 'zhuchunshu')).toBe(true)
    // 大小写不同就是不同的字符串：username 唯一性由 username_lower 负责，这里只比展示。
    expect(shouldShowUserHandle('Zhuchunshu', 'zhuchunshu')).toBe(true)
  })

  test('treats a missing or blank username as "no handle to show"', () => {
    expect(shouldShowUserHandle('大佬', '')).toBe(false)
    expect(shouldShowUserHandle('大佬', '   ')).toBe(false)
    expect(shouldShowUserHandle('大佬', null)).toBe(false)
    expect(shouldShowUserHandle('大佬', undefined)).toBe(false)
  })

  test('treats a missing display name as blank instead of throwing', () => {
    expect(shouldShowUserHandle('', 'zhuchunshu')).toBe(true)
    expect(shouldShowUserHandle(null, 'zhuchunshu')).toBe(true)
    expect(shouldShowUserHandle(undefined, 'zhuchunshu')).toBe(true)
    // 两边都空：没有 handle 可展示。
    expect(shouldShowUserHandle('', '')).toBe(false)
  })
})

const NullRender = testVue.defineComponent({ name: 'NullRender', render: () => null })

const NuxtLinkStub = testVue.defineComponent({
  name: 'NuxtLink',
  props: { to: { type: [String, Object], default: '' } },
  template: '<a :data-to="to"><slot /></a>'
})

const railBindings = {
  computed: testVue.computed,
  useI18n: () => ({ t: (key: string) => key, locale: testVue.ref('zh-CN') }),
  useLocalePath: () => (path: string) => path,
  useSiteDateTime: () => ({ formatDateOnly: (value: string) => value }),
  useWebOptions: () => ({ seoSettings: testVue.ref({ topicUrlMode: 'id_slug' }) }),
  forumTopicPath: () => '/',
  extensionTabLabel: () => '',
  safeUrl: (value: string) => value,
  shouldShowUserHandle,
  // 本用例不给 trustLevel，卡片不会真的渲染；只需要名字可解析。
  SFTrustLevelCard: NullRender
}

const ProfileRightRail = await compileVueSfc(railPath, 'profile-right-rail-handle', railBindings)

const railGlobal = { components: { SFAvatar: NullRender, UIcon: NullRender, NuxtLink: NuxtLinkStub } }

function makeProfile(overrides: Partial<PublicProfile> = {}): PublicProfile {
  return {
    userId: 1,
    username: 'zhuchunshu',
    displayName: 'zhuchunshu',
    profile: {
      userId: 1,
      bio: '',
      signature: '',
      location: '',
      websiteUrl: '',
      avatar: { kind: 'initials', url: '', alt: 'zhuchunshu' },
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

/** 右栏身份小字的原始 textContent：`text()` 会 trim，会把「前导空格」这类回归吃掉。 */
function mountRailIdentity(profile: PublicProfile, displayName: string) {
  const wrapper = mount(ProfileRightRail, {
    props: {
      profile,
      displayName,
      hasPublicDetails: false,
      recentTopics: []
    },
    global: railGlobal
  })
  const meta = wrapper.get('.sf-profile-side-identity__main small')
  return { wrapper, text: meta.element.textContent ?? '' }
}

describe('SFProfileRightRail identity line', () => {
  test('renders only `UID n` when the nickname equals the username (no leading separator)', () => {
    const profile = makeProfile()
    // 与页面同口径：displayName 是 userDisplayName 解析后的结果。
    const displayName = userDisplayName(profile, profile.username)
    const { wrapper, text } = mountRailIdentity(profile, displayName)
    try {
      expect(text).toBe('UID 1')
      expect(text).not.toContain('·')
      expect(text).not.toContain('@')
      expect(text).not.toContain('  ')
      // UID 是唯一账号标识，必须始终保留。
      expect(wrapper.find('.sf-profile-side-identity__main small').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('keeps `@handle · UID n` when the nickname differs from the username', () => {
    const profile = makeProfile({ displayName: '大佬' })
    const displayName = userDisplayName(profile, profile.username)
    const { wrapper, text } = mountRailIdentity(profile, displayName)
    try {
      expect(text).toBe('@zhuchunshu · UID 1')
    } finally {
      wrapper.unmount()
    }
  })

  test('trims a padded username instead of rendering doubled spaces', () => {
    const profile = makeProfile({ username: '  zhuchunshu  ', displayName: '大佬' })
    const { wrapper, text } = mountRailIdentity(profile, '大佬')
    try {
      expect(text).toBe('@zhuchunshu · UID 1')
    } finally {
      wrapper.unmount()
    }
  })
})

const identityBindings = {
  computed: testVue.computed,
  userDisplayName,
  shouldShowUserHandle
}

const SFUserIdentityName = await compileVueSfc(identityPath, 'sf-user-identity-name-handle', identityBindings)

function mountIdentity(props: Record<string, unknown>, slots: Record<string, string> = {}) {
  return mount(SFUserIdentityName, {
    props,
    slots,
    global: { components: { NuxtLink: NuxtLinkStub } }
  })
}

describe('SFUserIdentityName keeps its existing semantics', () => {
  test('comment/notification contexts still show @handle when the name differs', () => {
    const wrapper = mountIdentity({ user: { displayName: '大佬', username: 'zhuchunshu' }, to: '/u/zhuchunshu' })
    try {
      expect(wrapper.get('.sf-user-identity-name__name').text()).toBe('大佬')
      expect(wrapper.get('.sf-user-identity-name__handle').text()).toBe('@zhuchunshu')
      // 名字与 handle 同指资料页（原有链接语义不变）。
      expect(wrapper.get('a').attributes('data-to')).toBe('/u/zhuchunshu')
    } finally {
      wrapper.unmount()
    }
  })

  test('still hides the handle line when the name already is the username', () => {
    const blankNickname = mountIdentity({ user: { displayName: '', username: 'zhuchunshu' }, to: '/u/zhuchunshu' })
    try {
      expect(blankNickname.get('.sf-user-identity-name__name').text()).toBe('zhuchunshu')
      expect(blankNickname.find('.sf-user-identity-name__handle').exists()).toBe(false)
    } finally {
      blankNickname.unmount()
    }

    const sameName = mountIdentity({ user: { displayName: 'zhuchunshu', username: 'zhuchunshu' } })
    try {
      expect(sameName.find('.sf-user-identity-name__handle').exists()).toBe(false)
    } finally {
      sameName.unmount()
    }
  })

  test('still honours showHandle=false, the caller fallback, and the name-suffix slot', () => {
    const noHandle = mountIdentity({ user: { displayName: '大佬', username: 'zhuchunshu' }, showHandle: false })
    try {
      expect(noHandle.find('.sf-user-identity-name__handle').exists()).toBe(false)
    } finally {
      noHandle.unmount()
    }

    const fallback = mountIdentity({ user: null, fallback: '#42' })
    try {
      expect(fallback.get('.sf-user-identity-name__name').text()).toBe('#42')
      expect(fallback.find('.sf-user-identity-name__handle').exists()).toBe(false)
    } finally {
      fallback.unmount()
    }

    const withSuffix = mountIdentity(
      { user: { displayName: '大佬', username: 'zhuchunshu' } },
      { 'name-suffix': '<i data-testid="op">楼主</i>' }
    )
    try {
      expect(withSuffix.get('.sf-user-identity-name__name-line').find('[data-testid="op"]').exists()).toBe(true)
      expect(withSuffix.get('.sf-user-identity-name__handle').text()).toBe('@zhuchunshu')
    } finally {
      withSuffix.unmount()
    }
  })
})

describe('profile page header contract', () => {
  test('the @handle span is conditional while UID stays unconditional', () => {
    const page = source(pagePath)
    // 只有 handle 挂 v-if；UID 不能跟着消失。
    expect(page).toMatch(/<span v-if="showHandle" class="sf-profile-handle">@\{\{ profile\.username \}\}<\/span>/)
    expect(page).toMatch(/<span class="sf-profile-uid">UID \{\{ profile\.userId \}\}<\/span>/)
    // 无条件渲染的 handle span 不许回来。
    expect(page).not.toMatch(/<span class="sf-profile-handle"/)
    expect(page).not.toMatch(/<span v-if="[^"]*" class="sf-profile-uid"/)
  })

  test('the decision compares the resolved displayName, not the raw profile.displayName', () => {
    const page = source(pagePath)
    expect(page).toContain('shouldShowUserHandle')
    expect(page).toContain('const displayName = computed(() => userDisplayName(profile.value, username.value))')
    expect(page).toContain('shouldShowUserHandle(displayName.value, profile.value?.username)')
    // 拿原值比较会漏掉「昵称留空 → 后端回填 username」这一路。
    expect(page).not.toContain('shouldShowUserHandle(profile.value?.displayName')
  })

  test('the right rail shares the same helper and has no hardcoded separator text', () => {
    const rail = source(railPath)
    expect(rail).toContain("import { shouldShowUserHandle } from '~/utils/forum/userDisplay'")
    expect(rail).toContain('shouldShowUserHandle(props.displayName, props.profile.username)')
    // 旧的写死模板会残留前导 `·`。
    expect(rail).not.toContain('@{{ profile.username }} · UID')
  })

  test('the handle line sits below the nickname instead of sharing the h1 row', () => {
    const page = source(pagePath)
    // 身份块是「昵称一行 + (handle, UID) 一行」：h1 直接领起第二行容器。
    expect(page).toContain('class="sf-profile-intro__identity-meta"')
    expect(page).toMatch(/<h1 id="profile-name">\{\{ displayName \}\}<\/h1>\s*<div class="sf-profile-intro__identity-meta">/)
  })
})
