<script setup lang="ts">
import {
  executableDatabaseGrants,
  executableDatabaseMigrationRisk,
  type ExecutableGuardAuthorization,
  type ExecutableTrustImpact,
  type ExecutableTrustMigrationDeclaration
} from '~/utils/extensions/extensionTrust'

const props = defineProps<{
  impact: ExecutableTrustImpact
  // M5：授权归属派生视图（API 的 status.guardAuthorization）。它不在 impact 摘要内，
  // 因此缺失时不影响其余披露内容的渲染。
  guardAuthorization?: ExecutableGuardAuthorization[]
}>()
const { t, locale } = useI18n()

/**
 * 能力清单的可读面。
 *
 * 之前能力只在折叠的 JSON dump 里出现 —— 运营点「完成」前看不到 `badges.fact.write`
 * 这类条目到底允许扩展做什么。这里只做两件事：渲染后端已给的双语 label/description，
 * 以及为**需要额外解释**的能力补一句前端披露（不重复描述能力语义）。
 */
const CAPABILITY_DISCLOSURES: Record<string, string> = {
  'badges.fact.write': 'admin.extensions.trust.capabilities.disclosures.badgesFactWrite'
}

const zh = computed(() => String(locale.value ?? '').toLowerCase().startsWith('zh'))
const capabilityRows = computed(() => props.impact.capabilities ?? [])

function capabilityLabel(row: { labelZh: string, labelEn: string, key: string }) {
  const primary = zh.value ? row.labelZh : row.labelEn
  const secondary = zh.value ? row.labelEn : row.labelZh
  return primary || secondary || row.key
}

function capabilityDisclosure(row: { key: string }) {
  const key = CAPABILITY_DISCLOSURES[row.key]
  return key ? t(key) : ''
}
/**
 * 「可被信任等级授予的权限」（M4a 披露，计划 §4.5 M4.1）。
 *
 * 为什么必须在**启用前**看：勾进某一档 = 该档及以上自动获得这项能力，不是「可以手动授予」。
 * 标题本身就把这层语义写清（`trustLevelEarnable.title`），因为本弹窗的既有约束是
 * **只渲染 label、不渲染 description** —— 逐条 description 在这里是噪声，靠标题自解释。
 *
 * 三条边界：
 * 1. 只认 `trustLevelEarnable === true`（缺省 false 是后端刻意的安全默认）；
 * 2. `label` 是 Go 的 `LocalizedText`：wire 上**可能是字符串，也可能是 locale map**
 *    （`MarshalJSON` 按 `ByLocale` 是否为空二选一），两种都要吃；
 * 3. 一条都没有时**整段不渲染** —— 空段会让运营以为功能坏了或数据没加载。
 */
type TrustLevelEarnableRow = { key: string, label: string }

const trustLevelEarnableRows = computed<TrustLevelEarnableRow[]>(() => {
  const list = Array.isArray(props.impact.permissionDefinitions) ? props.impact.permissionDefinitions : []
  return list
    .filter(row => row && (row as Record<string, unknown>).trustLevelEarnable === true)
    .map(row => ({
      key: String((row as Record<string, unknown>).key ?? ''),
      label: localizedText((row as Record<string, unknown>).label)
    }))
    .filter(row => row.key !== '')
})

/** `LocalizedText` 的两种 wire 形态（字符串 / locale map）统一取当前语言。 */
function localizedText(value: unknown): string {
  if (typeof value === 'string') return value.trim()
  if (!value || typeof value !== 'object') return ''
  const map = value as Record<string, unknown>
  const order = zh.value
    ? ['zh-CN', 'zh', 'en-US', 'en']
    : ['en-US', 'en', 'zh-CN', 'zh']
  for (const key of order) {
    const candidate = map[key]
    if (typeof candidate === 'string' && candidate.trim() !== '') return candidate.trim()
  }
  const fallback = Object.values(map).find(entry => typeof entry === 'string' && entry.trim() !== '')
  return typeof fallback === 'string' ? fallback.trim() : ''
}

