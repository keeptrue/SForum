import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'

/**
 * 第十轮 a11y 的静态契约（簇 9 余 + 簇 17）。
 *
 * 这些条目大部分是「模板里少了一个属性」，渲染级用例抓不到（组件依赖太多 composable），
 * 所以按仓库既有做法做聚焦的静态断言：只读被点名的那一段，不整文件匹配实现字符串。
 * 每个用例都在报告里配了变异验证（改坏 → 红）。
 */

const read = (relative: string) => readFileSync(new URL(`../../${relative}`, import.meta.url), 'utf8')

/** 从 `startMarker` 处的开标签起，按同名标签深度找到它的闭合标签，返回整段源码。 */
function sliceElement(source: string, startMarker: string, tag = 'div'): string {
  const start = source.indexOf(startMarker)
  expect(start).toBeGreaterThanOrEqual(0)
  const open = new RegExp(`<${tag}\\b`, 'g')
  const close = new RegExp(`</${tag}>`, 'g')
  open.lastIndex = start
  let depth = 0
  let cursor = start
  while (cursor < source.length) {
    open.lastIndex = cursor
    close.lastIndex = cursor
    const nextOpen = open.exec(source)
    const nextClose = close.exec(source)
    if (!nextClose) break
    if (nextOpen && nextOpen.index < nextClose.index) {
      depth += 1
      cursor = nextOpen.index + 1
      continue
    }
    depth -= 1
    cursor = nextClose.index + 1
    if (depth === 0) {
      return source.slice(start, nextClose.index + `</${tag}>`.length)
    }
  }
  throw new Error(`未能闭合 ${startMarker}`)
}

describe('RV-21 自定义主题是组内第 6 个 radio', () => {
  const page = read('app/components/settings/SFAppearanceSettingsPage.vue')
  const group = sliceElement(page, '<div ref="themeGroup"')

  test('主题组是 radiogroup，自定义那项是 button 而不是 div', () => {
    expect(group).toContain('role="radiogroup"')
    expect(group).toContain('data-theme-option="custom"')
    // 旧实现：<div ... @click="mode === 'custom' && (themeMode = 'custom')">
    expect(page).not.toContain("@click=\"mode === 'custom' && (themeMode = 'custom')\"")
    expect(group).toContain('<button')
  })

  test('预设与自定义都是 radio，且都接上 roving tabindex 与方向键', () => {
    // 预设：v-for 一项 + 自定义：显式一项
    expect(group).toContain(':aria-checked="themeMode === choice.value"')
    expect(group).toContain(':aria-checked="themeMode === \'custom\'"')
    expect(group).toContain(':tabindex="radioGroupTabIndex(themeOptionValues, themeMode, choice.value)"')
    expect(group).toContain(':tabindex="radioGroupTabIndex(themeOptionValues, themeMode, \'custom\')"')
    expect(group).toContain('@keydown="handleRadioKeydown(themeGroup, themeOptionValues, choice.value, $event, selectTheme)"')
    expect(group).toContain('@keydown="handleRadioKeydown(themeGroup, themeOptionValues, \'custom\', $event, selectTheme)"')
  })

  test('取色器在 radiogroup 之外，选中自定义后才出现（无 nested-interactive）', () => {
    expect(group).not.toContain('type="color"')
    expect(page).toContain('data-custom-theme-controls')
    expect(page).toContain('v-if="themeMode === \'custom\'"')
  })
})

describe('RV-25 附件表格的键盘选中链路', () => {
  const tab = read('app/components/admin/settings/attachments/tabs/SFAdminAttachmentManagerTab.vue')

  test('行内文件单元格是可聚焦按钮，键盘触发会把焦点送到详情', () => {
    expect(tab).toContain('@click.stop="selectAttachment(item, { focusDetail: $event.detail === 0 })"')
    expect(tab).toContain('detailHeading.value?.focus()')
    expect(tab).toContain('ref="detailHeading"')
    expect(tab).toContain('tabindex="-1"')
  })

  test('选中态对读屏可见', () => {
    expect(tab).toContain(':aria-current="selected?.id === item.id ? \'true\' : undefined"')
  })
})

