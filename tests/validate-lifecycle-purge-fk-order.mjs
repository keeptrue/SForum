#!/usr/bin/env node
/**
 * task-4 验收片段：uninstall purge 的 FK 顺序探针。
 *
 * 给谁用：改 `bootstrap/extension_lifecycle_cleanup_postgres.go` 的 purge 路径、
 * 或验收 task-4 / task-6 的人。什么时候跑：老后交回时跑一次；之后每次动
 * commitPurge、extensions 的子表约束、publication 成员表时再跑。
 *
 * 它回答三个问题（都在一次性集群里，不碰开发库）：
 *   A. `extensions` 上的 RESTRICT 子表还是不是那 5 张（`confdeltype = 'r'`）——
 *      防止有人把 RESTRICT 偷改成 CASCADE 把拦截绕过去。
 *   B. "5 张表清空后就能删身份" 这个前提还成不成立——造一条被 publication 成员
 *      钉住版本的扩展，5 张子表留空后直接 `DELETE FROM extensions`，预期被
 *      `plugin_runtime_publication_members` 的 RESTRICT 拦下（23503）。
 *      这一层是 schema 级不可绕过的（members 上有 BEFORE DELETE 触发器），
 *      所以"硬删身份"不能作为修复目标，identityRetained 才是。
 *   C. identityRetained 路径有没有被测到并收敛：夹具是否真的插了 member 行、
 *      用例是否断言了身份保留、Go 用例是否绿。三者缺一都判不通过——
 *      夹具不插 member 行时，purge 用例会在"假场景"里通过。
 *   D. 收敛链在 finalizer 层是否闭合（物理层绿 ≠ 端到端收敛）：
 *      D1 `validateLifecycleCleanupPurgeReceipt` 是否无条件拒绝 identityPurged=false，
 *         或者只在 proof 带 retained 语义时放行（两个极端都算红：全拒 = 不收敛，
 *         全放 = 谁都能绕过证据校验）。
 *      D2 是否存在"pinned fixture → FinalizeLifecycleHostCleanup → Status==finalized"
 *         的端到端用例；静态找不到就报缺，不报通过。
 *      D3 `PhysicalPurgeCompleted` 两处赋值是否都诚实：提交路径绑 receipt.IdentityPurged，
 *         replay 路径（没有 proof body）如实留 false。写死 true、由 status 推导、或
 *         replay 编造 true 都算谎报。
 *
 *   E. 审计幂等：pinned 收敛后重复调用 finalize 不得重复写证据。用例自己断言之外，
 *      再从库里按 cleanupId 数一遍，确认"1"不是用例造出来的。
 *   F. 收敛判据护栏：`PhysicalPurgeComplete` 的字段说明必须写明它只描述物理清理，
 *      且生产代码里不得再有地方按它判收敛（否则同一类谎报会从别的入口回来）。
 *
 * D 段红是**被测实现的问题**，不是探针坏：探针跑完会打印"探针自身状态"一行区分两者。
 * 退出码：0 通过 / 1 被测实现有缺口（含 D 段红）/ 2 探针自身跑不起来（docker、go、迁移失败）。
 *
 * 用法：
 *   node tests/validate-lifecycle-purge-fk-order.mjs
 *   node tests/validate-lifecycle-purge-fk-order.mjs --skip-go    # 只跑 SQL 两段（秒级）
 *   node tests/validate-lifecycle-purge-fk-order.mjs --keep       # 保留探针容器便于手查
 */

import { spawnSync } from 'node:child_process'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { isAbsolute, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('..', import.meta.url)))
const apiDir = join(root, 'apps/api')
const PURGE_IMPL = 'apps/api/bootstrap/extension_lifecycle_cleanup_postgres.go'
const FK_TEST = 'apps/api/bootstrap/extension_lifecycle_cleanup_fk_order_test.go'
const EXPECTED_RESTRICT = [
  'attachment_storage_instances',
  'extension_builtin_removals',
  'extension_missing_artifact_removals',
  'extension_version_gc_removals',
  'notification_channel_selections'
]
const MEMBER_FK = 'plugin_runtime_publication_me_extension_version_id_extensi_fkey'

const options = { keep: false, skipGo: false }