const showBackupGuidance = ref(true)
let backupGuidanceTimer: ReturnType<typeof setTimeout> | undefined

const database = computed(() => props.impact.database)
const databaseGrants = computed(() => executableDatabaseGrants(database.value))
const databaseGrantSummary = computed(() => databaseGrants.value.join(' · '))
const migrationRisk = computed(() => executableDatabaseMigrationRisk(props.impact))
const databaseAuthorityColor = computed(() => {
  if (databaseGrants.value.includes('raw_core') || databaseGrants.value.includes('kernel')) return 'error'
  if (databaseGrants.value.includes('core_views') || databaseGrants.value.includes('host_commands')) return 'warning'
  return 'neutral'
})
const riskDescription = computed(() => [
  migrationRisk.value.nonTransactional.length
    ? t('admin.extensions.trust.databaseRisk.nonTransactionalWarning', { count: migrationRisk.value.nonTransactional.length })
    : '',
  migrationRisk.value.missingRequiredBackup
    ? t('admin.extensions.trust.databaseRisk.missingRequiredBackupWarning')
    : ''
].filter(Boolean).join(' '))

function migrationLabel(migration: ExecutableTrustMigrationDeclaration, index: number) {
  return migration.id || migration.path || t('admin.extensions.trust.databaseRisk.migrationFallback', { index: index + 1 })
}

function migrationTransaction(migration: ExecutableTrustMigrationDeclaration) {
  return migration.transaction || 'auto'
}

function scheduleBackupGuidanceDismissal() {
  showBackupGuidance.value = true
  if (backupGuidanceTimer) clearTimeout(backupGuidanceTimer)
  backupGuidanceTimer = setTimeout(() => {
    showBackupGuidance.value = false
  }, 10000)
}

onMounted(scheduleBackupGuidanceDismissal)
watch(() => props.impact.digest, scheduleBackupGuidanceDismissal)
onBeforeUnmount(() => {
  if (backupGuidanceTimer) clearTimeout(backupGuidanceTimer)
})

const guardAuthorization = computed(() => props.guardAuthorization ?? [])
const rawRequestGuards = computed(() => guardAuthorization.value.filter(row => row.rawRequest))
function guardRiskColor(row: ExecutableGuardAuthorization) {
  if (row.risk === 'raw_request') return 'error'
  if (row.risk === 'extension_code') return 'warning'
  return 'neutral'
}
function guardScope(row: ExecutableGuardAuthorization) {
  return `${row.methods.join(', ')} ${row.path}`.trim()
}
function guardRequestedFields(row: ExecutableGuardAuthorization) {
  return [...(row.mutableRequestFields ?? []), ...(row.mutableResponseFields ?? [])]
}

