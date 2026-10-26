import {
  existsSync,
  readFileSync,
  readdirSync
} from 'node:fs'
import { extname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('..', import.meta.url)))
const baselinePath = resolve(root, 'tests/architecture-boundaries-baseline.json')
const baseline = JSON.parse(readFileSync(baselinePath, 'utf8'))
const failures = []

function repoPath(path) {
  return resolve(root, path)
}

function normalizePath(path) {
  return path.replaceAll('\\', '/')
}

function read(path) {
  return readFileSync(repoPath(path), 'utf8')
}

function lineCount(content) {
  if (!content) return 0
  const normalized = content.replaceAll('\r\n', '\n')
  return normalized.split('\n').length - (normalized.endsWith('\n') ? 1 : 0)
}

function walkFiles(directory) {
  const absolute = repoPath(directory)
  if (!existsSync(absolute)) return []

  const files = []
  for (const entry of readdirSync(absolute, { withFileTypes: true })) {
    const child = join(absolute, entry.name)
    if (entry.isDirectory()) {
      files.push(...walkFiles(normalizePath(relative(root, child))))
    } else if (entry.isFile()) {
      files.push(normalizePath(relative(root, child)))
    }
  }
  return files
}

function directFiles(directory) {
  const absolute = repoPath(directory)
  if (!existsSync(absolute)) return []
  return readdirSync(absolute, { withFileTypes: true })
    .filter(entry => entry.isFile())
    .map(entry => normalizePath(join(directory, entry.name)))
}

function isGenerated(path, content) {
  return path.includes('/gen/') ||
    path.includes('/database/sqlc/') ||
    path.endsWith('.pb.go') ||
    content.slice(0, 512).includes('Code generated')
}

function shouldScanProductionSource(path) {
  if (path.startsWith('apps/api/')) {
    return path.endsWith('.go') &&
      !path.endsWith('_test.go') &&
      !path.includes('/testdata/')
  }
  if (path.startsWith('apps/web/app/')) {
    return path.endsWith('.vue') || path.endsWith('.ts')
  }
  return false
}

const productionFiles = [
  ...walkFiles('apps/api'),
  ...walkFiles('apps/web/app')
].filter(shouldScanProductionSource)

const legacyExtensionsImport = 'github.com/keeptrue/sforum/apps/api/app/Support/Extensions'
const goProductionFiles = productionFiles.filter(path => path.endsWith('.go'))
const legacyExtensionsImporters = goProductionFiles
  .filter(path => read(path).includes(`"${legacyExtensionsImport}"`))
  .sort()
const allowedLegacyImporters = [...(baseline.legacyExtensionsImportAllowlist || [])].sort()
const unexpectedLegacyImporters = legacyExtensionsImporters
  .filter(path => !allowedLegacyImporters.includes(path))
const staleLegacyImporters = allowedLegacyImporters
  .filter(path => !legacyExtensionsImporters.includes(path))
if (unexpectedLegacyImporters.length > 0) {
  failures.push(
    `new production imports of legacy Support/Extensions: ${unexpectedLegacyImporters.join(', ')}; depend on a stable Extension* package or a consumer-owned interface`
  )
}
if (staleLegacyImporters.length > 0) {
  failures.push(
    `legacy Support/Extensions import allowlist has stale entries: ${staleLegacyImporters.join(', ')}; remove reclaimed compatibility paths`
  )
}

const pluginV2LegacyImports = goProductionFiles
  .filter(path => path.startsWith('apps/api/sdk/plugin/v2/'))
  .filter(path => read(path).includes(`"${legacyExtensionsImport}"`))
if (pluginV2LegacyImports.length > 0) {
  failures.push(
    `plugin Protocol V2 SDK imports the legacy Host runtime: ${pluginV2LegacyImports.join(', ')}; keep dependency direction Host -> SDK`
  )
}

const lightweightStorageSDKFiles = goProductionFiles
  .filter(path => path.startsWith('apps/api/sdk/plugin/storageprovider/'))
const lightweightStorageSDKInvalidImports = lightweightStorageSDKFiles
  .filter(path => {
    const source = read(path)
    return source.includes(`"${legacyExtensionsImport}"`) ||
      source.includes('"github.com/keeptrue/sforum/apps/api/sdk/plugin"')
  })
if (lightweightStorageSDKInvalidImports.length > 0) {
  failures.push(
    `lightweight storage SDK imports the legacy Host or root plugin SDK: ${lightweightStorageSDKInvalidImports.join(', ')}`
  )
}