describe('RV-26 页脚链接三组输入都有带序号的可访问名称', () => {
  const tab = read('app/components/admin/settings/personalization/tabs/SFAdminPersonalizationAppearanceTab.vue')

  test('每组 3 个输入各带 {index} + {key} 的 aria-label', () => {
    for (const key of ['linkFieldZh', 'linkFieldEn', 'linkFieldUrl']) {
      expect(tab).toContain(`t('admin.personalization.footer.${key}', { index: linkIndex + 1, key:`)
    }
    expect(tab).toContain('v-for="(link, linkIndex) in state.form.footerLinks"')
  })
})

describe('RV-45 任务页 4 个 icon-only 按钮有名字', () => {
  const jobs = read('app/pages/admin/jobs.vue')

  test('详情 / 重试 / 取消 / 关闭详情 都同时给 aria-label 与 title', () => {
    for (const key of ['detail', 'retry', 'cancel', 'closeDetail']) {
      expect(jobs).toContain(`:aria-label="t('admin.jobs.${key}')"`)
      expect(jobs).toContain(`:title="t('admin.jobs.${key}')"`)
    }
  })
})

describe('UX2-09 筛选控件有可访问名称', () => {
  test('database 的列/运算符下拉', () => {
    const page = read('app/pages/admin/database.vue')
    expect(page).toContain(':aria-label="t(\'admin.database.filterColumnAria\')"')
    expect(page).toContain(':aria-label="t(\'admin.database.filterOperatorAria\')"')
  })

  test('jobs 的状态/队列筛选', () => {
    const page = read('app/pages/admin/jobs.vue')
    expect(page).toContain(':aria-label="t(\'admin.jobs.filterStateAria\')"')
    expect(page).toContain(':aria-label="t(\'admin.jobs.filterQueueAria\')"')
    expect(page).toContain(':aria-label="t(\'admin.jobs.filterKind\')"')
  })

  test('attachments 三个筛选下拉也有可访问名称（RV-25 触达面的 axe 复扫要求）', () => {
    const tab = read('app/components/admin/settings/attachments/tabs/SFAdminAttachmentManagerTab.vue')
    for (const key of ['filterProviderAria', 'filterStatusAria', 'filterReferenceAria']) {
      expect(tab).toContain(`:aria-label="t('admin.attachments.${key}')"`)
    }
  })

  test('registry-catalogs 的 label for 指向对应 USelect 的 id', () => {
    const page = read('app/pages/admin/extensions/registry-catalogs.vue')
    for (const id of ['registry-catalogs-dry-run-entity', 'registry-catalogs-dry-run-action']) {
      expect(page).toContain(`<label for="${id}"`)
      expect(page).toContain(`id="${id}"`)
    }
  })
})

describe('UX2-10 登录方式页 3 个开关有可访问名称', () => {
  const page = read('app/pages/admin/settings/login-methods.vue')

  test('每个开关的 aria-label 带操作类型与提供方', () => {
    for (const op of ['login', 'registration', 'link']) {
      expect(page).toContain(`:aria-label="operationSwitchLabel('${op}')"`)
      expect(page).toContain(`:title="operationSwitchLabel('${op}')"`)
    }
    expect(page).toContain("t('admin.loginMethods.ops.switchAriaLabel', {")
  })
})

