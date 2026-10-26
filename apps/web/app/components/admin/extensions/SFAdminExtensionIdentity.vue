<script setup lang="ts">
import SFAdminExtensionUpdateBadges from '~/components/admin/extensions/SFAdminExtensionUpdateBadges.vue'
import {
  capabilityCount,
  isExtensionArtifactAvailable,
  type AdminExtension
} from '~/utils/admin/adminExtensions'
import type { ExtensionLocalizedDisplay } from '~/utils/admin/adminExtensionsLocalization'
import type { AdminListBadge } from '~/utils/admin/adminSurfaces'

defineOptions({ name: 'SFAdminExtensionIdentity' })

const props = withDefaults(defineProps<{
  extension: AdminExtension
  display: ExtensionLocalizedDisplay
  statusLabel: string
  statusColor?: 'primary' | 'success' | 'warning' | 'error' | 'neutral'
  icon?: string
  badges?: AdminListBadge[]
  selectable?: boolean
  selected?: boolean
}>(), {
  statusColor: 'neutral',
  icon: 'i-lucide-blocks',
  badges: () => [],
  selectable: false,
  selected: false
})

const emit = defineEmits<{ select: [] }>()

const { t } = useI18n()

const artifactMissing = computed(() => !isExtensionArtifactAvailable(props.extension))
const authorUrl = computed(() => props.display.author.url || props.display.url)
</script>

<template>
  <div class="min-w-0">
    <!--
      可选中时身份簇整体是一个按钮：图标/名字/状态/页面徽章都可点。
      更新徽章是链接，必须放在按钮之后——<a> 嵌 <button> 既非法也会吞掉键盘操作。
    -->
    <div class="flex min-w-0 flex-wrap items-center gap-2">
      <button
        v-if="props.selectable"
        type="button"
        :aria-pressed="props.selected ? 'true' : 'false'"
        class="inline-flex min-w-0 max-w-full flex-wrap items-center gap-2 rounded-md text-left outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent)] focus-visible:ring-offset-2 focus-visible:ring-offset-white dark:focus-visible:ring-offset-zinc-900"
        @click="emit('select')"
      >
        <UIcon :name="props.icon" class="size-4 shrink-0 text-[var(--sf-accent)]" />
        <span
          role="heading"
          aria-level="3"
          class="truncate text-sm font-semibold text-slate-900 dark:text-zinc-100"
        >
          {{ props.display.name }}
        </span>
        <UBadge :color="props.statusColor" variant="subtle">
          {{ props.statusLabel }}
        </UBadge>
        <slot name="badges" />
      </button>
      <template v-else>
        <UIcon :name="props.icon" class="size-4 shrink-0 text-[var(--sf-accent)]" />
        <span
          role="heading"
          aria-level="3"
          class="truncate text-sm font-semibold text-slate-900 dark:text-zinc-100"
        >
          {{ props.display.name }}
        </span>
        <SFAdminExtensionUpdateBadges :badges="props.badges" :extension-name="props.display.name" />
        <UBadge :color="props.statusColor" variant="subtle">
          {{ props.statusLabel }}
        </UBadge>
        <slot name="badges" />
      </template>
      <SFAdminExtensionUpdateBadges v-if="props.selectable" :badges="props.badges" :extension-name="props.display.name" />
    </div>

    <p
      v-if="props.display.description"
      class="mt-1.5 line-clamp-2 text-sm leading-5 text-slate-600 dark:text-zinc-300"
    >
      {{ props.display.description }}
    </p>
    <p v-if="artifactMissing" class="mt-1.5 text-sm text-red-600 dark:text-red-400">
      {{ t('admin.extensions.artifact.missingDescription') }}
    </p>
    <p class="mt-1 truncate text-xs text-slate-500 dark:text-zinc-400">
      {{ props.extension.id }} · v{{ props.extension.version }} · {{ t('admin.extensions.capabilityCount', { count: capabilityCount(props.extension) }) }}
    </p>
    <slot name="details" />

    <a
      v-if="authorUrl"
      :href="authorUrl"
      target="_blank"
      rel="noopener noreferrer"
      class="mt-2 inline-flex max-w-full items-center gap-1.5 rounded text-xs font-medium text-slate-500 transition hover:text-[var(--sf-accent)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent)] focus-visible:ring-offset-2 focus-visible:ring-offset-white dark:text-zinc-400 dark:hover:text-[var(--sf-accent-dark)] dark:focus-visible:ring-offset-zinc-900"
      :title="t('admin.extensions.authorWebsiteTitle', { name: props.display.author.name })"
      :aria-label="t('admin.extensions.authorWebsiteTitle', { name: props.display.author.name })"
    >
      <UIcon name="i-lucide-user-round" class="size-3.5 shrink-0" />
      <span class="truncate">{{ t('admin.extensions.authorLinkLabel', { name: props.display.author.name }) }}</span>
      <UIcon name="i-lucide-external-link" class="size-3 shrink-0" />
    </a>
    <span
      v-else-if="props.display.author.name"
      class="mt-2 inline-flex max-w-full items-center gap-1.5 text-xs text-slate-500 dark:text-zinc-400"
    >
      <UIcon name="i-lucide-user-round" class="size-3.5 shrink-0" />
      <span class="truncate">{{ t('admin.extensions.authorLinkLabel', { name: props.display.author.name }) }}</span>
    </span>

    <slot name="footer" />
  </div>
</template>
