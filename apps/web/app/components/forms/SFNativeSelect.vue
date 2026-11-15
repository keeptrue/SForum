<script setup lang="ts">
import { computed, inject, useAttrs, useId, watch } from 'vue'
import { inputIdInjectionKey } from '@nuxt/ui/composables/useFormField'

/**
 * 原生 `<select>` 的 Nuxt UI 表单字段适配层（UX2-08）。
 *
 * `UFormField` 没有 `:for` prop：它的 `<label for>` 指向自己 `useId()` 生成的 id，
 * 并且把这个 id 通过 `inputIdInjectionKey` 交给子级输入控件。Nuxt UI 自己的
 * UInput/USelect 会消费该 key，**原生 select 不会** → label 的 `for` 指向不存在的元素，
 * 读屏里这个下拉框没有任何名字，点标签也不聚焦。
 *
 * 这里做的是最小修正：读出注入的字段 id 落到 select 的 `id` 上，`for` 因此自然命中；
 * 其余属性、class、事件原样透传，调用点只把 `<select>` 换成 `<SFNativeSelect>`。
 */
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  modelValue?: string | number | null
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string | number]
}>()

const attrs = useAttrs()
const fieldId = inject<{ value?: string } | undefined>(inputIdInjectionKey, undefined)
// 不在 UFormField 里时（普通筛选栏、插件 surface）退化成一个自生成 id，行为等同裸 select。
const fallbackId = useId()

/**
 * 显式 `id` 优先，但必须**回写注入值**：`UFormField` 的 `<label :for>` 读的就是这个 ref，
 * 只改 select 自己的 id 会让 label 重新指向不存在的元素（Nuxt UI 的输入组件同样回写）。
 */
const explicitId = computed(() => attrs.id as string | undefined)
if (explicitId.value && fieldId) fieldId.value = explicitId.value
watch(explicitId, (value) => {
  if (value && fieldId) fieldId.value = value
})

const resolvedId = computed(() => explicitId.value || fieldId?.value || fallbackId)
const modifiers = computed(() => attrs.modelModifiers as Record<string, boolean> | undefined)
/** `:value` + `@change` 的调用点（例如插件 surface 表单）也要能用，所以两套绑定都认。 */
const selectValue = computed<string | number | undefined>(() => {
  if (props.modelValue !== undefined && props.modelValue !== null) return props.modelValue
  return attrs.value as string | number | undefined
})
/** `modelModifiers` / `value` 只服务于本组件，不该作为 DOM 属性落到 select 上。 */
const forwardedAttrs = computed(() => {
  const { modelModifiers: _modelModifiers, value: _value, ...rest } = attrs as Record<string, unknown>
  return rest
})

function handleChange(event: Event) {
  const raw = (event.target as HTMLSelectElement).value
  // `v-model.number` 的取整由调用方声明，组件必须替它做，否则类型会从 number 变成 string。
  emit('update:modelValue', modifiers.value?.number ? Number(raw) : raw)
}
</script>

<template>
  <select v-bind="forwardedAttrs" :id="resolvedId" :value="selectValue" @change="handleChange">
    <slot />
  </select>
</template>
