import { describe, expect, test } from 'bun:test'
import { readFileSync, readdirSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import {
  collectProfileWidgetRefs
} from '../../app/utils/profile/profileExtensionTab'
import { publicPagePolicyPath } from '../../app/runtime/public-extensions/pagePolicy'
import { forumAvatarToneClass } from '../../app/utils/forum/forumListPresentation'
import type { ProfileExtensionTab, PublicProfile } from '../../app/composables/profile/useProfileApi'

/**
 * 资料页 widget tab 的 L2 refs 必须进入页面文档 CSP 的单点聚合链路。
 *
 * 注意：缺聚合**不会**让脚本被拦（script-src 'self' 放行同源原生 import()）。
 * 真实代价是资料页此前根本没有 CSP 头，聚合缺失意味着：没有 digest 绑定的样式预注入
 * （首帧样式闪变）、没有 X-SForum-Document-Policy-Digest 证据。反过来，一旦写上 CSP，
 * 资料页就从「无约束」变成 img-src 'self' 的严格约束，所以头像路径必须同时验证。
 *
 * 纯函数部分可执行验证；链路接线是 SFC 模板里的单点聚合条件，只能静态钉住——
 * 一旦有人把 extraL2Refs 漏掉，功能会在 dev 下正常、上线白做。
 */

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')

const appRoot = fileURLToPath(new URL('../../app', import.meta.url))

function listAppSourceFiles(dir = appRoot): string[] {
  const files: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      files.push(...listAppSourceFiles(path))
    } else if (/\.(ts|vue)$/.test(entry.name)) {
      files.push(path)
    }
  }
  return files
}

const profileWithTabs = (extensionTabs: ProfileExtensionTab[]) => ({ extensionTabs } as PublicProfile)

const widget = (
  extensionId: string,
  id: string,
  componentId: string | undefined,
  order = 10
): ProfileExtensionTab => ({
  extensionId,
  id,
  order,
  kind: 'widget',
  componentId,
  url: ''
})

describe('profile widget refs for the page document policy', () => {
  test('collects only mountable widget tabs, deduped and sorted', () => {
    const refs = collectProfileWidgetRefs(profileWithTabs([
      widget('demo', 'portfolio', 'demo.portfolio'),
      widget('demo', 'guestbook', 'demo.guestbook'),
      // 同一组件被两条贡献引用：CSP 只需要一条
      widget('demo', 'portfolio-again', 'demo.portfolio'),
      // 非 widget 贡献是跳转入口，不产生 L2 挂载
      { extensionId: 'demo', id: 'docs', order: 1, kind: 'hostLink', url: '/docs' },
      { extensionId: 'demo', id: 'route', order: 2, kind: 'extensionRoute', url: '/extensions/demo/x' },
      // 缺 componentId 的 widget 挂不上，也不该进策略
      widget('demo', 'broken', undefined)
    ]))

    expect(refs).toEqual([
      { extensionId: 'demo', componentId: 'demo.guestbook' },
      { extensionId: 'demo', componentId: 'demo.portfolio' }
    ])
  })

  test('drops invalid refs instead of throwing, keeping the valid siblings', () => {
    const refs = collectProfileWidgetRefs(profileWithTabs([
      widget('demo', 'portfolio', 'demo.portfolio'),
      // 非法 ID：路径穿越、空格、大写、单字符（normalize 的 ID_PATTERN 全部拒绝）
      widget('demo', 'traversal', '../evil'),
      widget('demo', 'space', 'demo widget'),
      widget('demo', 'uppercase', 'Demo.Widget'),
      widget('demo', 'short', 'a'),
      widget('Bad Ext', 'bad-extension', 'demo.bad-extension')
    ]))

    expect(refs).toEqual([{ extensionId: 'demo', componentId: 'demo.portfolio' }])
    expect(() => collectProfileWidgetRefs(profileWithTabs([widget('demo', 'x', '../../x')]))).not.toThrow()
    expect(collectProfileWidgetRefs(profileWithTabs([widget('demo', 'x', '../../x')]))).toEqual([])
    expect(collectProfileWidgetRefs(null)).toEqual([])
    expect(collectProfileWidgetRefs(profileWithTabs([]))).toEqual([])
  })

  test('a dropped ref is silent outside dev and carries ids inside dev', () => {
    const util = source('../../app/utils/profile/profileExtensionTab.ts')

    // dev 分支带 extensionId/componentId，生产由 import.meta.dev 静态折叠掉。
    expect(util).toContain('if (import.meta.dev) {')
    expect(util).toContain("console.warn('[sforum] profile widget ref dropped'")
    expect(util).toContain('extensionId: tab.extensionId')
    expect(util).toContain('componentId: tab.componentId')

    // bun test 下 import.meta.dev 为假，等价生产路径：丢弃但不打日志。
    const warnings: unknown[][] = []
    const original = console.warn
    console.warn = (...args: unknown[]) => {
      warnings.push(args)
    }
    try {
      expect(collectProfileWidgetRefs(profileWithTabs([widget('demo', 'x', '../x')]))).toEqual([])
    } finally {
      console.warn = original
    }
    expect(warnings).toEqual([])
  })

  test('refs reach the page-policy request as component=extensionId/componentId', () => {
    const path = publicPagePolicyPath(collectProfileWidgetRefs(profileWithTabs([
      widget('demo', 'portfolio', 'demo.portfolio'),
      widget('other', 'panel', 'other.panel')
    ])))

    expect(path.startsWith('/extensions/runtime/page-policy?')).toBe(true)
    expect(path).toContain('component=demo%2Fdemo.portfolio')
    expect(path).toContain('component=other%2Fother.panel')
    expect(decodeURIComponent(path)).toContain('component=demo/demo.portfolio')
  })

  test('no widget tab means no refs and no policy query', () => {
    // 第 3 条验收：没有 widget tab 的资料页保持零开销（不聚合、不请求 policy）。
    const refs = collectProfileWidgetRefs(profileWithTabs([
      { extensionId: 'demo', id: 'docs', order: 1, kind: 'hostLink', url: '/docs' }
    ]))
    expect(refs).toEqual([])
    expect(publicPagePolicyPath(refs)).toBe('/extensions/runtime/page-policy')
  })
})

