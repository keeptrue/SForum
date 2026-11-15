<script setup lang="ts">
import SFEditorTablePicker from '~/components/editor/SFEditorTablePicker.vue'
import type { SFEditorTableSize } from '~/utils/editor/table'

export type SFEditorToolbarAction =
  | 'undo'
  | 'redo'
  | 'bold'
  | 'italic'
  | 'strike'
  | 'code'
  | 'bulletList'
  | 'orderedList'
  | 'blockquote'
  | 'codeBlock'
  | 'link'
  | 'image'
  | 'table'

export type SFEditorBlockFormat = 'paragraph' | 'heading-2' | 'heading-3'
export type SFEditorViewMode = 'write' | 'preview'
export type SFEditorExtensionTool = {
  id: string
  label: string
  icon: string
  disabled?: boolean
}

const props = defineProps<{
  preset: 'full' | 'basic-field'
  disabled: boolean
  canUndo: boolean
  canRedo: boolean
  active: Partial<Record<SFEditorToolbarAction, boolean>>
  blockFormat: SFEditorBlockFormat
  viewMode: SFEditorViewMode
  extensionTools?: SFEditorExtensionTool[]
}>()

const emit = defineEmits<{
  action: [action: SFEditorToolbarAction]
  'block-format': [format: SFEditorBlockFormat]
  'view-mode': [mode: SFEditorViewMode]
  'extension-action': [id: string]
  'insert-table': [size: SFEditorTableSize]
}>()

const full = computed(() => props.preset === 'full')

const { t } = useI18n()

// 按钮名既进 title 也进 aria-label：读屏与悬浮提示共用同一份 i18n 文案（RV-28）。
const markActions = computed(() => [
  { action: 'bold' as const, label: t('composer.editor.action.bold'), icon: 'i-lucide-bold' },
  { action: 'italic' as const, label: t('composer.editor.action.italic'), icon: 'i-lucide-italic' },
  ...(full.value ? [
    { action: 'strike' as const, label: t('composer.editor.action.strike'), icon: 'i-lucide-strikethrough' },
    { action: 'code' as const, label: t('composer.editor.action.code'), icon: 'i-lucide-code-2' }
  ] : [])
])

const blockActions = computed(() => [
  { action: 'bulletList' as const, label: t('composer.editor.action.bulletList'), icon: 'i-lucide-list' },
  { action: 'orderedList' as const, label: t('composer.editor.action.orderedList'), icon: 'i-lucide-list-ordered' },
  ...(full.value ? [
    { action: 'blockquote' as const, label: t('composer.editor.action.quote'), icon: 'i-lucide-quote' },
    { action: 'codeBlock' as const, label: t('composer.editor.action.codeBlock'), icon: 'i-lucide-square-code' }
  ] : [])
])

const insertActions = computed(() => [
  { action: 'link' as const, label: t('composer.editor.action.link'), icon: 'i-lucide-link-2' },
  ...(full.value ? [
    { action: 'image' as const, label: t('composer.editor.action.image'), icon: 'i-lucide-image' },
    { action: 'table' as const, label: t('composer.editor.action.table'), icon: 'i-lucide-table' }
  ] : [])
])

// 表格按钮由 SFEditorTablePicker 自己渲染（触发器 + 尺寸网格），这里排除通用按钮。
const insertButtonActions = computed(() => insertActions.value.filter(item => item.action !== 'table'))

const modeItems = computed<Array<{ value: SFEditorViewMode, label: string }>>(() => [
  { value: 'write', label: t('composer.editor.mode.write') },
  { value: 'preview', label: t('composer.editor.mode.preview') }
])

function selectBlockFormat(event: Event) {
  emit('block-format', (event.target as HTMLSelectElement).value as SFEditorBlockFormat)
}
</script>