const sections = computed(() => [
  { key: 'artifactDigests', value: props.impact.artifactDigests },
  { key: 'requestedAuthority', value: props.impact.requestedAuthority },
  { key: 'contracts', value: props.impact.contracts },
  { key: 'binaries', value: props.impact.binaries },
  { key: 'backend', value: props.impact.backend },
  { key: 'routes', value: props.impact.routes },
  { key: 'guards', value: props.impact.guards },
  { key: 'guardDeclarations', value: props.impact.guardDeclarations },
  { key: 'hooks', value: props.impact.hooks },
  { key: 'events', value: props.impact.events },
  { key: 'migrations', value: props.impact.migrations },
  { key: 'migrationDeclarations', value: props.impact.migrationDeclarations },
  { key: 'providers', value: props.impact.providers },
  { key: 'jobs', value: props.impact.jobs },
  { key: 'schedules', value: props.impact.schedules },
  { key: 'components', value: props.impact.components },
  { key: 'registryComponents', value: props.impact.registryComponents },
  { key: 'templates', value: props.impact.templates },
  { key: 'assets', value: props.impact.assets },
  { key: 'content', value: props.impact.content },
  { key: 'database', value: props.impact.database },
  { key: 'cache', value: props.impact.cache },
  { key: 'services', value: props.impact.services },
  { key: 'commands', value: props.impact.commands },
  { key: 'adminSurfaces', value: props.impact.adminSurfaces },
  { key: 'queries', value: props.impact.queries },
  { key: 'queryResultFilters', value: props.impact.queryResultFilters },
  { key: 'identity', value: props.impact.identity },
  { key: 'permissionDefinitions', value: props.impact.permissionDefinitions },
  { key: 'media', value: props.impact.media },
  { key: 'navigation', value: props.impact.navigation },
  { key: 'regions', value: props.impact.regions },
  { key: 'contributions', value: props.impact.contributions },
  { key: 'capabilities', value: props.impact.capabilities },
  { key: 'permissions', value: props.impact.permissions },
  { key: 'requiredFeatures', value: props.impact.requiredFeatures },
  { key: 'dependencies', value: props.impact.dependencies },
  { key: 'lifecycle', value: props.impact.lifecycle },
  { key: 'openapi', value: props.impact.openapi },
  { key: 'packageFiles', value: props.impact.packageFiles }
])

function itemCount(value: unknown) {
  if (Array.isArray(value)) return value.length
  if (value && typeof value === 'object') return Object.keys(value).length
  return value ? 1 : 0
}

function formatted(value: unknown) {
  return JSON.stringify(value, null, 2)
}
</script>

