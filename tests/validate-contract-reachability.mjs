#!/usr/bin/env node
/**
 * 契约可达性常设门（第 14 轮 P1 的防回归）—— CLI 层。
 *
 * 为什么需要它：parity 门（validate-contract-status-parity.mjs）只认两条 guard 白名单
 * ——requireGuestRead / requireAdmin。ReadActor / RequireActor / LoadActor 产出的
 * 401/503 它完全看不见，于是「实现已经改、契约没跟上」这类缺陷会静默变绿（第 14 轮
 * P1：entity_meta.yaml 的值读写两条漏 503，正是这么溜过去的）。
 *
 * 本门补的是**另一半**：从路由注册出发，沿同包调用图 BFS 找到每个端点最终经过的
 * guard 助手，把这些 guard 会产出的状态码与契约声明做差集。它**不修改也不替代**
 * parity 门 —— 两道门各管一半，谁都不能关掉对方：
 *   - parity 门：handler 自己 + respond* / fail* / send* / write* 辅助函数产出的码；
 *   - 本门：guard 助手产出的码（身份问题 vs 依赖故障）。
 *
 * 三个可复核的产出：
 *   1. 退化区清单：parity 门「状态码集合为空」的 operation（∅ ⊆ 任意，比对失效）。
 *      这份清单与 parity 门的 194/435 必须逐条对上，是 W3 补契约的直接输入。
 *   2. 差集：路由可达 401/503 但契约未声明 —— 逐条登记基线，新增即红。
 *   3. 端点 → guard 映射：guard 从端点上**消失**（改名/换成直连）会红；只有登记在
 *      基线「允许升级对」里的替换才放行（见 helpers/reachability/rules.mjs）。
 *
 * 用法：
 *   node tests/validate-contract-reachability.mjs                  # 跑门
 *   node tests/validate-contract-reachability.mjs --refresh        # 复核后落盘基线
 *   node tests/validate-contract-reachability.mjs --print-manifest # 只打印退化区清单
 *   node tests/validate-contract-reachability.mjs --root <dir>     # 扫 fixture 树
 *
 * 分层（本文件只做参数解析与流程编排）：
 *   helpers/reachability/go-source.mjs     Go 源码模型
 *   helpers/reachability/contract-model.mjs 契约模型
 *   helpers/reachability/call-graph.mjs    调用图（路由 handler → guard 助手）
 *   helpers/reachability/observation.mjs   实测模型（扫描结果 → 可比对形状）
 *   helpers/reachability/baseline.mjs      基线读写与例外登记纪律
 *   helpers/reachability/rules.mjs         规则判定（输出交给调用方）
 *   helpers/reachability/report.mjs        输出格式化
 *
 * --refresh 的边界（与 parity 门同一套纪律）：
 *   - 更新计数、退化区清单、端点 guard 映射，写守卫升级记录，并**删除**已过期的登记；
 *   - 绝不自动**新增**「已登记未声明状态码」「通配路由」「已复核误报」
 *     「未登记401503产出点」「允许升级对」五组例外 —— 例外集合只能人工确认，
 *     否则等于把门关掉。
 *   - 存在硬失败时拒绝写盘。
 *
 * 失败项都带规则码（`[规则码] 说明`），反例测试按规则码断言「红的原因就是预期那条」。
 */

import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { GUARD_REGISTRY, scanReachability } from './helpers/reachability-scan.mjs';
import { loadBaseline, writeBaseline } from './helpers/reachability/baseline.mjs';
import { buildObservation } from './helpers/reachability/observation.mjs';
import { renderFailures, renderManifestIncomplete, renderNotes, renderSummary } from './helpers/reachability/report.mjs';
import { evaluateRules } from './helpers/reachability/rules.mjs';

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(SCRIPT_DIR, '..');

