import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import {
  BACKFILL_PROGRESS_TTL_MS,
  MAX_BACKFILL_BADGE_NAMES,
  backfillProgressCopy,
  normalizeBackfillStatusPayload,
  summarizeBackfillRuns,
  type AdminBadgeBackfillRun
} from '~/utils/admin/adminBadges'
import { useAdminBadgeBackfillStatus } from '~/composables/admin/badges/useAdminBadgeBackfillStatus'
import { compileVueSfc, mount, testVue } from '../helpers/vueSfc'

/**
 * 回扫进度行（计划 §13#15）。
 *
 * 断言按**行为类**写，不锁测试名/实现细节：
 * - 读侧只信 job 写的游标（不从授予数反推）；
 * - 三态聚合优先级（running > failed > completed）与「最多 3 个名字 + 等 N 枚」；
 * - completed 24h 后收起，`items: []` 不占位；
 * - 端点失败 → 空清单 + 可见提示，不抛错；
 * - 文案值级（键在值空要红），且不含 `cmp`/`params`/`children`/`op`/`rule` 这类内部字段名。
 * - 组件级：三态与降级提示真的渲染出来，刷新按钮有 aria-label/title 且只触发一次刷新。
 */
const localesRoot = new URL('../../i18n/locales/', import.meta.url)
const zh = JSON.parse(readFileSync(new URL('zh-CN.json', localesRoot), 'utf8')) as Record<string, unknown>
const en = JSON.parse(readFileSync(new URL('en-US.json', localesRoot), 'utf8')) as Record<string, unknown>

function lookup(node: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>(
    (current, key) => (current && typeof current === 'object' ? (current as Record<string, unknown>)[key] : undefined),
    node
  )
}

/** 把 `t(key, params)` 绑到真实 locale：断言最终句子，而不是断言源码里出现过某个键。 */
function translateWith(locale: Record<string, unknown>) {
  return (key: string, params: Record<string, unknown> = {}) => {
    const template = lookup(locale, key)
    if (typeof template !== 'string' || template === '') throw new Error(`missing or empty locale key: ${key}`)
    return template.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
  }
}

const tZh = translateWith(zh)
const tEn = translateWith(en)

const NOW = Date.parse('2026-11-12T12:00:00Z')

function run(overrides: Partial<AdminBadgeBackfillRun> = {}): AdminBadgeBackfillRun {
  return {
    badgeId: 1,
    slug: 'active-star',
    name: '活跃之星',
    status: 'running',
    lastUserId: 0,
    failedCursorUserId: 0,
    startedAt: new Date(NOW - 60_000).toISOString(),
    updatedAt: new Date(NOW - 30_000).toISOString(),
    finishedAt: '',
    error: '',
    ...overrides
  }
}

describe('backfill payload normalization', () => {
  test('accepts the frozen { items: [...] } shape', () => {
    const rows = normalizeBackfillStatusPayload({ items: [run({ badgeId: 7 })] })
    expect(rows).toHaveLength(1)
    expect(rows?.[0]?.badgeId).toBe(7)
  })

  test('an empty items array is a valid empty reading, not a failure', () => {
    expect(normalizeBackfillStatusPayload({ items: [] })).toEqual([])
  })

  test('rejects a bare array so contract drift cannot stay green', () => {
    // 只认一种形状：后端改响应形状时必须红，而不是被兼容分支悄悄吃掉。
    expect(normalizeBackfillStatusPayload([run()])).toBeNull()
    expect(normalizeBackfillStatusPayload(null)).toBeNull()
    expect(normalizeBackfillStatusPayload({ items: 'nope' })).toBeNull()
  })

  test('drops unusable rows but keeps the rest of the reading', () => {
    const rows = normalizeBackfillStatusPayload({
      items: [
        run({ badgeId: 3, status: 'running' }),
        { badgeId: 0, status: 'running' },
        { badgeId: 4, status: 'queued' },
        { status: 'failed' },
        'garbage'
      ]
    })
    expect(rows?.map(row => row.badgeId)).toEqual([3])
  })

  test('falls back to slug then id so the row never renders a blank badge name', () => {
    const rows = normalizeBackfillStatusPayload({
      items: [
        { badgeId: 9, slug: 'quiet-badge', name: '', status: 'failed', failedCursorUserId: 42 },
        { badgeId: 10, status: 'failed' }
      ]
    })
    expect(rows?.[0]?.name).toBe('quiet-badge')
    expect(rows?.[1]?.name).toBe('#10')
    expect(rows?.[0]?.failedCursorUserId).toBe(42)
  })
})

