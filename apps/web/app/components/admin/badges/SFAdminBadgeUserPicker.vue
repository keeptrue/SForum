<script setup lang="ts">
/**
 * 批量授予的用户选择器：300ms 防抖搜索 + 多选 chip。
 *
 * 为什么不是「一个用户名输入框」：批量授予是这次改版的主路径，而用户名不可枚举。
 * 搜索走后端 `grant-candidates`（`alreadyGranted` 由后端算），前端不自己比对已授予列表
 * —— 那需要先分页拉全量。
 *
 * 键盘可达性是硬要求：Enter 选首个可选结果、Backspace 在空查询时删末项、
 * 上下键移动高亮。鼠标只是其中一条路径。
 */
import type { AdminBadgeGrantCandidate } from '~/composables/admin/badges/useAdminBadges'

const props = withDefaults(defineProps<{
  modelValue: AdminBadgeGrantCandidate[]
  badgeId: number | null
  search: (q: string, badgeId: number | null) => Promise<AdminBadgeGrantCandidate[]>
  placeholder: string
  /** 空查询时的引导文案（不请求后端）。 */
  emptyLabel: string
  noResultLabel: string
  /** 已持有结果的标记（结果置灰 + 不可选）。 */
  alreadyLabel: string
  /** 移除 chip 的 aria-label，含 `{name}`。 */
  removeLabel: string
  /** 上限提示，含 `{max}`。 */
  maxLabel: string
  max?: number
  disabled?: boolean
}>(), {
  max: 50,
  disabled: false
})

const emit = defineEmits<{ 'update:modelValue': [value: AdminBadgeGrantCandidate[]] }>()

const DEBOUNCE_MS = 300

const query = ref('')
const results = ref<AdminBadgeGrantCandidate[]>([])
const loading = ref(false)
const open = ref(false)
const activeIndex = ref(-1)

let timer: ReturnType<typeof setTimeout> | undefined
let requestSeq = 0

const selectedIds = computed(() => new Set(props.modelValue.map(item => item.userId)))
const reachedMax = computed(() => props.modelValue.length >= props.max)

function selectable(item: AdminBadgeGrantCandidate): boolean {
  return !item.alreadyGranted && !selectedIds.value.has(item.userId)
}

const firstSelectableIndex = computed(() => results.value.findIndex(selectable))

function runSearch(value: string) {
  const term = value.trim()
  const seq = ++requestSeq
  if (!term) {
    results.value = []
    loading.value = false
    activeIndex.value = -1
    return
  }
  loading.value = true
  void props.search(term, props.badgeId)
    .then((items) => {
      if (seq !== requestSeq) return
      results.value = Array.isArray(items) ? items : []
      activeIndex.value = -1
    })
    .catch(() => {
      if (seq !== requestSeq) return
      results.value = []
    })
    .finally(() => {
      if (seq === requestSeq) loading.value = false
    })
}

watch(query, (value) => {
  open.value = true
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => runSearch(value), DEBOUNCE_MS)
})

onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
})

function add(item: AdminBadgeGrantCandidate) {
  if (props.disabled || !selectable(item) || reachedMax.value) return
  emit('update:modelValue', [...props.modelValue, item])
  query.value = ''
  results.value = []
  activeIndex.value = -1
  open.value = false
}

function remove(userId: number) {
  if (props.disabled) return
  emit('update:modelValue', props.modelValue.filter(item => item.userId !== userId))
}

function removeLast() {
  const last = props.modelValue.at(-1)
  if (last) remove(last.userId)
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Enter') {
    event.preventDefault()
    const target = results.value[activeIndex.value] ?? results.value[firstSelectableIndex.value]
    if (target) add(target)
    return
  }
  if (event.key === 'Backspace' && query.value === '' && props.modelValue.length > 0) {
    event.preventDefault()
    removeLast()
    return
  }
  if (event.key === 'ArrowDown') {
    event.preventDefault()
    if (results.value.length === 0) return
    activeIndex.value = (activeIndex.value + 1) % results.value.length
    return
  }
  if (event.key === 'ArrowUp') {
    event.preventDefault()
    if (results.value.length === 0) return
    activeIndex.value = activeIndex.value <= 0 ? results.value.length - 1 : activeIndex.value - 1
  }
}

