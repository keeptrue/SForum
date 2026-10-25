export const ADMIN_MICRO_FRONTEND_API_VERSION = 1 as const

export type SForumAdminToastInput = Readonly<{
  title: string
  description?: string
  kind?: 'success' | 'error'
}>

export type AdminExtensionSettingItem = Readonly<{
  key: string
  label: string
  description?: string
  type: string
  default: string
  value: string
  secretSet?: boolean
  placeholder?: string
  recommendedValue?: string
  group?: string
  options?: ReadonlyArray<{ value: string, label: string, description?: string }>
}>

export type AdminMicroFrontendAppearance = Readonly<{
  colorMode: 'light' | 'dark'
  accent: string
  accentContrast: string
}>

/**
 * 宿主构建标识。`coreVersion` 是兼容判定唯一权威（严格 semver）；
 * `buildVersion` 只用于识别开发版与排障，永远不要拿它做约束比较。
 *
 * 旧宿主可能不提供这个字段：插件必须 feature-detect（`bridge.host` 为
 * `undefined`），不要自己推断宿主版本。
 */
export type AdminHostIdentity = Readonly<{
  coreVersion: string
  buildVersion: string
  valid: boolean
}>

/**
 * 宿主图标解析器：插件只提供图标名，SVG 标记始终由宿主生成。
 * 名称语法为 `i-(lucide|tabler)-<name>`，与宿主贡献点校验共用同一条规则。
 * 无法解析的图标名会从结果中缺失，调用方必须容忍缺项。
 */
export type AdminIconResolver = Readonly<{
  resolve: (names: readonly string[]) => Promise<Readonly<Record<string, string>>>
}>

export type AdminMicroFrontendBridgeV1 = Readonly<{
  apiVersion: typeof ADMIN_MICRO_FRONTEND_API_VERSION
  extensionId: string
  extensionVersion: string
  locale: string
  appearance: AdminMicroFrontendAppearance
  /** 可选：宿主未提供时为 `undefined`，插件必须优雅降级。 */
  icons?: AdminIconResolver
  /** 可选：旧宿主没有这一项；缺失即「未知」，不要猜版本。 */
  host?: AdminHostIdentity
  settings: Readonly<{
    items: readonly AdminExtensionSettingItem[]
    values: () => Readonly<Record<string, string>>
    updateValue: (key: string, value: string) => void
    save: () => Promise<void>
    reset: () => Promise<void>
  }>
  request: <T>(path: string, options?: Record<string, unknown>) => Promise<T>
  toast: (input: SForumAdminToastInput) => void
  t: (key: string, params?: Record<string, unknown>) => string
  navigate: (adminPath: string) => Promise<void>
}>

/**
 * Host-mediated install request. Only available on a page whose manifest
 * declared `hostActions: ["extension.install"]`.
 *
 * The Host pre-checks the caller's permission, shows its own confirmation
 * dialog (URL host, full URL, SHA-256, declared identity, `super_admin` note),
 * then performs one synchronous install. It never enables, trusts, or activates
 * anything: a successful result means the package is *staged*.
 */
export type AdminHostInstallRequest = Readonly<{
  /** Public HTTPS URL of the ZIP. */
  url: string
  /** 64 lowercase hex characters; pins the exact bytes. */
  sha256: string
  /** Required: binds the package identity the Host verifies after download. */
  expectedExtensionId: string
  /** Required: selects the permission gate (and the follow-up management page). */
  expectedType: 'plugin' | 'theme'
  /** Optional: the exact version the catalog advertised. */
  expectedVersion?: string
}>

export type AdminHostInstallResult = Readonly<{
  extensionId: string
  version: string
  type: 'plugin' | 'theme'
  packageDigest: string
  upgraded: boolean
  /**
   * True when the package ships an executable backend, so enabling it still
   * needs the Host's exact-artifact trust confirmation.
   */
  requiresTrust: boolean
}>

export type AdminHostInstallFailureCode =
  | 'install_permission_denied'
  | 'install_cancelled'
  | 'install_in_progress'
  | 'install_invalid_request'
  | 'install_failed'

export type AdminPageDescriptor = Readonly<{
  path: string
  label: string
  description?: string
  icon?: string
}>

export type AdminPageBridgeV1 = Readonly<{
  apiVersion: typeof ADMIN_MICRO_FRONTEND_API_VERSION
  extensionId: string
  extensionVersion: string
  locale: string
  appearance: AdminMicroFrontendAppearance
  /** 可选：宿主未提供时为 `undefined`，插件必须优雅降级。 */
  icons?: AdminIconResolver
  /** 可选：旧宿主没有这一项；缺失即「未知」，不要猜版本。 */
  host?: AdminHostIdentity
  page: AdminPageDescriptor
  request: <T>(path: string, options?: Record<string, unknown>) => Promise<T>
  toast: (input: SForumAdminToastInput) => void
  t: (key: string, params?: Record<string, unknown>) => string
  navigate: (adminPath: string) => Promise<void>
  /**
   * Optional Host-mediated package install. Present only when this page
   * declared `hostActions: ["extension.install"]`; plugins MUST feature-detect
   * (`typeof bridge.installExtension === 'function'`) and degrade otherwise.
   *
   * The promise settles when the package is staged (success) or rejected:
   * `install_permission_denied`, `install_cancelled`, `install_in_progress`,
   * `install_invalid_request`, `install_failed`. Navigating away aborts the
   * install and rejects with `install_cancelled`.
   */
  installExtension?: (request: AdminHostInstallRequest) => Promise<AdminHostInstallResult>
}>

export type AdminMicroFrontendCleanup = () => void | Promise<void>

export type AdminMicroFrontendModuleV1 = Readonly<{
  apiVersion: typeof ADMIN_MICRO_FRONTEND_API_VERSION
  mount: (
    target: HTMLElement,
    bridge: AdminMicroFrontendBridgeV1
  ) => void | AdminMicroFrontendCleanup | Promise<void | AdminMicroFrontendCleanup>
}>

export type AdminPageModuleV1 = Readonly<{
  apiVersion: typeof ADMIN_MICRO_FRONTEND_API_VERSION
  mount: (
    target: HTMLElement,
    bridge: AdminPageBridgeV1
  ) => void | AdminMicroFrontendCleanup | Promise<void | AdminMicroFrontendCleanup>
}>
