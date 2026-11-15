import type { ForumCategoryGroup } from '~/utils/forum/forumTaxonomy'
import { useForumApi } from '~/composables/forum/useForumApi'

/**
 * 公开读面的分类目录（`GET /category-groups`）。
 *
 * 同一个端点的 `useAsyncData` key 曾按页面各起一个（18 处 18 个 key），分类页还把
 * `${categorySlug}` 拼进 key —— 每切一次分类就整表重取一次，而它和 slug 根本无关。
 * 公开读面统一走这一个 composable，因此整个公开面只有一条稳定 key：
 * 一次 SSR、一次会话只取一份 payload，页面之间来回切都命中缓存。
 *
 * 读法只有两种变体，key 跟着读法走，不接受调用方自定义 key：
 * - 默认：沿用 `useForumApi` 的 `serverInternal: import.meta.server`；
 * - `serverInternal: false`：必须走公开读的场景（公共导航侧栏），语义不同所以单独一条 key。
 */
const CATEGORY_GROUPS_KEY = 'forum-category-groups'
const PUBLIC_CATEGORY_GROUPS_KEY = 'forum-category-groups:public'

export type CategoryGroupsReadOptions = {
  /** false = 公开读，不携带 SSR 内参。 */
  serverInternal?: boolean
  /** 首屏不阻塞渲染时用（主题页左栏导航）。 */
  lazy?: boolean
}

export function categoryGroupsAsyncKey(options: CategoryGroupsReadOptions = {}) {
  return options.serverInternal === false ? PUBLIC_CATEGORY_GROUPS_KEY : CATEGORY_GROUPS_KEY
}

export function useCategoryGroups(options: CategoryGroupsReadOptions = {}) {
  const forumApi = useForumApi()
  const publicRead = options.serverInternal === false

  return useAsyncData(
    categoryGroupsAsyncKey(options),
    () => publicRead ? forumApi.listCategoryGroups({ serverInternal: false }) : forumApi.listCategoryGroups(),
    {
      default: () => [] as ForumCategoryGroup[],
      lazy: Boolean(options.lazy)
    }
  )
}
