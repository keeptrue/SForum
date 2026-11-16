#!/usr/bin/env node
/**
 * 两门等价性测试：parity 门与可达性门必须对同一棵树得出同样的分母与同样的退化区。
 *
 * 为什么要这条测试：可达性门把 parity 门的「状态码集合为空」规则复刻了一遍，用来量出
 * 退化区。复刻一旦漂移，两边数字就会各说各话 —— 而人工比对两个 194/435 是保不住的。
 *
 * **刻意不共享代码**：本测试分别以子进程跑两道门，只比对它们各自打印的结果。共享实现
 * 会让「互相独立复核」失去意义 —— 同一个 bug 在两边同时出现就查不出来了。
 * 测试自己的路径归一化也是独立的一份（故意重复），这样归一化漂移同样会被抓到。
 *
 * 用法：node tests/validate-contract-reachability-equivalence.test.mjs
 */

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(SCRIPT_DIR, '..');
const PARITY = path.join(SCRIPT_DIR, 'validate-contract-status-parity.mjs');
const REACHABILITY = path.join(SCRIPT_DIR, 'validate-contract-reachability.mjs');

const failures = [];

function run(script, args) {
  const result = spawnSync(process.execPath, [script, ...args], {
    cwd: ROOT,
    encoding: 'utf8',
    timeout: 180_000,
  });
  if (result.error) throw result.error;
  return { status: result.status, stdout: result.stdout || '', stderr: result.stderr || '' };
}

/** 测试自己的归一化（独立于两门）：`{id}` 与 `:id` 都折成 `:param`，去尾斜杠。 */
function normalize(target) {
  const value = target
    .replace(/\{[^}]+\}/g, ':param')
    .replace(/:[A-Za-z0-9_]+/g, ':param')
    .replace(/\/+$/, '');
  return value === '' ? '/' : value;
}

function parseNumber(text, pattern, label) {
  const match = pattern.exec(text);
  assert.ok(match, `无法从输出里解析「${label}」：\n${text}`);
  return Number(match[1]);
}

// ---------------------------------------------------------------------------
// parity 门
// ---------------------------------------------------------------------------

// 退出码刻意**不**参与断言：本测试比的是两道门对同一棵树算出的数字是否一致，
// 而不是它们各自的例外登记是否已经补齐。门红着（例如契约还没跟上实现）时数字仍然
// 可比，这时把退出码也钉住只会让等价性测试跟着一起红，查不出真正的问题。
const parityDefault = run(PARITY, []);
// 诊断开关必须默认关闭：无参数运行不许出现清单块，否则等于给门加了隐藏输出。
if (parityDefault.stdout.includes('[parity-empty-routes]')) {
  failures.push('parity 门无参数运行时打印了 [parity-empty-routes]：诊断开关必须是 opt-in');
}

const parity = run(PARITY, ['--print-empty-routes']);

const parityOperationCount = parseNumber(parity.stdout, /契约 operation 数：(\d+)/, 'parity 契约 operation 数');
const parityRouteCount = parseNumber(parity.stdout, /实现侧路由数：(\d+)/, 'parity 实现侧路由数');
const parityEmptyRouteCount = parseNumber(
  parity.stdout,
  /状态码集合为空的实现侧路由数：(\d+)\/\d+/,
  'parity 空集路由数',
);
const parityEmptyMappedCount = parseNumber(
  parity.stdout,
  /状态码集合为空的实现侧路由数：\d+\/\d+（其中已映射 operation (\d+) 条/,
  'parity 空集已映射数',
);

