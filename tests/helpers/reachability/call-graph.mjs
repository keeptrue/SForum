/**
 * 调用图：从路由 handler 出发，沿同包（以及 app/Http 包）的调用关系做 BFS，
 * 判定每个端点最终经过哪些 guard 助手，以及这些 guard 产出的状态码会不会真的
 * 逃到 HTTP 层（error 被 `x, _ :=`、`(Actor, bool)` 这类形状吞掉的都不算）。
 *
 * 这一层是「门盲区」的核心：parity 门看不见 guard，只有这里能把 guard 接上。
 */

import fs from 'node:fs';
import path from 'node:path';

import { functionBodies, methodBodies, scanGoSource } from './go-source.mjs';

// 本门自己的调用图深度上限。控制器里的「handler → wrapper → guard」链实测不超过 4 层，
// 8 层是安全余量；超过就说明遇到了非常规结构，宁可不判也不能误判。
const CALL_GRAPH_DEPTH = 8;

/**
 * guard 助手登记表：每条都是**逐条核对过语义**的，不做名字前缀泛匹配。
 *
 * codes 是「该 guard 自己（含其内部嵌套的 guard）能产出的状态码」。语义依据：
 *   - ReadActor / RequireActor：C→401 `auth.required`，D/E→503 `service.not_ready`
 *     （app/Http/auth_read.go 的 classifyActorError，503 由 errors.go 的
 *     readPathDependencyCause 分支渲染，带 Retry-After: 1）。
 *   - LoadActor：无凭据→401 `auth.required`（app/Http/auth_user.go）。它的
 *     ResolveUserID/LoadActor 错误**原样上抛**，由全局 handler 兜成 500 —— 那是
 *     第 14 轮登记的残余（必登面仍直连 LoadActor，D 仍是 500），本门不把 500 记到
 *     LoadActor 头上，避免把「依赖故障」泛化成全站 500。收口由 W1 负责。
 *   - OptionalActor / optionalActor（app/Http 包内）：**不产出状态码**，它把 error
 *     原样返回给调用方。它的价值在身份：把 ReadActor 换成 OptionalActor 就是「退回
 *     未分类」，必须能被门看见（否则第 14 轮 B1 那类改动可以静默回退）。
 *   - requireGuestRead：Forum 版 401（guest.read=login_required 时匿名读被拒）；
 *     Profile 版另有 503（读策略快照缺失）。**同门不同义**，所以按包分开登记。
 *   - requireAdmin：TrustLevels 版 404（产品开关关闭）+ 403（缺 admin.access）+
 *     401（内部 loadActor→LoadActor）。
 *
 * `dir` 是相对 `apps/api` 的包目录。同一名字在不同包里语义不同时必须各登记一条。
 */
export const GUARD_REGISTRY = [
  {
    dir: 'app/Http',
    name: 'ReadActor',
    codes: [401, 503],
    note: '公开读可选主体：C→401、D/E→503（auth_read.go classifyActorError）',
  },
  {
    dir: 'app/Http',
    name: 'RequireActor',
    codes: [401, 503],
    note: '必登主体：无凭据/C→401、D/E→503；不在门上判账号状态',
  },
  {
    dir: 'app/Http',
    name: 'LoadActor',
    codes: [401],
    note: '必登主体（旧路径）：无凭据→401；依赖故障仍上抛成 500，不计入本门',
  },
  {
    dir: 'app/Http',
    name: 'OptionalActor',
    codes: [],
    note: '可选主体（导出）：error 原样返回，自身不产出状态码；出现即「未分类」',
  },
  {
    dir: 'app/Http',
    name: 'optionalActor',
    codes: [],
    note: '可选主体（内部）：error 原样返回，自身不产出状态码；出现即「未分类」',
  },
  {
    dir: 'app/Http/Controllers/Forum',
    name: 'requireGuestRead',
    codes: [401],
    note: '论坛游客读策略：login_required 时匿名读 401',
  },
  {
    dir: 'app/Http/Controllers/Profile',
    name: 'requireGuestRead',
    codes: [401, 503],
    note: '资料页游客读策略：读策略快照缺失 503，login_required 时 401',
  },
  {
    dir: 'app/Http/Controllers/TrustLevels',
    name: 'requireAdmin',
    codes: [401, 403, 404],
    note: '信任等级后台守卫：开关关闭 404、缺权限 403、内部 loadActor→LoadActor 401',
  },
  // 以下两条不在派单点名的六个之内，但**不登记就会让端点看起来没有门**：
  // 它们是控制器自带的等价守卫（同一个「没登录就 401 auth.required」语义），
  // 只是没走 app/Http 的公共助手。登记时只收它自己直接判定的码，域层 mapper
  // （mapIdentityError 等）产出的 403/422/429/503 不算到守卫头上。
  {
    dir: 'app/Http/Controllers/Identity',
    name: 'actor',
    codes: [401],
    note: 'Identity 自带的必登守卫：actorForUserID 在 !ok 时直接 401 auth.required',
  },
  {
    dir: 'app/Http/Controllers/Notifications',
    name: 'userID',
    codes: [401],
    note: '通知中心自带的必登守卫：ResolveUserID 未登录时直接 401 auth.required',
  },
];

