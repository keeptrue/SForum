<script setup lang="ts">
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import { useAccountSecurityApi } from '~/composables/identity/useAccountSecurityApi'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import type { APIToken } from '~/composables/identity/useAccountSecurityApi'
import SFSettingsShell from '~/components/settings/SFSettingsShell.vue'

type TokenScopeOption = {
  key: string
  labelKey: string
  descriptionKey: string
  group: 'content' | 'moderation' | 'admin'
}

type TokenScopePreset = {
  key: string
  icon: string
  scopeKeys: string[]
}

const TOKEN_SCOPE_OPTIONS: TokenScopeOption[] = [
  { key: FORUM_PERMISSIONS.topicCreate, labelKey: 'topicCreate', descriptionKey: 'topicCreateHelp', group: 'content' },
  { key: FORUM_PERMISSIONS.postCreate, labelKey: 'postCreate', descriptionKey: 'postCreateHelp', group: 'content' },
  { key: FORUM_PERMISSIONS.topicEditOwn, labelKey: 'topicEditOwn', descriptionKey: 'topicEditOwnHelp', group: 'content' },
  { key: FORUM_PERMISSIONS.postEditOwn, labelKey: 'postEditOwn', descriptionKey: 'postEditOwnHelp', group: 'content' },
  { key: FORUM_PERMISSIONS.moderationReview, labelKey: 'moderationReview', descriptionKey: 'moderationReviewHelp', group: 'moderation' },
  { key: FORUM_PERMISSIONS.moderationManage, labelKey: 'moderationManage', descriptionKey: 'moderationManageHelp', group: 'moderation' },
  { key: FORUM_PERMISSIONS.settingsSiteManage, labelKey: 'settingsSiteManage', descriptionKey: 'settingsSiteManageHelp', group: 'admin' },
  { key: FORUM_PERMISSIONS.settingsNotificationsManage, labelKey: 'settingsNotificationsManage', descriptionKey: 'settingsNotificationsManageHelp', group: 'admin' },
  { key: FORUM_PERMISSIONS.forumSettingsManage, labelKey: 'forumSettingsManage', descriptionKey: 'forumSettingsManageHelp', group: 'admin' },
  { key: FORUM_PERMISSIONS.attachmentUpload, labelKey: 'attachmentUpload', descriptionKey: 'attachmentUploadHelp', group: 'content' }
]

const TOKEN_SCOPE_PRESETS: TokenScopePreset[] = [
  { key: 'publishing', icon: 'i-lucide-pencil-line', scopeKeys: [FORUM_PERMISSIONS.topicCreate, FORUM_PERMISSIONS.postCreate, FORUM_PERMISSIONS.attachmentUpload] },
  { key: 'ownContent', icon: 'i-lucide-file-pen-line', scopeKeys: [FORUM_PERMISSIONS.topicCreate, FORUM_PERMISSIONS.postCreate, FORUM_PERMISSIONS.topicEditOwn, FORUM_PERMISSIONS.postEditOwn, FORUM_PERMISSIONS.attachmentUpload] },
  { key: 'moderation', icon: 'i-lucide-shield-check', scopeKeys: [FORUM_PERMISSIONS.moderationReview, FORUM_PERMISSIONS.moderationManage] },
  { key: 'operations', icon: 'i-lucide-settings-2', scopeKeys: [FORUM_PERMISSIONS.settingsSiteManage, FORUM_PERMISSIONS.settingsNotificationsManage, FORUM_PERMISSIONS.forumSettingsManage] }
]

const scopeGroups: Array<TokenScopeOption['group']> = ['content', 'moderation', 'admin']

const { t } = useI18n()
const toast = useToast()
const tokensApi = useAccountSecurityApi()
const { can } = usePermissions()
const { format: formatSiteDateTime } = useSiteDateTime()

useSForumSeo({
  title: () => t('accessTokensSettings.metaTitle'),
  description: () => t('accessTokensSettings.metaDescription'),
  type: 'website',
  noindex: true
})

const tokens = ref<APIToken[]>([])
const tokensLoading = ref(false)
const tokenBusy = ref(false)
const createdPlaintext = ref('')
const activeTokenTab = ref<'create' | 'manage'>('create')
const form = reactive<{ name: string, scopes: string[] }>({
  name: '',
  scopes: [FORUM_PERMISSIONS.topicCreate, FORUM_PERMISSIONS.postCreate]
})

const availableScopes = computed(() => TOKEN_SCOPE_OPTIONS.filter(scope => can(scope.key)))
const availableScopeKeys = computed(() => new Set(availableScopes.value.map(scope => scope.key)))
const selectedScopes = computed(() => form.scopes.filter(scope => availableScopeKeys.value.has(scope)))
const canCreateToken = computed(() => form.name.trim().length > 0 && selectedScopes.value.length > 0 && !tokenBusy.value)
const availablePresets = computed(() => TOKEN_SCOPE_PRESETS
  .map(preset => ({
    ...preset,
    availableScopeKeys: preset.scopeKeys.filter(scope => availableScopeKeys.value.has(scope))
  }))
  .filter(preset => preset.availableScopeKeys.length > 0)
)

