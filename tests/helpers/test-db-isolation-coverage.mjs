/**
 * 从 `go test -json` 日志里数「实际执行了多少个测试」，并与基线比较。
 * 由 tests/validate-test-db-isolation.mjs 调用；单独导入可用合成日志做自检。
 */

import { createReadStream } from 'node:fs'
import { createInterface } from 'node:readline'

/**
 * 只统计顶层测试（子测试归并到父测试），跳过非 JSON 行（隔离脚本自己的 echo）。
 * skips 只记录「以 DATABASE_URL 缺失为由跳过」的测试：隔离环境就绪时它们必须为 0。
 */
export async function countExecutedTests(logPath) {
  const perPackage = new Map()
  const skips = new Map()
  // skip 事件本身不带 Output：跳过原因在前面的 output 事件里，必须按测试攒起来。
  const pendingOutput = new Map()
  let totalPass = 0
  const reader = createInterface({ input: createReadStream(logPath, { encoding: 'utf8' }), crlfDelay: Infinity })
  for await (const line of reader) {
    if (!line.startsWith('{')) continue
    let event
    try {
      event = JSON.parse(line)
    } catch {
      continue
    }
    if (!event.Package || !event.Test || event.Test.includes('/')) continue
    const key = `${event.Package}\u0000${event.Test}`
    if (event.Action === 'output') {
      pendingOutput.set(key, (pendingOutput.get(key) ?? '') + (event.Output ?? ''))
      continue
    }
    if (event.Action === 'pass') {
      totalPass += 1
      perPackage.set(event.Package, (perPackage.get(event.Package) ?? 0) + 1)
      pendingOutput.delete(key)
    } else if (event.Action === 'skip') {
      if (/DATABASE_URL/.test(pendingOutput.get(key) ?? '')) {
        skips.set(event.Package, (skips.get(event.Package) ?? 0) + 1)
      }
      pendingOutput.delete(key)
    }
  }
  const sorted = (map) => Object.fromEntries([...map.entries()].sort(([a], [b]) => a.localeCompare(b)))
  return { totalPass, perPackage: sorted(perPackage), skips: sorted(skips) }
}

/** 总量与逐包都必须 ≥ 基线：总量可能被「一个包涨、另一个包跌」掩盖。 */
export function compareToBaseline(measured, baseline) {
  const failures = []
  if (measured.totalPass < baseline.totalTests) {
    failures.push(`执行测试数 ${measured.totalPass} < 基线 ${baseline.totalTests}（Δ${measured.totalPass - baseline.totalTests}）`)
  }
  for (const [pkg, expected] of Object.entries(baseline.perPackage ?? {})) {
    const actual = measured.perPackage[pkg] ?? 0
    if (actual < expected) failures.push(`${pkg}: ${actual} < 基线 ${expected}`)
  }
  const skipped = Object.entries(measured.skips)
  const skipTotal = skipped.reduce((sum, [, count]) => sum + count, 0)
  if (skipTotal > 0) {
    failures.push(
      `隔离环境已就绪但仍有 ${skipTotal} 个测试以 DATABASE_URL 缺失为由跳过：` +
      skipped.slice(0, 8).map(([pkg, count]) => `${pkg}×${count}`).join(', ')
    )
  }
  return failures
}