const markerIndex = parity.stdout.indexOf('[parity-empty-routes]');
assert.ok(markerIndex >= 0, 'parity 门没有打印 [parity-empty-routes] 块');
const parityEmptyRoutes = parity.stdout
  .slice(markerIndex + '[parity-empty-routes]'.length)
  .split('\n')
  .map((line) => line.trim())
  .filter((line) => /^[A-Z]+ \//.test(line));

// 仅实现侧存在、契约未收录的路由：这些不可能出现在可达性门的「已映射 operation」清单里。
const implOnlyMarker = '[parity] 仅实现侧存在、契约未收录的路由（不参与状态码比对）：';
const implOnlyIndex = parity.stdout.indexOf(implOnlyMarker);
const parityImplementationOnly = implOnlyIndex < 0
  ? []
  : parity.stdout
      .slice(implOnlyIndex + implOnlyMarker.length)
      .split('\n')
      .map((line) => /^\s*-\s+([A-Z]+ \S+)\s+handler=/.exec(line))
      .filter(Boolean)
      .map((match) => {
        const [method, rawPath] = match[1].split(' ');
        return `${method} ${normalize(rawPath)}`;
      });

// ---------------------------------------------------------------------------
// 可达性门
// ---------------------------------------------------------------------------

const reach = run(REACHABILITY, []);
let reachOperationCount = Number.NaN;
let reachRouteCount = Number.NaN;
let reachDegenerateRouteCount = Number.NaN;
let reachDegenerateMappedCount = Number.NaN;
try {
  reachOperationCount = parseNumber(reach.stdout, /契约 operation 数：(\d+)/, '可达性门 契约 operation 数');
  reachRouteCount = parseNumber(reach.stdout, /实现侧路由数：(\d+)/, '可达性门 实现侧路由数');
  reachDegenerateRouteCount = parseNumber(
    reach.stdout,
    /退化区（parity 门状态码集合为空）：(\d+) 条路由/,
    '可达性门 退化区路由数',
  );
  reachDegenerateMappedCount = parseNumber(
    reach.stdout,
    /退化区（parity 门状态码集合为空）：\d+ 条路由 \/ (\d+) 条已映射 operation/,
    '可达性门 退化区已映射数',
  );
} catch (error) {
  failures.push(`可达性门没有打印可解析的计数（扫描链路可能已失效）：${error.message}\n${reach.stdout}\n${reach.stderr}`);
}

const manifest = run(REACHABILITY, ['--print-manifest']);
if (manifest.status !== 0) {
  failures.push(`可达性门 --print-manifest 失败（exit ${manifest.status}）：\n${manifest.stderr}`);
}
let reachDegenerate = [];
try {
  reachDegenerate = JSON.parse(manifest.stdout);
} catch (error) {
  failures.push(`可达性门 --print-manifest 输出的不是合法 JSON：${error.message}`);
}
const reachDegenerateKeys = new Set(
  reachDegenerate.map((entry) => `${entry.operation.split(' ')[0]} ${normalize(entry.contractPath)}`),
);

// ---------------------------------------------------------------------------
// 比对
// ---------------------------------------------------------------------------

const comparisons = [
  ['契约 operation 数', parityOperationCount, reachOperationCount],
  ['实现侧路由数', parityRouteCount, reachRouteCount],
  ['空集路由数', parityEmptyRouteCount, reachDegenerateRouteCount],
  ['空集已映射 operation 数', parityEmptyMappedCount, reachDegenerateMappedCount],
];

for (const [label, parityValue, reachValue] of comparisons) {
  if (parityValue !== reachValue) {
    failures.push(`两门对「${label}」不一致：parity=${parityValue} 可达性=${reachValue}`);
  } else {
    console.log(`  ok  ${label} = ${parityValue}（两门一致）`);
  }
}

// 成员集合：parity 的空集路由去掉「契约未收录」的那些，应当与可达性门的退化区逐条相等。
if (parityEmptyRoutes.length !== parityEmptyRouteCount) {
  failures.push(
    `parity 打印的空集路由条数 ${parityEmptyRoutes.length} 与它自己的计数 ${parityEmptyRouteCount} 不一致`,
  );
}
const expected = new Set(parityEmptyRoutes.filter((route) => !parityImplementationOnly.includes(route)));
const onlyInParity = [...expected].filter((route) => !reachDegenerateKeys.has(route));
const onlyInReachability = [...reachDegenerateKeys].filter((route) => !expected.has(route));

if (onlyInParity.length > 0) {
  failures.push(`退化区成员只在 parity 一侧（${onlyInParity.length} 条）：\n  ${onlyInParity.slice(0, 10).join('\n  ')}`);
}
if (onlyInReachability.length > 0) {
  failures.push(
    `退化区成员只在可达性门一侧（${onlyInReachability.length} 条）：\n  ${onlyInReachability.slice(0, 10).join('\n  ')}`,
  );
}
if (onlyInParity.length === 0 && onlyInReachability.length === 0) {
  console.log(`  ok  退化区成员集合逐条相等（${expected.size} 条）`);
}

if (failures.length > 0) {
  console.error(`\n[reach-equivalence] 失败 ${failures.length} 项：`);
  for (const message of failures) console.error(`  ✗ ${message}`);
  process.exitCode = 1;
} else {
  console.log('[reach-equivalence] 通过：两门的分母与退化区成员集合完全一致，且诊断开关默认关闭');
}
