<script setup lang="ts">
import type { AdminUserEditorForm } from '~/composables/admin/identity/useAdminUserDetailEditor'

const props = defineProps<{
  form: AdminUserEditorForm
  errors: Record<string, string[]>
  saving: boolean
  clearError: (name: string) => void
}>()

const emit = defineEmits<{ save: [] }>()

const { t } = useI18n()
</script>

<template>
  <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
    <div class="mb-4 flex items-center justify-between gap-3">
      <div>
        <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
          {{ t('admin.users.profileSection') }}
        </h4>
        <p class="text-xs text-slate-500 dark:text-zinc-400">
          {{ t('admin.users.profileSectionHelp') }}
        </p>
      </div>
      <UButton
        color="primary"
        leading-icon="i-lucide-save"
        :loading="saving"
        @click="emit('save')"
      >
        {{ t('admin.users.saveProfile') }}
      </UButton>
    </div>

    <div class="grid gap-3">
      <UFormField data-user-field="bio" :label="t('admin.users.bio')" :error="errors.bio?.[0]">
        <UTextarea
          v-model="props.form.bio"
          :rows="3"
          class="w-full"
          @update:model-value="clearError('bio')"
        />
      </UFormField>
      <UFormField data-user-field="signature" :label="t('admin.users.signature')" :error="errors.signature?.[0]">
        <UTextarea
          v-model="props.form.signature"
          :rows="2"
          class="w-full"
          @update:model-value="clearError('signature')"
        />
      </UFormField>
      <div class="grid gap-3 sm:grid-cols-2">
        <UFormField data-user-field="location" :label="t('admin.users.location')" :error="errors.location?.[0]">
          <UInput
            v-model="props.form.location"
            class="w-full"
            @update:model-value="clearError('location')"
          />
        </UFormField>
        <UFormField data-user-field="websiteUrl" :label="t('admin.users.websiteUrl')" :error="errors.websiteUrl?.[0]">
          <UInput
            v-model="props.form.websiteUrl"
            type="url"
            placeholder="https://"
            class="w-full"
            @update:model-value="clearError('websiteUrl')"
          />
        </UFormField>
      </div>
    </div>
  </section>
</template>
