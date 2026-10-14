<script setup lang="ts">
import { ADMIN_AI_DRAFT_KEY } from '~/composables/admin/useAdminAISettingsForm'

const { t } = useI18n()
// 草稿由页面壳持有并注入：触发方式与提示词属于同一次保存。
const draft = inject(ADMIN_AI_DRAFT_KEY)!
const { form, canManage } = draft

const options = computed(() => [
  { value: 'mention', label: t('admin.ai.triggerMode.mention'), hint: t('admin.ai.triggerMode.mentionHint') },
  { value: 'autonomous', label: t('admin.ai.triggerMode.autonomous'), hint: t('admin.ai.triggerMode.autonomousHint') },
  { value: 'off', label: t('admin.ai.triggerMode.off'), hint: t('admin.ai.triggerMode.offHint') }
])

function select(mode: string) {
  if (!form.value || !canManage.value) return
  form.value.reply = { ...form.value.reply, triggerMode: mode as 'mention' | 'autonomous' | 'off' }
}
</script>

<template>
  <section v-if="form" class="min-w-0 rounded-lg border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
    <div class="min-w-0">
      <p class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.ai.triggerMode.title') }}</p>
      <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.ai.triggerMode.intro') }}</p>
    </div>
    <div class="mt-3 grid gap-2 sm:grid-cols-3">
      <button
        v-for="option in options"
        :key="option.value"
        type="button"
        class="rounded-lg border p-3 text-left transition-colors"
        :class="form.reply?.triggerMode === option.value
          ? 'border-primary-500 bg-primary-50 dark:border-primary-400 dark:bg-primary-950/40'
          : 'border-slate-200 hover:border-slate-300 dark:border-zinc-800 dark:hover:border-zinc-700'"
        :aria-pressed="form.reply?.triggerMode === option.value"
        :disabled="!canManage"
        @click="select(option.value)"
      >
        <span class="block text-sm font-medium text-slate-900 dark:text-zinc-100">{{ option.label }}</span>
        <span class="mt-1 block text-xs text-slate-500 dark:text-zinc-400">{{ option.hint }}</span>
      </button>
    </div>
    <p v-if="form.reply?.triggerMode === 'autonomous'" class="mt-2 text-xs text-amber-600 dark:text-amber-400">
      {{ t('admin.ai.triggerMode.costHint') }}
    </p>
  </section>
</template>
