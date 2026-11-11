<script setup lang="ts">
/**
 * 标签芯片输入：展示可选标签 icon/颜色，支持建议列表与键盘添加。
 */
import {
  isForumTagSlug,
  normalizeForumTagSlugInput,
  taxonomyIconColor,
  type ForumTag
} from '~/utils/forum/forumTaxonomy'

const props = withDefaults(defineProps<{
  modelValue?: string[]
  options?: ForumTag[]
  id?: string
  label?: string
  hint?: string
  error?: string
  placeholder?: string
  placeholderMore?: string
  disabled?: boolean
  max?: number
  /** controlled：只能选已有标签；open/review 允许新 slug */
  creationMode?: 'controlled' | 'review' | 'open'
}>(), {
  modelValue: () => [],
  options: () => [],
  id: undefined,
  label: undefined,
  hint: undefined,
  error: undefined,
  placeholder: undefined,
  placeholderMore: undefined,
  disabled: false,
  max: 5,
  creationMode: 'open'
})

const emit = defineEmits<{
  'update:modelValue': [value: string[]]
  invalid: [messageKey: 'tagInvalid' | 'tagUnknownControlled' | 'tagLimit']
}>()

const { t } = useI18n()
const generatedId = useId()
const controlId = computed(() => props.id || generatedId)
const rootRef = ref<HTMLElement | null>(null)
const inputEl = ref<HTMLInputElement | null>(null)
const inputValue = ref('')
const suggestOpen = ref(false)
const activeSuggest = ref(0)

function focusInput() {
  inputEl.value?.focus()
}

const optionMap = computed(() => new Map(props.options.map(tag => [tag.slug, tag])))
const selected = computed(() => props.modelValue || [])
const atMax = computed(() => selected.value.length >= props.max)

const placeholderText = computed(() => {
  if (selected.value.length) {
    return props.placeholderMore || t('composer.tagsPlaceholderMore')
  }
  return props.placeholder || t('composer.tagsPlaceholder')
})

const message = computed(() => props.error || props.hint)

const suggestions = computed(() => {
  const query = normalizeForumTagSlugInput(inputValue.value)
  const unused = props.options.filter(tag => !selected.value.includes(tag.slug))
  if (!query) {
    return unused.slice(0, 8)
  }
  return unused
    .filter((tag) => {
      const hay = `${tag.slug} ${tag.name}`.toLowerCase()
      return hay.includes(query.toLowerCase())
    })
    .slice(0, 8)
})

function tagIconColor(tag: ForumTag | undefined) {
  return taxonomyIconColor(tag?.iconColor)
}

function tagIconName(tag: ForumTag | undefined) {
  const icon = tag?.icon?.trim() || ''
  if (icon.startsWith('i-')) {
    return icon
  }
  return 'i-lucide-hash'
}

function displayName(slug: string) {
  return optionMap.value.get(slug)?.name || slug
}

function resolveTag(slug: string) {
  return optionMap.value.get(slug)
}

function commitSlug(rawInput: string) {
  if (props.disabled || atMax.value) {
    if (atMax.value) {
      emit('invalid', 'tagLimit')
    }
    return
  }
  const raw = normalizeForumTagSlugInput(rawInput)
  if (!raw) {
    return
  }
  if (!isForumTagSlug(raw)) {
    emit('invalid', 'tagInvalid')
    return
  }
  if (props.creationMode === 'controlled' && !optionMap.value.has(raw)) {
    emit('invalid', 'tagUnknownControlled')
    return
  }
  if (selected.value.includes(raw)) {
    inputValue.value = ''
    suggestOpen.value = false
    return
  }
  if (selected.value.length >= props.max) {
    emit('invalid', 'tagLimit')
    return
  }
  emit('update:modelValue', [...selected.value, raw])
  inputValue.value = ''
  suggestOpen.value = false
  activeSuggest.value = 0
}

