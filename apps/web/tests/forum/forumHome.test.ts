import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import * as forumHome from '../../app/utils/forum/forumHome'
import {
  buildForumHomeQuery,
  firstQueryValue,
  forumHomeCategoryRedirect,
  forumHomeFeedKey,
  parseForumHomeQuery
} from '../../app/utils/forum/forumHome'

describe('forum homepage query helpers', () => {
  test('normalizes scalar route query values and ignores arrays', () => {
    expect(parseForumHomeQuery({ q: '  nuxt  ', category: 'dev', tag: ['go'] }))
      .toEqual({ query: 'nuxt', categorySlug: 'dev', tagSlug: '', sort: '' })
  })

  test('normalizes null and array route query values to empty filters', () => {
    expect(parseForumHomeQuery({ q: null, category: ['dev'], tag: null }))
      .toEqual({ query: '', categorySlug: '', tagSlug: '', sort: '' })
  })

  test('omits empty filters when building route query', () => {
    expect(buildForumHomeQuery({ query: '', categorySlug: 'dev', tagSlug: '', sort: '' }))
      .toEqual({ category: 'dev' })
  })

  test('omits filters containing only whitespace', () => {
    expect(buildForumHomeQuery({ query: '  ', categorySlug: '\n', tagSlug: ' \t ', sort: '' }))
      .toEqual({})
  })

  test('round-trips committed filters', () => {
    const filters = { query: '搜索', categorySlug: '开发', tagSlug: 'nuxt', sort: 'hot' as const }
    expect(parseForumHomeQuery(buildForumHomeQuery(filters))).toEqual(filters)
  })

  test('keeps the default sort out of the URL and rejects unknown values', () => {
    // '' = 站点默认排序：URL 不携带 sort，服务端按 forum.list.default_sort 决定
    expect(buildForumHomeQuery({ query: '', categorySlug: '', tagSlug: '', sort: '' })).toEqual({})
    expect(buildForumHomeQuery({ query: '', categorySlug: '', tagSlug: '', sort: 'active' }))
      .toEqual({ sort: 'active' })
    // 非法值不得进入请求（服务端虽然会回退，但前端不能把脏参数写进 URL）
    expect(parseForumHomeQuery({ sort: 'replies' }).sort).toBe('')
    expect(parseForumHomeQuery({ sort: ['hot'] }).sort).toBe('')
    expect(parseForumHomeQuery({ sort: ' HOT ' }).sort).toBe('hot')
  })

  test('changes the feed key when each committed filter changes', () => {
    const filters = { query: 'nuxt', categorySlug: 'dev', tagSlug: 'vue', sort: 'latest' as const }
    expect(forumHomeFeedKey(filters)).not.toBe(forumHomeFeedKey({ ...filters, query: 'go' }))
    expect(forumHomeFeedKey(filters)).not.toBe(forumHomeFeedKey({ ...filters, categorySlug: 'support' }))
    expect(forumHomeFeedKey(filters)).not.toBe(forumHomeFeedKey({ ...filters, tagSlug: 'go' }))
    // 排序必须参与 feedKey：换 sort 要重置分页与 cursor，而不是复用旧 feed
    expect(forumHomeFeedKey(filters)).not.toBe(forumHomeFeedKey({ ...filters, sort: 'hot' }))
    expect(forumHomeFeedKey(filters)).not.toBe(forumHomeFeedKey({ ...filters, sort: '' }))
  })

  test('rejects an old request when filters cycle from A to B and back to A', async () => {
    const isRequestCurrent = (forumHome as Record<string, unknown>).isForumHomeRequestCurrent
    expect(typeof isRequestCurrent).toBe('function')
    if (typeof isRequestCurrent !== 'function') return

    let resolveRequest!: () => void
    const pending = new Promise<void>((resolve) => {
      resolveRequest = resolve
    })
    let generation = 0
    let activeFeedKey = 'A'
    const applied: string[] = []
    const oldRequest = { generation, feedKey: activeFeedKey }

    const applyOldRequest = (async () => {
      await pending
      if (isRequestCurrent(oldRequest, generation, activeFeedKey)) {
        applied.push('old A')
      }
    })()

    generation += 1
    activeFeedKey = 'B'
    generation += 1
    activeFeedKey = 'A'
    resolveRequest()
    await applyOldRequest

    expect(applied).toEqual([])
    expect(isRequestCurrent({ generation, feedKey: 'A' }, generation, activeFeedKey)).toBe(true)
  })

  test('ends pagination when the backend clamps or a full page adds no new topics', () => {
    const hasReachedEnd = (forumHome as Record<string, unknown>).hasReachedForumHomeEnd
    expect(typeof hasReachedEnd).toBe('function')
    if (typeof hasReachedEnd !== 'function') return

    expect(hasReachedEnd({
      requestedPage: 201,
      responsePage: 200,
      responseItemCount: 10,
      newItemCount: 0,
      loadedCount: 2000,
      total: 6001,
      perPage: 10
    })).toBe(true)

    expect(hasReachedEnd({
      requestedPage: 2,
      responsePage: 2,
      responseItemCount: 10,
      newItemCount: 0,
      loadedCount: 10,
      total: 100,
      perPage: 10
    })).toBe(true)

    expect(hasReachedEnd({
      requestedPage: 2,
      responsePage: 2,
      responseItemCount: 10,
      newItemCount: 10,
      loadedCount: 20,
      total: 100,
      perPage: 10
    })).toBe(false)

    // M5：API hasMore=false 立即结束（不依赖 total）
    expect(hasReachedEnd({
      requestedPage: 2,
      responsePage: 1,
      responseItemCount: 10,
      newItemCount: 10,
      loadedCount: 30,
      total: 1_000_000,
      perPage: 10,
      hasMore: false
    })).toBe(true)

    expect(hasReachedEnd({
      requestedPage: 2,
      responsePage: 1,
      responseItemCount: 10,
      newItemCount: 10,
      loadedCount: 30,
      total: 1_000_000,
      perPage: 10,
      hasMore: true
    })).toBe(false)
  })

  test('home page prefers nextCursor for infinite scroll load-more', () => {
    const source = readFileSync(new URL('../../app/components/forum/SFHomePage.vue', import.meta.url), 'utf8')
    expect(source).toContain('nextCursor')
    expect(source).toContain('after')
    expect(source).toContain('hasMore')
  })

  test('keeps empty results inside the active theme with stable presentation hooks', () => {
    const source = readFileSync(new URL('../../app/components/forum/SFHomePage.vue', import.meta.url), 'utf8')
    const settings = readFileSync(new URL('../../app/composables/themes/useActiveThemeSettings.ts', import.meta.url), 'utf8')

    expect(source).not.toContain('forceDefaultTheme')
    expect(settings).not.toContain('forceDefaultTheme')
    expect(source).toContain(":data-feed-state=\"feedState\"")
    expect(source).toContain('data-sf-region="topic-list-empty"')
    expect(source).toContain('@action="resetFilters"')
  })

  // 搜索页从 SFHomePage 拆出后，搜索空态钩子与 island 身份由独立组件承担。
  test('search page owns its own empty-state hooks and island identity', () => {
    const page = readFileSync(new URL('../../app/components/forum/search/SFSearchPage.vue', import.meta.url), 'utf8')
    const surface = readFileSync(new URL('../../app/components/forum/search/SFSearchResultSurface.vue', import.meta.url), 'utf8')

    expect(page).toContain('data-sforum-island-body="forum.component.search_page"')
    expect(page).toContain(':data-feed-state="state"')
    expect(surface).toContain('data-sf-region="search-empty"')
    expect(surface).toContain('search.emptyTitle')
    expect(surface).toContain('search.emptyDescription')
  })
})

