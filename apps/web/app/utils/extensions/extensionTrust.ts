import type { AdminCapabilityGrant, AdminExtensionSource, AdminExtensionType } from '~/utils/admin/adminExtensions'

export type ExecutableTrustAuthority = {
  backendExecution: boolean
  adminFrontendExecution: boolean
  rawRequest: boolean
  rawCoreDatabase: boolean
  outboundNetwork: boolean
  packageFiles: string[]
  secrets: string[]
}

export type ExecutableTrustMigrationDeclaration = {
  id?: string
  contractVersion?: string
  path: string
  digest?: string
  transaction?: 'required' | 'forbidden' | 'auto'
}

/**
 * Go `LocalizedText` 的 wire 形态：`ByLocale` 为空时序列化成**字符串**，
 * 否则序列化成 `{ locale: text }`（`Support/ExtensionManifest/localized_text.go`）。
 * 两种都要吃——插件作者只写一句中文时拿到的是字符串，不是 map。
 */
export type ExtensionLocalizedText = string | Record<string, string>

/**
 * 一条权限声明，与后端 `ManifestPermissionDefinition` 对齐
 * （`apps/api/app/Support/ExtensionManifest/v3_platform_types.go`）。
 *
 * 为什么不留在 `Record<string, unknown>`：披露段要读 `key` / `label` /
 * `trustLevelEarnable` 三个字段，泛型记录只能一路 `as Record<string, unknown>` 硬转，
 * 字段改名（后端 `trustLevelEarnable` → 别的名字）在编译期完全看不出来，
 * 运行时静默变成「一条都不渲染」——那正是启用前最危险的失败方向。
 *
 * `label` / `description` 按 DTO 是**必填但可能为空**（`LocalizedText{}` 序列化成 `""`），
 * 这里保留 `?`：载荷来自扩展包里手写的 manifest，缺字段是现实存在的一种输入，
 * 读侧本来就要容错（缺 label 时回落 key）。
 */
export type ExtensionPermissionDefinition = {
  key: string
  contractVersion?: string
  label?: ExtensionLocalizedText
  description?: ExtensionLocalizedText
  recommendedRoles?: string[]
  assignmentPolicy?: string
  /** 缺省 false 是后端刻意的安全默认：只有显式声明 true 的键才进信任等级候选。 */
  trustLevelEarnable?: boolean
}

export type ExecutableDatabaseGrant = 'own_schema' | 'core_views' | 'host_commands' | 'raw_core' | 'kernel'

type ExecutableTrustDatabaseBase = {
  contractVersion: string
  schema?: string
  role?: string
  coreCompatibility?: string
  backup: {
    required: boolean
    strategy?: string
  }
  retention: {
    onDisable: 'retain'
    onUninstall: 'retain' | 'delete' | 'export'
    days?: number
  }
}

export type ExecutableTrustDatabase = ExecutableTrustDatabaseBase & (
  | { grants: ExecutableDatabaseGrant[], authority?: never }
  | { authority: ExecutableDatabaseGrant, grants?: never }
)

const databaseGrantTierOrder: ExecutableDatabaseGrant[] = [
  'own_schema', 'core_views', 'host_commands', 'raw_core', 'kernel'
]

export function executableDatabaseGrants(database: ExecutableTrustDatabase | null): ExecutableDatabaseGrant[] {
  if (!database) return []
  if (database.grants?.length) {
    return databaseGrantTierOrder.filter(grant => database.grants?.includes(grant))
  }
  if (!database.authority) return []
  const tier = databaseGrantTierOrder.indexOf(database.authority)
  return tier < 0 ? [] : databaseGrantTierOrder.slice(0, tier + 1)
}

export type ExecutableTrustImpact = {
  schemaVersion: 'sforum.trust-impact@2'
  action: 'enable'
  extensionId: string
  extensionVersion: string
  extensionType: AdminExtensionType
  source: AdminExtensionSource
  packageDigest: string
  manifestContract: string
  artifactDigests: Record<string, string>
  binaries: Array<Record<string, unknown>>
  backend: Record<string, unknown>
  routes: Array<Record<string, unknown>>
  guards: Array<Record<string, unknown>>
  guardDeclarations: Array<Record<string, unknown>>
  hooks: Array<Record<string, unknown>>
  events: Array<Record<string, unknown>>
  migrations: Array<Record<string, unknown>>
  migrationDeclarations: ExecutableTrustMigrationDeclaration[]
  providers: Array<Record<string, unknown>>
  jobs: Array<Record<string, unknown>>
  schedules: Array<Record<string, unknown>>
  components: Array<Record<string, unknown>>
  registryComponents: Array<Record<string, unknown>>
  templates: Array<Record<string, unknown>>
  assets: Array<Record<string, unknown>>
  content: Array<Record<string, unknown>>
  database: ExecutableTrustDatabase | null
  cache: Array<Record<string, unknown>>
  services: Array<Record<string, unknown>>
  commands: Array<Record<string, unknown>>
  adminSurfaces: Array<Record<string, unknown>>
  queries: Array<Record<string, unknown>>
  queryResultFilters: Array<Record<string, unknown>>
  identity: Record<string, unknown> | null
  permissionDefinitions: ExtensionPermissionDefinition[]
  media: Array<Record<string, unknown>>
  navigation: Array<Record<string, unknown>>
  regions: Array<Record<string, unknown>>
  contributions: Array<Record<string, unknown>>
  capabilities: AdminCapabilityGrant[]
  permissions: string[]
  requiredFeatures: string[]
  dependencies: Array<Record<string, unknown>>
  lifecycle: Record<string, unknown> | null
  openapi: Array<Record<string, unknown>>
  packageFiles: Array<Record<string, unknown>>
  requestedAuthority: ExecutableTrustAuthority
  contracts: { hostApi: string, frontendApi?: string }
  digest: string
}

// M5：授权归属派生视图（API 的 status.guardAuthorization）。它不在影响文档摘要内：
// 摘要覆盖权限本身，该视图只是把已披露的路由范围与守卫声明连接起来。
export type ExecutableGuardAuthorization = {
  routeId: string
  routeAction?: string
  path: string
  methods: string[]
  access?: string
  permission?: string
  guardId: string
  guardContractVersion?: string
  guardKind?: string
  guardEntry?: string
  guardDigest?: string
  guardPermissions?: string[]
  risk: 'host_owned' | 'extension_code' | 'raw_request' | string
  rawRequest: boolean
  requiresExactTrust: boolean
  fallback?: string
  mutableRequestFields?: string[]
  mutableResponseFields?: string[]
}

export function executableDatabaseMigrationRisk(impact: Pick<ExecutableTrustImpact, 'database' | 'migrationDeclarations'>) {
  const nonTransactional = impact.migrationDeclarations.filter(migration => migration.transaction === 'forbidden')
  const hasMigrations = impact.migrationDeclarations.length > 0
  const backupRequired = impact.database?.backup.required === true
  return {
    hasDatabaseChanges: Boolean(impact.database) || hasMigrations,
    hasMigrations,
    backupRequired,
    missingRequiredBackup: hasMigrations && !backupRequired,
    nonTransactional
  }
}

export type ExecutableTrustStatus = {
  impact: ExecutableTrustImpact
  trustRequired: boolean
  trusted: boolean
  // 仅由状态读取路径填充；影响文档本身不携带该派生视图。
  guardAuthorization?: ExecutableGuardAuthorization[]
}

export type ExecutableTrustChallenge = {
  token: string
  impact: ExecutableTrustImpact
  expiresAt: string
}

export type ExtensionEnableTrustMode = 'legacy' | 'exact'
