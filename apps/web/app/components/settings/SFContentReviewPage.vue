<script setup lang="ts">
import { apiErrorMessage } from '~/composables/useApiClient'
import { useAuthorContentReview } from '~/composables/moderation/useAuthorContentReview'
import {
  authorReviewReviewNote,
  authorReviewSections,
  authorReviewTargetPath,
  type AuthorReviewItem
} from '~/utils/moderation/authorReview'
import SFSettingsShell from '~/components/settings/SFSettingsShell.vue'

/**
 * 作者侧「我的内容」：待审与被退回两个列表。
 *
 * 数据只有 `GET /me/content-review` 一个来源（登录态、只读）。这里不引入
 * 撤审/重提按钮：后端没有这两个动作，前端造出来只会变成假承诺。
 *
 * 呈现契约：
 * - 两个列表按 status 分桶，各自带计数与空态，永远同时可见（不用 tab 藏一半）；
 * - 被退回项一定渲染「审核意见」块：有意见显示原文，没有意见显示明确说明，
 *   不留空白（空白的审核意见在作者眼里等于「没有理由」）。
 */
const { t } = useI18n()
const localePath = useLocalePath()
const reviewApi = useAuthorContentReview()
const { format } = useSiteDateTime()

const { data, pending, error, refresh } = await useAsyncData(
  'author-content-review',
  async () => {
    const response = await reviewApi.list()
    return Array.isArray(response?.items) ? response.items : []
  },
  { default: () => [] as AuthorReviewItem[] }
)

const sections = computed(() => authorReviewSections(data.value))
const total = computed(() => sections.value.pending.length + sections.value.rejected.length)
const errorMessage = computed(() => (error.value ? apiErrorMessage(error.value) || t('moderation.authorStatus.loadFailed') : ''))

function targetPath(item: AuthorReviewItem) {
  const path = authorReviewTargetPath(item)
  return path ? localePath(path) : ''
}

function submittedAt(item: AuthorReviewItem) {
  return item.createdAt ? format(item.createdAt) : ''
}

function goCreateTopic() {
  return navigateTo(localePath('/topics/new'))
}
</script>