for (const path of goProductionFiles.filter(path => path.startsWith('apps/api/app/Models/'))) {
  if (read(path).includes(`"${legacyExtensionsImport}"`)) {
    failures.push(`${path} imports concrete legacy Support/Extensions; Models must depend on stable or consumer-owned interfaces`)
  }
}

const stableExtensionPackages = [
  'apps/api/app/Support/ExtensionRuntime',
  'apps/api/app/Support/ExtensionProtocol',
  'apps/api/app/Support/ExtensionDatabase',
  'apps/api/app/Support/ExtensionComposition'
]
const forbiddenStableImports = [
  legacyExtensionsImport,
  'github.com/keeptrue/sforum/apps/api/app/Models/',
  'github.com/keeptrue/sforum/apps/api/app/Http/',
  'github.com/keeptrue/sforum/apps/api/bootstrap'
]
for (const directory of stableExtensionPackages) {
  const files = goProductionFiles.filter(path => path.startsWith(`${directory}/`))
  if (files.length === 0) {
    failures.push(`${directory} has no production implementation or contract files`)
  }
  for (const path of files) {
    const content = read(path)
    for (const forbidden of forbiddenStableImports) {
      if (content.includes(`"${forbidden}`)) {
        failures.push(`${path} imports forbidden higher-level or legacy package ${forbidden}`)
      }
    }
  }
}

let reviewSizeFileCount = 0
for (const path of productionFiles) {
  const content = read(path)
  if (isGenerated(path, content)) continue

  const lines = lineCount(content)
  const legacyCap = baseline.legacyLargeFiles[path]
  if (lines > 500) reviewSizeFileCount += 1

  if (legacyCap !== undefined) {
    if (lines > legacyCap) {
      failures.push(
        `${path} grew from its legacy cap ${legacyCap} to ${lines} lines; split responsibilities instead`
      )
    } else if (lines < legacyCap) {
      failures.push(
        `${path} shrank from ${legacyCap} to ${lines} lines; lower or remove its legacy baseline in the same change`
      )
    }
    continue
  }

  if (lines > 1000) {
    failures.push(
      `${path} has ${lines} lines; new handwritten production files must stay at or below 1000`
    )
  }
}

for (const rule of baseline.rootDirectoryAllowlists || []) {
  const actual = directFiles(rule.path)
    .map(path => path.slice(rule.path.length + 1))
    .sort()
  const allowed = [...rule.files].sort()
  const unexpected = actual.filter(path => !allowed.includes(path))
  const missing = allowed.filter(path => !actual.includes(path))
  if (unexpected.length > 0) {
    failures.push(
      `${rule.path} has unapproved root files: ${unexpected.join(', ')}; place product code in a domain subdirectory`
    )
  }
  if (missing.length > 0) {
    failures.push(
      `${rule.path} root allowlist contains missing files: ${missing.join(', ')}; remove stale allowlist entries`
    )
  }
}

for (const rule of baseline.testPackageRootAllowlists || []) {
  const packagePrefix = `package ${rule.package}\n`
  const actual = directFiles(rule.path)
    .filter(path => path.endsWith('_test.go') && read(path).startsWith(packagePrefix))
    .map(path => path.slice(rule.path.length + 1))
    .sort()
  const allowed = [...rule.files].sort()
  const unexpected = actual.filter(path => !allowed.includes(path))
  const missing = allowed.filter(path => !actual.includes(path))
  if (unexpected.length > 0) {
    failures.push(
      `${rule.path} has unapproved ${rule.package} root tests: ${unexpected.join(', ')}; place black-box tests in their focused test package`
    )
  }
  if (missing.length > 0) {
    failures.push(
      `${rule.path} ${rule.package} root-test allowlist contains missing files: ${missing.join(', ')}; remove stale allowlist entries`
    )
  }
}

