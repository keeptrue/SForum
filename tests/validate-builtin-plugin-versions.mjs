import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const pluginsRoot = join(repoRoot, 'extensions/builtin/plugins')
const baselinePath = join(repoRoot, 'tests/builtin-plugin-release-baseline.json')
const write = process.argv.includes('--write')

const sharedRuntimeRoots = [
  join(repoRoot, 'apps/api/sdk/plugin'),
  join(repoRoot, 'contracts/proto'),
]
const sharedRuntimeFiles = [
  join(repoRoot, 'apps/api/app/Support/PluginBootstrap/contract.go'),
]

const semverPattern = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/

const parseSemVer = (value) => {
  const match = semverPattern.exec(value)
  if (!match) return null
  const prerelease = match[4]?.split('.') ?? []
  if (prerelease.some(identifier => /^\d+$/.test(identifier) && identifier.length > 1 && identifier.startsWith('0'))) {
    return null
  }
  return {
    core: match.slice(1, 4).map(part => BigInt(part)),
    prerelease,
  }
}

const compareSemVer = (left, right) => {
  for (let index = 0; index < left.core.length; index += 1) {
    if (left.core[index] !== right.core[index]) return left.core[index] > right.core[index] ? 1 : -1
  }
  if (left.prerelease.length === 0 || right.prerelease.length === 0) {
    if (left.prerelease.length === right.prerelease.length) return 0
    return left.prerelease.length === 0 ? 1 : -1
  }
  const count = Math.max(left.prerelease.length, right.prerelease.length)
  for (let index = 0; index < count; index += 1) {
    const leftPart = left.prerelease[index]
    const rightPart = right.prerelease[index]
    if (leftPart === undefined || rightPart === undefined) return leftPart === undefined ? -1 : 1
    if (leftPart === rightPart) continue
    const leftNumeric = /^\d+$/.test(leftPart)
    const rightNumeric = /^\d+$/.test(rightPart)
    if (leftNumeric && rightNumeric) return BigInt(leftPart) > BigInt(rightPart) ? 1 : -1
    if (leftNumeric !== rightNumeric) return leftNumeric ? -1 : 1
    return leftPart > rightPart ? 1 : -1
  }
  return 0
}

const stableJSON = (value) => {
  if (Array.isArray(value)) return value.map(stableJSON)
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.keys(value).sort().map(key => [key, stableJSON(value[key])]))
  }
  return value
}

const stripDigests = (value) => {
  if (Array.isArray(value)) return value.map(stripDigests)
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value)
      .filter(([key]) => key !== 'digest')
      .map(([key, item]) => [key, stripDigests(item)]))
  }
  return value
}

// filesUnder 只返回**受版本控制**的文件。
//
// 为什么必须走 git 而不是 readdirSync：插件的 backend/ 目录里有编译产物
// （`backend/backend`、`backend/plugin`），它们是 .gitignore 掉的本地构建结果。
// 旧实现按目录遍历，只在文件名层面排除了 `plugin`，`backend` 这个二进制就被算进了
// source contract 摘要——于是「有构建产物的机器」和「干净 clone / CI」算出两个不同的
// digest，同一个提交在本地全绿、在 CI 却报 source contract drifted。摘要的定义必须是
// 「仓库里的源码」，不是「这台机器上恰好存在的东西」。
const trackedFilesUnder = (root) => {
  const listed = execFileSync('git', ['ls-files', '-z', '--', relative(repoRoot, root)], {
    cwd: repoRoot,
    encoding: 'utf8',
  })
  return listed
    .split('\0')
    .filter(Boolean)
    .map((path) => join(repoRoot, path))
}

const filesUnder = (root) => trackedFilesUnder(root)
    .filter((path) => {
      const name = relative(root, path).replaceAll('\\', '/')
      return name !== '.DS_Store'
    })
    .sort()

const sourceContractFiles = (pluginRoot) => {
  const files = []
  for (const path of filesUnder(pluginRoot)) {
    const name = relative(pluginRoot, path).replaceAll('\\', '/')
    if (name === 'sforum.extension.json' || name.startsWith('backend/') ||
        name.startsWith('manifest/') || name.startsWith('schemas/')) {
      files.push(path)
    }
  }
  return files
}

const digestPlugin = (pluginRoot) => {
  const hash = createHash('sha256')
  for (const path of sourceContractFiles(pluginRoot)) {
    const name = relative(pluginRoot, path).replaceAll('\\', '/')
    let body = readFileSync(path)
    if (name.endsWith('.json')) {
      const parsed = JSON.parse(body.toString('utf8'))
      body = Buffer.from(`${JSON.stringify(stableJSON(stripDigests(parsed)))}\n`)
    }
    hash.update(name)
    hash.update('\0')
    hash.update(body)
    hash.update('\0')
  }
  return hash.digest('hex')
}

