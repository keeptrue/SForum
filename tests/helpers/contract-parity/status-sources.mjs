/**
 * parity 门的「实现侧状态码来源」模型：从 handler 函数体出发，沿**可静态判定的调用边**
 * 找到真正决定状态码的函数体，再把它们的状态码字面量收上来。
 *
 * 为什么需要这一层：门原来只认 handler 自身 + 一张响应助手白名单。真实实现里
 * handler 常常只是**一行委派**（`return h.topicAction(c, forum.TopicActionHide)`），
 * 状态码全在下游的同包函数里；身份守卫的状态码又在**跟包** `apphttp.*` 里
 * （`apphttp.RequireActor` → 401/503）。看不见这两类，路由的状态码集合就是空的，
 * 比对退化成 ∅ ⊆ 任意 —— 这就是 G3 要打掉的 30 条退化区。
 *
 * 三条识别形态（都要求**能静态确定**，不做猜测）：
 *
 *   F1 同包委派：`h.name(...)`（receiver 与当前节点同名）→ 本包方法；
 *                 `name(...)`（裸调用）→ 本包顶层函数。
 *   F2 跟包助手：`apphttp.name(...)` → `apps/api/app/Http` 包的顶层函数
 *                 （RequireActor / ReadActor / LoadActor / OptionalActor ...）。
 *   F3 依赖故障出口：函数体里构造 `readPathDependencyFailure` ⇒ 该路径最终由
 *                 app/Http/errors.go 的 errorHandler 渲染成 **503 + Retry-After**
 *                 （见 errors.go 的 readPathDependencyCause 分支）。这条不是猜的：
 *                 渲染函数是全局唯一的。
 *
 * **刻意不跟随的两类边**（跟了就会凭空塞状态码，门会为不存在的缺陷变红）：
 *
 *   1. 跨包领域服务：`h.service.ApplyTopicAction(...)`、`reactions.LikeTopic(...)`。
 *      它们的状态码由**领域错误**决定，静态上无法判断某个 handler 能触发哪些错误。
 *   2. 错误映射器 `mapXxxError` 族（见 ERROR_MAPPER_PATTERN）。这是本层最重要的
 *      边界，理由不是「麻烦」而是**不成立**：
 *        - `mapForumError` 的分支按 `errors.Is(err, sentinel)` 选，一个具体 handler
 *          只会命中其中几条。把整个映射器的码并进路由集合就是过近似。
 *        - 实证：`POST /topics/{id}/hide` 的映射器全量并集是
 *          {200,400,401,403,404,409,422,429,503}，而契约声明 {200,401,403,404,503}。
 *          多出的 400/409/422/429 来自 `ErrAttachmentConflict` / `ErrRejected` /
 *          cooldown 等**该 handler 不可能触发**的分支 —— 归因会让门为 4 个假缺陷变红。
 *      所以映射器**被识别但不归因**：命中即登记到 `mappers`（门会打印规模），
 *      状态码不并入路由集合。按 handler 收窄映射器需要跨到 `app/Models/**` 追
 *      sentinel 产点，属于 AST 阶段的工作（见 G3 派单：AST 重写留作后续）。
 *
 * 与 tests/helpers/reachability/ 的关系：**刻意不共享代码**。可达性门有一份独立
 * 复刻（reachability/go-source.mjs 的 parityVisibleCodes），两门必须各自算出同样的
 * 退化区，等价性由 tests/validate-contract-reachability-equivalence.test.mjs 钉住。
 */

import fs from 'node:fs';
import path from 'node:path';

/** 错误映射器族：`mapForumError` / `MapOptionsError` / 裸 `mapError`。识别但不归因。 */
export const ERROR_MAPPER_PATTERN = /^(?:map|Map)\w*Error$/;

/** 依赖故障哨兵：命中即该路径会由全局 errorHandler 渲染成 503。 */
export const DEPENDENCY_FAILURE_MARKER = 'readPathDependencyFailure';
export const DEPENDENCY_FAILURE_CODE = 503;

/** 调用边深度上限。实测 handler → 委派 → 守卫 ≤ 4 层，6 层是安全余量。 */
export const SOURCE_DEPTH = 6;

