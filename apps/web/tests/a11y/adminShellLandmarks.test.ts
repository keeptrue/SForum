import { describe, expect, test } from 'bun:test'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

/**
 * 后台壳的地标契约（第 15 轮 · axe `region` / `landmark-unique` / `<main>` 唯一性）。
 *
 * 修前真机（`node tests/a11y/scan.mjs --mode=scan`，9 个后台页 × 2 视口）：
 * - `region` 每页 4 个节点：侧栏 header、顶栏两处、页签条都不在任何地标内；
 * - 后台壳根本没有 `<main>`（`document.querySelectorAll('main').length === 0`）；
 * - `content.vue` 的详情抽屉里还有一个嵌套 `<main>`（有页签时会出现两个 main 候选）。
 *
 * 这一轮把结构定成：侧栏 = navigation 地标、顶栏+页签条 = banner 地标、内容区 = 唯一 main，
 * 页签面板（`role="tabpanel"`）作为 main 里的容器，页签的 `aria-controls` 仍指向它。
 */

const ADMIN_LAYOUT = fileURLToPath(new URL('../../app/layouts/admin.vue', import.meta.url))
const ADMIN_PAGES = fileURLToPath(new URL('../../app/pages/admin/', import.meta.url))

const layout = readFileSync(ADMIN_LAYOUT, 'utf8')

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const full = `${dir.replace(/\/$/, '')}/${name}`
    if (statSync(full).isDirectory()) walk(full, out)
    else if (name.endsWith('.vue')) out.push(full)
  }
  return out
}

describe('后台壳：每页唯一 main，不嵌套', () => {
  test('壳层只有一个 <main>，且内容区与页签面板分层', () => {
    expect(layout.match(/<main\b/g) ?? []).toHaveLength(1)
    expect(layout.match(/<\/main>/g) ?? []).toHaveLength(1)
    expect(layout).toContain('<main')
    // tabpanel 是 main 里的容器：aria-controls 指向它，aria-labelledby 指向激活页签。
    expect(layout).toContain('id="sforum-admin-main"')
    expect(layout).toContain('role="tabpanel"')
    expect(layout).toContain(':aria-labelledby="adminTabElementId(adminTabs.activeTabId.value)"')
    // main 不能同时是 tabpanel：显式 role 会覆盖 main 的地标语义。
    expect(layout).not.toContain('<main\n        id="sforum-admin-main"')
  })

  test('skip link 仍然指向主内容容器', () => {
    expect(layout).toContain('href="#sforum-admin-main"')
    expect(layout.indexOf('href="#sforum-admin-main"')).toBeLessThan(layout.indexOf('id="sforum-admin-main"'))
  })

  test('没有任何后台页面自建 <main>（否则与壳层 main 重复）', () => {
    const offenders = walk(ADMIN_PAGES).filter(file => /<main\b/.test(readFileSync(file, 'utf8')))
    expect(offenders.map(file => file.slice(file.indexOf('app/pages/')))).toEqual([])
  })
})

describe('后台壳：地标覆盖（axe region）', () => {
  test('侧栏整块是 navigation 地标，并带可访问名', () => {
    expect(layout).toContain('role="navigation"')
    expect(layout).toContain(':aria-label="t(\'admin.shell.navigationLabel\')"')
  })

  test('顶栏与页签条同属一个 header 地标', () => {
    const headerOpen = layout.indexOf('<header class="shrink-0">')
    const tabStrip = layout.indexOf('<SFAdminTabStrip')
    const headerClose = layout.indexOf('</header>', headerOpen)
    expect(headerOpen).toBeGreaterThan(-1)
    expect(headerClose).toBeGreaterThan(headerOpen)
    // 页签条必须落在 header 内部（顶栏与页签条之间不能再夹一个 </header>）。
    expect(tabStrip).toBeGreaterThan(headerOpen)
    expect(tabStrip).toBeLessThan(headerClose)
  })

  test('侧栏导航与页签条的地标名不同（landmark-unique）', () => {
    expect(layout).toContain('t(\'admin.shell.navigationLabel\')')
    expect(layout).toContain(':label="t(\'admin.shell.tabsLabel\')"')
  })
})

describe('后台壳：侧栏分组项的 ARIA 来源（axe aria-allowed-attr）', () => {
  test('分组项走自定义链接插槽，不再让 Nuxt UI 渲染带 aria-expanded 的 span', () => {
    // Nuxt UI 默认把分组项的 trailing 渲染成 `<span aria-expanded>`（role=generic 不允许该属性）。
    // 我们给分组项声明 slot: 'folder'，链接内容自己渲染，那个 span 就不再出现。
    expect(layout).toContain("slot: 'folder'")
    expect(layout).toContain('<template #folder="{ item }">')
    // 插槽 props 在 vue-tsc 里是 never（Nuxt UI 未声明自定义插槽类型），
    // 因此渲染数据走显式入参的 folderNavView()，模板不直接读 item 的属性。
    expect(layout).toContain('function folderNavView(item: SidebarNavigationItem)')
    // 箭头必须是装饰元素（aria-hidden），展开态用 group-data-[state=open] 表达。
    expect(layout).toContain('group-data-[state=open]:rotate-180')
  })

  test('品牌链接与高级设置按钮的可访问名包含可见文案（label-content-name-mismatch）', () => {
    // 品牌链接可见文案是「SForum 控制台」→ aria-label 由同样两段拼成。
    expect(layout).toContain(':aria-label="`${t(\'admin.shell.brand\')} ${t(\'admin.shell.section\')}`"')
    // 高级设置按钮不再用 aria-label 覆盖可见的「管理员:xxx」，改为追加 sr-only 动作名。
    expect(layout).toContain('<span class="sr-only">{{ t(\'admin.shell.openAdvancedSettings\') }}</span>')
    expect(layout).not.toContain(':aria-label="t(\'admin.shell.openAdvancedSettings\')"')
  })
})
