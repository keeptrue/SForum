import { ref } from 'vue'
import { apiErrorStatusCode } from '~/composables/useApiClient'

/**
 * 后台页面共享的「权限不足」态（UX2-16）。
 *
 * 权限权威始终是后端策略：前端中间件与页面只做 UX 守卫，**不按 `can()` 预判读权限**，
 * 只把后端真的回 401/403 的结果如实渲染成可操作的状态。三种结局必须互斥 ——
 * `forbidden` 不能被画成空表、空目录或永久骨架屏（UX2-01 / UX2-02 的根因）。
 *
 * 文案复用既有 i18n 键，本轮不新增键：
 * - 标题 `errors.page.forbidden.title`
 * - 说明 `errors.permissionDenied`
 * - 重试 `common.retry`
 */
export type AdminAccessFailureKind = 'none' | 'forbidden' | 'unavailable'

/**
 * 403/401 是权限问题，其余非零状态码是「暂时不可用」：两者该给的解释不同。
 * 0 表示拿不到 HTTP 状态（非 HTTP 异常），交给调用方按自己的兜底文案处理。
 */
export function adminAccessFailureKind(statusCode: number): AdminAccessFailureKind {
  if (statusCode === 401 || statusCode === 403) {
    return 'forbidden'
  }

  return statusCode >= 400 ? 'unavailable' : 'none'
}

export function useAdminAccessState() {
  const forbidden = ref(false)

  /**
   * 记一次失败：**由这一次失败决定**状态，返回值供调用方决定提示文案。
   *
   * 必须是权威赋值而不是单向置位：`403 → 500` 的第二次失败若还留着 `forbidden`，
   * 页面会同时显示「权限不足」和「服务不可用」两套互相矛盾的解释。
   */
  function noteFailure(error: unknown): AdminAccessFailureKind {
    const kind = adminAccessFailureKind(apiErrorStatusCode(error))
    forbidden.value = kind === 'forbidden'

    return kind
  }

  /** 取数成功后清位：重试成功必须能退出权限不足态（失败路径由 noteFailure 决定）。 */
  function reset() {
    forbidden.value = false
  }

  return { forbidden, noteFailure, reset }
}