describe('forum homepage legacy ?category= redirect', () => {
  test('takes the first value of a repeated query parameter', () => {
    expect(firstQueryValue(['dev', 'ops'])).toBe('dev')
    expect(firstQueryValue('  dev  ')).toBe('dev')
    // 数组里不是字符串、空数组、null 都当空：宁可留在首页，也不跳到一个猜出来的分类。
    expect(firstQueryValue([1, 2])).toBe('')
    expect(firstQueryValue([])).toBe('')
    expect(firstQueryValue(null)).toBe('')
    expect(firstQueryValue(undefined)).toBe('')
  })

  test('never builds `/c/a%2Cb` out of an array parameter', () => {
    // 回归 RV-34：`?category[]=dev&category[]=ops` 曾被 `as string` 强转成 "dev,ops"。
    expect(forumHomeCategoryRedirect({ category: ['dev', 'ops'] }, '/')).toBe('/c/dev')
    expect(forumHomeCategoryRedirect({ category: ['dev', 'ops'] }, '/')).not.toContain('%2C')
  })

  test('skips the redirect for empty and non-string values', () => {
    expect(forumHomeCategoryRedirect({}, '/')).toBe('')
    expect(forumHomeCategoryRedirect({ category: null }, '/')).toBe('')
    expect(forumHomeCategoryRedirect({ category: '' }, '/')).toBe('')
    expect(forumHomeCategoryRedirect({ category: '   ' }, '/')).toBe('')
    expect(forumHomeCategoryRedirect({ category: { slug: 'dev' } }, '/')).toBe('')
  })

  test('stays put when the route already is a category path', () => {
    expect(forumHomeCategoryRedirect({ category: 'dev' }, '/c/dev')).toBe('')
  })

  test('encodes a single scalar category into the category path', () => {
    expect(forumHomeCategoryRedirect({ category: '开发' }, '/')).toBe(`/c/${encodeURIComponent('开发')}`)
  })

  test('the home route shell routes the redirect through the shared helper', () => {
    const source = readFileSync(new URL('../../app/pages/index.vue', import.meta.url), 'utf8')
    expect(source).toContain('forumHomeCategoryRedirect(route.query, route.path)')
    expect(source).toContain('navigateTo(categoryRedirect, { redirectCode: 301, replace: true })')
    expect(source).not.toContain('route.query.category as string')
  })
})

describe('SFSearch contract', () => {
  test('preserves v-model and filter events while exposing an accessible search form', () => {
    const source = readFileSync(new URL('../../app/components/SFSearch.vue', import.meta.url), 'utf8')

    expect(source).toContain('ariaLabel?: string')
    expect(source).toContain("'submit': [value: string]")
    expect(source).toContain('<form role="search"')
    expect(source).toContain('@submit.prevent')
    expect(source).toContain('@keydown="onKeydown"')
    expect(source).toContain("if (event.key !== 'Enter' || event.isComposing)")
    expect(source).toContain('type="button"')
    expect(source).toContain('@click="submit"')
    expect(source).toContain("emit('submit', (inputElement.value?.value ?? props.modelValue).trim())")
    expect(source).toContain('kbd: undefined')
    expect(source).toContain("'update:modelValue': [value: string]")
    expect(source).toContain("'update:selectedFilter': [value: string]")
    // 占位与过滤组名称都走 i18n（RV-28）：默认占位不是写死中文，而是 locale 文案。
    expect(source).toContain(':aria-label="ariaLabel || resolvedPlaceholder"')
    expect(source).toContain("t('search.placeholder')")
    expect(source).toContain('class="sf-search__filters" :aria-label="t(\'search.filtersLabel\')" role="group"')
    expect(source).toContain(':aria-pressed="selectedFilter === filter.value"')
  })
})
