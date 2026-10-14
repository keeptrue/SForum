<script setup lang="ts">
import { ADMIN_AI_DRAFT_KEY } from '~/composables/admin/useAdminAISettingsForm'
import { promptUsesDefault } from '~/utils/admin/adminAI'
import SFAdminAITriggerModeBlock from '~/components/admin/ai/blocks/SFAdminAITriggerModeBlock.vue'

const { t } = useI18n()
// 草稿由页面壳持有并注入：提示词与供应商配置是同一次保存的内容。
const draft = inject(ADMIN_AI_DRAFT_KEY)!
const { form, ai, canManage } = draft

// 提示词为空表示使用内置默认；一键恢复就是把自定义内容清空。判定与保存时的
// 归一化共用 promptUsesDefault，避免界面说「已自定义」而保存把它当成默认。
const customized = computed(() => !promptUsesDefault(form.value?.reply?.systemPrompt, ai.defaultSystemPrompt.value))

function restoreDefaultPrompt() {
  if (!form.value) return
  form.value.reply = { systemPrompt: ai.defaultSystemPrompt.value }
}
</script>

<template>
  <div v-if="form" class="flex min-w-0 flex-col gap-4">
    <SFAdminAITriggerModeBlock />
    <section class="min-w-0 rounded-lg border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div class="min-w-0">
          <p class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.ai.replyPrompt.title') }}</p>
          <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.ai.replyPrompt.intro') }}</p>
        </div>
        <UBadge :color="customized ? 'primary' : 'neutral'" variant="soft" size="sm">
          {{ customized ? t('admin.ai.replyPrompt.customized') : t('admin.ai.replyPrompt.default') }}
        </UBadge>
      </div>
      <div class="mt-3">
        <LazySFEditor
          v-model="form.reply.systemPrompt"
          preset="basic-field"
          :load-trusted-catalog="false"
          :rows="16"
          :max-characters="8000"
          :disabled="!canManage"
          :aria-label="t('admin.ai.replyPrompt.title')"
          :placeholder="t('admin.ai.replyPrompt.placeholder')"
        />
      </div>
      <div class="mt-3 flex flex-wrap items-center gap-2">
        <UButton
          icon="i-lucide-rotate-ccw"
          color="neutral"
          variant="outline"
          size="sm"
          :disabled="!canManage || !customized"
          @click="restoreDefaultPrompt"
        >
          {{ t('admin.ai.replyPrompt.restore') }}
        </UButton>
        <span class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.ai.replyPrompt.safetyHint') }}</span>
      </div>
      <p v-if="draft.dirty.value" class="mt-2 text-xs text-amber-600 dark:text-amber-400">
        {{ t('admin.ai.dirtyHint') }}
      </p>
      <div v-if="ai.safetyAppendix.value" class="mt-3 rounded-md bg-slate-50 p-3 dark:bg-zinc-950/60">
        <p class="text-xs font-medium text-slate-600 dark:text-zinc-300">{{ t('admin.ai.replyPrompt.safetyTitle') }}</p>
        <pre class="mt-1 whitespace-pre-wrap font-sans text-xs text-slate-500 dark:text-zinc-400">{{ ai.safetyAppendix.value }}</pre>
      </div>
    </section>
  </div>
</template>