function parseArgs(argv) {
  for (const arg of argv) {
    if (arg === '--help' || arg === '-h') {
      process.stdout.write('node tests/validate-lifecycle-purge-fk-order.mjs [--skip-go] [--keep]\n')
      process.exit(0)
    } else if (arg === '--skip-go') options.skipGo = true
    else if (arg === '--keep') options.keep = true
    else fail(`未知参数：${arg}`)
  }
}

function fail(message) {
  process.stderr.write(`${message}\n`)
  process.exit(2)
}

function run(command, args, runOptions = {}) {
  return spawnSync(command, args, {
    cwd: runOptions.cwd ?? root,
    encoding: 'utf8',
    timeout: runOptions.timeout ?? 300000,
    env: runOptions.env ?? process.env,
    maxBuffer: 32 * 1024 * 1024
  })
}

const container = `sforum-fk-probe-${process.pid}-${Date.now().toString(36)}`
let hostPort = ''

function cleanup() {
  if (options.keep) {
    process.stdout.write(`\n探针容器保留：${container}（host port ${hostPort}），用完手动 docker rm -f ${container}\n`)
    return
  }
  run('docker', ['rm', '-f', container])
}

function psql(sql, { allowFailure = false } = {}) {
  const result = run('docker', ['exec', '-i', container, 'psql', '-U', 'sforum', '-d', 'sforum_probe', '-v', 'ON_ERROR_STOP=1', '-tA', '-c', sql])
  if (result.status !== 0 && !allowFailure) {
    fail(`psql 失败：${(result.stderr || '').trim().split('\n').slice(-2).join(' | ')}`)
  }
  return { ok: result.status === 0, stdout: (result.stdout || '').trim(), stderr: (result.stderr || '').trim() }
}

function startCluster() {
  const docker = run('docker', ['version', '--format', '{{.Server.Version}}'])
  if (docker.status !== 0) fail('docker 不可用：本探针必须跑在一次性集群上，不碰开发库')

  const started = run('docker', [
    'run', '-d', '--name', container,
    '-e', 'POSTGRES_DB=sforum_probe', '-e', 'POSTGRES_USER=sforum', '-e', 'POSTGRES_PASSWORD=sforum',
    '-p', '127.0.0.1::5432',
    'postgres:17-alpine',
    '-c', 'fsync=off', '-c', 'synchronous_commit=off', '-c', 'full_page_writes=off'
  ])
  if (started.status !== 0) fail(`启动探针集群失败：${(started.stderr || '').trim()}`)

  const binding = run('docker', ['port', container, '5432/tcp'])
  const mapped = `${binding.stdout}`.trim()
  if (!mapped.startsWith('127.0.0.1:')) fail(`端口映射异常：${mapped}`)
  hostPort = mapped.slice('127.0.0.1:'.length)

  for (let attempt = 0; attempt < 60; attempt += 1) {
    const probe = run('docker', ['exec', container, 'psql', '-U', 'sforum', '-d', 'sforum_probe', '-tAc', 'SELECT 1'])
    if (probe.status === 0 && `${probe.stdout}`.trim() === '1') return
    run('sleep', ['1'])
  }
  fail(`探针集群未就绪：docker logs ${container}`)
}

function migrate() {
  const databaseURL = `postgres://sforum:sforum@127.0.0.1:${hostPort}/sforum_probe?sslmode=disable`
  const result = run('go', ['run', './cmd/migrate'], {
    cwd: apiDir,
    env: { ...process.env, DATABASE_URL: databaseURL, TMPDIR: process.env.TMPDIR || '/home/inkedus/.cache/gotmp' }
  })
  if (result.status !== 0) fail(`迁移失败：${(result.stderr || result.stdout || '').trim().split('\n').slice(-3).join(' | ')}`)
}

// ---------------------------------------------------------------------------
// A. 五张 RESTRICT 子表是否仍为 RESTRICT
// ---------------------------------------------------------------------------

