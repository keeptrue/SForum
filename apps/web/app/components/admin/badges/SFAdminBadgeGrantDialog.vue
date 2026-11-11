<script setup lang="ts">
import type { AdminBadgeRow } from '~/utils/admin/adminBadges'
import { apiErrorFields, apiErrorMessage } from '~/composables/useApiClient'

/**
 * 手动授予弹窗：按用户名授予（后端支持 `username` 直接解析），理由可选。
 * 自动徽章不允许手动授予，后端会回 422 `forum.badge_manual_only`，这里提前禁用提交。
 */
const props = defineProps<{ badge: AdminBadgeRow | null }>()
const emit = defineEmits<{ saved: [] }>()

const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const { request } = useApiClient()
const toast = useToast()

const username = ref('')
const reason = ref('')
const submitting = ref(false)
const errorMessage = ref('')
const fieldErrors = ref<Record<string, string[]>>({})

const manual = computed(() => props.badge?.grantMode === 'manual')
const canSubmit = computed(() => Boolean(props.badge) && manual.value && username.value.trim().length > 0 && !submitting.value)

watch(open, (value) => {
  if (!value) return
  username.value = ''
  reason.value = ''
  errorMessage.value = ''
  fieldErrors.value = {}
})

function close() {
  open.value = false
}

async function submit() {
  if (!canSubmit.value || !props.badge) return
  submitting.value = true
  errorMessage.value = ''
  fieldErrors.value = {}
  try {
    await request(`/admin/badges/${props.badge.id}/grants`, {
      method: 'POST',
      body: { username: username.value.trim(), reason: reason.value.trim() }
    })
    toast.add({ title: t('admin.badges.toast.granted'), color: 'success', icon: 'i-lucide-award', duration: 5000 })
    emit('saved')
    close()
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
</script>

<template>
  <UModal v-model:open="open" :ui="{ content: 'sm:max-w-md' }" @update:open="(value) => { if (!value) close() }">
    <template #content>
      <div class="flex flex-col">
        <div class="border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ t('admin.badges.grant.title') }}</h3>
          <p v-if="badge" class="mt-1 text-xs text-slate-500 dark:text-zinc-400">{{ badge.name }}</p>
        </div>

        <div class="space-y-3 px-5 py-4">
          <UFormField :label="t('admin.badges.grant.user')" :error="fieldErrors.username?.[0]">
            <UInput
              v-model="username"
              class="w-full"
              :placeholder="t('admin.badges.grant.userPlaceholder')"
              autocomplete="off"
            />
          </UFormField>
          <UFormField :label="t('admin.badges.grant.reason')" :error="fieldErrors.reason?.[0]">
            <UTextarea v-model="reason" :rows="2" class="w-full" :placeholder="t('admin.badges.grant.reasonPlaceholder')" />
          </UFormField>
          <p v-if="!manual" class="text-sm text-amber-600 dark:text-amber-400">{{ t('admin.badges.grant.alreadyGranted') }}</p>
          <p v-if="errorMessage" class="text-sm text-red-600 dark:text-red-400">{{ errorMessage }}</p>
        </div>

        <div class="flex items-center justify-end gap-2 border-t border-slate-200 px-5 py-3 dark:border-zinc-800">
          <UButton color="neutral" variant="ghost" @click="close">{{ t('admin.badges.actions.cancel') }}</UButton>
          <UButton color="primary" :loading="submitting" :disabled="!canSubmit" @click="submit">
            {{ t('admin.badges.grant.submit') }}
          </UButton>
        </div>
      </div>
    </template>
  </UModal>
</template>
