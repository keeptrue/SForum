#!/usr/bin/env node
// bun audit 棘轮门：对「已登记基线」做只降不升的比较，而不是一个全局开关。
//
// 为什么不是 fail-on-severity：当前 lockfile 的基线是既存债，落地当天按严重度
// 卡门禁 = 从第一天起恒红，然后所有人学会忽略这个 job。棘轮换一个判据：
// **新增**的 advisory 才红，已登记的一条一条清，清完必须删条目。
//
// 三条硬失败：
//   1) 报告里有、基线里没有      → 新增漏洞；
//   2) 基线里有、报告里没有      → 基线失效，必须在同一次改动里删掉（否则棘轮会烂）；
//   3) 同一条 advisory 严重度变了 → 基线必须刷新（升级是回归，降级也要记账）。
// 身份只取 advisory url，不比受影响包集合：包集合随 lockfile 抖动，不是安全信号。
//
// 用法：
//   bun audit --prod --registry https://registry.npmjs.org --json > audit.json
//   node scripts/bun-audit-gate.mjs --report audit.json --unseeded-policy=fail
//   node scripts/bun-audit-gate.mjs --report audit.json --refresh     # 播种 / 更新基线
//   node scripts/bun-audit-gate.mjs --self-test                       # 离线自检棘轮逻辑
//
// 退出码：0 通过 / 1 门禁失败 / 2 用法或解析错误。

import { mkdtempSync, readFileSync, writeFileSync, existsSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const DEFAULT_BASELINE = 'scripts/bun-audit-baseline.json'
const SEVERITY_ORDER = { low: 1, moderate: 2, high: 3, critical: 4 }

function parseArgs(argv) {
  const options = {
    report: '',
    baseline: DEFAULT_BASELINE,
    refresh: false,
    selfTest: false,
    unseededPolicy: 'notice',
    summary: '',
    annotate: false,
  }
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index]
    switch (argument) {
      case '--report':
        options.report = argv[++index] ?? ''
        break
      case '--baseline':
        options.baseline = argv[++index] ?? ''
        break
      case '--summary':
        options.summary = argv[++index] ?? ''
        break
      case '--refresh':
        options.refresh = true
        break
      case '--self-test':
        options.selfTest = true
        break
      case '--annotate':
        options.annotate = true
        break
      default:
        if (argument.startsWith('--unseeded-policy=')) {
          options.unseededPolicy = argument.slice('--unseeded-policy='.length)
          break
        }
        throw new Error(`unsupported argument: ${argument}`)
    }
  }
  if (!['notice', 'fail'].includes(options.unseededPolicy)) {
    throw new Error(`--unseeded-policy must be notice or fail, got ${options.unseededPolicy}`)
  }
  return options
}

// 报告形状：{ "<package>": [ { severity, url, title, ... }, ... ] }
// 同一条 advisory 会按每个受影响版本各出现一次，所以按 url 去重。
function collectAdvisories(report) {
  const advisories = new Map()
  for (const [name, entries] of Object.entries(report)) {
    if (!Array.isArray(entries)) throw new Error(`audit report entry for ${name} is not an array`)
    for (const entry of entries) {
      if (!entry || typeof entry.url !== 'string' || entry.url === '') {
        throw new Error(`audit report entry for ${name} has no advisory url`)
      }
      const existing = advisories.get(entry.url)
      if (existing) {
        existing.packages.add(name)
        continue
      }
      advisories.set(entry.url, {
        severity: String(entry.severity ?? 'unknown'),
        title: String(entry.title ?? ''),
        packages: new Set([name]),
      })
    }
  }
  return advisories
}

function readReport(path) {
  if (!existsSync(path)) throw new Error(`audit report not found: ${path}`)
  const raw = readFileSync(path, 'utf8').trim()
  // bun audit 扫描失败时 stdout 为空；「没有 advisory」是 {}，两者必须分开。
  if (raw === '') throw new Error(`audit report is empty (the scan did not complete): ${path}`)
  let parsed
  try {
    parsed = JSON.parse(raw)
  } catch (error) {
    throw new Error(`audit report is not valid JSON: ${path}: ${error.message}`)
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error(`audit report must be a JSON object: ${path}`)
  }
  return collectAdvisories(parsed)
}

function loadBaseline(path) {
  if (!existsSync(path)) {
    return { version: 1, status: 'unseeded', advisories: {}, missing: true }
  }
  const parsed = JSON.parse(readFileSync(path, 'utf8'))
  const advisories = parsed.advisories ?? {}
  return {
    version: parsed.version ?? 1,
    status: parsed.status === 'seeded' ? 'seeded' : 'unseeded',
    advisories,
    missing: false,
  }
}

