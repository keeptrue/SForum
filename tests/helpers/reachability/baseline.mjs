/**
 * 基线层：读写 tests/reachability-baseline.json，并校验「例外集合」的登记纪律
 * （每条必须带中文原因、该带 codes 的必须带 codes）。
 *
 * 例外集合只能人工新增：--refresh 只删除过期条目，绝不自动补 —— 否则等于把门关掉。
 */

import fs from 'node:fs';
import path from 'node:path';

function hasChinese(text) {
  return /[\u4e00-\u9fff]/.test(text || '');
}

// ---------------------------------------------------------------------------
// 基线
// ---------------------------------------------------------------------------

const EXCEPTION_GROUPS = [
  ['已登记未声明状态码', true],
  ['通配路由', false],
  ['已复核误报', true],
  ['未登记401503产出点', true],
];

function emptyBaseline() {
  return {
    counts: {},
    endpoints: new Map(),
    codeEndpoints: new Map(),
    degenerate: [],
    gaps: [],
    wildcard: [],
    falsePositives: [],
    unregistered: [],
    upgrades: [],
    upgradePairs: [],
  };
}

function loadBaseline({ baselinePath, root, ctx }) {
  if (!fs.existsSync(baselinePath)) {
    ctx.fail('baseline-missing', `基线文件缺失：${path.relative(root, baselinePath)}（删掉基线等于关掉这道门）`);
    return emptyBaseline();
  }
  let raw;
  try {
    raw = JSON.parse(fs.readFileSync(baselinePath, 'utf8'));
  } catch (error) {
    ctx.fail(
      'baseline-invalid',
      `基线 JSON 无法解析：${path.relative(root, baselinePath)}（${error.message}）`,
    );
    return emptyBaseline();
  }

  const baseline = {
    raw,
    counts: raw['期望计数'] || {},
    endpoints: new Map(Object.entries(raw['端点guard'] || {})),
    codeEndpoints: new Map(Object.entries(raw['端点guard出码'] || {})),
    degenerate: Array.isArray(raw['退化区']) ? raw['退化区'] : [],
    gaps: Array.isArray(raw['已登记未声明状态码']) ? raw['已登记未声明状态码'] : [],
    wildcard: Array.isArray(raw['通配路由']) ? raw['通配路由'] : [],
    falsePositives: Array.isArray(raw['已复核误报']) ? raw['已复核误报'] : [],
    unregistered: Array.isArray(raw['未登记401503产出点']) ? raw['未登记401503产出点'] : [],
    upgrades: Array.isArray(raw['守卫升级记录']) ? raw['守卫升级记录'] : [],
    upgradePairs: Array.isArray(raw['允许升级对']) ? raw['允许升级对'] : [],
  };

  // 例外集合必须逐条带中文原因：没有理由的豁免等于静默放行。
  for (const [label, requireCodes] of EXCEPTION_GROUPS) {
    const list = label === '通配路由' ? baseline.wildcard : label === '已复核误报'
      ? baseline.falsePositives
      : label === '未登记401503产出点'
        ? baseline.unregistered
        : baseline.gaps;
    for (const entry of list) {
      const key = entry?.operation || entry?.id;
      if (!entry || typeof key !== 'string') {
        ctx.fail('baseline-entry', `基线「${label}」条目缺少 operation/id：${JSON.stringify(entry)}`);
        continue;
      }
      if (!hasChinese(entry['原因'])) {
        ctx.fail('baseline-entry', `基线「${label}」条目缺少中文原因：${key}`);
      }
      if (requireCodes && (!Array.isArray(entry.codes) || entry.codes.length === 0)) {
        ctx.fail('baseline-entry', `基线「${label}」条目缺少 codes：${key}`);
      }
    }
  }
  // 「允许升级对」是本门唯一放行「守卫被换掉」的地方，所以逐条核对形状与理由：
  // 少一条原因就等于开了一个没有解释的后门。
  for (const pair of baseline.upgradePairs) {
    if (!pair || typeof pair.from !== 'string' || typeof pair.to !== 'string' || !pair.from || !pair.to) {
      ctx.fail('baseline-entry', `基线「允许升级对」条目缺少 from/to：${JSON.stringify(pair)}`);
      continue;
    }
    if (!hasChinese(pair['原因'])) {
      ctx.fail('baseline-entry', `基线「允许升级对」条目缺少中文原因：${pair.from} → ${pair.to}`);
    }
    if (pair.from === pair.to) {
      ctx.fail('baseline-entry', `基线「允许升级对」条目 from 与 to 相同：${pair.from}`);
    }
  }
  return baseline;
}

// 计数策略：只钉两条棘轮 + 两条精确计数。

// ---------------------------------------------------------------------------
// 落盘
// ---------------------------------------------------------------------------

function writeBaseline({ observation, baseline, upgrades, baselinePath }) {
  const currentGaps = new Map(observation.gaps.map((gap) => [gap.operation, gap]));
  const keptGaps = baseline.gaps
    .filter((entry) => currentGaps.has(entry.operation))
    .map((entry) => ({
      operation: entry.operation,
      codes: currentGaps.get(entry.operation).codes,
      原因: entry['原因'],
    }));

  const currentWildcard = new Set(observation.wildcard.map((entry) => entry.operation));
  const keptWildcard = baseline.wildcard.filter((entry) => currentWildcard.has(entry.operation));

  const currentGapKeys = new Set(observation.gaps.map((gap) => gap.operation));
  const keptExempt = baseline.falsePositives.filter((entry) => currentGapKeys.has(entry.operation));

  const currentProducers = new Set(observation.unregisteredGates.map((producer) => producer.id));
  const keptProducers = baseline.unregistered
    .filter((entry) => currentProducers.has(entry.id))
    .map((entry) => ({
      id: entry.id,
      codes: observation.unregisteredGates.find((producer) => producer.id === entry.id).codes,
      原因: entry['原因'],
    }));

  // 升级记录：只保留「当前守卫里仍然含有 to」的条目，避免记录烂在基线里。
  const seen = new Set();
  const keptUpgrades = [];
  for (const record of [...baseline.upgrades, ...upgrades]) {
    const key = `${record.operation}|${record.from}|${record.to}`;
    if (seen.has(key)) continue;
    seen.add(key);
    const current = observation.endpoints.get(record.operation) || [];
    if (!current.includes(record.to)) continue;
    keptUpgrades.push(record);
  }

  const next = {
    说明:
      '契约可达性门基线。由 node tests/validate-contract-reachability.mjs --refresh 维护；' +
      '「已登记未声明状态码」「通配路由」「已复核误报」「未登记401503产出点」四组例外只能人工新增，' +
      '--refresh 只会删除过期条目；「守卫升级记录」由 --refresh 自动写入。',
    期望计数: observation.counts,
    端点guard: Object.fromEntries(observation.endpoints),
    端点guard出码: Object.fromEntries(observation.codeEndpoints),
    退化区: observation.degenerate,
    已登记未声明状态码: keptGaps,
    通配路由: keptWildcard,
    已复核误报: keptExempt,
    未登记401503产出点: keptProducers,
    // 允许升级对是**标准**而非记录：--refresh 原样保留，绝不自动增删。
    允许升级对: baseline.upgradePairs,
    守卫升级记录: keptUpgrades,
  };
  fs.writeFileSync(baselinePath, `${JSON.stringify(next, null, 2)}\n`);
}


export { loadBaseline, writeBaseline, emptyBaseline, EXCEPTION_GROUPS };
