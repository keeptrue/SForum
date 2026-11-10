import { describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { computed, defineComponent, h, ref } from 'vue'
import { compileVueSfc, mount } from '../helpers/vueSfc'
import {
  bookmarkEmptyStateCopy,
  bookmarkRemindStatusKey,
  bookmarkTargetTypeIcon,
  bookmarkTargetTypeLabelKey,
  formatBookmarkRemindAt
} from '~/utils/forum/forumBookmark'
import { forumTopicPath } from '~/utils/forum/forumTaxonomy'

/**
 * 收藏 tab 的渲染契约：类型徽标、私密锁标记、深链、空状态 CTA。
 *
 * 浏览器 QA 只能看到匿名视角（他人只看公开行），本人视角的锁标记与文案由这里证明。
 */
const tabPath = fileURLToPath(new URL('../../app/components/profile/SFProfileBookmarkTab.vue', import.meta.url))

const messages: Record<string, string> = {
  'bookmark.profileTab.title': '收藏',
  'bookmark.profileTab.description': '按收藏时间排序的主题与评论。',
  'bookmark.profileTab.privateNotice': '只有你自己能看到私密收藏。',
  'bookmark.profileTab.loadMore': '加载更多收藏',
  'bookmark.profileTab.loadingMore': '正在加载…',
  'bookmark.profileTab.loadFailed': '收藏列表加载失败',
  'bookmark.empty.selfTitle': '还没有收藏',
  'bookmark.empty.selfDescription': '在主题或评论上点收藏。',
  'bookmark.empty.publicTitle': '还没有公开收藏',
  'bookmark.empty.publicDescription': '这位成员还没有公开分享任何收藏。',
  'bookmark.empty.cta': '去首页看看',
  'bookmark.remind.status.pending': '将提醒',
  'bookmark.remind.status.done': '已提醒',
  'bookmark.target.topic': '主题',
  'bookmark.target.comment': '评论',
  'bookmark.visibility.private': '私密',
  'common.retry': '重试'
}

const t = (key: string) => messages[key] ?? key

const NuxtLinkStub = defineComponent({
  props: { to: { type: [String, Object], default: '' } },
  setup(props, { slots }) {
    return () => h('a', { href: typeof props.to === 'string' ? props.to : '' }, slots.default ? slots.default() : [])
  }
})

const nullRender = defineComponent({ render: () => null })
const SkeletonStub = defineComponent({
  props: { lines: { type: Number, default: 3 } },
  render: () => h('div', { 'data-testid': 'skeleton' })
})

const BookmarkTab = await compileVueSfc(tabPath, 'profile-bookmark-tab', {
  computed,
  ref,
  useI18n: () => ({ t, locale: ref('zh-CN') }),
  useLocalePath: () => (path: string) => path,
  useWebOptions: () => ({ seoSettings: ref({ topicUrlMode: 'id_slug' }) }),
  useSiteDateTime: () => ({
    format: (value: string) => value,
    settings: ref({ timezone: 'UTC', dateFormat: 'Y-m-d', timeFormat: 'H:i', startOfWeek: 1 })
  }),
  bookmarkEmptyStateCopy,
  bookmarkRemindStatusKey,
  bookmarkTargetTypeIcon,
  bookmarkTargetTypeLabelKey,
  formatBookmarkRemindAt,
  forumTopicPath
})

const topicEntry = {
  targetType: 'topic' as const,
  targetId: 12,
  visibility: 'private' as const,
  createdAt: '2026-01-02T00:00:00.000Z',
  topic: { id: 12, slug: 'slug-12', title: '被收藏的主题', categoryName: '站点公告', excerpt: '主题摘要' }
}

// 后端 BookmarkCommentRef 只有 id/topicId：没有标题与摘要，渲染必须不依赖它们。
const commentEntry = {
  targetType: 'comment' as const,
  targetId: 56,
  visibility: 'public' as const,
  createdAt: '2026-01-03T00:00:00.000Z',
  comment: { id: 56, topicId: 34 }
}

function mountTab(props: Record<string, unknown>) {
  return mount(BookmarkTab, {
    props: {
      items: [],
      self: false,
      loading: false,
      loadingMore: false,
      hasMore: false,
      failed: false,
      ...props
    },
    global: { components: { NuxtLink: NuxtLinkStub, UIcon: nullRender, SFSkeleton: SkeletonStub } }
  })
}

describe('profile bookmark tab rendering', () => {
  test('renders type badges, titles and deep links for both target kinds', () => {
    const wrapper = mountTab({ items: [topicEntry, commentEntry], self: true })
    try {
      const rows = wrapper.findAll('.sf-profile-bookmark')
      expect(rows).toHaveLength(2)

      const badges = wrapper.findAll('.sf-profile-bookmark__badge').map(node => (node.text() || '').trim())
      expect(badges).toEqual(['主题', '评论'])

      // 评论行没有主题 slug，固定走 id 形态深链；主题行沿用站点 URL 形态。
      const links = wrapper.findAll('a.sf-profile-bookmark__overlay').map(node => node.attributes('href'))
      expect(links).toEqual(['/t/12/slug-12', '/t/34#comment-56'])

      expect(wrapper.text()).toContain('被收藏的主题')
      expect(wrapper.text()).toContain('#56')
    } finally {
      wrapper.unmount()
    }
  })

  test('marks private rows with a lock and leaves public rows unmarked', () => {
    const wrapper = mountTab({ items: [topicEntry, commentEntry], self: true })
    try {
      const rows = wrapper.findAll('.sf-profile-bookmark')
      expect(rows[0]?.find('.sf-profile-bookmark__lock').exists()).toBe(true)
      expect(rows[1]?.find('.sf-profile-bookmark__lock').exists()).toBe(false)
      // 私密提示只在本人视角出现
      expect(wrapper.find('.sf-profile-bookmarks__notice').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('the public view hides the private notice and never claims ownership', () => {
    const wrapper = mountTab({ items: [commentEntry], self: false })
    try {
      expect(wrapper.find('.sf-profile-bookmarks__notice').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('empty self view offers the home CTA, empty public view does not', () => {
    const selfWrapper = mountTab({ items: [], self: true })
    try {
      expect(selfWrapper.text()).toContain('还没有收藏')
      const cta = selfWrapper.findAll('a').find(node => (node.text() || '').includes('去首页看看'))
      expect(cta?.attributes('href')).toBe('/')
    } finally {
      selfWrapper.unmount()
    }

    const publicWrapper = mountTab({ items: [], self: false })
    try {
      expect(publicWrapper.text()).toContain('还没有公开收藏')
      expect(publicWrapper.text()).not.toContain('去首页看看')
    } finally {
      publicWrapper.unmount()
    }
  })

  test('a failed first load shows the retry action instead of the empty state', () => {
    const wrapper = mountTab({ items: [], self: true, failed: true })
    try {
      expect(wrapper.text()).toContain('收藏列表加载失败')
      expect(wrapper.text()).toContain('重试')
      expect(wrapper.text()).not.toContain('还没有收藏')
    } finally {
      wrapper.unmount()
    }
  })

  test('load-more only renders when the page reports more rows', () => {
    const withoutMore = mountTab({ items: [topicEntry], self: true })
    try {
      expect(withoutMore.find('.sf-profile-load-more').exists()).toBe(false)
    } finally {
      withoutMore.unmount()
    }

    const withMore = mountTab({ items: [topicEntry], self: true, hasMore: true })
    try {
      expect(withMore.find('.sf-profile-load-more').exists()).toBe(true)
    } finally {
      withMore.unmount()
    }
  })
})


describe('profile bookmark tab reminder state', () => {
  test('a scheduled reminder reads 将提醒 and an already-fired one reads 已提醒', () => {
    const scheduled = mountTab({
      self: true,
      items: [{ ...topicEntry, visibility: 'private' as const, remindAt: '2099-01-02T00:00:00.000Z' }]
    })
    try {
      const chip = scheduled.find('.sf-profile-bookmark__remind')
      expect(chip.exists()).toBe(true)
      expect((chip.text() || '').trim()).toBe('将提醒')
      expect(chip.classes()).not.toContain('is-done')
    } finally {
      scheduled.unmount()
    }

    const fired = mountTab({
      self: true,
      items: [{ ...topicEntry, visibility: 'private' as const, remindAt: '2026-01-02T00:00:00.000Z', remindedAt: '2026-01-03T00:00:00.000Z' }]
    })
    try {
      const chip = fired.find('.sf-profile-bookmark__remind')
      expect((chip.text() || '').trim()).toBe('已提醒')
      expect(chip.classes()).toContain('is-done')
    } finally {
      fired.unmount()
    }
  })

  test('the public view never renders a reminder chip, even if the payload leaks remindAt', () => {
    // 后端修好后他人视角不再返回提醒字段；前端这一层是兜底，不能靠服务端单独保证。
    const wrapper = mountTab({
      self: false,
      // 提醒时刻与 createdAt 用不同日期，避免把「收藏时间」误当成泄露。
      items: [{
        ...commentEntry,
        remindAt: '2099-03-03T00:00:00.000Z',
        remindedAt: '2026-02-02T00:00:00.000Z'
      }]
    })
    try {
      expect(wrapper.find('.sf-profile-bookmark__remind').exists()).toBe(false)
      expect(wrapper.text()).not.toContain('将提醒')
      expect(wrapper.text()).not.toContain('已提醒')
      // 悬停也不能泄露精确时刻
      expect(wrapper.html()).not.toContain('2099-03-03')
      expect(wrapper.html()).not.toContain('2026-02-02')
    } finally {
      wrapper.unmount()
    }
  })

  test('rows without any reminder metadata show no chip', () => {
    const wrapper = mountTab({ self: true, items: [topicEntry] })
    try {
      expect(wrapper.find('.sf-profile-bookmark__remind').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})
