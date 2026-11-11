import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { computed, defineComponent, ref } from 'vue'
import { compileVueSfc, mount } from '../helpers/vueSfc'
import {
  TIER_COLOR_TOKENS,
  defaultTierColorName,
  isTierColorToken
} from '~/utils/profile/trustLevelColors'
import { TRUST_LEVEL_SLUGS } from '~/composables/profile/useTrustLevel'

/**
 * 前台信任等级徽章（规格 §9）：形态 / 三档降级 / 三处接入顺序。
 *
 * 最贵的一类 bug 是「同一个作者在列表与详情不一致」，或者「徽章出现让作者名换行」：
 * 前者因为三处各写一套规则，后者因为 TL0 用了更大的盒子。这里逐条钉死。
 */
const messages = JSON.parse(
  readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
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

const i18n = () => ({ t, locale: ref('zh-CN') })

const ChipStub = defineComponent({
  name: 'SFBadgeChip',
  props: { badge: { type: Object, required: true }, username: { type: String, default: null } },
  template: '<i data-testid="chip" />'
})
const AvatarStub = defineComponent({ name: 'SFAvatar', template: '<i data-testid="avatar" />' })
const IconStub = defineComponent({ name: 'UIcon', template: '<i />' })
const NuxtLinkStub = defineComponent({
  name: 'NuxtLink',
  props: { to: { type: [String, Object], default: '' } },
  template: '<a :href="String(to)"><slot /></a>'
})

const badgePath = fileURLToPath(new URL('../../app/components/forum/SFTrustTierBadge.vue', import.meta.url))
const SFTrustTierBadge = await compileVueSfc(badgePath, 'sf-trust-tier-badge', {
  computed,
  useI18n: i18n,
  TRUST_LEVEL_SLUGS,
  TIER_COLOR_TOKENS,
  defaultTierColorName,
  isTierColorToken
})

const authorBlockPath = fileURLToPath(new URL('../../app/components/forum/SFTopicAuthorBlock.vue', import.meta.url))
const SFTopicAuthorBlock = await compileVueSfc(authorBlockPath, 'sf-topic-author-block-tl', {
  computed,
  useI18n: i18n,
  SFBadgeChip: ChipStub,
  SFTrustTierBadge,
  SFAvatar: AvatarStub
})
const commentRowPath = fileURLToPath(new URL('../../app/components/forum/SFCommentAuthorRow.vue', import.meta.url))
const SFCommentAuthorRow = await compileVueSfc(commentRowPath, 'sf-comment-author-row-tl', {
  computed,
  useI18n: i18n,
  SFUserIdentityName: defineComponent({
    name: 'SFUserIdentityName',
    props: { user: { type: Object, default: () => ({}) }, to: { type: String, default: '' }, fallback: { type: String, default: '' } },
    template: '<span data-testid="identity"><slot name="name-suffix" /></span>'
  }),
  SFBadgeChip: ChipStub,
  SFTrustTierBadge
})

/**
 * DOM 顺序（文档序）：按选择器把命中的元素标成标签，返回出现顺序。
 *
 * 不用 `wrapper.html()`：这个测试 DOM 里出现过 `XMLSerializer is not defined`
 * （注释节点 + 该 shim 的组合），而顺序断言本质只需要文档序。
 */
function domOrder(root: Element, selectors: Array<[string, string]>): string[] {
  return [...root.querySelectorAll(selectors.map(([selector]) => selector).join(','))]
    .map((node) => {
      const hit = selectors.find(([selector]) => node.matches(selector))
      return hit ? hit[1] : '?'
    })
}

function mountBadge(props: Record<string, unknown>) {
  return mount(SFTrustTierBadge, { props })
}

describe('SFTrustTierBadge 形态', () => {
  test('renders a dot plus TL{n}, and carries the tier color as a whitelist class (no inline style)', () => {
    const wrapper = mountBadge({ trustLevel: { level: 3, levelSlug: 'member', color: 'sky' } })
    const badge = wrapper.find('[data-sf-trust-tier-badge]')
    expect(badge.exists()).toBe(true)
    expect(badge.element.tagName).toBe('SPAN')
    expect(badge.classes()).toContain('sf-tier-badge--token-sky')
    expect(badge.classes()).toContain('sf-tier-badge--row')
    expect(badge.attributes('data-tier-level')).toBe('3')
    expect(badge.text()).toBe('TL3')
    // 颜色只经冻结白名单映射到 --sf-tl-*：组件不得把服务端字符串拼进 style。
    expect(badge.attributes('style')).toBeUndefined()
    expect(wrapper.find('.sf-tier-badge__dot').exists()).toBe(true)
    // 圆点不朗读，徽章本体读「信任等级 3」。
    expect(wrapper.find('.sf-tier-badge__dot').attributes('aria-hidden')).toBe('true')
    expect(badge.attributes('aria-label')).toBe(t('topicDetail.trustBadge', { level: 3 }))
    expect(badge.attributes('role')).toBe('img')
    // 只读装饰：不可聚焦。
    expect(badge.attributes('tabindex')).toBeUndefined()
  })

  test('two density sizes, both with the same DOM shape', () => {
    const row = mountBadge({ trustLevel: { level: 2, levelSlug: 'member', color: 'emerald' } })
    expect(row.classes()).toContain('sf-tier-badge--row')
    const author = mountBadge({ trustLevel: { level: 2, levelSlug: 'member', color: 'emerald' }, size: 'author' })
    expect(author.classes()).toContain('sf-tier-badge--author')
    expect(author.find('.sf-tier-badge__dot').exists()).toBe(true)
  })

  test('TL0 keeps the same 6px box and draws its ring with an inset shadow (no 8px outer diameter)', () => {
    const wrapper = mountBadge({ trustLevel: { level: 0, levelSlug: 'newcomer', color: 'slate' } })
    const badge = wrapper.find('[data-sf-trust-tier-badge]')
    expect(badge.attributes('data-tier-level')).toBe('0')
    expect(badge.text()).toBe('TL0')
    expect(badge.classes()).toContain('sf-tier-badge--token-slate')
    // 空心环是「数据属性 + 内描边」实现的，盒子尺寸由 CSS 保持 6px（不在模板里加尺寸）。
    expect(wrapper.find('.sf-tier-badge__dot').attributes('style')).toBeUndefined()
    const css = readFileSync(new URL('../../app/components/forum/SFTrustTierBadge.vue', import.meta.url), 'utf8')
    expect(css).toContain('box-shadow: inset 0 0 0 1px currentColor')
    expect(css).toContain('width: 6px')
    // 旧写法（外径 8px 的描边圆）必须彻底退场。
    expect(css).not.toContain('width: 8px')
  })
})

describe('SFTrustTierBadge 降级（规格 §5）', () => {
  test('1) a missing field renders nothing at all', () => {
    expect(mountBadge({ trustLevel: null }).find('[data-sf-trust-tier-badge]').exists()).toBe(false)
    expect(mountBadge({}).find('[data-sf-trust-tier-badge]').exists()).toBe(false)
    // level 缺失也算「读不到」，不留空容器。
    expect(mountBadge({ trustLevel: { levelSlug: 'member', color: 'teal' } }).find('[data-sf-trust-tier-badge]').exists())
      .toBe(false)
  })

  test('2) an unknown slug degrades to plain text without a dot', () => {
    const wrapper = mountBadge({ trustLevel: { level: 4, levelSlug: 'ghost-tier', color: 'indigo' } })
    const badge = wrapper.find('[data-sf-trust-tier-badge]')
    expect(badge.text()).toBe('TL4')
    expect(badge.classes()).toContain('sf-tier-badge--unknown')
    expect(badge.classes()).not.toContain('sf-tier-badge--token-indigo')
    expect(wrapper.find('.sf-tier-badge__dot').exists()).toBe(false)
  })

  test('3) an unknown color falls back to the level default token', () => {
    const wrapper = mountBadge({ trustLevel: { level: 6, levelSlug: 'tier_6', color: 'chartreuse' } })
    expect(wrapper.classes()).toContain(`sf-tier-badge--token-${defaultTierColorName(6)}`)
    // 没给 color 也走同一条兜底（level 序号本身是语义）。
    expect(mountBadge({ trustLevel: { level: 8, levelSlug: 'tier_8' } }).classes())
      .toContain('sf-tier-badge--token-amber')
  })

  test('4) out-of-range levels are hidden instead of wrapping around', () => {
    for (const level of [9, 12, -1, 1.5]) {
      expect(mountBadge({ trustLevel: { level, levelSlug: 'member', color: 'teal' } })
        .find('[data-sf-trust-tier-badge]').exists()).toBe(false)
    }
  })
})

describe('三处接入（顺序与一致性）', () => {
  test('详情作者块：昵称 → TL → 奖励 chip（chip 位置不变）', () => {
    const wrapper = mount(SFTopicAuthorBlock, {
      props: {
        author: { username: 'alice', displayName: 'Alice' },
        name: 'Alice',
        to: '/u/alice',
        topBadge: { id: 'b1', label: 'Veteran' },
        trustLevel: { level: 5, levelSlug: 'veteran', color: 'violet' }
      },
      global: { components: { NuxtLink: NuxtLinkStub } }
    })
    expect(domOrder(wrapper.element, [
      ['.sf-topic-author__name', 'name'],
      ['[data-sf-trust-tier-badge]', 'tl'],
      ['[data-testid="chip"]', 'chip']
    ])).toEqual(['name', 'tl', 'chip'])
    // 同一档位在详情用 author 尺寸。
    expect(wrapper.find('[data-sf-trust-tier-badge]').classes()).toContain('sf-tier-badge--author')
  })

  test('评论作者行：昵称 → 楼主标 → TL → 奖励 chip', () => {
    const wrapper = mount(SFCommentAuthorRow, {
      props: {
        author: 'Alice',
        authorLink: '/u/alice',
        username: 'alice',
        displayName: 'Alice',
        isOpAuthor: true,
        interactive: true,
        topBadge: { id: 'b1', label: 'Veteran' },
        trustLevel: { level: 2, levelSlug: 'member', color: 'emerald' }
      },
      global: { components: { NuxtLink: NuxtLinkStub } }
    })
    expect(domOrder(wrapper.element, [
      ['[data-testid="identity"]', 'identity'],
      ['.sf-comment__op-badge', 'op'],
      ['[data-sf-trust-tier-badge]', 'tl'],
      ['[data-testid="chip"]', 'chip']
    ])).toEqual(['identity', 'op', 'tl', 'chip'])
    // 评论是 row 尺寸，与列表同一档。
    expect(wrapper.find('[data-sf-trust-tier-badge]').classes()).toContain('sf-tier-badge--row')
  })

  test('列表行：作者名之后插入 TL，行容器类名不变（不引起行高变化）', async () => {
    const homeRowPath = fileURLToPath(new URL('../../app/components/forum/SFHomeTopicRow.vue', import.meta.url))
    const SFHomeTopicRow = await compileVueSfc(homeRowPath, 'sf-home-topic-row-tl', {
      computed,
      useI18n: i18n,
      useLocalePath: () => (path: string) => path,
      forumAuthorName: (author: { displayName?: string } | undefined, fallback: number) => author?.displayName || `user${fallback}`,
      forumTagPath: (slug: string) => `/t/${slug}`,
      forumTopicExtensionLabel: () => '',
      forumCategoryChipToneClass: () => '',
      forumListBadgeToneClass: () => '',
      SFAvatar: AvatarStub,
      SFTrustTierBadge
    })
    const topic = {
      id: 7,
      categoryId: 1,
      categorySlug: 'general',
      categoryName: 'General',
      authorUserId: 42,
      author: { username: 'alice', displayName: 'Alice' },
      authorTrustLevel: { level: 3, levelSlug: 'member', color: 'sky' },
      title: 'A topic',
      slug: 'a-topic',
      status: 'published',
      isPinned: false,
      commentCount: 2,
      viewCount: 10,
      likeCount: 0,
      tags: [],
      excerpt: 'body',
      createdAt: '2026-11-11T00:00:00Z',
      updatedAt: '2026-11-11T00:00:00Z',
      lastActivityAt: '2026-11-11T00:00:00Z',
      currentRevision: 1
    }
    const wrapper = mount(SFHomeTopicRow, {
      props: { topic, to: '/t/a-topic', createdLabel: '刚刚', activityLabel: '刚刚' },
      global: { components: { NuxtLink: NuxtLinkStub, UIcon: IconStub } }
    })
    // 行内有两处同构容器（作者行与「最近回复」行），只看前两位：作者行先出现、TL 紧随其后。
    expect(domOrder(wrapper.element, [
      ['.sf-home-topic-row__meta .inline-flex.min-w-0.items-center', 'author-line'],
      ['[data-sf-trust-tier-badge]', 'tl']
    ]).slice(0, 2)).toEqual(['author-line', 'tl'])
    // 徽章是行内 span，且塞在作者名所在的那个行内容器里：不新增行、不加外边距类。
    const badge = wrapper.find('[data-sf-trust-tier-badge]')
    expect(badge.element.tagName).toBe('SPAN')
    expect(badge.classes()).toContain('sf-tier-badge--row')
    const authorLine = wrapper.find('.sf-home-topic-row__meta .inline-flex.min-w-0.items-center')
    expect(authorLine.exists()).toBe(true)
    expect(authorLine.html()).toContain('data-sf-trust-tier-badge')
    // 行根类名逐字未变（徽章不得改动既有布局契约）。
    expect(wrapper.find('[data-sf-component="forum.topic_list_row"]').classes()).toContain('sf-home-topic-row')
    expect(wrapper.find('[data-sf-component="forum.topic_list_row"]').classes()).not.toContain('has-trust-badge')
  })

  test('字段缺席时三处都不留占位', () => {
    const authorBlock = mount(SFTopicAuthorBlock, {
      props: { author: { username: 'alice' }, name: 'Alice', topBadge: null, trustLevel: null },
      global: { components: { NuxtLink: NuxtLinkStub } }
    })
    expect(authorBlock.find('[data-sf-trust-tier-badge]').exists()).toBe(false)

    const commentRow = mount(SFCommentAuthorRow, {
      props: { author: 'Alice', username: 'alice', interactive: true, trustLevel: null },
      global: { components: { NuxtLink: NuxtLinkStub } }
    })
    expect(commentRow.find('[data-sf-trust-tier-badge]').exists()).toBe(false)
  })
})
