import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, ref } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import {
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeRequirementText,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle
} from '~/utils/forum/forumBadge'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 公开面「获得条件」句子的读侧契约。
 *
 * 句子由服务端从规则 AST 渲染成人话并逐语言回落，前端只有一件事：**选语言**。
 * 所以断言分三层——解析器（选语言 / 回落 / 空）、卡片（有句才画一行）、
 * 以及「公开面不许自己渲染 AST」（静态边界 + 输出词边界正则）。
 */
const messages = JSON.parse(
  readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
) as Record<string, unknown>

function lookup(path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    messages
  )
}

function t(key: string, params: Record<string, unknown> = {}) {
  const template = lookup(key)
  if (typeof template !== 'string') throw new Error(`missing locale key: ${key}`)
  return template.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
}

const has = (key: string) => typeof lookup(key) === 'string'

const UIconStub = defineComponent({
  name: 'UIcon',
  props: { name: { type: String, default: '' } },
  template: '<i :data-icon="name" />'
})

const cardPath = fileURLToPath(new URL('../../app/components/forum/badges/SFBadgeCard.vue', import.meta.url))
const cardSource = readFileSync(cardPath, 'utf8')

const SFBadgeCard = await compileVueSfc(cardPath, 'sf-badge-card', {
  computed,
  useI18n: () => ({ t, te: has, locale: ref('zh-CN') }),
  useSiteDateTime: () => ({ formatDateOnly: (value: string) => value.slice(0, 10) }),
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle
})

const BILINGUAL = { zh: '在任意版块发布 10 个主题', en: 'Publish 10 topics in any category' }

function mountCard(description?: string) {
  return mount(SFBadgeCard, {
    props: {
      badge: { slug: 'first-post', name: '初次发帖', tier: 'bronze', icon: '', position: 0, earned: false },
      ...(description === undefined ? {} : { description })
    },
    global: { components: { UIcon: UIconStub } }
  })
}

describe('forumBadgeRequirementText · language selection', () => {
  test('zh prefix picks zh, everything else picks en', () => {
    const badge = { requirementText: BILINGUAL }
    expect(forumBadgeRequirementText(badge, 'zh-CN')).toBe(BILINGUAL.zh)
    expect(forumBadgeRequirementText(badge, 'zh-TW')).toBe(BILINGUAL.zh)
    expect(forumBadgeRequirementText(badge, 'en-US')).toBe(BILINGUAL.en)
    // 未知 locale 不做猜测：非 zh 前缀一律走 en（与 pickTierName 同口径）。
    expect(forumBadgeRequirementText(badge, 'ja-JP')).toBe(BILINGUAL.en)
  })

  test('an empty selected language falls back to the other, and both empty yields an empty string', () => {
    expect(forumBadgeRequirementText({ requirementText: { zh: '', en: BILINGUAL.en } }, 'zh-CN')).toBe(BILINGUAL.en)
    expect(forumBadgeRequirementText({ requirementText: { zh: BILINGUAL.zh, en: '' } }, 'en-US')).toBe(BILINGUAL.zh)
    // 多条件徽章的句子为空：返回空串，让宿主据此不渲染整行。
    expect(forumBadgeRequirementText({ requirementText: { zh: '', en: '' } }, 'zh-CN')).toBe('')
    expect(forumBadgeRequirementText({ requirementText: null }, 'zh-CN')).toBe('')
    expect(forumBadgeRequirementText({}, 'en-US')).toBe('')
  })

  test('never surfaces rule AST members, only the server sentence', () => {
    const text = forumBadgeRequirementText({
      requirementText: { zh: '回复 5 个不同的 topics，每条父回复算 1 次', en: 'Reply in 5 topics; each parent property counts once' }
    }, 'zh-CN')
    expect(text).toContain('topics')
    // 词边界正则：子串匹配会把 `topics` 判成 `op`、`params` 里的 `param` 判成 `params` 前缀。
    expect(text).not.toMatch(/\b(cmp|params|children|op|rule)\b/)
  })
})

describe('SFBadgeCard description seam', () => {
  test('renders the sentence the host passes', () => {
    const wrapper = mountCard(BILINGUAL.zh)
    try {
      expect(wrapper.get('.sf-profile-badge-card__description').text()).toBe(BILINGUAL.zh)
    } finally {
      wrapper.unmount()
    }
  })

  test('renders no row when the host passes an empty description', () => {
    for (const description of ['', undefined]) {
      const wrapper = mountCard(description)
      try {
        // 删掉模板上的 v-if 会立刻变红：空串时 span 仍在，只是内容为空。
        expect(wrapper.find('.sf-profile-badge-card__description').exists()).toBe(false)
        expect(wrapper.text()).not.toContain(BILINGUAL.zh)
      } finally {
        wrapper.unmount()
      }
    }
  })

  test('the description block clamps to two lines instead of a single nowrap row', () => {
    const block = cardSource.match(/\.sf-profile-badge-card__description\s*\{([^}]*)\}/)?.[1] ?? ''
    expect(block).toContain('display: -webkit-box')
    expect(block).toContain('-webkit-box-orient: vertical')
    expect(block).toContain('-webkit-line-clamp: 2')
    expect(block).toContain('white-space: normal')
    expect(block).toContain('overflow-wrap: anywhere')
    // 单行的两条痕迹都不许回来：nowrap + text-overflow。
    expect(block).not.toContain('white-space: nowrap')
    expect(block).not.toContain('text-overflow')
    // 不写死高度：行数交给 line-height。
    expect(block).not.toMatch(/(?:^|\s)height:/)
  })
})

describe('public badge surfaces stay free of AST rendering', () => {
  const publicSources = [
    fileURLToPath(new URL('../../app/utils/forum/forumBadge.ts', import.meta.url)),
    cardPath,
    fileURLToPath(new URL('../../app/components/profile/SFProfileBadgeTab.vue', import.meta.url))
  ]

  // 注释里提到「禁止 describeRule」是文档，不是调用：先剥注释再查。
  function code(path: string): string {
    return readFileSync(path, 'utf8')
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .replace(/(^|\s)\/\/.*$/gm, ' ')
  }

  test('no public badge source imports or calls the admin rule renderer', () => {
    for (const path of publicSources) {
      const source = code(path)
      expect({ path, describeRule: /\bdescribeRule\b/.test(source) }).toEqual({ path, describeRule: false })
      expect({ path, adminRule: source.includes('admin/badgeRule') }).toEqual({ path, adminRule: false })
    }
  })
})
