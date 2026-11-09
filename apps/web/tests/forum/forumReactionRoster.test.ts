import { describe, expect, test } from 'bun:test'
import { computed, ref, watch } from 'vue'
import { fileURLToPath } from 'node:url'
import {
  forumReactionRosterCanLoadMore,
  forumReactionRosterMerge,
  forumReactionRosterTotal
} from '~/utils/forum/forumReaction'
import { compileVueSfc, flushPromises, mount } from '../helpers/vueSfc'

const rosterPath = fileURLToPath(new URL('../../app/components/forum/SFReactionRoster.vue', import.meta.url))

type Page = { items: Array<Record<string, unknown>>, total: number, hasMore: boolean }

const calls: Array<{ kind: string, id: number, limit: number, offset: number }> = []
const selfUser = ref<{ id: number, username: string, displayName: string, avatar: unknown } | null>(null)

let respond: (offset: number) => Promise<Page> = async () => ({ items: [], total: 0, hasMore: false })

function user(id: number, name = `用户${id}`) {
  return { id, username: `u${id}`, displayName: name, avatar: { kind: 'initials', url: '', alt: '' } }
}

const Roster = await compileVueSfc(rosterPath, 'reaction-roster', {
  computed,
  ref,
  watch,
  forumReactionRosterCanLoadMore,
  forumReactionRosterMerge,
  forumReactionRosterTotal,
  useI18n: () => ({
    t: (key: string, params?: { count?: number }) => (params?.count != null ? `${key}:${params.count}` : key)
  }),
  useLocalePath: () => (target: unknown) => target,
  useAuthSession: () => ({ user: selfUser }),
  useForumApi: () => ({
    listTopicLikes: async (id: number, limit: number, offset: number) => {
      calls.push({ kind: 'topic', id, limit, offset })
      return respond(offset)
    },
    listCommentLikes: async (id: number, limit: number, offset: number) => {
      calls.push({ kind: 'comment', id, limit, offset })
      return respond(offset)
    }
  })
})

function mountRoster(props: Record<string, unknown> = {}) {
  return mount(Roster, {
    props: { targetType: 'topic', targetId: 7, active: false, count: 5, ...props },
    global: {
      stubs: {
        NuxtLink: { template: '<a><slot /></a>' },
        SFAvatar: true,
        UIcon: true
      }
    }
  })
}

