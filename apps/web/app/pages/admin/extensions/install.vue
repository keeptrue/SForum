<script setup lang="ts">
import SFAdminExtensionInstallPanel from '~/components/admin/extensions/SFAdminExtensionInstallPanel.vue'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'

definePageMeta({ middleware: 'admin', layout: 'admin' })
defineOptions({ name: 'AdminExtensionInstall' })

const { t } = useI18n()
const adminPage = useAdminPage('/extensions/install')
const adminRoutes = useAdminRoutes()

useSeoMeta({ title: t('admin.extensionInstall.title') })
</script>

<template>
  <div class="w-full min-w-0">
    <header class="mb-4 flex flex-col gap-1">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.extensionInstall.title') }}
      </h2>
      <p class="text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.extensionInstall.intro') }}
      </p>
    </header>

    <UDashboardToolbar data-testid="extension-install-toolbar" class="mb-6 overflow-x-hidden rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
      <template #left>
        <div class="hidden min-w-0 items-center gap-2 text-sm lg:flex">
          <UIcon name="i-lucide-package" class="size-4 shrink-0" />
          <span class="truncate">{{ t('admin.extensionInstall.toolbarHint') }}</span>
        </div>
      </template>
      <template #right>
        <UButton
          color="neutral"
          variant="outline"
          icon="i-lucide-plug"
          :to="adminRoutes.path('/extensions/plugins')"
          :aria-label="t('admin.extensionInstall.openPlugins')"
          :title="t('admin.extensionInstall.openPlugins')"
        >
          <span class="hidden xl:inline">{{ t('admin.extensionInstall.openPlugins') }}</span>
        </UButton>
        <UButton
          color="neutral"
          variant="outline"
          icon="i-lucide-palette"
          :to="adminRoutes.path('/extensions/themes')"
          :aria-label="t('admin.extensionInstall.openThemes')"
          :title="t('admin.extensionInstall.openThemes')"
        >
          <span class="hidden xl:inline">{{ t('admin.extensionInstall.openThemes') }}</span>
        </UButton>
      </template>
    </UDashboardToolbar>

    <div class="flex w-full min-w-0 flex-col gap-4">
      <SFAdminExtensionInstallPanel />
    </div>
  </div>
</template>
