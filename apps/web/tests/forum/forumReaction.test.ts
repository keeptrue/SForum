import { describe, expect, test } from 'bun:test'
import {
  forumReactionApplyToTopic,
  forumReactionCanToggle,
  forumReactionFromResponse,
  forumReactionLoginTarget,
  forumReactionNeedsLogin,
  forumReactionPreviewAfterToggle,
  forumReactionPreviewItems,
  forumReactionRollback,
  forumReactionRosterCanLoadMore,
  forumReactionRosterMerge,
  forumReactionRosterTotal,
  forumReactionStackOverflow,
  forumReactionRosterVisible,
  forumReactionToggle
} from '~/utils/forum/forumReaction'

describe('forumReactionToggle', () => {
  test('likes add one and flip the flag', () => {
    expect(forumReactionToggle({ liked: false, count: 3 })).toEqual({ liked: true, count: 4 })
  })

  test('unlikes remove one and flip the flag', () => {
    expect(forumReactionToggle({ liked: true, count: 3 })).toEqual({ liked: false, count: 2 })
  })

  test('never goes negative when the local count is already stale', () => {
    expect(forumReactionToggle({ liked: true, count: 0 })).toEqual({ liked: false, count: 0 })
  })
})

describe('forumReactionRollback', () => {
  test('restores the pre-click snapshot instead of keeping the optimistic guess', () => {
    const before = { liked: false, count: 2 }
    const optimistic = forumReactionToggle(before)
    expect(optimistic).toEqual({ liked: true, count: 3 })
    expect(forumReactionRollback(before)).toEqual({ liked: false, count: 2 })
  })

  test('clamps a stale negative snapshot back to zero', () => {
    expect(forumReactionRollback({ liked: true, count: -3 })).toEqual({ liked: true, count: 0 })
  })
})

describe('forumReactionApplyToTopic', () => {
  test('parent topic state follows the child change event', () => {
    const topic = { likeCount: 2, likedByMe: false }
    forumReactionApplyToTopic(topic, true, 3)
    expect(topic).toEqual({ likeCount: 3, likedByMe: true })
  })

  test('unlike writes the server truth back instead of decrementing locally', () => {
    const topic = { likeCount: 7, likedByMe: true }
    forumReactionApplyToTopic(topic, false, 6)
    expect(topic).toEqual({ likeCount: 6, likedByMe: false })
  })

  test('clamps a stale negative server count', () => {
    const topic = { likeCount: 1, likedByMe: true }
    forumReactionApplyToTopic(topic, false, -1)
    expect(topic).toEqual({ likeCount: 0, likedByMe: false })
  })
})

describe('forumReactionCanToggle', () => {
  test('blocks the second click while the first request is still pending', () => {
    expect(forumReactionCanToggle(true, false)).toBe(true)
    expect(forumReactionCanToggle(true, true)).toBe(false)
  })

  test('blocks every click once the feature flag is off', () => {
    expect(forumReactionCanToggle(false, false)).toBe(false)
  })
})

describe('forumReactionFromResponse', () => {
  test('server truth wins over the optimistic guess', () => {
    expect(forumReactionFromResponse(false, 9)).toEqual({ liked: false, count: 9 })
  })

  test('clamps invalid counts instead of rendering NaN', () => {
    expect(forumReactionFromResponse(true, Number.NaN)).toEqual({ liked: true, count: 0 })
    expect(forumReactionFromResponse(true, -4)).toEqual({ liked: true, count: 0 })
  })
})

describe('forumReaction gating', () => {
  test('anonymous viewers must log in before the request', () => {
    expect(forumReactionNeedsLogin(false)).toBe(true)
    expect(forumReactionNeedsLogin(true)).toBe(false)
  })

  test('roster entry only appears when enabled and non-empty', () => {
    expect(forumReactionRosterVisible(true, 2)).toBe(true)
    expect(forumReactionRosterVisible(true, 0)).toBe(false)
    expect(forumReactionRosterVisible(false, 2)).toBe(false)
  })

  test('login target keeps the current path as redirect', () => {
    expect(forumReactionLoginTarget('/t/7?page=2')).toEqual({ path: '/login', redirect: '/t/7?page=2' })
  })

  test('login target keeps the locale prefix of the current page', () => {
    expect(forumReactionLoginTarget('/en/t/7?page=2#c-3')).toEqual({
      path: '/login',
      redirect: '/en/t/7?page=2#c-3'
    })
  })
})

function summary(id: number, name = `用户${id}`) {
  return { id, username: `u${id}`, displayName: name, avatar: { kind: 'initials', url: '', alt: '' } }
}

describe('forumReactionPreviewItems', () => {
  test('keeps server order and truncates to max', () => {
    const likedBy = [summary(3), summary(9), summary(12), summary(15)]
    expect(forumReactionPreviewItems(likedBy, 3).map(item => item.id)).toEqual([3, 9, 12])
  })

  test('dedupes repeated ids before truncating', () => {
    expect(forumReactionPreviewItems([summary(3), summary(3), summary(9)], 2).map(item => item.id)).toEqual([3, 9])
  })

  test('handles missing input and a zero max without throwing', () => {
    expect(forumReactionPreviewItems(undefined, 3)).toEqual([])
    expect(forumReactionPreviewItems([summary(3)], 0)).toEqual([])
    expect(forumReactionPreviewItems([summary(3)], Number.NaN)).toEqual([])
  })
})

