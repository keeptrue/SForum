<script setup lang="ts">
/**
 * `/search` 页面级搜索胶囊（02 极光玻璃）。
 *
 * 边界：拥有胶囊外壳、聚焦光晕、提交按钮与搜索地标；不拥有查询执行、不拥有结果、不写路由。
 * 输入本体复用 `SFSearch`，所以 `::-webkit-search-cancel-button` 重置、Enter 提交与
 * `formLabel` 地标命名都不会分叉出第二套行为。
 *
 * 地标：`formLabel` 必须是 `search.landmarkHero`——顶栏叫 `nav.search`，同页两个
 * `role="search"` 同名会触发 axe `landmark-unique`（见 SFSearch.vue 的注释）。
 */
const props = withDefaults(defineProps<{
  modelValue?: string
  placeholder?: string
  /** 提交中：按钮进入 busy 态，但不禁用输入（用户仍可继续改词） */
  busy?: boolean
}>(), {
  modelValue: '',
  placeholder: undefined,
  busy: false
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
  submit: [value: string]
}>()

const { t } = useI18n()
const focused = ref(false)

const resolvedPlaceholder = computed(() => props.placeholder ?? t('search.placeholder'))
const landmarkLabel = computed(() => t('search.landmarkHero'))

function submit(value: string) {
  emit('submit', value.trim())
}
</script>

<template>
  <div class="sf-aurora-hero">
    <div
      class="sf-aurora-capsule"
      :class="{ 'is-focus': focused, 'is-busy': props.busy }"
      @focusin="focused = true"
      @focusout="focused = false"
    >
      <span class="sf-aurora-capsule__mag" aria-hidden="true">
        <UIcon name="i-lucide-search" class="size-[18px]" />
      </span>

      <SFSearch
        :model-value="props.modelValue"
        :placeholder="resolvedPlaceholder"
        :ariaLabel="landmarkLabel"
        :formLabel="landmarkLabel"
        kbd="Enter"
        class="sf-aurora-capsule__search"
        @update:model-value="emit('update:modelValue', $event)"
        @submit="submit"
      />

      <!--
        `SFSearch` 自带的图标提交键在胶囊里被 CSS 隐藏（`.sf-search__submit{display:none}`），
        提交动作统一由这个按钮承担：它是页面上唯一的「开始搜索」控件，Enter 仍走表单 submit。
      -->
      <button
        class="sf-aurora-capsule__go"
        type="button"
        :aria-label="t('search.submit')"
        :aria-busy="props.busy"
        @click="submit(props.modelValue)"
      >
        <UIcon
          :name="props.busy ? 'i-lucide-loader-circle' : 'i-lucide-arrow-right'"
          class="size-[17px]"
          :class="{ 'sf-aurora-spin': props.busy }"
        />
      </button>
    </div>
  </div>
</template>