const groupedScopes = computed(() => scopeGroups.map(group => ({
  group,
  items: availableScopes.value.filter(scope => scope.group === group)
})).filter(group => group.items.length > 0))

const selectedScopeSummary = computed(() => selectedScopes.value.length
  ? selectedScopes.value.join(', ')
  : t('accessTokensSettings.noScopesSelected')
)
const tokenTabs = computed(() => [
  { label: t('accessTokensSettings.tabs.create'), value: 'create' },
  { label: t('accessTokensSettings.tabs.manage'), value: 'manage', badge: tokensLoading.value ? undefined : tokens.value.length }
])

function formatTime(iso: string): string {
  return formatSiteDateTime(iso)
}

function scopeLabel(scope: TokenScopeOption): string {
  return t(`accessTokensSettings.scopes.${scope.labelKey}`)
}

function scopeDescription(scope: TokenScopeOption): string {
  return t(`accessTokensSettings.scopes.${scope.descriptionKey}`)
}

function presetTitle(preset: TokenScopePreset): string {
  return t(`accessTokensSettings.presets.${preset.key}.title`)
}

function presetDescription(preset: TokenScopePreset): string {
  return t(`accessTokensSettings.presets.${preset.key}.description`)
}

function applyPreset(scopeKeys: string[]) {
  form.scopes = [...scopeKeys]
  toast.add({
    color: 'neutral',
    icon: 'i-lucide-list-checks',
    title: t('accessTokensSettings.presetApplied'),
    duration: 5000
  })
}

function resetRecommended() {
  const defaultPreset = TOKEN_SCOPE_PRESETS[0]
  const recommended = defaultPreset?.scopeKeys.filter(scope => availableScopeKeys.value.has(scope)) || []
  form.scopes = recommended.length > 0 ? recommended : availableScopes.value.slice(0, 1).map(scope => scope.key)
  toast.add({
    color: 'neutral',
    icon: 'i-lucide-rotate-ccw',
    title: t('accessTokensSettings.recommendedRestored'),
    duration: 5000
  })
}

async function loadTokens() {
  tokensLoading.value = true
  try {
    const result = await tokensApi.listAPITokens()
    tokens.value = result.items || []
  } catch (error) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-alert-triangle', title: apiErrorMessage(error) || t('accessTokensSettings.loadFailed') })
  } finally {
    tokensLoading.value = false
  }
}

async function createToken() {
  if (!canCreateToken.value) {
    return
  }
  tokenBusy.value = true
  createdPlaintext.value = ''
  try {
    const created = await tokensApi.createAPIToken({ name: form.name.trim(), scopes: selectedScopes.value })
    createdPlaintext.value = created.token
    form.name = ''
    toast.add({ color: 'primary', icon: 'i-lucide-check', title: t('accessTokensSettings.created'), duration: 5000 })
    await loadTokens()
    activeTokenTab.value = 'manage'
  } catch (error) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-alert-triangle', title: apiErrorMessage(error) || t('accessTokensSettings.saveFailed') })
  } finally {
    tokenBusy.value = false
  }
}

async function revokeToken(token: APIToken) {
  try {
    await tokensApi.revokeAPIToken(token.id)
    toast.add({ color: 'primary', icon: 'i-lucide-check', title: t('accessTokensSettings.revoked'), duration: 5000 })
    await loadTokens()
  } catch (error) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-alert-triangle', title: apiErrorMessage(error) || t('accessTokensSettings.saveFailed') })
  }
}

async function rotateToken(token: APIToken) {
  try {
    const created = await tokensApi.rotateAPIToken(token.id)
    createdPlaintext.value = created.token
    toast.add({ color: 'primary', icon: 'i-lucide-check', title: t('accessTokensSettings.rotated'), duration: 5000 })
    await loadTokens()
  } catch (error) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-alert-triangle', title: apiErrorMessage(error) || t('accessTokensSettings.saveFailed') })
  }
}

async function copyPlaintext() {
  if (!createdPlaintext.value) {
    return
  }
  try {
    await navigator.clipboard.writeText(createdPlaintext.value)
    toast.add({ color: 'primary', icon: 'i-lucide-check', title: t('accessTokensSettings.copied'), duration: 5000 })
  } catch {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-alert-triangle', title: t('accessTokensSettings.copyFailed') })
  }
}

onMounted(() => {
  resetRecommended()
  loadTokens()
})
</script>

