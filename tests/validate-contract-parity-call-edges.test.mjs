#!/usr/bin/env node
/**
 * parity 门「调用边识别面」的闭环测试（G3）。
 *
 * 为什么需要：G3 把空集从 30/437 打到 0/437，靠的是**扩识别面**而不是放宽判据。识别面
 * 一旦退化（包索引读不到、正则被改坏、`apphttp` 别名换掉），门不会报错 —— 它只是看不见
 * 那些状态码，空集路由数悄悄回升。这条测试把三件事钉住：
 *
 *   1. 对照：仓库自身的门是绿的，且空集计数确实是 0（不是「没扫到路由」的假绿）；
 *   2. 识别面有命中面：跟包函数命中、映射器识别、按实参剪枝三项都必须非 0 ——
 *      整体失灵时这条立刻红，而不是等到某个新 endpoint 掉进空集；
 *   3. 精度闸门：合成树 tests/fixtures/contract-parity-call-edges 里五条路由，
 *      契约声明的就是「真实可达」的那一组，门必须绿。**任何一条闸门失效都会让它变红**，
 *      这就是「摘掉新识别面后门会重新红」的常设版本（对应关系见下）：
 *
 *        摘掉 F1 同包委派      → /demo/delegate、/demo/pruned 掉进空集 → 棘轮红
 *        摘掉 F2 跟包 apphttp  → /demo/apphttp 掉进空集 → 棘轮红（真实仓库里另有
 *                                [call-edge] 自检：跟包命中面归零即红）
 *        摘掉 F3 依赖故障 503  → 门**不会**红（门只查「实现产出 ⊆ 契约声明」这一个方向，
 *                                少算一个码不算缺口），由本测试的集合相等断言抓：
 *                                /demo/apphttp 实测变成 200,401，期望 200,401,503
 *        把映射器整段并进集合  → /demo/mapper 多出 409,422 → 红
 *        取消 error 吞噬判定   → /demo/swallowed 多出 401,503 → 红
 *        取消实参 bool 剪枝    → /demo/pruned 多出 422 → 红
 *
 * 也就是说：**五个变异由门自己红，F3 由集合相等断言红**。这条区分要如实保留 ——
 * 把 F3 说成「门会红」是错的，子集方向对缺失码无感。
 *
 * 用法：node tests/validate-contract-parity-call-edges.test.mjs
 */

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { readParityReport } from './helpers/contract-parity/report.mjs';

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(SCRIPT_DIR, '..');
const GATE = path.join(SCRIPT_DIR, 'validate-contract-status-parity.mjs');
const FIXTURE = path.join(SCRIPT_DIR, 'fixtures', 'contract-parity-call-edges');

const REQUIRED_FIXTURE_FILES = [
  'apps/api/app/Http/responses.go',
  'apps/api/app/Http/auth_helpers.go',
  'apps/api/app/Http/Controllers/Demo/routes.go',
  'contracts/openapi.yaml',
  'contracts/openapi/paths/demo.yaml',
  'tests/contract-status-parity-baseline.json',
  'tests/contract-public-read-401.json',
];

const FIXTURE_ROUTES = [
  'GET /api/v1/demo/delegate',
  'GET /api/v1/demo/apphttp',
  'POST /api/v1/demo/mapper',
  'PUT /api/v1/demo/swallowed',
  'DELETE /api/v1/demo/pruned',
];

function runGate(args) {
  const result = spawnSync(process.execPath, [GATE, ...args], {
    cwd: ROOT,
    encoding: 'utf8',
    timeout: 240_000,
  });
  if (result.error) throw result.error;
  return { status: result.status, stdout: result.stdout || '', stderr: result.stderr || '' };
}

const repoReport = readParityReport();

function repoGate() {
  return repoReport || runGate([]);
}

function failureLines(stderr) {
  return stderr.split('\n').filter((line) => line.trim().startsWith('✗'));
}