/** `app/Http` 包在控制器里的导入别名（与可达性门同一份约定）。 */
export const APP_HTTP_ALIASES = new Set(['apphttp', 'appHttp', 'http', 'aphttp']);

// ---------------------------------------------------------------------------
// Go 文本模型（与门内那份同构，只服务本模块）
// ---------------------------------------------------------------------------

/** 与 parity 门同义：注释换空格（保留字符串）、字符串与注释都换空格。 */
export function scanGoSource(source) {
  const stripped = source.split('');
  const masked = source.split('');
  const blank = (index, keepNewline = false) => {
    if (keepNewline && source[index] === '\n') return;
    stripped[index] = ' ';
    masked[index] = ' ';
  };
  let i = 0;
  while (i < source.length) {
    const ch = source[i];
    if (ch === '"' || ch === '`' || ch === "'") {
      const quote = ch;
      masked[i] = ' ';
      i += 1;
      while (i < source.length) {
        if (source[i] === '\\' && quote !== '`') {
          masked[i] = source[i] === '\n' ? '\n' : ' ';
          if (i + 1 < source.length) masked[i + 1] = source[i + 1] === '\n' ? '\n' : ' ';
          i += 2;
          continue;
        }
        const closing = source[i] === quote;
        if (source[i] !== '\n') masked[i] = ' ';
        i += 1;
        if (closing) break;
      }
      continue;
    }
    if (ch === '/' && source[i + 1] === '/') {
      while (i < source.length && source[i] !== '\n') {
        blank(i);
        i += 1;
      }
      continue;
    }
    if (ch === '/' && source[i + 1] === '*') {
      blank(i);
      blank(i + 1);
      i += 2;
      while (i < source.length && !(source[i] === '*' && source[i + 1] === '/')) {
        blank(i, true);
        i += 1;
      }
      blank(i);
      blank(i + 1);
      i += 2;
      continue;
    }
    i += 1;
  }
  return { stripped: stripped.join(''), masked: masked.join('') };
}

export function braceBody(masked, fromIndex) {
  const start = masked.indexOf('{', fromIndex);
  if (start < 0) return null;
  let depth = 0;
  for (let i = start; i < masked.length; i++) {
    if (masked[i] === '{') depth += 1;
    else if (masked[i] === '}') {
      depth -= 1;
      if (depth === 0) return masked.slice(start, i + 1);
    }
  }
  return null;
}

