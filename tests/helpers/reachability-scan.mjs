/**
 * 契约可达性扫描器（W2 常设门的扫描入口）。
 *
 * 目的：把「路由 → 同包调用图 BFS → guard 助手」这条链算清楚，回答两个问题：
 *   1. 某个 operation 的 handler 最终经过哪些 guard 助手（ReadActor / RequireActor /
 *      LoadActor / requireGuestRead / requireAdmin / OptionalActor）；
 *   2. 每个 guard 会产出哪些状态码 —— 这些码是 parity 门**看不见**的，因为
 *      validate-contract-status-parity.mjs 的 GUARD_HELPERS 只收了 requireGuestRead 与
 *      requireAdmin 两个名字（第 14 轮 P1 就是这么溜过去的）。
 *
 * 与 parity 门的关系：**刻意不改它**。本扫描器独立实现，不复用、不修改 parity 门的
 * 任何代码，理由有二：
 *   - parity 门是已经过 20 次变异验证的防线，任何行为漂移都要重跑它的 194/435 棘轮；
 *   - 本门的用途是「量出 parity 门看不见的那部分」，两者必须能互相独立复核
 *     （等价性由 tests/validate-contract-reachability-equivalence.test.mjs 钉住，
 *     该测试**不共享代码**，只比对两门各自跑出来的结果）。
 * 但**路由枚举与状态码收集刻意与 parity 门逐条等价**（同样的 group 链解析、同样的
 * `respond* / fail* / send* / write*` 前缀规则、同样的深度 2），否则两边的分母对不上，
 * 「退化区」就无法与 parity 门的 194/435 相互印证。
 *
 * 分工：
 *   reachability/go-source.mjs      Go 源码模型（文本形状、函数体、状态码原语）
 *   reachability/contract-model.mjs 契约模型（openapi.yaml → operation 列表）
 *   reachability/call-graph.mjs     调用图（路由 handler → guard 助手）
 *   本文件                          路由枚举 + 组装
 */

import fs from 'node:fs';
import path from 'node:path';

import { GUARD_REGISTRY, bodyOf, collectGuards, packageIndexFactory } from './reachability/call-graph.mjs';
import { SERVER_PREFIX, readContractOperations } from './reachability/contract-model.mjs';
import {
  PARITY_GUARD_HELPERS,
  braceBody,
  listGoFiles,
  normalizePath,
  parityPackageIndexFactory,
  parityVisibleCodes,
  scanGoSource,
  splitTopLevelArgs,
  statusesIn,
} from './reachability/go-source.mjs';

const FIBER_METHODS = ['Get', 'Post', 'Put', 'Patch', 'Delete', 'Head', 'Options', 'All'];

// ---------------------------------------------------------------------------
// 主扫描
// ---------------------------------------------------------------------------

/**
 * 扫描一个仓库根（或 fixture 根）。返回：
 *   routes         实现侧路由（含 guard 集合与 guard 可达状态码）
 *   operations     契约 operation
 *   problems       扫描期发现的结构性问题（无法静态判定的注册等），一律视为红
 */
