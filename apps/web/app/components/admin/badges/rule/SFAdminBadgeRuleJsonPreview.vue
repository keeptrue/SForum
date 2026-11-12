<script setup lang="ts">
/**
 * 规则 JSON 只读预览 + 复制。
 *
 * 只读是刻意的：可视化树是唯一编辑入口，JSON 用来核对与贴给后端排查。
 * 复制成功/失败都走 Toast（5 秒自动消失的成功态），不在这里做第二套提示。
 */
import type { BadgeRule } from '~/utils/admin/badgeRule'

const props = defineProps<{ rule: BadgeRule }>()

const { t } = useI18n()
const toast = useToast()

const json = computed(() => JSON.stringify(props.rule, null, 2))

async function copy() {
  try {
    if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable')
    await navigator.clipboard.writeText(json.value)
    toast.add({ color: 'success', icon: 'i-lucide-check', title: t('admin.badges.rule.copied'), duration: 5000 })
  } catch {
    toast.add({ color: 'error', icon: 'i-lucide-alert-triangle', title: t('admin.badges.rule.copyFailed') })
  }
}
</script>

<template>
  <!-- 原生 details：默认收起，不占版面也不抢注意力；展开后仍是只读 + 复制。 -->
  <details class="sf-rule-json">
    <!-- 复制按钮放在 summary 里：折叠态也能一次点到，不必先展开再去内容区找。 -->
    <summary class="sf-rule-json__summary">
      <span class="sf-rule-json__title">{{ t('admin.badges.rule.jsonTitle') }}</span>
      <button
        type="button"
        class="sf-rule-json__copy"
        data-rule-copy
        :aria-label="t('admin.badges.rule.copy')"
        :title="t('admin.badges.rule.copy')"
        @click.stop.prevent="copy"
      >
        <UIcon name="i-lucide-copy" aria-hidden="true" />
        <span>{{ t('admin.badges.rule.copy') }}</span>
      </button>
    </summary>
    <pre class="sf-rule-json__code"><code>{{ json }}</code></pre>
  </details>
</template>

<style scoped>
.sf-rule-json {
  display: flex;
  flex-direction: column;
  gap: 6px;
  box-sizing: border-box;
  min-width: 0;
  margin-top: 16px;
}

.sf-rule-json__summary {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  cursor: pointer;
}

.sf-rule-json__title {
  color: var(--sf-fg-secondary);
  font-size: 12px;
  font-weight: 600;
}

.sf-rule-json__summary:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 1px;
}

.sf-rule-json__copy {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 8px;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: var(--sf-card);
  color: var(--sf-fg-secondary);
  font-size: 12px;
}

.sf-rule-json__copy:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 1px;
}

.sf-rule-json__code {
  box-sizing: border-box;
  max-height: 240px;
  margin: 0;
  padding: 10px;
  overflow: auto;
  border: 1px solid var(--sf-border);
  border-radius: 10px;
  background: var(--sf-muted);
  color: var(--sf-fg-secondary);
  font-size: 11px;
  line-height: 1.5;
}
</style>
