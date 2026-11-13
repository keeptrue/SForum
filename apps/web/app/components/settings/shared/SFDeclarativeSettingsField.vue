<script setup lang="ts">
import { computed } from 'vue'
import type { ProfileSettingsDraftValue, ProfileSettingsField } from '~/utils/settings/profileSettings'
/**
 * 中性声明式字段：只认 SettingsField 契约（string / text / number / boolean / select），
 * 不认任何具体插件，也不带业务语义。资料设置页与插件字段分区共用它，
 * 这样「5 种字段类型 + 必填 + 描述 + 默认值 + 错误」只有一份实现。
 *
 * 无障碍：label 用 for 绑定控件；描述与错误各占一个 id，一起进 aria-describedby；
 * 错误另加 role="alert"，服务端返回后的第一眼就能被读到。
 */
const props = withDefaults(defineProps<{
  field: ProfileSettingsField
  modelValue: ProfileSettingsDraftValue
  /** 服务端返回的字段错误（已翻译/已格式化）。 */
  errors?: string[]
  /** 必填标记的读屏文案（「必填」）。 */
  requiredLabel?: string
  disabled?: boolean
}>(), {
  errors: () => [],
  requiredLabel: '',
  disabled: false
})

const emit = defineEmits<{
  'update:modelValue': [value: ProfileSettingsDraftValue]
  /** 用户重新输入时清掉上一次的服务端错误。 */
  change: []
}>()

const fieldId = computed(() => `settings-field-${props.field.fieldKey}`)
const descriptionId = computed(() => `${fieldId.value}-description`)
const errorId = computed(() => `${fieldId.value}-error`)
const hasError = computed(() => props.errors.length > 0)
const describedBy = computed(() => {
  const ids = [
    props.field.description ? descriptionId.value : '',
    hasError.value ? errorId.value : ''
  ].filter(Boolean)
  return ids.length > 0 ? ids.join(' ') : undefined
})

const inputType = computed(() => props.field.type === 'number' ? 'number' : 'text')

function onInput(event: Event) {
  const target = event.target as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement
  const raw = target.value
  if (props.field.type === 'number') {
    emit('update:modelValue', raw === '' ? '' : Number(raw))
  } else {
    emit('update:modelValue', raw)
  }
  emit('change')
}

function onToggle(event: Event) {
  emit('update:modelValue', (event.target as HTMLInputElement).checked)
  emit('change')
}
</script>

<template>
  <div class="sforum-settings-field" :class="{ 'is-invalid': hasError }">
    <label :for="fieldId" class="sforum-settings-field__label">
      {{ field.label }}
      <span v-if="field.required" aria-hidden="true" class="sforum-settings-field__required">*</span>
      <span v-if="field.required && requiredLabel" class="sr-only">{{ requiredLabel }}</span>
    </label>

    <textarea
      v-if="field.type === 'text'"
      :id="fieldId"
      :value="typeof modelValue === 'string' ? modelValue : ''"
      :rows="4"
      :maxlength="field.constraints?.maxLength"
      :placeholder="field.placeholder"
      :disabled="disabled"
      :aria-invalid="hasError ? 'true' : undefined"
      :aria-describedby="describedBy"
      :class="{ 'is-invalid': hasError }"
      @input="onInput"
    />

    <select
      v-else-if="field.type === 'select'"
      :id="fieldId"
      :value="typeof modelValue === 'string' ? modelValue : ''"
      :disabled="disabled"
      :aria-invalid="hasError ? 'true' : undefined"
      :aria-describedby="describedBy"
      class="sforum-settings-field__select"
      @change="onInput"
    >
      <option value="">
        {{ field.placeholder || '—' }}
      </option>
      <option
        v-for="option in field.options || []"
        :key="option.value"
        :value="option.value"
      >
        {{ option.label }}
      </option>
    </select>

    <label
      v-else-if="field.type === 'boolean'"
      :for="fieldId"
      class="sforum-settings-field__toggle"
    >
      <input
        :id="fieldId"
        type="checkbox"
        :checked="Boolean(modelValue)"
        :disabled="disabled"
        :aria-describedby="describedBy"
        @change="onToggle"
      >
      <span>{{ field.description || field.label }}</span>
    </label>

    <input
      v-else
      :id="fieldId"
      :type="inputType"
      :value="modelValue === '' ? '' : String(modelValue)"
      :maxlength="field.type === 'number' ? undefined : field.constraints?.maxLength"
      :min="field.constraints?.min"
      :max="field.constraints?.max"
      :placeholder="field.placeholder"
      :disabled="disabled"
      :aria-invalid="hasError ? 'true' : undefined"
      :aria-describedby="describedBy"
      :class="{ 'is-invalid': hasError }"
      @input="onInput"
    >

    <p
      v-if="field.description && field.type !== 'boolean'"
      :id="descriptionId"
      class="sforum-settings-field__description"
    >
      {{ field.description }}
    </p>

    <p
      v-if="hasError"
      :id="errorId"
      class="sforum-settings-field__error"
      role="alert"
    >
      {{ errors.join('；') }}
    </p>
  </div>
</template>
