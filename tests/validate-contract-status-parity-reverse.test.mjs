#!/usr/bin/env node
/**
 * #237 的两条新判据 + 一条「不许退化」边界，逐条钉住。
 *
 * 背景：#237 指出契约门只做「实现产出 ⊆ 契约声明」这一个方向，而且**看不见映射器业务码**
 * （`mapXxxError` 族识别但不归因）。于是「契约删掉 403、实现仍能产出 403」这种缺口
 * 在门里是完全绿的一类。这个测试钉四件事：
 *
 *   1. 对照：仓库自身的门必须是绿的，并且打印两条新判据的规模（收窄归因 / 反向缺口）；
 *   2. 反例 1（`forward`）：契约**故意不声明** 403，而 handler 调用的领域方法直接 return
 *      哨兵、映射器确实会翻成 403 —— 门必须红，且文案点名那条 operation 与 `多出=403`。
 *      这就是 #237 里「契约删掉 403」那一类：在归因上线之前它只会红一条基线计数噪声，
 *      不会红在缺口本身。
 *   3. 反例 2（`declared`）：契约声明了实现永远产不出的 418 —— 门必须红，
 *      文案点名「反向缺口直方图出现未登记的状态码 418」。
 *   4. 边界（`narrow`）：同一张映射器表里有「哨兵 ⇒ 403」，但 handler **不调用**那个
 *      领域方法 —— 门必须保持绿。这条是「按 handler 收窄归因」与「把映射器全量并入
 *      每个 handler」的分界线：后者会让门为不存在的缺陷变红，#237 明确禁止。
 *
 * 三个合成树都做到**单一成因**：契约、基线、guard 白名单、公开读 401 清单配齐，
 * 只为让被测的那一条红（或保持绿）。
 *
 * 用法：node tests/validate-contract-status-parity-reverse.test.mjs
 */

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(SCRIPT_DIR, '..');
const GATE = path.join(SCRIPT_DIR, 'validate-contract-status-parity.mjs');
const FIXTURES = path.join(SCRIPT_DIR, 'fixtures', 'contract-parity-reverse');

function runGate(args) {
  const result = spawnSync(process.execPath, [GATE, ...args], {
    cwd: ROOT,
    encoding: 'utf8',
    timeout: 180_000,
  });
  if (result.error) throw result.error;
  return { status: result.status, stdout: result.stdout || '', stderr: result.stderr || '' };
}

function failureLines(stderr) {
  return stderr.split('\n').filter((line) => line.trim().startsWith('✗'));
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

check('对照：仓库自身的门为绿，且两条新判据都在自报里', () => {
  const repo = runGate([]);
  assert.equal(repo.status, 0, `仓库门退出码 ${repo.status}\n${repo.stderr}`);
  assert.match(repo.stdout, /\[parity\] 映射器收窄归因（#237）：映射器分支 \d+ 个、哨兵 \d+ 个/);
  assert.match(repo.stdout, /\[parity\] 反向检查（契约声明的码必须真被实现产出）：缺口 \d+ 条 operation/);
  assert.match(repo.stdout, /\[parity\] 契约码集合与实现码集合完全相等的 operation：\d+\/\d+/);
  assert.match(repo.stdout, /\[parity\] 缺失码直方图/);
  assert.match(repo.stdout, /通过：0 个未登记例外/);
});

check('反例 1：契约删掉 403、实现经映射器仍能产出 403 → 门必须红并指名', () => {
  const fixture = path.join(FIXTURES, 'forward');
  const result = runGate(['--root', fixture]);
  assert.notEqual(result.status, 0, `合成树竟然通过（该红）\n${result.stdout}`);
  const lines = failureLines(result.stderr);
  assert.equal(lines.length, 1, `应当只红一条，实际：\n${lines.join('\n')}`);
  assert.match(lines[0], /实现产出了契约未声明的状态码：GET \/api\/v1\/demo\/target/);
  assert.match(lines[0], /多出=403/);
});

check('反例 2：契约声明了实现永不产出的 418 → 门必须红并指名', () => {
  const fixture = path.join(FIXTURES, 'declared');
  const result = runGate(['--root', fixture]);
  assert.notEqual(result.status, 0, `合成树竟然通过（该红）\n${result.stdout}`);
  const lines = failureLines(result.stderr);
  assert.equal(lines.length, 1, `应当只红一条，实际：\n${lines.join('\n')}`);
  assert.match(lines[0], /反向缺口直方图出现未登记的状态码 418/);
});

check('边界：映射器里有 403，但 handler 不调用那个领域方法 → 门必须保持绿', () => {
  const fixture = path.join(FIXTURES, 'narrow');
  const result = runGate(['--root', fixture]);
  assert.equal(result.status, 0, `收窄归因退化成全量并入了（该绿却红）\n${result.stderr}`);
  assert.match(result.stdout, /实现多出、契约未声明的状态码：0 条/);
  assert.match(result.stdout, /契约码集合与实现码集合完全相等的 operation：1\/1/);
});

if (failures.length > 0) {
  console.error(`\n反向检查/归因 反例测试失败 ${failures.length} 项：`);
  for (const message of failures) console.error(`  ✗ ${message}`);
  process.exitCode = 1;
} else {
  console.log(`\n反向检查/归因 反例测试通过：${passed} 项`);
}
