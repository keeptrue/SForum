<script setup lang="ts">
import SFAdminExecutableTrustImpact from '~/components/admin/SFAdminExecutableTrustImpact.vue'
import SFAdminExtensionHostActionsNotice from '~/components/admin/SFAdminExtensionHostActionsNotice.vue'
import type { AdminExtension } from '~/utils/admin/adminExtensions'
import type {
  ExecutableTrustChallenge,
  ExecutableTrustStatus,
  ExtensionEnableTrustMode
} from '~/utils/extensions/extensionTrust'

const props = defineProps<{
  extension: AdminExtension | null
  mode: ExtensionEnableTrustMode
  trustStatus: ExecutableTrustStatus | null
  challenge: ExecutableTrustChallenge | null
  error: string
  busy: boolean
  isSuperAdmin: boolean
  // Theme activation, staged plugin upgrade and content-update restart reuse the exact
  // trust review pattern; `apply` is a restart that activates a staged artifact.
  purpose?: 'enable' | 'activate' | 'upgrade' | 'apply'
}>()
const emit = defineEmits<{ cancel: [], issueChallenge: [], confirm: [] }>()
const open = defineModel<boolean>('open', { required: true })
const { t, locale } = useI18n()

const purpose = computed(() => props.purpose || 'enable')
const isActivatePurpose = computed(() => purpose.value === 'activate')
const isUpgradePurpose = computed(() => purpose.value === 'upgrade')
const isApplyPurpose = computed(() => purpose.value === 'apply')
const titleKey = computed(() => props.mode !== 'exact'
  ? isApplyPurpose.value
    ? 'admin.extensions.update.restartTitle'
    : 'admin.extensions.confirmEnableTitle'
  : isActivatePurpose.value
    ? 'admin.extensions.trust.titleActivate'
    : isUpgradePurpose.value
      ? 'admin.extensions.trust.titleUpgrade'
      : isApplyPurpose.value
        ? 'admin.extensions.trust.titleApply'
        : 'admin.extensions.trust.title')
const bodyKey = computed(() => props.mode !== 'exact'
  ? isApplyPurpose.value
    ? 'admin.extensions.update.restartBody'
    : 'admin.extensions.confirmEnableBody'
  : isActivatePurpose.value
    ? 'admin.extensions.trust.bodyActivate'
    : isUpgradePurpose.value
      ? 'admin.extensions.trust.bodyUpgrade'
      : isApplyPurpose.value
        ? 'admin.extensions.trust.bodyApply'
        : 'admin.extensions.trust.body')
const blockingErrorKey = computed(() => isActivatePurpose.value
  ? 'admin.extensions.trust.blockingErrorActivate'
  : isUpgradePurpose.value
    ? 'admin.extensions.trust.blockingErrorUpgrade'
    : isApplyPurpose.value
      ? 'admin.extensions.trust.blockingErrorApply'
      : 'admin.extensions.trust.blockingError')
const confirmKey = computed(() => props.mode !== 'exact'
  ? isApplyPurpose.value
    ? 'admin.extensions.update.restartConfirm'
    : 'admin.extensions.confirmEnableAction'
  : isActivatePurpose.value
    ? 'admin.extensions.trust.confirmActivate'
    : isUpgradePurpose.value
      ? 'admin.extensions.trust.confirmUpgrade'
      : isApplyPurpose.value
        ? 'admin.extensions.trust.confirmApply'
        : 'admin.extensions.trust.confirmEnable')

const needsChallenge = computed(() => props.mode === 'exact'
  && props.trustStatus?.trustRequired === true
  && props.trustStatus.trusted === false)
const canConfirm = computed(() => props.mode === 'legacy'
  || props.trustStatus?.trusted === true
  || (needsChallenge.value && Boolean(props.challenge?.token)))

function cancel() {
  emit('cancel')
}
</script>

