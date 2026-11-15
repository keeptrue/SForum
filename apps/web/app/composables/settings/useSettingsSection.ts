import { apiErrorMessage } from '~/composables/useApiClient'

/**
 * 管理设置页各 tab 共用的「saving 标记 → 执行保存 → 成功/失败 toast」外壳。
 * 各 section 只提供 prepare / 构建 payload 的回调；校验与选项内容仍归 section 自己。
 */
export type SettingsSectionSaveOptions = {
  /** 可选：提交前的 clamp / 字段同步（不包含 toast） */
  prepare?: () => void | Promise<void>
  /** 真正执行保存（通常调用 saveAndApply / saveMany） */
  save: () => void | Promise<void>
  /** 成功 toast 标题；默认由调用方传入 i18n 文案 */
  successTitle: string
  /** 失败时无 API 消息时的回落标题 */
  failureTitle: string
  /** 成功 toast 自动关闭毫秒；与项目约定一致，默认 5000 */
  successDuration?: number
}

export type SettingsSectionRestoreOptions = {
  /** 把推荐默认写回表单（不写服务端） */
  apply: () => void
  title: string
  /** neutral = 重置更改；success = 恢复推荐默认 */
  color?: 'success' | 'neutral'
  duration?: number
  icon?: string
}

export function useSettingsSection() {
  const toast = useToast()
  const saving = ref(false)

  async function runSave(options: SettingsSectionSaveOptions): Promise<boolean> {
    if (options.prepare) {
      await options.prepare()
    }
    saving.value = true
    try {
      await options.save()
      toast.add({
        color: 'success',
        icon: 'i-lucide-check',
        title: options.successTitle,
        duration: options.successDuration ?? 5000
      })
      return true
    } catch (error) {
      toast.add({
        color: 'error', duration: 0,
        icon: 'i-lucide-triangle-alert',
        title: apiErrorMessage(error) || options.failureTitle
      })
      return false
    } finally {
      saving.value = false
    }
  }

  function runRestore(options: SettingsSectionRestoreOptions) {
    options.apply()
    toast.add({
      color: options.color ?? 'success',
      icon: options.icon ?? 'i-lucide-rotate-ccw',
      title: options.title,
      duration: options.duration ?? 5000
    })
  }

  return {
    saving,
    runSave,
    runRestore
  }
}
