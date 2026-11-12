<script setup lang="ts">
/**
 * 批量授予弹窗：搜索多选用户 → 一次提交 → 弹窗内展示三态结果。
 *
 * 三态是后端契约，不能合并成一个「成功」Toast：
 * - `granted`：本次真的授予
 * - `skipped`：已持有（幂等命中，不是错误）
 * - `failed`：逐条失败原因，可只重试这一批
 *
 * 布局：body 是唯一滚动区，footer 吸底 —— 失败名单可能很长，按钮不能被滚出视口。
 * 自动徽章不允许手动授予（后端 422 `forum.badge_manual_only`），这里提前禁用。
 */
import type { AdminBadgesStore, AdminBadgeBatchGrantResult, AdminBadgeGrantCandidate } from '~/composables/admin/badges/useAdminBadges'
import type { AdminBadgeRow } from '~/utils/admin/adminBadges'
import SFAdminBadgeUserPicker from '~/components/admin/badges/SFAdminBadgeUserPicker.vue'
import { apiErrorFields, apiErrorMessage } from '~/composables/useApiClient'

const props = defineProps<{
  badge: AdminBadgeRow | null
  store: AdminBadgesStore
}>()
const emit = defineEmits<{ saved: [] }>()

const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const toast = useToast()

const selected = ref<AdminBadgeGrantCandidate[]>([])
const reason = ref('')
const submitting = ref(false)
const errorMessage = ref('')
const fieldErrors = ref<Record<string, string[]>>({})
const result = ref<AdminBadgeBatchGrantResult | null>(null)

const manual = computed(() => props.badge?.grantMode === 'manual')
const canSubmit = computed(() =>
  Boolean(props.badge) && manual.value && selected.value.length > 0 && !submitting.value
)
const failed = computed(() => result.value?.failed ?? [])

watch(open, (value) => {
  if (!value) return
  selected.value = []
  reason.value = ''
  errorMessage.value = ''
  fieldErrors.value = {}
  result.value = null
})

function close() {
  open.value = false
}

function search(q: string, badgeId: number | null) {
  return props.store.searchGrantCandidates(q, badgeId)
}

async function submit(userIds?: number[]) {
  if (!props.badge || submitting.value) return
  const ids = userIds ?? selected.value.map(item => item.userId)
  if (ids.length === 0) return
  submitting.value = true
  errorMessage.value = ''
  fieldErrors.value = {}
  try {
    const batch = await props.store.batchGrant(props.badge.id, { userIds: ids, reason: reason.value })
    // 重试只跑失败项时，把两次结果累加：否则「重试成功」会把上一轮的 granted 冲掉。
    result.value = result.value && userIds
      ? {
          granted: result.value.granted + batch.granted,
          skipped: result.value.skipped + batch.skipped,
          failed: batch.failed
        }
      : batch
    emit('saved')
    if (batch.granted > 0) {
      toast.add({ title: t('admin.badges.toast.granted'), color: 'success', icon: 'i-lucide-award', duration: 5000 })
    }
  } catch (error) {
    const fields = apiErrorFields(error)
    fieldErrors.value = fields
    if (Object.keys(fields).length === 0) {
      errorMessage.value = apiErrorMessage(error) || t('admin.badges.grant.failed')
    }
  } finally {
    submitting.value = false
  }
}

/** 重试失败项：失败行只有 userId/username，补成候选形状即可复用同一端点。 */
function retryFailed() {
  const ids = failed.value.map(item => item.userId)
  if (ids.length === 0) return
  void submit(ids)
}
</script>

