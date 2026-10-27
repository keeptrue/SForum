// 内置插件自动更新是 Host 级开关，不在插件包内声明。这里集中放纯解析与
// 徽标判定，页面和 composable 都只消费这些结果，避免各写一套真假值规则。

export const BUILTIN_AUTO_UPDATE_OPTION = 'extension.builtin_auto_update'

const ENABLED_VALUES = new Set(['enabled', 'true', '1', 'yes', 'on'])

/** 与后端 normalizeEnabledOption 对齐：其余值（含空值）一律视为关闭。 */
export function isBuiltinAutoUpdateEnabledValue(value: string | null | undefined): boolean {
  if (typeof value !== 'string') {
    return false
  }
  return ENABLED_VALUES.has(value.trim().toLowerCase())
}

export type BuiltinAutoUpdateState = {
  supported: boolean
  enabled: boolean
}

type BuiltinAutoUpdateExtension = {
  source?: string | null
  stagedVersion?: unknown
}

/** SForum 发行版随包维护的插件。 */
export function isBuiltinMaintainedExtension(item: BuiltinAutoUpdateExtension): boolean {
  return item.source === 'builtin'
}

/** 开关存在但已关闭时，给内置插件行一个中性提示。 */
export function shouldShowBuiltinAutoUpdateDisabled(
  item: BuiltinAutoUpdateExtension,
  state: BuiltinAutoUpdateState
): boolean {
  return isBuiltinMaintainedExtension(item) && state.supported && !state.enabled
}

export function builtinAutoUpdateStateLabelKey(enabled: boolean): string {
  return enabled
    ? 'admin.extensions.plugins.autoUpdate.stateOn'
    : 'admin.extensions.plugins.autoUpdate.stateOff'
}
