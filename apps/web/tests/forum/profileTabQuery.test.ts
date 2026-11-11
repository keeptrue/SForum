import { describe, expect, test } from 'bun:test'
import { ref } from 'vue'
import { readFileSync } from 'node:fs'

/**
 * 行为级回归：`?tab=` 是作者旁徽章 chip 的落地入口（`/u/{username}?tab=badges`），
 * 所以「读一次 → 写回 → tab 不存在时回落并纠正 query」这三条不能只靠页面手测。
 *
 * 直接用真实源码跑：剥掉 import，注入 route/router/ref（与 useAuthReturnNavigation 同一手法）。
 */
const sourcePath = new URL('../../app/composables/profile/useProfileTabQuery.ts', import.meta.url)

type ReplacePayload = { query: Record<string, unknown> }

function loadComposable(initialQuery: Record<string, unknown> = {}, defaultTab?: string) {
  const source = readFileSync(sourcePath, 'utf8')
  const executable = new Bun.Transpiler({ loader: 'ts', target: 'browser' })
    .transformSync(source)
    .replace('export function useProfileTabQuery', 'return function useProfileTabQuery')

  // router.replace 之后 route.query 会跟着变，mock 必须同步改写，否则测不出幂等。
  const query = { ...initialQuery }
  const replaces: ReplacePayload[] = []
  const factory = new Function('useRoute', 'useRouter', 'ref', executable)
  const useProfileTabQuery = factory(
    () => ({ query }),
    () => ({
      replace: (payload: ReplacePayload) => {
        for (const key of Object.keys(query)) {
          delete query[key]
        }
        Object.assign(query, payload.query)
        replaces.push(payload)
        return Promise.resolve()
      }
    }),
    ref
  ) as (defaultTab?: string) => {
    activeTab: { value: string }
    syncTabQuery: (tab: string) => void
    resetTabIfMissing: (available: string[]) => void
  }

  return { tab: useProfileTabQuery(defaultTab), replaces, query }
}

describe('useProfileTabQuery', () => {
  test('initialises from the query and falls back to topics', () => {
    expect(loadComposable({}).tab.activeTab.value).toBe('topics')
    expect(loadComposable({ tab: 'badges' }).tab.activeTab.value).toBe('badges')
    expect(loadComposable({ tab: ['replies'] }).tab.activeTab.value).toBe('replies')
    expect(loadComposable({ tab: '   ' }).tab.activeTab.value).toBe('topics')
    expect(loadComposable({ tab: 42 }).tab.activeTab.value).toBe('topics')
  })

  test('writing a tab keeps other query params and drops the default one', () => {
    const { tab, replaces, query } = loadComposable({ page: '2' })

    tab.syncTabQuery('bookmarks')
    expect(query).toEqual({ page: '2', tab: 'bookmarks' })

    tab.syncTabQuery('topics')
    expect(query).toEqual({ page: '2' })
    expect(replaces).toHaveLength(2)
  })

  test('syncing the tab already in the query does not navigate again', () => {
    const { tab, replaces } = loadComposable({ tab: 'bookmarks' })

    tab.syncTabQuery('bookmarks')

    expect(replaces).toEqual([])
  })

  test('a tab that no longer exists falls back and fixes the query', () => {
    const { tab, query } = loadComposable({ tab: 'bookmarks' })

    tab.resetTabIfMissing(['topics', 'replies', 'about'])

    expect(tab.activeTab.value).toBe('topics')
    expect(query).toEqual({})
  })

  test('an available tab is left untouched', () => {
    const { tab, replaces } = loadComposable({ tab: 'about' })

    tab.resetTabIfMissing(['topics', 'replies', 'about'])

    expect(tab.activeTab.value).toBe('about')
    expect(replaces).toEqual([])
  })

  test('honours a custom default tab', () => {
    const { tab, query } = loadComposable({}, 'replies')

    expect(tab.activeTab.value).toBe('replies')

    tab.syncTabQuery('topics')
    expect(query).toEqual({ tab: 'topics' })

    tab.syncTabQuery('replies')
    expect(query).toEqual({})
  })
})
