import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { routeMethodIdentity } from '../scripts/v3-catalog/route-method-identity.mjs'

// 自定位到仓库根：不能依赖调用者的 cwd，否则 `cd apps/api && node ../../tests/...`
// 会让 runGenerator 的 cwd 指到 apps/api，子进程解析出 apps/api/scripts/... 而 MODULE_NOT_FOUND。
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')

function assert(condition, message) {
  if (!condition) throw new Error(message)
}

function load(path) {
  return JSON.parse(readFileSync(resolve(root, path), 'utf8'))
}

function runGenerator(...arguments_) {
  return spawnSync(process.execPath, ['scripts/v3-catalog/generate.mjs', ...arguments_], {
    cwd: root,
    encoding: 'utf8'
  })
}

function runGit(cwd, ...arguments_) {
  const result = spawnSync('git', arguments_, { cwd, encoding: 'utf8' })
  assert(result.status === 0, result.stderr || result.stdout || `git ${arguments_.join(' ')} failed`)
}

function unique(items, key, label) {
  const seen = new Set()
  for (const item of items) {
    const value = key(item)
    assert(value, `${label} contains an empty identity`)
    assert(!seen.has(value), `${label} contains duplicate ${value}`)
    seen.add(value)
  }
}

function normalizeRoutePath(...parts) {
  const value = parts.join('/').replaceAll(/\/+/g, '/')
  return value === '/' ? value : value.replace(/\/$/, '')
}

// 路由 source 的判据是 `path#symbol`，**不认行号**。
//
// 行号是脆的：任何在 RegisterRoutes 之前的 import 位移都会把整张表平移，让
// routes.json / routes.md / --check 三条门槛一起变红（c9da91ea7 就是一次纯
// import 位移的补行号提交）。符号是稳的。要守的不变量是「这个文件里仍有一条
// method+path 匹配、且挂在那个 handler 上的注册」：文件消失、符号改名、路径
// 改道都仍然红，import 位移不再红。内联 handler（server.go 的 /health、/ready）
// 没有可引用的符号，此时只记文件，并要求该文件里这条 method+path 恰好一条。
// 符号必须作为**完整标识符**出现在注册行上：`listDemo` 不能靠
// `listDemoRenamed` 蒙过去（子串匹配会让改名这类真实漂移漏网）。
function lineBindsSymbol(line, symbol) {
  const escaped = symbol.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return new RegExp(`(?<![A-Za-z0-9_])${escaped}(?![A-Za-z0-9_])`).test(line)
}

function routeSourceRegistrations(lines, route) {
  const routers = new Map([['api', '']])
  const registrations = []
  for (const line of lines) {
    const group = line.match(/(\w+)\s*:=\s*(\w+)\.Group\("([^"]*)"\)/)
    if (group && routers.has(group[2])) routers.set(group[1], normalizeRoutePath(routers.get(group[2]), group[3]))
    const registration = line.match(/(\w+)\.(Get|Post|Put|Patch|Delete|All)\("([^"]*)"/)
    if (!registration || !routers.has(registration[1])) continue
    const method = registration[2] === 'All' ? '*' : registration[2].toUpperCase()
    if (method !== route.method) continue
    if (normalizeRoutePath('/api/v1', routers.get(registration[1]), registration[3]) !== route.path) continue
    registrations.push(line)
  }
  return registrations
}

function validateRouteSource(route, baseRoot = root) {
  assert(!/:\d+$/.test(route.source), `${route.id} source still carries a line number: ${route.source}`)
  const separator = route.source.lastIndexOf('#')
  const source = separator > 0 ? route.source.slice(0, separator) : route.source
  const symbol = separator > 0 ? route.source.slice(separator + 1) : ''
  assert(source && !source.includes('#'), `${route.id} source path is invalid: ${route.source}`)
  assert(!symbol || /^[A-Za-z0-9_.]+$/.test(symbol), `${route.id} source symbol is invalid: ${symbol}`)
  const lines = readFileSync(resolve(baseRoot, source), 'utf8').split('\n')
  const registrations = routeSourceRegistrations(lines, route)
  assert(registrations.length > 0, `${route.id} source ${source} has no ${route.method} ${route.path} registration`)
  if (!symbol) {
    assert(
      registrations.length === 1,
      `${route.id} source ${source} matches ${registrations.length} ${route.method} ${route.path} registrations; bind a handler symbol instead`
    )
    return
  }
  // 同一个 method+path 允许出现多条注册（例如 Forum 的 POST /topics 有带/不带
  // 幂等中间件的两个分支），但它们必须**全部**挂在这个符号上：只要有一条注册
  // 换成了别的 handler，目录就说不清这条路由到底由谁服务，必须红。
  const unbound = registrations.filter(line => !lineBindsSymbol(line, symbol))
  assert(
    unbound.length === 0,
    `${route.id} source ${source} has a ${route.method} ${route.path} registration that does not bind ${symbol}: ${unbound[0]?.trim()}`
  )
}

