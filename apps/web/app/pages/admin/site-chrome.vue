<script setup lang="ts">
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
// 品牌与前台壳已并入个性化设置；保留路由以兼容旧书签与侧栏历史。
definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminSiteChromeRedirect'
})

const route = useRoute()
const adminRoutes = useAdminRoutes()
// 跳转前的占位文案走 i18n：这层壳只是旧书签的落地页（UX2-19）。
const { t } = useI18n()

const tab = typeof route.query.tab === 'string' ? route.query.tab : 'brand'
const target = adminRoutes.path('/personalization')

await navigateTo({
  path: target,
  query: { tab },
  replace: true
})
</script>

<template>
  <div class="py-10 text-center text-sm text-slate-500 dark:text-zinc-400">
    {{ t('common.redirecting') }}
  </div>
</template>
