import { describe, expect, mock, test } from 'bun:test'
import { existsSync, readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

/**
 * `app.config.ts` 的位置契约（issue #258）。
 *
 * 背景：`apps/web/app.config.ts`（仓库根）从来没被 Nuxt 读过。Nuxt 4 的 `srcDir` 是
 * `app/`，只有 `apps/web/app/app.config.ts` 会进 app config —— 根目录那份里的
 * `ui.alert.slots` 是死代码，生产环境一直按 Nuxt UI 默认主题渲染（默认 root 带
 * `overflow-hidden`，后台纵向 flex 里高度被压缩时会裁切正文）。
 *
 * 为什么不直接拿 `.nuxt/app.config.mjs` 当判据：那份产物**确实**会
 * `import cfg0 from "…/apps/web/app/app.config.ts"` 再 `defuFn(cfg0, inlineConfig)` 合并，
 * 也就是说它证明了「配置被引用」，但它是 `nuxt prepare`/dev 生成的构建产物（可能不存在、
 * 可能过期），而且**看不到最终 DOM 上的 class** —— class 是 `tv({ extend: theme, ...appConfig.ui.alert })`
 * 再用 tailwind-merge 按冲突组算出来的。所以判据取三样，各自钉一条真实链路：
 * 文件位置（在 `app/` 下、根目录没有）、导出内容（槽位字符串）、真实 tv 合并（顶层断言）。
 *
 * 第二个坑：`Alert.vue` 用 `tv({ extend: theme, ...appConfig.ui?.alert })`，走的是
 * tailwind-variants 的 extend 合并（tailwind-merge 按冲突组取后者），槽位字符串是
 * **合并**不是替换 —— 只写 `shrink-0` 不写 `overflow-visible`，默认的 `overflow-hidden`
 * 会继续留在 class 里，覆盖等于没生效。下面用真实 tv 复现这条合并链路钉住它。
 */

const webRoot = fileURLToPath(new URL('../../', import.meta.url))

// `@nuxt/ui` 的安装路径不要写死：bun 的隔离安装把它放在 node_modules/.bun/… 下，
// 目录名带版本哈希。用包自己的 package.json 定位包根，再拼组件路径。
const requireFromWeb = createRequire(import.meta.url)
const nuxtUiRoot = dirname(requireFromWeb.resolve('@nuxt/ui/package.json'))
const ALERT_COMPONENT_PATH = `${nuxtUiRoot}/dist/runtime/components/Alert.vue`
// `nuxt prepare` / dev 生成的 alert 主题（`#build/ui/alert` 的内容），可能不存在。
const GENERATED_ALERT_THEME_PATH = `${webRoot}.nuxt/ui/alert.ts`

/**
 * `@nuxt/ui` 的 alert 主题默认 root 的**人工副本**（4.12.2 实测值）。
 *
 * 这是上游默认值，不是我们的配置。上游换裁剪类（`overflow-hidden` → `overflow-clip` 等）
 * 或改 root 基础样式时，这条副本会**静默失效**：断言仍然绿，但它验的已经不是上游真值。
 * 所以优先读 `.nuxt/ui/alert.ts` 里生成的那份（见 skipIf 的两个用例），常量只在产物缺失
 * （例如没跑过 `nuxt prepare` 的裸环境）时兜底。
 */
const NUXT_UI_DEFAULT_ALERT_ROOT = 'relative overflow-hidden w-full rounded-lg p-4 flex gap-2.5'

type AppConfig = {
  ui?: {
    alert?: { slots?: Record<string, string> }
    modal?: { defaultVariants?: { content?: Record<string, string> } }
  }
}

async function loadAppConfig(): Promise<AppConfig> {
  // `defineAppConfig` 是 Nuxt 的编译期宏，测试里没有这个全局，补成恒等函数即可拿到原对象。
  const scope = globalThis as unknown as { defineAppConfig?: (value: AppConfig) => AppConfig }
  scope.defineAppConfig = value => value
  const module = await import('../../app/app.config')
  return (module.default ?? module) as AppConfig
}

function readAlertComponent(): string {
  if (!existsSync(ALERT_COMPONENT_PATH)) {
    throw new Error(
      `找不到上游组件 ${ALERT_COMPONENT_PATH}。` +
        '这是 @nuxt/ui 的包内路径（exports["./runtime/*"] → dist/runtime/*），' +
        '升级后结构变了就按新包的真实路径改这里，别把断言辞掉。'
    )
  }
  return readFileSync(ALERT_COMPONENT_PATH, 'utf8')
}

// 与 Alert.vue 里的 `tv({ extend: theme, ...appConfig.ui?.alert || {} })` 同构：
// 用真实 tailwind-variants（经 @nuxt/ui 的 tv 封装）合并，看最终 class 字符串。
async function mergedAlertRoot(defaultRoot: string): Promise<string> {
  // `@nuxt/ui/utils/tv` 顶部 `import appConfig from '#build/app.config'`，测试环境没有
  // Nuxt 的 #build 别名，替换成空对象即可（被测的是 tailwind-variants 的合并语义）。
  mock.module('#build/app.config', () => ({ default: {} }))
  const { tv } = await import('@nuxt/ui/utils/tv')
  const appConfig = await loadAppConfig()
  const ui = tv({
    extend: { slots: { root: defaultRoot } },
    ...(appConfig.ui?.alert ?? {})
  })()
  return ui.root() as string
}

describe('app.config.ts 必须在 apps/web/app/ 下（#258）', () => {
  test('仓库根的 apps/web/app.config.ts 不存在', () => {
    // Nuxt 4 srcDir=app/，这个位置的同名文件永远不会被加载，留着只会让人误以为配置生效。
    expect(existsSync(`${webRoot}app.config.ts`)).toBe(false)
  })

  test('app/app.config.ts 存在，且 alert root 去掉了 overflow-hidden', async () => {
    expect(existsSync(`${webRoot}app/app.config.ts`)).toBe(true)
    const root = (await loadAppConfig()).ui?.alert?.slots?.root
    expect(typeof root).toBe('string')
    expect(root).toContain('shrink-0')
    // 必须显式写 overflow-visible 才能挤掉默认的 overflow-hidden（见下面的合并用例）。
    expect(root).toContain('overflow-visible')
    expect(root).not.toContain('overflow-hidden')
  })

  test('合并没有吃掉 modal 的 aria-modal（#250 的配置仍在同一份文件里）', async () => {
    const appConfig = await loadAppConfig()
    expect(appConfig.ui?.modal?.defaultVariants?.content?.['aria-modal']).toBe('true')
  })

  test.skipIf(!existsSync(GENERATED_ALERT_THEME_PATH))(
    '真实 tv extend 合并后，root class 不含 overflow-hidden（默认值取生成的主题）',
    async () => {
      const themeSource = readFileSync(GENERATED_ALERT_THEME_PATH, 'utf8')
      const generatedRoot = themeSource.match(/"root":\s*"([^"]+)"/)?.[1]
      expect(generatedRoot, `${GENERATED_ALERT_THEME_PATH} 里没解析到 slots.root`).toBeTruthy()
      // 先确认上游默认值真的带裁剪类，否则下面的断言等于没测。
      expect(generatedRoot).toContain('overflow-hidden')

      const root = await mergedAlertRoot(generatedRoot as string)
      expect(root).toContain('shrink-0')
      expect(root).toContain('overflow-visible')
      expect(root).not.toContain('overflow-hidden')
    }
  )

  test.skipIf(existsSync(GENERATED_ALERT_THEME_PATH))(
    '真实 tv extend 合并后，root class 不含 overflow-hidden（产物缺失，退回常量副本）',
    async () => {
      const root = await mergedAlertRoot(NUXT_UI_DEFAULT_ALERT_ROOT)
      expect(root).toContain('shrink-0')
      expect(root).not.toContain('overflow-hidden')
    }
  )

  test('上游机制仍在：Alert.vue 把 appConfig.ui.alert 展开进 tv 的 extend', () => {
    const alertSource = readAlertComponent()
    // 拆成两条独立断言：任一环断了都能直接从失败信息看出是哪一环。
    expect(
      alertSource,
      'Alert.vue 不再用 tv({ extend: theme }) 组装样式：合并语义可能变了，回来重看本用例的前提'
    ).toMatch(/extend:\s*theme/)
    expect(
      alertSource,
      'Alert.vue 不再读 appConfig.ui?.alert：app.config 的 alert 覆盖会整体失效'
    ).toMatch(/appConfig\.ui\?\.alert/)
    expect(alertSource).toContain('useAppConfig()')
  })
})