// ---------------------------------------------------------------------------
// 实现侧：包索引 + 调用图
// ---------------------------------------------------------------------------

/**
 * 一个包目录的索引：顶层函数、方法（含 receiver 类型）、以及「parity 等价」用的
 * 合并函数表。全部基于 masked 文本，字符串/注释不会误命中。
 */
export function buildPackageIndex(dir) {
  const files = fs
    .readdirSync(dir)
    .filter((name) => name.endsWith('.go') && !name.endsWith('_test.go'))
    .sort();
  const sources = files.map((name) => scanGoSource(fs.readFileSync(path.join(dir, name), 'utf8')));

  const topFuncs = new Map();
  const methods = new Map();
  for (const { masked } of sources) {
    for (const [name, body] of functionBodies(masked)) {
      if (!topFuncs.has(name)) topFuncs.set(name, body);
    }
    for (const [name, list] of methodBodies(masked)) {
      if (!methods.has(name)) methods.set(name, []);
      methods.get(name).push(...list);
    }
  }

  // parity 等价：parity 门的 helperBodies = 该包全部顶层函数 + 全部方法（同名取首个），
  // 之后才按需补 GUARD_HELPERS 的方法体。
  const parityHelpers = new Map(topFuncs);
  for (const [name, list] of methods) {
    if (!parityHelpers.has(name) && list.length > 0) parityHelpers.set(name, list[0].body);
  }

  const ambiguousMethods = [...methods.entries()]
    .filter(([, list]) => list.length > 1)
    .map(([name, list]) => ({ name, receiverTypes: list.map((item) => item.receiverType) }));

  return { dir, files, sources, topFuncs, methods, parityHelpers, ambiguousMethods };
}

export function packageIndexFactory(apiRoot) {
  const cache = new Map();
  return (dir) => {
    const abs = path.resolve(dir);
    if (!cache.has(abs)) cache.set(abs, buildPackageIndex(abs));
    return cache.get(abs);
  };
}

/**
 * 从一个函数体出发做调用图 BFS，收集可达的 guard 助手。
 *
 * 解析规则（静态、无类型推断，但跟踪 receiver）：
 *   - `<alias>.<name>(` 且 alias 是 app/Http 包的导入别名 → 在 app/Http 包里解析；
 *   - `<recv>.<name>(` 且 recv 就是当前节点的 receiver 变量名 → 在本包里按
 *     **receiver 类型**精确解析同名方法；找不到才退回并集；
 *   - `<other>.<name>(` → 在本包里解析同名方法（多 receiver 取并集，过近似）；
 *   - `<name>(` 裸调用 → 本包顶层函数，其次本包方法。
 * 命中 GUARD_REGISTRY 的节点即记为 guard，**不再下探**（guard 是叶子，保住身份语义：
 * 否则 RequireActor 内部的 optionalActor 会被误记成「未分类」）。
 */