describe('UX2-17 共享 live region 与 6 个异步面', () => {
  test('列表异步面：失败不播成功（M1）且首屏要播（M2）', () => {
    const jobs = read('app/pages/admin/jobs.vue')
    expect(jobs).toContain('announceAsyncResult({')
    expect(jobs).toContain('pending: manager.jobs.pending.value,')
    expect(jobs).toContain('failed: Boolean(manager.jobs.error.value),')
    expect(jobs).toContain("failure: t('admin.jobs.listLoadFailed')")
    expect(jobs).toContain('}, { immediate: true })')

    const plugins = read('app/pages/admin/extensions/plugins.vue')
    expect(plugins).toContain('announceAsyncResult({')
    expect(plugins).toContain('pending: pending.value,')
    expect(plugins).toContain('failed: Boolean(error.value),')
    expect(plugins).toContain("failure: t('admin.extensions.plugins.listLoadFailed')")
    expect(plugins).toContain('}, { immediate: true })')
  })

  test('live region 原语是常驻的 polite + assertive 两条通道', () => {
    const component = read('app/components/a11y/SFLiveRegion.vue')
    expect(component).toContain('aria-live="polite"')
    expect(component).toContain('role="status"')
    expect(component).toContain('aria-live="assertive"')
    expect(component).toContain('role="alert"')
    expect(component).toContain('data-sf-live-region="polite"')
  })

  test('后台壳层挂载一次，并在路由标题变化时公告', () => {
    const layout = read('app/layouts/admin.vue')
    expect(layout).toContain('<SFLiveRegion />')
    expect(layout).toContain("import { announcePolite } from '~/utils/a11y/liveRegion'")
    expect(layout).toContain('watch(activeTabLabel, (label) => {')
    expect(layout).toContain('announcePolite(label)')
  })

  test('恰好 6 个异步面接入共享播报（polite 或 asyncResult）', () => {
    const files = execFileSync('grep', ['-rlE', 'announcePolite\\(|announceAsyncResult\\(', 'app'], { encoding: 'utf8' })
      .trim()
      .split('\n')
      .filter(Boolean)
      .filter(file => !file.startsWith('app/utils/a11y/'))
      .sort()

    expect(files).toEqual([
      'app/components/admin/settings/attachments/tabs/SFAdminAttachmentManagerTab.vue',
      'app/layouts/admin.vue',
      'app/pages/admin/database.vue',
      'app/pages/admin/extensions/plugins.vue',
      'app/pages/admin/jobs.vue',
      'app/pages/admin/settings/avatar.vue',
      'app/pages/admin/settings/features.vue'
    ])
    // 壳层 1 + 异步面 6
    expect(files).toHaveLength(7)
  })
})

describe('UX2-08 原生 select 不再落在 UFormField 里', () => {
  /** 扫每个 `<select`，用 UFormField 的开闭标签算嵌套深度。 */
  function selectsInsideFormField(source: string): number[] {
    const events: Array<{ line: number, open: boolean, index: number }> = []
    const re = /<UFormField\b|<\/UFormField>/g
    let match: RegExpExecArray | null
    while ((match = re.exec(source))) {
      events.push({ line: source.slice(0, match.index).split('\n').length, open: match[0].startsWith('<UFormField'), index: match.index })
    }

    const offenders: number[] = []
    const lines = source.split('\n')
    lines.forEach((line, index) => {
      const selectIndex = line.indexOf('<select')
      if (selectIndex < 0) return
      const lineNumber = index + 1
      // 行首偏移必须把每行结尾的换行符算进去，否则 same-line 的 UFormField 会被漏掉。
      const absolute = lines.slice(0, index).reduce((total, text) => total + text.length + 1, 0) + selectIndex
      let depth = 0
      for (const event of events) {
        if (event.index < absolute) depth += event.open ? 1 : -1
      }
      if (depth > 0) offenders.push(lineNumber)
    })
    return offenders
  }

  test('app 下没有任何原生 select 还嵌在 UFormField 里', () => {
    const files = execFileSync('grep', ['-rl', '<select', 'app'], { encoding: 'utf8' })
      .trim()
      .split('\n')
      .filter(Boolean)

    const offenders = files.flatMap(file =>
      selectsInsideFormField(readFileSync(file, 'utf8')).map(line => `${file}:${line}`)
    )

    expect(offenders).toEqual([])
  })

  test('31 个落点全部换成 SFNativeSelect（pages 9 / components 22）', () => {
    const files = execFileSync('grep', ['-rl', '<SFNativeSelect', 'app'], { encoding: 'utf8' })
      .trim()
      .split('\n')
      .filter(Boolean)
      .filter(file => !file.endsWith('forms/SFNativeSelect.vue'))

    const pages = files.filter(file => file.startsWith('app/pages/'))
    const components = files.filter(file => file.startsWith('app/components/'))

    const count = (list: string[]) => list.reduce((total, file) => {
      const source = readFileSync(file, 'utf8')
      return total + (source.match(/<SFNativeSelect\b/g)?.length ?? 0)
    }, 0)

    expect(count(pages)).toBe(9)
    expect(count(components)).toBe(22)
  })
})
