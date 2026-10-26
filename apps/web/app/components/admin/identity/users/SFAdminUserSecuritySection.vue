<script setup lang="ts">
import type { AdminUserEditorForm } from '~/composables/admin/identity/useAdminUserDetailEditor'

const props = defineProps<{
  form: AdminUserEditorForm
  errors: Record<string, string[]>
  resetting: boolean
  revoking: boolean
  clearing: boolean
  clearError: (name: string) => void
}>()

const emit = defineEmits<{
  reset: []
  revoke: []
  clearIps: []
}>()

const { t } = useI18n()
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
        <div>
          <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
            {{ t('admin.users.sessionSection') }}
          </h4>
          <p class="text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.users.sessionSectionHelp') }}
          </p>
        </div>
        <UButton
          color="error"
          variant="soft"
          leading-icon="i-lucide-log-out"
          :loading="revoking"
          @click="emit('revoke')"
        >
          {{ t('admin.users.revokeSessions') }}
        </UButton>
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
</template>