<template>
  <UModal v-model:open="open" :ui="{ content: 'sm:max-w-lg' }" @update:open="(value) => { if (!value) close() }">
    <template #content>
      <div class="flex max-h-[90vh] flex-col">
        <div class="border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ t('admin.badges.grant.title') }}</h3>
          <p v-if="badge" class="mt-1 text-xs text-slate-500 dark:text-zinc-400">{{ badge.name }}</p>
        </div>

        <div class="min-h-0 flex-1 space-y-3 overflow-y-auto px-5 py-4">
          <template v-if="!result">
            <!-- 自动徽章：不给一组「看得见点不动」的禁用控件，直接说明为什么不能授予。 -->
            <template v-if="manual">
              <UFormField :label="t('admin.badges.grant.user')" :error="fieldErrors.userIds?.[0] || fieldErrors.usernames?.[0]">
                <SFAdminBadgeUserPicker
                  v-model="selected"
                  :badge-id="badge?.id ?? null"
                  :search="search"
                  :placeholder="t('admin.badges.grant.userPlaceholder')"
                  :empty-label="t('admin.badges.grant.search.empty')"
                  :no-result-label="t('admin.badges.grant.search.noResult')"
                  :already-label="t('admin.badges.grant.search.already')"
                  :remove-label="t('admin.badges.grant.search.remove')"
                  :max-label="t('admin.badges.grant.search.max')"
                />
              </UFormField>

              <UFormField :label="t('admin.badges.grant.reason')" :error="fieldErrors.reason?.[0]">
                <UTextarea v-model="reason" :rows="2" class="w-full" :placeholder="t('admin.badges.grant.reasonPlaceholder')" />
              </UFormField>
            </template>

            <p v-if="!manual" class="text-sm text-amber-600 dark:text-amber-400">{{ t('admin.badges.grant.automaticNotGrantable') }}</p>
            <p v-if="errorMessage" class="text-sm text-red-600 dark:text-red-400">{{ errorMessage }}</p>
          </template>

          <!-- 结果区：三态分别计数，失败项逐条列出原因（不能只显示「部分失败」）。 -->
          <div v-else class="space-y-3" role="status">
            <dl class="grid grid-cols-3 gap-2 text-center">
              <div class="rounded-lg border border-slate-200 px-2 py-2 dark:border-zinc-800">
                <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.badges.grant.result.granted') }}</dt>
                <dd class="text-lg font-bold text-slate-900 dark:text-white">{{ result.granted }}</dd>
              </div>
              <div class="rounded-lg border border-slate-200 px-2 py-2 dark:border-zinc-800">
                <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.badges.grant.result.skipped') }}</dt>
                <dd class="text-lg font-bold text-slate-900 dark:text-white">{{ result.skipped }}</dd>
              </div>
              <div class="rounded-lg border border-slate-200 px-2 py-2 dark:border-zinc-800">
                <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.badges.grant.result.failed') }}</dt>
                <dd class="text-lg font-bold text-slate-900 dark:text-white">{{ result.failed.length }}</dd>
              </div>
            </dl>

            <div v-if="result.failed.length > 0">
              <p class="mb-1 text-xs font-medium text-slate-600 dark:text-zinc-300">{{ t('admin.badges.grant.result.failedTitle') }}</p>
              <ul class="space-y-1">
                <li
                  v-for="item in result.failed"
                  :key="item.userId"
                  class="flex flex-wrap items-baseline gap-x-2 text-xs text-slate-600 dark:text-zinc-300"
                >
                  <span class="font-medium">{{ item.username }}</span>
                  <span class="text-slate-500 dark:text-zinc-400">{{ item.reason }}</span>
                </li>
              </ul>
            </div>
          </div>
        </div>

        <div class="flex flex-wrap items-center justify-end gap-2 border-t border-slate-200 px-5 py-3 dark:border-zinc-800">
          <template v-if="result">
            <UButton
              v-if="failed.length > 0"
              color="neutral"
              variant="outline"
              icon="i-lucide-rotate-cw"
              :loading="submitting"
              @click="retryFailed"
            >
              {{ t('admin.badges.grant.result.retry') }}
            </UButton>
            <UButton color="primary" @click="close">{{ t('admin.badges.grant.result.done') }}</UButton>
          </template>
          <template v-else>
            <UButton color="neutral" variant="ghost" @click="close">{{ t('admin.badges.actions.cancel') }}</UButton>
            <UButton v-if="manual" color="primary" :loading="submitting" :disabled="!canSubmit" @click="submit()">
              {{ t('admin.badges.grant.submit') }}
            </UButton>
          </template>
        </div>
      </div>
    </template>
  </UModal>
</template>