describe('useAdminBadgeBackfillStatus', () => {
  test('loads runs on success and clears the failure flag', async () => {
    const store = useAdminBadgeBackfillStatus(async () => ({ items: [run({ badgeId: 5, lastUserId: 640 })] }))
    await store.load()
    expect(store.failed.value).toBe(false)
    expect(store.pending.value).toBe(false)
    expect(store.loaded.value).toBe(true)
    expect(store.runs.value).toHaveLength(1)
    expect(store.runs.value[0]?.lastUserId).toBe(640)
  })

  test('a throwing endpoint degrades to an empty list plus a visible failure flag', async () => {
    const store = useAdminBadgeBackfillStatus(async () => {
      throw new Error('500')
    })
    await store.load()
    expect(store.runs.value).toEqual([])
    expect(store.failed.value).toBe(true)
    expect(store.loaded.value).toBe(true)
  })

  test('a wrong-shaped payload is treated as a failure, not as "nothing running"', async () => {
    const store = useAdminBadgeBackfillStatus(async () => [] as unknown)
    await store.load()
    expect(store.runs.value).toEqual([])
    expect(store.failed.value).toBe(true)
  })

  test('an empty items list is success with nothing to show', async () => {
    const store = useAdminBadgeBackfillStatus(async () => ({ items: [] }))
    await store.load()
    expect(store.runs.value).toEqual([])
    expect(store.failed.value).toBe(false)
  })

  test('a rerun after failure recovers the same instance', async () => {
    let call = 0
    const store = useAdminBadgeBackfillStatus(async () => {
      call += 1
      if (call === 1) throw new Error('offline')
      return { items: [run({ status: 'completed', finishedAt: new Date(NOW).toISOString() })] }
    })
    await store.load()
    expect(store.failed.value).toBe(true)
    await store.load()
    expect(store.failed.value).toBe(false)
    expect(store.runs.value).toHaveLength(1)
  })
})

describe('summarizeBackfillRuns · aggregation priority', () => {
  test('nothing to render for no runs', () => {
    expect(summarizeBackfillRuns([], NOW)).toBeNull()
  })

  test('running wins over failed and completed, and reports the furthest cursor', () => {
    const summary = summarizeBackfillRuns([
      run({ badgeId: 1, status: 'completed', finishedAt: new Date(NOW - 1000).toISOString() }),
      run({ badgeId: 2, status: 'failed', failedCursorUserId: 77 }),
      run({ badgeId: 3, status: 'running', lastUserId: 120, name: '首帖达人' }),
      run({ badgeId: 4, status: 'running', lastUserId: 640, name: '邀请达人' })
    ], NOW)
    expect(summary?.status).toBe('running')
    // 多条在跑时取推进得最远的那条游标：进度行回答「已经扫到哪」。
    expect(summary?.userId).toBe(640)
    expect(summary?.badgeNames).toEqual(['首帖达人', '邀请达人'])
  })

  test('failed wins over completed and carries the failing cursor', () => {
    const summary = summarizeBackfillRuns([
      run({ badgeId: 1, status: 'completed', finishedAt: new Date(NOW - 1000).toISOString() }),
      run({ badgeId: 2, status: 'failed', failedCursorUserId: 77, name: '活跃之星' })
    ], NOW)
    expect(summary?.status).toBe('failed')
    expect(summary?.userId).toBe(77)
    expect(summary?.badgeNames).toEqual(['活跃之星'])
  })

  test('caps the visible badge names at three and counts the rest', () => {
    const runs = ['A', 'B', 'C', 'D', 'E'].map((name, index) =>
      run({
        badgeId: index + 1,
        status: 'failed',
        name,
        failedCursorUserId: 10 + index,
        updatedAt: new Date(NOW - index * 1000).toISOString()
      })
    )
    const summary = summarizeBackfillRuns(runs, NOW)
    expect(summary?.badgeNames).toHaveLength(MAX_BACKFILL_BADGE_NAMES)
    expect(summary?.hiddenCount).toBe(2)
    // 最近有动静的排前面，名字与失败游标来自同一条 run（不混搭）。
    expect(summary?.badgeNames).toEqual(['A', 'B', 'C'])
    expect(summary?.userId).toBe(10)
  })
})