function removeSlug(slug: string) {
  emit('update:modelValue', selected.value.filter(item => item !== slug))
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'ArrowDown' && suggestions.value.length) {
    event.preventDefault()
    suggestOpen.value = true
    activeSuggest.value = Math.min(suggestions.value.length - 1, activeSuggest.value + 1)
    return
  }
  if (event.key === 'ArrowUp' && suggestions.value.length) {
    event.preventDefault()
    activeSuggest.value = Math.max(0, activeSuggest.value - 1)
    return
  }
  if (event.key === 'Escape') {
    suggestOpen.value = false
    return
  }
  if (event.key === 'Backspace' && !inputValue.value && selected.value.length) {
    removeSlug(selected.value[selected.value.length - 1]!)
    return
  }
  if (event.key === 'Enter' || event.key === ',') {
    event.preventDefault()
    if (suggestOpen.value && suggestions.value[activeSuggest.value]) {
      commitSlug(suggestions.value[activeSuggest.value]!.slug)
      return
    }
    commitSlug(inputValue.value)
  }
}

function onFocus() {
  if (!props.disabled && !atMax.value) {
    suggestOpen.value = true
  }
}

function onBlur() {
  // 延迟关闭，允许点击建议项
  window.setTimeout(() => {
    if (!rootRef.value?.contains(document.activeElement)) {
      if (inputValue.value.trim()) {
        commitSlug(inputValue.value)
      }
      suggestOpen.value = false
    }
  }, 120)
}

function pickSuggestion(tag: ForumTag) {
  commitSlug(tag.slug)
}

function onDocumentPointerDown(event: PointerEvent) {
  if (!suggestOpen.value || !rootRef.value) {
    return
  }
  const target = event.target as Node | null
  if (target && !rootRef.value.contains(target)) {
    suggestOpen.value = false
  }
}

onMounted(() => {
  document.addEventListener('pointerdown', onDocumentPointerDown, true)
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocumentPointerDown, true)
})

watch(suggestions, (list) => {
  if (activeSuggest.value >= list.length) {
    activeSuggest.value = Math.max(0, list.length - 1)
  }
})
</script>

<template>
  <div
    ref="rootRef"
    class="sf-tag-input"
    :class="{
      'is-invalid': Boolean(error),
      'is-disabled': disabled,
      'is-open': suggestOpen && suggestions.length
    }"
  >
    <label v-if="label" class="sf-tag-input__label" :for="controlId">
      {{ label }}
    </label>

    <div class="sf-tag-input__control" @click="focusInput">
      <span
        v-for="slug in selected"
        :key="slug"
        class="sf-tag-input__chip"
      >
        <span
          class="sf-tag-input__chip-icon"
          :style="{ color: tagIconColor(resolveTag(slug)) }"
          aria-hidden="true"
        >
          <UIcon :name="tagIconName(resolveTag(slug))" class="size-3.5" />
        </span>
        <span class="sf-tag-input__chip-text">#{{ displayName(slug) }}</span>
        <button
          type="button"
          class="sf-tag-input__chip-remove"
          :aria-label="t('composer.removeTag')"
          :disabled="disabled"
          @click.stop="removeSlug(slug)"
        >
          <UIcon name="i-lucide-x" class="size-3" aria-hidden="true" />
        </button>
      </span>

      <input
        :id="controlId"
        ref="inputEl"
        v-model="inputValue"
        type="text"
        class="sf-tag-input__field"
        :placeholder="placeholderText"
        :disabled="disabled || atMax"
        :aria-invalid="error ? 'true' : undefined"
        :aria-describedby="message ? `${controlId}-message` : undefined"
        :aria-autocomplete="'list'"
        :aria-expanded="suggestOpen && suggestions.length > 0"
        :aria-controls="`${controlId}-suggest`"
        autocomplete="off"
        @keydown="onKeydown"
        @focus="onFocus"
        @blur="onBlur"
        @input="suggestOpen = true"
      >

      <ul
        v-if="suggestOpen && suggestions.length"
        :id="`${controlId}-suggest`"
        class="sf-tag-input__suggest"
        role="listbox"
      >
        <li
          v-for="(tag, index) in suggestions"
          :key="tag.id"
          role="option"
          class="sf-tag-input__suggest-item"
          :class="{ 'is-active': index === activeSuggest }"
          :aria-selected="index === activeSuggest"
          @mousedown.prevent="pickSuggestion(tag)"
          @mouseenter="activeSuggest = index"
        >
          <span
            class="sf-tag-input__chip-icon"
            :style="{ color: tagIconColor(tag) }"
            aria-hidden="true"
          >
            <UIcon :name="tagIconName(tag)" class="size-4" />
          </span>
          <span class="sf-tag-input__suggest-copy">
            <strong>#{{ tag.name }}</strong>
            <small>{{ tag.slug }}</small>
          </span>
        </li>
      </ul>
    </div>

    <p v-if="message" :id="`${controlId}-message`" class="sf-tag-input__message" :class="{ 'is-error': error }">
      {{ message }}
    </p>
  </div>
