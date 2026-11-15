import { nextTick, ref } from 'vue'

/**
 * 全站共用的 live region 状态（UX2-17）。
 *
 * 设计取舍：
 * - **一条**全局 polite 通道 + 一条 assertive 通道，由 `SFLiveRegion` 在壳层渲染一次；
 *   页面里不要再自建 `aria-live`，否则同一句话会被播报两次。
 * - 连续播报同一句话时读屏不会重复触发，所以先清空、下一拍再写入。
 * - 模块级 ref 在 SSR 期间从不被写入（`announce*` 只在用户交互后调用），
 *   因此服务端渲染出来的两个区域恒为空串，不存在跨请求串话。
 */

const politeMessage = ref('')
const assertiveMessage = ref('')

export function useLiveRegionState() {
  return { politeMessage, assertiveMessage }
}

async function write(target: typeof politeMessage, message: string) {
  const text = message.trim()
  if (!text) return
  if (target.value === text) {
    target.value = ''
    await nextTick()
  }
  target.value = text
}

/** 非打断式播报：加载完成、保存成功、列表数量变化。 */
export function announcePolite(message: string) {
  void write(politeMessage, message)
}

/** 打断式播报：提交失败、权限被拒这类必须立刻听到的结果。 */
export function announceAssertive(message: string) {
  void write(assertiveMessage, message)
}

/** 测试与登出清理用：把两条通道清回空串。 */
export function resetLiveRegion() {
  politeMessage.value = ''
  assertiveMessage.value = ''
}