function probeRestrictCatalog() {
  const rows = psql(`
    SELECT conrelid::regclass::text, confdeltype
    FROM pg_constraint
    WHERE contype = 'f' AND confrelid = 'extensions'::regclass
    ORDER BY confdeltype, 1
  `).stdout.split('\n').filter(Boolean).map((line) => {
    const [table, action] = line.split('|')
    return { table, action }
  })

  const restrict = rows.filter((row) => row.action === 'r').map((row) => row.table).sort()
  const cascades = rows.filter((row) => row.action === 'c').map((row) => row.table).sort()
  const missing = EXPECTED_RESTRICT.filter((table) => !restrict.includes(table))
  const extra = restrict.filter((table) => !EXPECTED_RESTRICT.includes(table))
  const tampered = rows.filter((row) => row.action !== 'r' && row.action !== 'c')

  const ok = missing.length === 0 && extra.length === 0 && tampered.length === 0
  return { ok, restrict, cascades, missing, extra, tampered }
}

// ---------------------------------------------------------------------------
// B. member 层拦截是否复现（"5 张表清完就能删身份" 的前提检验）
// ---------------------------------------------------------------------------

function probeMemberBlocking() {
  psql(`
    ALTER TABLE plugin_runtime_publication_members DISABLE TRIGGER USER;
    ALTER TABLE plugin_runtime_publications DISABLE TRIGGER USER;
    INSERT INTO extensions (id, type, name, status, source, is_system, is_deletable)
    VALUES ('probe.fk', 'plugin', 'FK Probe', 'disabled', 'uploaded', false, true);
    INSERT INTO extension_versions (extension_id, version, manifest, package_path, package_digest)
    VALUES ('probe.fk', '1.0.0', '{}'::jsonb, '/tmp/probe-fk', repeat('a', 64));
    INSERT INTO plugin_runtime_publications (revision, member_count, members_digest, reason)
    VALUES (9001, 1, repeat('b', 64), 'uninstall');
    INSERT INTO plugin_runtime_publication_members
      (publication_revision, extension_id, extension_version_id, extension_version, package_digest)
    SELECT 9001, 'probe.fk', id, '1.0.0', repeat('a', 64)
    FROM extension_versions WHERE extension_id = 'probe.fk';
    UPDATE extensions SET active_version_id = (SELECT id FROM extension_versions WHERE extension_id = 'probe.fk')
    WHERE id = 'probe.fk';
  `)

  // 5 张 RESTRICT 子表刻意留空：模拟 commitPurge 已经把五张表清完之后的状态。
  const blocked = psql('BEGIN; DELETE FROM extensions WHERE id = \'probe.fk\'; ROLLBACK;', { allowFailure: true })
  const reproduced = !blocked.ok && blocked.stderr.includes(MEMBER_FK)

  // 反向对照：把 member 行删掉（触发器已禁用）后同一 DELETE 必须成功，证明拦截来自 member 层。
  const control = psql(`
    BEGIN;
    DELETE FROM plugin_runtime_publication_members WHERE extension_id = 'probe.fk';
    DELETE FROM extensions WHERE id = 'probe.fk';
    SELECT 'control_ok';
    ROLLBACK;
  `, { allowFailure: true })
  const controlOK = control.ok && control.stdout.includes('control_ok')

  return {
    ok: reproduced && controlOK,
    reproduced,
    controlOK,
    error: blocked.ok ? '（DELETE 竟然成功：member 层拦截没有复现）' : blocked.stderr.split('\n').slice(-3).join(' | ')
  }
}

// ---------------------------------------------------------------------------
// C. identityRetained 路径是否被测到并收敛
// ---------------------------------------------------------------------------

