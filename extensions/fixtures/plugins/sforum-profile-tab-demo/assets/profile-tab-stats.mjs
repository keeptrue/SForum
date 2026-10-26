// Permission-gated public L2 component for the forum.profile.tabs `l2Widget` demo tab.
// 与 profile-tab-card.mjs 同一挂载契约，区别只在贡献声明：该 tab 带 `permission`，
// 宿主在服务端按权限过滤 tab，组件本身拿不到也不需要权限信息。
export const apiVersion = 1

export function mount(target, bridge) {
  const who = bridge.props.displayName || bridge.props.username || 'this member'
  const line = target.ownerDocument.createElement('p')
  line.setAttribute('data-testid', 'profile-tab-demo-stats')
  line.textContent = `Profile stats tab (permission gated) for ${who}`
  target.appendChild(line)
  return () => {
    line.remove()
  }
}