// 自检：上面这套判据必须同时满足「对行号漂移免疫」和「真实漂移仍然红」。
// 用临时目录里的假 routes.go 跑同一批函数，不依赖仓库当前状态。
function assertRouteSourceJudgement() {
  const directory = mkdtempSync(join(tmpdir(), 'sforum-v3-route-source-'))
  const relative = 'apps/api/app/Http/Controllers/Demo/routes.go'
  const file = join(directory, relative)
  const manifest = (imports, handler, path) => [
    'package demo',
    '',
    'import (',
    ...imports.map(name => `\t"${name}"`),
    ')',
    '',
    'func RegisterRoutes(api Router) {',
    `\tapi.Get(${JSON.stringify(path)}, h.${handler})`,
    '}',
    ''
  ].join('\n')
  const route = { id: 'core.route.demo.get', method: 'GET', path: '/api/v1/demo', source: `${relative}#listDemo` }
  const expectPass = label => {
    try {
      validateRouteSource(route, directory)
    } catch (error) {
      throw new Error(`${label} should have passed but failed: ${error.message}`)
    }
  }
  const expectFailure = (label, fragment) => {
    let message = ''
    try {
      validateRouteSource(route, directory)
    } catch (error) {
      message = error.message
    }
    assert(message, `${label} should have failed but passed`)
    assert(message.includes(fragment), `${label} failed for the wrong reason: ${message}`)
  }
  try {
    mkdirSync(dirname(file), { recursive: true })
    writeFileSync(file, manifest(['fmt'], 'listDemo', '/demo'))
    expectPass('baseline registration')
    // 行号漂移：在 RegisterRoutes 之前插入 import 行，旧判据必红，新判据必绿。
    writeFileSync(file, manifest(['fmt', 'strings', 'errors', 'io', 'os'], 'listDemo', '/demo'))
    expectPass('import shift above RegisterRoutes')
    // 真实漂移①：handler 符号改名。
    writeFileSync(file, manifest(['fmt'], 'listDemoRenamed', '/demo'))
    expectFailure('renamed handler symbol', 'does not bind listDemo')
    // 真实漂移②：注册路径改道。
    writeFileSync(file, manifest(['fmt'], 'listDemo', '/demos'))
    expectFailure('re-pathed registration', 'has no GET /api/v1/demo registration')
    // 真实漂移③：源文件消失。
    rmSync(file)
    expectFailure('missing source file', 'ENOENT')
  } finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

for (const [method, identity] of [
  ['*', 'all'],
  ['DELETE', 'delete'],
  ['GET', 'get'],
  ['PATCH', 'patch'],
  ['POST', 'post'],
  ['PUT', 'put']
]) {
  assert(routeMethodIdentity(method) === identity, `${method} route method identity drifted`)
}
for (const method of ['', '**', 'GET*', 'get']) {
  let rejected = false
  try {
    routeMethodIdentity(method)
  } catch {
    rejected = true
  }
  assert(rejected, `unsupported route method ${JSON.stringify(method)} was accepted`)
}

const generated = runGenerator('--check')
assert(generated.status === 0, generated.stderr || generated.stdout || 'V3 catalog drift check failed')

const routes = load('docs/extensions/v3/catalogs/routes.json')
const identities = load('docs/extensions/v3/catalog-identities.json')
const retiredIdentities = load('docs/extensions/v3/catalog-retired-identities.json')

assertRouteSourceJudgement()

function assertIdentityMutationRejected(name, mutate, expected) {
  const directory = mkdtempSync(join(tmpdir(), 'sforum-v3-identities-'))
  try {
    const changed = structuredClone(identities)
    const changedRetired = structuredClone(retiredIdentities)
    mutate(changed, changedRetired)
    const identitiesPath = join(directory, 'catalog-identities.json')
    const retiredPath = join(directory, 'catalog-retired-identities.json')
    writeFileSync(identitiesPath, `${JSON.stringify(changed, null, 2)}\n`)
    writeFileSync(retiredPath, `${JSON.stringify(changedRetired, null, 2)}\n`)
    const result = runGenerator('--check', `--identities=${identitiesPath}`, `--retired-identities=${retiredPath}`)
    const output = `${result.stderr || ''}${result.stdout || ''}`
    assert(result.status !== 0, `${name} mutation unexpectedly passed generation`)
    assert(output.includes(expected), `${name} mutation failed for the wrong reason:\n${output}`)
  } finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

function assertIdentityMutationAccepted(name, mutate) {
  const directory = mkdtempSync(join(tmpdir(), 'sforum-v3-identities-'))
  try {
    const changed = structuredClone(identities)
    const changedRetired = structuredClone(retiredIdentities)
    mutate(changed, changedRetired)
    const identitiesPath = join(directory, 'catalog-identities.json')
    const retiredPath = join(directory, 'catalog-retired-identities.json')
    const retiredCatalogPath = join(directory, 'ui-retired-identities.json')
    writeFileSync(identitiesPath, `${JSON.stringify(changed, null, 2)}\n`)
    writeFileSync(retiredPath, `${JSON.stringify(changedRetired, null, 2)}\n`)
    writeFileSync(retiredCatalogPath, `${JSON.stringify(changedRetired, null, 2)}\n`)
    const result = runGenerator('--check', `--identities=${identitiesPath}`, `--retired-identities=${retiredPath}`, `--retired-catalog=${retiredCatalogPath}`)
    assert(result.status === 0, `${name} mutation failed:\n${result.stderr || result.stdout || ''}`)
  } finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

const retiredUIIdentity = {
  id: 'core.component.retired.legacy_banner',
  contractVersion: 'sforum.component.retired.legacy_banner@1',
  kind: 'component',
  owners: ['public'],
  state: 'retired',
  source: 'apps/web/app/components/LegacyBanner.vue'
}
const retiredUITombstone = {
  id: retiredUIIdentity.id,
  contractVersion: retiredUIIdentity.contractVersion
}

function assertRetiredIdentityDeletionDetected() {
  const directory = mkdtempSync(join(tmpdir(), 'sforum-v3-retired-deletion-'))
  try {
    const retiredDirectory = join(directory, 'docs/extensions/v3')
    const catalogDirectory = join(retiredDirectory, 'catalogs')
    const retiredPath = join(retiredDirectory, 'catalog-retired-identities.json')
    const retiredCatalogPath = join(catalogDirectory, 'ui-retired-identities.json')
    const withTombstone = { schemaVersion: 1, ui: [retiredUITombstone] }
    mkdirSync(catalogDirectory, { recursive: true })
    runGit(directory, 'init', '--quiet')
    runGit(directory, 'config', 'user.name', 'SForum Catalog Test')
    runGit(directory, 'config', 'user.email', 'catalog-test@sforum.invalid')
    writeFileSync(retiredPath, `${JSON.stringify(withTombstone, null, 2)}\n`)
    writeFileSync(retiredCatalogPath, `${JSON.stringify(withTombstone, null, 2)}\n`)
    runGit(directory, 'add', '.')
    runGit(directory, 'commit', '--quiet', '-m', 'record retired component')
    writeFileSync(retiredPath, `${JSON.stringify(retiredIdentities, null, 2)}\n`)
    writeFileSync(retiredCatalogPath, `${JSON.stringify(retiredIdentities, null, 2)}\n`)
    runGit(directory, 'add', '.')
    runGit(directory, 'commit', '--quiet', '-m', 'delete and regenerate retired catalogs')

    const result = runGenerator('--check', `--retired-identities=${retiredPath}`, `--retired-catalog=${retiredCatalogPath}`, `--retired-history-root=${directory}`)
    const output = `${result.stderr || ''}${result.stdout || ''}`
    assert(result.status !== 0 && output.includes('append-only retired UI tombstone'), `retired identity deletion plus regeneration passed:\n${output}`)
  } finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

assert(identities.schemaVersion === 2, 'reviewed identity map must use explicit UI ownership schema v2')
assert(retiredIdentities.schemaVersion === 1 && Array.isArray(retiredIdentities.ui), 'reviewed retired identity ledger has an invalid schema')
assertIdentityMutationAccepted('retired UI exclusion', (changed, changedRetired) => {
  changed.ui.push(retiredUIIdentity)
  changedRetired.ui.push(retiredUITombstone)
})
assertRetiredIdentityDeletionDetected()
assertIdentityMutationRejected('UI id collision', changed => {
  changed.ui[1].id = changed.ui[0].id
}, 'duplicate core.component')
assertIdentityMutationRejected('retired UI id reuse', (changed, changedRetired) => {
  changedRetired.ui.push(retiredUITombstone)
  changed.ui[changed.ui.findIndex(item => item.state === 'active')].id = retiredUIIdentity.id
}, 'reuses a retired id')
assertIdentityMutationRejected('retired UI contract reuse', (changed, changedRetired) => {
  changedRetired.ui.push(retiredUITombstone)
  changed.ui[changed.ui.findIndex(item => item.state === 'active')].contractVersion = retiredUIIdentity.contractVersion
}, 'reuses retired contract')
assertIdentityMutationRejected('UI owner drift', changed => {
  const item = changed.ui.find(candidate => candidate.id === 'core.component.admin.sfadmin_footer')
  item.owners = ['public']
}, 'ownership drift')
assertIdentityMutationRejected('UI source drift', changed => {
  changed.ui[changed.ui.findIndex(item => item.state === 'active')].source = 'apps/web/app/components/Moved.vue'
}, 'new or moved UI surface')

// 路由清单的规模判据：与稳定身份账本双向一致，且不少于上次审阅值。
//
// 这里原本写死 `=== 370`。该断言落地当天（44c7c255d）routes.json 已经是 371 条，
// 之后 GROW 到 372、373——也就是说它从落地起就是红的，而「把数字往上抬一次」只是
// 把同一笔债推给下一个改路由的人，判据本身仍然不成立。真正要守的不变量有两条：
//   1. 生成器产出的每一条路由都有审阅过的稳定身份，且反向也成立（双向、逐条）；
//   2. 清单规模不减少（路由被删=能力消失，必须由人显式下调下面的下限）。
// 具体条数不再是判据；`generate.mjs --check` 在上方已经保证了清单与源码一致。
const REVIEWED_ROUTE_COUNT_FLOOR = 373
assert(
  routes.length >= REVIEWED_ROUTE_COUNT_FLOOR,
  `route inventory shrank below the last reviewed count ${REVIEWED_ROUTE_COUNT_FLOOR}: ${routes.length}`
)
assert(identities.routes.length === routes.length, 'reviewed route identity map must cover every current route')
const reviewedRoutes = new Map(identities.routes.map(item => [`${item.method} ${item.path}`, item]))
for (const route of routes) {
  const key = `${route.method} ${route.path}`
  const identity = reviewedRoutes.get(key)
  assert(identity, `${route.id} has no reviewed stable identity for ${key}`)
  assert(identity.id === route.id, `${key} stable identity id drifted: reviewed ${identity.id}, generated ${route.id}`)
  assert(
    identity.contractVersion === route.contractVersion,
    `${key} stable identity contract drifted: reviewed ${identity.contractVersion}, generated ${route.contractVersion}`
  )
}
const generatedRouteKeys = new Set(routes.map(route => `${route.method} ${route.path}`))
for (const identity of identities.routes) {
  assert(
    generatedRouteKeys.has(`${identity.method} ${identity.path}`),
    `reviewed stable identity ${identity.id} has no generated route (${identity.method} ${identity.path}); retire it instead of leaving a stale entry`
  )
}
unique(routes, item => item.id, 'route inventory ids')
unique(routes, item => `${item.method} ${item.path}`, 'route inventory method/path pairs')
for (const route of routes) {
  assert(/^core\.route\.[a-z0-9_.]+$/.test(route.id), `invalid route id ${route.id}`)
  assert(/^sforum\.route\.[a-z0-9_.]+@1$/.test(route.contractVersion), `invalid route contract ${route.contractVersion}`)
  assert(route.access && route.policy, `${route.id} must record access and policy`)
  assert(route.guard && ['public', 'login', 'guest', 'super_admin', 'permission_any', 'contextual'].includes(route.guard.kind), `${route.id} has no typed reviewed guard`)
  assert(Array.isArray(route.guard.permissions), `${route.id} guard permissions must be explicit`)
  if (route.guard.kind === 'permission_any') assert(route.guard.permissions.length > 0 && !route.guard.evaluatorId, `${route.id} permission guard is incomplete`)
  if (route.guard.kind === 'contextual') assert(/^core\.guard\.[a-z0-9_.]+$/.test(route.guard.evaluatorId), `${route.id} contextual evaluator is missing`)
  if (!['permission_any', 'contextual'].includes(route.guard.kind)) assert(route.guard.permissions.length === 0 && !route.guard.evaluatorId, `${route.id} metadata guard has contextual fields`)
  validateRouteSource(route)
}

const frontendTrustPolicies = new Map([
  ['core.route.extensions.frontend_confirmation', 'active super_admin challenge issuance'],
  ['core.route.extensions.grant_frontend_trust', 'active super_admin plus actor-bound exact frontend artifact confirmation'],
  ['core.route.extensions.revoke_frontend_trust', 'active super_admin trust revocation']
])
for (const [id, policy] of frontendTrustPolicies) {
  const route = routes.find(item => item.id === id)
  assert(route?.policy === policy, `${id} frontend trust policy drifted`)
  assert(route.guard.kind === 'super_admin', `${id} must retain its typed super_admin guard`)
}
const exactFrontendConfirmationRoutes = routes.filter(route => route.policy.includes('exact frontend artifact confirmation'))
assert(exactFrontendConfirmationRoutes.length === 1 && exactFrontendConfirmationRoutes[0].id === 'core.route.extensions.grant_frontend_trust', 'only frontend trust grant may claim exact artifact confirmation')

const ui = load('docs/extensions/v3/catalogs/ui-surfaces.json')
const retiredUI = load('docs/extensions/v3/catalogs/ui-retired-identities.json')
// Reviewed email-verification, mobile user-drawer, and user-identity-name
// identities expand the stable surface catalog without changing public/admin
// ownership semantics. The two outbound-link surfaces (`site.outbound` page and
// `core.component.shared.sfoutbound_interstitial`) were reviewed and mapped in
// docs/extensions/v3/catalog-identities.json in the same change.
//
// 规模判据与路由清单同一套（见上方 REVIEWED_ROUTE_COUNT_FLOOR）：这里原本写死
// `=== 322`，而该断言落地时 ui-surfaces.json 已经是 327 条，之后 GROW 到 329——
// 与路由那条一样，从落地起就是红的。要守的不变量是两条：每一条 UI 面都有审阅过的
// 稳定身份（反向也成立），且规模不减少。具体条数不再是判据。
const REVIEWED_UI_SURFACE_COUNT_FLOOR = 329
assert(
  ui.length >= REVIEWED_UI_SURFACE_COUNT_FLOOR,
  `UI inventory shrank below the last reviewed count ${REVIEWED_UI_SURFACE_COUNT_FLOOR}: ${ui.length}`
)
assert(JSON.stringify(retiredUI) === JSON.stringify(retiredIdentities), 'generated retired UI reservation catalog drifted from its reviewed ledger')
const activeUIIdentities = identities.ui.filter(item => item.state === 'active')
assert(activeUIIdentities.length === ui.length, 'active reviewed UI identity map must cover every current UI surface')
// 反向：账本里每一条 active 身份都必须有对应的现存 UI 面。等数 + 唯一 source 其实
// 已经蕴含这一点，但显式写出来才能在「删了一个组件、忘了退账本」时给出可读的原因，
// 而不是一条「数量对不上」。
const generatedUISources = new Set(ui.map(item => item.source))
for (const identity of activeUIIdentities) {
  assert(
    generatedUISources.has(identity.source),
    `reviewed UI identity ${identity.id} has no generated surface (${identity.source}); move it to the retired ledger instead of leaving a stale entry`
  )
}
unique(ui, item => item.id, 'UI surface ids')
unique(ui, item => item.contractVersion, 'UI surface contracts')
unique(ui, item => item.source, 'UI surface sources')
unique(identities.ui, item => item.id, 'reviewed UI ids')
unique(identities.ui, item => item.contractVersion, 'reviewed UI contracts')
unique(retiredIdentities.ui, item => item.id, 'retired UI tombstone ids')
unique(retiredIdentities.ui, item => item.contractVersion, 'retired UI tombstone contracts')
unique(activeUIIdentities, item => item.source, 'active reviewed UI sources')
for (const item of identities.ui) assert(['active', 'retired'].includes(item.state), `${item.id} has invalid identity state`)
const reviewedUI = new Map(activeUIIdentities.map(item => [item.source, item]))
const ownershipCounts = { public: 0, admin: 0, shared: 0 }
for (const item of ui) {
  assert(/^core\.component\.[a-z0-9_.-]+$/.test(item.id), `invalid component id ${item.id}`)
  assert(/^sforum\.component\.[a-z0-9_.-]+@1$/.test(item.contractVersion), `${item.id} has no v1 contract`)
  assert(['page', 'component'].includes(item.kind), `${item.id} has invalid kind ${item.kind}`)
  assert(Array.isArray(item.owners) && item.owners.length > 0, `${item.id} has no explicit owner`)
  assert(item.owners.every(owner => ['public', 'admin'].includes(owner)), `${item.id} has an invalid owner`)
  assert(JSON.stringify(item.owners) === JSON.stringify(['public', 'admin'].filter(owner => item.owners.includes(owner))), `${item.id} owners are not canonical`)
  if (item.kind === 'page') assert(item.owners.length === 1 && item.route, `${item.id} page ownership/route is incomplete`)
  else assert(!item.route, `${item.id} component unexpectedly owns a page route`)
  assert(existsSync(resolve(root, item.source)), `${item.id} source is missing`)
  const reviewed = reviewedUI.get(item.source)
  assert(reviewed && reviewed.id === item.id && reviewed.contractVersion === item.contractVersion, `${item.id} drifted from its reviewed identity`)
  assert(reviewed.kind === item.kind && JSON.stringify(reviewed.owners) === JSON.stringify(item.owners), `${item.id} kind/ownership drifted from review`)
  if (item.owners.includes('public')) ownershipCounts.public++
  if (item.owners.includes('admin')) ownershipCounts.admin++
  if (item.owners.length === 2) ownershipCounts.shared++
}
assert(ownershipCounts.public > 0 && ownershipCounts.admin > 0 && ownershipCounts.shared > 0, 'public/admin/shared ownership classes must all be represented')

const backend = load('docs/extensions/v3/catalogs/backend-surfaces.json')
for (const key of ['events', 'contributionPoints', 'providerSlots', 'schedules', 'jobKinds', 'cache', 'content', 'data']) {
  assert(Array.isArray(backend[key]) && backend[key].length > 0, `backend inventory ${key} is empty`)
}

const matrix = load('docs/extensions/v3/extension-surface-matrix.json')
const families = ['routes', 'hooks', 'queries', 'adminComponents', 'publicComponents', 'identityPermissions', 'media', 'navigationRegions', 'cacheInvalidation', 'jobs', 'lifecycle']
const requiredModules = ['system', 'systemUpdates', 'adminOverview', 'extensions', 'identity', 'forum', 'profile', 'attachments', 'database', 'entityMeta', 'options', 'siteChrome', 'jobs', 'moderation', 'mail', 'notifications', 'search', 'seo', 'webhooks', 'localization']
for (const module of requiredModules) assert(matrix[module], `Extension Surface Matrix is missing core module ${module}`)
assert(Object.keys(matrix).length === requiredModules.length, 'Extension Surface Matrix contains an unreviewed aggregate or unknown module')
for (const [module, values] of Object.entries(matrix)) {
  for (const family of families) {
    const value = values[family]
    assert(value, `${module} is missing matrix family ${family}`)
    assert(['open', 'planned', 'closed'].includes(value.state), `${module}.${family} has invalid state`)
    if (value.state === 'closed') assert(value.reason, `${module}.${family} is closed without a reason`)
    else assert(value.phase && value.phase.startsWith('P'), `${module}.${family} is open/planned without a phase`)
  }
}

const traceability = readFileSync(resolve(root, 'knowledge/plans/2026-07-13-trusted-plugin-theme-platform-v3-traceability.md'), 'utf8')
const traceRows = traceability.split('\n').filter(line => /^\| \d+ \| (theme|plugin) \|/.test(line))
assert(traceRows.length === 99, `traceability must contain exactly 99 rows, found ${traceRows.length}`)
assert(traceRows.filter(line => line.includes('| theme |')).length === 27, 'traceability must contain 27 theme rows')
assert(traceRows.filter(line => line.includes('| plugin |')).length === 72, 'traceability must contain 72 plugin rows')

for (const path of [
  'docs/extensions/v3/README.md',
  'docs/extensions/v3/governance.md',
  'docs/extensions/v3/performance-baseline.md'
]) {
  assert(existsSync(resolve(root, path)), `missing reviewed V3 governance artifact ${path}`)
}

console.log(`V3 P0 catalogs validated: ${routes.length} routes, ${ui.length} UI surfaces, 99 traceability rows.`)
