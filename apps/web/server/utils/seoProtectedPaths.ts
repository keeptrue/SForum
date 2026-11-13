/**
 * 受保护路径的 robots 判定。
 *
 * 这些路径要么依赖会话（登录/注册/通知收件箱/账号设置），要么是站内工具页
 * （组件预览、管理后台）。爬虫拿到的版本不可能是「同一份公开内容」，
 * 因此 SSR 响应头必须显式 `noindex, nofollow`，不能继承站点级可索引开关。
 *
 * 抽成独立 util 是为了让判定本身可被单元测试钉住 —— 之前它埋在 middleware 里，
 * 新增一条受保护路由只能靠肉眼确认，漏掉时表现为「生产上被索引了」。
 */
export function isProtectedSEOPath(path: string, adminPrefix: string): boolean {
  return path.startsWith('/api/') ||
    path === '/login' ||
    path.startsWith('/login/') ||
    path === '/register' ||
    path.startsWith('/register/') ||
    path === '/components' ||
    path.startsWith('/components/') ||
    path === '/notifications' ||
    path.startsWith('/notifications/') ||
    path === '/settings' ||
    path.startsWith('/settings/') ||
    path === adminPrefix ||
    path.startsWith(`${adminPrefix}/`)
}
