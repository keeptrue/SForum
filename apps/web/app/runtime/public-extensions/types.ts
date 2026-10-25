export const PUBLIC_FRONTEND_SCHEMA_VERSION = 'sforum.public-frontend-component@1'
export const PUBLIC_FRONTEND_API_VERSION = 1
export const PUBLIC_FRONTEND_TRUST_NOTICE = 'fully_trusted_browser_code'
export const PUBLIC_FRONTEND_DESCRIPTOR_TIMEOUT_MS = 10_000
export const PUBLIC_FRONTEND_EXACT_HEADERS = Object.freeze({
  extensionId: 'X-SForum-Public-Extension-ID',
  extensionVersion: 'X-SForum-Public-Extension-Version',
  packageDigest: 'X-SForum-Public-Package-Digest',
  impactDigest: 'X-SForum-Public-Impact-Digest',
  componentId: 'X-SForum-Public-Component-ID'
})

const ID_PATTERN = /^[a-z0-9][a-z0-9._-]{1,120}$/
const DIGEST_PATTERN = /^[0-9a-f]{64}$/
const CONTRACT_PATTERN = /^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$/
const MAX_ASSETS = 256

export type PublicFrontendAssetReference = {
  handle: string
  contractVersion: string
  extensionId: string
  packageDigest: string
  impactDigest: string
  type: 'script' | 'style'
  digest: string
  integrity: string
  dependencies: string[]
  scope: string[]
  module: boolean
  loading: 'blocking' | 'defer' | 'async' | 'preload' | 'lazy' | ''
  csp: string[]
  assetPath: string
}

export type PublicFrontendComponentDescriptor = {
  schemaVersion: typeof PUBLIC_FRONTEND_SCHEMA_VERSION
  apiVersion: typeof PUBLIC_FRONTEND_API_VERSION
  trustNotice: typeof PUBLIC_FRONTEND_TRUST_NOTICE
  extensionId: string
  extensionVersion: string
  /**
   * 宿主 release 级版本（严格 semver）。公开描述符匿名可读，因此只给这一项，
   * 不含 commit 或 dev 构建标识。旧宿主缺省，组件必须按「未知」处理。
   */
  coreVersion?: string
  packageDigest: string
  impactDigest: string
  componentId: string
  contractVersion: string
  action: string
  targetId?: string
  targetContractVersion?: string
  propsSchema?: string
  resultSchema?: string
  entry: PublicFrontendAssetReference
  assets: PublicFrontendAssetReference[]
  csp: string[]
}

export type PublicFrontendIconResolver = Readonly<{
  resolve: (names: readonly string[]) => Promise<Readonly<Record<string, string>>>
}>

export type PublicFrontendBridgeV1 = Readonly<{
  apiVersion: typeof PUBLIC_FRONTEND_API_VERSION
  trust: typeof PUBLIC_FRONTEND_TRUST_NOTICE
  extensionId: string
  extensionVersion: string
  packageDigest: string
  impactDigest: string
  componentId: string
  locale: string
  appearance: Readonly<{
    colorMode: 'light' | 'dark'
    accent: string
    accentContrast: string
  }>
  /** 可选：宿主未提供时为 `undefined`，插件必须优雅降级。 */
  icons?: PublicFrontendIconResolver
  /**
   * 宿主 release 级版本；公开面只有 coreVersion。缺失即「未知」，
   * 组件不得推断或硬编码宿主版本。
   */
  host?: Readonly<{ coreVersion: string }>
  props: Readonly<Record<string, unknown>>
  ssrRoot: HTMLElement
  request: <T>(path: string, options?: Record<string, unknown>) => Promise<T>
  navigate: (path: string) => Promise<void>
}>

export type PublicFrontendCleanup = () => void | Promise<void>

export type PublicFrontendModuleV1 = {
  apiVersion: typeof PUBLIC_FRONTEND_API_VERSION
  mount: (
    target: HTMLElement,
    bridge: PublicFrontendBridgeV1
  ) => void | PublicFrontendCleanup | Promise<void | PublicFrontendCleanup>
  unmount?: (target: HTMLElement, bridge: PublicFrontendBridgeV1) => void | Promise<void>
}

