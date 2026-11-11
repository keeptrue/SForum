<script setup lang="ts">
import { forumCategoryPath, taxonomyIconColor, type ForumCategory } from '~/utils/forum/forumTaxonomy'
import { limitDynamicNavigationItems } from '~/utils/navigation/publicNavigation'

const props = withDefaults(defineProps<{
  categories?: ForumCategory[]
  selectedCategorySlug?: string
  label: string
  maxItems?: number
  pending?: boolean
  showCounts?: boolean
  navigationMode?: 'filter' | 'route'
}>(), {
  categories: () => [],
  selectedCategorySlug: '',
  maxItems: 0,
  pending: false,
  showCounts: true,
  navigationMode: 'route'
})

const emit = defineEmits<{
  'select-category': [slug: string]
  navigate: []
}>()

const { t } = useI18n()
const localePath = useLocalePath()
const visibleCategories = computed(() => limitDynamicNavigationItems(
  props.categories,
  props.maxItems,
  props.selectedCategorySlug
))
const hasHiddenCategories = computed(() => visibleCategories.value.length < props.categories.length)
const showSkeleton = computed(() => props.pending && props.categories.length === 0)

function categoryTo(slug: string) {
  return localePath(forumCategoryPath(slug))
}

function categoryIconColor(category: ForumCategory) {
  return taxonomyIconColor(category.iconColor)
}

function categoryIconName(category: ForumCategory) {
  const icon = category.icon?.trim() || ''
  return icon.startsWith('i-') ? icon : 'i-lucide-folder'
}
</script>

<template>
  <div class="sf-category-navigation-block">
    <div class="sf-home-navigation__label">{{ label }}</div>
    <div v-if="showSkeleton" class="sf-home-navigation__pending">
      <SFSkeleton v-for="skeleton in 4" :key="skeleton" :lines="1" />
    </div>
    <template v-if="navigationMode === 'route'">
      <NuxtLink
        v-for="category in visibleCategories"
        :key="category.slug"
        :to="categoryTo(category.slug)"
        class="sf-home-navigation__link"
        :class="{ 'is-active': selectedCategorySlug === category.slug }"
        @click="emit('navigate')"
      >
        <span class="sf-home-navigation__link-main">
          <span class="sf-home-navigation__cat-icon" :style="{ color: categoryIconColor(category) }" aria-hidden="true">
            <UIcon :name="categoryIconName(category)" class="size-[18px]" />
          </span>
          {{ category.name }}
        </span>
        <span v-if="showCounts" class="sf-home-navigation__count">{{ category.topicCount }}</span>
      </NuxtLink>
    </template>
    <template v-else>
      <button
        v-for="category in visibleCategories"
        :key="category.slug"
        type="button"
        class="sf-home-navigation__link"
        :class="{ 'is-active': selectedCategorySlug === category.slug }"
        :aria-pressed="selectedCategorySlug === category.slug"
        @click="emit('select-category', category.slug)"
      >
        <span class="sf-home-navigation__link-main">
          <span class="sf-home-navigation__cat-icon" :style="{ color: categoryIconColor(category) }" aria-hidden="true">
            <UIcon :name="categoryIconName(category)" class="size-[18px]" />
          </span>
          {{ category.name }}
        </span>
        <span v-if="showCounts" class="sf-home-navigation__count">{{ category.topicCount }}</span>
      </button>
    </template>
    <NuxtLink
      v-if="hasHiddenCategories"
      :to="localePath('/categories')"
      class="sf-home-navigation__more"
      @click="emit('navigate')"
    >
      {{ t('home.sidebar.viewAllCategories') }}
      <UIcon name="i-lucide-arrow-right" class="size-4" aria-hidden="true" />
    </NuxtLink>
  </div>
</template>