function probeIdentityRetention() {
  if (!existsSync(join(root, FK_TEST))) {
    return { ok: false, fixture: false, assertion: false, goTest: 'missing', detail: `${FK_TEST} 不存在` }
  }
  const source = readFileSync(join(root, FK_TEST), 'utf8')
  // 夹具代表性：测试里必须真的造出 publication member 行（SQL 或 helper 均可）。
  const fixture = /plugin_runtime_publication_members/i.test(source)
  // 收敛断言：要么直接断言 IdentityRetained，要么有专门的"保留身份"用例。
  const assertion = /[Ii]dentityRetained|RetainsIdentity|retainedIdentity/i.test(source)

  if (options.skipGo) {
    return { ok: fixture && assertion, fixture, assertion, goTest: 'skipped', detail: '' }
  }
  const result = run('./scripts/test-api-isolated.sh', ['-count=1', '-run', 'TestLifecycleCleanupPurge', './bootstrap'], {
    cwd: root,
    timeout: 900000,
    env: { ...process.env, TMPDIR: process.env.TMPDIR || '/home/inkedus/.cache/gotmp' }
  })
  const output = `${result.stdout || ''}${result.stderr || ''}`
  // build failed 说明树在改（WIP）或符号没定义，与"用例红了"是两回事，分开报。
  const goTest = result.status === 0 ? 'pass' : /\[build failed\]/.test(output) ? 'build-failed' : 'fail'
  return {
    ok: fixture && assertion && goTest === 'pass',
    fixture,
    assertion,
    goTest,
    detail: goTest === 'pass' ? '' : output.trim().split('\n').slice(-6).join(' | ')
  }
}

// ---------------------------------------------------------------------------
// D. 收敛链是否在 finalizer 层闭合
// ---------------------------------------------------------------------------

/** 按大括号配对取出函数体，够用即可：这些文件的函数体没有裸花括号字符串。 */
function extractFuncBody(source, name) {
  const signature = new RegExp(`func ${name}\\s*\\(`, 'm').exec(source)
  if (!signature) return null
  const open = source.indexOf('{', signature.index)
  if (open < 0) return null
  let depth = 0
  for (let index = open; index < source.length; index += 1) {
    if (source[index] === '{') depth += 1
    else if (source[index] === '}') {
      depth -= 1
      if (depth === 0) return source.slice(open, index + 1)
    }
  }
  return null
}