<template>
  <UModal v-model:open="open" :ui="{ content: 'sm:max-w-4xl' }">
    <template #content>
      <div class="flex max-h-[min(88vh,900px)] flex-col">
        <header class="shrink-0 border-b border-slate-200 px-5 py-4 sm:px-6 dark:border-zinc-800">
          <div class="flex items-start justify-between gap-4">
            <div class="min-w-0">
              <h2 class="text-base font-semibold text-slate-900 dark:text-zinc-100">
                {{ t(titleKey) }}
              </h2>
              <p class="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-300">
                {{ t(bodyKey, { name: extension?.name || '' }) }}
              </p>
            </div>
            <UButton icon="i-lucide-x" color="neutral" variant="ghost" :aria-label="t('admin.extensions.confirmEnableCancel')" @click="cancel" />
          </div>
        </header>

        <div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 sm:px-6">
          <!-- 启用前披露：该插件的管理页可请求宿主安装扩展包（宿主动作，不是进程能力）。 -->
          <SFAdminExtensionHostActionsNotice :extension="extension" />

          <UAlert
            v-if="error"
            color="error"
            variant="subtle"
            icon="i-lucide-triangle-alert"
            :title="t(blockingErrorKey)"
            :description="error"
            role="alert"
          />

          <template v-if="mode === 'exact'">
            <UAlert
              color="warning"
              variant="subtle"
              icon="i-lucide-shield-alert"
              :title="t('admin.extensions.trust.fullTrustTitle')"
              :description="t('admin.extensions.trust.fullTrustDescription')"
            />
            <div v-if="trustStatus" class="flex flex-wrap items-center gap-2">
              <UBadge
                :color="trustStatus.trusted ? 'success' : trustStatus.trustRequired ? 'warning' : 'neutral'"
                variant="subtle"
                :icon="trustStatus.trusted ? 'i-lucide-shield-check' : 'i-lucide-shield-alert'"
              >
                {{ trustStatus.trusted
                  ? t('admin.extensions.trust.statusTrusted')
                  : trustStatus.trustRequired
                    ? t('admin.extensions.trust.statusRequired')
                    : t('admin.extensions.trust.statusNotRequired') }}
              </UBadge>
              <span v-if="challenge" class="text-xs text-slate-500 dark:text-zinc-400">
                {{ t('admin.extensions.trust.challengeExpires', { value: challenge.expiresAt }) }}
              </span>
            </div>
            <UAlert
              v-if="needsChallenge && !isSuperAdmin"
              color="error"
              variant="subtle"
              icon="i-lucide-lock-keyhole"
              :title="t('admin.extensions.trust.superAdminRequired')"
              :description="t('admin.extensions.trust.delegatedPreviewOnly')"
            />
            <UAlert
              v-if="challenge"
              color="success"
              variant="subtle"
              icon="i-lucide-key-round"
              :title="t('admin.extensions.trust.challengeReady')"
              :description="t('admin.extensions.trust.challengeReadyDescription')"
            />
            <SFAdminExecutableTrustImpact
            v-if="trustStatus"
            :impact="trustStatus.impact"
            :guard-authorization="trustStatus.guardAuthorization"
          />
          </template>

          <ul v-else-if="extension?.capabilityGrants?.length" class="space-y-2">
            <li
              v-for="grant in extension.capabilityGrants"
              :key="grant.key"
              class="rounded-md border border-slate-200 px-3 py-2 text-sm dark:border-zinc-700"
            >
              <div class="flex items-center justify-between gap-2">
                <span class="font-medium">{{ locale.startsWith('zh') ? grant.labelZh : grant.labelEn }}</span>
                <UBadge
                  size="xs"
                  variant="subtle"
                  :color="grant.risk === 'high' ? 'error' : grant.risk === 'medium' ? 'warning' : 'success'"
                >
                  {{ t(`admin.extensions.capabilityRisk.${grant.risk}`) }}
                </UBadge>
              </div>
              <p class="mt-1 break-all text-xs text-slate-500 dark:text-zinc-400">{{ grant.key }}</p>
            </li>
          </ul>
        </div>

        <footer class="flex shrink-0 flex-wrap justify-end gap-2 border-t border-slate-200 px-5 py-4 sm:px-6 dark:border-zinc-800">
          <UButton color="neutral" variant="ghost" :disabled="busy" @click="cancel">
            {{ t('admin.extensions.confirmEnableCancel') }}
          </UButton>
          <UButton
            v-if="needsChallenge && isSuperAdmin && !challenge"
            color="warning"
            icon="i-lucide-key-round"
            :loading="busy"
            @click="emit('issueChallenge')"
          >
            {{ t('admin.extensions.trust.issueChallenge') }}
          </UButton>
          <UButton
            v-if="!needsChallenge || isSuperAdmin"
            color="primary"
            icon="i-lucide-shield-check"
            :loading="busy"
            :disabled="!canConfirm"
            @click="emit('confirm')"
          >
            {{ t(confirmKey) }}
          </UButton>
        </footer>
      </div>
    </template>
  </UModal>
</template>