export function collectGuards(handlerBody, packageDir, ctx, startReceiver) {
  const guards = new Set();
  // 同一个 guard 可能被调用多次：只要有一处把 error 传出去，它就能产出状态码；
  // 全部调用点都丢弃 error 时才算「被吞掉」（例如 Forum 的 `viewer, _ := LoadActor`）。
  const sites = new Map();
  const reached = new Set();
  const unregisteredGates = new Set();
  const unregisteredGateCodes = new Map();
  const visited = new Set();
  // carriesError：当前节点产出的 error 是否会逃到 HTTP 层。必须**沿调用链传递**——
  // 只看直接调用点是不够的：TrustLevels 的 getPublic → viewer 在上层就把 error 折成了
  // (Actor, false)，于是 viewer 内部那句 `return apphttp.LoadActor(...)` 虽然字面上
  // 是 return，实际早已不会产出 401。
  const queue = [{ body: handlerBody, dir: packageDir, depth: 0, carriesError: true, ...startReceiver }];

  while (queue.length > 0) {
    const node = queue.shift();
    if (!node || node.depth > CALL_GRAPH_DEPTH) continue;
    for (const call of extractCalls(node.body)) {
      const target = resolveCall(call, node, ctx);
      if (!target) continue;
      const escapes = node.carriesError && !call.discarded;
      const key = `${target.dir}::${target.name}::${target.receiverType || '*'}::${escapes ? 'esc' : 'swallow'}`;
      if (visited.has(key)) continue;
      visited.add(key);
      const guard = guardFor(target.dir, target.name, ctx);
      if (guard) {
        guards.add(guard.id);
        const site = sites.get(guard.id) || { kept: 0, discarded: 0 };
        if (escapes) site.kept += 1;
        else site.discarded += 1;
        sites.set(guard.id, site);
        continue; // guard 是叶子
      }
      reached.add(key);
      const found = bodyOf(target, ctx);
      if (found) {
        // 反盲区：一个非登记节点自己直接产出 401/503，说明这里有一道本门看不见的门。
        // 不登记就等于门被绕过（Identity 的 actorForUserID 就是这样一道门）。
        const direct = directGateCodes(found.body);
        if (direct.size > 0) {
          const id = `${path.relative(ctx.apiRoot, target.dir).split(path.sep).join('/')}:${target.name}`;
          unregisteredGates.add(id);
          const codes = unregisteredGateCodes.get(id) || new Set();
          for (const code of direct) codes.add(code);
          unregisteredGateCodes.set(id, codes);
        }
        queue.push({
          body: found.body,
          dir: target.dir,
          depth: node.depth + 1,
          receiverName: found.receiverName,
          receiverType: found.receiverType,
          carriesError: escapes,
        });
      }
    }
  }
  const discarded = new Set();
  const effective = new Set();
  for (const [id, site] of sites) {
    if (site.kept > 0) effective.add(id);
    else discarded.add(id);
  }
  return { guards, effective, discarded, reached, unregisteredGates, unregisteredGateCodes };
}