function parseOperationCodes(stdout) {
  const marker = stdout.indexOf('[parity-operation-codes]');
  assert.ok(marker >= 0, '门没有打印 [parity-operation-codes] 块');
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
// 1. 对照 + 识别面命中面
// ---------------------------------------------------------------------------

check('对照：仓库自身的门为绿，且空集计数是 0（不是「没扫到路由」的假绿）', () => {
  const repo = repoGate();
  assert.equal(repo.status, 0, `仓库门退出码 ${repo.status}\n${repo.stderr}`);
  const empty = /状态码集合为空的实现侧路由数：(\d+)\/(\d+)/.exec(repo.stdout);
  assert.ok(empty, '门没有打印空集计数');
  assert.equal(Number(empty[1]), 0, `空集路由数应为 0，实测 ${empty[1]}`);
  assert.ok(Number(empty[2]) > 400, `实现侧路由数 ${empty[2]} 太小，扫描链路可能已失效`);
});

check('识别面有命中面：跟包函数 / 映射器 / 剪枝三项都非 0', () => {
  const repo = repoGate();
  assert.equal(repo.status, 0, `仓库门退出码 ${repo.status}\n${repo.stderr}`);
  const stats = /调用边识别（F1 同包委派 \/ F2 跟包 apphttp\.\* \/ F3 依赖故障 503）：跟包函数命中 (\d+) 个、错误映射器识别 (\d+) 个（识别后按 handler 收窄归因，不全量并入）、按实参剪枝 (\d+) 处/.exec(
    repo.stdout,
  );
  assert.ok(stats, `门没有打印调用边识别统计：\n${repo.stdout}`);
  assert.ok(Number(stats[1]) > 0, '跟包函数命中为 0：F2 识别链路已断');
  assert.ok(Number(stats[2]) > 0, '映射器识别为 0：调用边根本没走到 mapXxxError，链路已断');
  assert.ok(Number(stats[3]) > 0, '按实参剪枝为 0：剪枝链路已断（decodeBookmarkBody 一类会误算分支）');
});

// ---------------------------------------------------------------------------
// 2. 合成树：五条路由各自钉一条形态/闸门
// ---------------------------------------------------------------------------

check('合成树完整（被删/改名后本测试不能假装通过）', () => {
  for (const relative of REQUIRED_FIXTURE_FILES) {
    assert.ok(fs.existsSync(path.join(FIXTURE, relative)), `fixture 缺少文件 ${relative}`);
  }
});

check('合成树为绿：三条形态 + 三条精度闸门同时成立（任一条失效即红）', () => {
  const fixture = runGate(['--root', FIXTURE]);
  assert.equal(
    fixture.status,
    0,
    `fixture 应当绿（契约声明的就是真实可达集合），实际退出码 ${fixture.status}\n` +
      `--- 失败项 ---\n${failureLines(fixture.stderr).join('\n')}`,
  );
  const empty = /状态码集合为空的实现侧路由数：(\d+)\/(\d+)/.exec(fixture.stdout);
  assert.ok(empty, 'fixture 运行没有打印空集计数');
  assert.equal(Number(empty[1]), 0, `fixture 空集应为 0，实测 ${empty[1]}`);
  assert.equal(Number(empty[2]), FIXTURE_ROUTES.length, `fixture 路由数应为 ${FIXTURE_ROUTES.length}`);
});

check('合成树：五条路由的实现侧集合逐条非空，且与契约精确相等', () => {
  const fixture = runGate(['--root', FIXTURE, '--print-operation-codes']);
  assert.equal(fixture.status, 0, `fixture 退出码 ${fixture.status}\n${fixture.stderr}`);
  const codes = parseOperationCodes(fixture.stdout);
  const expected = new Map([
    // F1 同包委派：handler 只有一行 return h.action(...)，200 来自下游 apphttp.OK，401 来自 h.guard。
    ['GET /api/v1/demo/delegate', [200, 401]],
    // F2 跟包 apphttp.* + F3 依赖故障：401 来自 RequireActor 自身，503 来自 classifyActorError
    // 构造的 readPathDependencyFailure（全局 errorHandler 渲染）。
    ['GET /api/v1/demo/apphttp', [200, 401, 503]],
    // 映射器收窄归因：mapDemoError 里有 422/409 分支，但这条 handler 不调用会返回那些哨兵的
    // 领域方法，所以一个都不许出现（全量并入会凭空塞进 422/409，正是 #237 禁止的退化）。
    ['POST /api/v1/demo/mapper', [200]],
    // error 折成 bool：viewer 内部的 401/503 不许出现。
    ['PUT /api/v1/demo/swallowed', [200]],
    // 实参 bool 剪枝：decodePruned 的 422 只在「加」侧可达。
    ['DELETE /api/v1/demo/pruned', [200]],
  ]);
  for (const [operation, want] of expected) {
    const got = codes.get(operation);
    assert.ok(got, `门没有打印 ${operation} 的实现侧集合`);
    assert.equal(got.join(','), want.join(','), `${operation} 实现侧集合=${got.join(',')}，期望 ${want.join(',')}`);
  }
});

// ---------------------------------------------------------------------------
// 3. 诊断开关默认关闭
// ---------------------------------------------------------------------------

check('诊断开关默认关闭：无参数运行不打印任何 [parity-*] 清单块', () => {
  const fixture = runGate(['--root', FIXTURE]);
  assert.equal(fixture.status, 0, `fixture 退出码 ${fixture.status}\n${fixture.stderr}`);
  for (const marker of ['[parity-empty-routes]', '[parity-operation-codes]']) {
    assert.ok(!fixture.stdout.includes(marker), `无参数运行打印了 ${marker}：诊断开关必须是 opt-in`);
  }
});

if (failures.length > 0) {
  console.error(`\n[parity-call-edges] 失败 ${failures.length} 项：`);
  for (const message of failures) console.error(`  ✗ ${message}`);
  process.exitCode = 1;
} else {
  console.log(
    `[parity-call-edges] 通过：${passed} 项（三条识别形态 + 三条精度闸门都有常设反例；` +
      '摘掉 F1/F2 或任一精度闸门门会重新红，摘掉 F3 由集合相等断言红）',
  );
}
