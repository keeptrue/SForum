<script setup lang="ts">
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import {
  canRestartPlugin,
  canUpgradePlugin,
  extensionManageRoute,
  isExtensionArtifactAvailable,
  type AdminExtension
} from '~/utils/admin/adminExtensions'
import {
  extensionPrimaryAction,
  extensionPrimaryActionNeedsEnable,
  type ExtensionPrimaryAction
} from '~/utils/admin/extensions/updatePresentation'

defineOptions({ name: 'SFAdminExtensionPrimaryAction' })

const props = withDefaults(defineProps<{
  extension: AdminExtension
  busy?: boolean
  upgradeBusy?: boolean
  upgradeBlocked?: boolean
}>(), {
  busy: false,
  upgradeBusy: false,
  upgradeBlocked: false
})

const emit = defineEmits<{
  upgrade: []
  restart: []
  activate: []
  enable: []
}>()

const { t } = useI18n()
const adminRoutes = useAdminRoutes()

const action = computed(() => extensionPrimaryAction(props.extension))
const needsEnable = computed(() => extensionPrimaryActionNeedsEnable(props.extension, action.value))
const available = computed(() => isExtensionArtifactAvailable(props.extension))

const label = computed(() => {
  switch (action.value) {
    case 'upgrade':
      return needsEnable.value ? t('admin.extensions.update.upgradeAndEnable') : t('admin.extensions.update.upgradeNow')
    case 'restart':
      return needsEnable.value ? t('admin.extensions.update.restartAndEnable') : t('admin.extensions.restart')
    case 'activate':
      return t('admin.extensions.activateTheme')
    case 'reactivate':
      return t('admin.extensions.reactivateTheme')
    case 'restoreDefault':
      return t('admin.extensions.restoreDefaultTheme')
    case 'enable':
      return t('admin.extensions.enable')
    default:
      return t('admin.extensions.manage')
  }
})

const icon = computed(() => {
  switch (action.value) {
    case 'upgrade': return 'i-lucide-package-plus'
    case 'restart': return 'i-lucide-refresh-cw'
    case 'activate': return 'i-lucide-play'
    case 'reactivate': return 'i-lucide-refresh-cw'
    case 'restoreDefault': return 'i-lucide-rotate-ccw'
    case 'enable': return 'i-lucide-play'
    default: return 'i-lucide-settings'
  }
})

const color = computed(() => {
  switch (action.value) {
    case 'upgrade': return 'warning' as const
    case 'enable':
    case 'activate':
    case 'restoreDefault':
      return 'primary' as const
    default: return 'neutral' as const
  }
})
const variant = computed(() => {
  switch (action.value) {
    case 'upgrade':
    case 'restart':
    case 'reactivate':
      return 'subtle' as const
    case 'manage':
      return 'ghost' as const
    default:
      return 'solid' as const
  }
})

const loading = computed(() => props.busy || props.upgradeBusy)

const disabled = computed(() => {
  switch (action.value) {
    case 'upgrade': return props.upgradeBlocked || !canUpgradePlugin(props.extension)
    case 'restart': return !canRestartPlugin(props.extension)
    case 'manage': return !available.value
    default: return !available.value
  }
})

const title = computed(() => {
  switch (action.value) {
    case 'upgrade':
      return canUpgradePlugin(props.extension)
        ? t('admin.extensions.update.availableHint', {
          version: props.extension.stagedVersion?.version || '',
          current: props.extension.version
        })
        : t('admin.extensions.update.upgradeBlocked')
    case 'restart':
      return canRestartPlugin(props.extension) ? t('admin.extensions.restart') : t('admin.extensions.restartUnavailable')
    case 'reactivate':
      return t('admin.extensions.reactivateThemeHint')
    case 'manage':
      return available.value ? undefined : t('admin.extensions.artifact.actionUnavailable')
    default:
      return undefined
  }
})

const manageTarget = computed(() => available.value
  ? adminRoutes.path(extensionManageRoute(props.extension))
  : undefined)

function run() {
  const current: ExtensionPrimaryAction = action.value
  if (current === 'upgrade') emit('upgrade')
  else if (current === 'restart') emit('restart')
  else if (current === 'activate' || current === 'reactivate' || current === 'restoreDefault') emit('activate')
  else if (current === 'enable') emit('enable')
}
</script>

<template>
  <UButton
    size="sm"
    :icon="icon"
    :color="color"
    :variant="variant"
    :to="action === 'manage' ? manageTarget : undefined"
    :disabled="disabled"
    :loading="action !== 'manage' && loading"
    :title="title"
    :aria-label="title ? `${label} · ${title}` : label"
    @click="action === 'manage' ? undefined : run()"
  >
    {{ label }}
  </UButton>
</template>
