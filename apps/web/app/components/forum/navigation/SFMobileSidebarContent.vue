<script setup lang="ts">
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import { useCategoryGroups } from '~/composables/forum/useCategoryGroups'
import type { PublicNavigationItem } from '~/utils/navigation/publicNavigation'
import SFPublicSidebarContent from './SFPublicSidebarContent.vue'

const props = defineProps<{
  items: PublicNavigationItem[]
}>()

const emit = defineEmits<{ navigate: [] }>()
const route = useRoute()
const { can } = usePermissions()

// 侧栏必须走公开读（不带 SSR 内参），与公开面共用同一条稳定 key（RV-35）。
const { data: categoryGroups, pending } = useCategoryGroups({ serverInternal: false })

const categories = computed(() => categoryGroups.value.flatMap(group => group.categories || []))
const totalTopics = computed(() => categories.value.reduce((sum, category) => sum + (category.topicCount || 0), 0))
const selectedCategorySlug = computed(() => {
  const routeSlug = route.params.categorySlug
  if (typeof routeSlug === 'string') return routeSlug
  const querySlug = route.query.category
  return typeof querySlug === 'string' ? querySlug : ''
})
</script>

<template>
  <SFPublicSidebarContent
    :items="props.items"
    :categories="categories"
    :selected-category-slug="selectedCategorySlug"
    :total-topics="totalTopics"
    :pending="pending"
    :can-create-topic="can(FORUM_PERMISSIONS.topicCreate)"
    navigation-mode="route"
    @navigate="emit('navigate')"
  />
</template>
