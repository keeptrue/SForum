// Nuxt 4 的 srcDir 是 `app/`，app.config.ts 必须放在这里才会被加载 —— 仓库根目录那份
// `apps/web/app.config.ts` 从未生效（详见 PR 说明，另开 issue 处理）。
export default defineAppConfig({
  ui: {
    modal: {
      // UModal 不给内容根输出 aria-modal（@nuxt/ui 4.12.2 的 Modal.vue 里没有这个属性），
      // 而 `content` 是组件 prop：`useComponentProps` 会把 appConfig 的 defaultVariants
      // 合并进 prop，Modal.vue 再把 contentProps 绑到 DialogContent（= [role="dialog"]）。
      // 一处配置覆盖全仓 115 处 <UModal>，不改任何调用点。
      // 运行时由 useComponentProps 读取；@nuxt/ui 4.12.2 的 AppConfig 类型
      // 只声明了 slots / variants，没有 defaultVariants —— 运行时支持、类型没跟上。
      // 用 @ts-expect-error 而不是 as any：一旦上游补上类型，这行会因「未使用」报错，
      // 逼我们回来删掉它，不会留下一个永久失效的静默断言。
      // @ts-expect-error defaultVariants 未被 @nuxt/ui 的 AppConfig 类型声明
      defaultVariants: {
        content: { 'aria-modal': 'true' }
      }
    }
  }
})