export class PublicFrontendContractError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'PublicFrontendContractError'
  }
}

export function parsePublicFrontendDescriptor(
  input: unknown,
  expectedExtensionId: string,
  expectedComponentId: string
): PublicFrontendComponentDescriptor {
  if (!isRecord(input)
    || input.schemaVersion !== PUBLIC_FRONTEND_SCHEMA_VERSION
    || input.apiVersion !== PUBLIC_FRONTEND_API_VERSION
    || input.trustNotice !== PUBLIC_FRONTEND_TRUST_NOTICE
    || input.extensionId !== expectedExtensionId
    || input.componentId !== expectedComponentId
    || typeof input.extensionVersion !== 'string'
    || !input.extensionVersion
    || input.extensionVersion !== input.extensionVersion.trim()
    || !validOptionalCoreVersion(input.coreVersion)
    || !DIGEST_PATTERN.test(String(input.packageDigest))
    || !DIGEST_PATTERN.test(String(input.impactDigest))
    || !CONTRACT_PATTERN.test(String(input.contractVersion))
    || typeof input.action !== 'string'
    || !Array.isArray(input.assets)
    || input.assets.length > MAX_ASSETS
    || !Array.isArray(input.csp ?? [])) {
    throw new PublicFrontendContractError('invalid public component descriptor')
  }

  const entry = parseAssetReference(input.entry)
  if (entry.extensionId !== input.extensionId
    || entry.packageDigest !== input.packageDigest
    || entry.impactDigest !== input.impactDigest
    || entry.type !== 'script'
    || !entry.module) {
    throw new PublicFrontendContractError('invalid public component entry')
  }
  const assets = input.assets.map(parseAssetReference)
  const handles = new Set<string>()
  for (const asset of [...assets, entry]) {
    if (handles.has(asset.handle)) {
      throw new PublicFrontendContractError('duplicate public asset handle')
    }
    handles.add(asset.handle)
  }
  for (const dependency of entry.dependencies) {
    if (!dependency.startsWith('core.asset.') && !handles.has(dependency)) {
      throw new PublicFrontendContractError('missing public entry dependency')
    }
  }
  const descriptor: PublicFrontendComponentDescriptor = {
    schemaVersion: PUBLIC_FRONTEND_SCHEMA_VERSION,
    apiVersion: PUBLIC_FRONTEND_API_VERSION,
    trustNotice: PUBLIC_FRONTEND_TRUST_NOTICE,
    extensionId: input.extensionId,
    extensionVersion: input.extensionVersion,
    coreVersion: optionalString(input.coreVersion),
    packageDigest: input.packageDigest,
    impactDigest: input.impactDigest,
    componentId: input.componentId,
    contractVersion: input.contractVersion,
    action: input.action,
    targetId: optionalString(input.targetId),
    targetContractVersion: optionalString(input.targetContractVersion),
    propsSchema: optionalString(input.propsSchema),
    resultSchema: optionalString(input.resultSchema),
    entry,
    assets,
    csp: parseStrings(input.csp ?? [], 'csp')
  }
  validatePublicFrontendAssetGraph(descriptor)
  return descriptor
}

export function publicFrontendExactHeaders(descriptor: PublicFrontendComponentDescriptor): Record<string, string> {
  return {
    [PUBLIC_FRONTEND_EXACT_HEADERS.extensionId]: descriptor.extensionId,
    [PUBLIC_FRONTEND_EXACT_HEADERS.extensionVersion]: descriptor.extensionVersion,
    [PUBLIC_FRONTEND_EXACT_HEADERS.packageDigest]: descriptor.packageDigest,
    [PUBLIC_FRONTEND_EXACT_HEADERS.impactDigest]: descriptor.impactDigest,
    [PUBLIC_FRONTEND_EXACT_HEADERS.componentId]: descriptor.componentId
  }
}

