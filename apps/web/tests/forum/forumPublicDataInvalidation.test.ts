import { expect, test } from 'bun:test'

import { invalidateForumPublicData } from '../../app/composables/forum/useForumPublicDataInvalidation'

test('approved moderation content clears every persisted homepage authority', () => {
  const clearedData: string[] = []
  const clearedState: string[] = []
  const originalClearNuxtData = globalThis.clearNuxtData
  const originalClearNuxtState = globalThis.clearNuxtState
  globalThis.clearNuxtData = ((predicate: (key: string) => boolean) => {
    for (const key of ['forum-home-topics:forum.home:all:1', 'forum-category-groups', 'forum-category-groups:public', 'forum-home-tags', 'other']) {
      if (predicate(key)) clearedData.push(key)
    }
  }) as typeof clearNuxtData
  globalThis.clearNuxtState = ((key: string) => { clearedState.push(key) }) as typeof clearNuxtState

  try {
    invalidateForumPublicData()
  } finally {
    globalThis.clearNuxtData = originalClearNuxtData
    globalThis.clearNuxtState = originalClearNuxtState
  }

  expect(clearedData).toEqual(['forum-home-topics:forum.home:all:1', 'forum-category-groups', 'forum-category-groups:public', 'forum-home-tags'])
  expect(clearedState).toEqual([
    'forum-home-loaded-topics',
    'forum-home-topic-total',
    'forum-home-loaded-feed-key',
    'forum-home-rendered-at'
  ])
})
