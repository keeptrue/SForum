#!/usr/bin/env node
/**
 * 契约可达性门的反例测试（门自身必须能被证明会红）。
 *
 * 一个只会变绿的门没有价值，一个「对任何输入都变红」的门同样没有价值。所以这里既有
 * 对照用例（control 必须绿），也有 8 个反例，并且**逐个断言红的原因就是预期那条**：
 * 门把每个失败项都打上规则码（`✗ [规则码] 说明`），本测试比对**规则码集合**是否与
 * 预期完全相等 —— 多红一条（例如「顺带」触发了另一条规则）也算失败，因为那说明
 * 反例没有隔离好，或者规则之间串了。
 *
 * 覆盖的防线：
 *   1. missing-503        漏声明 503（第 14 轮 P1 的原形）
 *   2. guard-direct       guard 换成直连 → 守卫消失
 *   3. guard-renamed      guard 改名 → 守卫消失 + 登记表过期
 *   4. catch-all          未登记的 ALL /* 通配路由
 *   5. registry-stale     登记表名字对不上实现（端点映射已同步，只剩这一条）
 *   6. baseline-missing   基线被删
 *   7. baseline-truncated 基线被截断
 *   8. ratchet            退化区棘轮被上调
 *   9. error-alias        error 换名再返回（别名追踪不能退化成静默盲区）
 *  10. count-drift        精确计数漂移（覆盖 checkCounts 的 count-drift 分支）
 *
 * 每个 fixture 是一棵自洽的最小树：
 *   apps/api/app/Http/auth_helpers.go              guard 助手
 *   apps/api/app/Http/Controllers/Demo/routes.go   路由注册 + handler
 *   contracts/openapi.yaml + openapi/paths/demo.yaml
 *   tests/reachability-baseline.json               该 fixture 的基线
 *
 * 用法：node tests/validate-contract-reachability.test.mjs
 */

import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(SCRIPT_DIR, '..');
const FIXTURE_ROOT = path.join(SCRIPT_DIR, 'fixtures', 'contract-reachability');
const VALIDATOR = path.join(SCRIPT_DIR, 'validate-contract-reachability.mjs');

const REQUIRED_FILES = [
  'apps/api/app/Http/auth_helpers.go',
  'apps/api/app/Http/Controllers/Demo/routes.go',
  'contracts/openapi.yaml',
  'contracts/openapi/paths/demo.yaml',
];

const CASES = [
  {
    name: 'control',
    summary: '对照：实现与契约一致，门必须绿',
    rules: [],
  },
  {
    name: 'missing-503',
    summary: '反例 1：handler 经过 RequireActor（可达 503），契约只声明 200/401',
    rules: ['gap-unregistered'],
    stderr: /多出=503/,
  },
  {
    name: 'guard-direct',
    summary: '反例 2：RequireActor 换成直连 OptionalActor，守卫消失',
    rules: ['guard-removed'],
    stderr: /消失=app\/Http:RequireActor/,
  },
  {
    name: 'guard-renamed',
    summary: '反例 3：RequireActor 改名为 requireActor（守卫消失 + 登记表过期）',
    rules: ['guard-removed', 'registry-stale'],
    stderr: /消失=app\/Http:RequireActor/,
  },
  {
    name: 'catch-all',
    summary: '反例 4：新增未登记的 ALL /* 通配路由',
    rules: ['wildcard-unregistered'],
    stderr: /ALL \/api\/v1\/demo\/:extensionId\/\*/,
  },
  {
    name: 'registry-stale',
    summary: '反例 5：登记表里的 guard 名字对不上实现（端点映射已同步）',
    rules: ['registry-stale'],
    stderr: /guard 白名单条目过期：app\/Http:RequireActor/,
  },
  {
    name: 'baseline-missing',
    summary: '反例 6：基线被删',
    rules: ['baseline-missing'],
    stderr: /基线文件缺失/,
    baselineExpected: true,
  },
  {
    name: 'baseline-truncated',
    summary: '反例 7：基线被截断（半个 JSON）',
    rules: ['baseline-invalid'],
    stderr: /基线 JSON 无法解析/,
    baselineExpected: true,
  },
  {
    name: 'error-alias',
    summary: '反例 9：error 换名再返回（`e := err; return e`），别名必须仍算逃出',
    rules: ['gap-unregistered'],
    stderr: /多出=503/,
  },
  {
    name: 'count-drift',
    summary: '反例 10：基线记的实现侧路由数与实测不符（精确计数漂移）',
    rules: ['count-drift'],
    stderr: /基线计数「实现侧路由数」/,
  },
  {
    name: 'ratchet',
    summary: '反例 8：退化区棘轮被上调（成员已登记，计数被写小）',
    rules: ['ratchet'],
    stderr: /棘轮只许降不许升/,
  },
];