function probeFinalizerChain() {
  // SFORUM_FINALIZER_PATH 只用于自检：拿一份合成实现确认 D1/D3 会翻绿，
  // 避免"探针永远红"这种更糟的假象。
  // 默认扫整个 Support/Extensions 包：函数被挪文件或导出改名都不该让探针失灵。
  const finalizerPath = process.env.SFORUM_FINALIZER_PATH
  let finalizer = ''
  if (finalizerPath) {
    const explicit = isAbsolute(finalizerPath) ? finalizerPath : join(root, finalizerPath)
    finalizer = existsSync(explicit) ? readFileSync(explicit, 'utf8') : ''
  } else {
    const packageDir = join(root, 'apps/api/app/Support/Extensions')
    finalizer = readdirSync(packageDir)
      .filter((name) => name.endsWith('.go') && !name.endsWith('_test.go'))
      .map((name) => readFileSync(join(packageDir, name), 'utf8'))
      .join('\n')
  }

  // D1：证据校验是否无条件拒绝 identityPurged=false；或者反过来谁都放行。
  const validateName = /func (ValidateLifecycleCleanupPurgeReceipt|validateLifecycleCleanupPurgeReceipt)\s*\(/.exec(finalizer)
  const validateBody = validateName ? extractFuncBody(finalizer, validateName[1]) : null
  // 判据不是"接受 false"，而是"保留语义下接受 false、其余情况仍要证据"：
  //   - 必须仍然检查 IdentityPurged（否则等于谁都放行）；
  //   - 必须存在 retained 语义门禁（否则身份保留永远被拒）；
  //   - 不能是"只要 !IdentityPurged 就拒"的无条件版本。
  const mentionsPurged = validateBody ? /receipt\.IdentityPurged/.test(validateBody) : null
  const retainedGate = validateBody ? /IdentityRetained|identityRetained|retainedIdentity/i.test(validateBody) : null
  const requiresIdentityPurged = validateBody
    ? /!\s*receipt\.IdentityPurged/.test(validateBody) && retainedGate === false
    : null
  const d1 = {
    ok: validateBody !== null && mentionsPurged === true && requiresIdentityPurged === false && retainedGate === true,
    present: validateBody !== null,
    requiresIdentityPurged,
    retainedGate,
    detail: validateBody === null
      ? '找不到 validateLifecycleCleanupPurgeReceipt（含导出名）；若刚被改名/移动，探针需要同步'
      : requiresIdentityPurged
        ? '无条件要求 receipt.IdentityPurged=true → 身份保留的 receipt 会被判 ErrLifecycleCleanupPurgeInvalid'
        : mentionsPurged === false
          ? '完全不检查 receipt.IdentityPurged → 任何 receipt 都能过'
          : retainedGate
            ? ''
            : '不再要求 identityPurged=true，但也没有 retained 语义门禁 → 任何 receipt 都能过'
  }

  // D2：端到端用例是否存在（pinned fixture → FinalizeLifecycleHostCleanup → finalized）。
  const bootstrapTests = readdirSync(join(root, 'apps/api/bootstrap'))
    .filter((name) => name.endsWith('_test.go'))
    .map((name) => readFileSync(join(root, 'apps/api/bootstrap', name), 'utf8'))
    .join('\n')
  const finalizeCalls = [...bootstrapTests.matchAll(/func (Test\w+)\(t \*testing\.T\)/g)]
  let pinnedEndToEnd = null
  for (const match of finalizeCalls) {
    const body = extractFuncBody(bootstrapTests, match[1])
    if (!body || !body.includes('FinalizeLifecycleHostCleanup')) continue
    if (!/publication_members|Pinned|Published|newLifecyclePurgeFixture\(t,\s*true\)/.test(body)) continue
    if (!/finalized/.test(body)) continue
    pinnedEndToEnd = match[1]
    break
  }
  const d2 = {
    ok: pinnedEndToEnd !== null,
    test: pinnedEndToEnd,
    detail: pinnedEndToEnd ? '' : '没有"pinned fixture → FinalizeLifecycleHostCleanup → Status==finalized"的用例'
  }

  // D3：身份保留时 PhysicalPurgeCompleted 不得声称"物理身份已被删"。
  // 通过条件是「表达式不可能是 true 在身份未删时」：
  //   - 写死 true            → 谎报（红）
  //   - 由 Status=="finalized" 推导 → 身份保留时必然 true（红）
  //   - 写死 false           → 诚实但会丢掉硬删路径的 true（红：丢信息）
  //   - 绑到 receipt.IdentityPurged / IdentityRetained → 正确（绿）
  const commitSide = /PhysicalPurgeCompleted:\s*receipt\.IdentityPurged/.test(finalizer)
  const replayAssignments = [...finalizer.matchAll(/PhysicalPurgeCompleted:\s*([^,\n]+)/g)]
    .map((match) => match[1].trim())
    .filter((expression) => !/receipt\.IdentityPurged/.test(expression))
  const replayHonest = replayAssignments.length > 0 && replayAssignments.every((expression) => expression === 'false')
  const d3 = {
    ok: commitSide && replayHonest,
    expression: replayAssignments.length ? replayAssignments.join(' | ') : '（无 replay 赋值）',
    detail: !commitSide
      ? '提交路径没有绑到 receipt.IdentityPurged → 硬删与保留会报同一个值'
      : !replayHonest
        ? `replay 路径应为常量 false（无 proof 证据），实际：${replayAssignments.join(' | ')}`
        : ''
  }

  return { ok: d1.ok && d2.ok && d3.ok, d1, d2, d3 }
}

// ---------------------------------------------------------------------------
// E. 审计幂等：finalizer 收敛后重复调用不得重复写证据（独立于用例自己的断言）
// ---------------------------------------------------------------------------

function probeAuditIdempotency() {
  if (options.skipGo) return { skipped: true }

  // testdb.Decide 只认带一次性集群标记的库；这里补上标记，让 Go 用例能连进来。
  psql(`COMMENT ON DATABASE postgres IS 'sforum.disposable-test-cluster.v1'`)
  const databaseURL = `postgres://sforum:sforum@127.0.0.1:${hostPort}/sforum_probe?sslmode=disable`
  const result = run('go', [
    'test', '-count=1',
    '-run', 'TestFinalizerConvergesRetainedIdentityPurge|TestFinalizerRejectsUnjustifiedIdentityRetention',
    './bootstrap'
  ], {
    cwd: apiDir,
    timeout: 900000,
    env: {
      ...process.env,
      DATABASE_URL: databaseURL,
      SFORUM_TEST_DATABASE_URL: databaseURL,
      TMPDIR: process.env.TMPDIR || '/home/inkedus/.cache/gotmp'
    }
  })
  const output = `${result.stdout || ''}${result.stderr || ''}`
  const goTest = result.status === 0 ? 'pass' : /\[build failed\]/.test(output) ? 'build-failed' : 'fail'

  // 用例自己的断言可能被写成恒真；这里直接从库里数，验证"1"不是造出来的。
  const duplicates = Number(psql(`
    SELECT count(*) FROM (
      SELECT metadata->>'cleanupId' AS cleanup_id, count(*) AS n
      FROM audit_events
      WHERE action = 'extension.uninstall' AND metadata->>'cleanupId' IS NOT NULL
      GROUP BY 1 HAVING count(*) > 1
    ) duplicated
  `).stdout || '0')
  const total = Number(psql(`
    SELECT count(*) FROM audit_events
    WHERE action = 'extension.uninstall' AND metadata->>'cleanupId' IS NOT NULL
  `).stdout || '0')

  return {
    ok: goTest === 'pass' && duplicates === 0 && total > 0,
    goTest,
    duplicates,
    total,
    detail: goTest === 'pass' ? '' : output.trim().split('\n').slice(-5).join(' | ')
  }
}

// ---------------------------------------------------------------------------
// F. 语义护栏：字段说明 + 消费点不得再按 PhysicalPurgeComplete 判收敛
// ---------------------------------------------------------------------------

function probeConvergenceContract() {
  const typesPath = join(root, 'apps/api/app/Models/Extensions/types.go')
  const types = existsSync(typesPath) ? readFileSync(typesPath, 'utf8') : ''
  const documented = /PhysicalPurgeComplete[^\n]*\n(?:\s*\/\/[^\n]*\n)*\s*\/\/[^\n]*(物理|identity|收敛)/.test(types) ||
    /\/\/[^\n]*(物理清理|physical purge)[^\n]*\n\s*PhysicalPurgeComplete/.test(types)

  // 生产代码里还有谁按这个字段判收敛：只允许"透传赋值"，不允许参与条件。
  const productionFiles = []
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const child = join(dir, entry.name)
      if (entry.isDirectory()) walk(child)
      else if (entry.name.endsWith('.go') && !entry.name.endsWith('_test.go')) productionFiles.push(child)
    }
  }
  walk(join(root, 'apps/api'))
  const conditionUsers = []
  for (const file of productionFiles) {
    const content = readFileSync(file, 'utf8')
    for (const [index, line] of content.split('\n').entries()) {
      if (!/PhysicalPurgeComplete|PhysicalPurgeCompleted/.test(line)) continue
      if (/^\s*\/\//.test(line)) continue
      // 赋值/透传/字段声明/注释不算消费；参与 if/&&/||/! 才算。
      if (/^\s*(if|for|switch)\b.*PhysicalPurge/.test(line) || /(&&|\|\||!)\s*\w*\.?PhysicalPurge/.test(line)) {
        conditionUsers.push(`${relative(root, file)}:${index + 1}`)
      }
    }
  }
  return { ok: documented && conditionUsers.length === 0, documented, conditionUsers }
}