<template>
  <SFSettingsShell
    class="sforum-settings-content-review"
    active="content"
    title-id="content-review-title"
    :title="t('moderation.authorStatus.title')"
    :description="t('moderation.authorStatus.description')"
    :rail-label="t('moderation.authorStatus.railLabel')"
  >
    <template #head-actions>
      <SFButton variant="secondary" size="sm" :loading="pending" @click="refresh()">
        <UIcon name="i-lucide-refresh-cw" class="mr-1" />
        {{ t('moderation.authorStatus.refresh') }}
      </SFButton>
    </template>

    <UAlert
      v-if="errorMessage"
      class="mt-2"
      color="error"
      variant="soft"
      icon="i-lucide-triangle-alert"
      :title="errorMessage"
    />

    <div v-if="pending && total === 0" class="mt-5 space-y-3" aria-busy="true">
      <SFSkeleton v-for="index in 3" :key="index" class="h-24 w-full" />
    </div>

    <template v-else>
      <section
        class="mt-5"
        aria-labelledby="content-review-pending-heading"
        data-sforum-review-section="pending"
      >
        <h2 id="content-review-pending-heading" class="flex items-center gap-2 text-base font-semibold">
          {{ t('moderation.authorStatus.pending') }}
          <UBadge color="neutral" variant="soft" data-sforum-review-count>{{ sections.pending.length }}</UBadge>
        </h2>
        <p class="mt-1 text-sm text-muted">
          {{ t('moderation.authorStatus.pendingHint') }}
        </p>

        <ul v-if="sections.pending.length" class="mt-3 space-y-3">
          <li
            v-for="item in sections.pending"
            :key="`pending-${item.targetType}-${item.targetId}`"
            class="rounded-md border border-default p-4"
          >
            <div class="flex flex-wrap items-center gap-2 text-xs text-muted">
              <UBadge color="warning" variant="soft">
                {{ t('moderation.authorStatus.pending') }}
              </UBadge>
              <span>{{ item.targetType === 'comment' ? t('moderation.authorStatus.targetComment') : t('moderation.authorStatus.targetTopic') }}</span>
              <span v-if="submittedAt(item)">· {{ t('moderation.authorStatus.submittedAt', { date: submittedAt(item) }) }}</span>
            </div>
            <NuxtLink
              v-if="targetPath(item)"
              :to="targetPath(item)"
              class="mt-2 block font-medium underline-offset-2 hover:underline"
            >
              {{ item.title || t('moderation.authorStatus.untitled') }}
            </NuxtLink>
            <p v-else class="mt-2 font-medium">
              {{ item.title || t('moderation.authorStatus.untitled') }}
            </p>
            <p v-if="item.excerpt" class="mt-1 line-clamp-3 text-sm text-muted">
              {{ item.excerpt }}
            </p>
          </li>
        </ul>

        <SFEmptyState
          v-else
          class="mt-3"
          icon="i-lucide-clock"
          :title="t('moderation.authorStatus.pendingEmptyTitle')"
          :description="t('moderation.authorStatus.pendingEmptyDescription')"
        />
      </section>

      <section
        class="mt-8"
        aria-labelledby="content-review-rejected-heading"
        data-sforum-review-section="rejected"
      >
        <h2 id="content-review-rejected-heading" class="flex items-center gap-2 text-base font-semibold">
          {{ t('moderation.authorStatus.rejected') }}
          <UBadge color="neutral" variant="soft" data-sforum-review-count>{{ sections.rejected.length }}</UBadge>
        </h2>
        <p class="mt-1 text-sm text-muted">
          {{ t('moderation.authorStatus.rejectedHint') }}
        </p>

        <ul v-if="sections.rejected.length" class="mt-3 space-y-3">
          <li
            v-for="item in sections.rejected"
            :key="`rejected-${item.targetType}-${item.targetId}`"
            class="rounded-md border border-default p-4"
          >
            <div class="flex flex-wrap items-center gap-2 text-xs text-muted">
              <UBadge color="error" variant="soft">
                {{ t('moderation.authorStatus.rejected') }}
              </UBadge>
              <span>{{ item.targetType === 'comment' ? t('moderation.authorStatus.targetComment') : t('moderation.authorStatus.targetTopic') }}</span>
              <span v-if="submittedAt(item)">· {{ t('moderation.authorStatus.submittedAt', { date: submittedAt(item) }) }}</span>
            </div>
            <NuxtLink
              v-if="targetPath(item)"
              :to="targetPath(item)"
              class="mt-2 block font-medium underline-offset-2 hover:underline"
            >
              {{ item.title || t('moderation.authorStatus.untitled') }}
            </NuxtLink>
            <p v-else class="mt-2 font-medium">
              {{ item.title || t('moderation.authorStatus.untitled') }}
            </p>
            <p v-if="item.excerpt" class="mt-1 line-clamp-3 text-sm text-muted">
              {{ item.excerpt }}
            </p>

            <div class="mt-3 rounded-md bg-elevated/60 p-3" data-sforum-review-note>
              <p class="text-xs font-medium text-muted">
                {{ t('moderation.authorStatus.reason') }}
              </p>
              <p class="mt-1 whitespace-pre-line text-sm">
                {{ authorReviewReviewNote(item) || t('moderation.authorStatus.reviewNoteEmpty') }}
              </p>
            </div>
          </li>
        </ul>

        <SFEmptyState
          v-else
          class="mt-3"
          icon="i-lucide-shield-check"
          :title="t('moderation.authorStatus.rejectedEmptyTitle')"
          :description="t('moderation.authorStatus.rejectedEmptyDescription')"
        />
      </section>

      <SFEmptyState
        v-if="total === 0"
        class="mt-8"
        icon="i-lucide-pen-line"
        :title="t('moderation.authorStatus.emptyTitle')"
        :description="t('moderation.authorStatus.emptyDescription')"
        :action-label="t('moderation.authorStatus.createAction')"
        @action="goCreateTopic"
      />
    </template>
  </SFSettingsShell>
</template>
