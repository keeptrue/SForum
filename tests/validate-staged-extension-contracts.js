const fs = require('fs')
const path = require('path')

const root = path.resolve(__dirname, '..')

function read(relativePath) {
  return fs.readFileSync(path.join(root, relativePath), 'utf8')
}

function assert(condition, message) {
  if (!condition) throw new Error(message)
}

function section(source, start, end) {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert(startIndex >= 0 && endIndex > startIndex, `missing contract section ${start}`)
  return source.slice(startIndex, endIndex)
}

const schemas = read('contracts/openapi/schemas/extensions.yaml')
const versionSchema = section(schemas, 'ExtensionVersion:\n', 'Extension:\n')
for (const field of ['version', 'manifest', 'packageDigest', 'adminFrontendDigest', 'packagePath', 'installedAt']) {
  assert(versionSchema.includes(`  - ${field}\n`), `ExtensionVersion must require ${field}`)
  assert(versionSchema.includes(`    ${field}:\n`), `ExtensionVersion must expose ${field}`)
}
assert(!versionSchema.includes('    id:\n'), 'ExtensionVersion must not expose its database id')

const extensionSchema = section(schemas, '\nExtension:\n', '\nExtensionInstallResult:\n')
assert(extensionSchema.includes('    stagedVersion:\n      "$ref": "#/ExtensionVersion"'), 'Extension must expose stagedVersion through ExtensionVersion')

const installResultSchema = section(schemas, '\nExtensionInstallResult:\n', '\nExtensionMigrationRecord:\n')
assert(installResultSchema.includes('    activationPending:\n'), 'InstallResult must expose activationPending')
assert(installResultSchema.includes('Static package\n        upload and staging preserve grants'), 'trustRevoked must preserve active grants during static staging')
assert(installResultSchema.includes('Static package upload and staging preserve active status'), 'requiredReEnable must preserve active status during static staging')

const paths = read('contracts/openapi/paths/extensions.yaml')
assert(paths.includes('stores an immutable upgrade candidate without changing the active package'), 'upload contract must describe inert candidate staging')
assert(paths.includes('InstallResult (extension plus activationPending metadata)'), 'upload response must describe activationPending')
const enablePath = section(paths, 'adminExtensionEnable:\n', 'adminExtensionTrust:\n')
const disablePath = section(paths, 'adminExtensionDisable:\n', 'adminExtensionUpgrade:\n')
const upgradePath = section(paths, 'adminExtensionUpgrade:\n', 'adminExtensionRollback:\n')
const rollbackPath = section(paths, 'adminExtensionRollback:\n', 'adminExtensionVerify:\n')
for (const [name, contract] of Object.entries({ enablePath, disablePath, upgradePath, rollbackPath })) {
  assert(contract.includes('../components/parameters.yaml#/IdempotencyKey'), `${name} must accept Idempotency-Key`)
}
assert(upgradePath.includes('operationId: upgradeAdminExtension'), 'OpenAPI must expose the staged upgrade operation')
assert(rollbackPath.includes('operationId: rollbackAdminExtension'), 'OpenAPI must expose the exact rollback operation')
assert(rollbackPath.includes('required: [targetVersion, targetPackageDigest]'), 'rollback must bind exact version and digest')

const openapi = read('contracts/openapi.yaml')
assert(openapi.includes('/admin/extensions/{id}/upgrade'), 'OpenAPI index must expose upgrade')
assert(openapi.includes('/admin/extensions/{id}/rollback'), 'OpenAPI index must expose rollback')

const types = read('apps/web/app/utils/admin/adminExtensions.ts')
assert(types.includes('export type AdminExtensionVersion = {'), 'web types must define AdminExtensionVersion')
assert(types.includes('stagedVersion?: AdminExtensionVersion'), 'AdminExtension must expose stagedVersion')