describe('summarizeBackfillRuns · a finished run expires instead of occupying the row forever', () => {
  test('a completed run inside 24h still shows', () => {
    const summary = summarizeBackfillRuns([
      run({ status: 'completed', finishedAt: new Date(NOW - 23 * 60 * 60 * 1000).toISOString() })
    ], NOW)
    expect(summary?.status).toBe('completed')
    expect(summary?.userId).toBe(0)
  })

  test('a completed run older than 24h renders nothing', () => {
    expect(summarizeBackfillRuns([
      run({ status: 'completed', finishedAt: new Date(NOW - BACKFILL_PROGRESS_TTL_MS - 1000).toISOString() })
    ], NOW)).toBeNull()
  })

  test('a completed run without any usable timestamp also renders nothing', () => {
    // 无法判断新鲜度的「已完成」比不显示更糟：它会让运营以为刚跑过。
    expect(summarizeBackfillRuns([
      run({ status: 'completed', finishedAt: '', updatedAt: '', startedAt: '' })
    ], NOW)).toBeNull()
  })

  test('a stale completion does not hide a live failure', () => {
    const summary = summarizeBackfillRuns([
      run({ badgeId: 1, status: 'completed', finishedAt: new Date(NOW - BACKFILL_PROGRESS_TTL_MS - 1000).toISOString() }),
      run({ badgeId: 2, status: 'failed', failedCursorUserId: 33 })
    ], NOW)
    expect(summary?.status).toBe('failed')
  })
})

describe('backfillProgressCopy · value-level copy', () => {
  const running = summarizeBackfillRuns([
    run({ badgeId: 1, status: 'running', lastUserId: 128, name: '活跃之星' }),
    run({ badgeId: 2, status: 'running', lastUserId: 100, name: '首帖达人', updatedAt: new Date(NOW - 60_000).toISOString() }),
    run({ badgeId: 3, status: 'running', lastUserId: 90, name: '邀请达人', updatedAt: new Date(NOW - 90_000).toISOString() }),
    run({ badgeId: 4, status: 'running', lastUserId: 80, name: '收藏家', updatedAt: new Date(NOW - 120_000).toISOString() }),
    run({ badgeId: 5, status: 'running', lastUserId: 70, name: '追问者', updatedAt: new Date(NOW - 150_000).toISOString() })
  ], NOW)!
  const failed = summarizeBackfillRuns([
    run({ badgeId: 6, status: 'failed', name: '问答之星', failedCursorUserId: 771 })
  ], NOW)!
  const completed = summarizeBackfillRuns([
    run({ badgeId: 7, status: 'completed', name: '年度贡献', finishedAt: new Date(NOW - 1000).toISOString() })
  ], NOW)!

  test('running says where it has scanned to, with at most three names + the rest count', () => {
    const copy = backfillProgressCopy(running, tZh)
    expect(copy.title).toBe('回扫进行中：已扫描到用户 #128')
    expect(copy.description).toBe('涉及徽章：活跃之星、首帖达人、邀请达人 等 2 枚')
  })

  test('failed names both the badge and the exact user id', () => {
    const copy = backfillProgressCopy(failed, tZh)
    expect(copy.title).toBe('回扫失败于用户 #771')
    expect(copy.description).toContain('问答之星')
    expect(copy.description).toBe('涉及徽章：问答之星')
  })

  test('completed stays a short confirmation', () => {
    const copy = backfillProgressCopy(completed, tZh)
    expect(copy.title).toBe('回扫已完成')
    expect(copy.description).toBe('涉及徽章：年度贡献')
  })

  test('English copy is real copy, not a key echo', () => {
    expect(backfillProgressCopy(failed, tEn).title).toBe('Backfill failed at user #771')
    expect(backfillProgressCopy(completed, tEn).title).toBe('Backfill completed')
    expect(backfillProgressCopy(running, tEn).description).toBe('Badges: 活跃之星, 首帖达人, 邀请达人 and 2 more')
  })

  test('never leaks internal rule field names', () => {
    for (const summary of [running, failed, completed]) {
      for (const locale of [tZh, tEn]) {
        const copy = backfillProgressCopy(summary, locale)
        const text = `${copy.title} ${copy.description}`
        // 词边界匹配：`topics` 不是 `op`，别把正常词误判成字段名泄露。
        expect(text).not.toMatch(/\b(cmp|params|children|op|rule)\b/)
      }
    }
  })
})

