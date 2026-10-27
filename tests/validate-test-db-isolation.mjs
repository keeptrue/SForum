#!/usr/bin/env node
/**
 * 测试库隔离验收（P0）：证明 apps/api 的测试不再把开发库当测试库用。
 *
 * 三层证据，任一层不达标即验收失败：
 *   1. 静态：*_test.go 不得把 DATABASE_URL 直接当测试库读取；例外必须登记在
 *      tests/test-db-isolation-allowlist.json 并写明理由（例如子进程传参）。
 *   2. 运行时：在 source .env 的 shell 里跑 go test ./...，开发库的
 *      topics/posts/users 行数与 extensions 测试前缀行集合前后完全一致。
 *   3. 覆盖率：./scripts/test-api-isolated.sh 实际执行的测试数不低于
 *      tests/test-db-isolation-baseline.json 的基线（脚本自己数，不靠人眼）。
 *
 * 退出码：0 通过 / 1 验收不达标 / 2 前置不成立（脚本没跑起来，不是结论）。
 * 本脚本对开发库只做 SELECT，不写、不删。
 *
 * 用法：
 *   node tests/validate-test-db-isolation.mjs                  # 三层全跑
 *   node tests/validate-test-db-isolation.mjs --static-only     # 只跑静态扫描（秒级）
 *   node tests/validate-test-db-isolation.mjs --runtime-only    # 只跑运行时隔离
 *   node tests/validate-test-db-isolation.mjs --coverage-only   # 只跑覆盖率
 *   node tests/validate-test-db-isolation.mjs --write-baseline  # 记录当前覆盖率基线
 *   node tests/validate-test-db-isolation.mjs --list            # 列出全部静态违规
 *   node tests/validate-test-db-isolation.mjs --json            # 机器可读结果
 *   node tests/validate-test-db-isolation.mjs --force-runtime   # 静态未过也跑运行时（会真连开发库）
 *   node tests/validate-test-db-isolation.mjs --strict-quiet    # 开发 API 在跑时直接拒绝执行
 *   node tests/validate-test-db-isolation.mjs --coverage-args="-run TestX ./database/migrator"
 */

import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { compareToBaseline, countExecutedTests } from './helpers/test-db-isolation-coverage.mjs'
import { loadAllowlist, scanTestFile, walkGoTestFiles } from './helpers/test-db-isolation-scan.mjs'
import {
  DEV_TABLES,
  assertDevTarget,
  devDatabase,
  diffSnapshots,
  snapshotDevDatabase
} from './helpers/test-db-isolation-db.mjs'

const root = resolve(fileURLToPath(new URL('..', import.meta.url)))
const apiDir = join(root, 'apps/api')
const envPath = join(root, '.env')
const allowlistPath = join(root, 'tests/test-db-isolation-allowlist.json')
const baselinePath = process.env.SFORUM_TEST_DB_ISOLATION_BASELINE
  || join(root, 'tests/test-db-isolation-baseline.json')
const baselineLabel = relative(root, baselinePath).startsWith('..') ? baselinePath : relative(root, baselinePath)
const logDir = join(root, 'tests/.logs')
const RUNTIME_LOG = 'tests/.logs/test-db-isolation-runtime.log'
const COVERAGE_LOG = 'tests/.logs/test-db-isolation-coverage.log'
const REPORT_JSON = 'tests/.logs/test-db-isolation-report.json'

const options = {
  staticOnly: false,
  runtimeOnly: false,
  coverageOnly: false,
  writeBaseline: false,
  list: false,
  json: false,
  forceRuntime: false,
  strictQuiet: false,
  allowSuiteFailures: false,
  runtimeArgs: '',
  runtimePackages: './...',
  coverageArgs: '',
  coveragePackages: './...',
  timeoutMs: 1800000
}

function printHelp() {
  process.stdout.write(
    'node tests/validate-test-db-isolation.mjs [--static-only|--runtime-only|--coverage-only] ' +
    '[--write-baseline] [--list] [--json] [--force-runtime] [--strict-quiet] [--allow-suite-failures] ' +
    '[--runtime-args="..."] [--runtime-packages="./..."] ' +
    '[--coverage-args="..."] [--coverage-packages="./..."] [--timeout-ms=N]\n'
  )
}

