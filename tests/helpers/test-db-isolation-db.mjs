/**
 * 开发库只读观测：只允许 SELECT，连接方式固定为
 * `docker exec -e PGPASSWORD <container> psql`（密码不落 argv）。
 * 由 tests/validate-test-db-isolation.mjs 调用。
 */

import { spawnSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'

export const DEV_TABLES = ['topics', 'posts', 'users']
export const EXTENSION_PREFIXES = ['test.%', 'selection.pg.%', 'p4.e2e.%']
const WRITE_KEYWORDS = /\b(insert|update|delete|drop|alter|truncate|create|grant|revoke|copy|vacuum|comment|refresh)\b/i

export function fail(message) {
  const error = new Error(message)
  error.fatal = true
  throw error
}

export function parseEnvFile(path) {
  const env = {}
  if (!existsSync(path)) return env
  for (const raw of readFileSync(path, 'utf8').split('\n')) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const match = /^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/.exec(line)
    if (!match) continue
    let value = match[2].trim()
    if (value.length >= 2) {
      const first = value[0]
      const last = value[value.length - 1]
      if ((first === '"' && last === '"') || (first === "'" && last === "'")) value = value.slice(1, -1)
    }
    env[match[1]] = value
  }
  return env
}

export function assertReadOnly(sql) {
  const normalized = sql.trim()
  if (!/^(select|with)\b/i.test(normalized)) fail(`内部错误：只允许 SELECT，收到 ${normalized.slice(0, 40)}`)
  if (/;\s*\S/.test(normalized)) fail('内部错误：只允许单条 SELECT 语句')
  if (WRITE_KEYWORDS.test(normalized)) fail(`内部错误：SELECT 里出现写操作关键字：${normalized.slice(0, 60)}`)
  return normalized
}

export function runDocker(args, runOptions = {}) {
  return spawnSync('docker', args, {
    encoding: 'utf8',
    timeout: runOptions.timeout ?? 30000,
    env: runOptions.env ?? process.env,
    maxBuffer: 8 * 1024 * 1024
  })
}

export function devDatabase(envPath) {
  const env = parseEnvFile(envPath)
  const databaseURL = process.env.SFORUM_DEV_DATABASE_URL || env.DATABASE_URL || ''
  if (!databaseURL) fail('.env 缺少 DATABASE_URL：无法定位开发库（本脚本只读它）')
  let parsed
  try {
    parsed = new URL(databaseURL)
  } catch {
    fail(`DATABASE_URL 不是合法 URL：${databaseURL.replace(/:[^:@/]*@/, ':***@')}`)
  }
  return {
    host: parsed.hostname,
    port: parsed.port || '5432',
    database: decodeURIComponent(parsed.pathname.replace(/^\//, '')),
    user: decodeURIComponent(parsed.username || env.POSTGRES_USER || 'sforum'),
    password: env.POSTGRES_PASSWORD || '',
    container: process.env.SFORUM_DEV_DB_CONTAINER || 'sforum-postgres'
  }
}

export function psql(dev, sql) {
  const statement = assertReadOnly(sql)
  if (!dev.password) fail('.env 缺少 POSTGRES_PASSWORD：无法以 docker exec -e PGPASSWORD 连接开发库')
  const result = runDocker(
    ['exec', '-e', 'PGPASSWORD', '-i', dev.container,
      'psql', '-U', dev.user, '-d', dev.database, '-v', 'ON_ERROR_STOP=1', '-tA', '-c', statement],
    { env: { ...process.env, PGPASSWORD: dev.password } }
  )
  if (result.error) fail(`docker exec 失败：${result.error.message}`)
  if (result.status !== 0) {
    fail(`只读查询失败（exit ${result.status}）：${(result.stderr || '').trim().split('\n').slice(-3).join(' | ')}`)
  }
  return result.stdout.trim()
}

export function assertDevTarget(dev) {
  if (!['127.0.0.1', 'localhost'].includes(dev.host)) {
    fail(`DATABASE_URL 指向 ${dev.host}，不是本机开发库；运行时隔离检查无意义`)
  }
  const binding = runDocker(['port', dev.container, '5432/tcp'])
  if (binding.status !== 0) fail(`容器 ${dev.container} 不存在或未映射端口：${(binding.stderr || '').trim()}`)
  const mapped = `${binding.stdout}`.trim()
  if (mapped !== `127.0.0.1:${dev.port}`) {
    fail(`容器 ${dev.container} 映射 ${mapped}，与 DATABASE_URL 端口 ${dev.port} 不一致`)
  }
}

/** 只读快照：三张业务表行数、extensions 测试前缀行集合、写入统计（作为旁证）。 */
export function snapshotDevDatabase(dev) {
  const counts = {}
  const countSQL = DEV_TABLES
    .map((table) => `SELECT '${table}' AS t, count(*)::text AS c FROM ${table}`)
    .join(' UNION ALL ')
  for (const row of psql(dev, countSQL).split('\n')) {
    const [table, value] = row.split('|')
    if (table) counts[table] = Number(value)
  }

  const prefixSQL = `SELECT id FROM extensions WHERE ${EXTENSION_PREFIXES.map((p) => `id LIKE '${p}'`).join(' OR ')} ORDER BY id`
  const prefixOutput = psql(dev, prefixSQL)

  const stats = {}
  const tables = DEV_TABLES.concat('extensions')
  const statSQL = `SELECT relname, n_tup_ins, n_tup_del FROM pg_stat_user_tables WHERE relname IN (${tables.map((t) => `'${t}'`).join(',')})`
  for (const row of psql(dev, statSQL).split('\n')) {
    const [table, inserted, deleted] = row.split('|')
    if (table) stats[table] = { inserted: Number(inserted), deleted: Number(deleted) }
  }

  return {
    counts,
    extensionRows: prefixOutput ? prefixOutput.split('\n').filter(Boolean) : [],
    stats,
    capturedAt: new Date().toISOString()
  }
}

export function diffSnapshots(before, after) {
  const failures = []
  const notes = []
  for (const table of DEV_TABLES) {
    const delta = after.counts[table] - before.counts[table]
    if (delta !== 0) {
      failures.push(`${table}: ${before.counts[table]} → ${after.counts[table]}（Δ${delta > 0 ? '+' : ''}${delta}）`)
    }
  }
  const added = after.extensionRows.filter((id) => !before.extensionRows.includes(id))
  const removed = before.extensionRows.filter((id) => !after.extensionRows.includes(id))
  if (added.length > 0) failures.push(`extensions 新增测试前缀行：${added.slice(0, 10).join(', ')}${added.length > 10 ? ` …共 ${added.length} 行` : ''}`)
  if (removed.length > 0) failures.push(`extensions 丢失测试前缀行：${removed.slice(0, 10).join(', ')}`)

  // 计数一致但统计在动 = 测试写了又清干净，或开发 API 在写；两种都要人确认来源。
  for (const table of Object.keys(after.stats)) {
    const deltaIns = after.stats[table].inserted - (before.stats[table]?.inserted ?? 0)
    const deltaDel = after.stats[table].deleted - (before.stats[table]?.deleted ?? 0)
    if (deltaIns !== 0 || deltaDel !== 0) {
      notes.push(`${table} 期间写入统计变化（ins +${deltaIns}, del +${deltaDel}）：计数未变也要确认来源（测试清理 / 开发 API）`)
    }
  }
  return { failures, notes }
}
