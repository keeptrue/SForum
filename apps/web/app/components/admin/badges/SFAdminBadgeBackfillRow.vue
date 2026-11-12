<script setup lang="ts">
/**
 * 回扫进度行：**一行**紧凑状态，不是卡片、不是第二个页头。
 *
 * 放在 badges 分区筛选区上方，回答「后台回扫跑到哪了」——`recompute` 返回
 * `queued > 0` 之后运营看到的唯一进度来源（计划 §13#15）。
 * 文案全部由 `backfillProgressCopy` 决定，这里只负责配色/图标/摆位。
 */
import { computed } from 'vue'
import { backfillProgressCopy, type AdminBadgeBackfillSummary } from '~/utils/admin/adminBadges'

const props = withDefaults(defineProps<{
  /** `null` = 没有要展示的 run（无 run / completed 已过期），此时整行不渲染。 */
  summary?: AdminBadgeBackfillSummary | null
  /** 端点失败：空清单 + 一条可见提示，能刷新重试。 */
  failed?: boolean
  pending?: boolean
}>(), {
  summary: null,
  failed: false,
  pending: false
})

const emit = defineEmits<{ refresh: [] }>()

const { t } = useI18n()

const copy = computed(() => (props.summary ? backfillProgressCopy(props.summary, t) : null))

// 端点失败用 warning（可能只是接口还没上线），run 失败用 error（这一链真的卡住了）。
const tone = computed(() => {
  if (props.failed) return 'warning'
  if (props.summary?.status === 'failed') return 'error'
  return props.summary?.status === 'completed' ? 'success' : 'info'
})

const icon = computed(() => {
  if (props.failed) return 'i-tabler-cloud-off'
  if (props.summary?.status === 'failed') return 'i-tabler-alert-triangle'
  return props.summary?.status === 'completed' ? 'i-tabler-circle-check' : 'i-tabler-progress'
})

const refreshLabel = computed(() => t('admin.badges.backfill.refresh'))

defineOptions({ name: 'SFAdminBadgeBackfillRow' })
</script>

<template>
  <!-- 稳定选择器：老运的 390 验收按它定位这一行，改名要同步回报。 -->
  <div v-if="failed || summary" class="mb-3 flex min-w-0 items-center gap-2" data-testid="badge-backfill-progress">
    <UAlert
      class="min-w-0 flex-1"
      :color="tone"
      variant="soft"
      :icon="icon"
      :title="failed ? undefined : copy?.title"
      :description="failed ? t('admin.badges.backfill.unavailable') : copy?.description"
    />
    <UButton
      color="neutral"
      variant="ghost"
      icon="i-tabler-refresh"
      :loading="pending"
      :aria-label="refreshLabel"
      :title="refreshLabel"
      class="shrink-0"
      @click="emit('refresh')"
    />
  </div>
</template>