const manager = read('apps/web/app/composables/admin/useAdminExtensionsManager.ts')
const uninstallCleanup = read('apps/web/app/composables/admin/useAdminUninstallCleanup.ts')
const upgradeFlow = read('apps/web/app/composables/admin/useAdminExtensionUpgradeFlow.ts')
const lifecyclePresentation = read('apps/web/app/utils/admin/extensions/lifecyclePresentation.ts')
assert(types.includes('activationPending?: boolean'), 'upload result type must expose activationPending')
assert(manager.includes("t('admin.extensions.upgradeStagedHint')"), 'staged upload toast must explain pending activation')
assert(manager.includes("headers: { 'Idempotency-Key': idempotencyKey }"), 'lifecycle requests must send Idempotency-Key')
// 卸载清理收敛抽出后，幂等键的 UUID 生成在聚焦协作对象里；两处共同守住
// 「每个生命周期操作一个 UUID 幂等键」的契约。
assert(manager.includes('globalThis.crypto.randomUUID()') || uninstallCleanup.includes('globalThis.crypto.randomUUID()'), 'each lifecycle operation must use a UUID idempotency key')
assert(uninstallCleanup.includes("headers: { 'Idempotency-Key': uninstallKeys.get(item.id) || lifecycleIdempotencyKey() }"), 'uninstall retries must reuse the persisted idempotency key')
assert(upgradeFlow.includes('/admin/extensions/${item.id}/${action}'), 'staged candidates must choose the native upgrade or restart bridge lifecycle operation')
assert(upgradeFlow.includes('extensionUpgradeLifecycleAction(item)'), 'upgrade flow must preserve the Host legacy-to-V2 restart bridge')
assert(upgradeFlow.includes('globalThis.crypto.randomUUID()'), 'single and bulk upgrades must use per-operation UUID idempotency keys')
assert(upgradeFlow.includes("executableTrustPath(item, 'upgrade')"), 'upgrade must review the exact staged artifact before activation')
for (const action of ["'enable'", "'disable'", "'upgrade'", "'rollback'"]) {
  assert(lifecyclePresentation.includes(action), `frontend lifecycle helper must cover V2 operation ${action}`)
}

const overview = read('apps/web/app/pages/admin/extensions/index.vue')
assert(overview.includes('SFAdminExtensionPrimaryAction'), 'extension list must expose a context primary action')
assert(overview.includes("t('admin.extensions.update.newVersion')"), 'extension details must identify the downloaded target version')
assert(!overview.includes('stagedVersionBadge'), 'extension list must not keep the legacy staged badge')
assert(overview.includes("t('admin.extensions.upgradeAll'"), 'extension overview must expose bulk plugin upgrade')
assert(overview.includes('purpose="upgrade"'), 'extension overview must reuse exact-artifact trust review for upgrade')

for (const localeName of ['zh-CN', 'en-US']) {
  const locale = JSON.parse(read(`apps/web/i18n/locales/${localeName}.json`))
  const messages = locale.admin?.extensions
  assert(messages?.upgradeStaged && messages?.upgradeStagedHint, `${localeName} must describe the staged upload result`)
  assert(messages?.update?.available && messages?.update?.restartToApply && messages?.update?.activateToApply, `${localeName} must describe the three update states`)
  assert(!messages?.stagedVersion && !messages?.stagedVersionBadge && !messages?.stagedVersionHint, `${localeName} must drop the legacy staged-version copy`)
  assert(!messages?.upgradeUnavailable, `${localeName} must drop the legacy upgrade-unavailable copy`)
  assert(messages?.update?.upgradeNow && messages?.update?.onlyWithUpdates, `${localeName} must label the update actions`)
  assert(!messages?.upgrade, `${localeName} must drop the legacy single-upgrade label`)
  assert(messages?.upgradeAll && messages?.upgradeAllSuccess, `${localeName} must label bulk upgrade actions`)
}

console.log('Staged extension management contract validation passed.')
