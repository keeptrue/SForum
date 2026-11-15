<script setup lang="ts">
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import SFHomePage from '~/components/forum/SFHomePage.vue'
/**
 * 论坛首页路由壳：SEO + Page Registry outlet。
 * 默认呈现由主题 L1（home.html → forum.component.home_page 岛）拥有；
 * 本页 slot 仅作 SFPageOutlet fail-closed 紧急回退（永不删除）。
 */
import {
  buildForumHomeQuery,
  forumHomeCategoryRedirect,
  parseForumHomeQuery
} from '~/utils/forum/forumHome'

const { t } = useI18n()
const route = useRoute()
const localePath = useLocalePath()
const { seoSettings } = useWebOptions()
const committedFilters = computed(() => parseForumHomeQuery(route.query))
const currentPage = computed(() => parsePublicPage(route.query.page))
// `?category[]=` 是数组：强转字符串会 301 到 `/c/a%2Cb` 这种坏路径（RV-34）。
const categoryRedirect = forumHomeCategoryRedirect(route.query, route.path)
const hasActiveFilters = computed(() => Boolean(
  committedFilters.value.query
  || committedFilters.value.categorySlug
  || committedFilters.value.tagSlug
))

// 统一到独立搜索页；旧首页查询链接保留筛选和分页后永久跳转。
if (committedFilters.value.query) {
  await navigateTo(
    publicPageLocation(
      localePath('/search'),
      currentPage.value,
      buildForumHomeQuery(committedFilters.value)
    ),
    { redirectCode: 301, replace: true }
  )
} else if (categoryRedirect) {
  await navigateTo(categoryRedirect, { redirectCode: 301, replace: true })
}

useSForumSeo(computed(() => ({
  type: 'home',
  path: publicPagePath('/', currentPage.value),
  description: t('home.metaDescription', { siteName: seoSettings.value.seoSiteName }),
  public: true,
  noindex: hasActiveFilters.value || Object.keys(route.query).some(key => key !== 'page')
})))
</script>

<template>
  <SFPageOutlet page="forum.home">
    <!-- fail-closed 紧急回退：主题 L1 / resolve 失败时仍渲染完整首页岛 -->
    <SFHomePage />
  </SFPageOutlet>
</template>
