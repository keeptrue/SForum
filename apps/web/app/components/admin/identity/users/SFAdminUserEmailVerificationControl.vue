<script setup lang="ts">
import type { AdminUserDetail } from '~/utils/admin/adminUsers'
import { apiErrorMessage } from '~/composables/useApiClient'

const props = defineProps<{ user: AdminUserDetail }>()
const emit = defineEmits<{ updated: [user: AdminUserDetail] }>()

const { t } = useI18n()
const { request } = useApiClient()
const toast = useToast()
const confirmOpen = ref(false)
const targetVerified = ref(false)
const saving = ref(false)
// 失败留在确认弹窗里：关窗会让人以为「点了没反应」，还得自己再点一次。
const confirmError = ref('')

function requestChange(verified: boolean) {
  targetVerified.value = verified
  confirmError.value = ''
  confirmOpen.value = true
}

function closeConfirmation() {
  confirmOpen.value = false
  confirmError.value = ''
}

async function confirmChange() {
  if (saving.value) return
  saving.value = true
  try {
    const user = await request<AdminUserDetail>(`/users/${props.user.id}/email-verification`, {
      method: 'PUT',
      body: { verified: targetVerified.value }
    })
    emit('updated', user)
    confirmOpen.value = false
    toast.add({
      color: 'success',
      icon: targetVerified.value ? 'i-lucide-badge-check' : 'i-lucide-rotate-ccw',
      title: targetVerified.value
        ? t('admin.users.emailVerificationMarkedSuccess')
        : t('admin.users.emailVerificationResetSuccess'),
      duration: 5000
    })
  } catch (error) {
    confirmError.value = apiErrorMessage(error) || t('admin.users.emailVerificationUpdateFailed')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="mt-4 flex flex-col gap-3 border-t border-slate-200 pt-4 sm:flex-row sm:items-center sm:justify-between dark:border-zinc-800">
    <div class="min-w-0">
      <div class="flex flex-wrap items-center gap-2">
        <h5 class="text-sm font-medium text-slate-700 dark:text-zinc-300">
          {{ t('admin.users.emailVerificationStatus') }}
        </h5>
        <UBadge :color="user.emailVerified ? 'success' : 'warning'" variant="soft">
          {{ user.emailVerified ? t('admin.users.emailVerified') : t('admin.users.emailUnverified') }}
        </UBadge>
      </div>
      <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
        {{ t('admin.users.emailVerificationHelp') }}
      </p>
    </div>
    <UButton
      v-if="user.emailVerified"
      type="button"
      color="error"
      variant="soft"
      leading-icon="i-lucide-rotate-ccw"
      @click="requestChange(false)"
    >
      {{ t('admin.users.resetEmailVerification') }}
    </UButton>
    <UButton
      v-else
      type="button"
      color="primary"
      variant="soft"
      leading-icon="i-lucide-badge-check"
      @click="requestChange(true)"
    >
      {{ t('admin.users.markEmailVerified') }}
    </UButton>
  </div>

  <UModal v-model:open="confirmOpen" :ui="{ content: 'sm:max-w-md' }">
    <template #content>
      <div class="p-5">
        <div class="flex items-start gap-3">
          <span class="grid size-10 shrink-0 place-items-center rounded-md bg-slate-100 text-slate-700 dark:bg-zinc-800 dark:text-zinc-200">
            <UIcon :name="targetVerified ? 'i-lucide-badge-check' : 'i-lucide-rotate-ccw'" class="size-5" />
          </span>
          <div>
            <h3 class="text-base font-semibold text-slate-950 dark:text-zinc-50">
              {{ targetVerified ? t('admin.users.markEmailVerifiedConfirmTitle') : t('admin.users.resetEmailVerificationConfirmTitle') }}
            </h3>
            <p class="mt-1 text-sm leading-6 text-slate-500 dark:text-zinc-400">
              {{ targetVerified ? t('admin.users.markEmailVerifiedConfirmHelp') : t('admin.users.resetEmailVerificationConfirmHelp') }}
            </p>
          </div>
        </div>
        <UAlert
          v-if="confirmError"
          class="mt-4"
          color="error"
          variant="soft"
          icon="i-lucide-triangle-alert"
          :title="t('admin.users.emailVerificationUpdateFailed')"
          :description="confirmError"
        />
        <div class="mt-5 flex justify-end gap-2">
          <UButton type="button" color="neutral" variant="ghost" :disabled="saving" @click="closeConfirmation">
            {{ t('admin.common.cancel') }}
          </UButton>
          <UButton
            type="button"
            :color="targetVerified ? 'primary' : 'error'"
            :loading="saving"
            @click="confirmChange"
          >
            {{ targetVerified ? t('admin.users.markEmailVerified') : t('admin.users.resetEmailVerification') }}
          </UButton>
        </div>
      </div>
    </template>
  </UModal>
</template>
