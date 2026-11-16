/**
 * Go 源码模型：把 Go 源码切成「去掉注释/字符串的两份等长文本」，并提供函数体、
 * 方法体、状态码、路由参数列表这些**只看形状**的原语。
 *
 * 这一层不关心路由、不关心契约，只回答「这段 Go 文本里的函数长什么样」。
 * 它同时承担「parity 等价」的可见状态码收集（parityVisibleCodes）：那条规则是
 * parity 门的定义，放在这里是因为它同样只依赖 Go 文本形状，不依赖调用图。
 */

import fs from 'node:fs';
import path from 'node:path';

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

// ---------------------------------------------------------------------------
// 文本工具
// ---------------------------------------------------------------------------

export function readLines(file) {
  return fs.readFileSync(file, 'utf8').split(/\r?\n/);
}

export function indentOf(line) {
  const m = /^[ \t]*/.exec(line);
  return m ? m[0].length : 0;
}

export function isBlank(line) {
  return line.trim() === '' || line.trim().startsWith('#');
}

/**
 * 一次扫描产出两份与源码等长的文本，索引可直接互换：
 * - stripped：保留字符串字面量，注释换成空格（读路由注册里的路径字符串）；
 * - masked：字符串与注释都换成空格（花括号配对、调用点提取，避免误命中）。
 */
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

/** 顶层函数：`func Name(` —— 返回 name → body。 */
export function functionBodies(masked) {
  const bodies = new Map();
  const pattern = /^func\s+([A-Za-z_]\w*)\s*\(/gm;
  let match;
  while ((match = pattern.exec(masked))) {
    const body = braceBody(masked, match.index + match[0].length);
    if (body) bodies.set(match[1], body);
  }
  return bodies;
}

/**
 * 方法：`func (recv T) Name(` —— 返回 name → body[]。
 *
 * 同名方法可能挂在多个类型上。调用图先按「receiver 变量名 + receiver 类型」精确解析
 * （控制器里的 `h.foo` 绝大多数属于同一类型），只有在解析不出时才退回并集过近似；
 * `stats.ambiguousMethods` 会列出所有同名多类型的方法名，供人工核对是否影响结论。
 */
export function methodBodies(masked) {
  const bodies = new Map();
  const pattern = /^func\s*\(\s*([A-Za-z_]\w*)\s+\*?\s*([A-Za-z_]\w*)\s*\)\s*([A-Za-z_]\w*)\s*\(/gm;
  let match;
  while ((match = pattern.exec(masked))) {
    const body = braceBody(masked, match.index + match[0].length);
    if (!body) continue;
    const name = match[3];
    if (!bodies.has(name)) bodies.set(name, []);
    bodies.get(name).push({ receiverName: match[1], receiverType: match[2], body });
  }
  return bodies;
}

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

export function splitTopLevelArgs(stripped, openIndex) {
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
      if (depth === 0) break;
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

export function normalizePath(target) {
  const normalized = target
    .replace(/\{[^}]+\}/g, ':param')
    .replace(/:[A-Za-z0-9_]+/g, ':param')
    .replace(/\/+$/, '');
  return normalized === '' ? '/' : normalized;
}

// parity 等价：响应辅助函数的前缀规则与 guard 白名单（见 parity 门的 HELPER_PATTERN /
// GUARD_HELPERS）。这两个常量只用于**复现 parity 门的可见集合**，好让退化区计数对得上；
// 本门自己的 guard 判定走 GUARD_REGISTRY，不看前缀。
const PARITY_HELPER_PATTERN = /^(respond|fail|send|write)/i;
export const PARITY_GUARD_HELPERS = new Set(['requireGuestRead', 'requireAdmin']);
const PARITY_HELPER_DEPTH = 2;

/**
 * 复现 parity 门的可见状态码集合：handler 自身 + 只跟随 respond* / fail* / send* / write*
 * 前缀的辅助函数（深度 2）+ GUARD_HELPERS 白名单（requireGuestRead / requireAdmin）。
 *
 * 这条集合为空，就是 parity 门的退化区：比对退化成 ∅ ⊆ 任意。本门把这条规则原样
 * 复刻一遍，只为了**量出**退化区的成员集合，不参与本门自己的 guard 判定。
 */
export function parityVisibleCodes(handlerBody, helperBodies, unknownConstants) {
  const codes = statusesIn(handlerBody, unknownConstants);
  const visited = new Set();
  let frontier = [handlerBody];
  for (let depth = 0; depth < PARITY_HELPER_DEPTH && frontier.length > 0; depth++) {
    const next = [];
    for (const current of frontier) {
      for (const call of current.matchAll(/\b([A-Za-z_]\w*)\s*\(/g)) {
        const name = call[1];
        if (visited.has(name)) continue;
        if (!PARITY_HELPER_PATTERN.test(name) && !PARITY_GUARD_HELPERS.has(name)) continue;
        const helper = helperBodies.get(name);
        if (!helper) continue;
        visited.add(name);
        next.push(helper);
        for (const code of statusesIn(helper, unknownConstants)) codes.add(code);
      }
    }
    frontier = next;
  }
  return codes;
}