<template>
  <div class="sf-editor__topbar">
    <div class="sf-editor__toolbar" :aria-label="t('composer.editor.toolbarLabel')">
      <div class="sf-editor__tool-group">
        <button
          type="button"
          class="sf-editor__tool"
          :title="t('composer.editor.undo')"
          :aria-label="t('composer.editor.undo')"
          :disabled="disabled || !canUndo"
          @pointerdown.prevent
          @click="emit('action', 'undo')"
        >
          <UIcon name="i-lucide-undo-2" class="sf-editor__tool-icon" />
        </button>
        <button
          type="button"
          class="sf-editor__tool"
          :title="t('composer.editor.redo')"
          :aria-label="t('composer.editor.redo')"
          :disabled="disabled || !canRedo"
          @pointerdown.prevent
          @click="emit('action', 'redo')"
        >
          <UIcon name="i-lucide-redo-2" class="sf-editor__tool-icon" />
        </button>
      </div>

      <span class="sf-editor__separator" aria-hidden="true" />

      <select
        v-if="full"
        class="sf-editor__format"
        :value="blockFormat"
        :disabled="disabled"
        :aria-label="t('composer.editor.formatLabel')"
        @change="selectBlockFormat"
      >
        <option value="paragraph">{{ t('composer.editor.format.paragraph') }}</option>
        <option value="heading-2">{{ t('composer.editor.format.heading2') }}</option>
        <option value="heading-3">{{ t('composer.editor.format.heading3') }}</option>
      </select>

      <div class="sf-editor__tool-group">
        <button
          v-for="item in markActions"
          :key="item.action"
          type="button"
          class="sf-editor__tool"
          :class="{ 'sf-editor__tool--active': active[item.action] }"
          :title="item.label"
          :aria-label="item.label"
          :aria-pressed="Boolean(active[item.action])"
          :disabled="disabled"
          @pointerdown.prevent
          @click="emit('action', item.action)"
        >
          <UIcon :name="item.icon" class="sf-editor__tool-icon" />
        </button>
      </div>

      <span class="sf-editor__separator" aria-hidden="true" />

      <div class="sf-editor__tool-group">
        <button
          v-for="item in blockActions"
          :key="item.action"
          type="button"
          class="sf-editor__tool"
          :class="{ 'sf-editor__tool--active': active[item.action] }"
          :title="item.label"
          :aria-label="item.label"
          :aria-pressed="Boolean(active[item.action])"
          :disabled="disabled"
          @pointerdown.prevent
          @click="emit('action', item.action)"
        >
          <UIcon :name="item.icon" class="sf-editor__tool-icon" />
        </button>
      </div>

      <span class="sf-editor__separator" aria-hidden="true" />

      <div class="sf-editor__tool-group">
        <button
          v-for="item in insertButtonActions"
          :key="item.action"
          type="button"
          class="sf-editor__tool"
          :class="{ 'sf-editor__tool--active': active[item.action] }"
          :title="item.label"
          :aria-label="item.label"
          :aria-pressed="Boolean(active[item.action])"
          :disabled="disabled"
          @pointerdown.prevent
          @click="emit('action', item.action)"
        >
          <UIcon :name="item.icon" class="sf-editor__tool-icon" />
        </button>
        <SFEditorTablePicker
          v-if="full"
          :disabled="disabled || Boolean(active.table)"
          @select="emit('insert-table', $event)"
        />
        <button
          v-for="item in extensionTools || []"
          :key="item.id"
          type="button"
          class="sf-editor__tool"
          :title="item.label"
          :aria-label="item.label"
          :disabled="disabled || item.disabled"
          @pointerdown.prevent
          @click="emit('extension-action', item.id)"
        >
          <UIcon :name="item.icon" class="sf-editor__tool-icon" />
        </button>
      </div>
    </div>

    <div v-if="full" class="sf-editor__modes" :aria-label="t('composer.editor.viewLabel')">
      <button
        v-for="mode in modeItems"
        :key="mode.value"
        type="button"
        class="sf-editor__mode"
        :class="{ 'sf-editor__mode--active': viewMode === mode.value }"
        :aria-pressed="viewMode === mode.value"
        @click="emit('view-mode', mode.value)"
      >
        {{ mode.label }}
      </button>
    </div>
  </div>
</template>
