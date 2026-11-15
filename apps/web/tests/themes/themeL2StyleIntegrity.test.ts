import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import {
  PUBLIC_FRONTEND_DOCUMENT_POLICY_SCHEMA_VERSION,
  PUBLIC_FRONTEND_POLICY_SCHEMA_VERSION,
  parsePublicFrontendPolicy
} from '../../app/runtime/public-extensions/pagePolicy'
import { buildPublicL2StyleLinks } from '../../app/runtime/public-extensions/l2StyleLinks'

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')

const PACKAGE_DIGEST = 'a1'.repeat(32)
const IMPACT_DIGEST = 'b2'.repeat(32)
const GRAPH_DIGEST = 'c3'.repeat(32)
const EXTENSION_POLICY_DIGEST = 'd4'.repeat(32)
const DOCUMENT_POLICY_DIGEST = 'e5'.repeat(32)
const EXTENSION_ID = 'demo.theme'
const HANDLE = 'demo.theme.component.card.style'
const STYLE_DIGEST = '3f'.repeat(32)

// 独立口径：SRI 摘要 = base64(sha256 原始字节)。不 import 生产实现，避免自证。
function expectedIntegrity(digest: string) {
  return `sha256-${Buffer.from(digest, 'hex').toString('base64')}`
}

function styleAsset(digest: string, overrides: Record<string, unknown> = {}) {
  return {
    handle: HANDLE,
    contractVersion: `${HANDLE}@1`,
    extensionId: EXTENSION_ID,
    packageDigest: PACKAGE_DIGEST,
    impactDigest: IMPACT_DIGEST,
    type: 'style',
    digest,
    integrity: expectedIntegrity(digest),
    dependencies: [],
    scope: ['demo.theme.component.card'],
    module: false,
    loading: 'blocking',
    csp: [],
    assetPath: `/_sforum/assets/extensions/${EXTENSION_ID}/${PACKAGE_DIGEST}/frontend/public/card.css`,
    ...overrides
  }
}

// Host /pages/policy 的真实响应形状：SSR 与客户端换页解析的是同一份 payload。
function hostPolicyPayload(styles: unknown[]) {
  return {
    schemaVersion: PUBLIC_FRONTEND_POLICY_SCHEMA_VERSION,
    graphDigest: GRAPH_DIGEST,
    extensionPolicyDigest: EXTENSION_POLICY_DIGEST,
    directives: [],
    admittedComponents: [],
    documentPolicy: {
      schemaVersion: PUBLIC_FRONTEND_DOCUMENT_POLICY_SCHEMA_VERSION,
      digest: DOCUMENT_POLICY_DIGEST,
      directives: [{ name: 'style-src', sources: ["'self'"] }],
      headerValue: "default-src 'none'; style-src 'self'"
    },
    styles
  }
}

describe('theme L2 style link integrity', () => {
  test('SSR 预注入的样式 link 带上 digest 绑定的 integrity', () => {
    const policy = parsePublicFrontendPolicy(hostPolicyPayload([styleAsset(STYLE_DIGEST)]))
    expect(policy.styles).toHaveLength(1)

    const links = buildPublicL2StyleLinks(policy.styles)
    expect(links).toHaveLength(1)
    expect(links[0].rel).toBe('stylesheet')
    expect(links[0].href).toBe(styleAsset(STYLE_DIGEST).assetPath)
    expect(links[0].integrity).toBe(expectedIntegrity(STYLE_DIGEST))
    expect(links[0].integrity).toMatch(/^sha256-[A-Za-z0-9+/]+={0,2}$/)
    // 既有的属性绑定契约不变，只是多了一道字节校验。
    expect(links[0]['data-sforum-extension']).toBe(EXTENSION_ID)
    expect(links[0]['data-sforum-asset']).toBe(HANDLE)
    expect(links[0]['data-sforum-digest']).toBe(STYLE_DIGEST)
  })

  test('digest 变化时 integrity 随之变化，不是常量', () => {
    const first = '11'.repeat(32)
    const second = '22'.repeat(32)
    const firstLinks = buildPublicL2StyleLinks(
      parsePublicFrontendPolicy(hostPolicyPayload([styleAsset(first)])).styles
    )
    const secondLinks = buildPublicL2StyleLinks(
      parsePublicFrontendPolicy(hostPolicyPayload([styleAsset(second)])).styles
    )

    expect(firstLinks[0].integrity).toBe(expectedIntegrity(first))
    expect(secondLinks[0].integrity).toBe(expectedIntegrity(second))
    expect(firstLinks[0].integrity).not.toBe(secondLinks[0].integrity)
  })

  test('SSR 与客户端换页产出同一 integrity（同一解析来源）', () => {
    const payload = hostPolicyPayload([styleAsset(STYLE_DIGEST)])
    // SSR：import.meta.server 直接 apply 的 policy；客户端：fetch 后同一 parse。
    const serverPolicy = parsePublicFrontendPolicy(payload)
    const clientPolicy = parsePublicFrontendPolicy(JSON.parse(JSON.stringify(payload)))

    const serverLink = buildPublicL2StyleLinks(serverPolicy.styles)[0]
    const clientLink = buildPublicL2StyleLinks(clientPolicy.styles)[0]

    expect(serverPolicy.styles[0].integrity).toBe(clientPolicy.styles[0].integrity)
    expect(serverLink.integrity).toBe(clientLink.integrity)
    expect(serverLink.integrity).toBe(expectedIntegrity(STYLE_DIGEST))
  })

  test('integrity 与 digest 对不上的样式不会被预注入', () => {
    const policy = parsePublicFrontendPolicy(hostPolicyPayload([
      styleAsset(STYLE_DIGEST, { integrity: expectedIntegrity('ff'.repeat(32)) })
    ]))

    expect(policy.styles).toHaveLength(0)
    expect(buildPublicL2StyleLinks(policy.styles)).toEqual([])
  })

  test('主题模板与客户端加载读的是同一个 integrity 来源', () => {
    const template = source('../../app/components/SFThemeTemplate.vue')
    // SSR 预注入不再自建属性表，改由共享构造器产出（含 integrity）。
    expect(template).toContain("import { buildPublicL2StyleLinks } from '~/runtime/public-extensions/l2StyleLinks'")
    expect(template).toContain('return buildPublicL2StyleLinks(styles)')
    expect(template).not.toContain("'data-sforum-digest': style.digest")
    expect(template).toContain('useHead(() => ({ link: l2StyleLinks.value }))')

    // 客户端注入路径读取同一字段：PublicFrontendAssetReference.integrity。
    expect(source('../../app/runtime/public-extensions/assets.ts'))
      .toContain('element.integrity = reference.integrity')
    expect(source('../../app/composables/extensions/usePublicContentStyles.ts'))
      .toContain('integrity: style.integrity')

    // 该字段在解析期被断言等于 digestIntegrity(digest)，所以不存在第二个摘要来源。
    expect(source('../../app/runtime/public-extensions/types.ts'))
      .toContain('input.integrity !== digestIntegrity(input.digest)')
    expect(source('../../app/runtime/public-extensions/l2StyleLinks.ts'))
      .toContain('integrity: style.integrity')
  })
})