function parseArgs(argv) {
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i]
    const value = () => {
      const eq = arg.indexOf('=')
      if (eq >= 0) return arg.slice(eq + 1)
      const next = argv[i + 1]
      if (next === undefined) throw new Error(`参数 ${arg} 需要一个值`)
      i += 1
      return next
    }
    if (arg === '--help' || arg === '-h') {
      printHelp()
      process.exit(0)
    } else if (arg === '--static-only') options.staticOnly = true
    else if (arg === '--runtime-only') options.runtimeOnly = true
    else if (arg === '--coverage-only') options.coverageOnly = true
    else if (arg === '--write-baseline') options.writeBaseline = true
    else if (arg === '--list') options.list = true
    else if (arg === '--json') options.json = true
    else if (arg === '--force-runtime') options.forceRuntime = true
    else if (arg === '--strict-quiet') options.strictQuiet = true
    else if (arg === '--allow-suite-failures') options.allowSuiteFailures = true
    else if (arg.startsWith('--runtime-args')) options.runtimeArgs = value()
    else if (arg.startsWith('--runtime-packages')) options.runtimePackages = value()
    else if (arg.startsWith('--coverage-args')) options.coverageArgs = value()
    else if (arg.startsWith('--coverage-packages')) options.coveragePackages = value()
    else if (arg.startsWith('--timeout-ms')) options.timeoutMs = Number(value())
    else throw new Error(`未知参数：${arg}（--help 看用法）`)
  }
}

function shq(input) {
  return `'${String(input).replaceAll("'", "'\\''")}'`
}

function run(command, args, runOptions = {}) {
  return spawnSync(command, args, {
    cwd: runOptions.cwd ?? root,
    env: runOptions.env ?? process.env,
    encoding: 'utf8',
    timeout: runOptions.timeout ?? options.timeoutMs,
    maxBuffer: 64 * 1024 * 1024
  })
}

function failingPackagesFromLog(path) {
  if (!existsSync(path)) return []
  const packages = new Set()
  for (const line of readFileSync(path, 'utf8').split('\n')) {
    const match = /^FAIL\s+(\S+)/.exec(line)
    if (match && match[1]) packages.add(match[1])
  }
  return [...packages]
}

function readTail(path, lines) {
  if (!existsSync(path)) return '（无日志）'
  const all = readFileSync(path, 'utf8').replaceAll('\r\n', '\n').split('\n').filter((line) => line.length > 0)
  return all.slice(-lines).join('\n')
}

// ---------------------------------------------------------------------------
// 第 1 层：静态扫描
// ---------------------------------------------------------------------------

function checkStatic() {
  const files = walkGoTestFiles(apiDir)
  const allow = loadAllowlist(allowlistPath, root)
  const violations = []
  const allowlisted = []
  const staleEntries = []
  let forwardedOnly = 0
  let isolatedAware = 0
  let testdbAware = 0

  for (const absolute of files) {
    const file = scanTestFile(absolute, root)
    if (file.usesIsolatedEnv) isolatedAware += 1
    if (file.usesTestdb) testdbAware += 1
    if (file.reads.length === 0) {
      if (file.forwarded.length > 0) forwardedOnly += 1
      continue
    }
    const entry = allow.byFile.get(file.path)
    const fallbackRisk = file.usesIsolatedEnv
    if (!entry) {
      violations.push({
        file: file.path,
        lines: file.reads,
        kind: fallbackRisk ? 'dev-db-fallback' : 'direct-dev-db-read',
        message: fallbackRisk
          ? '同时读取 SFORUM_TEST_DATABASE_URL 与 DATABASE_URL：回退分支会把测试打到开发库'
          : '直接把 DATABASE_URL 当测试库读取'
      })
      continue
    }
    if (fallbackRisk && entry.kind !== 'guarded-read') {
      violations.push({
        file: file.path,
        lines: file.reads,
        kind: 'dev-db-fallback',
        message: `登记为 ${entry.kind}，但文件里仍存在 DATABASE_URL 回退读取`
      })
      continue
    }
    allowlisted.push({ file: file.path, kind: entry.kind, lines: file.reads })
  }

  const seen = new Set([...violations, ...allowlisted].map((item) => item.file))
  for (const file of allow.byFile.keys()) {
    if (!seen.has(file)) staleEntries.push({ file, reason: '当前已无 DATABASE_URL 读取，登记可删除' })
  }

  return {
    id: 'static',
    title: '静态：*_test.go 不得把 DATABASE_URL 当测试库',
    status: violations.length === 0 ? 'pass' : 'fail',
    scanned: files.length,
    isolatedAware,
    testdbAware,
    forwardedOnly,
    violations,
    allowlisted,
    staleEntries,
    warnings: allow.warnings
  }
}