<template>
  <div class="space-y-4">
    <dl class="grid gap-3 text-xs sm:grid-cols-2">
      <div>
        <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.schemaVersion') }}</dt>
        <dd class="mt-1 font-mono text-slate-900 dark:text-zinc-100">{{ impact.schemaVersion }}</dd>
      </div>
      <div>
        <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.manifestContract') }}</dt>
        <dd class="mt-1 font-mono text-slate-900 dark:text-zinc-100">{{ impact.manifestContract }}</dd>
      </div>
      <div class="sm:col-span-2">
        <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.identity') }}</dt>
        <dd class="mt-1 break-all font-mono text-slate-900 dark:text-zinc-100">
          {{ impact.extensionId }}@{{ impact.extensionVersion }} / {{ impact.extensionType }} / {{ impact.source }}
        </dd>
      </div>
      <div class="sm:col-span-2">
        <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.packageDigest') }}</dt>
        <dd class="mt-1 break-all font-mono text-slate-900 dark:text-zinc-100">{{ impact.packageDigest }}</dd>
      </div>
      <div class="sm:col-span-2">
        <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.impactDigest') }}</dt>
        <dd class="mt-1 break-all font-mono text-slate-900 dark:text-zinc-100">{{ impact.digest }}</dd>
      </div>
    </dl>

    <section
      v-if="migrationRisk.hasDatabaseChanges"
      class="space-y-4 border-y border-slate-200 py-4 dark:border-zinc-800"
      data-testid="extension-database-risk"
    >
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex min-w-0 items-center gap-2">
          <UIcon name="i-lucide-database" class="size-5 shrink-0 text-slate-500 dark:text-zinc-400" />
          <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
            {{ t('admin.extensions.trust.databaseRisk.title') }}
          </h3>
        </div>
        <UBadge v-if="database" :color="databaseAuthorityColor" variant="subtle" class="max-w-full whitespace-normal break-words font-mono">
          {{ databaseGrantSummary }}
        </UBadge>
      </div>

      <UAlert
        v-if="riskDescription"
        color="error"
        variant="subtle"
        icon="i-lucide-triangle-alert"
        :title="t('admin.extensions.trust.databaseRisk.highRiskTitle')"
        :description="riskDescription"
        data-testid="extension-database-high-risk"
      />
      <UAlert
        v-if="migrationRisk.hasMigrations && showBackupGuidance"
        color="warning"
        variant="subtle"
        icon="i-lucide-database-backup"
        :title="t('admin.extensions.trust.databaseRisk.backupGuidanceTitle')"
        :description="t('admin.extensions.trust.databaseRisk.backupGuidanceDescription')"
        data-testid="extension-database-backup-guidance"
      />

      <dl v-if="database" class="grid gap-x-5 gap-y-3 text-xs sm:grid-cols-2 lg:grid-cols-4">
        <div class="min-w-0">
          <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.databaseRisk.grants') }}</dt>
          <dd class="mt-1 break-all font-mono text-slate-900 dark:text-zinc-100">{{ databaseGrantSummary }}</dd>
        </div>
        <div class="min-w-0">
          <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.databaseRisk.coreCompatibility') }}</dt>
          <dd class="mt-1 break-all font-mono text-slate-900 dark:text-zinc-100">
            {{ database.coreCompatibility || t('admin.extensions.trust.databaseRisk.notDeclared') }}
          </dd>
        </div>
        <div class="min-w-0">
          <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.databaseRisk.backup') }}</dt>
          <dd class="mt-1 flex flex-wrap items-center gap-2">
            <UBadge :color="database.backup.required ? 'warning' : 'neutral'" variant="subtle" size="xs">
              {{ database.backup.required
                ? t('admin.extensions.trust.databaseRisk.backupRequired')
                : t('admin.extensions.trust.databaseRisk.backupNotRequired') }}
            </UBadge>
            <span class="break-all font-mono text-slate-900 dark:text-zinc-100">
              {{ database.backup.strategy || t('admin.extensions.trust.databaseRisk.strategyNotDeclared') }}
            </span>
          </dd>
        </div>
        <div class="min-w-0">
          <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.databaseRisk.retention') }}</dt>
          <dd class="mt-1 space-y-1 text-slate-900 dark:text-zinc-100">
            <div>{{ t('admin.extensions.trust.databaseRisk.onDisable') }}: <span class="font-mono">{{ database.retention.onDisable }}</span></div>
            <div>
              {{ t('admin.extensions.trust.databaseRisk.onUninstall') }}:
              <span class="font-mono">{{ database.retention.onUninstall }}</span>
              <span v-if="database.retention.days"> · {{ t('admin.extensions.trust.databaseRisk.retentionDays', { count: database.retention.days }) }}</span>
            </div>
          </dd>
        </div>
      </dl>

      <div v-if="migrationRisk.hasMigrations" class="space-y-2">
        <h4 class="text-xs font-semibold text-slate-700 dark:text-zinc-300">
          {{ t('admin.extensions.trust.databaseRisk.migrationPlan', { count: impact.migrationDeclarations.length }) }}
        </h4>
        <ul class="grid gap-2 lg:grid-cols-2">
          <li
            v-for="(migration, index) in impact.migrationDeclarations"
            :key="migration.id || migration.path"
            class="min-w-0 rounded-md border border-slate-200 px-3 py-2.5 dark:border-zinc-700"
          >
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <p class="truncate text-xs font-medium text-slate-900 dark:text-zinc-100">{{ migrationLabel(migration, index) }}</p>
                <p class="mt-1 break-all font-mono text-[11px] text-slate-500 dark:text-zinc-400">{{ migration.path }}</p>
              </div>
              <UBadge
                :color="migrationTransaction(migration) === 'forbidden' ? 'error' : migrationTransaction(migration) === 'required' ? 'primary' : 'neutral'"
                variant="subtle"
                size="xs"
              >
                {{ t(`admin.extensions.trust.databaseRisk.transactions.${migrationTransaction(migration)}`) }}
              </UBadge>
            </div>
            <dl class="mt-2 grid gap-2 text-[11px] sm:grid-cols-2">
              <div class="min-w-0">
                <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.databaseRisk.contractVersion') }}</dt>
                <dd class="mt-0.5 break-all font-mono text-slate-700 dark:text-zinc-300">{{ migration.contractVersion || t('admin.extensions.trust.databaseRisk.notDeclared') }}</dd>
              </div>
              <div class="min-w-0">
                <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.databaseRisk.digest') }}</dt>
                <dd class="mt-0.5 break-all font-mono text-slate-700 dark:text-zinc-300">{{ migration.digest || t('admin.extensions.trust.databaseRisk.notDeclared') }}</dd>
              </div>
            </dl>
          </li>
        </ul>
      </div>
    </section>

    <section
      v-if="guardAuthorization.length"
      class="space-y-3 border-y border-slate-200 py-4 dark:border-zinc-800"
      data-testid="extension-guard-authorization"
    >
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex min-w-0 items-center gap-2">
          <UIcon name="i-lucide-shield-check" class="size-5 shrink-0 text-slate-500 dark:text-zinc-400" />
          <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
            {{ t('admin.extensions.trust.guardAuthorization.title') }}
          </h3>
        </div>
        <UBadge color="neutral" variant="subtle">{{ guardAuthorization.length }}</UBadge>
      </div>
      <UAlert
        v-if="rawRequestGuards.length"
        color="error"
        variant="subtle"
        icon="i-lucide-triangle-alert"
        :title="t('admin.extensions.trust.guardAuthorization.rawRequestTitle')"
        :description="t('admin.extensions.trust.guardAuthorization.rawRequestDescription', { count: rawRequestGuards.length })"
        data-testid="extension-guard-raw-request"
      />
      <ul class="grid gap-2 lg:grid-cols-2">
        <li
          v-for="row in guardAuthorization"
          :key="row.routeId"
          class="min-w-0 rounded-md border border-slate-200 px-3 py-2.5 dark:border-zinc-700"
        >
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <p class="truncate font-mono text-xs text-slate-900 dark:text-zinc-100">{{ guardScope(row) }}</p>
              <p class="mt-1 truncate text-[11px] text-slate-500 dark:text-zinc-400">{{ row.routeId }} · {{ row.routeAction }}</p>
            </div>
            <UBadge :color="guardRiskColor(row)" variant="subtle" size="xs">
              {{ t(`admin.extensions.trust.guardAuthorization.risks.${row.risk}`) }}
            </UBadge>
          </div>
          <dl class="mt-2 grid gap-2 text-[11px] sm:grid-cols-2">
            <div class="min-w-0">
              <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.guardAuthorization.owner') }}</dt>
              <dd class="mt-0.5 break-all font-mono text-slate-700 dark:text-zinc-300">{{ row.guardId }}</dd>
            </div>
            <div class="min-w-0">
              <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.guardAuthorization.kind') }}</dt>
              <dd class="mt-0.5 break-all font-mono text-slate-700 dark:text-zinc-300">
                {{ row.guardKind || t('admin.extensions.trust.guardAuthorization.hostOwned') }}
              </dd>
            </div>
            <div class="min-w-0">
              <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.guardAuthorization.digest') }}</dt>
              <dd class="mt-0.5 break-all font-mono text-slate-700 dark:text-zinc-300">
                {{ row.guardDigest || t('admin.extensions.trust.guardAuthorization.noArtifact') }}
              </dd>
            </div>
            <div class="min-w-0">
              <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.guardAuthorization.fallback') }}</dt>
              <dd class="mt-0.5 break-all font-mono text-slate-700 dark:text-zinc-300">
                {{ row.fallback || t('admin.extensions.trust.guardAuthorization.notDeclared') }}
              </dd>
            </div>
            <div class="min-w-0 sm:col-span-2">
              <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.trust.guardAuthorization.requestedFields') }}</dt>
              <dd class="mt-0.5 break-all font-mono text-slate-700 dark:text-zinc-300">
                {{ guardRequestedFields(row).length
                  ? guardRequestedFields(row).join(' · ')
                  : t('admin.extensions.trust.guardAuthorization.noRequestedFields') }}
              </dd>
            </div>
          </dl>
        </li>
      </ul>
    </section>

    <section
      v-if="capabilityRows.length"
      class="space-y-2 border-y border-slate-200 py-4 dark:border-zinc-800"
      data-testid="extension-capabilities"
    >
      <h3 class="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-zinc-100">
        <UIcon name="i-lucide-key-round" class="size-5 shrink-0 text-slate-500 dark:text-zinc-400" />
        {{ t('admin.extensions.trust.capabilities.title') }}
      </h3>
      <ul class="grid gap-2 lg:grid-cols-2">
        <li
          v-for="row in capabilityRows"
          :key="row.key"
          class="min-w-0 rounded-md border border-slate-200 px-3 py-2.5 dark:border-zinc-700"
        >
          <p class="truncate text-xs font-medium text-slate-900 dark:text-zinc-100">{{ capabilityLabel(row) }}</p>
          <p class="mt-1 break-all font-mono text-[11px] text-slate-500 dark:text-zinc-400">{{ row.key }}</p>
          <p
            v-if="capabilityDisclosure(row)"
            class="mt-1 text-[11px] leading-5 text-amber-600 dark:text-amber-400"
            data-capability-disclosure
          >
            {{ capabilityDisclosure(row) }}
          </p>
          <p v-else-if="row.description" class="mt-1 text-[11px] leading-5 text-slate-600 dark:text-zinc-300">
            {{ row.description }}
          </p>
        </li>
      </ul>
    </section>

    <!--
      可被信任等级授予的权限：与上面 capability 清单同风格（卡片网格 + key 小字），
      但**不渲染 description**（见 script 里的说明）。一条都没有时整段不渲染。
    -->
    <section
      v-if="trustLevelEarnableRows.length"
      class="space-y-2 border-y border-slate-200 py-4 dark:border-zinc-800"
      data-testid="extension-trust-level-earnable"
    >
      <h3 class="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-zinc-100">
        <UIcon name="i-lucide-trending-up" class="size-5 shrink-0 text-slate-500 dark:text-zinc-400" />
        {{ t('admin.extensions.trust.trustLevelEarnable.title') }}
      </h3>
      <ul class="grid gap-2 lg:grid-cols-2">
        <li
          v-for="row in trustLevelEarnableRows"
          :key="row.key"
          class="min-w-0 rounded-md border border-slate-200 px-3 py-2.5 dark:border-zinc-700"
          :data-trust-level-earnable-key="row.key"
        >
          <p class="truncate text-xs font-medium text-slate-900 dark:text-zinc-100">{{ row.label || row.key }}</p>
          <p class="mt-1 break-all font-mono text-[11px] text-slate-500 dark:text-zinc-400">{{ row.key }}</p>
        </li>
      </ul>
    </section>

    <div class="grid gap-2 sm:grid-cols-2">
      <details
        v-for="section in sections"
        :key="section.key"
        class="group min-w-0 rounded-md border border-slate-200 bg-white open:sm:col-span-2 dark:border-zinc-700 dark:bg-zinc-950"
      >
        <summary class="flex min-h-10 cursor-pointer list-none items-center justify-between gap-3 px-3 py-2 text-sm font-medium text-slate-800 dark:text-zinc-100">
          <span class="flex min-w-0 items-center gap-2">
            <UIcon name="i-lucide-chevron-right" class="size-4 shrink-0 transition-transform group-open:rotate-90" />
            <span class="truncate">{{ t(`admin.extensions.trust.sections.${section.key}`) }}</span>
          </span>
          <UBadge color="neutral" variant="subtle" size="xs">{{ itemCount(section.value) }}</UBadge>
        </summary>
        <pre class="max-h-56 overflow-auto border-t border-slate-200 p-3 text-[11px] leading-5 text-slate-700 dark:border-zinc-800 dark:text-zinc-300">{{ formatted(section.value) }}</pre>
      </details>
    </div>
  </div>
</template>
