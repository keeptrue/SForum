#!/usr/bin/env node
/**
 * 契约状态码 parity 门的**集合相等**样本测试（G3 验收项）。
 *
 * 门本身只保证一个方向：实现可达 ⊆ 契约声明。契约多声明（中间件统一兜的 401/403、
 * 范围响应 default/2XX）是允许的，所以「子集成立」不等于「两边说的是同一件事」。
 * 这条测试挑三个 operation 做**双向相等**比对，用来证明门的实现侧集合确实已经收敛到
 * 契约语义上，而不是靠 ∅ ⊆ 任意 或 少算几个码混过棘轮：
 *
 *   1. GET  /api/v1/categories          公开读：200 + requireGuestRead 的 401
 *   2. PUT  /api/v1/profile             必登写：200 + RequireActor 的 401/503 + 422
 *   3. POST /api/v1/auth/tokens         必登创建：201 + 401/403/422/503
 *
 * 三条分别覆盖「公开读守卫」「必登写守卫 + 依赖故障」「创建语义（201 而非 200）」，
 * 都是 G3 之前掉在退化区或只能靠白名单硬编码的形态。
 *
 * **契约侧独立解析**：这里不复用门里的解析器，也不读门打印的「已声明」列。门的实现
 * 与契约的声明各算一遍再比对，才能抓到「门和契约一起漂移」的情况。
 *
 * 用法：node tests/validate-contract-parity-sample-equality.test.mjs
 */

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(SCRIPT_DIR, '..');
const GATE = path.join(SCRIPT_DIR, 'validate-contract-status-parity.mjs');
const CONTRACT_ENTRY = path.join(ROOT, 'contracts', 'openapi.yaml');
const CONTRACT_ROOT = path.join(ROOT, 'contracts');

const SAMPLES = [
  { operation: 'GET /api/v1/categories', note: '公开读：200 + requireGuestRead 的 401' },
  { operation: 'PUT /api/v1/profile', note: '必登写：200 + RequireActor 的 401/503 + 422' },
  { operation: 'POST /api/v1/auth/tokens', note: '必登创建：201 + 401/403/422/503' },
];

// ---------------------------------------------------------------------------
// 契约侧：独立解析（不共享门里的实现）
// ---------------------------------------------------------------------------

function normalizePath(target) {
  const value = target
    .replace(/\{[^}]+\}/g, ':param')
    .replace(/:[A-Za-z0-9_]+/g, ':param')
    .replace(/\/+$/, '');
  return value === '' ? '/' : value;
}

function readLines(file) {
  return fs.readFileSync(file, 'utf8').split(/\r?\n/);
}

function indentOf(line) {
  const match = /^[ \t]*/.exec(line);
  return match ? match[0].length : 0;
}

function isBlank(line) {
  return line.trim() === '' || line.trim().startsWith('#');
}

