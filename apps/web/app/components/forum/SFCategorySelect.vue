<script setup lang="ts">
/**
 * 分类选择器：触发器与下拉均展示管理端 icon + iconColor（原生 select 无法着色）。
 */
import { taxonomyIconColor, type ForumCategory } from '~/utils/forum/forumTaxonomy'

const props = withDefaults(defineProps<{
  modelValue?: string
  categories?: ForumCategory[]
  id?: string
  label?: string
  hint?: string
  error?: string
  emptyLabel?: string
  disabled?: boolean
  pending?: boolean
}>(), {
  modelValue: '',
  categories: () => [],
  id: undefined,
  label: undefined,
  hint: undefined,
  error: undefined,
  emptyLabel: undefined,
  disabled: false,
  pending: false
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const { t } = useI18n()
const generatedId = useId()
const controlId = computed(() => props.id || generatedId)
const listboxId = computed(() => `${controlId.value}-listbox`)
const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)
const activeIndex = ref(-1)

const selected = computed(() =>
  props.categories.find(category => category.slug === props.modelValue) || null
)

const emptyText = computed(() => props.emptyLabel || t('composer.categoryDefault'))
const message = computed(() => props.error || props.hint)

/** 与左栏导航一致：优先管理端 iconColor，否则主色。 */
function categoryIconColor(category: ForumCategory | null) {
  if (!category) {
    return 'var(--sf-public-text-muted)'
  }
  return taxonomyIconColor(category.iconColor)
}

function categoryIconName(category: ForumCategory | null) {
  if (!category) {
    return 'i-lucide-folder'
  }
  const icon = category.icon?.trim() || ''
  if (icon.startsWith('i-')) {
    return icon
  }
  return 'i-lucide-folder'
}

const optionCount = computed(() => props.categories.length + 1)

function optionAt(index: number): { slug: string; category: ForumCategory | null } {
  if (index <= 0) {
    return { slug: '', category: null }
  }
  const category = props.categories[index - 1] || null
  return { slug: category?.slug || '', category }
}

function indexOfSlug(slug: string) {
  if (!slug) {
    return 0
  }
  const idx = props.categories.findIndex(category => category.slug === slug)
  return idx >= 0 ? idx + 1 : 0
}

function openMenu() {
  if (props.disabled || props.pending) {
    return
  }
  open.value = true
  activeIndex.value = indexOfSlug(props.modelValue || '')
}

function closeMenu() {
  open.value = false
  activeIndex.value = -1
}

function toggleMenu() {
  if (open.value) {
    closeMenu()
  } else {
    openMenu()
  }
}

function selectSlug(slug: string) {
  emit('update:modelValue', slug)
  closeMenu()
}

function onTriggerKeydown(event: KeyboardEvent) {
  if (props.disabled) {
    return
  }
  if (event.key === 'ArrowDown' || event.key === 'Enter' || event.key === ' ') {
    event.preventDefault()
    if (!open.value) {
      openMenu()
      return
    }
    if (event.key === 'Enter' || event.key === ' ') {
      const option = optionAt(Math.max(0, activeIndex.value))
      selectSlug(option.slug)
    }
  } else if (event.key === 'Escape') {
    event.preventDefault()
    closeMenu()
  } else if (open.value && event.key === 'ArrowUp') {
    event.preventDefault()
    activeIndex.value = Math.max(0, activeIndex.value - 1)
  } else if (open.value && event.key === 'ArrowDown') {
    event.preventDefault()
    activeIndex.value = Math.min(optionCount.value - 1, activeIndex.value + 1)
  } else if (open.value && event.key === 'Home') {
    event.preventDefault()
    activeIndex.value = 0
  } else if (open.value && event.key === 'End') {
    event.preventDefault()
    activeIndex.value = optionCount.value - 1
  }
}

function onDocumentPointerDown(event: PointerEvent) {
  if (!open.value || !rootRef.value) {
    return
  }
  const target = event.target as Node | null
  if (target && !rootRef.value.contains(target)) {
    closeMenu()
  }
}

onMounted(() => {
  document.addEventListener('pointerdown', onDocumentPointerDown, true)
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocumentPointerDown, true)
})
</script>

