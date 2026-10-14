<script setup lang="ts">
import type { AdminAISettings } from '~/composables/admin/useAdminAI'

const { t } = useI18n()
const form = defineModel<AdminAISettings | null>('form', { required: true })
const disabled = defineModel<boolean>('disabled', { default: false })

// 旧文档（多模态开关引入前保存的）没有 multimodal 字段：先补全再绑定，
// 与 reply.systemPrompt 的补全策略一致。
const enabled = computed<boolean>({
  get: () => form.value?.multimodal?.enabled === true,
  set: (value: boolean) => {
    const current = form.value
    if (!current) return
    current.multimodal = { ...(current.multimodal ?? { enabled: false }), enabled: value }
  }
})
</script>

<template>
  <section v-if="form" class="min-w-0 rounded-lg border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
    <p class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.ai.multimodal') }}</p>
    <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.ai.multimodalIntro') }}</p>

    <UFormField class="mt-3">
      <UCheckbox
        v-model="enabled"
        :label="t('admin.ai.multimodalEnabled')"
        :disabled="disabled"
      />
    </UFormField>

    <p class="mt-2 text-xs text-slate-500 dark:text-zinc-400">
      {{ t('admin.ai.multimodalLimitsHint') }}
    </p>
  </section>
</template>
