<script setup lang="ts">
/**
 * 搜索快捷词 chips。只渲染词条并回传选中值；不请求、不改路由。
 * 词条来自真实标签数据（`SFSearchPage` 传入），不是写死的示例文案。
 */
const props = withDefaults(defineProps<{
  terms: Array<{ label: string, value: string }>
  /** 分组可访问名（不用 ariaLabel：aria-* 前缀在 vue-tsc 的 prop 解析里会被当成普通属性） */
  groupLabel: string
  /** 当前输入框已有词时高亮对应 chip */
  activeValue?: string
}>(), {
  activeValue: ''
})

const emit = defineEmits<{
  select: [value: string]
}>()
</script>

<template>
  <div
    v-if="props.terms.length"
    class="sf-aurora-quick"
    role="group"
    :aria-label="props.groupLabel"
  >
    <button
      v-for="term in props.terms"
      :key="term.value"
      type="button"
      class="sf-aurora-quick__chip"
      :class="{ 'is-active': props.activeValue === term.value }"
      :aria-pressed="props.activeValue === term.value"
      @click="emit('select', term.value)"
    >
      {{ term.label }}
    </button>
  </div>
</template>