<template>
  <div
    ref="rootRef"
    class="sf-category-select"
    :class="{
      'is-open': open,
      'is-invalid': Boolean(error),
      'is-disabled': disabled
    }"
  >
    <label v-if="label" class="sf-category-select__label" :for="controlId">
      {{ label }}
    </label>

    <button
      :id="controlId"
      type="button"
      class="sf-category-select__trigger"
      :disabled="disabled"
      :aria-expanded="open"
      aria-haspopup="listbox"
      :aria-controls="listboxId"
      :aria-invalid="error ? 'true' : undefined"
      :aria-describedby="message ? `${controlId}-message` : undefined"
      @click="toggleMenu"
      @keydown="onTriggerKeydown"
    >
      <span class="sf-category-select__value">
        <span
          class="sf-category-select__icon"
          :style="{ color: categoryIconColor(selected) }"
          aria-hidden="true"
        >
          <UIcon :name="categoryIconName(selected)" class="size-[18px]" />
        </span>
        <span class="sf-category-select__text" :class="{ 'is-placeholder': !selected }">
          {{ selected?.name || emptyText }}
        </span>
      </span>
      <UIcon
        :name="pending ? 'i-lucide-loader-circle' : 'i-lucide-chevron-down'"
        class="size-4 sf-category-select__chevron"
        :class="{ 'is-spin': pending }"
        aria-hidden="true"
      />
    </button>

    <div
      v-if="open"
      :id="listboxId"
      class="sf-category-select__menu"
      role="listbox"
      :aria-label="label || emptyText"
    >
      <button
        type="button"
        role="option"
        class="sf-category-select__option"
        :class="{
          'is-selected': !modelValue,
          'is-active': activeIndex === 0
        }"
        :aria-selected="!modelValue"
        @mouseenter="activeIndex = 0"
        @click="selectSlug('')"
      >
        <span class="sf-category-select__icon is-muted" aria-hidden="true">
          <UIcon name="i-lucide-folder" class="size-[18px]" />
        </span>
        <span class="sf-category-select__option-copy">
          <strong>{{ emptyText }}</strong>
        </span>
      </button>

      <button
        v-for="(category, index) in categories"
        :key="category.id"
        type="button"
        role="option"
        class="sf-category-select__option"
        :class="{
          'is-selected': modelValue === category.slug,
          'is-active': activeIndex === index + 1
        }"
        :aria-selected="modelValue === category.slug"
        @mouseenter="activeIndex = index + 1"
        @click="selectSlug(category.slug)"
      >
        <span
          class="sf-category-select__icon"
          :style="{ color: categoryIconColor(category) }"
          aria-hidden="true"
        >
          <UIcon :name="categoryIconName(category)" class="size-[18px]" />
        </span>
        <span class="sf-category-select__option-copy">
          <strong>{{ category.name }}</strong>
          <small v-if="category.groupName">{{ category.groupName }}</small>
        </span>
        <UIcon
          v-if="modelValue === category.slug"
          name="i-lucide-check"
          class="size-4 sf-category-select__check"
          aria-hidden="true"
        />
      </button>

      <p v-if="!categories.length" class="sf-category-select__empty">
        {{ pending ? t('composer.tagsLoading') : t('composer.categoryHint') }}
      </p>
    </div>

    <p v-if="message" :id="`${controlId}-message`" class="sf-category-select__message" :class="{ 'is-error': error }">
      {{ message }}
    </p>
  </div>
</template>

<style scoped>
.sf-category-select {
  position: relative;
  display: grid;
  gap: 0.4rem;
  min-width: 0;
}

.sf-category-select__label {
  color: var(--sf-fg);
  font-size: 0.82rem;
  font-weight: 700;
}

.sf-category-select__trigger {
  width: 100%;
  min-height: 2.5rem;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  padding: 0 0.75rem 0 0.65rem;
  background: var(--sf-card);
  color: var(--sf-fg);
  text-align: left;
  cursor: pointer;
  transition: border-color 0.16s ease, box-shadow 0.16s ease;
}