function baselineEntries(advisories) {
  const sorted = {}
  for (const url of Object.keys(advisories).sort()) {
    const entry = advisories[url]
    sorted[url] = {
      severity: entry.severity,
      packages: [...(entry.packages ?? [])].sort(),
    }
  }
  return sorted
}

function writeSummary(path, report, counts, totals, packageDrift) {
  if (!path) return
  const lines = [
    '## bun audit (production dependencies)',
    '',
    `Total: **${totals.unique} unique advisories** across ${totals.rows} package-version rows — ` +
      `critical ${counts.critical} / high ${counts.high} / moderate ${counts.moderate} / low ${counts.low} (per row)`,
    '',
    `Package sets that moved since the baseline: **${packageDrift}** (informational, never fails the gate)`,
    '',
    `Ratchet baseline: \`scripts/bun-audit-baseline.json\``,
    '',
  ]
  const ordered = [...report.entries()].sort((a, b) => {
    const bySeverity = (SEVERITY_ORDER[b[1].severity] ?? 0) - (SEVERITY_ORDER[a[1].severity] ?? 0)
    return bySeverity !== 0 ? bySeverity : a[0].localeCompare(b[0])
  })
  for (const [url, advisory] of ordered) {
    lines.push(
      `- \`${advisory.severity}\` ${[...advisory.packages].sort().join(', ')} — [${advisory.title}](${url})`,
    )
  }
  writeFileSync(path, `${lines.join('\n')}\n`)
}

function annotate(report) {
  const actionable = [...report.entries()]
    .filter(([, advisory]) => advisory.severity === 'critical' || advisory.severity === 'high')
    .sort((a, b) => (SEVERITY_ORDER[b[1].severity] ?? 0) - (SEVERITY_ORDER[a[1].severity] ?? 0))
  for (const [url, advisory] of actionable.slice(0, 10)) {
    console.log(
      `::warning::${advisory.severity} ${[...advisory.packages].sort().join(', ')}: ${advisory.title} (${url})`,
    )
  }
  if (actionable.length > 10) {
    console.log(
      `::notice::另有 ${actionable.length - 10} 条 critical/high advisory，完整清单见 job summary`,
    )
  }
}

function seedInstructions(options) {
  return [
    'bun audit 棘轮基线尚未播种（scripts/bun-audit-baseline.json status=unseeded）。',
    '播种需要一次联网扫描；基线不能凭计数伪造，所以这里不会自动写。',
    '在能访问 registry.npmjs.org 的机器上执行：',
    '  cd apps/web && bun audit --prod --registry https://registry.npmjs.org --json > /tmp/audit.json',
    '  node scripts/bun-audit-gate.mjs --report /tmp/audit.json --refresh',
    '然后把 scripts/bun-audit-baseline.json 的 diff 提交上去。',
  ].join('\n')
}