/** 从门的 stderr 里抽出所有规则码（去重、排序）。 */
function ruleCodesOf(stderr) {
  const codes = new Set();
  for (const match of stderr.matchAll(/✗ \[([a-z-]+)\]/g)) codes.add(match[1]);
  return [...codes].sort();
}

async function runFixture(name) {
  const root = path.join(FIXTURE_ROOT, name);
  for (const relative of REQUIRED_FILES) {
    assert.ok(
      fs.existsSync(path.join(root, relative)),
      `fixture ${name} 缺少文件 ${relative}（fixture 被删/改名后本测试不能假装通过）`,
    );
  }
  return await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [VALIDATOR, '--root', root], {
      cwd: ROOT,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let stdout = '';
    let stderr = '';
    const timeout = setTimeout(() => child.kill('SIGKILL'), 120_000);
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    child.stdout.on('data', (chunk) => {
      stdout += chunk;
    });
    child.stderr.on('data', (chunk) => {
      stderr += chunk;
    });
    child.on('error', (error) => {
      clearTimeout(timeout);
      reject(error);
    });
    child.on('close', (status, signal) => {
      clearTimeout(timeout);
      resolve({
        status: status ?? 1,
        stdout,
        stderr: signal ? `${stderr}\nterminated by ${signal}` : stderr,
        root,
      });
    });
  });
}

const failures = [];
let passed = 0;

const fixtureResults = new Array(CASES.length);
let nextFixture = 0;
async function runFixtureWorker() {
  while (true) {
    const index = nextFixture++;
    if (index >= CASES.length) return;
    try {
      fixtureResults[index] = { result: await runFixture(CASES[index].name) };
    } catch (error) {
      fixtureResults[index] = { error };
    }
  }
}
await Promise.all(Array.from({ length: Math.min(4, CASES.length) }, () => runFixtureWorker()));

for (let caseIndex = 0; caseIndex < CASES.length; caseIndex += 1) {
  const testCase = CASES[caseIndex];
  const fixtureResult = fixtureResults[caseIndex];
  if (fixtureResult.error) {
    failures.push(`${testCase.name}: 运行失败 —— ${fixtureResult.error.message}`);
    continue;
  }
  let result;
  try {
    result = fixtureResult.result;
  } catch (error) {
    failures.push(`${testCase.name}: 运行失败 —— ${error.message}`);
    continue;
  }

  const expectedStatus = testCase.rules.length === 0 ? 0 : 1;
  if (result.status !== expectedStatus) {
    failures.push(
      `${testCase.name}（${testCase.summary}）：退出码 ${result.status}，期望 ${expectedStatus}\n` +
        `--- stdout ---\n${result.stdout}\n--- stderr ---\n${result.stderr}`,
    );
    continue;
  }

  const actualRules = ruleCodesOf(result.stderr);
  const expectedRules = [...testCase.rules].sort();
  if (actualRules.join(',') !== expectedRules.join(',')) {
    failures.push(
      `${testCase.name}（${testCase.summary}）：红的规则集合是 [${actualRules.join(', ')}]，` +
        `期望恰好是 [${expectedRules.join(', ')}] —— 多红/少红都说明反例没隔离好\n` +
        `--- stderr ---\n${result.stderr}`,
    );
    continue;
  }

  if (testCase.stderr && !testCase.stderr.test(result.stderr)) {
    failures.push(
      `${testCase.name}（${testCase.summary}）：stderr 里没有命中预期文案 ${testCase.stderr}\n` +
        `--- stderr ---\n${result.stderr}`,
    );
    continue;
  }

  if (testCase.baselineExpected && !result.stderr.includes(path.join('tests', 'reachability-baseline.json'))) {
    failures.push(`${testCase.name}：失败文案没有指出基线文件路径，运维无法定位`);
    continue;
  }

  if (testCase.rules.length === 0) {
    if (!/通过：0 个未登记例外/.test(result.stdout)) {
      failures.push(`${testCase.name}：退出码 0 但 stdout 没有「通过」行，判定自相矛盾`);
      continue;
    }
    if (/✗ \[/.test(result.stderr)) {
      failures.push(`${testCase.name}：退出码 0 但 stderr 里出现失败项，判定自相矛盾`);
      continue;
    }
  }

  passed += 1;
  const ruleLabel = testCase.rules.length === 0 ? '绿' : testCase.rules.join(' + ');
  console.log(`  ok  ${testCase.name} —— ${testCase.summary}（${ruleLabel}）`);
}

if (failures.length > 0) {
  console.error(`\n[reach-test] 失败 ${failures.length} 项：`);
  for (const message of failures) console.error(`  ✗ ${message}`);
  process.exitCode = 1;
} else {
  console.log(
    `[reach-test] 通过：${passed}/${CASES.length} 个 fixture 行为符合预期` +
      `（1 绿 + ${CASES.length - 1} 红，每个反例只红在预期规则上）`,
  );
}
