<script setup lang="ts">
import { useAuthProviders } from '~/composables/identity/useAuthProviders'
import { useAccountSecurityApi } from '~/composables/identity/useAccountSecurityApi'
/**
 * 账号登录方式：外部身份（redacted 列表 / link / unlink / inert）。
 * 展示名/图标只消费 Host catalog；Core 不硬编码供应商品牌。
 * 敏感操作依赖 Host session-bound recent-auth；失败时引导重新登录（step-up）。
 */

import type { ExternalIdentityItem } from '~/composables/identity/useAccountSecurityApi'
import type { PublicAuthProvider } from '~/composables/identity/useAuthProviders'
import { authProviderDisplayMeta } from '~/composables/identity/useAuthProviders'
import { apiErrorMessage, apiErrorReason } from '~/composables/useApiClient'
import { buildAuthPageLink } from '~/utils/identity/authReturn'

const props = withDefaults(defineProps<{
  returnPath?: string
  showHeading?: boolean
}>(), {
  returnPath: '/settings/security/login-methods',
  showHeading: true
})

const { t } = useI18n()
const toast = useToast()
const localePath = useLocalePath()
const securityApi = useAccountSecurityApi()
const {
  linkProviders,
  providers: catalogProviders,
  pending: catalogPending,
  redirectToProvider,
  refresh: refreshCatalog
} = useAuthProviders()
const { format: formatSiteDateTime } = useSiteDateTime()

const identities = ref<ExternalIdentityItem[]>([])
const identitiesPending = ref(true)
const identitiesError = ref('')
const linkStartingId = ref('')
const unlinkingId = ref<number | null>(null)

// session-bound recent-auth / step-up 门控：Host 拒绝后展示，引导重新登录。
const recentAuthRequired = ref(false)
const surfaceError = ref('')

const activeIdentities = computed(() =>
  identities.value.filter(item => item.status === 'active' || item.status === 'inert')
)

const activeProviderIds = computed(() => {
  const set = new Set<string>()
  for (const item of identities.value) {
    if (item.status === 'active') {
      set.add(item.providerId)
    }
  }
  return set
})

/** Host 已开放 link 且当前账号尚未 active 绑定的提供方。 */
const availableLinkProviders = computed(() =>
  linkProviders.value.filter(provider => !activeProviderIds.value.has(provider.id))
)

const activeLinkCount = computed(() =>
  identities.value.filter(item => item.status === 'active').length
)

const reauthLoginLink = computed(() =>
  buildAuthPageLink(localePath('/login'), props.returnPath)
)

function catalogFor(providerId: string): PublicAuthProvider | undefined {
  return catalogProviders.value.find(item => item.id === providerId)
}

function displayFor(providerId: string) {
  const catalog = catalogFor(providerId)
  if (catalog) {
    return authProviderDisplayMeta(catalog, t('auth.providers.genericName'))
  }
  // 插件禁用/不在公共 catalog 时：Host 通用回退，不按 id 猜品牌。
  return {
    id: providerId,
    label: t('auth.providers.genericName'),
    icon: 'i-lucide-key-round',
    activatedOperations: [] as const
  }
}

function statusLabel(status: string) {
  if (status === 'active') {
    return t('accountSecurity.linkedAccounts.statusActive')
  }
  if (status === 'inert') {
    return t('accountSecurity.linkedAccounts.statusInert')
  }
  return t('accountSecurity.linkedAccounts.statusOther')
}

function formatLinkedAt(iso?: string | null) {
  if (!iso) {
    return t('accountSecurity.unknown')
  }
  return formatSiteDateTime(iso)
}

/** 仅 1 条 active 时提示可能为最后登录方式（Host 仍校验是否有密码）。 */
function mayBeLastLoginMethod(item: ExternalIdentityItem) {
  return item.status === 'active' && activeLinkCount.value <= 1
}

async function loadIdentities() {
  identitiesPending.value = true
  identitiesError.value = ''
  try {
    identities.value = await securityApi.listExternalIdentities()
  } catch (error) {
    identitiesError.value = apiErrorMessage(error) || t('accountSecurity.linkedAccounts.loadFailed')
    // 401/会话拒绝：不伪造列表。
    if (apiErrorReason(error) === 'auth.required' || apiErrorReason(error) === 'auth.recent_auth_required') {
      recentAuthRequired.value = apiErrorReason(error) === 'auth.recent_auth_required'
    }
  } finally {
    identitiesPending.value = false
  }
}

function markRecentAuthRequired(message?: string) {
  recentAuthRequired.value = true
  surfaceError.value = message || t('accountSecurity.linkedAccounts.recentAuthRequired')
}

function clearSurfaceError() {
  surfaceError.value = ''
}