const args = process.argv.slice(2);
const refresh = args.includes('--refresh');
const printManifest = args.includes('--print-manifest');
const rootIndex = args.indexOf('--root');
const ROOT = rootIndex >= 0 && args[rootIndex + 1] ? path.resolve(args[rootIndex + 1]) : REPO_ROOT;
const BASELINE_PATH = path.join(ROOT, 'tests', 'reachability-baseline.json');

function main() {
  const started = process.hrtime.bigint();
  const failures = [];
  const notes = [];
  const upgrades = [];
  const ctx = {
    fail: (rule, message) => failures.push({ rule, message }),
    note: (message) => notes.push(message),
    upgrades,
  };

  let scan;
  try {
    scan = scanReachability(ROOT);
  } catch (error) {
    console.error(`[reach] 扫描失败：${error.message}`);
    process.exitCode = 1;
    return;
  }

  // 扫描期结构性问题先收进失败集合：--print-manifest 也必须把它们算进退出码，
  // 否则 W3 会拿到一份「少了一条」的清单还以为它是完整的。
  for (const problem of scan.problems) ctx.fail('scan', `[${problem.code}] ${problem.message}`);
  if (scan.unknownConstants.length > 0) {
    ctx.fail('scan', `出现状态码常量表外的写法，需补表后重跑：${scan.unknownConstants.join(', ')}`);
  }

  if (scan.routes.length === 0) {
    console.error('[reach] 实现侧一条路由都没解析出来，扫描链路已失效（不是绿灯）');
    process.exitCode = 1;
    return;
  }

  const observation = buildObservation(scan);

  if (printManifest) {
    console.log(JSON.stringify(observation.degenerate, null, 2));
    if (failures.length > 0) {
      for (const line of renderManifestIncomplete(failures)) console.error(line);
      process.exitCode = 1;
    }
    return;
  }

  const loaded = loadBaseline({ baselinePath: BASELINE_PATH, root: ROOT, ctx });
  // 基线读不出来时，后面每一条比对都失去参照物，只会产出一串级联噪声。
  // 这里短路，让「基线被删/被截断」只有一条红，原因才说得清。
  const baselineFatal = failures.some(
    (failure) => failure.rule === 'baseline-missing' || failure.rule === 'baseline-invalid',
  );
  if (baselineFatal) {
    for (const line of renderFailures(failures, '[reach] 基线不可用，已跳过其余比对（它们都需要基线做参照）')) {
      console.error(line);
    }
    process.exitCode = 1;
    return;
  }

  const { guardChecked, gapChecked } = evaluateRules({
    observation,
    scan,
    baseline: loaded,
    ctx,
    root: ROOT,
    repoRoot: REPO_ROOT,
  });

  const present = new Set();
  for (const guards of observation.endpoints.values()) for (const guard of guards) present.add(guard);

  if (ROOT !== REPO_ROOT) {
    console.log(`[reach] fixture 模式（--root ${path.relative(REPO_ROOT, ROOT) || ROOT}）：只核对存在的包目录`);
  }
  for (const line of renderSummary({
    observation,
    scan,
    baseline: loaded,
    guardRegistrySize: GUARD_REGISTRY.length,
    presentCount: present.size,
    guardChecked,
    gapChecked,
  })) {
    console.log(line);
  }
  for (const line of renderNotes(notes)) console.log(line);

  const seconds = Number(process.hrtime.bigint() - started) / 1e9;

  if (failures.length > 0) {
    const footer = refresh
      ? '[reach] 本次运行有失败项，拒绝写基线：先把问题修掉'
      : null;
    for (const line of renderFailures(failures, footer)) console.error(line);
    console.error(`[reach] 耗时 ${seconds.toFixed(2)}s`);
    process.exitCode = 1;
    return;
  }

  if (refresh) {
    writeBaseline({ observation, baseline: loaded, upgrades, baselinePath: BASELINE_PATH });
    console.log(`[reach] 已刷新基线：${path.relative(ROOT, BASELINE_PATH)}`);
  }

  console.log(`[reach] 通过：0 个未登记例外，耗时 ${seconds.toFixed(2)}s`);
}

main();
