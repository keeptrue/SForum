// Minimal public L2 component for the settingsSection `component` tab shape.
//
// 公开 L2 模块契约（照抄这一份即可）：
// - 必须**具名导出** `apiVersion` 与 `mount`；`export default` 会让契约校验失败。
// - `target` 是宿主提供的空容器；设置面把 `bridge.props = { locale }` 传进来。
// - `mount` 返回的函数是卸载钩子，宿主切走 tab 时立即调用它。
export const apiVersion = 1

export function mount(target, bridge) {
  const locale = (bridge.props && bridge.props.locale) || 'en-US'
  const line = target.ownerDocument.createElement('p')
  line.setAttribute('data-testid', 'settings-surface-demo-panel')
  line.textContent = locale === 'zh-CN' ? '设置面演示组件' : 'Settings surface demo panel'
  target.appendChild(line)
  return () => {
    line.remove()
  }
}
