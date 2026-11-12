<script setup lang="ts">
/**
 * 新建 / 编辑徽章等级弹窗。
 *
 * key 只在新建时可填：它是 `?tier=` 深链、`data-tier` 与已授徽章的关联键，
 * 改名等于把历史授予全部指向一个不存在的档。所以编辑态只读展示 key。
 *
 * 颜色字段的「按位置自动」= 用位次取默认色（`defaultTierColorName`），保存时落成具体 token：
 * 服务端存的是确定值，公开面不需要猜。
 */
import type { AdminBadgesStore, AdminBadgeTierRow } from '~/composables/admin/badges/useAdminBadges'
import SFAdminBadgeIconField from '~/components/admin/badges/SFAdminBadgeIconField.vue'
import SFAdminTierColorField from '~/components/admin/shared/SFAdminTierColorField.vue'
import { defaultTierColorName, normalizeTierColor, tierColorVar } from '~/utils/theme/tierColor'
import { apiErrorFields, apiErrorMessage } from '~/composables/useApiClient'

const props = defineProps<{
  tier: AdminBadgeTierRow | null
  colorChoices: string[]
  store: AdminBadgesStore
}>()

const emit = defineEmits<{ saved: [] }>()
const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const toast = useToast()

const KEY_PATTERN = /^[a-z0-9][a-z0-9_-]*$/

const form = reactive({
  key: '',
  nameZh: '',
  nameEn: '',
  icon: 'i-lucide-award',
  color: '',
  enabled: true
})

const submitting = ref(false)
const errorMessage = ref('')
const fieldErrors = ref<Record<string, string[]>>({})
const keyInvalid = ref(false)

const isEdit = computed(() => Boolean(props.tier?.key))
const title = computed(() => (isEdit.value ? t('admin.badges.tiers.form.editTitle') : t('admin.badges.tiers.form.createTitle')))

/** 新等级追加到末尾：位次决定默认色，也决定列表里的位置。 */
const fallbackPosition = computed(() => props.store.tiers.value.length)

const canSubmit = computed(() => {
  if (submitting.value) return false
  if (!form.nameZh.trim() || !form.nameEn.trim() || !form.icon.trim()) return false
  if (!isEdit.value && !KEY_PATTERN.test(form.key.trim())) return false
  return true
})

watch(open, (value) => {
  if (!value) return
  errorMessage.value = ''
  fieldErrors.value = {}
  keyInvalid.value = false
  const tier = props.tier
  form.key = tier?.key ?? ''
  form.nameZh = tier?.name?.zh ?? ''
  form.nameEn = tier?.name?.en ?? ''
  form.icon = tier?.icon || 'i-lucide-award'
  form.color = normalizeTierColor(tier?.color) ?? ''
  form.enabled = tier?.enabled ?? true
})

function close() {
  open.value = false
}

function resolvedColor(): string {
  if (form.color) return form.color
  return defaultTierColorName(props.tier?.position ?? fallbackPosition.value)
}

async function submit() {
  if (!canSubmit.value) return
  errorMessage.value = ''
  fieldErrors.value = {}
  keyInvalid.value = false
  submitting.value = true
  try {
    const payload = {
      nameZh: form.nameZh.trim(),
      nameEn: form.nameEn.trim(),
      icon: form.icon,
      color: resolvedColor(),
      enabled: form.enabled
    }
    if (isEdit.value && props.tier) {
      await props.store.updateTier(props.tier.key, payload)
      toast.add({ title: t('admin.badges.tiers.toast.updated'), color: 'success', icon: 'i-lucide-check', duration: 5000 })
    } else {
      await props.store.createTier({ key: form.key.trim(), ...payload })
      toast.add({ title: t('admin.badges.tiers.toast.created'), color: 'success', icon: 'i-lucide-check', duration: 5000 })
    }
    emit('saved')
    close()
  } catch (error) {
    const fields = apiErrorFields(error)
    fieldErrors.value = fields
    if (fields.key) keyInvalid.value = true
    if (Object.keys(fields).length === 0) {
      errorMessage.value = apiErrorMessage(error) || t('admin.badges.tiers.form.saveFailed')
    }
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" :ui="{ content: 'sm:max-w-xl' }" @update:open="(value) => { if (!value) close() }">
    <template #content>
      <div class="flex max-h-[90vh] flex-col">
        <div class="border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ title }}</h3>
        </div>

        <div class="min-h-0 flex-1 space-y-3 overflow-y-auto px-5 py-4">
          <div>
            <UFormField
              :label="t('admin.badges.tiers.form.key')"
              :error="fieldErrors.key?.[0] || (keyInvalid ? t('admin.badges.tiers.form.keyInvalid') : '')"
            >
              <UInput v-model="form.key" :disabled="isEdit" class="w-full" autocomplete="off" />
            </UFormField>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400" data-field-hint data-field-hint-for="key">
              {{ t('admin.badges.tiers.form.keyHint') }}
            </p>
          </div>

          <div class="grid gap-3 sm:grid-cols-2">
            <UFormField :label="t('admin.badges.tiers.form.nameZh')" :error="fieldErrors.nameZh?.[0]">
              <UInput v-model="form.nameZh" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.badges.tiers.form.nameEn')" :error="fieldErrors.nameEn?.[0]">
              <UInput v-model="form.nameEn" class="w-full" />
            </UFormField>
          </div>

          <SFAdminTierColorField
            v-model="form.color"
            :choices="props.colorChoices"
            :label="t('admin.badges.tiers.form.color')"
            :auto-label="t('admin.badges.tiers.color.auto')"
            :picker-label="t('admin.badges.tiers.color.picker')"
            :hex-label="t('admin.badges.tiers.color.hex')"
            :apply-label="t('admin.badges.tiers.color.apply')"
            :invalid-label="t('admin.badges.tiers.color.invalid')"
            :low-contrast-label="t('admin.badges.tiers.color.lowContrast')"
          />

          <SFAdminBadgeIconField
            v-model="form.icon"
            :label="t('admin.badges.form.icon')"
            :more-label="t('admin.badges.icon.more')"
            :less-label="t('admin.badges.icon.less')"
            :accent="tierColorVar(form.color)"
            :error="fieldErrors.icon?.[0]"
          />

          <UFormField :label="t('admin.badges.form.enabled')">
            <USwitch v-model="form.enabled" :aria-label="t('admin.badges.form.enabled')" />
          </UFormField>

          <p v-if="errorMessage" class="text-sm text-red-600 dark:text-red-400">{{ errorMessage }}</p>
        </div>

        <div class="flex items-center justify-end gap-2 border-t border-slate-200 px-5 py-3 dark:border-zinc-800">
          <UButton color="neutral" variant="ghost" @click="close">{{ t('admin.badges.actions.cancel') }}</UButton>
          <UButton color="primary" :loading="submitting" :disabled="!canSubmit" @click="submit">
            {{ t('admin.badges.actions.save') }}
          </UButton>
        </div>
      </div>
    </template>
  </UModal>
</template>