/** contracts/openapi.yaml 的 paths 索引 → [{ path, file, pointer }] */
function parseIndex() {
  const lines = readLines(CONTRACT_ENTRY);
  const start = lines.findIndex((line) => /^paths:\s*$/.test(line));
  assert.ok(start >= 0, 'contracts/openapi.yaml 缺少 paths 段');
  const entries = [];
  for (let i = start + 1; i < lines.length; i += 1) {
    const line = lines[i];
    if (!isBlank(line) && indentOf(line) === 0) break;
    if (isBlank(line) || indentOf(line) !== 2) continue;
    const key = /^ {2}(?:"([^"]+)"|'([^']+)'|(\/[^:]*?))\s*:\s*$/.exec(line);
    if (!key) continue;
    const ref = /^\s{4}"?\$ref"?\s*:\s*"?([^"\s]+)"?\s*$/.exec(lines[i + 1] || '');
    assert.ok(ref, `paths."${key[1] ?? key[2] ?? key[3]}" 没有 $ref`);
    const [file, pointer] = ref[1].split('#');
    entries.push({ path: key[1] ?? key[2] ?? key[3], file, pointer: (pointer || '').replace(/^\//, '') });
    i += 1;
  }
  return entries;
}

/** 收集契约侧每个 operation 的响应码（数字键），按 `METHOD 归一化路径` 建索引。 */
function readDeclaredCodes() {
  const fileCache = new Map();
  const declared = new Map();
  for (const entry of parseIndex()) {
    const file = path.resolve(CONTRACT_ROOT, entry.file);
    if (!fileCache.has(file)) fileCache.set(file, readLines(file));
    const lines = fileCache.get(file);
    const start = lines.findIndex((line) => line === `${entry.pointer}:`);
    assert.ok(start >= 0, `${entry.file} 找不到 path item "${entry.pointer}"`);
    let end = lines.length;
    for (let i = start + 1; i < lines.length; i += 1) {
      if (!isBlank(lines[i]) && indentOf(lines[i]) === 0) {
        end = i;
        break;
      }
    }
    const block = lines.slice(start + 1, end);
    for (let i = 0; i < block.length; i += 1) {
      const method = /^\s*([a-z]+):\s*$/.exec(block[i]);
      if (!method) continue;
      const upper = method[1].toUpperCase();
      if (!['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'].includes(upper)) continue;
      const methodIndent = indentOf(block[i]);
      let methodEnd = block.length;
      for (let j = i + 1; j < block.length; j += 1) {
        if (!isBlank(block[j]) && indentOf(block[j]) <= methodIndent) {
          methodEnd = j;
          break;
        }
      }
      const body = block.slice(i + 1, methodEnd);
      const childIndent = Math.min(...body.filter((line) => !isBlank(line)).map((line) => indentOf(line)));
      const responsesIndex = body.findIndex(
        (line) => indentOf(line) === childIndent && /^\s*responses:/.test(line),
      );
      assert.ok(responsesIndex >= 0, `${entry.file}#/${entry.pointer} ${upper} 缺少 responses`);
      const responsesIndent = indentOf(body[responsesIndex]);
      const codes = new Set();
      let hasRange = false;
      for (let j = responsesIndex + 1; j < body.length; j += 1) {
        const line = body[j];
        if (isBlank(line)) continue;
        if (indentOf(line) <= responsesIndent) break;
        const key = /^\s*'?([0-9A-Za-z]+)'?\s*:/.exec(line);
        if (!key) continue;
        if (/^\d{3}$/.test(key[1])) codes.add(Number(key[1]));
        else if (/^(default|[1-5]XX)$/i.test(key[1])) hasRange = true;
      }
      declared.set(`${upper} ${normalizePath('/api/v1' + entry.path)}`, { codes, hasRange });
    }
  }
  return declared;
}

// ---------------------------------------------------------------------------
// 实现侧：跑门，读它打印的逐 operation 集合
// ---------------------------------------------------------------------------

function runGate(args) {
  const result = spawnSync(process.execPath, [GATE, ...args], {
    cwd: ROOT,
    encoding: 'utf8',
    timeout: 240_000,
  });
  if (result.error) throw result.error;
  return { status: result.status, stdout: result.stdout || '', stderr: result.stderr || '' };
}

function parseImplementationCodes(stdout) {
  const marker = stdout.indexOf('[parity-operation-codes]');
  assert.ok(marker >= 0, '门没有打印 [parity-operation-codes] 块（诊断开关被删或改名？）');
  const codes = new Map();
  for (const line of stdout.slice(marker + '[parity-operation-codes]'.length).split('\n')) {
    const match = /^([A-Z]+) (\S+) => (.*)$/.exec(line.trim());
    if (!match) continue;
    codes.set(`${match[1]} ${match[2]}`, match[3] === '' ? [] : match[3].split(',').map(Number));
  }
  return codes;
}

const failures = [];
let passed = 0;

function check(name, fn) {
  try {
    fn();
    passed += 1;
    console.log(`  ok  ${name}`);
  } catch (error) {
    failures.push(`${name}：${error.message}`);
  }
}

// ---------------------------------------------------------------------------

const gateRun = runGate(['--print-operation-codes']);
const declared = readDeclaredCodes();
const implementation = parseImplementationCodes(gateRun.stdout);

check('门自身为绿（相等比对不能建在红着的门上）', () => {
  assert.equal(gateRun.status, 0, `门退出码 ${gateRun.status}\n${gateRun.stderr}`);
});

for (const sample of SAMPLES) {
  check(`${sample.operation} 契约集合 == 实现可达集合（${sample.note}）`, () => {
    const contract = declared.get(sample.operation);
    assert.ok(contract, `契约里找不到 ${sample.operation}`);
    assert.equal(contract.hasRange, false, `${sample.operation} 用了 default/范围响应，无法做精确集合比对`);
    const impl = implementation.get(sample.operation);
    assert.ok(impl, `门没有映射到 ${sample.operation}（实现侧找不到该路由，或诊断输出被改）`);

    const declaredCodes = [...contract.codes].sort((a, b) => a - b);
    const implCodes = [...impl].sort((a, b) => a - b);
    assert.equal(
      implCodes.join(','),
      declaredCodes.join(','),
      `${sample.operation}：实现可达=[${implCodes.join(',')}] 契约声明=[${declaredCodes.join(',')}] —— ` +
        '两边必须逐码相等；实现侧少算说明识别面又退化了，契约多声明说明该补的码还没补',
    );
  });
}

check('样本集合非平凡：至少一条含 401，且不是「只有 200」', () => {
  // 防止样本被换成三条平凡 operation（例如只有 200 的健康检查）后测试变成空转。
  const withGuard = SAMPLES.filter((sample) => (implementation.get(sample.operation) || []).includes(401));
  assert.ok(withGuard.length >= 2, `只有 ${withGuard.length} 条样本含 401，样本已退化成平凡集合`);
  const created = SAMPLES.filter((sample) => (implementation.get(sample.operation) || []).includes(201));
  assert.equal(created.length, 1, '样本里应当恰好有一条创建语义（201），用来钉住「不是所有成功都是 200」');
});

if (failures.length > 0) {
  console.error(`\n[parity-sample-equality] 失败 ${failures.length} 项：`);
  for (const message of failures) console.error(`  ✗ ${message}`);
  process.exitCode = 1;
} else {
  console.log(
    `[parity-sample-equality] 通过：${passed} 项（三条样本的实现可达集合与契约声明逐码相等，契约侧独立解析）`,
  );
}