// ---------------------------------------------------------------------------
// 第 2 层：运行时隔离
// ---------------------------------------------------------------------------

function devApiRunning() {
  const result = run('pgrep', ['-f', 'sforum-api'], { timeout: 10000 })
  return result.status === 0 && `${result.stdout ?? ''}`.trim().length > 0
}

function checkRuntime(staticResult) {
  const dev = devDatabase(envPath)
  assertDevTarget(dev)
  const notes = []

  if (devApiRunning()) {
    notes.push('开发 API（sforum-api）正在运行：它自身的写入会让计数漂移，判定前先确认漂移来源')
    if (options.strictQuiet) {
      return {
        id: 'runtime',
        title: '运行时：source .env 后 go test ./... 不改动开发库',
        status: 'inconclusive',
        failures: ['--strict-quiet 下检测到开发 API 在运行，拒绝执行（先停 ./scripts/api-dev.sh）'],
        notes
      }
    }
  }

  if (staticResult && staticResult.status !== 'pass' && !options.forceRuntime) {
    return {
      id: 'runtime',
      title: '运行时：source .env 后 go test ./... 不改动开发库',
      status: 'skipped',
      failures: [],
      notes: [
        ...notes,
        `静态层仍有 ${staticResult.violations.length} 个文件直接读取 DATABASE_URL：此时跑全量 go test 会真的打到开发库，已跳过（--force-runtime 可强制）`
      ]
    }
  }

  mkdirSync(logDir, { recursive: true })
  const before = snapshotDevDatabase(dev)
  const command = [
    'set -euo pipefail',
    // 继承下来的隔离库变量会让这条检查变成空转：先清掉，只留 .env 里的开发库地址。
    'unset SFORUM_TEST_DATABASE_URL SFORUM_COMPAT_DATABASE_URL',
    'set -a',
    `. ${shq(envPath)}`,
    'set +a',
    `cd ${shq(apiDir)}`,
    `exec go test -count=1 ${options.runtimeArgs.trim()} ${options.runtimePackages} > ${shq(join(root, RUNTIME_LOG))} 2>&1`
  ].join('; ')
  const started = Date.now()
  const result = run('bash', ['-c', command])
  const elapsed = Math.round((Date.now() - started) / 1000)
  const after = snapshotDevDatabase(dev)
  const diff = diffSnapshots(before, after)
  notes.push(...diff.notes)

  if (result.status !== 0) {
    notes.push(`go test 退出码 ${result.status}（日志 ${RUNTIME_LOG}）：\n${readTail(join(root, RUNTIME_LOG), 25)}`)
  }

  // 判定只认开发库是否被改动；套件红不红是 tests/test.sh 的事。
  // 但"红了还判通过"必须显式开口（--allow-suite-failures），默认仍然 fail-closed。
  const suiteFailed = result.status !== 0
  const failingPackages = suiteFailed ? failingPackagesFromLog(join(root, RUNTIME_LOG)) : []
  let status = 'pass'
  if (diff.failures.length > 0) status = 'fail'
  else if (suiteFailed && !options.allowSuiteFailures) status = 'inconclusive'

  return {
    id: 'runtime',
    title: '运行时：source .env 后 go test ./... 不改动开发库',
    status,
    elapsed,
    dev: `${dev.user}@${dev.host}:${dev.port}/${dev.database}`,
    before: before.counts,
    after: after.counts,
    extensionRows: { before: before.extensionRows.length, after: after.extensionRows.length },
    suiteExit: result.status,
    failingPackages,
    log: RUNTIME_LOG,
    failures: diff.failures,
    notes
  }
}

// ---------------------------------------------------------------------------
// 第 3 层：覆盖率不退化
// ---------------------------------------------------------------------------

function readBaseline() {
  if (!existsSync(baselinePath)) return null
  try {
    return JSON.parse(readFileSync(baselinePath, 'utf8'))
  } catch (error) {
    throw new Error(`无法解析 ${baselinePath}：${error.message}`)
  }
}

