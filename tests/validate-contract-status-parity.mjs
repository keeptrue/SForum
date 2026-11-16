#!/usr/bin/env node
/**
 * 契约状态码 parity 常设门（RV-38 的防回归）。
 *
 * 比对方向固定为「实现可能产出的状态码 ⊆ OpenAPI 声明的状态码」：
 * - 契约多声明（例如中间件统一兜出的 401/403）不算问题；
 * - 实现产出了契约没声明的码，就是 RV-38 那种回归，门变红。
 *
 * 数据源两侧都是仓库内的静态事实，不连网、不启服务：
 * - 契约：contracts/openapi.yaml 的 paths 索引 → openapi/paths/<模块>.yaml 的 path item
 *   → 每个 method 的 responses 键。响应体是 `$ref: components/responses.yaml#/Xxx` 时，
 *   真实码由 responses 的键给出，$ref 只影响描述，所以不做整文件映射；`default` 与
 *   `2XX/4XX` 这类范围键按整段放行。
 * - 实现：各控制器包 RegisterRoutes 里的 `<router>.<Method>("/path", h.handler)` 注册，
 *   按 `api := app.Group("/api/v1")` 起算的 group 链拼出完整路径；再定位同包 handler 函数体，
 *   收集 `fiber.StatusXxx` / `http.StatusXxx` / `c.Status(数字)` / `Status: 数字`，
 *   以及同包 `respond* / fail* / send* / write*` 响应辅助函数里的状态码。
 *   另有一条**显式白名单**的 guard 助手（见 GUARD_HELPERS）：它们是实现里的鉴权分支，
 *   语义等价于「可能产出的状态码」，但名字不带 respond/fail/send/write 前缀。
 *   白名单手工维护、逐条核对语义，不做前缀泛匹配——泛匹配会把业务函数当真，
 *   凭空给路由塞状态码。
 *
 * 两类例外必须逐条写进 tests/contract-status-parity-baseline.json 并带中文原因：
 * 1. 不可静态判定：通配/插件代理路由，契约 operation 无法静态落到某个 handler；
 * 2. 已复核未声明状态码：实现确实会产出、契约没声明，人工判定后接受的条目
 *    （审计遗留来不及补契约，或静态扫描无法按方法/分支拆分）。
 * 没有登记在基线里的未映射 operation 或未声明状态码一律变红，不会静默放行。
 *
 * 用法：
 *   node tests/validate-contract-status-parity.mjs            # 跑门
 *   node tests/validate-contract-status-parity.mjs --refresh  # 只刷新基线里的「期望计数」
 *   node tests/validate-contract-status-parity.mjs --print-empty-routes
 *                                                             # 只额外打印「状态码集合为空」的路由清单
 *
 * --refresh 只重写计数，绝不增删「不可静态判定」「已复核未声明状态码」条目：例外集合只能人工确认。
 */

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(SCRIPT_DIR, '..');
const CONTRACT_ENTRY = path.join(ROOT, 'contracts', 'openapi.yaml');
const CONTRACT_ROOT = path.join(ROOT, 'contracts');
const API_ROOT = path.join(ROOT, 'apps', 'api');
const BASELINE_PATH = path.join(SCRIPT_DIR, 'contract-status-parity-baseline.json');
const PUBLIC_READ_401_MANIFEST = path.join(SCRIPT_DIR, 'contract-public-read-401.json');

const SERVER_PREFIX = '/api/v1'; // 与 apps/api/app/Http/server.go 的 app.Group("/api/v1") 对应
const HTTP_METHODS = ['get', 'post', 'put', 'patch', 'delete', 'head', 'options', 'trace'];
const FIBER_METHODS = ['Get', 'Post', 'Put', 'Patch', 'Delete', 'Head', 'Options', 'All'];
const HELPER_PATTERN = /^(respond|fail|send|write)/i;
const HELPER_DEPTH = 2; // 辅助函数最多跟随两层，够覆盖 respond*/fail* 链，避免无限递归

// 显式 guard 助手白名单：键是函数名，值是「为什么它可以被当响应辅助函数」的语义说明。
// 只收**叶子守卫**：函数体自己就直接 return fiber.NewError(...) 决定状态码，不靠调用方
// 的 mapXxxError 兜底。同名实现分布在多个控制器包时按包各自取（见 readCoreRoutes）。
// 新增条目必须逐条核对：① 确实是鉴权/策略分支；② 状态码由它自己产出；③ 名字不加前缀
// 会被误读成业务函数时，宁可不收。
const GUARD_HELPERS = new Map([
  [
    'requireGuestRead',
    '游客读策略守卫：forum.guest.read=login_required 时匿名读直接 401；Profile 的同名实现另含 503（读策略快照缺失）',
  ],
  [
    'requireAdmin',
    '后台读权限守卫：产品开关关闭 404、缺 admin.access 权限 403，二者都在守卫函数体内直接产出',
  ],
]);

// fiber/v3 与 net/http 的 Status 常量（两者同名的值一致）。遇到表外常量直接失败，
// 避免新常量被静默忽略。
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