function handleSensitiveError(error: unknown, fallback: string) {
  const reason = apiErrorReason(error)
  if (reason === 'auth.recent_auth_required') {
    markRecentAuthRequired()
    return
  }
  if (reason === 'auth.required') {
    markRecentAuthRequired(t('accountSecurity.linkedAccounts.sessionRequired'))
    return
  }
  if (reason === 'auth.last_login_method_required') {
    surfaceError.value = t('accountSecurity.linkedAccounts.lastMethodBlocked')
    toast.add({
      color: 'error', duration: 0,
      icon: 'i-lucide-shield-alert',
      title: t('accountSecurity.linkedAccounts.lastMethodBlocked')
    })
    return
  }
  const message = apiErrorMessage(error) || fallback
  surfaceError.value = message
  toast.add({ color: 'error', duration: 0, icon: 'i-lucide-alert-triangle', title: message })
}

async function startLink(provider: PublicAuthProvider) {
  if (linkStartingId.value) {
    return
  }
  clearSurfaceError()
  recentAuthRequired.value = false
  linkStartingId.value = provider.id
  try {
    // 回跳登录方式页；成功 Toast 由根布局 ext_auth 消费。
    await redirectToProvider(provider.id, 'link', {
      redirectHint: props.returnPath
    })
  } catch (error) {
    handleSensitiveError(error, t('accountSecurity.linkedAccounts.linkFailed'))
  } finally {
    linkStartingId.value = ''
  }
}

async function unlinkIdentity(item: ExternalIdentityItem) {
  if (item.status !== 'active' || unlinkingId.value !== null) {
    return
  }
  clearSurfaceError()
  recentAuthRequired.value = false
  unlinkingId.value = item.linkId
  try {
    await securityApi.unlinkExternalIdentity(item.linkId)
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title: t('accountSecurity.linkedAccounts.unlinked'),
      duration: 5000
    })
    await loadIdentities()
  } catch (error) {
    handleSensitiveError(error, t('accountSecurity.linkedAccounts.unlinkFailed'))
  } finally {
    unlinkingId.value = null
  }
}

async function refreshAll() {
  await Promise.all([loadIdentities(), refreshCatalog()])
}

onMounted(() => {
  void loadIdentities()
})

// 绑定成功回跳后刷新列表（根布局已 Toast）。
const route = useRoute()
watch(
  () => route.query.ext_auth,
  (reason) => {
    if (typeof reason === 'string' && reason.includes('link')) {
      void loadIdentities()
    }
  }
)

defineExpose({ refreshAll, loadIdentities })
</script>

