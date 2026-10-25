import { afterEach, describe, expect, it } from 'bun:test'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { Window } from 'happy-dom'

import {
  loadPublicFrontendRelease,
  resetPublicAssetRuntimeForTest,
  sameOriginAssetURL,
  verifyPublicAssetBytes
} from '../../app/runtime/public-extensions/assets'
import {
  PUBLIC_FRONTEND_API_VERSION,
  PUBLIC_FRONTEND_SCHEMA_VERSION,
  PUBLIC_FRONTEND_TRUST_NOTICE,
  PublicFrontendContractError,
  parsePublicFrontendDescriptor,
  publicFrontendExactHeaders,
  publicFrontendRequestOptions,
  publicExtensionRequestPath,
  type PublicFrontendAssetReference,
  type PublicFrontendComponentDescriptor
} from '../../app/runtime/public-extensions/types'

afterEach(() => resetPublicAssetRuntimeForTest())

describe('public extension runtime contract', () => {
  it('accepts only exact Host-issued descriptor paths', () => {
    const descriptor = descriptorFixture()
    expect(parsePublicFrontendDescriptor(descriptor, descriptor.extensionId, descriptor.componentId)).toEqual(descriptor)

    expect(() => parsePublicFrontendDescriptor({ ...descriptor, trustNotice: 'sandboxed' }, descriptor.extensionId, descriptor.componentId))
      .toThrow(PublicFrontendContractError)
    expect(() => parsePublicFrontendDescriptor({
      ...descriptor,
      entry: { ...descriptor.entry, assetPath: 'https://evil.example/entry.mjs' }
    }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
    expect(() => parsePublicFrontendDescriptor({
      ...descriptor,
      entry: {
        ...descriptor.entry,
        assetPath: `/_sforum/assets/extensions/demo.public/${descriptor.packageDigest}/%2e%2e/entry.mjs`
      }
    }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
    expect(() => parsePublicFrontendDescriptor({
      ...descriptor,
      entry: { ...descriptor.entry, assetPath: descriptor.entry.assetPath.replace(/\.mjs$/, '.css') }
    }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
    expect(() => parsePublicFrontendDescriptor(descriptor, 'other.plugin', descriptor.componentId))
      .toThrow(PublicFrontendContractError)
  })

  it('keeps bridge requests inside the extension route', () => {
    expect(publicExtensionRequestPath('demo.public', 'items?page=2')).toBe('/extensions/demo.public/items?page=2')
    expect(() => publicExtensionRequestPath('demo.public', '../admin')).toThrow(PublicFrontendContractError)
    expect(() => publicExtensionRequestPath('demo.public', 'https://evil.example')).toThrow(PublicFrontendContractError)
  })

  it('pins bridge requests to the exact Host descriptor and prevents header override', () => {
    const descriptor = descriptorFixture()
    const options = publicFrontendRequestOptions(descriptor, {
      method: 'POST',
      headers: {
        'X-Trace-ID': 'trace-1',
        'x-sforum-public-package-digest': 'forged'
      }
    })
    expect(options.headers).toEqual({
      'X-Trace-ID': 'trace-1',
      ...publicFrontendExactHeaders(descriptor)
    })
  })

  it('accepts same-owner and cross-owner dependency graphs', () => {
    const styleBytes = new TextEncoder().encode('.same-owner { display: block }').buffer
    const entryBytes = new TextEncoder().encode('export const apiVersion = 1').buffer
    const style = assetReference('demo.public.asset.same-owner', 'style', styleBytes)
    const entry = assetReference(
      'demo.public.component.card.l2.entry', 'script', entryBytes, true, [style.handle]
    )
    const sameOwner = descriptorFixture(entry, [style])
    expect(parsePublicFrontendDescriptor(sameOwner, sameOwner.extensionId, sameOwner.componentId)).toEqual(sameOwner)

    const { descriptor: crossOwner } = crossOwnerDescriptorFixture()
    expect(parsePublicFrontendDescriptor(crossOwner, crossOwner.extensionId, crossOwner.componentId)).toEqual(crossOwner)
  })

  it('rejects forged cross-owner identities and contract versions', () => {
    const { descriptor, style } = crossOwnerDescriptorFixture()
    const forgedOwner = 'forged.assets'
    const forgedOwnerPath = style.assetPath.replace('/shared.assets/', `/${forgedOwner}/`)
    const invalidReferences = [
      { ...style, extensionId: forgedOwner, assetPath: forgedOwnerPath },
      { ...style, extensionId: '' },
      { ...style, packageDigest: '' },
      { ...style, impactDigest: '' },
      { ...style, contractVersion: '' },
      { ...style, contractVersion: `${style.handle}@0` },
      { ...style, contractVersion: 'forged.assets.asset.style@1' }
    ]
    for (const invalidReference of invalidReferences) {
      expect(() => parsePublicFrontendDescriptor({
        ...descriptor,
        assets: [invalidReference, descriptor.assets[1]]
      }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
    }
    expect(() => parsePublicFrontendDescriptor({
      ...descriptor,
      extensionVersion: ''
    }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
    // coreVersion 可选但必须规范：空串或含空白都属于破损描述符。
    for (const coreVersion of ['', ' 3.0.16 ']) {
      expect(() => parsePublicFrontendDescriptor({
        ...descriptor,
        coreVersion
      }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
    }
    expect(parsePublicFrontendDescriptor({
      ...descriptor,
      coreVersion: '3.0.16'
    }, descriptor.extensionId, descriptor.componentId).coreVersion).toBe('3.0.16')
    expect(parsePublicFrontendDescriptor(descriptor, descriptor.extensionId, descriptor.componentId).coreVersion)
      .toBeUndefined()
  })

  it('rejects forged cross-owner artifact digests and immutable URLs', () => {
    const { descriptor, style } = crossOwnerDescriptorFixture()
    const forgedPackageDigest = 'e'.repeat(64)
    const invalidReferences = [
      {
        ...style,
        packageDigest: forgedPackageDigest,
        assetPath: style.assetPath.replace(style.packageDigest, forgedPackageDigest)
      },
      { ...style, impactDigest: 'e'.repeat(64) },
      { ...style, digest: 'e'.repeat(64) },
      { ...style, assetPath: style.assetPath.replace('/shared.assets/', '/forged.assets/') }
    ]
    for (const invalidReference of invalidReferences) {
      expect(() => parsePublicFrontendDescriptor({
        ...descriptor,
        assets: [invalidReference, descriptor.assets[1]]
      }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
    }
  })

  it('rejects undeclared, out-of-order, and cyclic dependency edges', () => {
    const { descriptor, style, sharedModule } = crossOwnerDescriptorFixture()
    expect(() => parsePublicFrontendDescriptor({
      ...descriptor,
      entry: { ...descriptor.entry, dependencies: [] }
    }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
    expect(() => parsePublicFrontendDescriptor({
      ...descriptor,
      assets: [sharedModule, style]
    }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
    expect(() => parsePublicFrontendDescriptor({
      ...descriptor,
      assets: [
        { ...style, dependencies: [sharedModule.handle] },
        sharedModule
      ]
    }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
  })

  it('keeps duplicate, asset-count, and CSP graph bounds fail-closed', () => {
    const descriptor = descriptorFixture()
    expect(() => parsePublicFrontendDescriptor({
      ...descriptor,
      assets: [descriptor.entry]
    }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
    expect(() => parsePublicFrontendDescriptor({
      ...descriptor,
      assets: Array.from({ length: 257 }, () => descriptor.entry)
    }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)

    const { descriptor: crossOwner, style } = crossOwnerDescriptorFixture()
    expect(() => parsePublicFrontendDescriptor({
      ...crossOwner,
      assets: [{ ...style, csp: ["style-src 'self'"] }, crossOwner.assets[1]]
    }, crossOwner.extensionId, crossOwner.componentId)).toThrow(PublicFrontendContractError)
  })

  it('rejects graph drift, classic scripts, wrong scope, and unreachable assets', () => {
    const styleBytes = new TextEncoder().encode('.graph { display: block }').buffer
    const entryBytes = new TextEncoder().encode('export const apiVersion = 1').buffer
    const styleAsset = assetReference('demo.public.asset.graph', 'style', styleBytes)
    const entry = assetReference('demo.public.component.card.l2.entry', 'script', entryBytes, true, [styleAsset.handle])
    const descriptor = descriptorFixture(entry, [styleAsset])
    const style = descriptor.assets[0]
    expect(style).toBeDefined()
    expect(() => parsePublicFrontendDescriptor({
      ...descriptor,
      assets: [{ ...style, impactDigest: 'c'.repeat(64) }]
    }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
    expect(() => parsePublicFrontendDescriptor({
      ...descriptor,
      assets: [{ ...style, type: 'script', module: false }]
    }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
    expect(() => parsePublicFrontendDescriptor({
      ...descriptor,
      assets: [{ ...style, scope: ['demo.public.component.other'] }]
    }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
    expect(() => parsePublicFrontendDescriptor({
      ...descriptor,
      entry: { ...descriptor.entry, dependencies: [] }
    }, descriptor.extensionId, descriptor.componentId)).toThrow(PublicFrontendContractError)
  })

  it('accepts only same-origin immutable runtime module URLs', () => {
    const descriptor = descriptorFixture()
    expect(sameOriginAssetURL('/api/v1', descriptor.entry.assetPath, 'https://forum.example'))
      .toBe(`https://forum.example${descriptor.entry.assetPath}`)
    expect(() => sameOriginAssetURL('https://cdn.example/api/v1', descriptor.entry.assetPath, 'https://forum.example'))
      .toThrow(PublicFrontendContractError)
  })

  it('never executes public extension code through blob or classic script URLs', () => {
    const source = readFileSync(new URL('../../app/runtime/public-extensions/assets.ts', import.meta.url), 'utf8')
    expect(source).not.toContain('createObjectURL')
    expect(source).not.toContain('blob:')
    expect(source).not.toContain('createElement(\'script\')')
  })

  it('verifies asset bytes against the descriptor digest', async () => {
    const bytes = new TextEncoder().encode('verified').buffer
    const reference = assetReference('demo.public.asset.verified', 'style', bytes)
    await expect(verifyPublicAssetBytes(reference, bytes)).resolves.toBeUndefined()
    await expect(verifyPublicAssetBytes(reference, new TextEncoder().encode('changed').buffer))
      .rejects.toBeInstanceOf(PublicFrontendContractError)
  })

  it('aborts a verified asset request at the runtime timeout boundary', async () => {
    const browser = new Window({ url: 'http://127.0.0.1/' })
    const descriptor = descriptorFixture()
    let aborted = false
    const fetcher = ((_input: RequestInfo | URL, init?: RequestInit) => new Promise<Response>((_resolve, reject) => {
      const fail = () => {
        aborted = true
        reject(new Error('aborted'))
      }
      if (init?.signal?.aborted) {
        fail()
        return
      }
      init?.signal?.addEventListener('abort', fail, { once: true })
    })) as typeof fetch

    await expect(loadPublicFrontendRelease(descriptor, '/api/v1', {
      fetch: fetcher,
      document: browser.document,
      origin: browser.location.origin,
      importModule: async () => ({ apiVersion: 1, mount: () => undefined }),
      timeoutMS: 1
    })).rejects.toThrow('public asset request timed out')
    expect(aborted).toBe(true)
  })

  it('falls back when a native module import exceeds the runtime timeout', async () => {
    const browser = new Window({ url: 'http://127.0.0.1/' })
    const descriptor = descriptorFixture()
    let imports = 0

    await expect(loadPublicFrontendRelease(descriptor, '/api/v1', {
      fetch: async () => verifiedResponse(descriptor.entry, new TextEncoder().encode('entry').buffer),
      document: browser.document,
      origin: browser.location.origin,
      importModule: () => {
        imports++
        return new Promise(() => {})
      },
      timeoutMS: 1
    })).rejects.toThrow('public module import timed out')
    expect(imports).toBe(1)
  })

  it('removes a stylesheet element when its load event exceeds the runtime timeout', async () => {
    const browser = new Window({ url: 'http://127.0.0.1/' })
    const styleBytes = new TextEncoder().encode('.timeout { display: block }').buffer
    const entryBytes = new TextEncoder().encode('export const apiVersion = 1').buffer
    const style = assetReference('demo.public.asset.timeout', 'style', styleBytes)
    const entry = assetReference('demo.public.component.card.l2.entry', 'script', entryBytes, true, [style.handle])
    const descriptor = descriptorFixture(entry, [style])
    const stylesheet = browser.document.createElement('link')
    let appended = false
    let removed = false
    stylesheet.addEventListener = (() => {}) as typeof stylesheet.addEventListener
    stylesheet.remove = (() => { removed = true }) as typeof stylesheet.remove
    const runtimeDocument = {
      createElement: () => stylesheet,
      head: { append: () => { appended = true } }
    } as unknown as Document

    await expect(loadPublicFrontendRelease(descriptor, '/api/v1', {
      fetch: async () => verifiedResponse(style, styleBytes),
      document: runtimeDocument,
      origin: browser.location.origin,
      importModule: async () => ({ apiVersion: 1, mount: () => undefined }),
      timeoutMS: 1
    })).rejects.toThrow('public stylesheet load timed out')
    expect(appended).toBe(true)
    expect(removed).toBe(true)
  })

  it('loads CSS from its immutable package URL with SRI and waits for readiness', async () => {
    const browser = new Window({
      url: 'http://127.0.0.1/',
      settings: { disableCSSFileLoading: true, handleDisabledFileLoadingAsSuccess: true }
    })
    const styleBytes = new TextEncoder().encode("@import './nested.css'; .demo { src: url('./font.woff2') }").buffer
    const entryBytes = new TextEncoder().encode('export const apiVersion = 1').buffer
    const style = assetReference(
      'demo.public.asset.relative', 'style', styleBytes, false, [], 'frontend/public/card.css'
    )
    const entry = assetReference(
      'demo.public.component.card.l2.entry', 'script', entryBytes, true, [style.handle], 'frontend/public/card.mjs'
    )
    const descriptor = descriptorFixture(entry, [style])
    const fetcher: typeof fetch = async (input) => {
      const reference = String(input).endsWith(style.assetPath) ? style : entry
      return verifiedResponse(reference, reference === style ? styleBytes : entryBytes)
    }
    const loading = loadPublicFrontendRelease(descriptor, '/api/v1', {
      fetch: fetcher,
      document: browser.document,
      origin: browser.location.origin,
      importModule: async () => ({ apiVersion: 1, mount: () => undefined })
    })
    const stylesheet = await waitForStylesheet(browser.document)
    expect(stylesheet.href).toBe(`http://127.0.0.1${style.assetPath}`)
    expect(stylesheet.integrity).toBe(style.integrity)
    // 不声明 crossorigin：同源时 SRI 仍校验；被 X5 云加速/CDN 换源时按 no-cors
    // 加载（opaque 响应跳过 SRI），而不是整块样式被 CORS 拒载。
    expect(stylesheet.getAttribute('crossorigin')).toBeNull()
    const release = await loading
    await release.release()
    expect(stylesheet.isConnected).toBe(false)
  })

  it('deduplicates modules and styles, then removes CSS after the final unmount', async () => {
    const browser = new Window({ url: 'http://127.0.0.1/' })
    const styleBytes = new TextEncoder().encode('.demo { color: red }').buffer
    const entryBytes = new TextEncoder().encode('export const apiVersion = 1').buffer
    const style = assetReference('demo.public.asset.style', 'style', styleBytes)
    const entry = assetReference('demo.public.component.card.l2.entry', 'script', entryBytes, true, [style.handle])
    const descriptor = descriptorFixture(entry, [style])
    let imports = 0
    let fetches = 0
    const fetcher: typeof fetch = async (input) => {
      fetches++
      const path = String(input)
      const reference = path.endsWith(style.assetPath) ? style : entry
      const bytes = reference === style ? styleBytes : entryBytes
      return new Response(bytes, {
        headers: {
          'content-type': reference.type === 'style' ? 'text/css; charset=utf-8' : 'application/javascript; charset=utf-8',
          'x-sforum-asset-digest': reference.digest,
          'x-sforum-asset-integrity': reference.integrity
        }
      })
    }
    const environment = {
      fetch: fetcher,
      document: browser.document,
      origin: browser.location.origin,
      importModule: async (url: string) => {
        imports++
        expect(url).toBe(`http://127.0.0.1${entry.assetPath}`)
        return { apiVersion: 1, mount: () => undefined }
      },
      loadStyle: testStyleLoader(browser.document)
    }

    const first = await loadPublicFrontendRelease(descriptor, '/api/v1', environment)
    const second = await loadPublicFrontendRelease(descriptor, '/api/v1', environment)
    expect(fetches).toBe(2)
    expect(imports).toBe(1)
    const stylesheet = browser.document.head.querySelector<HTMLLinkElement>('link[data-sforum-asset]')
    expect(stylesheet?.href).toBe(`http://127.0.0.1${style.assetPath}`)
    expect(stylesheet?.integrity).toBe(style.integrity)
    await first.release()
    expect(browser.document.head.querySelectorAll('link[data-sforum-asset]').length).toBe(1)
    await second.release()
    expect(browser.document.head.querySelectorAll('link[data-sforum-asset]').length).toBe(0)
  })

  it('cleans already-acquired CSS when entry integrity fails', async () => {
    const browser = new Window({ url: 'http://127.0.0.1/' })
    const styleBytes = new TextEncoder().encode('.demo { color: red }').buffer
    const entryBytes = new TextEncoder().encode('expected entry').buffer
    const style = assetReference('demo.public.asset.style', 'style', styleBytes)
    const entry = assetReference('demo.public.component.card.l2.entry', 'script', entryBytes, true, [style.handle])
    const descriptor = descriptorFixture(entry, [style])
    const fetcher: typeof fetch = async (input) => {
      const reference = String(input).endsWith(style.assetPath) ? style : entry
      const bytes = reference === style ? styleBytes : new TextEncoder().encode('tampered entry').buffer
      return new Response(bytes, {
        headers: {
          'content-type': reference.type === 'style' ? 'text/css' : 'application/javascript',
          'x-sforum-asset-digest': reference.digest,
          'x-sforum-asset-integrity': reference.integrity
        }
      })
    }
    await expect(loadPublicFrontendRelease(descriptor, '/api/v1', {
      fetch: fetcher,
      document: browser.document,
      origin: browser.location.origin,
      importModule: async () => ({ apiVersion: 1, mount: () => undefined }),
      loadStyle: testStyleLoader(browser.document)
    })).rejects.toBeInstanceOf(PublicFrontendContractError)
    expect(browser.document.head.querySelectorAll('link[data-sforum-asset]').length).toBe(0)
  })

  it('shares one pending CSS lease across concurrent mounts and releases it once', async () => {
    const browser = new Window({ url: 'http://127.0.0.1/' })
    const styleBytes = new TextEncoder().encode('.concurrent { display: block }').buffer
    const entryBytes = new TextEncoder().encode('export const apiVersion = 1').buffer
    const style = assetReference('demo.public.asset.concurrent', 'style', styleBytes)
    const entry = assetReference('demo.public.component.card.l2.entry', 'script', entryBytes, true, [style.handle])
    const descriptor = descriptorFixture(entry, [style])
    let styleFetches = 0
    let releaseStyleFetch: (() => void) | undefined
    const styleGate = new Promise<void>(resolve => { releaseStyleFetch = resolve })
    const fetcher: typeof fetch = async (input) => {
      const reference = String(input).endsWith(style.assetPath) ? style : entry
      if (reference === style) {
        styleFetches++
        await styleGate
      }
      const bytes = reference === style ? styleBytes : entryBytes
      return verifiedResponse(reference, bytes)
    }
    const environment = {
      fetch: fetcher,
      document: browser.document,
      origin: browser.location.origin,
      importModule: async () => ({ apiVersion: 1, mount: () => undefined }),
      loadStyle: testStyleLoader(browser.document)
    }
    const first = loadPublicFrontendRelease(descriptor, '/api/v1', environment)
    const second = loadPublicFrontendRelease(descriptor, '/api/v1', environment)
    await Promise.resolve()
    expect(styleFetches).toBe(1)
    releaseStyleFetch?.()
    const [firstRelease, secondRelease] = await Promise.all([first, second])
    expect(browser.document.head.querySelectorAll('link[data-sforum-asset]').length).toBe(1)
    await Promise.all([firstRelease.release(), firstRelease.release()])
    expect(browser.document.head.querySelectorAll('link[data-sforum-asset]').length).toBe(1)
    await secondRelease.release()
    expect(browser.document.head.querySelectorAll('link[data-sforum-asset]').length).toBe(0)
  })
})

function verifiedResponse(reference: PublicFrontendAssetReference, bytes: ArrayBuffer) {
  return new Response(bytes, {
    headers: {
      'content-type': reference.type === 'style' ? 'text/css; charset=utf-8' : 'application/javascript; charset=utf-8',
      'x-sforum-asset-digest': reference.digest,
      'x-sforum-asset-integrity': reference.integrity
    }
  })
}

function testStyleLoader(runtimeDocument: Document) {
  return async (reference: PublicFrontendAssetReference, url: string) => {
    const element = runtimeDocument.createElement('link')
    element.rel = 'stylesheet'
    element.href = url
    element.integrity = reference.integrity
    element.crossOrigin = 'anonymous'
    runtimeDocument.head.append(element)
    return element
  }
}

async function waitForStylesheet(runtimeDocument: Document) {
  for (let attempt = 0; attempt < 100; attempt++) {
    const element = runtimeDocument.head.querySelector<HTMLLinkElement>('link[rel="stylesheet"]')
    if (element) return element
    await new Promise(resolve => setTimeout(resolve, 0))
  }
  throw new Error('stylesheet was not attached')
}

function descriptorFixture(
  entry = assetReference('demo.public.component.card.l2.entry', 'script', new TextEncoder().encode('entry').buffer, true),
  assets: PublicFrontendAssetReference[] = []
): PublicFrontendComponentDescriptor {
  return {
    schemaVersion: PUBLIC_FRONTEND_SCHEMA_VERSION,
    apiVersion: PUBLIC_FRONTEND_API_VERSION,
    trustNotice: PUBLIC_FRONTEND_TRUST_NOTICE,
    extensionId: 'demo.public',
    extensionVersion: '1.0.0',
    packageDigest: entry.packageDigest,
    impactDigest: entry.impactDigest,
    componentId: 'demo.public.component.card',
    contractVersion: 'demo.public.component.card@1',
    action: 'add',
    propsSchema: 'demo.public.component.card.props@1',
    entry,
    assets,
    csp: []
  }
}

const SHARED_ASSET_ARTIFACT = {
  extensionId: 'shared.assets',
  packageDigest: 'c'.repeat(64),
  impactDigest: 'd'.repeat(64)
}

function crossOwnerDescriptorFixture() {
  const styleBytes = new TextEncoder().encode('.shared { display: block }').buffer
  const moduleBytes = new TextEncoder().encode('export const shared = true').buffer
  const entryBytes = new TextEncoder().encode('export const apiVersion = 1').buffer
  const style = assetReference(
    'shared.assets.asset.style', 'style', styleBytes, false, [], 'frontend/public/shared.css', SHARED_ASSET_ARTIFACT
  )
  const sharedModule = assetReference(
    'shared.assets.asset.module', 'script', moduleBytes, true, [style.handle],
    'frontend/public/shared.mjs', SHARED_ASSET_ARTIFACT
  )
  const entry = assetReference(
    'demo.public.component.card.l2.entry', 'script', entryBytes, true, [sharedModule.handle]
  )
  return {
    descriptor: descriptorFixture(entry, [style, sharedModule]),
    style,
    sharedModule
  }
}

function assetReference(
  handle: string,
  type: 'script' | 'style',
  body: ArrayBuffer,
  module = false,
  dependencies: string[] = [],
  packagePath = `frontend/public/${handle}.${type === 'style' ? 'css' : 'mjs'}`,
  artifact = {
    extensionId: 'demo.public',
    packageDigest: 'a'.repeat(64),
    impactDigest: 'b'.repeat(64)
  }
): PublicFrontendAssetReference {
  const digest = createHash('sha256').update(Buffer.from(body)).digest('hex')
  return {
    handle,
    contractVersion: `${handle}@1`,
    extensionId: artifact.extensionId,
    packageDigest: artifact.packageDigest,
    impactDigest: artifact.impactDigest,
    type,
    digest,
    integrity: `sha256-${Buffer.from(digest, 'hex').toString('base64')}`,
    dependencies,
    scope: ['demo.public.component.card'],
    module,
    loading: type === 'script' ? 'lazy' : 'blocking',
    csp: [],
    assetPath: `/_sforum/assets/extensions/${encodeURIComponent(artifact.extensionId)}/${artifact.packageDigest}/${packagePath.split('/').map(encodeURIComponent).join('/')}`
  }
}