export function topLevelBodies(masked) {
  const bodies = new Map();
  const pattern = /^func\s+([A-Za-z_]\w*)\s*\(/gm;
  let match;
  while ((match = pattern.exec(masked))) {
    const body = braceBody(masked, match.index + match[0].length);
    if (body) bodies.set(match[1], { body, params: paramNamesAt(masked, match.index + match[0].length - 1) });
  }
  return bodies;
}

/**
 * 取参数名列表：`func f(c fiber.Ctx, add bool)` → `['c', 'add']`。
 *
 * 只服务「按实参字面量剪掉不可达分支」（见 pruneBoolBranches），所以只认
 * `名字 类型` 的常见形态；`a, b int` 这种合并写法按出现顺序拆开。
 */
export function paramNamesAt(masked, openParenIndex) {
  const close = closingParen(masked, openParenIndex);
  if (close < 0) return [];
  const text = masked.slice(openParenIndex + 1, close);
  const names = [];
  for (const part of splitParams(text)) {
    const tokens = part.trim().split(/\s+/);
    if (tokens.length < 2) continue;
    for (const token of tokens.slice(0, -1)) {
      const name = token.replace(/\*/g, '').trim();
      if (/^[A-Za-z_]\w*$/.test(name)) names.push(name);
    }
  }
  return names;
}

function splitParams(text) {
  const out = [];
  let depth = 0;
  let current = '';
  for (const ch of text) {
    if (ch === '(' || ch === '[' || ch === '{') depth += 1;
    if (ch === ')' || ch === ']' || ch === '}') depth -= 1;
    if (ch === ',' && depth === 0) {
      out.push(current);
      current = '';
      continue;
    }
    current += ch;
  }
  if (current.trim()) out.push(current);
  return out;
}

function closingParen(masked, openParenIndex) {
  let depth = 0;
  for (let i = openParenIndex; i < masked.length; i++) {
    if (masked[i] === '(') depth += 1;
    else if (masked[i] === ')') {
      depth -= 1;
      if (depth === 0) return i;
    }
  }
  return -1;
}

export function methodBodies(masked) {
  const bodies = new Map();
  const pattern = /^func\s*\(\s*([A-Za-z_]\w*)\s+\*?\s*([A-Za-z_]\w*)\s*\)\s*([A-Za-z_]\w*)\s*\(/gm;
  let match;
  while ((match = pattern.exec(masked))) {
    const body = braceBody(masked, match.index + match[0].length);
    if (!body) continue;
    const name = match[3];
    if (!bodies.has(name)) bodies.set(name, []);
    bodies.get(name).push({
      receiverName: match[1],
      receiverType: match[2],
      body,
      params: paramNamesAt(masked, match.index + match[0].length - 1),
    });
  }
  return bodies;
}

const STATUS_CONSTANTS = {
  StatusContinue: 100, StatusSwitchingProtocols: 101, StatusProcessing: 102, StatusEarlyHints: 103,
  StatusOK: 200, StatusCreated: 201, StatusAccepted: 202, StatusNonAuthoritativeInformation: 203,
  StatusNoContent: 204, StatusResetContent: 205, StatusPartialContent: 206, StatusMultiStatus: 207,
  StatusAlreadyReported: 208, StatusIMUsed: 226,
  StatusMultipleChoices: 300, StatusMovedPermanently: 301, StatusFound: 302, StatusSeeOther: 303,
  StatusNotModified: 304, StatusUseProxy: 305, StatusSwitchProxy: 306, StatusTemporaryRedirect: 307,
  StatusPermanentRedirect: 308,
  StatusBadRequest: 400, StatusUnauthorized: 401, StatusPaymentRequired: 402, StatusForbidden: 403,
  StatusNotFound: 404, StatusMethodNotAllowed: 405, StatusNotAcceptable: 406, StatusProxyAuthRequired: 407,
  StatusRequestTimeout: 408, StatusConflict: 409, StatusGone: 410, StatusLengthRequired: 411,
  StatusPreconditionFailed: 412, StatusRequestEntityTooLarge: 413, StatusRequestURITooLong: 414,
  StatusUnsupportedMediaType: 415, StatusRequestedRangeNotSatisfiable: 416, StatusExpectationFailed: 417,
  StatusTeapot: 418, StatusMisdirectedRequest: 421, StatusUnprocessableEntity: 422, StatusLocked: 423,
  StatusFailedDependency: 424, StatusTooEarly: 425, StatusUpgradeRequired: 426, StatusPreconditionRequired: 428,
  StatusTooManyRequests: 429, StatusRequestHeaderFieldsTooLarge: 431, StatusUnavailableForLegalReasons: 451,
  StatusInternalServerError: 500, StatusNotImplemented: 501, StatusBadGateway: 502,
  StatusServiceUnavailable: 503, StatusGatewayTimeout: 504, StatusHTTPVersionNotSupported: 505,
  StatusVariantAlsoNegotiates: 506, StatusInsufficientStorage: 507, StatusLoopDetected: 508,
  StatusNotExtended: 510, StatusNetworkAuthenticationRequired: 511,
};

/** 与门内 statusesIn 同义；unknownConstants 由调用方传入以便统一报红。 */
export function statusesIn(body, unknownConstants) {
  const codes = new Set();
  for (const m of body.matchAll(/\b(?:fiber|http|nethttp|stdhttp|net\/http)\.(Status[A-Za-z]+)\b/g)) {
    const value = STATUS_CONSTANTS[m[1]];
    if (value === undefined) {
      if (unknownConstants) unknownConstants.add(m[1]);
    } else codes.add(value);
  }
  for (const m of body.matchAll(/\.Status\(\s*(\d{3})\s*\)/g)) codes.add(Number(m[1]));
  for (const m of body.matchAll(/\bStatus:\s*(\d{3})\b/g)) codes.add(Number(m[1]));
  return codes;
}

// ---------------------------------------------------------------------------
// 包索引
// ---------------------------------------------------------------------------

export function listGoFiles(dir) {
  const out = [];
  if (!fs.existsSync(dir)) return out;
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.name.startsWith('.') || entry.name === 'node_modules') continue;
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) out.push(...listGoFiles(full));
    else if (entry.name.endsWith('.go') && !entry.name.endsWith('_test.go')) out.push(full);
  }
  return out;
}