describe('reaction roster pagination', () => {
  test('stays quiet until the popover opens, then loads the first page', async () => {
    calls.length = 0
    selfUser.value = null
    respond = async () => ({ items: [user(3, '种子用户2'), user(9, '阿岚')], total: 2, hasMore: false })
    const wrapper = mountRoster()
    try {
      expect(calls).toEqual([])
      await wrapper.setProps({ active: true })
      await flushPromises()
      expect(calls).toEqual([{ kind: 'topic', id: 7, limit: 20, offset: 0 }])
      const text = wrapper.text()
      expect(text).toContain('种子用户2')
      expect(text).toContain('阿岚')
      expect(text).toContain('forum.reaction.rosterTotal:2')
    } finally {
      wrapper.unmount()
    }
  })

  test('load more appends the next page and dedupes overlapping ids', async () => {
    calls.length = 0
    respond = async (offset) => {
      if (offset === 0) {
        return { items: Array.from({ length: 20 }, (_, index) => user(index + 1)), total: 22, hasMore: true }
      }
      // 第二页故意重复第 20 号：合并必须去重
      return { items: [user(20), user(21), user(22)], total: 22, hasMore: false }
    }
    const wrapper = mountRoster({ active: true })
    try {
      await flushPromises()
      expect(wrapper.findAll('.sf-roster__item')).toHaveLength(20)

      await wrapper.get('.sf-roster__more').trigger('click')
      await flushPromises()
      expect(calls).toEqual([
        { kind: 'topic', id: 7, limit: 20, offset: 0 },
        { kind: 'topic', id: 7, limit: 20, offset: 20 }
      ])
      expect(wrapper.findAll('.sf-roster__item')).toHaveLength(22)
      // hasMore=false 之后不再渲染「加载更多」
      expect(wrapper.find('.sf-roster__more').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('keeps the load-more button mounted while the next page is in flight', async () => {
    calls.length = 0
    let releasePage: ((page: Page) => void) | null = null
    respond = async (offset) => {
      if (offset === 0) {
        return { items: Array.from({ length: 20 }, (_, index) => user(index + 1)), total: 40, hasMore: true }
      }
      // 第二页挂起：模拟加载中的窗口期
      return await new Promise<Page>((resolve) => { releasePage = resolve })
    }
    const wrapper = mountRoster({ active: true })
    try {
      await flushPromises()
      const before = wrapper.get('.sf-roster__more')
      expect(before.attributes('aria-busy')).toBeUndefined()
      expect(before.attributes('disabled')).toBeUndefined()

      await before.trigger('click')
      await flushPromises()

      // 按钮仍在原位（焦点不会掉到 body），进入 disabled + aria-busy 的加载态
      const during = wrapper.get('.sf-roster__more')
      expect(during.exists()).toBe(true)
      expect(during.attributes('aria-busy')).toBe('true')
      // 用 aria-disabled 而非原生 disabled：原生 disabled 会让 Chrome 把焦点踢到 body
      expect(during.attributes('aria-disabled')).toBe('true')
      expect(during.attributes('disabled')).toBeUndefined()
      // spinner 分支因此可达（不再被 v-if 提前摘掉）
      expect(during.find('.sf-roster__more-icon--motion').exists()).toBe(true)

      releasePage?.({ items: [user(21)], total: 40, hasMore: false })
      await flushPromises()
      expect(wrapper.find('.sf-roster__more-icon--motion').exists()).toBe(false)
      // hasMore=false 后按钮才收起
      expect(wrapper.find('.sf-roster__more').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('an out-of-range append page (total=0) keeps the known headcount and stops paging', async () => {
    calls.length = 0
    respond = async (offset) => {
      if (offset === 0) {
        return { items: Array.from({ length: 20 }, (_, index) => user(index + 1)), total: 22, hasMore: true }
      }
      // 两次翻页之间有人取消赞：offset 越界，服务端回 total=0，但这一页仍有内容
      return { items: [user(21), user(22)], total: 0, hasMore: true }
    }
    const wrapper = mountRoster({ active: true })
    try {
      await flushPromises()
      expect(wrapper.text()).toContain('forum.reaction.rosterTotal:22')

      await wrapper.get('.sf-roster__more').trigger('click')
      await flushPromises()
      // 人数不能回退成 0（头部「共 N 人」不消失），列表照常追加
      expect(wrapper.text()).toContain('forum.reaction.rosterTotal:22')
      expect(wrapper.findAll('.sf-roster__item')).toHaveLength(22)
      // 服务端没给有效总数：不再继续翻页
      expect(wrapper.find('.sf-roster__more').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('comment reactions load the comment roster', async () => {
    calls.length = 0
    respond = async () => ({ items: [user(4)], total: 1, hasMore: false })
    const wrapper = mountRoster({ targetType: 'comment', targetId: 42, active: true })
    try {
      await flushPromises()
      expect(calls).toEqual([{ kind: 'comment', id: 42, limit: 20, offset: 0 }])
    } finally {
      wrapper.unmount()
    }
  })
})

describe('reaction roster failure state', () => {
  test('shows the inline error with a retry instead of a toast', async () => {
    calls.length = 0
    respond = async () => { throw new Error('boom') }
    const wrapper = mountRoster({ active: true })
    try {
      await flushPromises()
      const error = wrapper.get('.sf-roster__error')
      expect(error.text()).toContain('forum.reaction.rosterFailed')
      expect(wrapper.get('.sf-roster__retry').text()).toBe('forum.reaction.retry')
      expect(wrapper.findAll('.sf-roster__item')).toHaveLength(0)

      respond = async () => ({ items: [user(5, '恢复用户')], total: 1, hasMore: false })
      await wrapper.get('.sf-roster__retry').trigger('click')
      await flushPromises()
      expect(wrapper.find('.sf-roster__error').exists()).toBe(false)
      expect(wrapper.text()).toContain('恢复用户')
    } finally {
      wrapper.unmount()
    }
  })
})

describe('reaction roster self row', () => {
  test('marks the current user row with the localized (you) marker', async () => {
    calls.length = 0
    selfUser.value = { id: 3, username: 'u3', displayName: '我', avatar: { kind: 'initials', url: '', alt: '' } }
    respond = async () => ({ items: [user(3, '我'), user(9, '别人')], total: 2, hasMore: false })
    const wrapper = mountRoster({ active: true })
    try {
      await flushPromises()
      const selfRow = wrapper.get('.sf-roster__item--self')
      expect(selfRow.text()).toContain('forum.reaction.you')
      // 标记在截断 span 之外，不会被省略号吃掉
      expect(selfRow.get('.sf-roster__you').exists()).toBe(true)
      expect(wrapper.findAll('.sf-roster__item--self')).toHaveLength(1)
    } finally {
      selfUser.value = null
      wrapper.unmount()
    }
  })
})