describe('profile page CSP wiring', () => {
  test('route shell hands page-level refs to the outlet', () => {
    const shell = source('../../app/pages/u/[username].vue')

    expect(shell).toContain('collectProfileWidgetRefs(profile.value)')
    expect(shell).toContain(':extra-l2-refs="extraL2Refs"')
  })

  test('profile is fetched exactly once: shell creates, island injects', () => {
    const shell = source('../../app/pages/u/[username].vue')
    const page = source('../../app/components/profile/SFProfileShowPage.vue')
    const composable = source('../../app/composables/profile/useProfileShowData.ts')

    // 唯一取数点：composable 里只有一处 useAsyncData，用的还是默认 options。
    expect(composable.match(/useAsyncData\(/g)).toHaveLength(1)
    expect(composable).toContain('() => `forum-profile-${username.value}`')
    expect(composable).toContain('profileApi.getPublicProfile(username.value)')
    // 红线：不给创建者塞自定义 getCachedData——那会让 hasCustomGetCachedData 为真、关掉 payload purge。
    expect(composable).not.toContain('getCachedData:')
    expect(composable).not.toContain('dedupe:')
    // 壳创建并 provide，岛 inject 同一份 refs。
    expect(composable).toContain('provide(PROFILE_SHOW_DATA, data)')
    expect(composable).toContain('inject(PROFILE_SHOW_DATA, null)')

    // 两边都只调 composable，谁都不再各自对这个 key 调 useAsyncData（否则 dev 会打
    // Incompatible options 警告、服务端还会因为 payloadExtraction 关闭而重复请求）。
    expect(shell).toContain('await useProfileShowData(username)')
    expect(page).toContain('await useProfileShowData(username)')
    expect(page).not.toContain('() => `forum-profile-${username.value}`')
    expect(shell).toContain(':extra-l2-refs="extraL2Refs"')
  })

  test('exactly one call site owns the profile asyncData key', () => {
    // Nuxt 的 dev 警告只在「同 key 的第二次 useAsyncData 看到已存在的 _asyncData」时触发
    // （asyncData.js:59-80，比较 handler / getCachedData / default 的哈希）。
    // app/ 目录里只有 composable 一处声明这个 key（server/ 侧也没有），第二次调用不可能存在
    // → 警告结构性消失，而且创建者用的是默认 options，payload purge 保持开启。
    const owners = listAppSourceFiles()
      .filter(file => readFileSync(file, 'utf8').includes('`forum-profile-${username.value}`'))
      .map(file => relative(appRoot, file).replaceAll('\\', '/'))

    expect(owners).toEqual(['composables/profile/useProfileShowData.ts'])
  })

  test('outlet forwards page-level refs into the single aggregation', () => {
    const outlet = source('../../app/components/SFPageOutlet.vue')
    const resolver = source('../../app/components/SFPageOutletResolver.vue')
    const render = source('../../app/components/SFPageOutletRender.vue')

    expect(outlet).toContain('extraL2Refs?: PublicFrontendComponentRef[]')
    expect(outlet).toContain(':extra-l2-refs="extraL2Refs"')
    expect(resolver).toContain('extraL2Refs?: PublicFrontendComponentRef[]')
    expect(resolver).toContain('normalizePublicFrontendComponentRefs([...regionRefs, ...extraRefs])')
    // 只有页面级 widget、没有区域 widget 时也必须写 header
    expect(resolver).toContain('regionWidgetRefs.value.length > 0 && !willUseThemeTemplate.value')
    expect(resolver).toContain(':region-widget-refs="regionWidgetRefs"')
    expect(render).toContain(':extra-l2-refs="regionWidgetRefs"')
  })

  test('writes the document policy header exactly once per response', () => {
    const resolver = source('../../app/components/SFPageOutletResolver.vue')
    const render = source('../../app/components/SFPageOutletRender.vue')
    const themeTemplate = source('../../app/components/SFThemeTemplate.vue')
    const outlet = source('../../app/components/SFPageOutlet.vue')
    const policy = source('../../app/composables/errors/usePublicPageDocumentPolicy.ts')

    // 两条路径互斥：resolver 只在「不会走主题模板」时写头，主题模板是另一条唯一调用点。
    expect(resolver.match(/applyPublicPageDocumentPolicy\(/g)).toHaveLength(1)
    expect(themeTemplate.match(/applyPublicPageDocumentPolicy\(/g)).toHaveLength(1)
    // 中间层不得各自聚合（否则会二次 setHeader 覆盖）
    expect(outlet).not.toContain('applyPublicPageDocumentPolicy')
    expect(render).not.toContain('applyPublicPageDocumentPolicy')
    // digest 证据头与 CSP 头同点写入
    expect(policy).toContain('PUBLIC_PAGE_CSP_HEADER')
    expect(policy).toContain("'X-SForum-Document-Policy-Digest'")
    // 空 refs 时不聚合（性能保护），避免无 widget 页面白打一次 policy 请求
    expect(resolver).toContain('if (!extraRefs.length)')
  })
})

describe('avatar rendering on a CSP-constrained profile page', () => {
  const bindings: Record<string, unknown> = {
    computed: testVue.computed,
    ref: testVue.ref,
    watch: testVue.watch,
    forumAvatarToneClass
  }
  const avatarPath = fileURLToPath(new URL('../../app/components/SFAvatar.vue', import.meta.url))

  const NuxtImgStub = testVue.defineComponent({
    props: { src: { type: String, default: '' } },
    emits: ['error'],
    setup(props, { emit }) {
      return () => testVue.h('img', {
        src: props.src,
        'data-nuxt-img': '',
        onError: () => emit('error')
      })
    }
  })

  async function mountAvatar(avatar: { kind: string, url: string, alt: string }) {
    const Avatar = await compileVueSfc(avatarPath, `sf-avatar-${avatar.kind}`, bindings)
    return mount(Avatar, {
      props: { name: 'Alice', avatar },
      global: { components: { NuxtImg: NuxtImgStub } }
    })
  }

  test('initials avatar renders no image at all, so img-src cannot block it', async () => {
    const wrapper = await mountAvatar({ kind: 'initials', url: '', alt: 'Alice' })
    await flushPromises()

    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.text()).toContain('A')
    wrapper.unmount()
  })

  test('uploaded avatar uses the same-origin media URL allowed by img-src self', async () => {
    const wrapper = await mountAvatar({ kind: 'uploaded', url: '/media/avatars/pub-1', alt: 'Alice' })
    await flushPromises()

    expect(wrapper.find('img').attributes('src')).toBe('/media/avatars/pub-1')
    wrapper.unmount()
  })

  test('remote (gravatar) avatar is the documented CSP trade-off and degrades to initials', async () => {
    const wrapper = await mountAvatar({
      kind: 'gravatar',
      url: 'https://gravatar.com/avatar/abc123',
      alt: 'Alice'
    })
    await flushPromises()

    // 远程地址会渲染成 <img>，在 img-src 'self' 下被拦 → error → 首字母兜底。
    const img = wrapper.find('img')
    expect(img.attributes('src')).toBe('https://gravatar.com/avatar/abc123')
    await img.trigger('error')
    await flushPromises()

    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.text()).toContain('A')
    wrapper.unmount()
  })

  test('static provider URL is the same trade-off, not a gravatar-only problem', async () => {
    // KindStatic 是运营填的任意 URL（view.go:104），默认值来自 avatar.default_static_url。
    const wrapper = await mountAvatar({
      kind: 'static',
      url: 'https://cdn.example.com/default-avatar.png',
      alt: 'Alice'
    })
    await flushPromises()

    expect(wrapper.find('img').attributes('src')).toBe('https://cdn.example.com/default-avatar.png')
    await wrapper.find('img').trigger('error')
    await flushPromises()

    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.text()).toContain('A')
    wrapper.unmount()
  })

  test('uploaded avatar on an external CDN degrades, same-origin media does not', async () => {
    // 防御性覆盖：`avatar.Source.Attachment.URL` 目前没有任何写入方——四个构造点都不填、
    // `GetAvatarAttachment` 的 SELECT 也不取 URL，所以 uploaded 恒走同源 `/media/avatars/…`。
    // 但 `view.go:88` 会原样采用该字段，将来接入远程存储的绝对地址就会被 img-src 'self' 挡。
    const remote = await mountAvatar({
      kind: 'uploaded',
      url: 'https://cdn.example.com/avatars/pub-1.webp',
      alt: 'Alice'
    })
    await flushPromises()

    expect(remote.find('img').attributes('src')).toBe('https://cdn.example.com/avatars/pub-1.webp')
    await remote.find('img').trigger('error')
    await flushPromises()
    expect(remote.find('img').exists()).toBe(false)
    remote.unmount()
  })
})
