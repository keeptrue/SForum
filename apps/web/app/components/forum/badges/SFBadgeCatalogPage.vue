<script setup lang="ts">
import SFHomeNavigation from '~/components/forum/SFHomeNavigation.vue'
import SFResponsivePublicSidebar from '~/components/forum/navigation/SFResponsivePublicSidebar.vue'
import SFProfileBadgeTab from '~/components/profile/SFProfileBadgeTab.vue'
import SFPublicPageHeader from '~/components/public/SFPublicPageHeader.vue'
import { useBadgesCatalog } from '~/composables/forum/badges/useBadgesCatalog'
import { useBadgeDetailQuery } from '~/composables/profile/useBadgeDetailQuery'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useForumApi } from '~/composables/forum/useForumApi'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import type { ForumCategoryGroup } from '~/utils/forum/forumTaxonomy'
/**
 * 宿主 body 岛：forum.badge.index。
 * 两栏壳对齐标签 / 分类目录（本页不启用右栏）；卡片复用 SFProfileBadgeTab 的 catalog 变体，
 * 不在这里再造一份卡片实现。公共 chrome（navbar / footer）由主题模板挂载，岛不自带。
 *
 * `?badge=<slug>` 深链由 useBadgeDetailQuery 拥有：目录页是「分享某一枚徽章」的落地页，
 * 所以选中项必须写回地址栏；清单里没有的 slug 静默清掉（不弹空弹窗）。
 */
const { t } = useI18n()
const { user } = useAuthSession()
const forumApi = useForumApi()
const { can } = usePermissions()
const { groups, truncated, pending, error, refresh } = await useBadgesCatalog()
const { focusSlug, setFocusSlug, resetBadgeIfMissing } = useBadgeDetailQuery()

/**
 * 左栏与个人主页（forum.profile.show）同源：导航 + 分类清单 + 总主题数 + 新建主题入口。
 * SSR 取数；失败只让分类块退化成骨架/空，不阻断徽章目录本身（default 空数组）。
 */
const { data: categoryGroups, pending: categoriesPending } = await useAsyncData(
  'forum-badges-category-groups',
  () => forumApi.listCategoryGroups(),
  { default: () => [] as ForumCategoryGroup[] }
)
const navCategories = computed(() => categoryGroups.value.flatMap(group => group.categories || []))
const navTotalTopics = computed(() => navCategories.value.reduce((sum, category) => sum + category.topicCount, 0))
const canCreateTopic = computed(() => can(FORUM_PERMISSIONS.topicCreate))

const totalBadges = computed(() => groups.value.reduce((sum, group) => sum + group.badges.length, 0))
const badgeSlugs = computed(() => groups.value.flatMap(group => group.badges.map(badge => badge.slug)))

// 取数完成（且没失败）之后才判断「这个 slug 还在不在」：加载中/失败/还没请求时清单不可信。
const badgesLoaded = computed(() => !pending.value && !error.value)
watch([badgeSlugs, badgesLoaded, error], ([slugs, loaded, failed]) => {
  if (!loaded || failed) return
  resetBadgeIfMissing(slugs)
}, { immediate: true })
</script>

<template>
  <main
    class="sforum-home sforum-badges-catalog"
    data-sforum-island-body="forum.component.badge_index"
    data-layout="fullwidth-2col"
    :data-truncated="truncated ? 'true' : 'false'"
  >
    <div class="sforum-home__layout">
      <SFResponsivePublicSidebar
        owner-id="forum.badge.index"
        :title="t('home.sidebar.drawerTitle')"
        class="sforum-home__sidebar"
      >
        <SFHomeNavigation
          :categories="navCategories"
          :total-topics="navTotalTopics"
          :pending="categoriesPending"
          :can-create-topic="canCreateTopic"
          navigation-mode="route"
          desktop-only
        />
      </SFResponsivePublicSidebar>

      <section
        class="sforum-home__main sforum-badges-catalog__main"
        aria-labelledby="badges-catalog-title"
      >
        <SFPublicPageHeader
          class="sforum-badges-catalog__head"
          title-id="badges-catalog-title"
          :title="t('badges.catalog.title')"
          :subtitle="t('badges.catalog.description')"
        >
          <template #aside>
            <!-- 总数来自同一个请求：失败时整块隐藏，不显示 0 冒充成功。 -->
            <div v-if="!pending && !error" class="sforum-badges-catalog__head-count">
              <strong>{{ totalBadges }}</strong>
              <span>{{ t('badges.catalog.headerCount') }}</span>
            </div>
          </template>
        </SFPublicPageHeader>

        <p v-if="!user" class="sforum-badges-catalog__hint">
          {{ t('badges.catalog.anonymousHint') }}
        </p>

        <!-- 截断是整份响应的属性：说清「不是全部」，别让用户以为站点只有这些。 -->
        <p v-if="truncated" class="sforum-badges-catalog__truncated" role="status">
          {{ t('badges.catalog.truncated') }}
        </p>

        <SFProfileBadgeTab
          variant="catalog"
          :groups="groups"
          :loading="pending"
          :failed="Boolean(error)"
          :loaded="badgesLoaded"
          :focus-slug="focusSlug"
          @retry="refresh"
          @update:focus-slug="setFocusSlug"
        />
      </section>
    </div>
  </main>
</template>

<style scoped>
.sforum-badges-catalog__main {
  min-width: 0;
}

.sforum-badges-catalog__head-count {
  display: flex;
  align-items: baseline;
  gap: 4px;
  color: var(--sf-fg-tertiary);
  font-size: 12px;
}

.sforum-badges-catalog__head-count strong {
  color: var(--sf-fg);
  font-size: 18px;
  font-variant-numeric: tabular-nums;
  font-weight: 700;
}

.sforum-badges-catalog__hint {
  margin: 6px 0 0;
  color: var(--sf-fg-secondary);
  font-size: 12px;
}

.sforum-badges-catalog__truncated {
  margin: 10px 0 0;
  color: var(--sf-fg-secondary);
  font-size: 12px;
}
</style>