</template>

<style scoped>
.sf-tag-input {
  position: relative;
  display: grid;
  gap: 0.4rem;
  min-width: 0;
}

.sf-tag-input__label {
  color: var(--sf-fg);
  font-size: 0.82rem;
  font-weight: 700;
}

.sf-tag-input__control {
  position: relative;
  display: flex;
  min-height: 2.5rem;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  padding: 6px 8px;
  background: var(--sf-card);
  transition: border-color 0.16s ease, box-shadow 0.16s ease;
  cursor: text;
}

.sf-tag-input__control:focus-within {
  border-color: var(--sf-accent);
  box-shadow: 0 0 0 3px var(--sf-accent-focus);
}

.sf-tag-input.is-invalid .sf-tag-input__control {
  border-color: var(--sf-danger);
}

.sf-tag-input.is-disabled .sf-tag-input__control {
  cursor: not-allowed;
  opacity: 0.62;
}

.sf-tag-input__chip {
  display: inline-flex;
  max-width: 100%;
  min-height: 28px;
  align-items: center;
  gap: 5px;
  border: 1px solid color-mix(in srgb, var(--sf-accent) 22%, var(--sf-border));
  border-radius: 999px;
  padding: 2px 6px 2px 6px;
  background: var(--sf-accent-soft);
  color: var(--sf-accent);
  font-size: 0.74rem;
  font-weight: 750;
}

.sf-tag-input__chip-icon {
  display: inline-grid;
  width: 16px;
  height: 16px;
  flex: none;
  place-items: center;
}

.sf-tag-input__chip-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-tag-input__chip-remove {
  display: grid;
  width: 18px;
  height: 18px;
  flex: none;
  place-items: center;
  border: 0;
  border-radius: 999px;
  padding: 0;
  background: transparent;
  color: inherit;
  cursor: pointer;
}

.sf-tag-input__chip-remove:hover:not(:disabled) {
  background: color-mix(in srgb, var(--sf-accent) 16%, transparent);
}

.sf-tag-input__field {
  min-width: 120px;
  flex: 1 1 140px;
  border: 0;
  padding: 2px 4px;
  background: transparent;
  color: var(--sf-fg);
  font-size: 0.86rem;
  outline: 0;
}

.sf-tag-input__suggest {
  position: absolute;
  z-index: 40;
  top: calc(100% + 4px);
  left: 0;
  right: 0;
  list-style: none;
  max-height: 240px;
  overflow: auto;
  margin: 0;
  border: 1px solid var(--sf-public-border, var(--sf-border));
  border-radius: 10px;
  padding: 6px;
  background: var(--sf-public-surface, var(--sf-card));
  box-shadow: var(--sf-public-shadow, 0 12px 28px rgba(15, 23, 42, 0.12));
}

.sf-tag-input__suggest-item {
  display: grid;
  grid-template-columns: 22px minmax(0, 1fr);
  align-items: center;
  gap: 10px;
  border-radius: 8px;
  padding: 8px;
  cursor: pointer;
}

.sf-tag-input__suggest-item:hover,
.sf-tag-input__suggest-item.is-active {
  background: var(--sf-public-surface-muted, var(--sf-muted));
}

.sf-tag-input__suggest-copy {
  min-width: 0;
  display: grid;
  gap: 1px;
}

.sf-tag-input__suggest-copy strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--sf-public-text, var(--sf-fg));
  font-size: 0.84rem;
  font-weight: 700;
}

.sf-tag-input__suggest-copy small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--sf-public-text-muted, var(--sf-fg-tertiary));
  font-size: 0.7rem;
}

.sf-tag-input__message {
  margin: 0;
  color: var(--sf-public-text-muted, var(--sf-fg-tertiary));
  font-size: 0.72rem;
  line-height: 1.5;
}

.sf-tag-input__message.is-error {
  color: var(--sf-danger);
  font-weight: 650;
}
</style>