// ---------------------------------------------------------------------------
// 输出
// ---------------------------------------------------------------------------

function main() {
  parseArgs(process.argv.slice(2))
  const label = (ok) => (ok ? 'PASS' : 'FAIL')

  startCluster()
  process.on('exit', cleanup)
  process.on('SIGINT', () => process.exit(130))

  migrate()

  const catalog = probeRestrictCatalog()
  process.stdout.write(`[A] 五张 RESTRICT 子表仍是 RESTRICT：${label(catalog.ok)}\n`)
  process.stdout.write(`    RESTRICT(${catalog.restrict.length})：${catalog.restrict.join(', ') || '（无）'}\n`)
  process.stdout.write(`    CASCADE(${catalog.cascades.length})：${catalog.cascades.join(', ') || '（无）'}\n`)
  if (catalog.missing.length) process.stdout.write(`    缺失：${catalog.missing.join(', ')}\n`)
  if (catalog.extra.length) process.stdout.write(`    多出：${catalog.extra.join(', ')}（更新探针预期清单）\n`)
  if (catalog.tampered.length) process.stdout.write(`    非 r/c 动作：${catalog.tampered.map((r) => `${r.table}=${r.action}`).join(', ')}\n`)

  const member = probeMemberBlocking()
  process.stdout.write(`[B] member 层拦截复现（硬删身份不可行）：${label(member.ok)}\n`)
  process.stdout.write(`    5 张子表留空后 DELETE extensions → ${member.reproduced ? '被 23503 拦下（预期）' : '竟然成功（约束被改？）'}\n`)
  process.stdout.write(`    反向对照（删 member 后同一 DELETE）：${member.controlOK ? '成功（证明拦截来自 member 层）' : '未成功'}\n`)
  if (!member.reproduced) process.stdout.write(`    错误：${member.error}\n`)

  const retention = probeIdentityRetention()
  const retentionSkipped = retention.goTest === 'skipped'
  process.stdout.write(
    `[C] identityRetained 路径被测到并收敛：${retentionSkipped ? 'SKIP（--skip-go 只做静态检查）' : label(retention.ok)}\n`
  )
  process.stdout.write(`    夹具插入 publication member 行：${retention.fixture ? '是' : '否（用例会在假场景里通过）'}\n`)
  process.stdout.write(`    用例断言身份保留：${retention.assertion ? '是' : '否'}\n`)
  process.stdout.write(`    Go 用例（TestLifecycleCleanupPurge）：${retention.goTest}\n`)
  if (retention.detail) process.stdout.write(`    ${retention.detail}\n`)

  const chain = probeFinalizerChain()
  process.stdout.write(`[D] 收敛链在 finalizer 层闭合：${label(chain.ok)}\n`)
  process.stdout.write(`    D1 证据校验（${chain.d1.present ? 'found' : 'missing'}）：${label(chain.d1.ok)}\n`)
  if (chain.d1.detail) process.stdout.write(`        ${chain.d1.detail}\n`)
  process.stdout.write(`    D2 pinned 端到端用例：${chain.d2.ok ? `有（${chain.d2.test}）` : '缺'}\n`)
  if (chain.d2.detail) process.stdout.write(`        ${chain.d2.detail}\n`)
  process.stdout.write(`    D3 PhysicalPurgeCompleted（${chain.d3.expression || '未找到'}）：${label(chain.d3.ok)}\n`)
  if (chain.d3.detail) process.stdout.write(`        ${chain.d3.detail}\n`)

  const audit = probeAuditIdempotency()
  if (audit.skipped) {
    process.stdout.write('[E] 审计幂等（重复 finalize 不重复写证据）：SKIP（--skip-go）\n')
  } else {
    process.stdout.write(`[E] 审计幂等（重复 finalize 不重复写证据）：${label(audit.ok)}\n`)
    process.stdout.write(`    Go 用例：${audit.goTest}；同一 cleanupId 的 extension.uninstall 审计行：${audit.total} 行，重复组 ${audit.duplicates} 个\n`)
    if (audit.detail) process.stdout.write(`        ${audit.detail}\n`)
  }

  const contract = probeConvergenceContract()
  process.stdout.write(`[F] 收敛判据护栏（字段说明 + 消费点）：${label(contract.ok)}\n`)
  process.stdout.write(`    字段说明标注语义：${contract.documented ? '有' : '缺'}\n`)
  process.stdout.write(`    按 PhysicalPurge* 判收敛的生产代码：${contract.conditionUsers.length ? contract.conditionUsers.join(', ') : '无'}\n`)

  const auditCounted = !audit.skipped
  const failed = [!catalog.ok, !member.ok, (!retentionSkipped && !retention.ok), !chain.ok, (auditCounted && !audit.ok), !contract.ok].filter(Boolean).length
  const verdict = failed > 0
    ? `task-4 FK 顺序验收不达标（${failed} 项）`
    : retentionSkipped
      ? 'A/B/D 通过；C/E 未验证（去掉 --skip-go 才算完整验收）'
      : 'task-4 FK 顺序验收通过'
  process.stdout.write(`\n探针自身状态：正常（docker、迁移、查询、用例全部执行完毕）\n`)
  process.stdout.write(`${verdict}\n`)
  if (failed > 0) process.stdout.write('上面的红项是被测实现的问题，不是探针坏了。\n')
  process.exitCode = failed === 0 ? 0 : 1
}

main()