.sf-category-select__trigger:hover:not(:disabled) {
  border-color: var(--sf-public-border-strong, var(--sf-border));
}

.sf-category-select__trigger:focus-visible {
  border-color: var(--sf-accent);
  box-shadow: 0 0 0 3px var(--sf-accent-focus);
  outline: 0;
}

.sf-category-select.is-open .sf-category-select__trigger {
  border-color: var(--sf-accent);
  box-shadow: 0 0 0 3px var(--sf-accent-focus);
}

.sf-category-select.is-invalid .sf-category-select__trigger {
  border-color: var(--sf-danger);
}

.sf-category-select__trigger:disabled {
  cursor: not-allowed;
  opacity: 0.62;
}

.sf-category-select__value {
  min-width: 0;
  display: inline-flex;
  align-items: center;
  gap: 9px;
}

.sf-category-select__icon {
  display: inline-grid;
  width: 22px;
  height: 22px;
  flex: none;
  place-items: center;
  border-radius: 6px;
  background: var(--sf-public-surface-muted, var(--sf-muted));
  color: var(--sf-accent);
}

.sf-category-select__icon.is-muted {
  color: var(--sf-public-text-muted, var(--sf-fg-tertiary));
}

.sf-category-select__text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 0.9rem;
  font-weight: 650;
}

.sf-category-select__text.is-placeholder {
  color: var(--sf-public-text-muted, var(--sf-fg-tertiary));
  font-weight: 500;
}

.sf-category-select__chevron {
  flex: none;
  color: var(--sf-public-text-muted, var(--sf-fg-tertiary));
  transition: transform 0.16s ease;
}

.sf-category-select.is-open .sf-category-select__chevron {
  transform: rotate(180deg);
}

.sf-category-select__chevron.is-spin {
  animation: sf-category-select-spin 0.9s linear infinite;
}

@keyframes sf-category-select-spin {
  to {
    transform: rotate(360deg);
  }
}

.sf-category-select__menu {
  position: absolute;
  z-index: 40;
  top: calc(100% + 4px);
  left: 0;
  right: 0;
  max-height: min(320px, 50vh);
  overflow: auto;
  border: 1px solid var(--sf-public-border, var(--sf-border));
  border-radius: 10px;
  padding: 6px;
  background: var(--sf-public-surface, var(--sf-card));
  box-shadow: var(--sf-public-shadow, 0 12px 28px rgba(15, 23, 42, 0.12));
}

.sf-category-select__option {
  width: 100%;
  min-height: 40px;
  display: grid;
  grid-template-columns: 22px minmax(0, 1fr) auto;
  align-items: center;
  gap: 10px;
  border: 0;
  border-radius: 8px;
  padding: 7px 8px;
  background: transparent;
  color: var(--sf-public-text, var(--sf-fg));
  text-align: left;
  cursor: pointer;
}

.sf-category-select__option:hover,
.sf-category-select__option.is-active {
  background: var(--sf-public-surface-muted, var(--sf-muted));
}

.sf-category-select__option.is-selected {
  background: var(--sf-accent-soft);
}

.sf-category-select__option-copy {
  min-width: 0;
  display: grid;
  gap: 1px;
}

.sf-category-select__option-copy strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 0.86rem;
  font-weight: 700;
}

.sf-category-select__option-copy small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--sf-public-text-muted, var(--sf-fg-tertiary));
  font-size: 0.7rem;
}

.sf-category-select__check {
  color: var(--sf-accent);
}

.sf-category-select__empty {
  margin: 0;
  padding: 12px 10px;
  color: var(--sf-public-text-muted, var(--sf-fg-tertiary));
  font-size: 0.78rem;
  text-align: center;
}

.sf-category-select__message {
  margin: 0;
  color: var(--sf-public-text-muted, var(--sf-fg-tertiary));
  font-size: 0.72rem;
  line-height: 1.5;
}

.sf-category-select__message.is-error {
  color: var(--sf-danger);
  font-weight: 650;
}
</style>