export function publicFrontendRequestOptions(
  descriptor: PublicFrontendComponentDescriptor,
  options: Record<string, unknown> = {}
) {
  const headers: Record<string, string> = {}
  const protectedHeaders = new Set(Object.values(PUBLIC_FRONTEND_EXACT_HEADERS).map(value => value.toLowerCase()))
  if (isRecord(options.headers)) {
    for (const [name, value] of Object.entries(options.headers)) {
      if (typeof value === 'string' && !protectedHeaders.has(name.toLowerCase())) {
        headers[name] = value
      }
    }
  }
  return {
    ...options,
    headers: { ...headers, ...publicFrontendExactHeaders(descriptor) }
  }
}

export function samePublicFrontendDescriptor(
  left: PublicFrontendComponentDescriptor,
  right: PublicFrontendComponentDescriptor
) {
  return descriptorFingerprint(left) === descriptorFingerprint(right)
}

export function publicComponentPath(extensionId: string, componentId: string) {
  assertID(extensionId, 'extension id')
  assertID(componentId, 'component id')
  return `/extensions/runtime/${encodeURIComponent(extensionId)}/components/${encodeURIComponent(componentId)}`
}

export function publicExtensionRequestPath(extensionId: string, path: string) {
  assertID(extensionId, 'extension id')
  const cleanPath = path.trim()
  const routePath = cleanPath.split(/[?#]/, 1)[0] || ''
  if (/^(?:[a-z][a-z\d+.-]*:)?\/\//i.test(cleanPath)
    || cleanPath.includes('\\')
    || /%(?:2e|2f|5c)/i.test(routePath)
    || routePath.split('/').some(segment => segment === '.' || segment === '..')) {
    throw new PublicFrontendContractError('public extension request path escaped its route')
  }
  const suffix = cleanPath ? `/${cleanPath.replace(/^\/+/, '')}` : ''
  return `/extensions/${encodeURIComponent(extensionId)}${suffix}`
}

export function assertPublicNavigationPath(path: string) {
  const value = path.trim()
  if (!value.startsWith('/') || value.startsWith('//') || value.includes('\\') || /[\u0000-\u001f]/.test(value)) {
    throw new PublicFrontendContractError('public extension navigation must stay on this site')
  }
  return value
}

function parseAssetReference(input: unknown): PublicFrontendAssetReference {
  if (!isRecord(input)
    || typeof input.handle !== 'string'
    || !ID_PATTERN.test(input.handle)
    || typeof input.contractVersion !== 'string'
    || !CONTRACT_PATTERN.test(input.contractVersion)
    || typeof input.extensionId !== 'string'
    || !ID_PATTERN.test(input.extensionId)
    || typeof input.packageDigest !== 'string'
    || !DIGEST_PATTERN.test(input.packageDigest)
    || typeof input.impactDigest !== 'string'
    || !DIGEST_PATTERN.test(input.impactDigest)
    || typeof input.digest !== 'string'
    || !DIGEST_PATTERN.test(input.digest)
    || (input.type !== 'script' && input.type !== 'style')
    || typeof input.integrity !== 'string'
    || input.integrity !== digestIntegrity(input.digest)
    || typeof input.module !== 'boolean'
    || typeof input.loading !== 'string'
    || !['', 'blocking', 'defer', 'async', 'preload', 'lazy'].includes(input.loading)
    || !Array.isArray(input.dependencies ?? [])
    || !Array.isArray(input.scope ?? [])
    || !Array.isArray(input.csp ?? [])
    || typeof input.assetPath !== 'string') {
    throw new PublicFrontendContractError('invalid public asset reference')
  }
  if (!validPublicAssetIdentity(input.extensionId, input.handle, input.contractVersion)) {
    throw new PublicFrontendContractError('public asset identity does not match its owner')
  }
  if (input.type === 'style' && input.module) {
    throw new PublicFrontendContractError('stylesheet cannot be an ESM module')
  }
  validatePackageAssetPath(input.assetPath, input.extensionId, input.packageDigest, input.type)
  return {
    handle: input.handle,
    contractVersion: input.contractVersion,
    extensionId: input.extensionId,
    packageDigest: input.packageDigest,
    impactDigest: input.impactDigest,
    type: input.type,
    digest: input.digest,
    integrity: input.integrity,
    dependencies: parseIDs(input.dependencies ?? []),
    scope: parseIDs(input.scope ?? []),
    module: input.module,
    loading: input.loading as PublicFrontendAssetReference['loading'],
    csp: parseStrings(input.csp ?? [], 'csp'),
    assetPath: input.assetPath
  }
}

export function parsePublicFrontendAssetReference(input: unknown): PublicFrontendAssetReference {
  return parseAssetReference(input)
}

function validatePackageAssetPath(
  assetPath: string,
  extensionId: string,
  packageDigest: string,
  assetType: 'script' | 'style'
) {
  const prefix = `/_sforum/assets/extensions/${encodeURIComponent(extensionId)}/${packageDigest}/`
  if (!assetPath.startsWith(prefix)) {
    throw new PublicFrontendContractError('public asset path is not exact-artifact bound')
  }
  const encodedPath = assetPath.slice(prefix.length)
  if (!encodedPath || encodedPath.includes('\\') || encodedPath.includes('?') || encodedPath.includes('#')) {
    throw new PublicFrontendContractError('public asset package path is invalid')
  }
  const decodedSegments: string[] = []
  for (const segment of encodedPath.split('/')) {
    let decoded: string
    try {
      decoded = decodeURIComponent(segment)
    } catch {
      throw new PublicFrontendContractError('public asset package path is invalid')
    }
    if (!decoded || decoded === '.' || decoded === '..' || decoded.includes('/') || decoded.includes('\\')
      || /[\u0000-\u001f]/.test(decoded)) {
      throw new PublicFrontendContractError('public asset package path is invalid')
    }
    decodedSegments.push(decoded)
  }
  const packagePath = decodedSegments.join('/').toLowerCase()
  if (assetType === 'script' ? !/\.m?js$/.test(packagePath) : !packagePath.endsWith('.css')) {
    throw new PublicFrontendContractError('public asset package type is invalid')
  }
}

function validatePublicFrontendAssetGraph(descriptor: PublicFrontendComponentDescriptor) {
  const assets = [...descriptor.assets, descriptor.entry]
  const byHandle = new Map(assets.map(asset => [asset.handle, asset]))
  const resolved = new Set<string>()
  const reachable = new Set<string>()
  const ownerArtifacts = new Map<string, { packageDigest: string, impactDigest: string }>([[
    descriptor.extensionId,
    { packageDigest: descriptor.packageDigest, impactDigest: descriptor.impactDigest }
  ]])

  for (const asset of assets) {
    // 依赖节点不携带 extensionVersion；Host 在签发 descriptor 前校验每个 owner 的 live 版本与信任状态。
    const ownerArtifact = ownerArtifacts.get(asset.extensionId)
    if (ownerArtifact
      && (ownerArtifact.packageDigest !== asset.packageDigest || ownerArtifact.impactDigest !== asset.impactDigest)) {
      throw new PublicFrontendContractError('public asset owner has inconsistent exact artifacts')
    }
    ownerArtifacts.set(asset.extensionId, {
      packageDigest: asset.packageDigest,
      impactDigest: asset.impactDigest
    })
    if (asset.type === 'script' && !asset.module) {
      throw new PublicFrontendContractError('public component scripts must be ESM')
    }
    if (asset.scope.length > 0
      && !asset.scope.includes(descriptor.componentId)
      && (!descriptor.targetId || !asset.scope.includes(descriptor.targetId))) {
      throw new PublicFrontendContractError('public asset scope does not include the component')
    }
    for (const dependency of asset.dependencies) {
      if (dependency === asset.handle) {
        throw new PublicFrontendContractError('public asset depends on itself')
      }
      const dependencyAsset = byHandle.get(dependency)
      if (!dependencyAsset && dependency.startsWith('core.asset.')) continue
      if (!dependencyAsset) {
        throw new PublicFrontendContractError('public asset dependency is missing')
      }
      if (!resolved.has(dependency)) {
        throw new PublicFrontendContractError('public asset graph is not in dependency order')
      }
    }
    resolved.add(asset.handle)
  }

  const visit = (handle: string) => {
    if (reachable.has(handle)) return
    const asset = byHandle.get(handle)
    if (!asset) {
      if (handle.startsWith('core.asset.')) return
      throw new PublicFrontendContractError('public asset dependency is missing')
    }
    reachable.add(handle)
    for (const dependency of asset.dependencies) visit(dependency)
  }
  visit(descriptor.entry.handle)
  if (descriptor.assets.some(asset => !reachable.has(asset.handle))) {
    throw new PublicFrontendContractError('public descriptor contains an unreachable asset')
  }

  const declaredCSP = uniqueStrings(descriptor.csp, 'csp')
  const graphCSP = [...new Set(assets.flatMap(asset => asset.csp))].sort()
  if (declaredCSP.length !== graphCSP.length || declaredCSP.some((value, index) => value !== graphCSP[index])) {
    throw new PublicFrontendContractError('public descriptor CSP does not match its asset graph')
  }
}

function validPublicAssetIdentity(extensionId: string, handle: string, contractVersion: string) {
  const contractID = contractVersion.slice(0, contractVersion.lastIndexOf('@'))
  const coreOwner = extensionId.startsWith('core.')
  if (coreOwner || handle.startsWith('core.')) {
    return coreOwner
      && handle.startsWith('core.asset.')
      && contractID === `sforum.${handle.slice('core.'.length)}`
  }
  return handle.startsWith(`${extensionId}.`) && contractID === handle
}

function parseIDs(input: unknown[]) {
  const values = parseStrings(input, 'id list')
  if (values.some(value => !ID_PATTERN.test(value)) || new Set(values).size !== values.length) {
    throw new PublicFrontendContractError('invalid public asset id list')
  }
  return values
}

function parseStrings(input: unknown[], label: string) {
  if (input.some(value => typeof value !== 'string')) {
    throw new PublicFrontendContractError(`invalid public asset ${label}`)
  }
  return input as string[]
}

function uniqueStrings(input: string[], label: string) {
  const values = [...input].sort()
  if (values.some(value => !value.trim()) || new Set(values).size !== values.length) {
    throw new PublicFrontendContractError(`invalid public asset ${label}`)
  }
  return values
}

function digestIntegrity(digest: string) {
  if (!DIGEST_PATTERN.test(digest)) return ''
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'
  const bytes = digest.match(/../g)?.map(value => Number.parseInt(value, 16)) ?? []
  let encoded = ''
  for (let index = 0; index < bytes.length; index += 3) {
    const first = bytes[index] ?? 0
    const second = bytes[index + 1]
    const third = bytes[index + 2]
    encoded += alphabet[first >> 2]
    encoded += alphabet[((first & 3) << 4) | ((second ?? 0) >> 4)]
    encoded += second === undefined ? '=' : alphabet[((second & 15) << 2) | ((third ?? 0) >> 6)]
    encoded += third === undefined ? '=' : alphabet[third & 63]
  }
  return `sha256-${encoded}`
}

function descriptorFingerprint(descriptor: PublicFrontendComponentDescriptor) {
  return JSON.stringify(descriptor)
}

function optionalString(input: unknown) {
  return typeof input === 'string' && input ? input : undefined
}

// coreVersion 是可选的（旧宿主没有），但一旦出现就必须是规范的非空字符串：
// 描述符是 fail-closed 契约，半截版本号比没有更容易被误用。
function validOptionalCoreVersion(input: unknown) {
  if (input === undefined || input === null) return true
  return typeof input === 'string' && input.length > 0 && input === input.trim()
}

function assertID(value: string, label: string) {
  if (!ID_PATTERN.test(value)) {
    throw new PublicFrontendContractError(`invalid ${label}`)
  }
}

function isRecord(input: unknown): input is Record<string, any> {
  return typeof input === 'object' && input !== null && !Array.isArray(input)
}