// 核心比较：返回 { exitCode, lines }。不直接退出，方便自检复用同一份逻辑。
function evaluate(options) {
  const lines = []
  const report = readReport(options.report)
  const baseline = loadBaseline(options.baseline)

  const counts = { critical: 0, high: 0, moderate: 0, low: 0 }
  let rows = 0
  for (const advisory of report.values()) {
    counts[advisory.severity] = (counts[advisory.severity] ?? 0) + 1
    rows += advisory.packages.size
  }
  const totals = { unique: report.size, rows }
  lines.push(
    `bun audit: ${totals.unique} 条去重 advisory / ${rows} 行` +
      `（critical ${counts.critical} / high ${counts.high} / moderate ${counts.moderate} / low ${counts.low}）`,
  )

  if (options.refresh) {
    if (report.size === 0) {
      lines.push('bun audit: 报告为空，拒绝用它刷新基线（空报告更可能是扫描失败）。')
      return { exitCode: 1, lines }
    }
    const added = [...report.keys()].filter((url) => !(url in baseline.advisories)).length
    const removed = Object.keys(baseline.advisories).filter((url) => !report.has(url)).length
    writeFileSync(
      options.baseline,
      `${JSON.stringify(
        {
          version: 1,
          status: 'seeded',
          generatedBy: 'node scripts/bun-audit-gate.mjs --refresh',
          advisories: baselineEntries(
            Object.fromEntries(
              [...report.entries()].map(([url, advisory]) => [
                url,
                { severity: advisory.severity, packages: [...advisory.packages] },
              ]),
            ),
          ),
        },
        null,
        2,
      )}\n`,
    )
    lines.push(`bun audit: 基线已写入 ${options.baseline}（新增 ${added} 条、移除 ${removed} 条）。`)
    return { exitCode: 0, lines }
  }

  if (baseline.status !== 'seeded') {
    lines.push(seedInstructions(options))
    if (options.unseededPolicy === 'fail') {
      lines.push('bun audit: --unseeded-policy=fail，未播种视为失败。')
      return { exitCode: 1, lines }
    }
    lines.push('bun audit: 未播种，本次只报告不判红（本地默认 notice）。')
    return { exitCode: 0, lines }
  }

  const added = []
  const escalated = []
  let packageDrift = 0
  for (const [url, advisory] of report) {
    const recorded = baseline.advisories[url]
    if (!recorded) {
      added.push({ url, ...advisory })
      continue
    }
    // 受影响包集合只做观测：它随 lockfile 抖动（同一 advisory 换一个传递依赖就会出现
    // 或消失），不是安全信号，所以既不进基线判据也不判红。留着是为了让「这条洞现在
    // 影响哪几个包」在 job summary 里可读。
    if ([...(recorded.packages ?? [])].sort().join('\u0000') !== [...advisory.packages].sort().join('\u0000')) {
      packageDrift += 1
    }
    const was = SEVERITY_ORDER[recorded.severity] ?? 0
    const now = SEVERITY_ORDER[advisory.severity] ?? 0
    if (now > was) escalated.push({ url, from: recorded.severity, to: advisory.severity, ...advisory })
    else if (now < was) escalated.push({ url, from: recorded.severity, to: advisory.severity, ...advisory })
  }
  const stale = Object.keys(baseline.advisories).filter((url) => !report.has(url))

  let failed = false
  lines.push(`bun audit: 本次受影响包集合与基线不同：${packageDrift} 条（只报告，不判红）`)
  if (added.length > 0) {
    failed = true
    lines.push(`bun audit: 新增 ${added.length} 条未登记 advisory：`)
    for (const advisory of added) {
      lines.push(`  [${advisory.severity}] ${advisory.url} ${advisory.title}`)
    }
  }
  if (escalated.length > 0) {
    failed = true
    lines.push(`bun audit: ${escalated.length} 条 advisory 严重度与基线不一致：`)
    for (const advisory of escalated) {
      const direction = (SEVERITY_ORDER[advisory.to] ?? 0) > (SEVERITY_ORDER[advisory.from] ?? 0)
        ? '升级'
        : '降级'
      lines.push(`  ${advisory.url} ${advisory.from} → ${advisory.to}（${direction}；请 --refresh 记账）`)
    }
  }
  if (stale.length > 0) {
    failed = true
    lines.push(`bun audit: 基线里 ${stale.length} 条 advisory 已不再出现，必须删条目（棘轮要跟着债一起降）：`)
    for (const url of stale) lines.push(`  ${url}`)
  }
  if (!failed) lines.push('bun audit: 与基线一致，无新增。')

  return { exitCode: failed ? 1 : 0, lines, report, counts, totals, packageDrift }
}