function buildPackageIndex(dir) {
  const files = fs
    .readdirSync(dir)
    .filter((name) => name.endsWith('.go') && !name.endsWith('_test.go'))
    .sort();
  const topFuncs = new Map();
  const methods = new Map();
  for (const name of files) {
    const { masked } = scanGoSource(fs.readFileSync(path.join(dir, name), 'utf8'));
    for (const [func, body] of topLevelBodies(masked)) {
      if (!topFuncs.has(func)) topFuncs.set(func, body);
    }
    for (const [func, list] of methodBodies(masked)) {
      if (!methods.has(func)) methods.set(func, []);
      methods.get(func).push(...list);
    }
  }
  return { dir, files, topFuncs, methods };
}

/** 按需建包索引（同一目录只扫一次）。 */
export function createPackageIndexFactory() {
  const cache = new Map();
  return (dir) => {
    const abs = path.resolve(dir);
    if (!cache.has(abs)) cache.set(abs, buildPackageIndex(abs));
    return cache.get(abs);
  };
}

// ---------------------------------------------------------------------------
// 识别形态
// ---------------------------------------------------------------------------

function methodBodyOf(index, name, receiverName) {
  const list = index.methods.get(name);
  if (!list || list.length === 0) return null;
  if (receiverName) {
    const hit = list.filter((item) => item.receiverName === receiverName);
    if (hit.length === 1) return { body: hit[0].body, params: hit[0].params, receiverName: hit[0].receiverName };
    if (hit.length > 1) {
      return {
        body: hit.map((item) => item.body).join('\n'),
        params: hit[0].params,
        receiverName,
      };
    }
  }
  // 无 receiver 线索时取并集（过近似），但只在同名方法确实存在时。
  return { body: list.map((item) => item.body).join('\n'), params: list[0].params, receiverName: list[0].receiverName };
}

/**
 * 按**实参字面量**剪掉不可达分支。
 *
 * 为什么必须剪：同一族 handler 常把「加/删」折进一个 bool 参数（`applyTopicBookmark(c, add)`），
 * 而下游解码函数在 `if !add { return }` 之后就只服务「加」这一侧。整段跟随会把「加」侧
 * 独有的 422 塞给 DELETE —— 门就会为一个**实现明确不可能产出**的码变红
 * （`decodeBookmarkBody` 的注释写着「契约里 DELETE 也就不必声明 422」）。
 *
 * 只处理一种形态，够用且可判：函数体**顶层**的 `if <param> {` / `if !<param> {` 块，
 * 且调用点给该参数传了 bool 字面量。两种情况：
 *   - 块**会执行**且块内顶层有 `return` → 块之后全部不可达，截断；
 *   - 块**不会执行** → 整块删掉，其余保留。
 * 其它形态（无字面量、参数不是 bool、条件不是裸参数）一律不剪 —— 宁可不判，不能误剪。
 */
export function pruneBoolBranches(body, params, args) {
  if (!params || !args) return body;
  let current = body;
  for (let index = 0; index < params.length; index += 1) {
    const arg = (args[index] || '').trim();
    if (arg !== 'true' && arg !== 'false') continue;
    const value = arg === 'true';
    const param = params[index];
    // 每轮必须真的改动文本才继续：条件命中但不剪（块会执行且无顶层 return）时
    // 不改文本，继续循环会原地打转。
    let cursor = 0;
    for (;;) {
      const guard = findTopLevelBoolGuard(current, param, cursor);
      if (!guard) break;
      // `if !p` 在 p=false 时执行，`if p` 在 p=true 时执行 —— 即「取反后等于值」。
      if (guard.negated !== value) {
        if (!hasTopLevelReturn(guard.block)) {
          cursor = guard.end; // 这一处不剪，跳过它继续找下一处
          continue;
        }
        // 块会执行且块内顶层 return：块之后全部不可达，只保留到块尾。
        current = current.slice(0, guard.end);
        break;
      }
      // 块不会执行：整块删掉。
      current = current.slice(0, guard.index) + current.slice(guard.end);
      cursor = guard.index;
    }
  }
  return current;
}

