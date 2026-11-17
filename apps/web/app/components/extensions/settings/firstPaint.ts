/**
 * 预构建设置组件的首帧判定。
 *
 * 设置文档在 SSR 之前就证明了「已经声明预构建组件」，而信任结论由 page-bootstrap
 * 同步带回，所以「已授权但组件尚未挂载」这一段既不是未知、也不是未授权。旧的模板
 * 只在 `!trustResolved` 时给骨架，这段窗口就会把 fallback 插槽里的整张 Schema 表单
 * 画进首屏，等微前端挂载后再整块替换 —— 用户看到的就是闪屏与滚动跳动。
 *
 * 两个分支由同一个函数产出，避免模板条件与测试各自维护一套规则而漂移。
 */

export interface TrustedSettingsFirstPaintState {
  /** 微前端已挂载到宿主边界。 */
  mounted: boolean
  /** 加载或契约校验失败。 */
  failed: boolean
  /** 操作员主动选择了声明式 Schema UI。 */
  forcedFallback: boolean
  /** 信任结论已读到（bootstrap 初值或 /frontend 回源）。 */
  trustResolved: boolean
  /** 已读到非空 status。 */
  hasStatus: boolean
  /** 状态为已信任且未被强制回退。 */
  trusted: boolean
  /** status 里带着组件描述符。 */
  hasComponent: boolean
}

export interface TrustedSettingsFirstPaint {
  showSkeleton: boolean
  showSchemaFallback: boolean
}

export function resolveTrustedSettingsFirstPaint(state: TrustedSettingsFirstPaintState): TrustedSettingsFirstPaint {
  const { mounted, failed, forcedFallback, trustResolved, hasStatus, trusted, hasComponent } = state
  const pending = !mounted && !failed && !forcedFallback
  return {
    // 信任未读到、或已授权却还没挂载：都先给骨架。失败与强制回退不属于骨架语义。
    showSkeleton: pending && (!trustResolved || (hasStatus && trusted && hasComponent)),
    // 只有确实要展示 Schema 表单时才放开 fallback；信任未读到时绝不提前画表单。
    showSchemaFallback: trustResolved && !mounted
      && (failed || forcedFallback || !hasStatus || !trusted || !hasComponent)
  }
}
