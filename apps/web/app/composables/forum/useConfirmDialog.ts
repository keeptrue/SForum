import { reactive } from 'vue'

/** 一次确认请求的内容：文案由调用方给（页面负责翻译），组件只负责呈现。 */
export type ConfirmDialogOptions = {
  title: string
  description: string
  confirmLabel: string
  cancelLabel: string
}

/** 组件渲染所需的确认请求状态。 */
export type ConfirmDialogRequest = ConfirmDialogOptions & {
  open: boolean
}

/**
 * 站内确认弹窗的状态机：confirm() 返回 Promise，弹窗关闭时按用户的结算结果兑现。
 *
 * - 同一时刻只允许一个待结算请求；连续两次 confirm 时前一个按「取消」结算，避免旧 Promise 悬空。
 * - 结算后 request.open 归 false，弹窗由页面持有的状态驱动关闭（ESC / 遮罩 / 关闭按钮统一按取消结算）。
 */
export function useConfirmDialog() {
  const request = reactive<ConfirmDialogRequest>({
    open: false,
    title: '',
    description: '',
    confirmLabel: '',
    cancelLabel: ''
  })

  let pending: ((value: boolean) => void) | null = null

  function confirm(options: ConfirmDialogOptions): Promise<boolean> {
    // 前一个请求还没结算时按「取消」结算：不能让它永远悬空。
    if (pending) {
      const previous = pending
      pending = null
      previous(false)
    }
    request.title = options.title
    request.description = options.description
    request.confirmLabel = options.confirmLabel
    request.cancelLabel = options.cancelLabel
    request.open = true
    return new Promise<boolean>((resolve) => {
      pending = resolve
    })
  }

  function resolveConfirm(value: boolean) {
    const resolve = pending
    pending = null
    request.open = false
    resolve?.(value)
  }

  return { request, confirm, resolveConfirm }
}