function runSelfTest() {
  const directory = mkdtempSync(join(tmpdir(), 'sforum-bun-audit-selftest.'))
  const failures = []
  const check = (label, condition) => {
    if (!condition) failures.push(label)
  }
  const advisory = (severity, url) => ({ severity, url, title: `advisory ${url}` })
  const write = (name, value) => {
    const path = join(directory, name)
    writeFileSync(path, typeof value === 'string' ? value : JSON.stringify(value))
    return path
  }
  const baseline = {
    version: 1,
    status: 'seeded',
    advisories: {
      'https://example.test/GHSA-aaaa': { severity: 'high', packages: ['left-pad'] },
      'https://example.test/GHSA-bbbb': { severity: 'low', packages: ['ms'] },
    },
  }
  const baselinePath = write('baseline.json', baseline)

  // 1) 与基线一致 → 绿
  const sameReport = write('same.json', {
    'left-pad': [advisory('high', 'https://example.test/GHSA-aaaa')],
    ms: [advisory('low', 'https://example.test/GHSA-bbbb')],
  })
  check('baseline match must pass', evaluate({ report: sameReport, baseline: baselinePath, unseededPolicy: 'fail' }).exitCode === 0)

  // 2) 注入一条新 advisory → 红，且点名
  const injectedReport = write('injected.json', {
    'left-pad': [advisory('high', 'https://example.test/GHSA-aaaa')],
    ms: [advisory('low', 'https://example.test/GHSA-bbbb')],
    evil: [advisory('critical', 'https://example.test/GHSA-cccc')],
  })
  const injected = evaluate({ report: injectedReport, baseline: baselinePath, unseededPolicy: 'fail' })
  check('injected advisory must fail', injected.exitCode === 1)
  check('injected advisory must be named', injected.lines.join('\n').includes('GHSA-cccc'))

  // 3) 基线条目消失 → 红（棘轮要跟着债一起降）
  const shrunkReport = write('shrunk.json', {
    'left-pad': [advisory('high', 'https://example.test/GHSA-aaaa')],
  })
  const shrunk = evaluate({ report: shrunkReport, baseline: baselinePath, unseededPolicy: 'fail' })
  check('stale baseline entry must fail', shrunk.exitCode === 1)
  check('stale entry must be named', shrunk.lines.join('\n').includes('GHSA-bbbb'))

  // 4) 严重度升级 → 红
  const escalatedReport = write('escalated.json', {
    'left-pad': [advisory('critical', 'https://example.test/GHSA-aaaa')],
    ms: [advisory('low', 'https://example.test/GHSA-bbbb')],
  })
  check('severity escalation must fail', evaluate({ report: escalatedReport, baseline: baselinePath, unseededPolicy: 'fail' }).exitCode === 1)

  // 5) 未播种：notice 绿、fail 红
  const unseededPath = write('unseeded.json', { version: 1, status: 'unseeded', advisories: {} })
  check('unseeded + notice must pass', evaluate({ report: sameReport, baseline: unseededPath, unseededPolicy: 'notice' }).exitCode === 0)
  check('unseeded + fail must fail', evaluate({ report: sameReport, baseline: unseededPath, unseededPolicy: 'fail' }).exitCode === 1)

  // 6) 空报告（扫描失败）→ 红，不能当成「零漏洞」
  const emptyPath = write('empty.json', '')
  let emptyFailed = false
  try {
    evaluate({ report: emptyPath, baseline: baselinePath, unseededPolicy: 'fail' })
  } catch {
    emptyFailed = true
  }
  check('empty report must be rejected', emptyFailed)

  // 7) --refresh 播种后同一份报告变绿
  const freshPath = join(directory, 'fresh.json')
  check('refresh must succeed', evaluate({ report: injectedReport, baseline: freshPath, refresh: true, unseededPolicy: 'fail' }).exitCode === 0)
  check('refresh must seed', loadBaseline(freshPath).status === 'seeded')
  check('seeded baseline must then pass', evaluate({ report: injectedReport, baseline: freshPath, unseededPolicy: 'fail' }).exitCode === 0)

  // 8) 严重度降级 → 红。降级同样要记账：不判红的话基线会永远停在更严的旧值上，
  // 于是「这条洞已经降到 low」这件事永远不会进 review。
  const downgradedReport = write('downgraded.json', {
    'left-pad': [advisory('low', 'https://example.test/GHSA-aaaa')],
    ms: [advisory('low', 'https://example.test/GHSA-bbbb')],
  })
  const downgraded = evaluate({ report: downgradedReport, baseline: baselinePath, unseededPolicy: 'fail' })
  check('severity downgrade must fail', downgraded.exitCode === 1)
  check('severity downgrade must be labelled', downgraded.lines.join('\n').includes('降级'))

  rmSync(directory, { recursive: true, force: true })

  if (failures.length > 0) {
    for (const failure of failures) console.error(`self-test FAIL: ${failure}`)
    return 1
  }
  console.log('bun-audit-gate self-test: 8 scenarios passed')
  return 0
}

function main() {
  let options
  try {
    options = parseArgs(process.argv.slice(2))
  } catch (error) {
    console.error(`bun-audit-gate: ${error.message}`)
    return 2
  }
  if (options.selfTest) return runSelfTest()
  if (!options.report) {
    console.error('bun-audit-gate: --report is required (or pass --self-test)')
    return 2
  }

  let result
  try {
    result = evaluate(options)
  } catch (error) {
    console.error(`bun-audit-gate: ${error.message}`)
    return 2
  }
  for (const line of result.lines) {
    if (result.exitCode === 0) console.log(line)
    else console.error(line)
  }
  if (result.report) {
    writeSummary(options.summary, result.report, result.counts, result.totals, result.packageDrift ?? 0)
    if (options.annotate) annotate(result.report)
  }
  if (result.exitCode !== 0) console.error('::error::bun audit 棘轮门：出现未登记或已失效的 advisory，见上。')
  return result.exitCode
}

process.exit(main())