const digestSharedRuntime = () => {
  const hash = createHash('sha256')
  const paths = [...sharedRuntimeFiles]
  for (const root of sharedRuntimeRoots) {
    paths.push(...filesUnder(root).filter(path => {
      const name = relative(root, path).replaceAll('\\', '/')
      return name.endsWith('.proto') || (name.endsWith('.go') && !name.endsWith('_test.go'))
    }))
  }
  for (const path of [...new Set(paths)].sort()) {
    const name = relative(repoRoot, path).replaceAll('\\', '/')
    hash.update(name)
    hash.update('\0')
    hash.update(readFileSync(path))
    hash.update('\0')
  }
  const transportDependencies = readFileSync(join(repoRoot, 'apps/api/go.mod'), 'utf8')
    .split('\n')
    .map(line => line.trim())
    .filter(line => /^(github\.com\/hashicorp\/go-plugin|google\.golang\.org\/(grpc|protobuf))\s/.test(line))
    .sort()
  hash.update('apps/api/go.mod#plugin-transport\0')
  hash.update(`${transportDependencies.join('\n')}\n`)
  return hash.digest('hex')
}

const plugins = Object.fromEntries(readdirSync(pluginsRoot).sort().flatMap((name) => {
  const root = join(pluginsRoot, name)
  if (!statSync(root).isDirectory()) return []
  const manifest = JSON.parse(readFileSync(join(root, 'sforum.extension.json'), 'utf8'))
  if (!parseSemVer(manifest.version ?? '')) {
    throw new Error(`${manifest.id ?? name}: built-in plugin version must be SemVer`)
  }
  return [[manifest.id, { version: manifest.version, sourceDigest: digestPlugin(root) }]]
}))
const current = {
  sharedRuntimeDigest: digestSharedRuntime(),
  plugins,
}

if (write) {
  writeFileSync(baselinePath, `${JSON.stringify(current, null, 2)}\n`)
  process.stdout.write(`updated ${relative(repoRoot, baselinePath)}\n`)
  process.exit(0)
}

const baseline = JSON.parse(readFileSync(baselinePath, 'utf8'))
if (JSON.stringify(stableJSON(current)) !== JSON.stringify(stableJSON(baseline))) {
  // 报出「到底谁漂了」：只说 drifted 会逼下一个人自己跑 --write 再 diff 基线，
  // 而共享运行时漂移（一个 proto 变更）和单个插件改源码的处理方式完全不同。
  const drifted = []
  if (baseline.sharedRuntimeDigest !== current.sharedRuntimeDigest) drifted.push('shared plugin runtime')
  const baselinePlugins = baseline.plugins ?? {}
  for (const [id, entry] of Object.entries(current.plugins)) {
    const before = baselinePlugins[id]
    if (!before) drifted.push(`${id} (new)`)
    else if (before.sourceDigest !== entry.sourceDigest || before.version !== entry.version) {
      drifted.push(`${id} (${before.version} -> ${entry.version})`)
    }
  }
  for (const id of Object.keys(baselinePlugins)) {
    if (!current.plugins[id]) drifted.push(`${id} (removed)`)
  }
  throw new Error(`built-in plugin source contract drifted (${drifted.join(', ') || 'baseline formatting'}); bump every changed plugin version, then run: node tests/validate-builtin-plugin-versions.mjs --write`)
}

const baseRef = process.env.SFORUM_BUILTIN_VERSION_BASE?.trim()
if (baseRef && !/^0+$/.test(baseRef)) {
  let previous
  try {
    previous = JSON.parse(execFileSync('git', ['show', `${baseRef}:tests/builtin-plugin-release-baseline.json`], {
      cwd: repoRoot,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }))
  } catch {
    previous = null
  }
  if (previous) {
    const violations = []
    const previousPlugins = previous.plugins ?? previous
    const sharedRuntimeChanged = Boolean(previous.sharedRuntimeDigest) &&
      previous.sharedRuntimeDigest !== current.sharedRuntimeDigest
    for (const [id, entry] of Object.entries(current.plugins)) {
      const before = previousPlugins[id]
      if (!before) continue
      const previousVersion = parseSemVer(before.version)
      if (!previousVersion) {
        violations.push(`${id}: previous baseline has invalid SemVer ${before.version}`)
        continue
      }
      const order = compareSemVer(parseSemVer(entry.version), previousVersion)
      if (order < 0) violations.push(`${id}: version regressed from ${before.version} to ${entry.version}`)
      else if ((before.sourceDigest !== entry.sourceDigest || sharedRuntimeChanged) && order <= 0) {
        const reason = sharedRuntimeChanged ? 'shared plugin runtime changed' : 'plugin source changed'
        violations.push(`${id}: ${reason} without a version bump (${entry.version})`)
      }
    }
    if (violations.length > 0) {
      throw new Error(`built-in plugin release contract violation: ${violations.join('; ')}`)
    }
  }
}

process.stdout.write(`validated ${Object.keys(current.plugins).length} built-in plugin release contracts\n`)