const failures = [];
const unknownConstants = new Set();
// 白名单里真正在实现侧找到函数体的 guard 名字；缺席即视为白名单过期（见 main）。
const guardHelperHits = new Set();

function fail(message) {
  failures.push(message);
}

function indentOf(line) {
  const m = /^[ \t]*/.exec(line);
  return m ? m[0].length : 0;
}

function isBlank(line) {
  return line.trim() === '' || line.trim().startsWith('#');
}

function readLines(file) {
  return fs.readFileSync(file, 'utf8').split(/\r?\n/);
}

// ---------------------------------------------------------------------------
// 契约侧
// ---------------------------------------------------------------------------

function parseContractIndex() {
  const lines = readLines(CONTRACT_ENTRY);
  const start = lines.findIndex((line) => /^paths:\s*$/.test(line));
  if (start < 0) {
    throw new Error('contracts/openapi.yaml 缺少 paths 段');
  }
  const entries = [];
  let keyLines = 0; // paths: 段内缩进 2 的行（每条都必须是路径键，不允许静默跳过）
  for (let i = start + 1; i < lines.length; i++) {
    const line = lines[i];
    if (!isBlank(line) && indentOf(line) === 0) break; // paths 段结束
    if (isBlank(line) || indentOf(line) !== 2) continue;
    keyLines += 1;
    // 路径键可能带双引号、单引号，也可能不加引号（openapi.yaml 里两种都存在）。
    const keyMatch = /^ {2}(?:"([^"]+)"|'([^']+)'|(\/[^:]*?))\s*:\s*$/.exec(line);
    if (!keyMatch) {
      throw new Error(
        `contracts/openapi.yaml 第 ${i + 1} 行的 paths 条目不是可解析的路径键，拒绝静默跳过：${line.trim()}`,
      );
    }
    const pathKey = keyMatch[1] ?? keyMatch[2] ?? keyMatch[3];
    const refMatch = /^\s{4}"?\$ref"?\s*:\s*"?([^"\s]+)"?\s*$/.exec(lines[i + 1] || '');
    if (!refMatch) {
      throw new Error(`contracts/openapi.yaml 的 paths."${pathKey}" 没有 $ref 指向 path item`);
    }
    const [file, pointer] = refMatch[1].split('#');
    entries.push({ path: pathKey, file, pointer: (pointer || '').replace(/^\//, '') });
    i += 1;
  }
  if (keyLines !== entries.length) {
    throw new Error(
      `contracts/openapi.yaml 自检失败：paths 段缩进 2 的条目 ${keyLines} 行，只解析出 ${entries.length} 条`,
    );
  }
  return entries;
}

function slicePathItem(lines, pointer, sourceLabel) {
  const pattern = new RegExp(`^${pointer.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}:\\s*$`);
  const start = lines.findIndex((line) => pattern.test(line));
  if (start < 0) {
    throw new Error(`${sourceLabel} 找不到 path item "${pointer}"`);
  }
  let end = lines.length;
  for (let i = start + 1; i < lines.length; i++) {
    if (!isBlank(lines[i]) && indentOf(lines[i]) === 0) {
      end = i;
      break;
    }
  }
  return lines.slice(start + 1, end);
}

function responsesOf(block, methodLineIndex, sourceLabel) {
  const methodIndent = indentOf(block[methodLineIndex]);
  let methodEnd = block.length;
  for (let i = methodLineIndex + 1; i < block.length; i++) {
    if (!isBlank(block[i]) && indentOf(block[i]) <= methodIndent) {
      methodEnd = i;
      break;
    }
  }
  const methodBlock = block.slice(methodLineIndex + 1, methodEnd);
  // operation 的直接子键缩进 = 块内最小缩进；description 多行文本缩得更深，按缩进等值匹配即可排除误命中。
  const childIndent = Math.min(
    ...methodBlock.filter((line) => !isBlank(line)).map((line) => indentOf(line)),
  );
  const responsesIndex = methodBlock.findIndex(
    (line) => indentOf(line) === childIndent && /^\s*responses:\s*\S*/.test(line),
  );
  if (responsesIndex < 0) {
    throw new Error(`${sourceLabel} 的 operation 缺少 responses 段`);
  }
  const responsesIndent = indentOf(methodBlock[responsesIndex]);
  const codes = new Set();
  // default / 2XX / 4XX 这类范围响应涵盖整段状态码，单独记下来，比对时按范围放行。
  const ranges = new Set();
  const noteKey = (key) => {
    const numeric = /^(\d{3})$/.exec(key);
    if (numeric) {
      codes.add(Number(numeric[1]));
      return;
    }
    if (/^default$/i.test(key)) ranges.add('all');
    else if (/^[1-5]XX$/i.test(key)) ranges.add(key[0]);
  };
  const inline = methodBlock[responsesIndex].replace(/^\s*responses:\s*/, '').trim();
  if (inline.startsWith('{')) {
    // 形如 responses: { '200': {...}, '403': {...} }
    for (const m of inline.matchAll(/'?([0-9A-Za-z]+)'?\s*:/g)) noteKey(m[1]);
  } else {
    for (let i = responsesIndex + 1; i < methodBlock.length; i++) {
      const line = methodBlock[i];
      if (isBlank(line)) continue;
      if (indentOf(line) <= responsesIndent) break;
      const m = /^\s*'?([0-9A-Za-z]+)'?\s*:/.exec(line);
      if (m) noteKey(m[1]);
    }
  }
  if (codes.size === 0 && ranges.size === 0) {
    throw new Error(`${sourceLabel} 的 operation 解析不出任何状态码`);
  }
  return { codes, ranges };
}

function readContractOperations() {
  const entries = parseContractIndex();
  if (entries.length === 0) {
    throw new Error('contracts/openapi.yaml 的 paths 索引里一条 path 都没解析出来');
  }
  const fileCache = new Map();
  const operations = [];

  for (const entry of entries) {
    const file = path.resolve(CONTRACT_ROOT, entry.file);
    if (!fileCache.has(file)) fileCache.set(file, readLines(file));
    const label = `${entry.file}#/${entry.pointer}`;
    const block = slicePathItem(fileCache.get(file), entry.pointer, label);
    let found = 0;
    for (let i = 0; i < block.length; i++) {
      const m = /^\s*([a-z]+):\s*$/.exec(block[i]);
      if (!m || !HTTP_METHODS.includes(m[1])) continue;
      found += 1;
      const declared = responsesOf(block, i, label);
      operations.push({
        method: m[1].toUpperCase(),
        path: SERVER_PREFIX + entry.path,
        codes: [...declared.codes].sort((a, b) => a - b),
        ranges: [...declared.ranges],
        source: label,
      });
    }
    if (found === 0) {
      throw new Error(`${label} 是空 path item（没有任何 HTTP method）`);
    }
  }
  return { operations, pathCount: entries.length };
}

// ---------------------------------------------------------------------------
// 实现侧：Go 源码扫描
// ---------------------------------------------------------------------------

/**
 * 一次扫描产出两份与源码等长的文本，索引可直接互换：
 * - stripped：保留字符串字面量，注释换成空格（读路由注册里的路径字符串）；
 * - masked：字符串与注释都换成空格（花括号配对、状态码扫描，避免误命中注释/字面量）。
 */
function scanGoSource(source) {
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

function braceBody(masked, fromIndex) {
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

function methodBody(masked, name) {
  const pattern = new RegExp(`func\\s*\\([^)]*\\)\\s*${name}\\s*\\(`, 'g');
  let match;
  while ((match = pattern.exec(masked))) {
    const body = braceBody(masked, match.index + match[0].length);
    if (body) return body;
  }
  return null;
}

function functionBodies(masked) {
  const bodies = new Map();
  const pattern = /^func\s+([A-Za-z_]\w*)\s*\(/gm;
  let match;
  while ((match = pattern.exec(masked))) {
    const body = braceBody(masked, match.index + match[0].length);
    if (body) bodies.set(match[1], body);
  }
  return bodies;
}

function statusesIn(body) {
  const codes = new Set();
  for (const m of body.matchAll(/\b(?:fiber|http|nethttp|stdhttp|net\/http)\.(Status[A-Za-z]+)\b/g)) {
    const value = STATUS_CONSTANTS[m[1]];
    if (value === undefined) unknownConstants.add(m[1]);
    else codes.add(value);
  }
  for (const m of body.matchAll(/\.Status\(\s*(\d{3})\s*\)/g)) codes.add(Number(m[1]));
  for (const m of body.matchAll(/\bStatus:\s*(\d{3})\b/g)) codes.add(Number(m[1]));
  return codes;
}

function splitTopLevelArgs(stripped, openIndex) {
  const args = [];
  let depth = 0;
  let current = '';
  let currentStart = openIndex + 1;
  let started = false;
  let i = openIndex + 1;
  for (; i < stripped.length; i++) {
    const ch = stripped[i];
    if (ch === '"' || ch === '`' || ch === "'") {
      if (!started) {
        currentStart = i;
        started = true;
      }
      const quote = ch;
      let literal = ch;
      i += 1;
      while (i < stripped.length) {
        if (stripped[i] === '\\' && quote !== '`') {
          literal += stripped[i] + (stripped[i + 1] || '');
          i += 2;
          continue;
        }
        literal += stripped[i];
        if (stripped[i] === quote) break;
        i += 1;
      }
      current += literal;
      continue;
    }
    if (ch === '(' || ch === '[' || ch === '{') depth += 1;
    if (ch === ')' || ch === ']' || ch === '}') {
      if (depth === 0) break; // 调用自身的右括号
      depth -= 1;
    }
    if (ch === ',' && depth === 0) {
      if (current.trim()) args.push({ text: current.trim(), start: currentStart });
      current = '';
      started = false;
      continue;
    }
    if (!started && ch.trim() !== '') {
      currentStart = i;
      started = true;
    }
    current += ch;
  }
  if (current.trim()) args.push({ text: current.trim(), start: currentStart });
  return { args, closeIndex: i };
}

function listGoFiles(dir) {
  const out = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.name.startsWith('.') || entry.name === 'node_modules') continue;
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) out.push(...listGoFiles(full));
    else if (entry.name.endsWith('.go') && !entry.name.endsWith('_test.go')) out.push(full);
  }
  return out;
}

function readCoreRoutes() {
  const files = listGoFiles(API_ROOT);
  const routes = new Map();
  const packageCache = new Map();
  const skippedRouters = new Set();

  const packageOf = (dir) => {
    if (!packageCache.has(dir)) {
      const texts = fs
        .readdirSync(dir)
        .filter((name) => name.endsWith('.go') && !name.endsWith('_test.go'))
        .map((name) => fs.readFileSync(path.join(dir, name), 'utf8'));
      packageCache.set(dir, {
        masked: texts.map((text) => scanGoSource(text).masked),
        bodies: texts.map(functionBodies),
      });
    }
    return packageCache.get(dir);
  };

  for (const file of files) {
    const source = fs.readFileSync(file, 'utf8');
    if (!/\.(Get|Post|Put|Patch|Delete|Head|Options|All)\s*\(/.test(source)) continue;
    const { stripped, masked } = scanGoSource(source);
    const dir = path.dirname(file);
    const relFile = path.relative(ROOT, file);

    // group 链：api 来自 server.go 的 app.Group("/api/v1")，其余在文件内按出现顺序解析
    const groups = new Map([
      ['api', SERVER_PREFIX],
      ['app', ''],
    ]);
    for (const m of stripped.matchAll(/([A-Za-z_]\w*)\s*:?=\s*([A-Za-z_]\w*)\.Group\(\s*"([^"]*)"/g)) {
      const parent = groups.get(m[2]);
      if (parent === undefined) {
        fail(`${relFile}: group "${m[1]}" 的父级 "${m[2]}" 无法解析，路径会算错`);
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
        // 非路由对象（store.Get(ctx)、cache.Delete(ctx) 之类）不是注册语句；
        // 但首参是 "/..." 字面量说明确实在注册路由，只是 router 前缀解析不出来，必须报出来。
        if (pathLiteral && pathLiteral[1].startsWith('/')) {
          skippedRouters.add(`${relFile}: ${receiver}.${methodToken}("${pathLiteral[1]}")`);
        }
        continue;
      }
      if (!pathLiteral) {
        fail(`${relFile}: 路由 ${receiver}.${methodToken} 的首参不是字符串字面量，无法静态解析路径：${args[0].text}`);
        continue;
      }
      const handlerArg = args[args.length - 1];
      const handlerExpr = handlerArg.text;
      const method = methodToken === 'All' ? 'ALL' : methodToken.toUpperCase();
      const fullPath = base + pathLiteral[1];

      let codes;
      let handlerLabel;
      if (/^func\s*\(/.test(handlerExpr)) {
        const body = braceBody(masked, handlerArg.start);
        if (!body) {
          fail(`${relFile}: 内联 handler ${method} ${fullPath} 的函数体无法定位`);
          continue;
        }
        codes = collectStatuses(body, null);
        handlerLabel = '<inline closure>';
      } else {
        const name = handlerExpr.split('.').pop();
        const pkg = packageOf(dir);
        let body = null;
        for (const maskedText of pkg.masked) {
          body = methodBody(maskedText, name);
          if (body) break;
        }
        if (!body) {
          for (const maskedText of pkg.masked) {
            const bodies = functionBodies(maskedText);
            if (bodies.has(name)) {
              body = bodies.get(name);
              break;
            }
          }
        }
        if (!body) {
          fail(`${relFile}: 路由 ${method} ${fullPath} 的 handler "${handlerExpr}" 在包内找不到函数体`);
          continue;
        }
        const helperBodies = new Map();
        for (const bodies of pkg.bodies) for (const [k, v] of bodies) helperBodies.set(k, v);
        // guard 守卫在实现里是 *Controller 的方法，functionBodies 只认顶层 func，
        // 所以白名单名称要额外按方法签名补一次（只补白名单，不扩大既有 respond/fail 语义）。
        for (const name of GUARD_HELPERS.keys()) {
          if (helperBodies.has(name)) {
            guardHelperHits.add(name);
            continue;
          }
          for (const maskedText of pkg.masked) {
            const guardBody = methodBody(maskedText, name);
            if (guardBody) {
              helperBodies.set(name, guardBody);
              guardHelperHits.add(name);
              break;
            }
          }
        }
        codes = collectStatuses(body, helperBodies);
        handlerLabel = name;
      }

      // 路由按归一化路径建索引：实现侧是 ":id"，契约侧是 "{id}"，同一段参数统一成 ":param"。
      const key = `${method} ${normalizePath(fullPath)}`;
      const existing = routes.get(key);
      if (existing) {
        for (const code of codes) existing.codes.add(code);
        if (!existing.handlers.includes(handlerLabel)) existing.handlers.push(handlerLabel);
      } else {
        routes.set(key, { method, path: fullPath, codes: new Set(codes), handlers: [handlerLabel], file: relFile });
      }
    }
  }

  if (skippedRouters.size > 0) {
    fail(`以下路由注册的 router 变量解析不出前缀，无法计入映射：\n  ${[...skippedRouters].join('\n  ')}`);
  }
  return routes;
}

/**
 * handler 函数体 + 同包 respond / fail / send / write 前缀辅助函数里的状态码，
 * 以及 GUARD_HELPERS 白名单里的 guard 助手（后者按名字精确匹配，不走前缀）。
 */
function collectStatuses(body, helperBodies) {
  const codes = statusesIn(body);
  if (!helperBodies) return codes;
  const visited = new Set();
  let frontier = [body];
  for (let depth = 0; depth < HELPER_DEPTH && frontier.length > 0; depth++) {
    const next = [];
    for (const current of frontier) {
      for (const call of current.matchAll(/\b([A-Za-z_]\w*)\s*\(/g)) {
        const name = call[1];
        if (visited.has(name)) continue;
        if (!HELPER_PATTERN.test(name) && !GUARD_HELPERS.has(name)) continue;
        const helper = helperBodies.get(name);
        if (!helper) continue;
        visited.add(name);
        next.push(helper);
        for (const code of statusesIn(helper)) codes.add(code);
      }
    }
    frontier = next;
  }
  return codes;
}

// ---------------------------------------------------------------------------
// 比对
// ---------------------------------------------------------------------------

function normalizePath(target) {
  const normalized = target
    .replace(/\{[^}]+\}/g, ':param')
    .replace(/:[A-Za-z0-9_]+/g, ':param')
    .replace(/\/+$/, '');
  return normalized === '' ? '/' : normalized;
}

function operationKey(operation) {
  return `${operation.method} ${normalizePath(operation.path)}`;
}

function compare(contractOperations, routes) {
  const mapped = [];
  const unmapped = [];
  const gaps = [];

  for (const operation of contractOperations) {
    const hit = routes.get(operationKey(operation));
    if (!hit) {
      unmapped.push(operation);
      continue;
    }
    mapped.push({ operation, route: hit });
    const declared = new Set(operation.codes);
    const declaredRanges = new Set(operation.ranges || []);
    const isDeclared = (code) =>
      declared.has(code) || declaredRanges.has('all') || declaredRanges.has(String(Math.floor(code / 100)));
    const produced = [...hit.codes].filter((code) => code >= 200 && code <= 599).sort((a, b) => a - b);
    const lacking = produced.filter((code) => !isDeclared(code));
    if (lacking.length > 0) {
      gaps.push({ operation, route: hit, lacking, declared: operation.codes });
    }
  }

  // 状态码集合为空的路由：实现侧一个码都没扫到，比对退化成 ∅ ⊆ 任意。数量必须可见，且只许降不许升。
  const emptyRouteCount = [...routes.values()].filter((route) => route.codes.size === 0).length;
  const emptyMappedCount = mapped.filter((entry) => entry.route.codes.size === 0).length;
  const contractKeys = new Set();
  for (const operation of contractOperations) {
    const key = operationKey(operation);
    if (contractKeys.has(key)) {
      fail(`契约里两条 operation 归一化后重名，无法唯一比对：${key}（${operation.method} ${operation.path}）`);
    }
    contractKeys.add(key);
  }
  const implementationOnly = [...routes.values()].filter(
    (route) => !contractKeys.has(`${route.method} ${normalizePath(route.path)}`),
  );
  return { mapped, unmapped, gaps, implementationOnly, emptyRouteCount, emptyMappedCount };
}

// ---------------------------------------------------------------------------
// 基线
// ---------------------------------------------------------------------------

function loadBaseline() {
  if (!fs.existsSync(BASELINE_PATH)) {
    fail(`基线文件缺失：${path.relative(ROOT, BASELINE_PATH)}`);
    return { counts: {}, unmapped: [], gaps: [] };
  }
  let raw;
  try {
    raw = JSON.parse(fs.readFileSync(BASELINE_PATH, 'utf8'));
  } catch (error) {
    fail(`基线 JSON 无法解析：${error.message}`);
    return { counts: {}, unmapped: [], gaps: [] };
  }
  const baseline = {
    raw,
    counts: raw['期望计数'] || {},
    unmapped: Array.isArray(raw['不可静态判定']) ? raw['不可静态判定'] : [],
    gaps: Array.isArray(raw['已复核未声明状态码']) ? raw['已复核未声明状态码'] : [],
  };
  for (const entry of baseline.unmapped) {
    if (!entry || typeof entry.operation !== 'string' || !/[\u4e00-\u9fff]/.test(entry['原因'] || '')) {
      fail(`基线「不可静态判定」条目缺少中文原因：${JSON.stringify(entry)}`);
    }
  }
  for (const entry of baseline.gaps) {
    if (
      !entry ||
      typeof entry.operation !== 'string' ||
      !Array.isArray(entry.codes) ||
      entry.codes.length === 0 ||
      !/[\u4e00-\u9fff]/.test(entry['原因'] || '')
    ) {
      fail(`基线「已复核未声明状态码」条目缺少 codes 或中文原因：${JSON.stringify(entry)}`);
    }
  }
  return baseline;
}

function applyBaseline(result, baseline) {
  const baselineUnmapped = new Set(baseline.unmapped.map((entry) => entry.operation));
  const actualUnmapped = new Set(result.unmapped.map((operation) => `${operation.method} ${operation.path}`));

  for (const operation of result.unmapped) {
    const key = `${operation.method} ${operation.path}`;
    if (!baselineUnmapped.has(key)) {
      fail(`未映射的契约 operation 未登记基线：${key}（${operation.source}）`);
    }
  }
  for (const key of baselineUnmapped) {
    if (!actualUnmapped.has(key)) {
      fail(`基线「不可静态判定」已过期：${key} 现在能映射（或契约已删除该 operation），请从基线移除`);
    }
  }

  const baselineGaps = new Map(baseline.gaps.map((entry) => [entry.operation, entry]));
  for (const gap of result.gaps) {
    const key = `${gap.operation.method} ${gap.operation.path}`;
    const recorded = baselineGaps.get(key);
    if (!recorded) {
      fail(
        `实现产出了契约未声明的状态码：${key} → handler=${gap.route.handlers.join('/')} ` +
          `多出=${gap.lacking.join(',')}（契约已声明 ${gap.declared.join(',')}）[${gap.route.file}]`,
      );
      continue;
    }
    const recordedCodes = [...recorded.codes].sort((a, b) => a - b);
    const actualCodes = [...gap.lacking].sort((a, b) => a - b);
    if (recordedCodes.join(',') !== actualCodes.join(',')) {
      fail(
        `基线「已复核未声明状态码」与实测不符：${key} 基线=${recordedCodes.join(',')} 实测=${actualCodes.join(',')} ` +
          `handler=${gap.route.handlers.join('/')}`,
      );
    }
  }
  for (const [key, recorded] of baselineGaps) {
    const stillGap = result.gaps.some((gap) => `${gap.operation.method} ${gap.operation.path}` === key);
    if (!stillGap) {
      fail(
        `基线「已复核未声明状态码」已过期：${key} 现在没有未声明状态码了，请从基线移除（代码 ${recorded.codes.join(',')}）`,
      );
    }
  }
}

// 棘轮计数：实测只能小于等于基线，变大即红（防比对覆盖面悄悄萎缩）。
const RATCHET_COUNTS = new Set(['状态码集合为空的实现侧路由数']);

// ---------------------------------------------------------------------------
// 定向棘轮：公开读 operation 的 401
// ---------------------------------------------------------------------------

/**
 * 12 条公开读 operation 的定向棘轮（清单见 tests/contract-public-read-401.json）。
 *
 * 为什么单列一条：这 12 条的 handler 第一步都是 requireGuestRead，游客读被设成
 * login_required 时匿名读会 401，但它们曾全部漏声明 401。集合层面的 parity 门只保证
 * 「实现产出 ⊆ 契约声明」，对「契约少声明、实现也扫不到」这种双盲区无能为力；这条棘轮
 * 盯的是另一件事：**契约必须显式声明 401，且实现侧必须仍然扫得到 401**。
 * 删除任意一条 '401' 会红，删除清单条目（低于最少条数）也会红。
 */
function checkPublicRead401Ratchet(contractOperations, result) {
  if (!fs.existsSync(PUBLIC_READ_401_MANIFEST)) {
    fail(`定向棘轮清单缺失：${path.relative(ROOT, PUBLIC_READ_401_MANIFEST)}（删掉清单等于关掉这条棘轮）`);
    return 0;
  }
  let manifest;
  try {
    manifest = JSON.parse(fs.readFileSync(PUBLIC_READ_401_MANIFEST, 'utf8'));
  } catch (error) {
    fail(`定向棘轮清单无法解析：${error.message}`);
    return 0;
  }
  const entries = Array.isArray(manifest.operations) ? manifest.operations : [];
  const minimum = Number.isInteger(manifest['最少条数']) ? manifest['最少条数'] : 0;
  if (minimum <= 0) {
    fail('定向棘轮清单缺少正整数「最少条数」：没有下限，删条目就能绕过棘轮');
  }
  if (entries.length < minimum) {
    fail(`定向棘轮清单条目数 ${entries.length} 低于「最少条数」${minimum}：删条目不能代替补 401`);
  }

  const byNormalizedKey = new Map();
  for (const operation of contractOperations) byNormalizedKey.set(operationKey(operation), operation);
  const routeByNormalizedKey = new Map();
  for (const entry of result.mapped) routeByNormalizedKey.set(operationKey(entry.operation), entry.route);

  const seen = new Set();
  let checked = 0;
  for (const entry of entries) {
    const key = typeof entry?.operation === 'string' ? entry.operation : '(缺少 operation)';
    if (!/[\u4e00-\u9fff]/.test(entry?.['原因'] || '')) {
      fail(`定向棘轮清单条目缺少中文原因：${key}`);
    }
    if (seen.has(key)) {
      fail(`定向棘轮清单里重复登记同一条 operation：${key}`);
      continue;
    }
    seen.add(key);
    const [, method = '', ...rest] = /^([A-Z]+)\s+(.+)$/.exec(key) || [];
    if (!method || rest.length === 0) {
      fail(`定向棘轮清单条目不是「METHOD /path」形状：${key}`);
      continue;
    }
    const normalizedKey = `${method} ${normalizePath(rest.join(' '))}`;
    const operation = byNormalizedKey.get(normalizedKey);
    if (!operation) {
      fail(`定向棘轮清单条目在契约里找不到：${key}（operation 改名/删除后必须同步清单，不允许清单烂掉）`);
      continue;
    }
    if (!operation.codes.includes(401)) {
      fail(
        `定向棘轮：${key} 未声明 401。该 handler 第一步是 requireGuestRead，` +
          `forum.guest.read=login_required 时匿名读会被拒（当前已声明 ${operation.codes.join(',')}）`,
      );
      continue;
    }
    const route = routeByNormalizedKey.get(normalizedKey);
    if (!route) {
      fail(`定向棘轮：${key} 契约声明了 401，但实现侧映射不到路由，无法证明守卫还在`);
      continue;
    }
    if (!route.codes.has(401)) {
      fail(
        `定向棘轮：${key} 契约声明了 401，但实现侧 handler=${route.handlers.join('/')} ` +
          `扫不到 401（requireGuestRead 是否还在 guard 白名单/调用链上？）[${route.file}]`,
      );
      continue;
    }
    checked += 1;
  }
  return checked;
}

function checkCounts(result, baseline, contractOperations, refresh) {
  const actual = {
    契约operation数: contractOperations.length,
    已映射operation数: result.mapped.length,
    未映射operation数: result.unmapped.length,
    已复核未声明状态码条数: result.gaps.length,
    状态码集合为空的实现侧路由数: result.emptyRouteCount,
  };
  for (const [name, value] of Object.entries(actual)) {
    const expected = baseline.counts[name];
    const missing = expected === undefined;
    const message = `基线计数「${name}」=${missing ? '(缺失)' : expected}，实测=${value}`;
    if (RATCHET_COUNTS.has(name)) {
      // 棘轮键缺失不是「没配」，而是防护被关掉：它必须显式存在才算数，且不接受 --refresh 自动补。
      if (missing) {
        fail(
          `基线棘轮键「${name}」缺失 = 防护被关掉（空集路由数从此没有上限）。` +
            `请手写回基线 "期望计数" 里（当前实测 ${value}），本门不会用 --refresh 替你补，避免静默失效`,
        );
        continue;
      }
      if (value > expected) {
        fail(`${message}；棘轮只许降不许升——空集路由越多，状态码比对越是退化成 ∅ ⊆ 任意`);
        continue;
      }
      if (refresh || value < expected) console.log(`[parity] 待刷新（棘轮）：${message}`);
      if (!refresh && value < expected) {
        console.log(`[parity] 提示：「${name}」从 ${expected} 收紧到 ${value}，可跑 --refresh 落盘`);
      }
      continue;
    }
    if (!missing && expected === value) continue;
    // --refresh 只负责把计数对齐实测；例外集合（不可静态判定 / 已复核未声明状态码）仍然逐条校验。
    if (refresh) console.log(`[parity] 待刷新：${message}`);
    else fail(`${message}；确认无回归后跑 --refresh 刷新计数`);
  }
  return actual;
}

function refreshCounts(actual, baseline) {
  const next = {
    ...baseline.raw,
    期望计数: actual,
  };
  fs.writeFileSync(BASELINE_PATH, `${JSON.stringify(next, null, 2)}\n`);
}

// ---------------------------------------------------------------------------
// 主流程
// ---------------------------------------------------------------------------

function main() {
  const started = process.hrtime.bigint();
  const refresh = process.argv.includes('--refresh');
  // 诊断开关，默认关闭：只把「状态码集合为空」的路由逐条打印出来，供
  // tests/validate-contract-reachability-equivalence.test.mjs 与可达性门做集合比对。
  // 它不参与任何判定，也不改变无参数运行时的输出（等价性测试里有一条断言钉住这点）。
  const printEmptyRoutes = process.argv.includes('--print-empty-routes');

  let contractOperations;
  let contractPathCount = 0;
  try {
    const parsedContract = readContractOperations();
    contractOperations = parsedContract.operations;
    contractPathCount = parsedContract.pathCount;
  } catch (error) {
    console.error(`[parity] 契约解析失败：${error.message}`);
    process.exitCode = 1;
    return;
  }

  const routes = readCoreRoutes();
  if (routes.size === 0) {
    console.error('[parity] 实现侧一条路由都没解析出来，扫描链路已失效（不是绿灯）');
    process.exitCode = 1;
    return;
  }
  const result = compare(contractOperations, routes);
  const baseline = loadBaseline();
  applyBaseline(result, baseline);
  const actualCounts = checkCounts(result, baseline, contractOperations, refresh);
  const publicReadChecked = checkPublicRead401Ratchet(contractOperations, result);

  if (unknownConstants.size > 0) {
    fail(`出现脚本状态码常量表外的写法，需补表后重跑：${[...unknownConstants].join(', ')}`);
  }
  // 白名单过期同样是红的：名字对不上时守卫状态码会悄悄消失，空集路由数才会又涨回去。
  for (const name of GUARD_HELPERS.keys()) {
    if (!guardHelperHits.has(name)) {
      fail(
        `guard 白名单条目过期：实现侧找不到 "${name}" 的函数体（改名/删除后必须同步 GUARD_HELPERS，` +
          '否则白名单静默失效、空集路由数会悄悄回升）',
      );
    }
  }

  const seconds = Number(process.hrtime.bigint() - started) / 1e9;
  const rate = contractOperations.length === 0 ? 0 : (result.mapped.length / contractOperations.length) * 100;

  console.log('[parity] 契约状态码 parity 门（实现产出 ⊆ 契约声明）');
  console.log(`[parity] 契约 operation 数：${contractOperations.length}（path item ${contractPathCount} 个）`);
  console.log(`[parity] 成功映射到 handler 的 operation 数：${result.mapped.length}（映射率 ${rate.toFixed(1)}%）`);
  console.log(`[parity] 未映射 operation 数：${result.unmapped.length}`);
  console.log(`[parity] 实现侧路由数：${routes.size}（其中契约里没有对应 operation 的 ${result.implementationOnly.length} 条）`);
  console.log(
    `[parity] 状态码集合为空的实现侧路由数：${result.emptyRouteCount}/${routes.size}` +
      `（其中已映射 operation ${result.emptyMappedCount} 条；这类路由的比对退化成 ∅ ⊆ 任意，计数是只降不升的棘轮）`,
  );
  console.log(
    `[parity] guard 助手白名单：${[...GUARD_HELPERS.keys()].join(', ')}` +
      `（命中 ${guardHelperHits.size}/${GUARD_HELPERS.size}）`,
  );
  if (printEmptyRoutes) {
    console.log('[parity-empty-routes]');
    for (const route of routes.values()) {
      if (route.codes.size > 0) continue;
      console.log(`${route.method} ${normalizePath(route.path)}`);
    }
  }
  console.log(
    `[parity] 定向棘轮：公开读 401 已核对 ${publicReadChecked} 条（清单 tests/contract-public-read-401.json）`,
  );

  if (result.unmapped.length > 0) {
    console.log('[parity] 未映射清单（均须在基线的「不可静态判定」里）：');
    const baselineSet = new Set(baseline.unmapped.map((entry) => entry.operation));
    for (const operation of result.unmapped) {
      const key = `${operation.method} ${operation.path}`;
      const mark = baselineSet.has(key) ? '已登记' : '未登记';
      console.log(`  - ${key} [${operation.source}] ${mark}`);
    }
  }

  const baselinedGaps = result.gaps.length;
  console.log(`[parity] 实现多出、契约未声明的状态码：${baselinedGaps} 条（须逐条登记基线）`);
  for (const gap of result.gaps) {
    console.log(
      `  - ${gap.operation.method} ${gap.operation.path} handler=${gap.route.handlers.join('/')} ` +
        `多出=${gap.lacking.join(',')} 已声明=${gap.declared.join(',')} [${gap.route.file}]`,
    );
  }

  if (result.implementationOnly.length > 0) {
    console.log('[parity] 仅实现侧存在、契约未收录的路由（不参与状态码比对）：');
    for (const route of result.implementationOnly) {
      console.log(`  - ${route.method} ${route.path} handler=${route.handlers.join('/')} [${route.file}]`);
    }
  }

  if (failures.length > 0) {
    console.error(`\n[parity] 失败 ${failures.length} 项：`);
    for (const message of failures) console.error(`  ✗ ${message}`);
    if (refresh) {
      console.error('[parity] 本次运行有失败项，拒绝写基线：先把问题修掉，红着的计数不能被顺手刷成新基线');
    }
    console.error(`[parity] 耗时 ${seconds.toFixed(2)}s`);
    process.exitCode = 1;
    return;
  }

  // 只有全绿才允许落盘计数：失败态绝不能被 --refresh 固化。
  if (refresh) {
    refreshCounts(actualCounts, baseline);
    console.log(`[parity] 已刷新计数：${JSON.stringify(actualCounts)}`);
  }

  console.log(`[parity] 通过：0 个未登记例外，耗时 ${seconds.toFixed(2)}s`);
}

main();