for (const rule of baseline.flatDirectoryCaps || []) {
  const matching = directFiles(rule.path).filter((path) => {
    if (!rule.extensions.includes(extname(path))) return false
    return !(rule.excludeSuffixes || []).some(suffix => path.endsWith(suffix))
  })
  if (matching.length > rule.maxFiles) {
    failures.push(
      `${rule.path} has ${matching.length} direct files (cap ${rule.maxFiles}); place new product code in a domain subdirectory/package`
    )
  } else if (matching.length < rule.maxFiles) {
    failures.push(
      `${rule.path} dropped to ${matching.length} direct files; lower its flat-directory baseline from ${rule.maxFiles}`
    )
  }
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

// Nuxt 会给嵌套目录生成带路径前缀的自动组件名。产品组件归位后，
// 调用方必须显式导入原有名称，避免模板静默退化为未解析的自定义元素。
const nestedComponentFiles = walkFiles('apps/web/app/components')
  .filter(path => path.endsWith('.vue') && relative(
    repoPath('apps/web/app/components'),
    repoPath(path)
  ).includes('/'))
const nestedComponents = new Map()
for (const path of nestedComponentFiles) {
  const name = path.slice(path.lastIndexOf('/') + 1, -'.vue'.length)
  const existing = nestedComponents.get(name)
  if (existing) {
    failures.push(
      `nested component basename ${name} is ambiguous between ${existing} and ${path}`
    )
  } else {
    nestedComponents.set(name, path)
  }
}
for (const path of productionFiles.filter(path => path.endsWith('.vue'))) {
  const content = read(path)
  for (const [name, componentPath] of nestedComponents) {
    if (path === componentPath) continue
    const componentTag = new RegExp(`<${escapeRegExp(name)}(?:\\s|/|>)`)
    if (!componentTag.test(content)) continue
    const runtimeImport = new RegExp(
      `import\\s+(?!type\\b)(?:${escapeRegExp(name)}\\b|\\{[^}]*\\b${escapeRegExp(name)}\\b[^}]*\\})`
    )
    if (!runtimeImport.test(content)) {
      failures.push(
        `${path} uses nested component ${name} without an explicit runtime import`
      )
    }
  }
}

for (const rule of baseline.receiverMethodCaps) {
  const receiver = escapeRegExp(rule.type)
  const methodPattern = new RegExp(
    `^func\\s+\\(\\s*[A-Za-z_][A-Za-z0-9_]*\\s+\\*?${receiver}\\s*\\)`,
    'gm'
  )
  let methods = 0
  for (const path of directFiles(rule.path)) {
    if (!path.endsWith('.go') || path.endsWith('_test.go')) continue
    methods += read(path).match(methodPattern)?.length || 0
  }
  if (methods > rule.maxMethods) {
    failures.push(
      `${rule.path} ${rule.type} has ${methods} receiver methods (cap ${rule.maxMethods}); add a focused collaborator instead`
    )
  } else if (methods < rule.maxMethods) {
    failures.push(
      `${rule.path} ${rule.type} dropped to ${methods} receiver methods; lower its baseline from ${rule.maxMethods}`
    )
  }
}

function literalTabBranches(content) {
  const values = new Set()
  const pattern = /\b(?:activeTab|activeView|section)\s*===\s*['"]([A-Za-z0-9_-]+)['"]/g
  for (const match of content.matchAll(pattern)) values.add(match[1])
  return values
}

for (const [path, rule] of Object.entries(baseline.legacyInlineAdminTabPages)) {
  if (!existsSync(repoPath(path))) continue
  const content = read(path)
  const lines = lineCount(content)
  const branches = literalTabBranches(content).size
  if (lines > rule.maxLines) {
    failures.push(
      `${path} is a legacy inline-tab page and grew from ${rule.maxLines} to ${lines} lines; extract a tab component`
    )
  } else if (lines < rule.maxLines) {
    failures.push(
      `${path} shrank from ${rule.maxLines} to ${lines} lines; lower or remove its inline-tab baseline`
    )
  }
  if (branches > rule.maxLiteralBranches) {
    failures.push(
      `${path} added an inline tab branch (${branches}, cap ${rule.maxLiteralBranches}); fixed Core tabs require separate files`
    )
  } else if (branches < rule.maxLiteralBranches) {
    failures.push(
      `${path} reduced inline tab branches to ${branches}; lower or remove its branch baseline from ${rule.maxLiteralBranches}`
    )
  }
}

const legacyInlineTabPages = new Set(Object.keys(baseline.legacyInlineAdminTabPages))
for (const path of walkFiles('apps/web/app/pages/admin').filter(path => path.endsWith('.vue'))) {
  if (legacyInlineTabPages.has(path)) continue
  const content = read(path)
  if (!content.includes('role="tablist"')) continue

  const branches = literalTabBranches(content)
  const importsTabComponents = /from\s+['"][^'"]+\/tabs\/[^'"]+\.vue['"]/.test(content)
  if (branches.size >= 2 && !importsTabComponents) {
    failures.push(
      `${path} implements ${branches.size} fixed tab branches inline; move substantial tabs under a domain /tabs/ component directory`
    )
  }
}

if (failures.length > 0) {
  console.error('Architecture boundary validation failed:')
  for (const failure of failures) console.error(`- ${failure}`)
  process.exit(1)
}

console.log(
  `Architecture boundary validation passed (${productionFiles.length} production files scanned, ${reviewSizeFileCount} files remain above the 500-line review threshold).`
)
