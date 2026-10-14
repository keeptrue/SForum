<script setup lang="ts">
import SFAdminAIAutoActionBlock from '~/components/admin/ai/blocks/SFAdminAIAutoActionBlock.vue'
import SFAdminAIModelField from '~/components/admin/ai/blocks/SFAdminAIModelField.vue'
import SFAdminAIRedactionBlock from '~/components/admin/ai/blocks/SFAdminAIRedactionBlock.vue'
import SFAdminAIMultimodalBlock from '~/components/admin/ai/blocks/SFAdminAIMultimodalBlock.vue'
import SFAdminAIProviderPicker from '~/components/admin/ai/blocks/SFAdminAIProviderPicker.vue'
import { ADMIN_AI_KEY, MICRO_PER_UNIT } from '~/composables/admin/useAdminAI'
import { ADMIN_AI_DRAFT_KEY } from '~/composables/admin/useAdminAISettingsForm'
import { DEFAULT_TOOL_CALLS_PER_REPLY, toolCallsPerReplyUsesDefault } from '~/utils/admin/adminAI'

const { t, te } = useI18n()
// 数据由页面壳统一持有并注入；页签只渲染，不重新拉取。
const ai = inject(ADMIN_AI_KEY)!
// 草稿同样由页面壳持有：回复提示词在另一个页签里编辑，两者必须落在同一份表单上，
// 保存也只有一个（页面壳底部的操作条）。
const draft = inject(ADMIN_AI_DRAFT_KEY)!
const { form, selectedProviderId, selectedProfile, isCustom, profileIncomplete, providerOptions, canManage, readOnly } = draft

// 密钥输入按 profile 暂存；它只在下一次保存密钥之前存在。
const credentials = reactive<Record<string, string>>({})
const advancedOpen = ref(false)

const localizedWarnings = computed(() => ai.warnings.value.map((warning) => {
  const [code, detail] = warning.split(':')
  const key = `admin.ai.warnings.${code}`
  if (te(key)) return t(key)
  return detail ? `${code} (${detail})` : code
}))

const protocolOptions = computed(() => [
  { label: 'OpenAI 兼容', value: 'openai-chat' },
  { label: 'Claude', value: 'anthropic-messages' }
])

async function submitCredential(profileId: string) {
  const apiKey = (credentials[profileId] || '').trim()
  if (!apiKey) return
  // 后端同样会拒绝，这里挡住是为了不让运营者填完密钥之后才看到失败。
  if (!ai.encryptionEnabled.value) return
  if (await ai.saveCredential(profileId, apiKey)) credentials[profileId] = ''
}

function setNumber(path: 'rateLimitPerMinute' | 'extensionDailyQuota' | 'userDailyQuota', value: string | number) {
  if (!form.value) return
  form.value.gates[path] = Math.max(0, Math.round(Number(value || 0)))
}

// toolCallsPerReply 用指针语义：清空表示「未设置，按默认 3 生效」，0 表示停用
// 工具调用。两者对服务端是不同的状态，界面必须都能表达。
function setToolCallsPerReply(value: string | number) {
  if (!form.value) return
  const raw = typeof value === 'string' ? value.trim() : value
  if (raw === '' || raw === null || raw === undefined || Number.isNaN(Number(raw))) {
    delete form.value.gates.toolCallsPerReply
    return
  }
  const parsed = Math.round(Number(raw))
  if (parsed < 0 || parsed > 5) return
  form.value.gates.toolCallsPerReply = parsed
}

// 恢复「未设置」：让站点以后能跟随后端默认值的调整，而不是被一次点击固化。
function restoreDefaultToolCalls() {
  if (!form.value) return
  delete form.value.gates.toolCallsPerReply
}

// 输入框里显示的是「当前生效值」：未设置时显示默认，但那不是已保存的显式值。
function toolCallsUsesDefault() {
  return toolCallsPerReplyUsesDefault(form.value?.gates.toolCallsPerReply)
}

function toolCallsValue() {
  return toolCallsUsesDefault() ? DEFAULT_TOOL_CALLS_PER_REPLY : form.value?.gates.toolCallsPerReply
}

function setBudgetYuan(value: string | number) {
  if (!form.value) return
  form.value.gates.monthlyBudgetMicros = Math.round(Number(value || 0) * MICRO_PER_UNIT)
}

function budgetYuan() {
  return form.value ? form.value.gates.monthlyBudgetMicros / MICRO_PER_UNIT : 0
}

function toggleAdvanced() {
  advancedOpen.value = !advancedOpen.value
}