function findTopLevelBoolGuard(body, param, from = 0) {
  const pattern = new RegExp(`if\\s+(!?)\\s*${param}\\b\\s*\\{`, 'g');
  pattern.lastIndex = from;
  let match;
  while ((match = pattern.exec(body))) {
    if (depthAt(body, match.index) !== 1) continue;
    const open = match.index + match[0].length - 1;
    const close = closingBrace(body, open);
    if (close < 0) return null;
    return { index: match.index, end: close + 1, negated: match[1] === '!', block: body.slice(open, close + 1) };
  }
  return null;
}

function depthAt(body, index) {
  let depth = 0;
  for (let i = 0; i < index; i += 1) {
    if (body[i] === '{') depth += 1;
    else if (body[i] === '}') depth -= 1;
  }
  return depth;
}

function closingBrace(body, openIndex) {
  let depth = 0;
  for (let i = openIndex; i < body.length; i += 1) {
    if (body[i] === '{') depth += 1;
    else if (body[i] === '}') {
      depth -= 1;
      if (depth === 0) return i;
    }
  }
  return -1;
}

function hasTopLevelReturn(block) {
  for (const match of block.matchAll(/\breturn\b/g)) {
    if (depthAt(block, match.index) === 1) return true;
  }
  return false;
}

/**
 * 从一个函数体出发收集可达状态码。
 *
 * 返回：
 *   codes           归因到该 handler 的状态码集合
 *   mappers         命中但**刻意不归因**的错误映射器名（门会打印规模）
 *   appHttpHits     真正被跟随的跟包 app/Http 函数名（门会打印规模）
 *   skipped         因「白名单形态是权威口径」而跳过跟随的函数名
 *   swallowed       error 被折成 bool / 显式丢弃、因而不产出状态码的调用名
 *   prunedBranches  因实参字面量被剪掉的分支（`decodeBookmarkBody(c, false)` 之类）
 *   dependency      是否命中依赖故障出口（503）
 *   attributed      **按 handler 收窄后**归因到的映射器业务码（#237；由调用方注入
 *                   `attribute` 回调，本模块不知道哨兵怎么解析 —— 见 mapper-attribution.mjs）
 */
