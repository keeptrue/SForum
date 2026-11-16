<script setup lang="ts">
import type { AdminUserEditorForm } from '~/composables/admin/identity/useAdminUserDetailEditor'

const props = defineProps<{
  form: AdminUserEditorForm
  errors: Record<string, string[]>
  resetting: boolean
  revoking: boolean
  revokingApiTokens: boolean
  clearing: boolean
  /** 与后端同口径：没有 user.manage 时连入口都不渲染。 */
  canManage: boolean
  clearError: (name: string) => void
}>()

const emit = defineEmits<{
  reset: []
  revoke: []
  revokeApiTokens: []
  clearIps: []
}>()

const { t } = useI18n()

/**
 * 本区两个破坏性动作共用同一个确认框：撤销令牌（只杀 PAT）与强制下线（会话 + 全部
 * PAT，更重）。重的那个更需要护栏——两者共用一个户型，也避免只给轻的那个加确认，
 * 让人误以为「没确认的更安全」。
 */
type DestructiveAction = 'sessions' | 'apiTokens'

const pendingAction = ref<DestructiveAction | null>(null)

const confirmOpen = computed({
  get: () => pendingAction.value !== null,
  set: (value: boolean) => {
    if (!value) {
      pendingAction.value = null
    }
  }
})

const confirmTitle = computed(() => pendingAction.value === 'sessions'
  ? t('admin.users.revokeSessionsConfirmTitle')
  : t('admin.users.revokeAPITokensConfirmTitle'))

const confirmHelp = computed(() => pendingAction.value === 'sessions'
  ? t('admin.users.revokeSessionsConfirmHelp')
  : t('admin.users.revokeAPITokensConfirmHelp'))

const confirmLabel = computed(() => pendingAction.value === 'sessions'
  ? t('admin.users.revokeSessions')
  : t('admin.users.revokeAPITokens'))

const confirmIcon = computed(() => pendingAction.value === 'sessions' ? 'i-lucide-log-out' : 'i-lucide-key-round')

const confirmPending = computed(() => pendingAction.value === 'sessions' ? props.revoking : props.revokingApiTokens)

function openConfirmation(action: DestructiveAction) {
  pendingAction.value = action
}

function closeConfirmation() {
  pendingAction.value = null
}

function confirmAction() {
  const action = pendingAction.value
  pendingAction.value = null
  if (action === 'sessions') {
    emit('revoke')
    return
  }
  if (action === 'apiTokens') {
    emit('revokeApiTokens')
  }
}
</script>

<template>
  <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
    <div class="space-y-4">
      <div>
        <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
          {{ t('admin.users.passwordSection') }}
        </h4>
        <p class="text-xs text-slate-500 dark:text-zinc-400">
          {{ t('admin.users.passwordSectionHelp') }}
        </p>
        <div class="mt-3 grid gap-3 sm:grid-cols-2">
          <UFormField data-user-field="password" :label="t('admin.users.newPassword')" :error="errors.password?.[0]">
            <UInput
              v-model="props.form.newPassword"
              type="password"
              autocomplete="new-password"
              class="w-full"
              :placeholder="t('admin.users.newPasswordPlaceholder')"
              @update:model-value="clearError('password')"
            />
          </UFormField>
          <UFormField data-user-field="confirmPassword" :label="t('admin.users.confirmPassword')" :error="errors.confirmPassword?.[0]">
            <UInput
              v-model="props.form.confirmPassword"
              type="password"
              autocomplete="new-password"
              class="w-full"
              :placeholder="t('admin.users.confirmPasswordPlaceholder')"
              @update:model-value="clearError('confirmPassword')"
            />
          </UFormField>
        </div>
        <div class="mt-3 flex flex-wrap items-center gap-2">
          <UButton
            color="primary"
            leading-icon="i-lucide-key-round"
            :loading="resetting"
            :disabled="!props.form.newPassword || !props.form.confirmPassword"
            @click="emit('reset')"
          >
            {{ t('admin.users.resetPassword') }}
          </UButton>
          <p class="text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.users.passwordResetHint') }}
          </p>
        </div>
      </div>

      <div class="flex flex-wrap items-center justify-between gap-3 border-t border-slate-200 pt-4 dark:border-zinc-800">
        <div class="min-w-0">
          <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
            {{ t('admin.users.sessionSection') }}
          </h4>
          <p class="text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.users.sessionSectionHelp') }}
          </p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <UButton
            data-testid="revoke-sessions"
            color="error"
            variant="soft"
            leading-icon="i-lucide-log-out"
            :loading="revoking"
            @click="openConfirmation('sessions')"
          >
            {{ t('admin.users.revokeSessions') }}
          </UButton>
          <UButton
            v-if="canManage"
            data-testid="revoke-api-tokens"
            color="error"
            variant="outline"
            leading-icon="i-lucide-key-round"
            :title="t('admin.users.revokeAPITokensHelp')"
            :loading="revokingApiTokens"
            @click="openConfirmation('apiTokens')"
          >
            {{ t('admin.users.revokeAPITokens') }}
          </UButton>
        </div>
      </div>

      <div class="flex flex-wrap items-center justify-between gap-3 border-t border-slate-200 pt-4 dark:border-zinc-800">
        <div>
          <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
            {{ t('admin.users.clearClientIPs') }}
          </h4>
          <p class="text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.users.clearClientIPsHelp') }}
          </p>
        </div>
        <UButton
          color="neutral"
          variant="soft"
          leading-icon="i-lucide-shield-off"
          :loading="clearing"
          @click="emit('clearIps')"
        >
          {{ t('admin.users.clearClientIPs') }}
        </UButton>
      </div>
    </div>
  </section>

  <UModal v-model:open="confirmOpen" :ui="{ content: 'sm:max-w-md' }">
    <template #content>
      <div class="p-5">
        <div class="flex items-start gap-3">
          <span class="grid size-10 shrink-0 place-items-center rounded-md bg-rose-50 text-rose-700 dark:bg-rose-950/40 dark:text-rose-200">
            <UIcon :name="confirmIcon" class="size-5" />
          </span>
          <div class="min-w-0">
            <h3 class="text-base font-semibold text-slate-950 dark:text-zinc-50">
              {{ confirmTitle }}
            </h3>
            <p class="mt-1 text-sm leading-6 text-slate-500 dark:text-zinc-400">
              {{ confirmHelp }}
            </p>
          </div>
        </div>
        <div class="mt-5 flex justify-end gap-2">
          <UButton
            type="button"
            color="neutral"
            variant="ghost"
            :disabled="confirmPending"
            @click="closeConfirmation"
          >
            {{ t('admin.common.cancel') }}
          </UButton>
          <UButton
            type="button"
            color="error"
            :loading="confirmPending"
            @click="confirmAction"
          >
            {{ confirmLabel }}
          </UButton>
        </div>
      </div>
    </template>
  </UModal>
</template>
