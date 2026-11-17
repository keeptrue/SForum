// Nuxt 4 的 srcDir 是 `app/`，app.config.ts 必须放在这里才会被加载。
// 仓库根目录的 `apps/web/app.config.ts` 从未被 Nuxt 读取过（issue #258）：
// 那里的 alert 覆盖是死代码，Nuxt UI 一直用默认主题渲染。
// tests/appConfigLocation.test.ts 钉住这个位置，避免再被挪回去。
export default defineAppConfig({
  ui: {
    alert: {
      // Nuxt UI 默认 root 是 `relative overflow-hidden w-full rounded-lg p-4 flex gap-2.5`：
      // `overflow-hidden` 在后台纵向 flex 布局里高度被压缩时会裁切正文，看起来像下方
      // tab/面板把提示“压住”。这里改成 items-start 顶部对齐，并给 root 加 shrink-0
      // 让它在 flex 列里保持自身高度不被压扁。
      //
      // `overflow-visible` 不能省：`tv({ extend: theme, ...appConfig.ui.alert })` 走的是
      // tailwind-variants 的 extend 合并（tailwind-merge 按冲突组取后者），槽位字符串是
      // 合并不是替换 —— 不写 overflow-visible，默认的 overflow-hidden 会一直留在 class 里，
      // 覆盖等于没生效。写同组类才能把它挤掉（实测见 tests/framework/appConfigLocation.test.ts）。
      slots: {
        root: 'relative w-full rounded-lg p-4 flex items-start gap-2.5 shrink-0 overflow-visible',
        wrapper: 'min-w-0 flex-1 flex flex-col',
        title: 'text-sm font-medium',
        description: 'text-sm opacity-90 leading-6',
        icon: 'shrink-0 size-5',
        avatar: 'shrink-0',
        avatarSize: '2xl',
        actions: 'flex flex-wrap gap-1.5 shrink-0',
        close: 'p-0'
      }
    },
    modal: {
      // UModal 不给内容根输出 aria-modal（@nuxt/ui 4.12.2 的 Modal.vue 里没有这个属性），
      // 而 `content` 是组件 prop：`useComponentProps` 会把 appConfig 的 defaultVariants
      // 合并进 prop，Modal.vue 再把 contentProps 绑到 DialogContent（= [role="dialog"]）。
      // 一处配置覆盖全仓 54 处 <UModal>，不改任何调用点。
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
