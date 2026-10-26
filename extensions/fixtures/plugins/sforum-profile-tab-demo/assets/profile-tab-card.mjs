// Minimal public L2 component for the forum.profile.tabs `l2Widget` demo tab.
//
// 公开 L2 模块契约（照抄这一份即可）：
// - 必须**具名导出** `apiVersion` 与 `mount`；`export default` 会让契约校验失败。
// - `target` 是宿主提供的空容器；资料页 tab 面板传入
//   `bridge.props = { username, userId, displayName }`。
// - `mount` 返回的函数是卸载钩子，宿主切走 tab 时立即调用它。
export const apiVersion = 1

export function mount(target, bridge) {
  const who = bridge.props.displayName || bridge.props.username || 'this member'
  const line = target.ownerDocument.createElement('p')
  line.setAttribute('data-testid', 'profile-tab-demo-card')
  line.textContent = `Profile tab demo widget for ${who}`
  target.appendChild(line)
  return () => {
    line.remove()
  }
}