// ---- 组件级：真的渲染出来，而不是只验证纯函数 ----

const rowPath = fileURLToPath(new URL('../../app/components/admin/badges/SFAdminBadgeBackfillRow.vue', import.meta.url))

const alertStub = {
  props: ['title', 'description', 'color', 'variant', 'icon'],
  setup: (props: Record<string, unknown>) => () => testVue.h('div', { class: 'alert-stub' }, [
    testVue.h('p', String(props.title ?? '')),
    testVue.h('p', String(props.description ?? ''))
  ])
}

const buttonStub = {
  props: ['ariaLabel', 'title', 'icon', 'loading', 'color', 'variant'],
  emits: ['click'],
  setup: (props: Record<string, unknown>, { emit }: { emit: (event: string) => void }) => () =>
    testVue.h('button', {
      class: 'button-stub',
      title: String(props.title ?? ''),
      'aria-label': String(props.ariaLabel ?? ''),
      onClick: () => emit('click')
    }, String(props.title ?? ''))
}

async function mountRow(props: Record<string, unknown>, t: (key: string, params?: Record<string, unknown>) => string) {
  // 编译后的 SFC 剥离了 import，运行时标识符必须按名注入。
  const component = await compileVueSfc(rowPath, 'badge-backfill-row', {
    useI18n: () => ({ t }),
    backfillProgressCopy
  })
  const wrapper = mount(component, {
    props,
    global: { stubs: { UAlert: alertStub, UButton: buttonStub, UIcon: true } }
  })
  return wrapper
}

describe('SFAdminBadgeBackfillRow · three states and the degraded hint', () => {
  test('renders nothing when there is no run to show', async () => {
    const wrapper = await mountRow({ summary: null, failed: false }, tZh)
    expect(wrapper.find('.alert-stub').exists()).toBe(false)
    expect(wrapper.find('.button-stub').exists()).toBe(false)
  })

  test('renders the running copy', async () => {
    const summary = summarizeBackfillRuns([run({ status: 'running', lastUserId: 128, name: '活跃之星' })], NOW)
    const wrapper = await mountRow({ summary, failed: false }, tZh)
    // 稳定选择器：老运的 390 验收按它定位这一行。
    expect(wrapper.find('[data-testid="badge-backfill-progress"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('回扫进行中：已扫描到用户 #128')
    expect(wrapper.text()).toContain('活跃之星')
  })

  test('renders the failed copy with badge name and user id', async () => {
    const summary = summarizeBackfillRuns([run({ status: 'failed', name: '问答之星', failedCursorUserId: 771 })], NOW)
    const wrapper = await mountRow({ summary, failed: false }, tZh)
    expect(wrapper.text()).toContain('回扫失败于用户 #771')
    expect(wrapper.text()).toContain('问答之星')
  })

  test('renders the completed copy', async () => {
    const summary = summarizeBackfillRuns([run({
      status: 'completed',
      name: '年度贡献',
      finishedAt: new Date(NOW - 1000).toISOString()
    })], NOW)
    const wrapper = await mountRow({ summary, failed: false }, tZh)
    expect(wrapper.text()).toContain('回扫已完成')
    expect(wrapper.text()).toContain('年度贡献')
  })

  test('a failed endpoint still renders a visible hint instead of a silent gap', async () => {
    const wrapper = await mountRow({ summary: null, failed: true }, tZh)
    expect(wrapper.text()).toContain('回扫进度暂时无法读取，稍后重试。')
  })

  test('the refresh button is keyboard-labelled and refreshes only this row', async () => {
    const summary = summarizeBackfillRuns([run({ status: 'running', lastUserId: 12 })], NOW)
    const wrapper = await mountRow({ summary, failed: false }, tZh)
    const button = wrapper.find('button')
    expect(button.attributes('aria-label')).toBe('刷新回扫进度')
    expect(button.attributes('title')).toBe('刷新回扫进度')
    await button.trigger('click')
    expect(wrapper.emitted('refresh')).toHaveLength(1)
  })
})
