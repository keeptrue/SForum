<script setup lang="ts">
import type { AdminBadgeGroupRow } from '~/composables/admin/badges/useAdminBadges'
import {
  BADGE_ICON_CHOICES,
  BADGE_METRIC_OPTIONS,
  toAdminBadgeInput,
  type AdminBadgeRow
} from '~/utils/admin/adminBadges'
import { forumBadgeTier } from '~/utils/forum/forumBadge'
import { apiErrorFields, apiErrorMessage } from '~/composables/useApiClient'

/**
 * 新建 / 编辑徽章弹窗。
 *
 * 自己负责校验与提交（与用户创建弹窗同一分工），成功后 emit `saved` 让页面重拉列表。
 * 删除走两步确认，不嵌套第二个 Modal：第二次点击才真正发 DELETE。
 */
const props = defineProps<{
  groups: AdminBadgeGroupRow[]
  badge: AdminBadgeRow | null
}>()

const emit = defineEmits<{ saved: [] }>()

const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const { request } = useApiClient()
const toast = useToast()

const form = reactive({
  slug: '',
  name: '',
  description: '',
  groupId: null as number | null,
  tier: 'bronze' as 'bronze' | 'silver' | 'gold',
  icon: 'i-lucide-medal',
  grantMode: 'manual' as 'automatic' | 'manual',
  metric: 'topic_count',
  threshold: 1 as number | null,
  enabled: true,
  position: 0
})

const submitting = ref(false)
const deleting = ref(false)
const confirmDelete = ref(false)
const errorMessage = ref('')
const fieldErrors = ref<Record<string, string[]>>({})

const isEdit = computed(() => Boolean(props.badge?.id))
const title = computed(() => (isEdit.value ? t('admin.badges.form.editTitle') : t('admin.badges.form.createTitle')))
const automatic = computed(() => form.grantMode === 'automatic')

const groupItems = computed(() => [
  { value: null as number | null, label: '—' },
  ...props.groups.map(group => ({ value: group.id as number | null, label: group.name }))
])

const tierItems = computed(() => (['gold', 'silver', 'bronze'] as const).map(tier => ({
  value: tier,
  label: t(`badges.tier.${tier}`)
})))

const grantModeItems = computed(() => (['automatic', 'manual'] as const).map(mode => ({
  value: mode,
  label: t(`admin.badges.grantMode.${mode}`)
})))

const metricItems = computed(() => BADGE_METRIC_OPTIONS.map(metric => ({
  value: metric,
  label: t(`admin.badges.metrics.${metric}`)
})))

function selectIcon(choice: string) {
  form.icon = choice
}

const canSubmit = computed(() => {
  if (submitting.value || deleting.value) return false
  if (!form.slug.trim() || !form.name.trim()) return false
  if (automatic.value && (!form.metric || !form.threshold || form.threshold <= 0)) return false
  return true
})

watch(open, (value) => {
  if (!value) return
  errorMessage.value = ''
  fieldErrors.value = {}
  confirmDelete.value = false
  const badge = props.badge
  form.slug = badge?.slug ?? ''
  form.name = badge?.name ?? ''
  form.description = badge?.description ?? ''
  form.groupId = badge?.groupId ?? null
  form.tier = forumBadgeTier(badge?.tier)
  form.icon = badge?.icon || 'i-lucide-medal'
  form.grantMode = badge?.grantMode ?? 'manual'
  form.metric = badge?.metric ?? 'topic_count'
  form.threshold = badge?.threshold ?? 1
  form.enabled = badge?.enabled ?? true
  form.position = badge?.position ?? 0
})

function close() {
  open.value = false
}

async function submit() {
  if (!canSubmit.value) return
  errorMessage.value = ''
  fieldErrors.value = {}
  submitting.value = true
  try {
    const body = toAdminBadgeInput({
      ...form,
      tier: forumBadgeTier(form.tier),
      metric: automatic.value ? form.metric : null,
      threshold: automatic.value ? form.threshold : null
    })
    if (isEdit.value && props.badge) {
      await request(`/admin/badges/${props.badge.id}`, { method: 'PATCH', body })
      toast.add({ title: t('admin.badges.toast.updated'), color: 'success', icon: 'i-lucide-check', duration: 5000 })
    } else {
      await request('/admin/badges', { method: 'POST', body })
      toast.add({ title: t('admin.badges.toast.created'), color: 'success', icon: 'i-lucide-check', duration: 5000 })
    }
    emit('saved')
    close()
  } catch (error) {
    const fields = apiErrorFields(error)
    fieldErrors.value = fields
    if (Object.keys(fields).length === 0) {
      errorMessage.value = apiErrorMessage(error) || t('admin.badges.form.saveFailed')
    }
  } finally {
    submitting.value = false
  }
}

