<script setup lang="ts">
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import type { AdminListBadge } from '~/utils/admin/adminSurfaces'

defineOptions({ name: 'SFAdminExtensionUpdateBadges' })

const props = withDefaults(defineProps<{
  badges?: AdminListBadge[]
}>(), {
  badges: () => []
})

const { t } = useI18n()
const adminRoutes = useAdminRoutes()

// links 值已在 normalizeResourceLinks 里过 safeAdminPageId（后台相对路径），
// 这里只做前缀拼接；没有深链的徽章降级为不可点击文本，而不是伪造成链接。
function badgeHref(badge: AdminListBadge) {
  // 双保险：normalize 之后 pageId 只可能是字符串，但徽章数据也可能来自调用方拼装。
  return typeof badge.pageId === 'string' && badge.pageId ? adminRoutes.path(badge.pageId) : undefined
}

// 来源取 surface 的扩展 id 原值：Core 不认识商城，不查展示名。
function badgeDescription(badge: AdminListBadge) {
  if (!badge.sourceExtensionId) return badge.title
  return `${badge.label} · ${t('admin.extensions.update.from', { extension: badge.sourceExtensionId })}`
}

// tone / icon 来自 surface 自己的声明，Core 只做映射，不改写语义。
function badgeColor(badge: AdminListBadge) {
  return badge.tone
}

// 声明里没给图标时用 arrow-up-circle：它表达「有更新」，与右侧的
// arrow-up-right（跳到后台另一个页面）分工不同，两个都要保留。
function badgeIcon(badge: AdminListBadge) {
  return badge.icon || 'i-lucide-arrow-up-circle'
}
</script>

<template>
  <template v-for="badge in props.badges" :key="badge.id">
    <a
      v-if="badgeHref(badge)"
      :href="badgeHref(badge)"
      class="inline-flex max-w-full shrink-0 rounded-md transition hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent)] focus-visible:ring-offset-2 focus-visible:ring-offset-white dark:focus-visible:ring-offset-zinc-900"
      :title="badgeDescription(badge)"
      :aria-label="badgeDescription(badge)"
    >
      <UBadge
        :color="badgeColor(badge)"
        variant="soft"
        :icon="badgeIcon(badge)"
        trailing-icon="i-lucide-arrow-up-right"
        class="max-w-full font-semibold"
      >
        {{ badge.label }}
      </UBadge>
    </a>
    <UBadge
      v-else
      :color="badgeColor(badge)"
      variant="soft"
      :icon="badgeIcon(badge)"
      class="max-w-full cursor-default font-semibold"
      :title="badgeDescription(badge)"
    >
      {{ badge.label }}
    </UBadge>
  </template>
</template>
