<script setup lang="ts">
import SFHomeNavigation from '~/components/forum/SFHomeNavigation.vue'
import SFResponsivePublicSidebar from '~/components/forum/navigation/SFResponsivePublicSidebar.vue'
import SFProfileBadgeTab from '~/components/profile/SFProfileBadgeTab.vue'
import SFPublicPageHeader from '~/components/public/SFPublicPageHeader.vue'
import { useBadgesCatalog } from '~/composables/forum/badges/useBadgesCatalog'
import { useAuthSession } from '~/composables/identity/useAuthSession'
/**
 * 宿主 body 岛：forum.badge.index。
 * 两栏壳对齐标签 / 分类目录（本页不启用右栏）；卡片复用 SFProfileBadgeTab 的 catalog 变体，
 * 不在这里再造一份卡片实现。公共 chrome（navbar / footer）由主题模板挂载，岛不自带。
 */
const { t } = useI18n()
const { user } = useAuthSession()
const { groups, truncated, pending, error, refresh } = await useBadgesCatalog()

const totalBadges = computed(() => groups.value.reduce((sum, group) => sum + group.badges.length, 0))
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
          desktop-only
          navigation-mode="route"
          :show-categories="false"
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
          @retry="refresh"
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
