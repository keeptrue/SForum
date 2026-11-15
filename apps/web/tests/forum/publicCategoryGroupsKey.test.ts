import { afterEach, describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

import {
  categoryGroupsAsyncKey,
  useCategoryGroups
} from '../../app/composables/forum/useCategoryGroups'

/**
 * RV-35：`GET /category-groups` 在公开读面上只允许一条稳定 key。
 *
 * 旧状是 18 处 18 个 key，分类页还把 slug 拼进 key（`forum-category-page-groups:${slug}`），
 * 于是每切一次分类就整表重取一次。现在公开面共用 `useCategoryGroups()`，
 * key 只跟着「读法」分两个变体：内参一条、公开读一条。
 */

const globals = globalThis as typeof globalThis & Record<string, unknown>
const originalGlobals: Record<string, unknown> = {}

const PUBLIC_SURFACES = [
  'app/components/forum/SFHomePage.vue',
  'app/components/forum/SFCategoryIndexPage.vue',
  'app/components/forum/SFCategoryShowPage.vue',
  'app/components/forum/SFTagIndexPage.vue',
  'app/components/forum/SFTagShowPage.vue',
  'app/components/forum/SFTopicShowPage.vue',
  'app/components/forum/badges/SFBadgeCatalogPage.vue',
  'app/components/forum/navigation/SFMobileSidebarContent.vue'
] as const

function surfaceSource(relativePath: string) {
  return readFileSync(new URL(`../../${relativePath}`, import.meta.url), 'utf8')
}

function installGlobals() {
  for (const key of ['useApiClient', 'useAsyncData'] as const) {
    originalGlobals[key] = globals[key]
  }

  // 记录每次 useAsyncData 的 key 与选项；同一个 key 只执行一次 handler（Nuxt 的去重语义）。
  const calls: Array<{ key: string, options: Record<string, unknown> }> = []
  const payload = new Map<string, unknown>()
  const loads: string[] = []

  Object.assign(globals, {
    useApiClient: () => ({ request: async () => [] }),
    useAsyncData: async (key: string, handler: () => Promise<unknown>, options: Record<string, unknown> = {}) => {
      calls.push({ key, options })
      if (!payload.has(key)) {
        loads.push(key)
        payload.set(key, await handler())
      }
      return { data: { value: payload.get(key) }, pending: { value: false }, error: { value: null }, refresh: async () => {} }
    }
  })

  return { calls, loads }
}

function restoreGlobals() {
  for (const key of Object.keys(originalGlobals)) {
    if (originalGlobals[key] === undefined) {
      delete globals[key]
    } else {
      globals[key] = originalGlobals[key]
    }
  }
}

afterEach(() => {
  restoreGlobals()
})

describe('public category groups key', () => {
  test('keeps exactly two variants: SSR-internal and public read', () => {
    expect(categoryGroupsAsyncKey()).toBe('forum-category-groups')
    expect(categoryGroupsAsyncKey({ serverInternal: undefined })).toBe('forum-category-groups')
    expect(categoryGroupsAsyncKey({ serverInternal: false })).toBe('forum-category-groups:public')
    // lazy 只是渲染策略，不许影响 key：否则同一个读法又会分裂成两条。
    expect(categoryGroupsAsyncKey({ lazy: true })).toBe(categoryGroupsAsyncKey())
    expect(categoryGroupsAsyncKey({ serverInternal: false, lazy: true })).toBe('forum-category-groups:public')
  })

  test('serves every public surface from one payload instead of refetching per page', async () => {
    const { calls, loads } = installGlobals()

    const home = await useCategoryGroups()
    const categoryIndex = await useCategoryGroups()
    const categoryPage = await useCategoryGroups()
    const tagShow = await useCategoryGroups({ lazy: true })

    expect(calls.map(call => call.key)).toEqual([
      'forum-category-groups',
      'forum-category-groups',
      'forum-category-groups',
      'forum-category-groups'
    ])
    // 四次挂载、一条 key ⇒ 只真正取了一次。
    expect(loads).toEqual(['forum-category-groups'])
    expect(home.data.value).toEqual([])
    expect(categoryPage.data.value).toBe(tagShow.data.value)
  })

  test('keeps the public-read surface on its own key', async () => {
    const { calls, loads } = installGlobals()

    await useCategoryGroups()
    await useCategoryGroups({ serverInternal: false })

    expect(calls.map(call => call.key)).toEqual(['forum-category-groups', 'forum-category-groups:public'])
    expect(loads).toHaveLength(2)
  })

  test('forwards lazy for the topic page navigation column', async () => {
    const { calls } = installGlobals()

    await useCategoryGroups({ lazy: true })
    await useCategoryGroups()

    expect(calls[0]!.options.lazy).toBe(true)
    expect(calls[1]!.options.lazy).toBe(false)
  })

  test('no public surface keeps a private useAsyncData key for the same endpoint', () => {
    // 带引号的旧 key 字面量：`forum-categories-index-rendered-at` 这类 useState key 不在其中。
    const retiredKeys = [
      "'forum-home-category-groups'",
      "'forum-categories-index'",
      "'forum-tags-index-category-groups'",
      "'forum-tag-page-category-groups'",
      "'forum-topic-show-category-groups'",
      "'forum-badges-category-groups'",
      'forum-category-page-groups',
      'public-sidebar-navigation-categories'
    ]

    for (const surface of PUBLIC_SURFACES) {
      const source = surfaceSource(surface)
      expect(source).toContain('useCategoryGroups(')
      // 这些页面不再自己读 category-groups 端点，也不再自带该端点的 key。
      expect(source).not.toContain('listCategoryGroups')
      for (const retired of retiredKeys) {
        expect(source).not.toContain(retired)
      }
    }
  })

  test('the category page key never carries the slug', () => {
    const source = surfaceSource('app/components/forum/SFCategoryShowPage.vue')
    expect(source).not.toContain('forum-category-page-groups')

    // 分类目录的调用不带任何参数：key 里没有 slug，也没有分页。
    const calls = source.match(/useCategoryGroups\([^)]*\)/g) || []
    expect(calls).toEqual(['useCategoryGroups()'])
    expect(source).toContain('const { data: categoryGroups, pending: categoriesPending } = await useCategoryGroups()')
    // 主题列表仍然按分类 + 分页分裂（那是另一条端点，本来就该按 slug 取）。
    expect(source).toContain('forum-category-page-topics:')
  })

  test('admin, moderation, notifications and composer keep their own page-scoped keys', () => {
    // 有意保留：同一条路由内唯一、没有跨页复用收益，也不属于公开读面。
    const ownKeySurfaces = [
      'app/pages/admin/forum/settings.vue',
      'app/pages/admin/forum/categories.vue',
      'app/components/moderation/SFModerationReviewPage.vue',
      'app/components/notifications/detail/SFNotificationDetailPage.vue',
      'app/components/notifications/SFNotificationsPage.vue',
      'app/components/forum/SFTopicComposerPage.vue',
      'app/components/forum/SFTopicEditPage.vue',
      'app/components/forum/SFTopicEditor.vue',
      'app/components/profile/SFProfileShowPage.vue',
      'app/components/settings/SFSettingsAccountSidebar.vue'
    ]

    for (const surface of ownKeySurfaces) {
      expect(surfaceSource(surface)).toContain('listCategoryGroups')
    }
  })
})
