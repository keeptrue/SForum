#!/usr/bin/env node
/**
 * parity 门「响应助手白名单是闭的」的反例测试。
 *
 * 为什么需要：响应助手白名单一旦漏登记，那个助手的码就静默消失，调用它的路由状态码
 * 集合变空，比对退化成 ∅ ⊆ 任意 —— 正是第 14 轮 P1 那一类。这条测试钉住两件事：
 *   1. 对照：仓库自身的门必须是绿的，且打印白名单规模；
 *   2. 反例 1：在合成树里加一个「首参 fiber.Ctx 且自己产出状态码」但**未登记**的助手，
 *      门必须只红这一条（不是顺带红一堆别的），并且文案点名那个函数；
 *   3. 反例 2：实现侧设置 503 的 `Retry-After`，而共享响应组件没声明该 header，
 *      门必须只红这一条（header-global 全局存在性检查，非逐 operation 绑定）。
 *
 * 合成树（tests/fixtures/contract-parity-helpers/unlisted）刻意做到**单一成因**：
 * 契约、基线、guard 白名单、公开读 401 清单都配齐，只为让「未登记助手」这一条红。
 *
 * 用法：node tests/validate-contract-status-parity-helpers.test.mjs
 */

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(SCRIPT_DIR, '..');
const GATE = path.join(SCRIPT_DIR, 'validate-contract-status-parity.mjs');
const FIXTURE_UNLISTED = path.join(SCRIPT_DIR, 'fixtures', 'contract-parity-helpers', 'unlisted');
const FIXTURE_HEADER = path.join(SCRIPT_DIR, 'fixtures', 'contract-parity-helpers', 'header-missing');

const REQUIRED_FILES = [
  'apps/api/app/Http/responses.go',
  'apps/api/app/Http/Controllers/Demo/routes.go',
  'contracts/openapi.yaml',
  'contracts/openapi/paths/demo.yaml',
  'tests/contract-status-parity-baseline.json',
  'tests/contract-public-read-401.json',
];

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

check('对照：仓库自身的门为绿，且打印响应助手白名单规模', () => {
  const repo = runGate([]);
  assert.equal(repo.status, 0, `仓库门退出码 ${repo.status}\n${repo.stderr}`);
  assert.match(repo.stdout, /\[parity\] 响应助手白名单：\d+ 条/);
  assert.match(repo.stdout, /通过：0 个未登记例外/);
});

check('反例 1：未登记的响应助手必须让门变红，且只红这一条', () => {
  for (const relative of REQUIRED_FILES) {
    assert.ok(
      fs.existsSync(path.join(FIXTURE_UNLISTED, relative)),
      `fixture 缺少文件 ${relative}（fixture 被删/改名后本测试不能假装通过）`,
    );
  }
  const fixture = runGate(['--root', FIXTURE_UNLISTED]);
  assert.equal(fixture.status, 1, `fixture 应当红，实际退出码 ${fixture.status}\n${fixture.stdout}`);
  const lines = failureLines(fixture.stderr);
  assert.equal(
    lines.length,
    1,
    `fixture 应当只红一条（单一成因），实际 ${lines.length} 条：\n${lines.join('\n')}`,
  );
  assert.match(lines[0], /未登记的响应助手：AcceptedResponse/);
  assert.match(lines[0], /RESPONSE_HELPERS/);
});

check('反例 2：实现设了 503 的 Retry-After，组件未声明就必须红，且只红这一条', () => {
  const fixture = runGate(['--root', FIXTURE_HEADER]);
  assert.equal(fixture.status, 1, `fixture 应当红，实际退出码 ${fixture.status}\n${fixture.stdout}`);
  const lines = failureLines(fixture.stderr);
  assert.equal(lines.length, 1, `应当只红一条，实际 ${lines.length} 条：\n${lines.join('\n')}`);
  assert.match(lines[0], /\[header-global\]/);
  assert.match(lines[0], /Retry-After/);
});

check('header 全局存在性检查在仓库自身生效：503 组件必须声明 Retry-After', () => {
  const repo = runGate([]);
  assert.equal(repo.status, 0, `仓库门退出码 ${repo.status}\n${repo.stderr}`);
  assert.match(repo.stdout, /\[parity\] header 全局存在性（header-global/);
});

if (failures.length > 0) {
  console.error(`\n[parity-helpers-test] 失败 ${failures.length} 项：`);
  for (const message of failures) console.error(`  ✗ ${message}`);
  process.exitCode = 1;
} else {
  console.log(`[parity-helpers-test] 通过：${passed} 项（白名单是闭的：漏登记即红，且红得单因）`);
}
