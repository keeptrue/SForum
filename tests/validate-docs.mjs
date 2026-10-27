/**
 * Documentation and version-governance validation.
 *
 * Checks:
 *  1. Markdown local links and heading anchors in README.md, the docs tree,
 *     and every README.md under extensions/.
 *  2. docs/zh-CN and docs/en-US have parallel file structures.
 *  3. Rolling docs must not contain concrete stable release numbers. Full
 *     prerelease tokens remain valid historical/example values, but their
 *     stable prefix never becomes a global exemption.
 *  4. The documented Go requirement matches apps/api/go.mod, and the
 *     apps/api/Dockerfile golang base image matches go.mod.
 *  5. The CLI overview covers the real top-level commands and `extension`
 *     subcommands (derived from apps/api/cmd/sforum sources); an unknown
 *     AddCommand constructor is a failure, never silently skipped.
 *  6. Every explicit HTTP method/path pair in rolling docs exists in both
 *     OpenAPI and the generated Go Core Route Catalog.
 *  7. README and both deployment guides use the verified bootstrap as the
 *     stable rolling install/update entry.
 *  8. The deployment guide set is complete in both locales, every
 *     `.env.production.example` key is documented, the documented proxy and
 *     Safe Mode variables are actually passed through Compose, and the
 *     operational markers for install/update/manual/troubleshooting survive.
 *  9. The Release workflow produces the fixed-name deploy assets and includes
 *     them in SHA256SUMS.
 * 10. Dependabot uses the Compose-aware ecosystem at the repository root and
 *     covers every Go module in the governed source/tooling scopes.
 *
 * Structured sources (go.mod, command Use strings, workflow YAML, finalize
 * script) are preferred over matching large page bodies.
 */
import {
  existsSync,
  readFileSync,
  readdirSync,
  statSync
} from 'node:fs'
import { spawnSync } from 'node:child_process'
import { dirname, join, normalize, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const defaultRoot = resolve(fileURLToPath(new URL('..', import.meta.url)))
// Overridable for the failure-path tests in tests/validate-docs_test.sh.
const root = resolve(process.env.SFORUM_VALIDATE_ROOT || defaultRoot)
const failures = []
const gitWorktree = existsSync(repoPath('.git')) && spawnSync(
  'git',
  ['rev-parse', '--is-inside-work-tree'],
  { cwd: root, encoding: 'utf8' }
).status === 0

function repoPath(path) {
  return resolve(root, path)
}

function read(path) {
  return readFileSync(repoPath(path), 'utf8')
}

function readIfExists(path) {
  if (!existsSync(repoPath(path))) return null
  return read(path)
}

function fail(message) {
  failures.push(message)
}

function walkFiles(directory) {
  const absolute = repoPath(directory)
  if (!existsSync(absolute)) return []
  const files = []
  for (const entry of readdirSync(absolute, { withFileTypes: true })) {
    const child = join(absolute, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === 'archive') continue
      files.push(...walkFiles(join(directory, entry.name)))
    } else if (entry.name.endsWith('.md')) {
      files.push(join(directory, entry.name))
    }
  }
  return files
}

function walkReadmeFiles(directory) {
  const absolute = repoPath(directory)
  if (!existsSync(absolute)) return []
  const files = []
  for (const entry of readdirSync(absolute, { withFileTypes: true })) {
    const child = join(absolute, entry.name)
    if (entry.isDirectory()) {
      files.push(...walkReadmeFiles(join(directory, entry.name)))
    } else if (entry.name === 'README.md') {
      files.push(join(directory, entry.name))
    }
  }
  return files
}

function walkNamedFiles(directory, filename) {
  const absolute = repoPath(directory)
  if (!existsSync(absolute)) return []
  const files = []
  for (const entry of readdirSync(absolute, { withFileTypes: true })) {
    if (entry.name === '.git' || entry.name === 'node_modules') continue
    if (entry.isDirectory()) {
      files.push(...walkNamedFiles(join(directory, entry.name), filename))
    } else if (entry.name === filename) {
      files.push(join(directory, entry.name))
    }
  }
  return files
}

