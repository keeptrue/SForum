<script setup lang="ts">
/**
 * 删除徽章等级：两步确认 + **必须**选择迁移目标。
 *
 * 删除等级不是「删掉一个标签」：该档下的徽章会被改挂到迁移目标（后端 `?migrateTo=`）。
 * 所以这里不允许空目标 —— 没有其它启用等级时直接禁止删除，并说明原因（先启用或新建一个）。
 */
import type { AdminBadgesStore, AdminBadgeTierRow } from '~/composables/admin/badges/useAdminBadges'
import { apiErrorMessage } from '~/composables/useApiClient'

const props = defineProps<{
  tier: AdminBadgeTierRow | null
  /** 迁移目标候选：只列**其它启用中**的等级。 */
  targets: AdminBadgeTierRow[]
  store: AdminBadgesStore
}>()

const emit = defineEmits<{ deleted: [] }>()
const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const toast = useToast()

const migrateTo = ref('')
const confirming = ref(false)
const deleting = ref(false)
const errorMessage = ref('')

const badgeCount = computed(() => Number(props.tier?.badgeCount ?? 0))
const canDelete = computed(() => Boolean(props.tier) && props.targets.length > 0 && migrateTo.value !== '')

watch(open, (value) => {
  if (!value) return
  migrateTo.value = ''
  confirming.value = false
  errorMessage.value = ''
})

function close() {
  open.value = false
}

async function remove() {
  if (!props.tier || deleting.value) return
  if (!canDelete.value) return
  if (!confirming.value) {
    confirming.value = true
    return
  }
  deleting.value = true
  errorMessage.value = ''
  try {
    await props.store.deleteTier(props.tier.key, migrateTo.value)
    toast.add({
      title: t('admin.badges.tiers.toast.deleted'),
      description: t('admin.badges.tiers.remove.doneHint'),
      color: 'success',
      icon: 'i-lucide-trash-2',
      duration: 5000
    })
    emit('deleted')
    close()
  } catch (error) {
    errorMessage.value = apiErrorMessage(error) || t('admin.badges.tiers.toast.deleteFailed')
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" :ui="{ content: 'sm:max-w-md' }" @update:open="(value) => { if (!value) close() }">
    <template #content>
      <div class="flex flex-col">
        <div class="border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ t('admin.badges.tiers.remove.title') }}</h3>
          <p v-if="props.tier" class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
            {{ props.tier.name.zh }} · {{ props.tier.key }}
          </p>
        </div>

        <div class="space-y-3 px-5 py-4">
          <p class="text-sm text-slate-700 dark:text-zinc-200">
            {{ t('admin.badges.tiers.remove.body', { count: badgeCount }) }}
          </p>

          <UFormField :label="t('admin.badges.tiers.remove.migrateTo')">
            <USelect v-model="migrateTo" :items="props.targets.map(target => ({ value: target.key, label: target.name.zh }))" class="w-full" />
            <template #hint>
              <span class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.badges.tiers.remove.migrateHint') }}</span>
            </template>
          </UFormField>

          <p v-if="props.targets.length === 0" class="text-sm text-amber-600 dark:text-amber-400">
            {{ t('admin.badges.tiers.remove.noTarget') }}
          </p>
          <p v-else-if="confirming" class="text-sm text-red-600 dark:text-red-400">
            {{ t('admin.badges.tiers.remove.confirmBody', { count: badgeCount }) }}
          </p>
          <p v-if="errorMessage" class="text-sm text-red-600 dark:text-red-400">{{ errorMessage }}</p>
        </div>

        <div class="flex items-center justify-end gap-2 border-t border-slate-200 px-5 py-3 dark:border-zinc-800">
          <UButton color="neutral" variant="ghost" @click="close">{{ t('admin.badges.actions.cancel') }}</UButton>
          <UButton
            color="error"
            :loading="deleting"
            :disabled="!canDelete"
            @click="remove"
          >
            {{ confirming ? t('admin.badges.tiers.remove.confirm') : t('admin.badges.actions.delete') }}
          </UButton>
        </div>
      </div>
    </template>
  </UModal>
</template>
