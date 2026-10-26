<script setup lang="ts">
import type { Role } from '~/utils/admin/adminUsers'

const props = defineProps<{
  roles: Role[]
  selectedKeys: string[]
  saving: boolean
  dirty: boolean
}>()

const emit = defineEmits<{
  toggle: [key: string]
  save: []
}>()

const { t } = useI18n()
</script>

<template>
  <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
    <div class="mb-4 flex items-center justify-between gap-3">
      <div>
        <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
          {{ t('admin.users.roleSection') }}
        </h4>
        <p class="text-xs text-slate-500 dark:text-zinc-400">
          {{ t('admin.users.roleSectionHelp') }}
        </p>
      </div>
      <UButton
        color="primary"
        leading-icon="i-lucide-save"
        :loading="saving"
        :disabled="!props.dirty"
        @click="emit('save')"
      >
        {{ t('admin.users.saveRoles') }}
      </UButton>
    </div>

    <div class="grid gap-2 sm:grid-cols-2">
      <label
        v-for="role in props.roles"
        :key="role.key"
        class="flex min-h-14 cursor-pointer items-start gap-3 rounded-md border border-slate-200 bg-white px-3 py-2 text-sm transition hover:border-[var(--sf-accent-soft-border)] dark:border-zinc-800 dark:bg-zinc-950"
      >
        <input
          type="checkbox"
          class="mt-1 size-4 accent-[var(--sf-accent)]"
          :checked="props.selectedKeys.includes(role.key)"
          @change="emit('toggle', role.key)"
        >
        <span class="min-w-0">
          <span class="block font-semibold text-slate-900 dark:text-zinc-100">{{ role.alias }}</span>
          <span class="block truncate text-xs text-slate-500 dark:text-zinc-400">{{ role.key }}</span>
        </span>
      </label>
    </div>
  </section>
</template>
