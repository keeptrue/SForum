<script setup lang="ts">
type SearchFilter = {
  label: string
  value: string
}

const props = withDefaults(defineProps<{
  modelValue?: string
  placeholder?: string
  filters?: SearchFilter[]
  selectedFilter?: string
  ariaLabel?: string
  /**
   * `role="search"` 地标的可访问名。
   *
   * 同一页可能出现多个搜索地标（顶栏 + 移动端搜索条 + 错误页恢复搜索），
   * 都叫「搜索」会触发 axe `landmark-unique`。留空时回落到输入框的可读名称，
   * 保持既有渲染不变；需要区分的宿主显式传入。
   */
  formLabel?: string
  kbd?: string
}>(), {
  modelValue: '',
  placeholder: undefined,
  filters: () => [],
  selectedFilter: undefined,
  ariaLabel: undefined,
  formLabel: undefined,
  kbd: undefined
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
  'update:selectedFilter': [value: string]
  'submit': [value: string]
}>()

const { t } = useI18n()

/** 占位文案同时也是可读名称：默认值必须走 i18n，不能写死中文（RV-28）。 */
const resolvedPlaceholder = computed(() => props.placeholder ?? t('search.placeholder'))

/** 地标名：显式 `formLabel` 优先，否则与输入框同源（默认行为不变）。 */
const resolvedFormLabel = computed(() => props.formLabel ?? props.ariaLabel ?? resolvedPlaceholder.value)

const inputElement = ref<HTMLInputElement | null>(null)

function onInput(event: Event) {
  emit('update:modelValue', (event.target as HTMLInputElement).value)
}

function submit() {
  emit('submit', (inputElement.value?.value ?? props.modelValue).trim())
}

function onKeydown(event: KeyboardEvent) {
  if (event.key !== 'Enter' || event.isComposing) {
    return
  }
  event.preventDefault()
  event.stopPropagation()
  submit()
}
</script>

<template>
  <div class="sf-search">
    <form role="search" :aria-label="resolvedFormLabel" @submit.prevent="submit">
      <div class="sf-search__box">
        <button
          type="button"
          class="sf-search__submit"
          :aria-label="ariaLabel || resolvedPlaceholder"
          :title="ariaLabel || resolvedPlaceholder"
          @click="submit"
        >
          <UIcon name="i-lucide-search" class="sf-search__icon size-4.5 shrink-0" aria-hidden="true" />
        </button>
        <input
          ref="inputElement"
          class="sf-search__input"
          type="search"
          :value="modelValue"
          :placeholder="resolvedPlaceholder"
          :aria-label="ariaLabel || resolvedPlaceholder"
          @input="onInput"
          @keydown="onKeydown"
        >
        <span v-if="kbd" class="sf-search__kbd">{{ kbd }}</span>
      </div>
    </form>
    <div v-if="filters.length" class="sf-search__filters" :aria-label="t('search.filtersLabel')" role="group">
      <button
        v-for="filter in filters"
        :key="filter.value"
        type="button"
        class="sf-badge"
        :class="selectedFilter === filter.value ? 'sf-badge--primary' : 'sf-badge--neutral'"
        :aria-pressed="selectedFilter === filter.value"
        @click="emit('update:selectedFilter', filter.value)"
      >
        {{ filter.label }}
      </button>
    </div>
  </div>
</template>
