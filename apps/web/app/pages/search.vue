<script setup lang="ts">
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import SFSearchPage from '~/components/forum/search/SFSearchPage.vue'
import { buildForumHomeQuery, parseForumHomeQuery } from '~/utils/forum/forumHome'

/** forum.search 路由壳：SEO + Page Registry outlet；结果交互由搜索页 Host 岛拥有。
 *  slot 里的 SFSearchPage 只在 fail-closed 回退（registry 关闭 / resolve 失败）时渲染；
 *  主题生效时由 templates/search.html 的 <sf-search-page> 岛挂载同一个组件。 */
const { t } = useI18n()
const route = useRoute()
const committedFilters = computed(() => parseForumHomeQuery(route.query))
const currentPage = computed(() => parsePublicPage(route.query.page))

useSForumSeo(computed(() => ({
  title: t('search.metaTitle'),
  type: 'static',
  path: publicPagePath('/search', currentPage.value, buildForumHomeQuery(committedFilters.value)),
  description: t('search.metaDescription'),
  public: true,
  noindex: true
})))
</script>

<template>
  <SFPageOutlet page="forum.search">
    <SFSearchPage />
  </SFPageOutlet>
</template>
