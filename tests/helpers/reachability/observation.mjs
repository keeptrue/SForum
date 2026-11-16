/**
 * 实测模型：把扫描器的原始输出（routes / operations / problems）归一化成与基线可比对的
 * 形状。这一层只做**投影**，不判定任何东西 —— 判定在 rules.mjs。
 */

import { normalizePath } from './go-source.mjs';

function operationKey(method, normalizedPath) {
  return `${method} ${normalizedPath}`;
}

// ---------------------------------------------------------------------------
// 实测
// ---------------------------------------------------------------------------

/**
 * 把扫描结果整理成与基线可比对的形状。
 *
 * 退化区条目刻意带上 contractPath / source / guards / guardCodes / declared 五项：
 * W3 拿到清单后不需要再跑一遍扫描就能定位契约文件并判断该补哪个码，而 `成因` 说明
 * 这条为什么退化。
 */
function buildObservation(scan) {
  const operationByKey = new Map(
    scan.operations.map((operation) => [operationKey(operation.method, normalizePath(operation.path)), operation]),
  );

  const endpoints = new Map();
  const codeEndpoints = new Map();
  const degenerate = [];
  const gaps = [];
  const wildcard = [];

  for (const route of scan.routes) {
    const key = operationKey(route.method, route.normalizedPath);
    if (route.guards.length > 0) endpoints.set(key, route.guards);
    if (route.codeGuards.length > 0) codeEndpoints.set(key, route.codeGuards);
    if (route.wildcard || route.allMethods) {
      wildcard.push({ operation: `${route.method} ${route.path}`, handler: route.handlers.join('/'), file: route.file });
    }

    const operation = operationByKey.get(key);
    if (!operation) continue;

    const declared = new Set(operation.codes);
    const ranges = new Set(operation.ranges || []);
    const isDeclared = (code) =>
      declared.has(code) || ranges.has('all') || ranges.has(String(Math.floor(code / 100)));
    const missing = route.guardCodes.filter((code) => !isDeclared(code));

    if (missing.length > 0) {
      gaps.push({ operation: `${route.method} ${route.path}`, codes: missing });
    }

    if (route.parityCodes.length === 0) {
      degenerate.push({
        operation: `${route.method} ${route.path}`,
        contractPath: route.normalizedPath,
        source: operation.source,
        handler: route.handlers.join('/'),
        guards: route.guards,
        guardCodes: route.guardCodes,
        declared: operation.codes,
        missing,
        成因: degenerateCause(route, missing),
      });
    }
  }

  return {
    implementationOnly: scan.routes.filter(
      (route) => !operationByKey.has(operationKey(route.method, route.normalizedPath)),
    ).length,
    counts: {
      契约operation数: scan.operations.length,
      实现侧路由数: scan.routes.length,
      退化区路由数: scan.routes.filter((route) => route.parityCodes.length === 0).length,
      退化区已映射operation数: degenerate.length,
      路由可达guard码但契约未声明条数: gaps.length,
      未登记401503产出点数: scan.unregisteredGates.length,
    },
    endpoints: new Map([...endpoints.entries()].sort(([a], [b]) => a.localeCompare(b))),
    codeEndpoints: new Map([...codeEndpoints.entries()].sort(([a], [b]) => a.localeCompare(b))),
    degenerate: degenerate.sort((a, b) => a.operation.localeCompare(b.operation)),
    gaps: gaps.sort((a, b) => a.operation.localeCompare(b.operation)),
    wildcard: wildcard.sort((a, b) => a.operation.localeCompare(b.operation)),
    unregisteredGates: scan.unregisteredGates,
  };
}

/**
 * 退化区成因：parity 门的空集不是「实现真的没有状态码」，绝大多数是响应辅助函数
 * 名字不带 respond/fail/send/write 前缀（apphttp.OK / Created / JSON ...），
 * 于是 parity 门扫不到。分类让 W3 能区分「该补契约」与「该修门」。
 */
function degenerateCause(route, missing) {
  if (missing.length > 0) return 'guard-code-missing';
  if (route.guardCodes.length > 0) return 'guard-code-declared-but-set-empty';
  return 'no-status-literal-reachable';
}


export { buildObservation, operationKey };