<template>
  <section class="sf-linked-accounts" data-testid="linked-accounts-section">
    <header v-if="props.showHeading">
      <h2 class="text-lg font-semibold text-slate-900 dark:text-zinc-50">
        {{ t('accountSecurity.linkedAccounts.title') }}
      </h2>
      <p class="mt-1 text-sm text-slate-500 dark:text-zinc-400">
        {{ t('accountSecurity.linkedAccounts.intro') }}
      </p>
    </header>

    <SFAlert
      v-if="recentAuthRequired"
      variant="warning"
      class="mt-4"
      :title="t('accountSecurity.linkedAccounts.recentAuthTitle')"
      :description="t('accountSecurity.linkedAccounts.recentAuthRequired')"
      closable
      @close="recentAuthRequired = false"
    >
      <NuxtLink
        :to="reauthLoginLink"
        class="mt-2 inline-flex items-center gap-1 text-sm font-semibold text-amber-800 underline dark:text-amber-200"
      >
        <UIcon name="i-lucide-log-in" class="size-4" aria-hidden="true" />
        {{ t('accountSecurity.linkedAccounts.reauthenticate') }}
      </NuxtLink>
    </SFAlert>

    <SFAlert
      v-else-if="surfaceError"
      variant="danger"
      class="mt-4"
      :title="surfaceError"
      closable
      @close="clearSurfaceError"
    />

    <SFCard class="mt-4 p-0 overflow-hidden">
      <div v-if="identitiesPending && activeIdentities.length === 0" class="divide-y divide-slate-100 dark:divide-zinc-800">
        <div v-for="i in 2" :key="i" class="p-4">
          <SFSkeleton class="h-4 w-1/3 mb-2" />
          <SFSkeleton class="h-3 w-1/2" />
        </div>
      </div>

      <p v-else-if="identitiesError" class="p-4 text-sm text-red-600 dark:text-red-400">
        {{ identitiesError }}
      </p>

      <ul
        v-else-if="activeIdentities.length > 0"
        class="divide-y divide-slate-100 dark:divide-zinc-800"
        data-testid="linked-accounts-list"
      >
        <li
          v-for="item in activeIdentities"
          :key="item.linkId"
          class="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between"
          :data-provider-id="item.providerId"
          :data-link-status="item.status"
        >
          <div class="min-w-0 flex items-start gap-3">
            <UIcon
              :name="displayFor(item.providerId).icon"
              class="mt-0.5 size-5 shrink-0 text-slate-500 dark:text-zinc-400"
              aria-hidden="true"
            />
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <span class="font-medium text-slate-900 dark:text-zinc-100">
                  {{ displayFor(item.providerId).label }}
                </span>
                <span
                  class="inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium"
                  :class="item.status === 'active'
                    ? 'bg-teal-50 text-teal-700 dark:bg-teal-900/30 dark:text-teal-300'
                    : 'bg-slate-100 text-slate-600 dark:bg-zinc-800 dark:text-zinc-300'"
                >
                  {{ statusLabel(item.status) }}
                </span>
              </div>
              <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
                {{ t('accountSecurity.linkedAccounts.linkedAt') }}: {{ formatLinkedAt(item.linkedAt) }}
              </p>
              <p
                v-if="item.status === 'inert'"
                class="mt-1 text-xs text-amber-700 dark:text-amber-300"
                data-testid="linked-account-inert-hint"
              >
                {{ t('accountSecurity.linkedAccounts.inertHint') }}
              </p>
              <p
                v-else-if="mayBeLastLoginMethod(item)"
                class="mt-1 text-xs text-slate-500 dark:text-zinc-400"
              >
                {{ t('accountSecurity.linkedAccounts.lastMethodHint') }}
              </p>
            </div>
          </div>
          <div class="shrink-0">
            <SFButton
              v-if="item.status === 'active'"
              variant="ghost"
              size="sm"
              :disabled="unlinkingId !== null"
              :aria-busy="unlinkingId === item.linkId ? 'true' : undefined"
              data-testid="unlink-external-identity"
              @click="unlinkIdentity(item)"
            >
              <UIcon name="i-lucide-unlink" class="mr-1" aria-hidden="true" />
              {{ unlinkingId === item.linkId
                ? t('accountSecurity.linkedAccounts.unlinking')
                : t('accountSecurity.linkedAccounts.unlink') }}
            </SFButton>
            <span
              v-else
              class="inline-flex items-center text-xs font-medium text-slate-400 dark:text-zinc-500"
            >
              {{ t('accountSecurity.linkedAccounts.inertAction') }}
            </span>
          </div>
        </li>
      </ul>

      <p v-else class="p-4 text-sm text-slate-500 dark:text-zinc-400" data-testid="linked-accounts-empty">
        {{ t('accountSecurity.linkedAccounts.empty') }}
      </p>
    </SFCard>

    <!-- link 入口：仅 Host 有效开放 link 且会话可用时展示 -->
    <div
      v-if="availableLinkProviders.length > 0"
      class="mt-4"
      data-testid="link-provider-entry"
    >
      <p class="mb-2 text-sm font-medium text-slate-700 dark:text-zinc-300">
        {{ t('accountSecurity.linkedAccounts.linkTitle') }}
      </p>
      <p class="mb-3 text-xs text-slate-500 dark:text-zinc-400">
        {{ t('accountSecurity.linkedAccounts.linkHelp') }}
      </p>
      <div class="flex flex-col gap-2 sm:flex-row sm:flex-wrap">
        <SFButton
          v-for="provider in availableLinkProviders"
          :key="provider.id"
          variant="secondary"
          size="sm"
          class="justify-center sm:justify-start"
          :disabled="Boolean(linkStartingId) || catalogPending"
          :data-provider-id="provider.id"
          data-testid="link-provider-button"
          @click="startLink(provider)"
        >
          <UIcon
            :name="authProviderDisplayMeta(provider, t('auth.providers.genericName')).icon"
            class="mr-1.5 size-4"
            aria-hidden="true"
          />
          {{ linkStartingId === provider.id
            ? t('accountSecurity.linkedAccounts.linking')
            : t('accountSecurity.linkedAccounts.linkWith', {
              name: authProviderDisplayMeta(provider, t('auth.providers.genericName')).label
            }) }}
        </SFButton>
      </div>
    </div>
    <p
      v-else-if="!catalogPending && linkProviders.length === 0"
      class="mt-3 text-xs text-slate-500 dark:text-zinc-400"
      data-testid="link-entry-closed"
    >
      {{ t('accountSecurity.linkedAccounts.linkClosed') }}
    </p>

  </section>
</template>

<style scoped>
@media (max-width: 640px) {
  .sf-linked-accounts :deep([data-testid='link-provider-button']) {
    width: 100%;
    min-height: 44px;
  }
}
</style>