<template>
  <SFSettingsShell
    class="sforum-settings-tokens"
    data-sforum-island-body="identity.component.personal_access_tokens"
    active="tokens"
    title-id="personal-access-tokens-title"
    :title="t('accessTokensSettings.title')"
    :description="t('accessTokensSettings.intro')"
    :rail-label="t('accessTokensSettings.rail.ariaLabel')"
  >
    <SFTabs
      v-model="activeTokenTab"
      class="mt-2"
      :items="tokenTabs"
      :aria-label="t('accessTokensSettings.tabs.ariaLabel')"
    />

    <div
      v-if="createdPlaintext"
      class="mt-4 rounded-md border border-amber-200 bg-amber-50 p-3 dark:border-amber-900/50 dark:bg-amber-950/30"
      data-testid="personal-access-token-secret"
    >
      <p class="text-xs font-medium text-amber-800 dark:text-amber-200">
        {{ t('accessTokensSettings.onceHint') }}
      </p>
      <p class="mt-2 break-all font-mono text-sm text-slate-900 dark:text-zinc-100">
        {{ createdPlaintext }}
      </p>
      <SFButton class="mt-2" variant="secondary" size="sm" @click="copyPlaintext">
        <UIcon name="i-lucide-copy" class="mr-1" />
        {{ t('accessTokensSettings.copy') }}
      </SFButton>
    </div>

    <SFCard v-if="activeTokenTab === 'create'" class="mt-4 p-4" data-testid="personal-access-token-create-tab">
      <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div class="min-w-0">
          <h2 class="text-base font-semibold text-slate-900 dark:text-zinc-50">
            {{ t('accessTokensSettings.createTitle') }}
          </h2>
          <p class="mt-1 text-sm text-muted">
            {{ t('accessTokensSettings.createDescription') }}
          </p>
        </div>
        <SFButton variant="secondary" size="sm" @click="resetRecommended">
          <UIcon name="i-lucide-rotate-ccw" class="mr-1" />
          {{ t('accessTokensSettings.restoreRecommended') }}
        </SFButton>
      </div>

      <label class="mt-5 block text-sm">
        <span class="mb-1 block font-medium text-slate-700 dark:text-zinc-200">{{ t('accessTokensSettings.nameLabel') }}</span>
        <input
          v-model="form.name"
          class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 text-sm dark:border-zinc-700 dark:bg-zinc-950"
          type="text"
          :placeholder="t('accessTokensSettings.namePlaceholder')"
        >
      </label>

      <section class="mt-5">
        <div class="flex flex-col gap-1 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-50">
              {{ t('accessTokensSettings.presetsTitle') }}
            </h3>
            <p class="text-xs text-muted">{{ t('accessTokensSettings.presetsDescription') }}</p>
          </div>
          <p class="text-xs font-mono text-slate-500 dark:text-zinc-400">
            {{ selectedScopeSummary }}
          </p>
        </div>

        <div class="mt-3 grid gap-3 sm:grid-cols-2">
          <button
            v-for="preset in availablePresets"
            :key="preset.key"
            type="button"
            class="rounded-md border border-slate-200 bg-white p-3 text-left transition hover:border-teal-300 hover:bg-teal-50/60 dark:border-zinc-800 dark:bg-zinc-950 dark:hover:border-teal-800 dark:hover:bg-teal-950/20"
            @click="applyPreset(preset.availableScopeKeys)"
          >
            <span class="flex items-start gap-3">
              <UIcon :name="preset.icon" class="mt-0.5 size-5 shrink-0 text-teal-700 dark:text-teal-300" aria-hidden="true" />
              <span class="min-w-0">
                <strong class="block text-sm text-slate-900 dark:text-zinc-50">{{ presetTitle(preset) }}</strong>
                <span class="mt-1 block text-xs text-muted">{{ presetDescription(preset) }}</span>
              </span>
            </span>
          </button>
        </div>
      </section>

      <section class="mt-5 space-y-4">
        <div
          v-for="group in groupedScopes"
          :key="group.group"
          class="rounded-md border border-slate-200 p-3 dark:border-zinc-800"
        >
          <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-50">
            {{ t(`accessTokensSettings.groups.${group.group}`) }}
          </h3>
          <div class="mt-3 grid gap-2">
            <label
              v-for="scope in group.items"
              :key="scope.key"
              class="flex items-start gap-3 rounded-md border border-slate-100 bg-slate-50/70 p-3 text-sm dark:border-zinc-800 dark:bg-zinc-950/60"
            >
              <input
                v-model="form.scopes"
                type="checkbox"
                :value="scope.key"
                class="mt-1 size-4 shrink-0"
              >
              <span class="min-w-0">
                <strong class="block text-slate-900 dark:text-zinc-50">{{ scopeLabel(scope) }}</strong>
                <span class="mt-0.5 block text-xs text-muted">{{ scopeDescription(scope) }}</span>
                <span class="mt-1 block break-all font-mono text-xs text-slate-500 dark:text-zinc-400">{{ scope.key }}</span>
              </span>
            </label>
          </div>
        </div>
      </section>

      <SFAlert
        v-if="availableScopes.length === 0"
        class="mt-4"
        variant="info"
        :title="t('accessTokensSettings.noAvailableScopesTitle')"
        :description="t('accessTokensSettings.noAvailableScopesDescription')"
      >
        <template #icon>
          <UIcon name="i-lucide-info" class="size-4" aria-hidden="true" />
        </template>
      </SFAlert>

      <div class="mt-5 flex flex-wrap items-center gap-3">
        <SFButton
          variant="primary"
          size="sm"
          :disabled="!canCreateToken"
          @click="createToken"
        >
          <UIcon name="i-lucide-key-round" class="mr-1" />
          {{ tokenBusy ? t('accessTokensSettings.creating') : t('accessTokensSettings.create') }}
        </SFButton>
        <p v-if="selectedScopes.length === 0" class="text-xs text-amber-700 dark:text-amber-300">
          {{ t('accessTokensSettings.scopeRequired') }}
        </p>
      </div>
    </SFCard>

    <SFCard v-else class="mt-4 p-0 overflow-hidden" data-testid="personal-access-token-manage-tab">
      <div class="border-b border-slate-100 p-4 dark:border-zinc-800">
        <h2 class="text-base font-semibold text-slate-900 dark:text-zinc-50">
          {{ t('accessTokensSettings.manageTitle') }}
        </h2>
        <p class="mt-1 text-sm text-muted">
          {{ t('accessTokensSettings.manageDescription') }}
        </p>
      </div>
      <div v-if="tokensLoading" class="p-4 text-sm text-slate-500">
        {{ t('accessTokensSettings.loading') }}
      </div>
      <ul v-else-if="tokens.length" class="divide-y divide-slate-100 dark:divide-zinc-800">
        <li
          v-for="token in tokens"
          :key="token.id"
          class="flex flex-col gap-3 p-4 lg:flex-row lg:items-center lg:justify-between"
        >
          <div class="min-w-0">
            <p class="font-medium text-slate-900 dark:text-zinc-100">
              {{ token.name }}
              <span class="ml-2 font-mono text-xs text-slate-400">{{ token.prefix }}...</span>
            </p>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('accessTokensSettings.createdAt') }}: {{ formatTime(token.createdAt) }}
            </p>
            <div class="mt-2 flex flex-wrap gap-1.5">
              <span
                v-for="scope in token.scopes"
                :key="scope"
                class="rounded-md bg-slate-100 px-2 py-1 font-mono text-xs text-slate-600 dark:bg-zinc-800 dark:text-zinc-300"
              >
                {{ scope }}
              </span>
            </div>
          </div>
          <div class="flex shrink-0 gap-2">
            <SFButton variant="ghost" size="sm" @click="rotateToken(token)">
              <UIcon name="i-lucide-refresh-cw" class="mr-1" />
              {{ t('accessTokensSettings.rotate') }}
            </SFButton>
            <SFButton variant="ghost" size="sm" @click="revokeToken(token)">
              <UIcon name="i-lucide-trash-2" class="mr-1" />
              {{ t('accessTokensSettings.revoke') }}
            </SFButton>
          </div>
        </li>
      </ul>
      <SFEmptyState
        v-else
        class="p-4"
        icon-label="PAT"
        :title="t('accessTokensSettings.emptyTitle')"
        :description="t('accessTokensSettings.emptyDescription')"
      />
    </SFCard>

    <template #rail>
      <section class="sforum-settings__rail-section">
        <div class="sforum-settings__rail-head">
          <h2>{{ t('accessTokensSettings.rail.tokensTitle') }}</h2>
          <span>{{ t('accessTokensSettings.rail.currentPage') }}</span>
        </div>
        <div class="sforum-settings__summary">
          <strong>{{ tokensLoading ? '-' : tokens.length }}</strong>
          <span>{{ t('accessTokensSettings.rail.tokensLabel') }}</span>
        </div>
        <p class="sforum-settings__rail-help">{{ t('accessTokensSettings.rail.tokensHelp') }}</p>
      </section>

      <section class="sforum-settings__rail-section">
        <div class="sforum-settings__rail-head">
          <h2>{{ t('accessTokensSettings.rail.scopeTitle') }}</h2>
          <span>{{ t('accessTokensSettings.rail.recommended') }}</span>
        </div>
        <p class="sforum-settings__rail-help">{{ t('accessTokensSettings.rail.scopeHelp') }}</p>
      </section>
    </template>
  </SFSettingsShell>
</template>