function setMicro(field: 'inputPerMillionMicros' | 'outputPerMillionMicros', value: string | number) {
  const profile = selectedProfile.value
  if (!profile) return
  profile.price[field] = Math.round(Number(value || 0) * MICRO_PER_UNIT)
}

// 缺省（undefined）视为支持工具调用；只有明确关闭时才写 false。
function setSupportsTools(value: boolean | 'indeterminate') {
  const profile = selectedProfile.value
  if (!profile) return
  profile.supportsTools = value === true
}
</script>

<template>
  <div v-if="form" class="flex min-w-0 flex-col gap-4">
    <UAlert
      v-if="!ai.encryptionEnabled.value"
      color="warning"
      variant="soft"
      icon="i-lucide-shield-alert"
      :title="t('admin.ai.encryptionMissing')"
    />
    <UAlert
      v-for="warning in localizedWarnings"
      :key="warning"
      color="warning"
      variant="soft"
      icon="i-lucide-info"
      :title="warning"
    />

    <SFAdminAIProviderPicker
      :options="providerOptions"
      :selected-id="selectedProviderId"
      :credential-status="ai.credentialStatus.value"
      :disabled="!canManage"
      @select="draft.selectProvider"
    />
    <section v-if="selectedProfile" class="min-w-0 rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
      <div class="border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
        <p class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.ai.providerConfig') }}</p>
        <p v-if="profileIncomplete" class="mt-1 text-xs text-amber-600 dark:text-amber-400">{{ t('admin.ai.profileIncomplete') }}</p>
      </div>
      <div class="flex flex-col gap-3 px-4 py-4">
        <UFormField v-if="isCustom" :label="t('admin.ai.customName')">
          <UInput v-model="selectedProfile.label" :disabled="!canManage" class="w-full" />
        </UFormField>

        <UFormField
          :label="t('admin.ai.credential')"
          :help="ai.encryptionEnabled.value ? t('admin.ai.credentialHint') : t('admin.ai.credentialBlockedHint')"
        >
          <div class="flex min-w-0 flex-col gap-2 sm:flex-row">
            <UInput
              v-model="credentials[selectedProfile.id]"
              type="password"
              autocomplete="off"
              :placeholder="ai.credentialStatus.value[selectedProfile.id] ? t('admin.ai.credentialPlaceholderKeep') : t('admin.ai.credentialPlaceholder')"
              :disabled="!canManage || !ai.encryptionEnabled.value"
              class="min-w-0 flex-1"
            />
            <UButton
              icon="i-lucide-key-round"
              color="neutral"
              variant="subtle"
              :loading="ai.busy.value === `credential:${selectedProfile.id}`"
              :disabled="!canManage || !ai.encryptionEnabled.value || !(credentials[selectedProfile.id] || '').trim()"
              @click="submitCredential(selectedProfile.id)"
            >
              {{ t('admin.ai.credentialSave') }}
            </UButton>
          </div>
        </UFormField>

        <div class="grid grid-cols-1 gap-3 lg:grid-cols-3">
          <UFormField v-if="isCustom" :label="t('admin.ai.protocol')">
            <USelect v-model="selectedProfile.protocol" :items="protocolOptions" :disabled="!canManage" class="w-full" />
          </UFormField>
          <UFormField :label="t('admin.ai.baseUrl')">
            <UInput v-model="selectedProfile.baseUrl" :disabled="!canManage" class="w-full" />
          </UFormField>
          <UFormField :label="t('admin.ai.model')">
            <!-- 模型标识既可以手填，也可以一键从供应商读取列表后选择。探测用当前
                 表单里的地址与密钥（未保存也能用），因此不会逼运营者先存一份没有
                 模型的配置。 -->
            <SFAdminAIModelField
              v-model="selectedProfile.model"
              :profile-id="selectedProfile.id"
              :protocol="selectedProfile.protocol"
              :base-url="selectedProfile.baseUrl"
              :api-key="credentials[selectedProfile.id] || ''"
              :credential-configured="ai.credentialStatus.value[selectedProfile.id] === true"
              :disabled="!canManage"
            />
          </UFormField>
        </div>

        <div>
          <UButton
            size="xs"
            color="neutral"
            variant="ghost"
            :icon="advancedOpen ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
            @click="toggleAdvanced"
          >
            {{ advancedOpen ? t('admin.ai.advancedHide') : t('admin.ai.advancedShow') }}
          </UButton>
        </div>

        <div v-if="advancedOpen" class="grid grid-cols-1 gap-3 rounded-md bg-slate-50 p-3 dark:bg-zinc-950/60 lg:grid-cols-3">
          <UFormField :label="t('admin.ai.costClass')">
            <USelect v-model="selectedProfile.costClass" :items="[
              { label: t('admin.ai.costEconomy'), value: 'economy' },
              { label: t('admin.ai.costStandard'), value: 'standard' },
              { label: t('admin.ai.costPremium'), value: 'premium' }
            ]" :disabled="!canManage" class="w-full" />
          </UFormField>
          <UFormField :label="t('admin.ai.maxTokens')">
            <UInput v-model.number="selectedProfile.defaults.maxTokens" type="number" :disabled="!canManage" class="w-full" />
          </UFormField>
          <UFormField :label="t('admin.ai.timeoutMs')">
            <UInput v-model.number="selectedProfile.defaults.timeoutMs" type="number" :disabled="!canManage" class="w-full" />
          </UFormField>
          <UFormField :label="t('admin.ai.supportsTools')" :help="t('admin.ai.supportsToolsHint')">
            <UCheckbox
              :model-value="selectedProfile?.supportsTools !== false"
              :disabled="!canManage"
              @update:model-value="setSupportsTools"
            />
          </UFormField>
          <UFormField :label="t('admin.ai.priceInput')" :help="t('admin.ai.priceHint')">
            <UInput
              :model-value="selectedProfile.price.inputPerMillionMicros / MICRO_PER_UNIT"
              type="number"
              step="0.01"
              :disabled="!canManage"
              class="w-full"
              @update:model-value="value => setMicro('inputPerMillionMicros', value)"
            />
          </UFormField>
          <UFormField :label="t('admin.ai.priceOutput')">
            <UInput
              :model-value="selectedProfile.price.outputPerMillionMicros / MICRO_PER_UNIT"
              type="number"
              step="0.01"
              :disabled="!canManage"
              class="w-full"
              @update:model-value="value => setMicro('outputPerMillionMicros', value)"
            />
          </UFormField>
        </div>
      </div>
    </section>

    <section class="min-w-0 rounded-lg border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
      <p class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.ai.gates') }}</p>
      <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.ai.gatesIntro') }}</p>
      <div class="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <UFormField :label="t('admin.ai.rateLimit')">
          <UInput :model-value="form.gates.rateLimitPerMinute" type="number" :disabled="!canManage" class="w-full"
            @update:model-value="value => setNumber('rateLimitPerMinute', value)" />
        </UFormField>
        <UFormField :label="t('admin.ai.extensionQuota')">
          <UInput :model-value="form.gates.extensionDailyQuota" type="number" :disabled="!canManage" class="w-full"
            @update:model-value="value => setNumber('extensionDailyQuota', value)" />
        </UFormField>
        <UFormField :label="t('admin.ai.userQuota')">
          <UInput :model-value="form.gates.userDailyQuota" type="number" :disabled="!canManage" class="w-full"
            @update:model-value="value => setNumber('userDailyQuota', value)" />
        </UFormField>
        <UFormField :label="t('admin.ai.monthlyBudget')">
          <UInput :model-value="budgetYuan()" type="number" step="0.01" :disabled="!canManage" class="w-full"
            @update:model-value="setBudgetYuan" />
        </UFormField>
        <UFormField :label="t('admin.ai.toolCalls')" :help="t('admin.ai.toolCallsHint')">
          <div class="flex min-w-0 items-center gap-2">
            <UInput
              :model-value="toolCallsValue()"
              type="number"
              min="0"
              max="5"
              :placeholder="String(DEFAULT_TOOL_CALLS_PER_REPLY)"
              :disabled="!canManage"
              class="min-w-0 flex-1"
              @update:model-value="setToolCallsPerReply"
            />
            <UButton
              size="xs"
              color="neutral"
              variant="ghost"
              icon="i-lucide-rotate-ccw"
              :disabled="!canManage || toolCallsUsesDefault()"
              :aria-label="t('admin.ai.toolCallsRestore')"
              :title="t('admin.ai.toolCallsRestore')"
              @click="restoreDefaultToolCalls"
            />
          </div>
          <template #help>
            <span class="text-xs">{{ t('admin.ai.toolCallsHint') }}</span>
            <span v-if="toolCallsUsesDefault()" class="mt-0.5 block text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.ai.toolCallsDefaultState') }}
            </span>
            <span v-else class="mt-0.5 block text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.ai.toolCallsExplicitState') }}
            </span>
          </template>
        </UFormField>
      </div>
    </section>

    <SFAdminAIAutoActionBlock v-model:form="form" v-model:disabled="readOnly" />

    <SFAdminAIRedactionBlock v-model:form="form" v-model:disabled="readOnly" />

    <SFAdminAIMultimodalBlock v-model:form="form" v-model:disabled="readOnly" />
  </div>
</template>