async function removeBadge() {
  if (!props.badge || deleting.value) return
  if (!confirmDelete.value) {
    confirmDelete.value = true
    return
  }
  deleting.value = true
  errorMessage.value = ''
  try {
    await request(`/admin/badges/${props.badge.id}`, { method: 'DELETE' })
    // 删除把 top_badge_id 置空（外键 SET NULL）且不会自动挑次高级：提示重算，
    // 否则用户帖子旁的徽章 chip 会消失到下一次评估/重算（失真窗口 #2）。
    toast.add({
      title: t('admin.badges.toast.deleted'),
      description: t('admin.badges.toast.deletedRecomputeHint'),
      color: 'success',
      icon: 'i-lucide-trash-2',
      duration: 5000
    })
    emit('saved')
    close()
  } catch (error) {
    errorMessage.value = apiErrorMessage(error) || t('admin.badges.toast.deleteFailed')
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" :ui="{ content: 'sm:max-w-xl' }" @update:open="(value) => { if (!value) close() }">
    <template #content>
      <div class="flex max-h-[90vh] flex-col">
        <div class="border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ title }}</h3>
        </div>

        <div class="min-h-0 flex-1 space-y-3 overflow-y-auto px-5 py-4">
          <UFormField :label="t('admin.badges.form.slug')" :error="fieldErrors.slug?.[0]">
            <UInput v-model="form.slug" :disabled="isEdit" class="w-full" />
            <template #hint>
              <span class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.badges.form.slugHint') }}</span>
            </template>
          </UFormField>

          <UFormField :label="t('admin.badges.form.name')" :error="fieldErrors.name?.[0]">
            <UInput v-model="form.name" class="w-full" />
          </UFormField>

          <UFormField :label="t('admin.badges.form.description')" :error="fieldErrors.description?.[0]">
            <UTextarea v-model="form.description" :rows="2" class="w-full" />
          </UFormField>

          <div class="grid gap-3 sm:grid-cols-2">
            <UFormField :label="t('admin.badges.form.group')" :error="fieldErrors.groupId?.[0]">
              <USelect v-model="form.groupId" :items="groupItems" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.badges.form.tier')" :error="fieldErrors.tier?.[0]">
              <USelect v-model="form.tier" :items="tierItems" class="w-full" />
            </UFormField>
          </div>

          <UFormField :label="t('admin.badges.form.icon')" :error="fieldErrors.icon?.[0]">
            <div class="grid grid-cols-8 gap-2 sm:grid-cols-8">
              <UButton
                v-for="choice in BADGE_ICON_CHOICES"
                :key="choice"
                type="button"
                size="xs"
                :color="form.icon === choice ? 'primary' : 'neutral'"
                :variant="form.icon === choice ? 'solid' : 'outline'"
                :icon="choice"
                :aria-label="choice"
                :title="choice"
                class="justify-center"
                @click="selectIcon(choice)"
              />
            </div>
          </UFormField>

          <div class="grid gap-3 sm:grid-cols-2">
            <UFormField :label="t('admin.badges.form.grantMode')" :error="fieldErrors.grantMode?.[0]">
              <USelect v-model="form.grantMode" :items="grantModeItems" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.badges.form.enabled')">
              <USwitch v-model="form.enabled" :aria-label="t('admin.badges.form.enabled')" />
            </UFormField>
          </div>

          <div v-if="automatic" class="grid gap-3 sm:grid-cols-2">
            <UFormField :label="t('admin.badges.form.metric')" :error="fieldErrors.metric?.[0]">
              <USelect v-model="form.metric" :items="metricItems" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.badges.form.threshold')" :error="fieldErrors.threshold?.[0]">
              <UInput v-model.number="form.threshold" type="number" min="1" class="w-full" />
            </UFormField>
          </div>

          <p v-if="errorMessage" class="text-sm text-red-600 dark:text-red-400">{{ errorMessage }}</p>
          <p v-if="confirmDelete" class="text-sm text-red-600 dark:text-red-400">
            {{ t('admin.badges.confirmDeleteBody') }}
          </p>
          <!-- 两步确认第二步追加失真提示：删除清空 top_badge_id，chip 消失到重算为止。 -->
          <p v-if="confirmDelete" class="text-sm text-amber-600 dark:text-amber-400">
            {{ t('admin.badges.confirmDeleteTopBadgeHint') }}
          </p>
        </div>

        <div class="flex flex-wrap items-center justify-between gap-2 border-t border-slate-200 px-5 py-3 dark:border-zinc-800">
          <UButton
            v-if="isEdit"
            color="error"
            variant="ghost"
            icon="i-lucide-trash-2"
            :loading="deleting"
            @click="removeBadge"
          >
            {{ confirmDelete ? t('admin.badges.confirmDeleteTitle') : t('admin.badges.actions.delete') }}
          </UButton>
          <span v-else />

          <div class="flex items-center gap-2">
            <UButton color="neutral" variant="ghost" @click="close">{{ t('admin.badges.actions.cancel') }}</UButton>
            <UButton color="primary" :loading="submitting" :disabled="!canSubmit" @click="submit">
              {{ t('admin.badges.actions.save') }}
            </UButton>
          </div>
        </div>
      </div>
    </template>
  </UModal>
</template>
