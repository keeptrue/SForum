<script setup lang="ts">
import type { Permission } from '~/utils/admin/adminUsers'
import { usePermissionText } from '~/composables/identity/usePermissionText'

const props = defineProps<{
  permissions: Permission[]
  allow: string[]
  deny: string[]
  saving: boolean
  locked: boolean
  dirty: boolean
}>()

const emit = defineEmits<{
  setMode: [key: string, mode: 'inherit' | 'allow' | 'deny']
  save: []
}>()

const { t } = useI18n()
const { permissionLabel, permissionDescription, permissionModuleLabel } = usePermissionText()

const overrideModes = ['inherit', 'allow', 'deny'] as const

const groupedPermissions = computed(() => {
  const groups = new Map<string, Permission[]>()
  for (const permission of props.permissions) {
    const group = groups.get(permission.module) ?? []
    group.push(permission)
    groups.set(permission.module, group)
  }
  return Array.from(groups.entries()).map(([module, items]) => ({ module, items }))
})

function modeOf(key: string) {
  if (props.allow.includes(key)) {
    return 'allow'
  }
  if (props.deny.includes(key)) {
    return 'deny'
  }
  return 'inherit'
}
</script>

<template>
  <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
    <div class="mb-4 flex items-center justify-between gap-3">
      <div>
        <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
          {{ t('admin.users.permissionSection') }}
        </h4>
        <p class="text-xs text-slate-500 dark:text-zinc-400">
          {{ props.locked ? t('admin.users.superAdminOverrideLocked') : t('admin.users.permissionSectionHelp') }}
        </p>
      </div>
      <UButton
        color="primary"
        leading-icon="i-lucide-save"
        :loading="props.saving"
        :disabled="props.locked || !props.dirty"
        @click="emit('save')"
      >
        {{ t('admin.users.savePermissions') }}
      </UButton>
    </div>

    <div class="space-y-4">
      <div v-for="group in groupedPermissions" :key="group.module" class="space-y-2">
        <div class="text-xs font-semibold uppercase text-slate-500 dark:text-zinc-400">
          {{ permissionModuleLabel(group.module) }}
        </div>
        <div class="divide-y divide-slate-200 overflow-hidden rounded-md border border-slate-200 bg-white dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-950">
          <div
            v-for="permission in group.items"
            :key="permission.key"
            class="grid gap-3 px-3 py-2 text-sm sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center"
          >
            <div class="min-w-0">
              <span class="block font-semibold text-slate-900 dark:text-zinc-100">
                {{ permissionLabel(permission) }}
              </span>
              <code class="mt-0.5 block text-xs text-slate-500 dark:text-zinc-400">{{ permission.key }}</code>
              <p class="mt-0.5 text-xs text-slate-500 dark:text-zinc-400">
                {{ permissionDescription(permission) }}
              </p>
            </div>
            <div class="inline-flex rounded-md border border-slate-200 bg-slate-50 p-0.5 text-xs dark:border-zinc-800 dark:bg-zinc-900">
              <button
                v-for="mode in overrideModes"
                :key="mode"
                type="button"
                class="h-7 px-2.5 font-medium transition"
                :class="modeOf(permission.key) === mode
                  ? 'rounded bg-white text-[var(--sf-accent)] shadow-sm dark:bg-zinc-800 dark:text-[var(--sf-accent-dark)]'
                  : 'text-slate-500 hover:text-slate-900 dark:text-zinc-400 dark:hover:text-zinc-100'"
                :disabled="props.locked"
                @click="emit('setMode', permission.key, mode)"
              >
                {{ t(`admin.users.overrideMode.${mode}`) }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
