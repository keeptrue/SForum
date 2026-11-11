import { describe, expect, test } from 'bun:test'
import { computed, defineComponent } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import {
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeTier,
  forumBadgeTierIcon,
  sortProfileBadgeEntries,
  sortProfileBadgeGroups
} from '~/utils/forum/forumBadge'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 资料页徽章 tab 的渲染契约（M8 会按这些断言）：
 * 卡片根 class `sf-profile-badge-card`、未获得态 `data-earned="false"`、
 * 进度条 `role="progressbar"` + `aria-valuenow` / `aria-valuemax` / `aria-valuetext`。
 * 期望文案取自真实 zh-CN locale，不在这里自造。
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
  if (typeof template !== 'string') {
    throw new Error(`missing locale key: ${key}`)
  }
  return template.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
}

const UIconStub = defineComponent({
  name: 'UIcon',
  props: { name: { type: String, default: '' } },
  template: '<i :data-icon="name" />'
})

const tabPath = fileURLToPath(new URL('../../app/components/profile/SFProfileBadgeTab.vue', import.meta.url))
const SFProfileBadgeTab = await compileVueSfc(tabPath, 'sf-profile-badge-tab', {
  computed,
  useI18n: () => ({ t }),
  useSiteDateTime: () => ({ formatDateOnly: (value: string) => value.slice(0, 10) }),
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeTier,
  forumBadgeTierIcon,
  sortProfileBadgeEntries,
  sortProfileBadgeGroups
})

function mountTab(props: Record<string, unknown>) {
  return mount(SFProfileBadgeTab, {
    props,
    global: { components: { UIcon: UIconStub } }
  })
}

const groups = [
  {
    key: 'honor',
    name: '荣誉',
    position: 2,
    badges: [
      { slug: 'helper', name: '优质解答', tier: 'silver' as const, icon: 'i-lucide-award', position: 1, earned: true, current: 0, threshold: 0, grantedAt: '2026-11-03T08:00:00Z' }
    ]
  },
  {
    key: 'content',
    name: '内容',
    position: 1,
    badges: [
      { slug: 'first-post', name: '初次发帖', tier: 'bronze' as const, icon: 'i-lucide-zap', position: 1, earned: false, current: 3, threshold: 10, grantedAt: null },
      { slug: 'liked', name: '广受好评', tier: 'silver' as const, icon: 'i-lucide-thumbs-up', position: 2, earned: true, current: 100, threshold: 100, grantedAt: '2026-11-01T08:00:00Z' }
    ]
  }
]

describe('SFProfileBadgeTab', () => {
  test('groups follow position and entries follow tier weight', () => {
    const wrapper = mountTab({ groups })
    try {
      const titles = wrapper.findAll('.sf-badge-group__name').map(node => node.text())
      expect(titles).toEqual(['内容', '荣誉'])
      const slugs = wrapper.findAll('.sf-profile-badge-card').map(node => node.attributes('data-badge-slug'))
      expect(slugs).toEqual(['liked', 'first-post', 'helper'])
    } finally {
      wrapper.unmount()
    }
  })

  test('earned and unearned cards differ only by data-earned and the progress block', () => {
    const wrapper = mountTab({ groups })
    try {
      const earned = wrapper.get('[data-badge-slug="liked"]')
      expect(earned.attributes('data-earned')).toBe('true')
      expect(earned.attributes('data-tier')).toBe('silver')
      expect(earned.attributes('aria-label')).toBe('银牌徽章：广受好评，获得于 2026-11-01')
      expect(earned.find('[role="progressbar"]').exists()).toBe(false)

      const unearned = wrapper.get('[data-badge-slug="first-post"]')
      expect(unearned.attributes('data-earned')).toBe('false')
      expect(unearned.attributes('aria-label')).toBe('铜牌徽章：初次发帖，未获得，进度 3/10')
    } finally {
      wrapper.unmount()
    }
  })

  test('progress bar exposes now/max/text and a proportional fill width', () => {
    const wrapper = mountTab({ groups })
    try {
      const bar = wrapper.get('[role="progressbar"]')
      expect(bar.attributes('aria-valuenow')).toBe('3')
      expect(bar.attributes('aria-valuemax')).toBe('10')
      expect(bar.attributes('aria-valuetext')).toBe('3 / 10')
      expect(wrapper.get('.sf-profile-badge-card__fill').attributes('style')).toContain('width: 30%')
    } finally {
      wrapper.unmount()
    }
  })

  test('group header counts earned against total', () => {
    const wrapper = mountTab({ groups })
    try {
      const counts = wrapper.findAll('.sf-badge-group__count').map(node => node.text())
      expect(counts).toEqual(['1 / 2', '1 / 1'])
    } finally {
      wrapper.unmount()
    }
  })

  test('empty and failed states come from the locale, and retry re-emits', async () => {
    const empty = mountTab({ groups: [] })
    try {
      expect(empty.text()).toContain('还没有获得徽章')
    } finally {
      empty.unmount()
    }

    const failed = mountTab({ groups: [], failed: true })
    try {
      expect(failed.text()).toContain('徽章加载失败。')
      await failed.get('.sf-profile-badges__retry').trigger('click')
      expect(failed.emitted('retry')).toHaveLength(1)
    } finally {
      failed.unmount()
    }
  })

  test('loading renders three skeleton cards without exposing fake content', () => {
    const wrapper = mountTab({ groups, loading: true })
    try {
      expect(wrapper.findAll('.sf-profile-badge-card--skeleton')).toHaveLength(3)
      expect(wrapper.find('.sf-badge-group').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})
