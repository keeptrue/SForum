<script setup lang="ts">
import SFNotificationsPage from '~/components/notifications/SFNotificationsPage.vue'
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
/** forum.notifications 路由壳：SEO + outlet + fail-closed；呈现由主题 L1 + 岛拥有。 */
definePageMeta({ requiresAuth: true })

const { t } = useI18n()

// 收件箱是会话私有内容：标题走公共 SEO 解析器，robots 恒 noindex。
useSForumSeo({
  title: () => t('notifications.title'),
  description: () => t('notifications.description'),
  type: 'website',
  noindex: true
})
</script>

<template>
  <SFPageOutlet page="forum.notifications">
    <SFNotificationsPage />
  </SFPageOutlet>
</template>
