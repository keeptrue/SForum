<script setup lang="ts">
import SFBadgeCatalogPage from '~/components/forum/badges/SFBadgeCatalogPage.vue'
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
/** forum.badge.index 路由壳：SEO + outlet；呈现由主题 L1 + SFBadgeCatalogPage 岛拥有。 */
const { t } = useI18n()
const { seoSettings, siteName } = useWebOptions()

useSForumSeo(computed(() => ({
  type: 'static' as const,
  path: '/badges',
  title: t('badges.catalog.title'),
  description: t('badges.catalog.description'),
  public: true,
  breadcrumbs: [
    { name: seoSettings.value.seoSiteName || siteName.value, path: '/' },
    { name: t('badges.catalog.title'), path: '/badges' }
  ]
})))
</script>

<template>
  <SFPageOutlet page="forum.badge.index">
    <SFBadgeCatalogPage />
  </SFPageOutlet>
</template>