// 「自己直接产出 401/503」的形状：fiber/http.NewError(StatusUnauthorized|StatusServiceUnavailable)。
// 只认这两类码：它们正是「身份问题 vs 依赖故障」这条轴，也是本门负责的语义。
const DIRECT_GATE_PATTERN = /NewError\(\s*[\w.]*\.?Status(?:Unauthorized|ServiceUnavailable)\b/;

/** 从一个函数体里取出它自己直接产出的 401/503 码。 */
export function directGateCodes(body) {
  const codes = new Set();
  for (const m of body.matchAll(/NewError\(\s*[\w.]*\.?Status([A-Za-z]+)\b/g)) {
    if (m[1] === 'Unauthorized') codes.add(401);
    else if (m[1] === 'ServiceUnavailable') codes.add(503);
  }
  for (const m of body.matchAll(/NewError\(\s*(401|503)\b/g)) codes.add(Number(m[1]));
  return codes;
}

/**
 * 取调用点的赋值左值列表（`a, b := call(...)` 里的 [a, b]）。取不到返回 null，
 * 表示这次调用没有赋值语境（直接 return、作实参、裸调用）。

 */
function assignmentTargets(body, index) {
  const lineStart = body.lastIndexOf('\n', index) + 1;
  const before = body.slice(lineStart, index);
  const m = /([A-Za-z_][\w\s,]*?)\s*:?=\s*$/.exec(before);
  if (!m) return null;
  const raw = m[1].replace(/^\s*(?:if|for|switch)\s+/, '').trim();
  if (!raw) return null;
  const names = raw.split(',').map((part) => part.trim());
  if (names.some((name) => name !== '_' && !/^[A-Za-z_]\w*$/.test(name))) return null;
  return names;
}

/**
 * 判断一次调用产出的 error 是否**逃出当前函数**。三种形态必须分开：
 *
 *   1. `x, _ := guard(...)`                  error 被显式丢弃        → 不逃出
 *   2. `actor, err := guard(...); if err != nil { return Actor{}, false }`
 *      或 `if actor, ok := h.viewer(c); ok`  error 折成 bool/零值     → 不逃出
 *      （TrustLevels 的 viewer 就是这一种：匿名拿 200，永不产出 401）
 *   3. `if err != nil { return err }` / `return guard(...)`          → 逃出
 *
 * 判定办法：把左值里最后一个名字当 error 变量，再看函数体内是否有 `return` 语句
 * 把它传出去。`err` 同名复用时按「有一处传出去就算逃出」处理（过近似，宁多不漏）。
 */
function errorEscapes(body, targets) {
  if (!targets || targets.length === 0) return true; // 直接 return / 作实参
  const last = targets[targets.length - 1];
  if (last === '_') return false;
  const names = errorAliases(body, last);
  return returnStatements(body).some((statement) => [...names].some((name) => new RegExp(`\\b${name}\\b`).test(statement)));
}

/**
 * 收集 error 变量的别名：`e := err; return e` 这类「换名再返回」必须算逃出。
 *
 * 只认 `X := Y` / `X = Y` 且右侧是**单个标识符**的形态（跨行也算），做传递闭包，
 * 所以 `e := err; f := e; return f` 也成立。
 *
 * 已知边界（有意取向）：闭包是**过近似** —— 只要函数里出现过 `X := err`，任何
 * `return X` 都算逃出，即使那条 return 在另一条分支上。宁可多报一条差集（可见、
 * 可登记），也不要漏成静默盲区；反过来漏报才会让门变绿而缺陷留下。
 * 不跟踪 `fmt.Errorf("...: %w", err)` 这类包装：那种 return 语句本身就含有 `err`，
 * 已经由 return 语句匹配覆盖。
 */
function errorAliases(body, name) {
  const names = new Set([name]);
  let changed = true;
  while (changed) {
    changed = false;
    for (const match of body.matchAll(/([A-Za-z_]\w*)\s*:?=\s*([A-Za-z_]\w*)\s*(?:\n|;|$)/g)) {
      const [, target, source] = match;
      if (!names.has(source) || names.has(target)) continue;
      names.add(target);
      changed = true;
    }
  }
  return names;
}

/**
 * 按语句切出所有 `return ...` 的表达式文本。
 *
 * 不能简单用 `return[^;{}]*err`：`return identity.Actor{}, err` 里的复合字面量花括号
 * 会提前截断匹配，把「真的把 error 传出去了」误判成「吞掉了」（Mail 的 actor 就是
 * 这一形状）。这里按括号深度找语句边界：深度 0 遇到 `;` 或换行即结束。
 */
function returnStatements(body) {
  const out = [];
  const re = /\breturn\b/g;
  let match;
  while ((match = re.exec(body))) {
    const start = match.index + match[0].length;
    let depth = 0;
    let i = start;
    for (; i < body.length; i++) {
      const ch = body[i];
      if (ch === '(' || ch === '[' || ch === '{') depth += 1;
      else if (ch === ')' || ch === ']' || ch === '}') {
        if (depth === 0) break; // 到了外层函数体的收尾
        depth -= 1;
      } else if (depth === 0 && (ch === ';' || ch === '\n')) break;
    }
    out.push(body.slice(start, i));
    re.lastIndex = i;
  }
  return out;
}

function extractCalls(body) {
  const calls = [];
  const push = (index, qualifier, name) => {
    const targets = assignmentTargets(body, index);
    calls.push({ qualifier, name, discarded: !errorEscapes(body, targets) });
  };
  for (const m of body.matchAll(/([A-Za-z_]\w*)\s*\.\s*([A-Za-z_]\w*)\s*\(/g)) {
    push(m.index, m[1], m[2]);
  }
  for (const m of body.matchAll(/(?<![.\w])([A-Za-z_]\w*)\s*\(/g)) {
    push(m.index, null, m[1]);
  }
  return calls;
}

function resolveCall(call, node, ctx) {
  const { qualifier, name } = call;
  if (qualifier && ctx.appHttpAliases.has(qualifier)) {
    return { dir: ctx.appHttpDir, name, receiverType: null };
  }
  if (qualifier && node.receiverName && qualifier === node.receiverName) {
    return { dir: node.dir, name, receiverType: node.receiverType };
  }
  return { dir: node.dir, name, receiverType: null };
}

function guardFor(dir, name, ctx) {
  const key = `${dir}::${name}`;
  if (ctx.guardIndex.has(key)) return ctx.guardIndex.get(key);
  return null;
}

/**
 * 取目标函数体。解析优先级：精确 receiver 类型 → 同名 receiver 变量 → 顶层函数 →
 * 同名方法并集（过近似）。返回体与它自己的 receiver 变量名/类型，供下一层解析。
 */
export function bodyOf(target, ctx) {
  const index = ctx.index(target.dir);
  const list = index.methods.get(target.name);
  if (target.receiverType && list) {
    const hit = list.find((item) => item.receiverType === target.receiverType);
    if (hit) {
      return { body: hit.body, receiverName: hit.receiverName, receiverType: hit.receiverType };
    }
  }
  if (target.receiverName && list) {
    const candidates = list.filter((item) => item.receiverName === target.receiverName);
    if (candidates.length > 0) {
      return {
        body: candidates.map((item) => item.body).join('\n'),
        receiverName: candidates[0].receiverName,
        receiverType: candidates.length === 1 ? candidates[0].receiverType : null,
      };
    }
  }
  if (index.topFuncs.has(target.name)) {
    return { body: index.topFuncs.get(target.name), receiverName: null, receiverType: null };
  }
  if (list && list.length > 0) {
    // 并集：多类型同名方法全部拼进来，并沿用首个 receiver 名（过近似，宁多不漏）。
    return {
      body: list.map((item) => item.body).join('\n'),
      receiverName: list[0].receiverName,
      receiverType: null,
    };
  }
  return null;
}