export function scanReachability(root) {
  const apiRoot = path.join(root, 'apps', 'api');
  const contractRoot = path.join(root, 'contracts');
  const problems = [];
  const unknownConstants = new Set();
  const stats = { ambiguousMethods: [], skippedRouters: [] };

  const index = packageIndexFactory(apiRoot);
  const appHttpDir = path.join(apiRoot, 'app', 'Http');
  // parity 门的调用边识别（G3）用另一份带参数名的包索引：剪枝需要参数名。
  // 与上面的 index 分开建，避免把两套职责塞进一个缓存。
  const parityIndexOf = parityPackageIndexFactory();
  const guardIndex = new Map();
  for (const entry of GUARD_REGISTRY) {
    guardIndex.set(`${path.join(apiRoot, entry.dir)}::${entry.name}`, {
      ...entry,
      id: `${entry.dir}:${entry.name}`,
    });
  }

  const ctx = {
    apiRoot,
    appHttpDir,
    appHttpAliases: new Set(['apphttp', 'appHttp', 'http', 'aphttp']),
    guardIndex,
    guardById: new Map([...guardIndex.values()].map((entry) => [entry.id, entry])),
    index,
  };

  const routes = new Map();
  const unregisteredGateHits = new Set();
  const unregisteredGateCodeMap = new Map();
  const files = listGoFiles(apiRoot);

  for (const file of files) {
    const source = fs.readFileSync(file, 'utf8');
    if (!/\.(Get|Post|Put|Patch|Delete|Head|Options|All)\s*\(/.test(source)) continue;
    const { stripped, masked } = scanGoSource(source);
    const dir = path.dirname(file);
    const relFile = path.relative(root, file);

    // group 链：api 来自 server.go 的 app.Group("/api/v1")，其余按出现顺序解析。
    const groups = new Map([
      ['api', SERVER_PREFIX],
      ['app', ''],
    ]);
    for (const m of stripped.matchAll(/([A-Za-z_]\w*)\s*:?=\s*([A-Za-z_]\w*)\.Group\(\s*"([^"]*)"/g)) {
      const parent = groups.get(m[2]);
      if (parent === undefined) {
        problems.push({
          code: 'group-parent-unresolved',
          message: `${relFile}: group "${m[1]}" 的父级 "${m[2]}" 无法解析，路径会算错`,
        });
        continue;
      }
      groups.set(m[1], parent + m[3]);
    }

    for (const m of stripped.matchAll(/([A-Za-z_]\w*)\.(Get|Post|Put|Patch|Delete|Head|Options|All)\s*\(/g)) {
      const receiver = m[1];
      const methodToken = m[2];
      if (!FIBER_METHODS.includes(methodToken)) continue;
      const openIndex = stripped.indexOf('(', m.index + m[0].length - 1);
      const { args } = splitTopLevelArgs(stripped, openIndex);
      if (args.length < 2) continue;
      const pathLiteral = /^"([^"]*)"$/.exec(args[0].text);
      const base = groups.get(receiver);
      if (base === undefined) {
        if (pathLiteral && pathLiteral[1].startsWith('/')) {
          stats.skippedRouters.push(`${relFile}: ${receiver}.${methodToken}("${pathLiteral[1]}")`);
        }
        continue;
      }
      if (!pathLiteral) {
        problems.push({
          code: 'route-path-not-literal',
          message: `${relFile}: 路由 ${receiver}.${methodToken} 的首参不是字符串字面量：${args[0].text}`,
        });
        continue;
      }

      const handlerArg = args[args.length - 1];
      const handlerExpr = handlerArg.text;
      const method = methodToken === 'All' ? 'ALL' : methodToken.toUpperCase();
      const fullPath = base + pathLiteral[1];

      let handlerBody;
      let handlerLabel;
      let handlerReceiver = { receiverName: null, receiverType: null };
      if (/^func\s*\(/.test(handlerExpr)) {
        handlerBody = braceBody(masked, handlerArg.start);
        handlerLabel = '<inline closure>';
        if (!handlerBody) {
          problems.push({
            code: 'handler-body-missing',
            message: `${relFile}: 内联 handler ${method} ${fullPath} 的函数体无法定位`,
          });
          continue;
        }
      } else {
        const parts = handlerExpr.split('.');
        const name = parts.pop();
        handlerLabel = name;
        // `h.foo` 的第一跳：把 h 作为 receiver 变量名带进去，bodyOf 会挑出挂在该
        // receiver 上的方法（通常是 *Controller），避免与同名多类型方法并集混淆。
        const found = bodyOf({ dir, name, receiverName: parts.pop() || null, receiverType: null }, ctx);
        if (!found) {
          problems.push({
            code: 'handler-body-missing',
            message: `${relFile}: 路由 ${method} ${fullPath} 的 handler "${handlerExpr}" 在包内找不到函数体`,
          });
          continue;
        }
        handlerBody = found.body;
        handlerReceiver = { receiverName: found.receiverName, receiverType: found.receiverType };
      }

      const { guards, effective, discarded, unregisteredGates, unregisteredGateCodes } = collectGuards(
        handlerBody,
        dir,
        ctx,
        handlerReceiver,
      );
      for (const gate of unregisteredGates) unregisteredGateHits.add(gate);
      for (const [gate, codes] of unregisteredGateCodes) {
        const merged = unregisteredGateCodeMap.get(gate) || new Set();
        for (const code of codes) merged.add(code);
        unregisteredGateCodeMap.set(gate, merged);
      }

      const guardCodes = new Set();
      for (const id of effective) {
        const entry = ctx.guardById.get(id);
        for (const code of entry ? entry.codes : []) guardCodes.add(code);
      }

      const pkgIndex = index(dir);
      const helperBodies = new Map(pkgIndex.parityHelpers);
      for (const name of PARITY_GUARD_HELPERS) {
        if (helperBodies.has(name)) continue;
        const list = pkgIndex.methods.get(name);
        if (list && list.length > 0) helperBodies.set(name, list[0].body);
      }
      const parityCodes = parityVisibleCodes(handlerBody, helperBodies, unknownConstants, {
        indexOf: parityIndexOf,
        appHttpDir,
        packageDir: dir,
        receiverName: handlerReceiver.receiverName || null,
      });
      for (const code of statusesIn(handlerBody, unknownConstants)) parityCodes.add(code);

      const key = `${method} ${normalizePath(fullPath)}`;
      const record = {
        method,
        path: fullPath,
        normalizedPath: normalizePath(fullPath),
        handler: handlerLabel,
        file: relFile,
        packageDir: path.relative(root, dir).split(path.sep).join('/'),
        guards: [...guards].sort(),
        // 真正把状态码传到 HTTP 层的 guard（error 被吞掉的不在其中）。基线的
        // 「守卫升级/回退」判定只看这一份：被吞掉的守卫换掉不算回退。
        codeGuards: [...effective].sort(),
        discardedGuards: [...discarded].sort(),
        guardCodes: [...guardCodes].sort((a, b) => a - b),
        parityCodes: [...parityCodes].sort((a, b) => a - b),
        wildcard: fullPath.includes('*'),
        allMethods: method === 'ALL',
      };
      const existing = routes.get(key);
      if (existing) {
        // 同一路径多注册（不同 method token 归一化后同名）时合并，与 parity 门一致。
        for (const id of record.guards) if (!existing.guards.includes(id)) existing.guards.push(id);
        existing.guards.sort();
        for (const code of record.guardCodes) if (!existing.guardCodes.includes(code)) existing.guardCodes.push(code);
        existing.guardCodes.sort((a, b) => a - b);
        for (const code of record.parityCodes) if (!existing.parityCodes.includes(code)) existing.parityCodes.push(code);
        existing.parityCodes.sort((a, b) => a - b);
        existing.wildcard = existing.wildcard || record.wildcard;
        existing.allMethods = existing.allMethods || record.allMethods;
        if (!existing.handlers.includes(record.handler)) existing.handlers.push(record.handler);
      } else {
        routes.set(key, { ...record, handlers: [record.handler] });
      }
    }
  }

  for (const item of stats.skippedRouters) {
    problems.push({
      code: 'router-prefix-unresolved',
      message: `路由注册的 router 变量解析不出前缀，无法计入映射：${item}`,
    });
  }

  // 包索引的歧义方法名（多 receiver 同名）汇总，供人工核对过近似是否影响结论。
  const seenAmbiguous = new Set();
  for (const file of files) {
    const dir = path.dirname(file);
    const relDir = path.relative(root, dir).split(path.sep).join('/');
    if (seenAmbiguous.has(relDir)) continue;
    seenAmbiguous.add(relDir);
    const pkg = index(dir);
    for (const item of pkg.ambiguousMethods) {
      stats.ambiguousMethods.push({ package: relDir, name: item.name, receiverTypes: item.receiverTypes });
    }
  }

  // 登记表自检：每个 guard 名字必须在实现侧真的有函数体。名字对不上就是白名单过期
  // （改名/删除），必须红 —— 否则可达性会静默变空、差集永远变绿。
  const registryPresence = GUARD_REGISTRY.map((entry) => {
    const dir = path.join(apiRoot, entry.dir);
    if (!fs.existsSync(dir)) return { id: `${entry.dir}:${entry.name}`, present: false, reason: '包目录不存在' };
    const pkg = index(dir);
    const present = pkg.topFuncs.has(entry.name) || (pkg.methods.get(entry.name) || []).length > 0;
    return { id: `${entry.dir}:${entry.name}`, present, reason: present ? '' : '包内找不到同名函数或方法' };
  });

  const { operations, pathCount } = readContractOperations(contractRoot);

  return {
    root,
    routes: [...routes.values()],
    operations,
    contractPathCount: pathCount,
    problems,
    unknownConstants: [...unknownConstants],
    unregisteredGates: [...unregisteredGateHits]
      .sort()
      .map((id) => ({ id, codes: [...(unregisteredGateCodeMap.get(id) || [])].sort((a, b) => a - b) })),
    registryPresence,
    stats,
  };
}

export { SERVER_PREFIX, GUARD_REGISTRY, normalizePath };
