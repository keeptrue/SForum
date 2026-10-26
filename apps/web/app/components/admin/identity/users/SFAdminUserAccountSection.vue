<script setup lang="ts">
import type { AdminUserDetail } from '~/utils/admin/adminUsers'
import type { AdminUserEditorForm } from '~/composables/admin/identity/useAdminUserDetailEditor'
import SFAdminUserEmailVerificationControl from '~/components/admin/identity/users/SFAdminUserEmailVerificationControl.vue'

const props = defineProps<{
  user: AdminUserDetail
  form: AdminUserEditorForm
  errors: Record<string, string[]>
  saving: boolean
  clearError: (name: string) => void
}>()

const emit = defineEmits<{
  save: []
  refreshed: [user: AdminUserDetail]
}>()

const { t } = useI18n()

const localeOptions = computed(() => [
  { value: 'zh-CN', label: t('admin.users.localeZh') },
  { value: 'en-US', label: t('admin.users.localeEn') }
])

const statusOptions = computed(() => [
  { value: 'active', label: t('admin.users.statusActive') },
  { value: 'disabled', label: t('admin.users.statusDisabled') },
  { value: 'banned', label: t('admin.users.statusBanned') }
])

const kindOptions = computed(() => [
  { value: 'human', label: t('admin.users.kindHuman') },
  { value: 'bot', label: t('admin.users.kindBot') }
])
</script>

<template>
  <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
    <div class="mb-4 flex items-center justify-between gap-3">
      <div>
        <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
          {{ t('admin.users.accountSection') }}
        </h4>
        <p class="text-xs text-slate-500 dark:text-zinc-400">
          {{ t('admin.users.accountSectionHelp') }}
        </p>
      </div>
      <UButton
        color="primary"
        leading-icon="i-lucide-save"
        :loading="saving"
        @click="emit('save')"
      >
        {{ t('admin.users.saveAccount') }}
      </UButton>
    </div>

    <div class="grid gap-3 sm:grid-cols-2">
      <UFormField data-user-field="username" :label="t('admin.users.username')" :error="errors.username?.[0]">
        <UInput
          v-model="props.form.username"
          class="w-full"
          @update:model-value="clearError('username')"
        />
      </UFormField>
      <UFormField data-user-field="email" :label="t('admin.users.email')" :error="errors.email?.[0]">
        <UInput
          v-model="props.form.email"
          type="email"
          class="w-full"
          @update:model-value="clearError('email')"
        />
      </UFormField>
      <UFormField data-user-field="displayName" :label="t('admin.users.displayName')" :error="errors.displayName?.[0]">
        <UInput
          v-model="props.form.displayName"
          class="w-full"
          @update:model-value="clearError('displayName')"
        />
      </UFormField>
      <UFormField data-user-field="locale" :label="t('admin.users.locale')" :error="errors.locale?.[0]">
        <USelect v-model="props.form.locale" :items="localeOptions" class="w-full" />
      </UFormField>
      <UFormField
        data-user-field="status"
        class="sm:col-span-2"
        :label="t('admin.users.status')"
        :error="errors.status?.[0]"
      >
        <USelect v-model="props.form.status" :items="statusOptions" class="w-full max-w-xs" />
        <p v-if="props.user.isInitialSuperAdmin" class="mt-1 text-xs text-amber-600 dark:text-amber-400">
          {{ t('admin.users.initialSuperAdmin') }}
        </p>
      </UFormField>
      <UFormField
        data-user-field="kind"
        class="sm:col-span-2"
        :label="t('admin.users.kind')"
        :error="errors.kind?.[0]"
        :help="t('admin.users.kindHint')"
      >
        <USelect
          v-model="props.form.kind"
          :items="kindOptions"
          :disabled="props.user.isInitialSuperAdmin"
          class="w-full max-w-xs"
        />
      </UFormField>
    </div>

    <SFAdminUserEmailVerificationControl :user="props.user" @updated="value => emit('refreshed', value)" />
  </section>
</template>