export function collectReachableStatuses({
  body,
  dir,
  receiverName,
  indexOf,
  appHttpDir,
  unknownConstants,
  registry,
  attribute,
  depth = SOURCE_DEPTH,
}) {
  const codes = new Set(statusesIn(body, unknownConstants));
  const mappers = new Set();
  const appHttpHits = new Set();
  const skipped = new Set();
  const swallowed = new Set();
  const prunedBranches = new Set();
  const attributed = new Set();
  if (attribute) for (const code of attribute(body, dir)) attributed.add(code);
  let dependency = false;
  const visited = new Set();

  const noteBody = (text) => {
    if (text.includes(DEPENDENCY_FAILURE_MARKER)) {
      dependency = true;
      codes.add(DEPENDENCY_FAILURE_CODE);
    }
  };
  noteBody(body);

  // env：当前节点里「参数 → bool 字面量」的绑定。调用点的字面量要沿调用链传下去，
  // 否则 `unbookmarkTopic(c)` → `applyTopicBookmark(c, false)` → `decodeBookmarkBody(c, add)`
  // 这条链到第二跳就丢了 false，下游「加」侧独有的 422 会被误算到 DELETE 上。
  const queue = [{ body, dir, receiverName, level: 0, env: new Map() }];
  while (queue.length > 0) {
    const node = queue.shift();
    if (node.level >= depth) continue;
    // 每个被跟随到的节点都做一次收窄归因：`h.topicAction(c, ...)` 这类一行委派的
    // handler，业务码是在下游节点里经 `h.service.X` 产生的，只看首节点会漏。
    if (attribute && node.level > 0) {
      for (const code of attribute(node.body, node.dir)) attributed.add(code);
    }
    const index = indexOf(node.dir);

    for (const call of extractSourceCalls(node, index, appHttpDir)) {
      const key = `${call.dir}::${call.name}`;
      if (visited.has(key)) continue;
      visited.add(key);
      const registryHit = registry ? registry(call.name, node.body, call.callIndex) : null;
      if (registryHit && registryHit.codes) {
        // 白名单形态（RESPONSE_HELPERS）对同名函数是**权威**：固定码由登记表给出，
        // 不再跟随函数体（跟随会引入第二套口径 —— `NoData` 登记 204、实现写 200，
        // 跟了就会给 8 个「无内容」端点塞进 200 并让门变红）。
        // 但**委派下游**的助手调用同样要走这条口径，否则 `return h.action(c)` 这类
        // 一行委派的 handler 拿不到 apphttp.OK 的 200。
        skipped.add(call.name);
        for (const code of registryHit.codes) codes.add(code);
        continue;
      }
      if (registryHit && registryHit.follow) {
        // GUARD_HELPERS / `follow: true` 的助手：按登记表的语义跟随函数体。
        const target = resolveTarget({ ...call, crossPackage: false }, indexOf);
        if (!target) continue;
        for (const code of statusesIn(target.body, unknownConstants)) codes.add(code);
        noteBody(target.body);
        queue.push({ body: target.body, dir: call.dir, receiverName: target.receiverName, level: node.level + 1, env: new Map() });
        continue;
      }
      if (ERROR_MAPPER_PATTERN.test(call.name)) {
        // 识别但不归因：映射器的码按 err 分支产生，静态无法按 handler 收窄。
        mappers.add(call.name);
        continue;
      }
      if (call.crossPackage) appHttpHits.add(call.name);
      if (!callErrorEscapes(node.body, call.callIndex)) {
        // error 被折成 bool / 显式丢弃：该调用不产出可观测状态码（`viewer, ok := h.viewer(c)`）。
        swallowed.add(call.name);
        continue;
      }
      const target = resolveTarget(call, indexOf);
      if (!target) continue;
      const args = call.args.map((arg) => node.env.get(arg) || arg);
      const pruned = pruneBoolBranches(target.body, target.params, args);
      if (pruned !== target.body) prunedBranches.add(`${call.name}(${args.join(', ')})`);
      for (const code of statusesIn(pruned, unknownConstants)) codes.add(code);
      noteBody(pruned);
      const env = new Map();
      (target.params || []).forEach((param, position) => {
        const bound = args[position];
        if (bound === 'true' || bound === 'false') env.set(param, bound);
      });
      queue.push({ body: pruned, dir: call.dir, receiverName: target.receiverName, level: node.level + 1, env });
    }
  }

  return { codes, mappers, appHttpHits, skipped, swallowed, prunedBranches, dependency, attributed };
}

function splitCallArgs(body, openParenIndex) {
  const args = [];
  let depth = 0;
  let current = '';
  for (let i = openParenIndex + 1; i < body.length; i += 1) {
    const ch = body[i];
    if (ch === '(' || ch === '[' || ch === '{') depth += 1;
    if (ch === ')' || ch === ']' || ch === '}') {
      if (depth === 0) break;
      depth -= 1;
    }
    if (ch === ',' && depth === 0) {
      args.push(current.trim());
      current = '';
      continue;
    }
    current += ch;
  }
  if (current.trim()) args.push(current.trim());
  return args;
}

/**
 * 从一个函数体里取出三条形态的调用点：
 *   - `apphttp.Name(` → 跟包 app/Http 顶层函数（F2）
 *   - `<recv>.Name(` 且 recv 是当前节点的 receiver 名 → 本包方法（F1）
 *   - 裸 `Name(` → 本包顶层函数（F1）
 *
 * 刻意不匹配 `<x>.<y>.Name(`（领域服务调用）与 `<x>.Name(` 里的其它 receiver：
 * 那些是别的类型的领域方法，静态上不属于「响应路径」。
 */