function initials(item: AdminBadgeGrantCandidate): string {
  const source = item.displayName?.trim() || item.username?.trim() || '?'
  return source.slice(0, 1).toUpperCase()
}

function removeLabelOf(item: AdminBadgeGrantCandidate): string {
  return props.removeLabel.replace('{name}', item.displayName || item.username)
}
</script>

<template>
  <div class="min-w-0">
    <ul v-if="props.modelValue.length > 0" class="mb-2 flex flex-wrap gap-1.5">
      <li
        v-for="item in props.modelValue"
        :key="item.userId"
        class="inline-flex items-center gap-1 rounded-full border border-slate-200 bg-slate-50 py-0.5 pl-1 pr-1.5 text-xs text-slate-700 dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-200"
      >
        <span class="font-medium">{{ item.displayName || item.username }}</span>
        <button
          type="button"
          class="inline-flex size-4 items-center justify-center rounded-full text-slate-500 hover:bg-slate-200 dark:text-zinc-400 dark:hover:bg-zinc-700"
          :aria-label="removeLabelOf(item)"
          :disabled="props.disabled"
          @click="remove(item.userId)"
        >
          <UIcon name="i-lucide-x" class="size-3" aria-hidden="true" />
        </button>
      </li>
    </ul>

    <div class="relative">
      <UInput
        :model-value="query"
        :placeholder="props.placeholder"
        :disabled="props.disabled || reachedMax"
        icon="i-lucide-search"
        class="w-full"
        autocomplete="off"
        role="combobox"
        :aria-expanded="open"
        aria-autocomplete="list"
        @update:model-value="query = String($event)"
        @keydown="onKeydown"
        @focus="open = true"
        @blur="open = false"
      />

      <p v-if="reachedMax" class="mt-1 text-xs text-amber-600 dark:text-amber-400">
        {{ props.maxLabel.replace('{max}', String(props.max)) }}
      </p>

      <div
        v-if="open && !reachedMax"
        class="absolute z-20 mt-1 w-full overflow-hidden rounded-lg border border-slate-200 bg-white shadow-lg dark:border-zinc-700 dark:bg-zinc-900"
      >
        <p v-if="query.trim() === ''" class="px-3 py-2 text-xs text-slate-500 dark:text-zinc-400">
          {{ props.emptyLabel }}
        </p>
        <p v-else-if="loading" class="px-3 py-2 text-xs text-slate-500 dark:text-zinc-400">…</p>
        <p v-else-if="results.length === 0" class="px-3 py-2 text-xs text-slate-500 dark:text-zinc-400">
          {{ props.noResultLabel }}
        </p>
        <ul v-else role="listbox" class="max-h-56 overflow-y-auto py-1">
          <li
            v-for="(item, index) in results"
            :key="item.userId"
            role="option"
            :aria-selected="index === activeIndex"
            :aria-disabled="!selectable(item)"
            class="flex items-center gap-2 px-3 py-1.5 text-sm"
            :class="[
              selectable(item) ? 'cursor-pointer text-slate-700 dark:text-zinc-200' : 'cursor-not-allowed text-slate-400 dark:text-zinc-500',
              index === activeIndex && selectable(item) ? 'bg-slate-100 dark:bg-zinc-800' : ''
            ]"
            @mousedown.prevent="add(item)"
          >
            <span class="inline-flex size-6 shrink-0 items-center justify-center rounded-full bg-slate-200 text-[11px] font-semibold text-slate-700 dark:bg-zinc-700 dark:text-zinc-200">
              {{ initials(item) }}
            </span>
            <span class="min-w-0 flex-1 truncate">
              <span class="font-medium">{{ item.displayName || item.username }}</span>
              <span class="ml-1 text-xs text-slate-500 dark:text-zinc-400">@{{ item.username }}</span>
            </span>
            <UBadge v-if="item.status" color="neutral" variant="soft" size="sm">{{ item.status }}</UBadge>
            <span v-if="item.alreadyGranted" class="text-xs font-medium">{{ props.alreadyLabel }}</span>
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>
