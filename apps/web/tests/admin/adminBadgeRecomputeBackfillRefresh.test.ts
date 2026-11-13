import { describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { computed, ref, watch } from 'vue'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import {
  recomputeToastDescription,
  shouldRefreshBackfillAfterRecompute,
  summarizeBackfillRuns
} from '../../app/utils/admin/adminBadges'

/**
 * GAP-4：`recompute` 之后要不要重拉回扫进度行。
 *
 * 之前的实现只有代码路径、没有断言调用次数——「0 不刷」这条最容易被后来的重构
 * 悄悄改掉（多打一次请求没人发现，少打一次进度行就永远停在旧值）。所以两处都钉：
 * 1. 纯函数真值表；
 * 2. 页壳挂载：`queued>0` 刷新**恰一次**，`0` 与字段缺失**零次**。
 */
describe('shouldRefreshBackfillAfterRecompute', () => {
  test('only a real queued badge justifies another request', () => {
    expect(shouldRefreshBackfillAfterRecompute({ queued: 1 })).toBe(true)
    expect(shouldRefreshBackfillAfterRecompute({ queued: 3 })).toBe(true)
    expect(shouldRefreshBackfillAfterRecompute({ queued: 0 })).toBe(false)
  })

  test('a missing or null queued field is "nothing queued", not "unknown but try"', () => {
    // 全 0 的既有语义是「不编副标题」（recomputeToastDescription 返回 undefined），
    // 这里必须同一口径：字段缺失 = 0，不打空请求。
    expect(shouldRefreshBackfillAfterRecompute({})).toBe(false)
    expect(shouldRefreshBackfillAfterRecompute({ queued: null })).toBe(false)
    expect(shouldRefreshBackfillAfterRecompute(undefined)).toBe(false)
    expect(shouldRefreshBackfillAfterRecompute(null)).toBe(false)
    expect(recomputeToastDescription({ granted: 0, refreshed: 0, queued: 0 }, key => key)).toBeUndefined()
  })

  test('negative or NaN queued values never trigger a refresh', () => {
    expect(shouldRefreshBackfillAfterRecompute({ queued: -1 })).toBe(false)
    expect(shouldRefreshBackfillAfterRecompute({ queued: Number.NaN })).toBe(false)
  })
})

// ---- 页壳挂载：断言 loader 的真实调用次数 ----

const i18nStub = () => ({ t: (key: string) => key })

type RecomputeResult = { granted: number, refreshed: number, queued?: number }

/** 用最小的替身盯住 `useAdminBadgeBackfillStatus().load` 的调用次数。 */
function createShellHarness(result: RecomputeResult) {
  const backfillLoad = { calls: 0 }
  const toastCalls: unknown[] = []

  const store = {
    blocked: ref(''),
    failed: ref(false),
    pending: ref(false),
    badges: ref([]),
    groups: ref([]),
    tiers: ref([]),
    colorChoices: ref([]),
    load: async () => {},
    recompute: async () => result
  }

  const bindings = {
    ref,
    computed,
    watch,
    onMounted: testVue.onMounted,
    definePageMeta: () => {},
    useSeoMeta: () => {},
    useRoute: () => ({ query: {} }),
    useRouter: () => ({ replace: async () => {} }),
    useI18n: i18nStub,
    useToast: () => ({ add: (payload: unknown) => { toastCalls.push(payload) } }),
    useAuthSession: () => ({ can: () => true }),
    useAdminPage: () => ({ icon: 'i-lucide-medal' }),
    useAdminBadges: () => store,
    useAdminBadgeConditions: () => ({
      catalog: ref([]),
      factCandidates: ref([]),
      factCandidatesFailed: ref(false),
      refCandidates: ref({}),
      refCandidatesIncomplete: ref(false),
      failed: ref(false),
      load: async () => {}
    }),
    useAdminBadgeRuleHealth: () => ({ items: ref([]), failed: ref(false), load: async () => {} }),
    useAdminBadgeBackfillStatus: () => ({
      runs: ref([]),
      failed: ref(false),
      pending: ref(false),
      load: async () => { backfillLoad.calls += 1 }
    }),
    recomputeToastDescription,
    shouldRefreshBackfillAfterRecompute,
    summarizeBackfillRuns,
    SFAdminFixedTabNav: { props: ['items', 'modelValue', 'ariaLabel'], template: '<div />' },
    SFAdminBadgesBadgesTab: { template: '<div />' },
    SFAdminBadgesGroupsTab: { template: '<div />' },
    SFAdminBadgesTiersTab: { template: '<div />' },
    SFAdminBadgesGrantsTab: { template: '<div />' }
  }

  return { bindings, backfillLoad, toastCalls }
}

const shellPath = fileURLToPath(new URL('../../app/pages/admin/badges.vue', import.meta.url))

async function mountShell(result: RecomputeResult) {
  const harness = createShellHarness(result)
  const Shell = await compileVueSfc(shellPath, `shell-${JSON.stringify(result)}`, harness.bindings)
  const wrapper = mount(Shell, {
    global: {
      stubs: {
        UButton: {
          props: ['ariaLabel', 'title', 'icon', 'loading', 'color', 'variant'],
          emits: ['click'],
          template: '<button :aria-label="ariaLabel" :title="title" @click="$emit(\'click\')"><slot /></button>'
        },
        UDashboardToolbar: { template: '<div><slot name="left" /><slot name="right" /></div>' },
        UAlert: { props: ['title', 'description', 'color', 'variant', 'icon'], template: '<div />' },
        UCard: { template: '<div><slot /></div>' },
        UIcon: { props: ['name'], template: '<i />' }
      }
    }
  })
  return { wrapper, ...harness }
}

async function clickRecompute(wrapper: Awaited<ReturnType<typeof mountShell>>['wrapper']) {
  const button = wrapper.find('button[aria-label="admin.badges.recompute.action"]')
  expect(button.exists()).toBe(true)
  // 两步确认：第一次只进确认态，第二次才真的发 POST + 走刷新分支。
  await button.trigger('click')
  await wrapper.find('button[aria-label="admin.badges.recompute.confirm"]').trigger('click')
  await flushPromises()
}

describe('badges shell · recompute refreshes the backfill row at most once', () => {
  // 页壳 mount 时会 onMounted 首拉一次；断言的是**重算带来的增量**，不是总数。
  test('queued > 0 refreshes exactly once', async () => {
    const { wrapper, backfillLoad } = await mountShell({ granted: 0, refreshed: 0, queued: 2 })
    const before = backfillLoad.calls
    await clickRecompute(wrapper)
    expect(backfillLoad.calls - before).toBe(1)
    wrapper.unmount()
  })

  test('queued == 0 makes zero extra requests', async () => {
    const { wrapper, backfillLoad } = await mountShell({ granted: 4, refreshed: 2, queued: 0 })
    const before = backfillLoad.calls
    await clickRecompute(wrapper)
    expect(backfillLoad.calls - before).toBe(0)
    wrapper.unmount()
  })

  test('a response without the queued field (old backend) also makes zero', async () => {
    const { wrapper, backfillLoad } = await mountShell({ granted: 1, refreshed: 0 })
    const before = backfillLoad.calls
    await clickRecompute(wrapper)
    expect(backfillLoad.calls - before).toBe(0)
    wrapper.unmount()
  })

  test('a failed recompute never touches the backfill row', async () => {
    const harness = createShellHarness({ granted: 0, refreshed: 0, queued: 9 })
    const store = harness.bindings.useAdminBadges()
    store.recompute = async () => { throw new Error('boom') }
    const Shell = await compileVueSfc(shellPath, 'shell-recompute-failure', harness.bindings)
    const wrapper = mount(Shell, {
      global: {
        stubs: {
          UButton: {
            props: ['ariaLabel', 'title', 'icon', 'loading', 'color', 'variant'],
            emits: ['click'],
            template: '<button :aria-label="ariaLabel" @click="$emit(\'click\')"><slot /></button>'
          },
          UDashboardToolbar: { template: '<div><slot name="left" /><slot name="right" /></div>' },
          UAlert: { props: ['title', 'description', 'color', 'variant', 'icon'], template: '<div />' },
          UCard: { template: '<div><slot /></div>' },
          UIcon: { props: ['name'], template: '<i />' }
        }
      }
    })
    const before = harness.backfillLoad.calls
    await clickRecompute(wrapper)
    expect(harness.backfillLoad.calls - before).toBe(0)
    wrapper.unmount()
  })
})
