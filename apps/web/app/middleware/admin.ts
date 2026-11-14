import { useAuthSession } from '~/composables/identity/useAuthSession'
export default defineNuxtRouteMiddleware(async (to) => {
  const localePath = useLocalePath()
  const { user, refresh, can } = useAuthSession()

  if (!user.value) {
    await refresh({ timeout: import.meta.dev ? 800 : 2000 })
  }

  if (!user.value) {
    // 认证服务短暂不可用时不要升级成 Nuxt 错误页；没有可用会话则退回登录页。
    return navigateTo({
      path: localePath('/login'),
      query: { redirect: to.fullPath }
    })
  }

  if (!can('admin.access')) {
    // 前端中间件只是 UX 守卫，权限权威始终在后端策略：这里不替后端下结论，
    // 也不静默弹回首页 —— 那会让「没有后台权限」和「链接失效」长得一模一样。
    // 落到共享的 403 态（error.vue → errors.page.forbidden.*），操作者才知道是权限问题。
    throw createError({
      statusCode: 403,
      statusMessage: 'admin.access'
    })
  }
})