async function checkCoverage() {
  mkdirSync(logDir, { recursive: true })
  const command = [
    'set -euo pipefail',
    `cd ${shq(root)}`,
    'export SFORUM_TEST_SHARDS=1',
    `exec ./scripts/test-api-isolated.sh -count=1 -json ${options.coverageArgs.trim()} ${options.coveragePackages} > ${shq(join(root, COVERAGE_LOG))} 2>&1`
  ].join('; ')
  const started = Date.now()
  const result = run('bash', ['-c', command])
  const elapsed = Math.round((Date.now() - started) / 1000)
  const title = '覆盖率：隔离脚本实际执行的测试数不低于基线'

  if (!existsSync(join(root, COVERAGE_LOG))) {
    return {
      id: 'coverage',
      title,
      status: 'inconclusive',
      failures: [`隔离脚本没有产生日志（exit ${result.status}）：${(result.stderr ?? '').trim().slice(0, 400)}`],
      notes: []
    }
  }

  const measured = await countExecutedTests(join(root, COVERAGE_LOG))
  const packageCount = Object.keys(measured.perPackage).length

  if (options.writeBaseline) {
    // fail-closed：测试没跑绿时写基线会把缺口固化成「合规」，之后任何退化都能蒙混过关。
    if (result.status !== 0) {
      return {
        id: 'coverage',
        title,
        status: 'inconclusive',
        elapsed,
        measured: { totalTests: measured.totalPass, packages: packageCount },
        failures: [`隔离脚本退出码 ${result.status}，拒绝写入基线（先让测试跑绿，否则基线会把失败固化成合规）：见 ${COVERAGE_LOG}`],
        notes: []
      }
    }
    const payload = {
      version: 1,
      measuredAt: new Date().toISOString(),
      command: './scripts/test-api-isolated.sh -count=1 -json ./...',
      note: '由 tests/validate-test-db-isolation.mjs --write-baseline 自动记录；只能手工上调，不允许为了通过而调低。',
      totalTests: measured.totalPass,
      dbPackages: packageCount,
      perPackage: measured.perPackage,
      dbSkips: measured.skips
    }
    writeFileSync(baselinePath, `${JSON.stringify(payload, null, 2)}\n`)
    return {
      id: 'coverage',
      title,
      status: 'pass',
      elapsed,
      baselineWritten: `${baselineLabel}（${measured.totalPass} 个测试 / ${packageCount} 个包）`,
      failures: [],
      notes: []
    }
  }

  const baseline = readBaseline()
  if (!baseline) {
    return {
      id: 'coverage',
      title,
      status: 'inconclusive',
      elapsed,
      measured: { totalTests: measured.totalPass, packages: packageCount },
      failures: [`缺少 ${baselineLabel}：先跑 --write-baseline 记录一次基线`],
      notes: []
    }
  }

  const failures = compareToBaseline(measured, baseline)
  if (result.status !== 0 && failures.length === 0) {
    failures.push(`隔离脚本退出码 ${result.status}：见 ${COVERAGE_LOG}`)
  }

  return {
    id: 'coverage',
    title,
    status: failures.length === 0 ? 'pass' : 'fail',
    elapsed,
    measured: { totalTests: measured.totalPass, packages: packageCount, baseline: baseline.totalTests },
    failures,
    notes: []
  }
}

// ---------------------------------------------------------------------------
// 输出
// ---------------------------------------------------------------------------

const STATUS_LABEL = { pass: '通过', fail: '不通过', inconclusive: '不确定', skipped: '跳过' }

function printStatic(result) {
  const lines = [
    `[1/3] ${result.title}`,
    `      扫描 ${result.scanned} 个 *_test.go；${result.isolatedAware} 个使用 SFORUM_TEST_DATABASE_URL，` +
    `${result.testdbAware} 个已走 internal/testdb，${result.forwardedOnly} 个仅向子进程传参`
  ]
  if (result.status === 'pass') {
    lines.push(`      结果：通过（登记例外 ${result.allowlisted.length} 个）`)
  } else {
    lines.push(`      结果：不通过，${result.violations.length} 个文件仍在把 DATABASE_URL 当测试库`)
    const shown = options.list ? result.violations : result.violations.slice(0, 15)
    for (const item of shown) {
      lines.push(`        - ${item.file}:${item.lines.join(',')} [${item.kind}] ${item.message}`)
    }
    if (!options.list && result.violations.length > shown.length) {
      lines.push(`        …其余 ${result.violations.length - shown.length} 个用 --list 查看`)
    }
  }
  for (const entry of result.allowlisted) lines.push(`      例外：${entry.file}（${entry.kind}）`)
  for (const stale of result.staleEntries) lines.push(`      提示：allowlist 中的 ${stale.file} 已过期（${stale.reason}）`)
  for (const warning of result.warnings) lines.push(`      警告：${warning}`)
  return lines.join('\n')
}