function extractSourceCalls(node, index, appHttpDir) {
  const calls = [];
  const seen = new Set();
  const push = (dir, name, crossPackage, callIndex) => {
    const key = `${dir}::${name}::${crossPackage}`;
    if (seen.has(key)) return;
    seen.add(key);
    // 记下调用点的实参文本：pruneBoolBranches 靠它判断下游哪一侧分支不可达。
    const open = node.body.indexOf('(', callIndex);
    calls.push({ dir, name, crossPackage, callIndex, args: open >= 0 ? splitCallArgs(node.body, open) : [] });
  };

  if (appHttpDir) {
    for (const m of node.body.matchAll(/(?<![\w.])([A-Za-z_]\w*)\s*\.\s*([A-Za-z_]\w*)\s*\(/g)) {
      if (!APP_HTTP_ALIASES.has(m[1])) continue;
      push(appHttpDir, m[2], true, m.index);
    }
  }
  if (node.receiverName) {
    const pattern = new RegExp(`(?<![\\w.])${node.receiverName}\\s*\\.\\s*([A-Za-z_]\\w*)\\s*\\(`, 'g');
    for (const m of node.body.matchAll(pattern)) {
      if (!index.methods.has(m[1])) continue;
      push(node.dir, m[1], false, m.index);
    }
  }
  for (const m of node.body.matchAll(/(?<![\w.])([A-Za-z_]\w*)\s*\(/g)) {
    const name = m[1];
    if (name === 'func') continue;
    if (!index.topFuncs.has(name)) continue;
    push(node.dir, name, false, m.index);
  }
  return calls;
}

function resolveTarget(call, indexOf) {
  const index = indexOf(call.dir);
  if (index.topFuncs.has(call.name)) {
    const hit = index.topFuncs.get(call.name);
    return { body: hit.body, params: hit.params, receiverName: null };
  }
  if (!call.crossPackage) return methodBodyOf(index, call.name, null);
  return null;
}

/**
 * 一次调用的 **error 是否逃出当前函数**。
 *
 * 这是「调用边识别」必须有的精度闸门：不判它，`viewer, ok := h.viewer(c)` 这种把
 * error 折成 bool 的调用会把守卫的 401/503 硬塞进端点集合，门就会为不存在的缺陷变红
 * （TrustLevels 的 `GET /trust-levels` 就是这样：守卫语义是「匿名拿 200」，不是 401）。
 *
 * 判定与可达性门的 call-graph 同构（同一套形态，独立实现）：
 *   1. 没有赋值左值（`return f(...)` / 作实参 / 裸调用）→ 逃出；
 *   2. 左值末位是 `_` → 被显式丢弃，不逃出；
 *   3. 否则看函数体内是否有 `return` 语句提到这个名字（含别名 `e := err` 传递闭包）；
 *      一处都没有 → 不逃出。
 */
function callErrorEscapes(body, callIndex) {
  const lineStart = body.lastIndexOf('\n', callIndex) + 1;
  const before = body.slice(lineStart, callIndex);
  const m = /([A-Za-z_][\w\s,]*?)\s*:?=\s*$/.exec(before);
  if (!m) return true;
  const raw = m[1].replace(/^\s*(?:if|for|switch)\s+/, '').trim();
  if (!raw) return true;
  const names = raw.split(',').map((part) => part.trim());
  if (names.some((name) => name !== '_' && !/^[A-Za-z_]\w*$/.test(name))) return true;
  const last = names[names.length - 1];
  if (last === '_') return false;
  const aliases = errorAliases(body, last);
  return returnStatements(body).some((statement) =>
    [...aliases].some((name) => new RegExp(`\\b${name}\\b`).test(statement)),
  );
}

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
        if (depth === 0) break;
        depth -= 1;
      } else if (depth === 0 && (ch === ';' || ch === '\n')) break;
    }
    out.push(body.slice(start, i));
    re.lastIndex = i;
  }
  return out;
}