describe('forumReactionStackOverflow', () => {
  test('returns the hidden remainder', () => {
    expect(forumReactionStackOverflow(8, 3)).toBe(5)
    expect(forumReactionStackOverflow(3, 3)).toBe(0)
  })

  test('clamps a stale total below the shown count to zero', () => {
    expect(forumReactionStackOverflow(1, 3)).toBe(0)
    expect(forumReactionStackOverflow(-4, 0)).toBe(0)
    expect(forumReactionStackOverflow(Number.NaN, 2)).toBe(0)
  })
})

describe('forumReactionPreviewAfterToggle', () => {
  test('puts the current user first when liking', () => {
    const base = [summary(9), summary(12)]
    expect(forumReactionPreviewAfterToggle(base, summary(3), true, 3).map(item => item.id)).toEqual([3, 9, 12])
  })

  test('does not duplicate the current user when already in the base', () => {
    const base = [summary(3), summary(9), summary(12)]
    expect(forumReactionPreviewAfterToggle(base, summary(3), true, 3).map(item => item.id)).toEqual([3, 9, 12])
  })

  test('removes the current user when unliking', () => {
    const base = [summary(3), summary(9)]
    expect(forumReactionPreviewAfterToggle(base, summary(3), false, 3).map(item => item.id)).toEqual([9])
  })

  test('still truncates to max after the insert', () => {
    const base = [summary(9), summary(12), summary(15)]
    expect(forumReactionPreviewAfterToggle(base, summary(3), true, 2).map(item => item.id)).toEqual([3, 9])
  })

  test('a guest toggle leaves the preview untouched', () => {
    const base = [summary(9)]
    expect(forumReactionPreviewAfterToggle(base, null, true, 3).map(item => item.id)).toEqual([9])
  })
})

describe('forumReactionRosterMerge', () => {
  test('appends only unseen ids and keeps the first order', () => {
    const merged = forumReactionRosterMerge([summary(3), summary(9)], [summary(9), summary(12)])
    expect(merged.map(item => item.id)).toEqual([3, 9, 12])
  })

  test('handles an empty first page', () => {
    expect(forumReactionRosterMerge(undefined, [summary(3)]).map(item => item.id)).toEqual([3])
    expect(forumReactionRosterMerge([summary(3)], null).map(item => item.id)).toEqual([3])
  })
})

describe('forumReactionRosterCanLoadMore', () => {
  test('requires a next page, no in-flight request, and room under the cap', () => {
    expect(forumReactionRosterCanLoadMore(true, false, 20, 1000)).toBe(true)
    expect(forumReactionRosterCanLoadMore(false, false, 20, 1000)).toBe(false)
    expect(forumReactionRosterCanLoadMore(true, true, 20, 1000)).toBe(false)
    expect(forumReactionRosterCanLoadMore(true, false, 1000, 1000)).toBe(false)
    expect(forumReactionRosterCanLoadMore(true, false, 980, 1000)).toBe(true)
  })
})

describe('forumReactionApplyToTopic likedBy write-back', () => {
  test('writes the preview back so a parent re-render cannot reset the avatars', () => {
    const topic = { likeCount: 2, likedByMe: false, likedBy: [summary(9)] }
    forumReactionApplyToTopic(topic, true, 3, [summary(3), summary(9)])
    expect(topic).toEqual({ likeCount: 3, likedByMe: true, likedBy: [summary(3), summary(9)] })
  })

  test('leaves the existing preview alone when no new preview is passed', () => {
    const topic = { likeCount: 2, likedByMe: false, likedBy: [summary(9)] }
    forumReactionApplyToTopic(topic, true, 3)
    expect(topic.likedBy.map(item => item.id)).toEqual([9])
  })
})

describe('forumReactionRosterTotal', () => {
  test('takes the server total on the first page', () => {
    expect(forumReactionRosterTotal(0, 22, true)).toBe(22)
    expect(forumReactionRosterTotal(99, 22, true)).toBe(22)
  })

  test('never shrinks an already known total on append pages', () => {
    // offset 越界时服务端回 total=0：已知人数必须保住
    expect(forumReactionRosterTotal(22, 0, false)).toBe(22)
    expect(forumReactionRosterTotal(22, 5, false)).toBe(22)
    expect(forumReactionRosterTotal(22, 25, false)).toBe(25)
  })

  test('clamps invalid input instead of propagating NaN or negatives', () => {
    expect(forumReactionRosterTotal(0, Number.NaN, true)).toBe(0)
    expect(forumReactionRosterTotal(-3, -1, false)).toBe(0)
    expect(forumReactionRosterTotal(Number.NaN, 7, false)).toBe(7)
  })
})
