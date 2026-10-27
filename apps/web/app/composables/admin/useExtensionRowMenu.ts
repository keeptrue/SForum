import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import {
  canRestartPlugin,
  extensionManageRoute,
  isExtensionArtifactAvailable,
  isLifecycleV2Plugin,
  type AdminExtension
} from '~/utils/admin/adminExtensions'

export type ExtensionMenuItem = {
  label?: string
  icon?: string
  to?: string
  color?: 'error'
  type?: 'separator'
  disabled?: boolean
  title?: string
  onSelect?: () => void
}

export type ExtensionRowMenuHandlers = {
  /** 每页的卸载可见性判定不同（权限 vs 可删除性），由页面注入。 */
  canUninstall: (item: AdminExtension) => boolean
  /** 插件页的存储实例配置入口；返回空串表示该插件不声明多实例 provider。 */
  storageRoute?: (item: AdminExtension) => string
  /** 插件页提供；主题页的菜单只有管理/卸载，不需要这些回调。 */
  enable?: (item: AdminExtension) => void
  disable?: (item: AdminExtension) => void
  restart?: (item: AdminExtension) => void
  lifecycle?: (item: AdminExtension) => void
  uninstall: (item: AdminExtension) => void
}

/**
 * 行尾「⋯」菜单：完整动作清单，顺序固定（管理 → 存储实例 → 启用/停用 → 重启 →
 * 生命周期记录 → 分隔线 → 卸载）。主按钮只是当前最可能动作的快捷方式，允许重复。
 * 菜单至少含「管理」并永远渲染触发器：即使主题行只剩重复项，管理入口也不能消失
 * （Lead 对规格「只剩重复项时不渲染触发器」的有意覆盖）。
 */
export function useExtensionRowMenu(handlers: ExtensionRowMenuHandlers) {
  const { t } = useI18n()
  const adminRoutes = useAdminRoutes()

  return function menuFor(item: AdminExtension): ExtensionMenuItem[][] {
    const available = isExtensionArtifactAvailable(item)
    const items: ExtensionMenuItem[] = [{
      label: t('admin.extensions.manage'),
      icon: 'i-lucide-settings',
      to: available ? adminRoutes.path(extensionManageRoute(item)) : undefined,
      disabled: !available,
      title: available ? undefined : t('admin.extensions.artifact.actionUnavailable')
    }]

    if (item.type === 'plugin') {
      const storage = handlers.storageRoute?.(item) || ''
      if (storage) {
        const enabled = item.status === 'enabled'
        items.push({
          label: t('admin.extensions.configureStorage'),
          icon: 'i-lucide-database',
          to: enabled ? storage : undefined,
          disabled: !enabled,
          title: enabled ? t('admin.extensions.configureStorage') : t('admin.extensions.configureStorageEnableFirst')
        })
      }
      items.push(item.status === 'enabled'
        ? {
          label: t('admin.extensions.disable'),
          icon: 'i-lucide-pause',
          disabled: !available,
          onSelect: () => handlers.disable?.(item)
        }
        : {
          label: t('admin.extensions.enable'),
          icon: 'i-lucide-play',
          disabled: !available,
          onSelect: () => handlers.enable?.(item)
        })
      const restartable = canRestartPlugin(item)
      items.push({
        label: t('admin.extensions.restart'),
        icon: 'i-lucide-refresh-cw',
        disabled: !restartable,
        title: restartable ? t('admin.extensions.restart') : t('admin.extensions.restartUnavailable'),
        onSelect: () => handlers.restart?.(item)
      })
      items.push({
        label: t('admin.extensions.lifecycle.title'),
        icon: 'i-lucide-history',
        onSelect: () => handlers.lifecycle?.(item)
      })
    }

    const canUninstall = handlers.canUninstall(item)
    if (canUninstall) {
      items.push({ type: 'separator' })
      const blocked = item.type === 'plugin' && item.status === 'enabled' && !isLifecycleV2Plugin(item)
      items.push({
        label: t('admin.extensions.uninstall'),
        icon: 'i-lucide-trash-2',
        color: 'error',
        disabled: blocked,
        title: blocked ? t('admin.extensions.confirmUninstallBody', { name: item.name }) : t('admin.extensions.uninstall'),
        onSelect: () => handlers.uninstall(item)
      })
    }

    return [items]
  }
}
