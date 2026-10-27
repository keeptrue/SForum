/**
 * 静态扫描：找出仍把 DATABASE_URL 当测试库读取的 *_test.go。
 * 由 tests/validate-test-db-isolation.mjs 调用，也可单独导入做快速检查。
 */

import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { join, relative } from 'node:path'

export const ALLOWED_KINDS = new Set(['subprocess-forwarding', 'guarded-read', 'non-test-database'])

const ENV_READ_RE = /(?:os\.)?(?:Getenv|LookupEnv)\s*\(\s*[`"]DATABASE_URL[`"]\s*\)/
const GENERIC_READ_RE = /[A-Za-z_][A-Za-z0-9_.]*\s*\(\s*[`"]DATABASE_URL[`"]\s*[,)]/
const FORWARD_RE = /[`"]DATABASE_URL=/
const ALIAS_RE = /^\s*(?:const\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?::=|=)\s*[`"]DATABASE_URL[`"]\s*$/

export function walkGoTestFiles(directory, out = []) {
  if (!existsSync(directory)) return out
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const child = join(directory, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === 'node_modules' || entry.name === 'tmp' || entry.name === 'vendor') continue
      walkGoTestFiles(child, out)
      continue
    }
    if (entry.name.endsWith('_test.go')) out.push(child)
  }
  return out
}

/**
 * 逐行判定，避免把注释与「向子进程传参」误判成读取测试库。
 * 返回 reads（读测试库的行号）、forwarded（DATABASE_URL= 透传的行号）。
 */
export function scanTestFile(absolute, root) {
  const content = readFileSync(absolute, 'utf8')
  const lines = content.replaceAll('\r\n', '\n').split('\n')
  const reads = new Set()
  const forwarded = []
  const aliases = new Set()

  for (const [index, line] of lines.entries()) {
    const trimmed = line.trim()
    if (!trimmed || trimmed.startsWith('//')) continue
    if (FORWARD_RE.test(line)) {
      forwarded.push(index + 1)
      continue
    }
    const alias = ALIAS_RE.exec(line)
    if (alias) aliases.add(alias[1])
    if (ENV_READ_RE.test(line) || GENERIC_READ_RE.test(line)) reads.add(index + 1)
  }

  // 间接写法：const dbEnv = "DATABASE_URL" 后再 os.Getenv(dbEnv)。
  if (aliases.size > 0) {
    for (const [index, line] of lines.entries()) {
      if (line.trim().startsWith('//')) continue
      for (const alias of aliases) {
        if (new RegExp(`(?:os\\.)?(?:Getenv|LookupEnv)\\s*\\(\\s*${alias}\\b`).test(line)) reads.add(index + 1)
      }
    }
  }

  return {
    path: relative(root, absolute).replaceAll('\\', '/'),
    reads: [...reads].sort((a, b) => a - b),
    forwarded,
    usesIsolatedEnv: /[`"]SFORUM_TEST_DATABASE_URL[`"]/.test(content),
    usesTestdb: /internal\/testdb/.test(content)
  }
}

export function loadAllowlist(allowlistPath, root) {
  if (!existsSync(allowlistPath)) return { entries: [], byFile: new Map(), warnings: [] }
  let parsed
  try {
    parsed = JSON.parse(readFileSync(allowlistPath, 'utf8'))
  } catch (error) {
    throw new Error(`无法解析 ${allowlistPath}：${error.message}`)
  }
  const warnings = []
  const byFile = new Map()
  const entries = Array.isArray(parsed.entries) ? parsed.entries : []
  for (const entry of entries) {
    if (typeof entry.file !== 'string' || !entry.file) {
      warnings.push('allowlist 条目缺少 file 字段，已忽略')
      continue
    }
    if (!ALLOWED_KINDS.has(entry.kind)) {
      warnings.push(`${entry.file}: kind=${entry.kind} 不在允许集合 ${[...ALLOWED_KINDS].join('/')}，已忽略`)
      continue
    }
    if (typeof entry.reason !== 'string' || entry.reason.trim().length < 12) {
      warnings.push(`${entry.file}: reason 缺失或过短（需 ≥12 字说明为什么安全），已忽略`)
      continue
    }
    if (!existsSync(join(root, entry.file))) {
      warnings.push(`${entry.file}: 文件不存在，条目已忽略`)
      continue
    }
    byFile.set(entry.file, entry)
  }
  return { entries, byFile, warnings }
}
