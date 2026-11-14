import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

/**
 * 资料页头部不再渲染「个人简介（bio）」行。
 *
 * 头部身份块收敛为昵称行（h1 + 佩戴徽章 chip）与身份行（@handle / UID / TL 等级），
 * 其后是 meta 行（位置 / 网站 / 加入时间）；个人简介只在「关于」tab 出现一次，
 * 避免同一段文字在首屏与详情里重复占位。
 *
 * 本文件锁的是源契约（页面组件 + 样式 + i18n 键）：
 * - 头部不再有 `.sf-profile-intro__bio`（含空态变体），CSS 里也不许残留同名规则；
 * - 「关于」tab 仍渲染 bio 与空态文案 `profile.bioEmpty`；
 * - `profile.bioEmpty` 键必须留在两个 locale 里（关于 tab 与资料设置预览共用），不许删。
 *
 * 两条抗误报约束：
 * 1. 对空白不敏感——模板换行、缩进调整、属性顺序调整都不该让用例误报；
 *    做法是把片段压成单行（`squash`）后用正则匹配，属性用前瞻断言而不是固定顺序。
 * 2. 对 class 只做 token 边界匹配——`class="sf-profile-intro__meta sf-extra"`
 *    里的追加工具类不该误报，而 `sf-profile-intro__meta--empty` 是**另一个**类名，
 *    也不该被当成命中。边界因此排除字母/数字/下划线/连字符（见 `classTokenPattern`）。
 */

const pagePath = fileURLToPath(new URL('../../app/components/profile/SFProfileShowPage.vue', import.meta.url))
const cssPath = fileURLToPath(new URL('../../app/assets/css/sforum-profile.css', import.meta.url))
const zhPath = fileURLToPath(new URL('../../i18n/locales/zh-CN.json', import.meta.url))
const enPath = fileURLToPath(new URL('../../i18n/locales/en-US.json', import.meta.url))

const source = (path: string) => readFileSync(path, 'utf8')

/** 把连续空白压成单个空格：让断言只关心结构，不关心排版。 */
function squash(text: string) {
  return text.replace(/\s+/g, ' ').trim()
}

/**
 * class 属性里的 token 边界匹配：class 列表中含该类名才算命中。
 *
 * 边界用前后瞻排除字母/数字/下划线/连字符：
 * - `class="sf-profile-intro__meta sf-extra"` 命中（追加工具类不误报）；
 * - `class="sf-profile-intro__meta--empty"` 不命中（那是另一个类名）。
 */
function classTokenPattern(name: string) {
  const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return new RegExp(`class="[^"]*(?<![\\w-])${escaped}(?![\\w-])[^"]*"`)
}

/** 「某个标签带这个 class token」：属性顺序与额外类名都不敏感。 */
function tagWithClassPattern(tag: string, name: string) {
  return new RegExp(`<${tag}\\b[^>]*${classTokenPattern(name).source}`)
}

/** CSS 选择器里的 class token 边界匹配（`.foo` 形式的 token）。 */
function cssSelectorPattern(name: string) {
  const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return new RegExp(`\\.${escaped}(?![\\w-])`)
}

/**
 * 「关于」tab 段落：从 about 分支起点到下一个分支起点，避免把头部段落混进来。
 *
 * 结束锚点（下一个 tab 分支）找不到＝契约漂移，必须显式失败：静默延伸到文件末尾
 * 会把别的段落一起吞进来，锚点改名这类回归反而测不出来。
 */
function aboutTabSection(page: string) {
  const start = page.search(/activeTab\s*===\s*'about'/)
  expect(start).toBeGreaterThan(-1)
  const rest = page.slice(start)
  const end = rest.search(/activeTab\s*===\s*'bookmarks'/)
  expect(end).toBeGreaterThan(-1)
  return squash(rest.slice(0, end))
}

/**
 * 头部 intro 段落：从 header 起点到 tab 导航起点。
 *
 * 两个锚点都按 class token 匹配（`tagWithClassPattern`），所以给 `<header>` 或 `<nav>`
 * 追加工具类不会让段落切分失效；被测类名被改名或删掉时锚点找不到，用例照样红。
 *
 * 结束锚点（tab 导航）找不到时同样显式失败，不静默把头部段落延伸到文件末尾。
 */
