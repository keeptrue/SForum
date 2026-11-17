import { describe, expect, mock, test } from 'bun:test'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

/**
 * 管理端模态弹窗的 `aria-modal`（issue #250）。
 *
 * 背景：`@nuxt/ui@4.12.2` 的 `UModal` 不给内容根输出 `aria-modal`，于是全仓 54 处
 * `<UModal>` 渲染出的 `[role="dialog"]` 都是「模态但没声明模态」——读屏软件不会把
 * 弹窗外内容当背景忽略，焦点/朗读可能落到后面的表格和工具栏上。
 *
 * 修法：在 `app/app.config.ts` 用 `ui.modal.defaultVariants.content` 给 `content` prop
 * 一个默认值。Nuxt UI 的 `useComponentProps()` 会把 `appConfig.ui.<name>.defaultVariants[prop]`
 * 合并进 prop（`useComponentProps.js` 里的 `appConfigValue` 分支），`Modal.vue` 再把
 * `contentProps` 绑到 `DialogContent` 上 —— 也就是落在 `[role="dialog"]` 那个元素。
 *
 * 这个用例钉两件事：
 *   1. 真实 app.config + 真实 Nuxt UI 合并逻辑下，`content` 真的带出 `aria-modal`；
 *   2. 上游机制没变（`Modal.vue` 仍把 contentProps 绑到 DialogContent、`useComponentProps`
 *      仍读 defaultVariants）——下次升 UI 栈把这条链路改掉时，这里先红。
 */

const appRoot = fileURLToPath(new URL('../../', import.meta.url))

type AppConfig = { ui?: { modal?: { defaultVariants?: { content?: Record<string, string> } } } }

async function loadAppConfig(): Promise<AppConfig> {
  // `defineAppConfig` 是 Nuxt 的编译期宏，测试里没有这个全局，补成恒等函数即可拿到原对象。
  const scope = globalThis as unknown as { defineAppConfig?: (value: AppConfig) => AppConfig }
  scope.defineAppConfig = value => value
  const module = await import('../../app/app.config')
  return (module.default ?? module) as AppConfig
}

describe('UModal 的 aria-modal（#250）', () => {
  test('app.config 给 modal.content 声明了 aria-modal', async () => {
    const appConfig = await loadAppConfig()
    expect(appConfig.ui?.modal?.defaultVariants?.content?.['aria-modal']).toBe('true')
  })

  test('真实 useComponentProps 把 aria-modal 合并进 content prop，并渲染到元素上', async () => {
    const appConfig = await loadAppConfig()
    // 只替换 `useAppConfig`，其余 Nuxt 自动导入保持原样：被测的合并逻辑是 Nuxt UI 自己的。
    mock.module('#imports', () => ({ useAppConfig: () => appConfig }))

    const { mount } = await import('../helpers/vueSfc')
    const { h, defineComponent } = await import('vue')
    const { useComponentProps } = await import('@nuxt/ui/composables/useComponentProps')

    const Probe = defineComponent({
      props: { content: { type: Object, required: false } },
      setup(props) {
        const merged = useComponentProps('modal', props)
        // 与 Modal.vue 里 `v-bind="contentProps"` 落到 DialogContent 的形态一致。
        return () => h('div', { role: 'dialog', ...(merged.content ?? {}) })
      }
    })

    const wrapper = mount(Probe, {})
    const dialog = wrapper.find('[role="dialog"]')
    expect(dialog.exists()).toBe(true)
    expect(dialog.attributes('aria-modal')).toBe('true')
    // 只出现一次，不是重复属性（重复属性浏览器会丢弃后者，等于随机）。
    expect(Object.keys(dialog.attributes()).filter(name => name === 'aria-modal')).toHaveLength(1)
    wrapper.unmount()
  })

  test('显式传入 content 时以调用方为准（当前全仓没有这种调用点）', async () => {
    const appConfig = await loadAppConfig()
    mock.module('#imports', () => ({ useAppConfig: () => appConfig }))

    const { mount } = await import('../helpers/vueSfc')
    const { h, defineComponent } = await import('vue')
    const { useComponentProps } = await import('@nuxt/ui/composables/useComponentProps')

    const Probe = defineComponent({
      props: { content: { type: Object, required: false } },
      setup(props) {
        const merged = useComponentProps('modal', props)
        return () => h('div', { role: 'dialog', ...(merged.content ?? {}) })
      }
    })

    const wrapper = mount(Probe, { props: { content: { class: 'custom' } } })
    expect(wrapper.find('[role="dialog"]').attributes('aria-modal')).toBeUndefined()
    wrapper.unmount()
  })

  test('上游机制仍在：Modal.vue 把 contentProps 绑到 DialogContent', () => {
    const modalSource = readFileSync(
      `${appRoot}node_modules/@nuxt/ui/dist/runtime/components/Modal.vue`,
      'utf8'
    )
    expect(modalSource).toContain('const contentProps = toRef(() => props.content)')
    expect(modalSource).toMatch(/<DialogContent[\s\S]*?v-bind="contentProps"/)
  })

  test('上游机制仍在：useComponentProps 读 appConfig 的 defaultVariants', () => {
    const source = readFileSync(
      `${appRoot}node_modules/@nuxt/ui/dist/runtime/composables/useComponentProps.js`,
      'utf8'
    )
    expect(source).toContain('appConfigEntry?.defaultVariants?.[prop]')
  })

  test('没有调用点自带 content prop（否则会盖掉默认值）', () => {
    const offenders: string[] = []
    const walk = (dir: string) => {
      for (const entry of readdirSync(dir, { withFileTypes: true })) {
        const path = `${dir}/${entry.name}`
        if (entry.isDirectory()) {
          walk(path)
          continue
        }
        if (!entry.name.endsWith('.vue') && !entry.name.endsWith('.ts')) continue
        const source = readFileSync(path, 'utf8')
        for (const match of source.matchAll(/<UModal\b[^>]*>/g)) {
          if (/(^|\s):?content=/.test(match[0])) offenders.push(path.replace(appRoot, ''))
        }
      }
    }
    walk(`${appRoot}app`)
    expect(offenders).toEqual([])
  })
})