function collectHeadings(markdown) {
  const headings = []
  for (const line of markdown.split('\n')) {
    const match = /^(#{1,6})\s+(.+?)\s*$/.exec(line)
    if (match) headings.push(match[2])
  }
  return headings
}

// GitHub-style anchor slug for the heading text (lowercase, punctuation
// removed except hyphens/spaces, spaces become hyphens; CJK ideographs and
// kana are kept because GitHub keeps them in anchors). A second variant
// strips spaces entirely to stay compatible with existing CJK anchors in the
// handbook.
const ANCHOR_KEEP_CLASS = '\\p{L}\\p{N}\\u4E00-\\u9FFF\\u3040-\\u30FF\\uAC00-\\uD7AF -'

function slugifyHeading(text) {
  const kept = text
    .toLowerCase()
    .replace(new RegExp(`[^${ANCHOR_KEEP_CLASS}]`, 'gu'), '')
    .trim()
  return kept.replace(/\s+/g, '-').replace(/-+/g, '-')
}

function slugifyHeadingNoSpaces(text) {
  const kept = text
    .toLowerCase()
    .replace(new RegExp(`[^${ANCHOR_KEEP_CLASS.replace(' ', '')}]`, 'gu'), '')
  return kept.replace(/-+/g, '-')
}

function validateLocalLinks(fileRelative) {
  const content = read(fileRelative)
  const directory = dirname(fileRelative)
  const inlineLinks = content.matchAll(/\[[^\]]*\]\(([^)\s]+)(?:[ \t]+"[^"]*")?\)/g)
  for (const match of inlineLinks) {
    const target = match[1]
    if (/^(https?:|mailto:|#|\/)/i.test(target)) continue
    const [pathPart, anchorPart] = target.split('#', 2)
    if (!pathPart) continue
    const normalizedTarget = normalize(join(directory, pathPart))
    let targetAbsolute = repoPath(normalizedTarget)
    if (!existsSync(targetAbsolute)) {
      if (existsSync(`${targetAbsolute}.md`)) {
        targetAbsolute = `${targetAbsolute}.md`
      } else if (statSafe(targetAbsolute)?.isDirectory() && existsSync(join(targetAbsolute, 'README.md'))) {
        targetAbsolute = join(targetAbsolute, 'README.md')
      } else {
        fail(`${fileRelative}: broken local link -> ${target}`)
        continue
      }
    }
    if (isGitIgnored(targetAbsolute)) {
      fail(`${fileRelative}: local link points to a gitignored target unavailable in a clean checkout -> ${target}`)
      continue
    }
    if (!anchorPart) continue
    const headings = collectHeadings(readFileSync(targetAbsolute, 'utf8'))
    const slugs = new Set(headings.map(slugifyHeading))
    const slugsNoSpaces = new Set(headings.map(slugifyHeadingNoSpaces))
    if (!slugs.has(slugifyHeading(anchorPart)) && !slugsNoSpaces.has(slugifyHeadingNoSpaces(anchorPart))) {
      fail(`${fileRelative}: broken heading anchor '${anchorPart}' in ${normalizedTarget}`)
    }
  }
}

function isGitIgnored(absolutePath) {
  if (!gitWorktree) return false
  const target = relative(root, absolutePath)
  if (target.startsWith('..')) return false
  return spawnSync('git', ['check-ignore', '--quiet', '--', target], {
    cwd: root,
    encoding: 'utf8'
  }).status === 0
}

function statSafe(path) {
  try {
    return statSync(path)
  } catch {
    return null
  }
}

function validateParallelStructure() {
  const zhFiles = walkFiles('docs/zh-CN').map((file) => file.slice('docs/zh-CN/'.length)).sort()
  const enFiles = walkFiles('docs/en-US').map((file) => file.slice('docs/en-US/'.length)).sort()
  for (const file of zhFiles) {
    if (!enFiles.includes(file)) fail(`docs/en-US is missing the parallel file: ${file}`)
  }
  for (const file of enFiles) {
    if (!zhFiles.includes(file)) fail(`docs/zh-CN is missing the parallel file: ${file}`)
  }
}

function validateRollingVersions() {
  const rollingFiles = [
    'README.md',
    'docs/README.md',
    ...walkFiles('docs/zh-CN'),
    ...walkFiles('docs/en-US'),
  ]
  const releaseToken = /\bv\d+\.\d+\.\d+(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?\b/g
  for (const file of rollingFiles) {
    const lines = read(file).split('\n')
    lines.forEach((line, index) => {
      for (const match of line.matchAll(releaseToken)) {
        const token = match[0]
        if (token.includes('-')) continue
        fail(`${file}:${index + 1}: rolling docs must not contain the concrete release number ${token}; use \$SFORUM_VERSION or <VERSION>`)
      }
    })
  }
}

function validateGoVersion() {
  const goMod = read('apps/api/go.mod')
  const match = /^go\s+(\d+\.\d+\.\d+)/m.exec(goMod)
  if (!match) {
    fail('apps/api/go.mod has no toolchain version line')
    return
  }
  const goVersion = match[1]
  const checkedFiles = [
    'AGENTS.md',
    'docs/zh-CN/getting-started.md',
    'docs/en-US/getting-started.md',
    'docs/zh-CN/development/setup.md',
    'docs/en-US/development/setup.md',
    'knowledge/modules/backend.md',
  ]
  for (const file of checkedFiles) {
    const content = readIfExists(file)
    if (content === null) {
      fail(`Go version check target is missing: ${file}`)
      continue
    }
    if (!content.includes(goVersion)) {
      fail(`${file}: must mention the Go toolchain version ${goVersion} anchored by apps/api/go.mod`)
    }
  }
  // The API Dockerfile must build with the same Go toolchain as go.mod.
  const dockerfile = readIfExists('apps/api/Dockerfile')
  if (dockerfile === null) {
    fail('apps/api/Dockerfile is missing for the Go toolchain check')
  } else {
    const baseMatch = /^FROM\s+golang:(\d+\.\d+\.\d+)/m.exec(dockerfile)
    if (!baseMatch) {
      fail('apps/api/Dockerfile has no golang base image line')
    } else if (baseMatch[1] !== goVersion) {
      fail(`apps/api/Dockerfile golang base image ${baseMatch[1]} must match the go.mod toolchain version ${goVersion}`)
    }
  }
}

// Constructor call -> CLI command name, mirroring the AddCommand list in
// apps/api/cmd/sforum/command.go.
const TOP_LEVEL_COMMANDS = {
  'newVersionCommand': 'version',
  'newMakeCommand("plugin")': 'make:plugin',
  'newMakeCommand("theme")': 'make:theme',
  'newSeedCommand': 'seed:forum',
  'newSeedPerfCommand': 'seed:perf',
  'newUsersResetPasswordCommand': 'users:reset-password',
  'newPrivacyEraseUserCommand': 'privacy:erase-user',
  'newRevisionsCommand': 'revisions',
  'newExtensionCommand': 'extension',
  'newExtensionRecoveryCommand': 'recovery',
  'newDevCleanupOrphanPluginsCommand': 'dev:cleanup-orphan-plugins',
  'newAIEvalCommand': 'ai:eval',
  'newAIReplyCommand': 'ai:reply',
  'newAIDerivedCommand': 'ai:derived',
}

// File -> command path prefix for commands that belong to `extension`.
const EXTENSION_COMMAND_FILES = {
  'validate.go': ['extension'],
  'build_extension.go': ['extension'],
  'manifest_digest.go': ['extension'],
  'test_extension.go': ['extension'],
  'package_extension.go': ['extension'],
  'extension_brand.go': ['extension', 'brand'],
  'docs.go': ['extension', 'docs'],
  'recovery.go': ['extension'],
  'recovery_lifecycle_close.go': ['extension', 'recovery'],
  'recovery_activation_state.go': ['extension', 'recovery'],
  'recovery_recheck.go': ['extension', 'recovery'],
  'plugin_command.go': ['extension', 'command'],
  'api_lts.go': ['extension'],
  'system_tier.go': ['extension', 'system-tier'],
  'dev_extension.go': ['extension', 'dev'],
  'dev_builtins.go': ['extension', 'builtins'],
}

function collectUseTokens(filePath) {
  const content = read(filePath)
  const tokens = []
  for (const match of content.matchAll(/Use:\s+"([^"]+)"/g)) {
    const first = match[1].trim().split(/\s+/)[0]
    if (first) tokens.push(first)
  }
  return tokens
}

function collectCliExpectedCommands() {
  const commands = new Set()
  const commandGo = read('apps/api/cmd/sforum/command.go')
  const addBlock = /cmd\.AddCommand\(([\s\S]*?)\)\n\treturn cmd/.exec(commandGo)
  if (!addBlock) {
    fail('apps/api/cmd/sforum/command.go: could not parse the AddCommand list')
    return commands
  }
  const constructorMatches = [...addBlock[1].matchAll(/new(\w+Command)\(([^)]*)\)/g)]
  for (const match of constructorMatches) {
    const raw = match[0].replace(/\s+/g, '')
    // Accept both the plain constructor name and the name plus its literal
    // arguments (e.g. newMakeCommand("plugin")).
    const candidates = new Set([raw.replace(/\(\)$/, ''), raw])
    let name
    for (const candidate of candidates) {
      if (TOP_LEVEL_COMMANDS[candidate]) {
        name = TOP_LEVEL_COMMANDS[candidate]
        break
      }
    }
    if (!name) {
      // An unknown constructor is a real gap: the CLI coverage table must
      // know about every command registered at the root. Never skip silently.
      fail(`apps/api/cmd/sforum/command.go registers an unknown command constructor: ${match[0]}`)
      continue
    }
    commands.add(name)
  }
  let unparsed = addBlock[1]
  for (const match of constructorMatches.reverse()) {
    unparsed = `${unparsed.slice(0, match.index)}${unparsed.slice(match.index + match[0].length)}`
  }
  unparsed = unparsed
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/\/\/.*$/gm, '')
    .replace(/[\s,]/g, '')
  if (unparsed) {
    fail(`apps/api/cmd/sforum/command.go contains an unparsed AddCommand argument: ${unparsed}`)
  }
  // Extension subcommands: derive each leaf path from its parent file base and
  // the command's Use token, skipping the parent's own name.
  for (const [file, base] of Object.entries(EXTENSION_COMMAND_FILES)) {
    const parent = base[base.length - 1]
    // 文件不存在 = 这个文件没有注册任何子命令（最小 fixture 树里就是这种情况）。
    // 硬读会让「缺一个可选命令文件」变成一条 ENOENT 崩溃，而不是一条可读的校验结论。
    const commandFile = `apps/api/cmd/sforum/${file}`
    if (!existsSync(repoPath(commandFile))) continue
    for (const token of collectUseTokens(commandFile)) {
      if (token === parent) continue
      commands.add([...base, token].join(' '))
    }
  }
  for (const token of collectUseTokens('apps/api/cmd/sforum/revisions.go')) {
    if (token === 'revisions') continue
    commands.add(`revisions ${token}`)
  }
  return commands
}

function validateCliOverview() {
  const expected = collectCliExpectedCommands()
  for (const locale of ['zh-CN', 'en-US']) {
    const doc = read(`docs/${locale}/development/cli.md`)
    const plainText = doc.replace(/`/g, '')
    for (const command of expected) {
      if (!plainText.includes(command)) {
        fail(`docs/${locale}/development/cli.md does not cover the real CLI command: ${command}`)
      }
    }
  }
}

const HTTP_METHODS = new Set(['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'])

function rollingDocumentationFiles() {
  return [
    'README.md',
    'docs/README.md',
    ...walkFiles('docs/zh-CN'),
    ...walkFiles('docs/en-US'),
    ...walkReadmeFiles('extensions'),
  ]
}

function collectDocumentedHttpEndpoints() {
  const endpoints = new Map()
  const pattern = /\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+(\/[-A-Za-z0-9._~{}:/]+)(?=[\s`'"),，。；;]|$)/g
  for (const file of rollingDocumentationFiles()) {
    const lines = read(file).split('\n')
    lines.forEach((line, index) => {
      for (const match of line.matchAll(pattern)) {
        let path = match[2]
        if (!path.startsWith('/api/v1/')) path = `/api/v1${path}`
        const key = `${match[1]} ${path}`
        if (!endpoints.has(key)) endpoints.set(key, [])
        endpoints.get(key).push(`${file}:${index + 1}`)
      }
    })
  }
  return endpoints
}

function parseOpenApiOperations() {
  const operations = new Set()
  const entrypoint = readIfExists('contracts/openapi.yaml')
  if (entrypoint === null) return null

  const pathRefs = entrypoint.matchAll(/^  "([^"]+)":\s*\n    "\$ref": "([^"]+)"/gm)
  for (const match of pathRefs) {
    const [, path, ref] = match
    const [relativeFile, rawAnchor] = ref.split('#', 2)
    if (!relativeFile || !rawAnchor?.startsWith('/')) continue
    const contractFile = normalize(join('contracts', relativeFile))
    const content = readIfExists(contractFile)
    if (content === null) continue
    const anchor = rawAnchor.slice(1)
    const lines = content.split('\n')
    const start = lines.findIndex((line) => line === `${anchor}:`)
    if (start < 0) continue
    for (let index = start + 1; index < lines.length; index += 1) {
      if (/^[^\s#][^:]*:/.test(lines[index])) break
      const method = /^  ([a-z]+):\s*$/.exec(lines[index])?.[1]?.toUpperCase()
      if (HTTP_METHODS.has(method)) operations.add(`${method} /api/v1${path}`)
    }
  }
  return operations
}

function parseGoRouteCatalog() {
  const content = readIfExists('apps/api/app/Support/Routes/core_catalog_gen.go')
  if (content === null) return null
  const operations = new Set()
  for (const match of content.matchAll(/Method:\s*"([A-Z]+)", Path:\s*"([^"]+)"/g)) {
    const path = match[2].replace(/:([A-Za-z][A-Za-z0-9]*)/g, '{$1}')
    operations.add(`${match[1]} ${path}`)
  }
  return operations
}

function validateDocumentedHttpEndpoints() {
  const documented = collectDocumentedHttpEndpoints()
  if (documented.size === 0) return

  const openApi = parseOpenApiOperations()
  const goRoutes = parseGoRouteCatalog()
  if (openApi === null) fail('contracts/openapi.yaml is missing for documented HTTP endpoint validation')
  if (goRoutes === null) fail('apps/api/app/Support/Routes/core_catalog_gen.go is missing for documented HTTP endpoint validation')
  if (openApi === null || goRoutes === null) return

  for (const [endpoint, sources] of documented) {
    const locations = sources.join(', ')
    if (!openApi.has(endpoint)) {
      fail(`${locations}: documented HTTP endpoint is not declared by OpenAPI: ${endpoint}`)
    }
    if (!goRoutes.has(endpoint)) {
      fail(`${locations}: documented HTTP endpoint is not registered in the Go route catalog: ${endpoint}`)
    }
  }
}

function validateStableInstallEntry() {
  // The rolling entry is the one-line loader. Piping it into bash is allowed
  // because it only resolves a tag, verifies that tag's bootstrap, promotes it,
  // and execs it. The bootstrap itself carries the install state machine and
  // must never be piped; the auditable sequence stays complete in one-click.md.
  const entryFiles = [
    'README.md',
    'docs/zh-CN/deployment.md',
    'docs/en-US/deployment.md',
  ]
  const auditFiles = [
    'docs/zh-CN/deployment/one-click.md',
    'docs/en-US/deployment/one-click.md',
  ]

  for (const file of entryFiles) {
    const content = read(file)
    if (!content.includes('releases/latest/download/sforum-install.sh')) {
      fail(`${file}: must use the stable rolling one-line entry releases/latest/download/sforum-install.sh`)
    }
    if (!content.includes('--channel prerelease')) {
      fail(`${file}: must document the explicit prerelease channel (--channel prerelease)`)
    }
    if (!content.includes('deployment/one-click.md')) {
      fail(`${file}: must link the auditable one-click guide`)
    }
    if (!content.includes('./sforum-bootstrap.sh upgrade')) {
      fail(`${file}: existing-install updates must still use the verified local bootstrap`)
    }
    for (const match of content.matchAll(/curl\b[^\n]*\|\s*(?:ba)?sh\b/g)) {
      if (!match[0].includes('sforum-install.sh')) {
        fail(`${file}: only the one-line loader may be piped into a shell (found: ${match[0].trim()})`)
      }
    }
  }

  for (const file of auditFiles) {
    const content = read(file)
    const requiredAuditMarkers = [
      'releases/latest/download/sforum-bootstrap.sh',
      'set -eu',
      `awk '$2 == "sforum-bootstrap.sh" { print }' SHA256SUMS`,
      `test "$(wc -l < sforum-bootstrap.sha256 | tr -d '[:space:]')" = 1`,
      'install -m 0755 "$bootstrap_dir/sforum-bootstrap.sh" ./sforum-bootstrap.sh',
      './sforum-bootstrap.sh install',
    ]
    for (const marker of requiredAuditMarkers) {
      if (!content.includes(marker)) {
        fail(`${file}: the auditable bootstrap sequence must stay complete (missing: ${marker})`)
      }
    }
    if (!content.includes('./sforum-bootstrap.sh upgrade')) {
      fail(`${file}: existing-install updates must use the verified bootstrap`)
    }
  }
}

function validateDeploymentGuides() {
  const pages = [
    'deployment/one-click.md',
    'deployment/manual.md',
    'deployment/environment.md',
    'deployment/troubleshooting.md',
  ]
  const locales = ['zh-CN', 'en-US']
  const missing = new Set()

  for (const locale of locales) {
    for (const page of pages) {
      const file = `docs/${locale}/${page}`
      if (!existsSync(repoPath(file))) {
        missing.add(file)
        fail(`${file}: required deployment guide is missing`)
      }
    }
    const hub = read(`docs/${locale}/deployment.md`)
    for (const page of pages) {
      if (!hub.includes(`./${page}`)) {
        fail(`docs/${locale}/deployment.md: must link to ./${page}`)
      }
    }
  }

  // Every documented production key must stay discoverable in both locales.
  const envKeys = [...read('.env.production.example').matchAll(/^([A-Z][A-Z0-9_]*)=/gm)]
    .map((match) => match[1])
  const passthroughKeys = [
    'TRUST_PROXY',
    'TRUSTED_PROXIES',
    'TRUST_PROXY_PRIVATE',
    'TRUST_PROXY_LOOPBACK',
    'SFORUM_SAFE_MODE',
    'PPROF_ENABLED',
  ]
  for (const locale of locales) {
    const file = `docs/${locale}/deployment/environment.md`
    if (missing.has(file)) continue
    const content = read(file)
    for (const key of [...envKeys, ...passthroughKeys]) {
      if (!new RegExp(`\\b${key}\\b`).test(content)) {
        fail(`${file}: must document ${key}`)
      }
    }
  }

  // The documented proxy/safe-mode variables must actually reach the API container.
  const compose = read('compose.yaml')
  for (const key of passthroughKeys.filter((key) => key !== 'PPROF_ENABLED')) {
    const pattern = new RegExp(`^\\s+${key}:\\s+\\$\\{${key}:`, 'm')
    if (!pattern.test(compose)) {
      fail(`compose.yaml: API environment must pass through ${key}`)
    }
  }

  const requiredMarkers = {
    'deployment/one-click.md': [
      './deploy.sh --action deploy',
      './upgrade.sh',
      '--confirm-maintenance',
      '.deployrc',
      'SFORUM_NONINTERACTIVE',
    ],
    'deployment/manual.md': [
      'docker compose',
      'migrate',
      'SFORUM_VERSION',
      'SFORUM_CONFIRM_RESTORE',
    ],
    'deployment/troubleshooting.md': [
      'recovery_required',
      'sforum extension disable',
      'PPROF_ENABLED',
    ],
  }
  for (const locale of locales) {
    for (const [page, markers] of Object.entries(requiredMarkers)) {
      const file = `docs/${locale}/${page}`
      if (missing.has(file)) continue
      const content = read(file)
      for (const marker of markers) {
        if (!content.includes(marker)) {
          fail(`${file}: missing operational marker ${marker}`)
        }
      }
    }
  }

  // Guard the two counts that drifted before the guides were split.
  for (const locale of locales) {
    const file = `docs/${locale}/deployment.md`
    const content = read(file)
    for (const phrase of ['四个 GHCR', 'four GHCR', '五个常驻服务', 'five long-running services']) {
      if (content.includes(phrase)) {
        fail(`${file}: stale service/image count: ${phrase}`)
      }
    }
  }
}

function validateReleaseAssets() {
  if (!existsSync(repoPath('scripts/ci/build-deploy-asset.sh'))) {
    fail('scripts/ci/build-deploy-asset.sh is missing')
  }
  const workflow = read('.github/workflows/release.yml')
  if (!workflow.includes('build-deploy-asset.sh')) {
    fail('.github/workflows/release.yml must run scripts/ci/build-deploy-asset.sh')
  }
  if (!workflow.includes('release-asset-deploy')) {
    fail('.github/workflows/release.yml must upload the deploy asset artifact')
  }
  const finalize = read('scripts/ci/finalize-release-assets.sh')
  for (const asset of ['sforum-deploy.tar.gz', 'sforum-bootstrap.sh', 'upgrade.sh', 'sforum-install.sh']) {
    if (!finalize.includes(asset)) {
      fail(`scripts/ci/finalize-release-assets.sh must include ${asset} in the expected asset set`)
    }
  }
}

function validateDependencyGovernance() {
  const content = readIfExists('.github/dependabot.yml')
  if (content === null) {
    fail('.github/dependabot.yml is missing')
    return
  }

  const entries = []
  for (const match of content.matchAll(/^\s*- package-ecosystem:\s*"([^"]+)"\s*\n\s+directory:\s*"([^"]+)"/gm)) {
    entries.push({ ecosystem: match[1], directory: match[2] })
  }
  if (!entries.some((entry) => entry.ecosystem === 'docker-compose' && entry.directory === '/')) {
    fail('.github/dependabot.yml must use the docker-compose ecosystem at directory "/" so compose*.yaml files are actually fetched')
  }

  const governedModules = new Set()
  if (existsSync(repoPath('apps/api/go.mod'))) governedModules.add('/apps/api')
  for (const scope of ['extensions/builtin', 'tools', 'tests/compat']) {
    for (const file of walkNamedFiles(scope, 'go.mod')) {
      governedModules.add(`/${dirname(file)}`)
    }
  }
  const configuredModules = new Set(
    entries
      .filter((entry) => entry.ecosystem === 'gomod')
      .map((entry) => entry.directory)
  )
  for (const directory of [...governedModules].sort()) {
    if (!configuredModules.has(directory)) {
      fail(`.github/dependabot.yml does not cover governed Go module: ${directory}/go.mod`)
    }
  }
}

// 1. Local links and anchors across README.md, docs/** and extensions/** READMEs.
for (const file of walkFiles('docs/zh-CN')) validateLocalLinks(file)
for (const file of walkFiles('docs/en-US')) validateLocalLinks(file)
for (const file of walkReadmeFiles('extensions')) validateLocalLinks(file)
validateLocalLinks('docs/README.md')
validateLocalLinks('README.md')

// 2. Parallel handbook structure.
validateParallelStructure()

// 3. No concrete current release numbers in rolling docs.
validateRollingVersions()

// 4. Go toolchain version consistency.
validateGoVersion()

// 5. CLI overview coverage.
validateCliOverview()

// 6. Explicit HTTP method/path pairs agree with both API authorities.
validateDocumentedHttpEndpoints()

// 7. Stable rolling install entry everywhere it matters.
validateStableInstallEntry()

// 8. Deployment guides stay complete, bilingual, and aligned with Compose.
validateDeploymentGuides()

// 9. Release workflow produces and checksums the deploy assets.
validateReleaseAssets()

// 10. Dependabot covers Compose and all governed Go modules.
validateDependencyGovernance()

if (failures.length > 0) {
  console.error('validate-docs.mjs:')
  for (const failure of failures) console.error(`  - ${failure}`)
  console.error(`validate-docs.mjs: ${failures.length} documentation validation failure(s)`)
  process.exit(1)
}
console.log('validate-docs.mjs: all checks passed')