function printRuntime(result) {
  const lines = [`[2/3] ${result.title}`]
  if (result.dev) lines.push(`      开发库：${result.dev}（只读快照）`)
  if (result.status === 'skipped') {
    lines.push('      结果：跳过（静态层未通过，跑全量 go test 会真的写开发库）')
  } else {
    if (result.before) {
      lines.push(`      ${DEV_TABLES.map((t) => `${t}=${result.before[t]}→${result.after[t]}`).join('  ')}`)
      lines.push(`      extensions 测试前缀行：${result.extensionRows.before} → ${result.extensionRows.after}`)
    }
    lines.push(`      结果：${STATUS_LABEL[result.status]}（go test exit ${result.suiteExit ?? '未执行'}，${result.elapsed ?? 0}s）`)
  }
  for (const failure of result.failures) lines.push(`        - ${failure}`)
  if (result.failingPackages?.length) lines.push(`      go test 失败包：${result.failingPackages.join(', ')}`)
  for (const note of result.notes) lines.push(`      说明：${note}`)
  return lines.join('\n')
}

function printCoverage(result) {
  const lines = [`[3/3] ${result.title}`]
  if (result.baselineWritten) lines.push(`      已写入基线：${result.baselineWritten}`)
  if (result.measured) {
    const baseline = result.measured.baseline === undefined ? '' : ` / 基线 ${result.measured.baseline}`
    lines.push(`      实际执行：${result.measured.totalTests} 个测试，${result.measured.packages} 个包${baseline}（${result.elapsed ?? 0}s）`)
  }
  lines.push(`      结果：${STATUS_LABEL[result.status]}`)
  for (const failure of result.failures) lines.push(`        - ${failure}`)
  for (const note of result.notes) lines.push(`      说明：${note}`)
  return lines.join('\n')
}

async function main() {
  parseArgs(process.argv.slice(2))
  if ([options.staticOnly, options.runtimeOnly, options.coverageOnly].filter(Boolean).length > 1) {
    throw new Error('--static-only / --runtime-only / --coverage-only 只能选一个')
  }
  const runStatic = !options.runtimeOnly && !options.coverageOnly
  const runRuntime = !options.staticOnly && !options.coverageOnly
  const runCoverage = !options.staticOnly && !options.runtimeOnly

  const results = []
  let staticResult = null
  if (runStatic) {
    staticResult = checkStatic()
    results.push(staticResult)
    if (!options.json) process.stdout.write(`${printStatic(staticResult)}\n`)
  }
  if (runRuntime) {
    const runtimeResult = checkRuntime(staticResult)
    results.push(runtimeResult)
    if (!options.json) process.stdout.write(`${printRuntime(runtimeResult)}\n`)
  }
  if (runCoverage) {
    const coverageResult = await checkCoverage()
    results.push(coverageResult)
    if (!options.json) process.stdout.write(`${printCoverage(coverageResult)}\n`)
  }

  const failed = results.filter((item) => item.status === 'fail')
  const pending = results.filter((item) => item.status === 'inconclusive' || item.status === 'skipped')
  const exitCode = failed.length > 0 ? 1 : pending.length > 0 ? 2 : 0

  // 每次都落一份机器可读结果：验收结论要能被人独立复核，而不是只活在终端里。
  writeFileSync(join(root, REPORT_JSON), `${JSON.stringify({
    exitCode,
    finishedAt: new Date().toISOString(),
    options: { staticOnly: options.staticOnly, runtimeOnly: options.runtimeOnly, coverageOnly: options.coverageOnly, allowSuiteFailures: options.allowSuiteFailures, forceRuntime: options.forceRuntime },
    results
  }, null, 2)}\n`)

  if (options.json) {
    process.stdout.write(`${JSON.stringify({ exitCode, results }, null, 2)}\n`)
    process.exit(exitCode)
  }
  const verdict = exitCode === 0
    ? '验收通过：静态 / 运行时 / 覆盖率三层证据齐备'
    : exitCode === 1
      ? `验收不达标：${failed.map((item) => item.id).join(', ')}`
      : `未得出结论：${pending.map((item) => item.id).join(', ')} 未能完成`
  process.stdout.write(`\n${verdict}（exit ${exitCode}）\n`)
  process.exit(exitCode)
}

try {
  await main()
} catch (error) {
  process.stderr.write(`${error.message}\n`)
  process.exit(2)
}
