import {
  isExtensionArtifactAvailable,
  themeActionState,
  type AdminExtension
} from '~/utils/admin/adminExtensions'
import { resolveExtensionUpdateState } from './updateState'

/**
 * 「更新」在行内与行尾各出现一次：徽章说会发生什么，主按钮说现在做什么。
 * 这里只做纯映射，页面不再各自推断状态。
 */
export type ExtensionPrimaryAction =
  | 'upgrade'
  | 'restart'
  | 'activate'
  | 'reactivate'
  | 'restoreDefault'
  | 'enable'
  | 'manage'

export function extensionPrimaryAction(item: AdminExtension): ExtensionPrimaryAction {
  // 制品缺失时任何应用动作都会失败，只保留「管理」。
  if (!isExtensionArtifactAvailable(item)) return 'manage'
  const state = resolveExtensionUpdateState(item)
  if (item.type === 'theme') {
    const themeAction = themeActionState(item)
    if (state.kind !== 'none') {
      return themeAction === 'active' ? 'reactivate' : themeAction === 'activateDefault' ? 'restoreDefault' : 'activate'
    }
    return themeAction === 'active' ? 'manage' : themeAction === 'activateDefault' ? 'restoreDefault' : 'activate'
  }
  if (state.kind === 'version') return 'upgrade'
  if (state.kind === 'content') return 'restart'
  return item.status === 'enabled' ? 'manage' : 'enable'
}

/** 停用插件的 Restart 以 Enable 收尾，按钮必须把副作用写在文案上。 */
export function extensionPrimaryActionNeedsEnable(item: AdminExtension, action: ExtensionPrimaryAction) {
  return item.type === 'plugin' && item.status !== 'enabled' && (action === 'upgrade' || action === 'restart')
}

export type ExtensionUpdateBadge = {
  labelKey: string
  labelParams: Record<string, string>
  icon: string
  variant: 'subtle' | 'outline'
  color: 'warning'
  titleKey: string
  titleParams: Record<string, string>
}

export function shortDigest(digest?: string | null) {
  const value = typeof digest === 'string' ? digest.trim() : ''
  return value.slice(0, 8)
}

export function extensionUpdateBadge(item: AdminExtension): ExtensionUpdateBadge | null {
  if (!isExtensionArtifactAvailable(item)) return null
  const state = resolveExtensionUpdateState(item)
  if (state.kind === 'version') {
    return {
      labelKey: 'admin.extensions.update.available',
      labelParams: { version: state.version },
      icon: 'i-lucide-package-plus',
      variant: 'subtle',
      color: 'warning',
      titleKey: 'admin.extensions.update.availableHint',
      titleParams: { version: state.version, current: state.current }
    }
  }
  if (state.kind !== 'content') return null
  const digest = shortDigest(item.stagedVersion?.packageDigest)
  if (item.type === 'theme') {
    return {
      labelKey: 'admin.extensions.update.activateToApply',
      labelParams: {},
      icon: 'i-lucide-play',
      variant: 'outline',
      color: 'warning',
      titleKey: 'admin.extensions.update.activateHint',
      titleParams: { version: state.version, digest }
    }
  }
  return {
    labelKey: 'admin.extensions.update.restartToApply',
    labelParams: {},
    icon: 'i-lucide-rotate-cw',
    variant: 'outline',
    color: 'warning',
    titleKey: item.status === 'enabled'
      ? 'admin.extensions.update.restartHint'
      : 'admin.extensions.update.restartEnableHint',
    titleParams: { version: state.version, digest }
  }
}
