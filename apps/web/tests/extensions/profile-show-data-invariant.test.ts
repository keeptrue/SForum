import { afterEach, beforeEach, describe, expect, test } from 'bun:test'
import { createSSRApp, defineComponent, h, ref, type Ref } from 'vue'
import { renderToString } from 'vue/server-renderer'

import {
  PROFILE_SHOW_DATA,
  useProfileShowData,
  type ProfileShowData
} from '../../app/composables/profile/useProfileShowData'

/**
 * `useProfileShowData` 的不变量：路由壳创建并 provide 的占位容器，一定在主题岛
 * inject 之前被填成真实 refs。
 *
 * 这条不变量此前零执行覆盖——组件测试把 `useProfileShowData` 整个 stub 掉了，
 * 只剩「岛一定在壳 await 之后渲染」的 Suspense 契约口头保证。这里用真实 Vue SSR
 * （`renderToString` 会先等 async setup 与其 serverPrefetch 再渲染子树）跑一遍：
 * 壳 async setup 里 provide 占位 → await 取数 → 替换容器字段 → 子组件 setup 里
 * inject 并立刻解构，读到的必须是填好的值。
 *
 * 不依赖 Nuxt 运行时：composable 里 `useAsyncData` / `useApiClient` 是 Nuxt 自动导入的
 * 自由标识符，这里用 globalThis 注入最小替身。
 */

type FetchCall = { path: string }

let fetchCalls: FetchCall[] = []
const globals = globalThis as Record<string, unknown>
const originalUseAsyncData = globals.useAsyncData
const originalUseApiClient = globals.useApiClient

function profileFixture(username: string) {
  return {
    userId: 7,
    username,
    displayName: username.toUpperCase(),
    profile: {
      userId: 7,
      bio: '',
      signature: '',
      location: '',
      websiteUrl: '',
      avatar: { kind: 'initials', url: '', alt: username },
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z'
    },
    topicCount: 0,
    commentCount: 0,
    recentTopics: [],
    activities: [],
    joinedAt: '2026-01-01T00:00:00Z'
  }
}

beforeEach(() => {
  fetchCalls = []
  globals.useApiClient = () => ({
    apiBaseUrl: 'http://localhost',
    request: async (path: string) => {
      fetchCalls.push({ path })
      const username = decodeURIComponent(path.split('/').filter(Boolean).pop() || '')
      return profileFixture(username)
    }
  })
  // 模拟真实取数：结果一定在 await 之后才可用，占位容器必须在填充后才被子组件读到。
  globals.useAsyncData = async (_key: unknown, handler: () => Promise<unknown>) => {
    await new Promise(resolve => setTimeout(resolve, 0))
    const value = await handler()
    return { data: ref(value), pending: ref(false), error: ref(undefined) }
  }
})

afterEach(() => {
  globals.useAsyncData = originalUseAsyncData
  globals.useApiClient = originalUseApiClient
})

function childComponent(captured: ProfileShowData[]) {
  return defineComponent({
    name: 'ProfileProbeChild',
    async setup() {
      const data = await useProfileShowData(ref('alice'))
      captured.push(data)
      // 立刻解构：岛就是这么用的，容器没填好就会读到 null 占位。
      const { profile, pending } = data
      return () => h('span', { 'data-testid': 'child' }, `child:${profile.value?.username ?? 'EMPTY'}:${pending.value}`)
    }
  })
}

describe('useProfileShowData provide/inject invariant', () => {
  test('the shell fills the provided container before the island injects it', async () => {
    const childCaptures: ProfileShowData[] = []
    const shellProfiles: Ref<unknown>[] = []
    const Child = childComponent(childCaptures)

    const Shell = defineComponent({
      name: 'ProfileProbeShell',
      async setup() {
        const { profile } = await useProfileShowData(ref('alice'))
        shellProfiles.push(profile)
        return () => h('div', [
          h('span', { 'data-testid': 'shell' }, `shell:${profile.value?.username ?? 'EMPTY'}`),
          h(Child)
        ])
      }
    })

    const html = await renderToString(createSSRApp(Shell))

    expect(html).toContain('shell:alice')
    expect(html).toContain('child:alice:false')
    // 子组件拿到的是壳填进去的那一份 ref，而不是 provide 时的空占位。
    expect(childCaptures).toHaveLength(1)
    expect(childCaptures[0]!.profile).toBe(shellProfiles[0]!)
    // 单一取数点：inject 路径不再发请求。
    expect(fetchCalls).toHaveLength(1)
    expect(fetchCalls[0]!.path).toBe('/profiles/alice')
  })

  test('without a provider the same composable creates the data itself', async () => {
    const childCaptures: ProfileShowData[] = []
    const Child = childComponent(childCaptures)

    const html = await renderToString(createSSRApp(Child))

    expect(html).toContain('child:alice:false')
    expect(fetchCalls).toHaveLength(1)
    expect(childCaptures[0]!.profile.value?.username).toBe('alice')
  })
})
