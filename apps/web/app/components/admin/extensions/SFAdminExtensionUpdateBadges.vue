<script setup lang="ts">
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import type { AdminListBadge } from '~/utils/admin/adminSurfaces'

defineOptions({ name: 'SFAdminExtensionUpdateBadges' })

const props = withDefaults(defineProps<{
  badges?: AdminListBadge[]
  extensionName?: string
}>(), {
  badges: () => [],
  extensionName: ''
})

const adminRoutes = useAdminRoutes()

// links 值已在 normalizeResourceLinks 里过 safeAdminPageId（后台相对路径），
// 这里只做前缀拼接；没有深链的徽章降级为不可点击，而不是伪造成链接。
function badgeHref(badge: AdminListBadge) {
  // 双保险：normalize 之后 pageId 只可能是字符串，但徽章数据也可能来自调用方拼装。
  return typeof badge.pageId === 'string' && badge.pageId ? adminRoutes.path(badge.pageId) : undefined
}

// 徽章文本通常只有「可更新」这类短词，读屏需要补上是哪个扩展。
function badgeAriaLabel(badge: AdminListBadge) {
  const detail = `${badge.title}: ${badge.label}`
  return props.extensionName ? `${detail} · ${props.extensionName}` : detail
}
</script>

<template>
  <template v-for="badge in props.badges" :key="badge.id">
    <a
      v-if="badgeHref(badge)"
      :href="badgeHref(badge)"
      class="inline-flex max-w-full shrink-0 items-center rounded focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent)] focus-visible:ring-offset-2 focus-visible:ring-offset-white dark:focus-visible:ring-offset-zinc-900"
      :title="badge.title"
      :aria-label="badgeAriaLabel(badge)"
    >
      <UBadge :color="badge.tone" variant="subtle" :icon="badge.icon">
        {{ badge.label }}
      </UBadge>
    </a>
    <UBadge
      v-else
      :color="badge.tone"
      variant="subtle"
      :icon="badge.icon"
      :title="badge.title"
      :aria-label="badgeAriaLabel(badge)"
    >
      {{ badge.label }}
    </UBadge>
  </template>
</template>