function headerSection(page: string) {
  const start = page.search(tagWithClassPattern('header', 'sf-profile-intro'))
  expect(start).toBeGreaterThan(-1)
  const rest = page.slice(start)
  const end = rest.search(tagWithClassPattern('nav', 'sf-profile-tabs'))
  expect(end).toBeGreaterThan(-1)
  return squash(rest.slice(0, end))
}

describe('profile header no longer renders the bio line', () => {
  test('the header intro block has no bio paragraph', () => {
    const header = headerSection(source(pagePath))

    // 头部简介行（含 --empty 空态变体）都已删除。
    expect(header).not.toMatch(classTokenPattern('sf-profile-intro__bio'))
    expect(header).not.toMatch(classTokenPattern('sf-profile-intro__bio--empty'))
    // meta 行保留：位置 / 网站 / 加入时间仍是头部的一部分。
    expect(header).toMatch(tagWithClassPattern('div', 'sf-profile-intro__meta'))
    expect(header).toMatch(tagWithClassPattern('div', 'sf-profile-intro__name-row'))
    expect(header).toMatch(tagWithClassPattern('div', 'sf-profile-intro__identity-meta'))
  })

  test('the whole page component carries no intro bio class', () => {
    const page = source(pagePath)

    expect(page).not.toMatch(classTokenPattern('sf-profile-intro__bio'))
    expect(page).not.toMatch(classTokenPattern('sf-profile-intro__bio--empty'))
  })

  test('the stylesheet drops the intro bio rules', () => {
    const css = source(cssPath)

    expect(css).not.toMatch(cssSelectorPattern('sf-profile-intro__bio'))
    expect(css).not.toMatch(cssSelectorPattern('sf-profile-intro__bio--empty'))
    // meta 样式仍在：只删 bio 规则，不顺手删头部其它规则。
    expect(css).toMatch(cssSelectorPattern('sf-profile-intro__meta'))
  })

  test('the about tab still renders the bio and its empty state', () => {
    const about = aboutTabSection(source(pagePath))

    // 正文段：v-if 条件与插值都指向 profile.profile.bio，属性顺序不敏感。
    expect(about).toMatch(
      /<p(?=[^>]*v-if="profile\.profile\.bio")[^>]*>\s*\{\{\s*profile\.profile\.bio\s*\}\}\s*<\/p>/
    )
    // 空态段：v-else 分支 + sf-profile-about__empty 类 token + profile.bioEmpty 文案键。
    expect(about).toMatch(new RegExp(
      `<p(?=[^>]*v-else)(?=[^>]*${classTokenPattern('sf-profile-about__empty').source})[^>]*>\\s*\\{\\{\\s*t\\('profile\\.bioEmpty'\\)\\s*\\}\\}\\s*<\\/p>`
    ))
    // 关于 tab 里不许混进头部那份 bio 类名（含空态变体）。
    expect(about).not.toMatch(classTokenPattern('sf-profile-intro__bio'))
    expect(about).not.toMatch(classTokenPattern('sf-profile-intro__bio--empty'))
  })

  test('the SEO description still uses the bio', () => {
    const page = squash(source(pagePath))

    expect(page).toContain("description: profile.value?.profile.bio || t('profile.metaDescription')")
  })
})

describe('profile.bioEmpty locale key is kept', () => {
  test('zh-CN still ships profile.bioEmpty', () => {
    const locale = JSON.parse(source(zhPath)) as { profile?: { bioEmpty?: string } }

    expect(typeof locale.profile?.bioEmpty).toBe('string')
    expect(locale.profile?.bioEmpty).not.toBe('')
  })

  test('en-US still ships profile.bioEmpty', () => {
    const locale = JSON.parse(source(enPath)) as { profile?: { bioEmpty?: string } }

    expect(typeof locale.profile?.bioEmpty).toBe('string')
    expect(locale.profile?.bioEmpty).not.toBe('')
  })
})
