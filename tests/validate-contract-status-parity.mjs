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
 *   在此之上还有一层**调用边识别**（tests/helpers/contract-parity/status-sources.mjs）：
 *   handler 常常只有一行委派（`return h.topicAction(c, ...)`），状态码在下游同包函数里；
 *   身份守卫的状态码又在跟包 `apphttp.*` 里（`apphttp.RequireActor` → 401/503）。
 *   门沿这两类边收集，并把「构造 readPathDependencyFailure」识别为 503 出口。
 *   **错误映射器 `mapXxxError` 族被识别，并按 handler 收窄归因**（#237，见下方 C）：
 *   它的分支按 err sentinel 选，一个具体 handler 只命中其中几条，**全量并入就是过近似**
 *   （会让门为假缺陷变红）—— 所以只沿「handler 调用的领域方法直接 return 的哨兵」归因，
 *   归因不到的记盲区。`status-sources.mjs` 本身仍只负责收集直出码，归因走独立模块
 *   `mapper-attribution.mjs`，两条口径互不污染。
 *
 * 两类例外必须逐条写进 tests/contract-status-parity-baseline.json 并带中文原因：
 * 1. 不可静态判定：通配/插件代理路由，契约 operation 无法静态落到某个 handler；
 * 2. 已复核未声明状态码：实现确实会产出、契约没声明，人工判定后接受的条目
 *    （审计遗留来不及补契约，或静态扫描无法按方法/分支拆分）。
 * 没有登记在基线里的未映射 operation 或未声明状态码一律变红，不会静默放行。
 *
 * 在此之上的四条自证（#235 / #237）：
 *
 *   A. 登记值自证（#235）：`RESPONSE_HELPERS` 里 `codes` 型条目的登记值必须等于该助手
 *      函数体里真实写出的状态码（checkResponseHelperCodes）。`NoData` 实现写 200、登记表
 *      写 204 的那段历史就是这样被钉住的：登记表是门的权威口径，值与实现脱钩时，门会把
 *      契约里错误的 204 认证成「一致」。
 *   B. 反向检查（#237）：契约声明的码必须真被实现产出。此前门只有「实现 ⊆ 契约」一个方向，
 *      「契约声明了实现永不产出的码」完全看不见（只有 401 定向棘轮一个特例）。
 *   C. 映射器收窄归因（#237）：业务码（403/404/409/422…）只从 `mapXxxError` 族产生，
 *      而门原本**识别但不归因**。现在按「handler 调用的 service 方法直接 return 的哨兵」
 *      收窄归因（mapper-attribution.mjs），归因不到的记盲区。**不是**把映射器全量并入
 *      handler —— 那会让门为假缺陷变红，#237 明确禁止，边界由 narrow fixture 钉住。
 *   D. 缺口直方图 + 完全相等计数（#237）：反向缺口按状态码聚合，每码只许下降；
 *      「实现码集合 == 契约码集合」的 operation 数只升不降（起点 119/433，见基线）。
 *
 * 用法：
 *   node tests/validate-contract-status-parity.mjs            # 跑门
 *   node tests/validate-contract-status-parity.mjs --refresh  # 只刷新基线里的「期望计数」
 *   node tests/validate-contract-status-parity.mjs --print-empty-routes
 *                                                             # 只额外打印「状态码集合为空」的路由清单
 *
 * --refresh 只重写计数（含「缺失码直方图」的数字），绝不增删「不可静态判定」「已复核未声明
 * 状态码」条目：例外集合只能人工确认；红着的运行也拒绝落盘。
 */

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  SOURCE_DEPTH,
  collectReachableStatuses,
  createPackageIndexFactory,
} from './helpers/contract-parity/status-sources.mjs';
import { createMapperAttribution } from './helpers/contract-parity/mapper-attribution.mjs';

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const ARGV = process.argv.slice(2);
const ROOT_INDEX = ARGV.indexOf('--root');
// 默认扫仓库自身；--root 供反例测试指向合成树（与可达性门同一套约定）。
const ROOT = ROOT_INDEX >= 0 && ARGV[ROOT_INDEX + 1] ? path.resolve(ARGV[ROOT_INDEX + 1]) : path.resolve(SCRIPT_DIR, '..');
const CONTRACT_ENTRY = path.join(ROOT, 'contracts', 'openapi.yaml');
const CONTRACT_ROOT = path.join(ROOT, 'contracts');
const API_ROOT = path.join(ROOT, 'apps', 'api');
// 基线随 --root 走：仓库里就是 tests/ 自身，fixture 树里则是那棵树的 tests/。
const BASELINE_PATH = path.join(ROOT, 'tests', 'contract-status-parity-baseline.json');
const PUBLIC_READ_401_MANIFEST = path.join(ROOT, 'tests', 'contract-public-read-401.json');

const SERVER_PREFIX = '/api/v1'; // 与 apps/api/app/Http/server.go 的 app.Group("/api/v1") 对应
const HTTP_METHODS = ['get', 'post', 'put', 'patch', 'delete', 'head', 'options', 'trace'];
const FIBER_METHODS = ['Get', 'Post', 'Put', 'Patch', 'Delete', 'Head', 'Options', 'All'];
const HELPER_DEPTH = 2; // 辅助函数最多跟随两层，够覆盖助手链，避免无限递归

/**
 * 显式响应助手白名单（与 GUARD_HELPERS 同构：名字 + 语义 + 产出的状态码）。
 *
 * **不做前缀泛匹配**。历史实现用 /^(respond|fail|send|write)/ 兜住 6 个函数，
 * 前缀泛匹配的问题是「新增一个名字碰巧带前缀的业务函数，就会被当成响应助手，凭空
 * 给路由塞状态码」。这里改成逐条登记，并把原来靠前缀命中的 6 个显式写下来。
 *
 * 三种形态：
 *   - `codes`      固定码（助手自己决定，不看调用点）—— apphttp.OK/Created/NoData
 *   - `statusArg`  码由调用点的第 N 个实参给出（`fiber.StatusXxx` 或数字字面量）
 *   - `follow`     跟随同包函数体收集（原来靠前缀命中的那几个控制器内助手）
 *
 * 未登记即红：app/Http 里凡是「首参是 fiber.Ctx 且函数体产出状态码」的函数都必须
 * 在这张表或 GUARD_HELPERS 里，否则新增助手会静默不被识别、路由状态码集合变空、
 * 比对退化成 ∅ ⊆ 任意（见 checkResponseHelperRegistry）。
 */
const RESPONSE_HELPERS = new Map([
  ['OK', { codes: [200], note: '成功响应：200 + 统一 envelope' }],
  ['Created', { codes: [201], note: '创建成功：201 + 统一 envelope' }],
  // #235 取 b：实现是 `JSON(c, fiber.StatusOK, MessageOK, nil)`，即 200 + 统一 envelope
  // （`data: null`）。登记 204 曾让门与实现各说一套：契约写着 204、实现答 200，
  // 而门因为「登记表对同名函数是权威口径」看不见这个差异。这里如实登记 200，
  // 并由 checkResponseHelperCodes 从函数体反查，登记值再也不能与实现脱钩。
  ['NoData', { codes: [200], note: '无内容：200 + 统一 envelope（`data: null`）' }],
  ['JSON', { statusArg: 1, note: '通用 JSON 响应：码由第 2 个实参给出' }],
  ['ErrorResponse', { statusArg: 1, note: '错误响应：码由第 2 个实参给出' }],
  ['ErrorResponseWithFields', { statusArg: 1, note: '带字段级错误的错误响应' }],
  ['ErrorResponseWithFieldsAndDetails', { statusArg: 1, note: '带字段与细节的错误响应' }],
  ['ErrorResponseWithRetryAt', { statusArg: 1, note: '带 Retry-After 的错误响应' }],
  // 原来靠前缀 /^(respond|fail|send|write)/ 命中的 6 个，改成显式登记后行为不变。
  ['sendAttachmentDelivery', { follow: true, note: '附件直连/流式交付：302（签名地址）或 503（存储不可用）' }],
  ['sendAttachmentContent', { follow: true, note: '附件内容写出：委托 sendAttachmentDelivery' }],
  ['sendFrontendAsset', { follow: true, note: '插件前端制品写出' }],
  ['writeRouteDispatchResponse', { follow: true, note: '路由分发响应写出' }],
  // 不是控制器直接调用，但符合「首参 fiber.Ctx 且产状态码」的形状，必须登记。
  ['storageUnavailable', { codes: [503], note: '依赖故障统一出口：503 + Retry-After（全局 error handler 用）' }],
  // 刻意不收的两类（多收也是错的，见 checkResponseHelperRegistry）：
  //   - Abort/AbortIf/AbortUnless/NewError*：首参是 status int，不是 fiber.Ctx，属 *APIError
  //     构造器。它们的码在调用点以 `fiber.StatusXxx` 常量出现，statusesIn 已经看得到；
  //     目前 34 个调用点全部是常量实参。**若将来出现 `NewError(422, ...)` 这类数字字面量
  //     实参**，就必须把它们加回来，并同时放宽 checkResponseHelperRegistry 的形状规则。
  //   - writeRevisionEvent/writeCommentRevisionEvent（首参 *bufio.Writer）、
  //     validRouteWebSocketPreflight（返回 bool 的谓词）：都不写 HTTP 响应，不该占白名单。
  ['serveRouteStream', { follow: true, note: '插件路由流式响应' }],
  ['serveRouteWebSocket', { follow: true, note: '插件路由 WebSocket 升级' }],
]);

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
// 调用边识别命中面（见 tests/helpers/contract-parity/status-sources.mjs）：
//   - mapperNames     命中但**刻意不归因**的错误映射器名，打印规模 + 反向自检；
//   - appHttpSourceHits 真正被跟随的跟包 app/Http 函数名，打印规模 + 反向自检。
const mapperNames = new Set();
const appHttpSourceHits = new Set();
// 因实参字面量被剪掉的不可达分支（`decodeBookmarkBody(c, false)` 之类），打印规模即可。
const prunedBranchHits = new Set();
// 映射器收窄归因的索引（在 readCoreRoutes 里建，main 打印统计面用）。
let attributionIndex = null;

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
  // 映射器业务码的收窄归因（#237）：索引只建一次，逐 handler 复用。
  const attribution = createMapperAttribution({ apiRoot: API_ROOT });
  attributionIndex = attribution;
  // 调用边识别的包索引（同包委派 + 跟包 apphttp.*）。与上面 packageOf 的缓存分开：
  // 那个只服务白名单形态，这个服务 F1/F2/F3 三条形态，两者职责不同不要合并。
  const sourceIndexOf = createPackageIndexFactory();
  const appHttpDir = path.join(API_ROOT, 'app', 'Http');
  // 白名单形态与调用边形态共用一次遍历：登记表在**任何**被访问到的函数体里都是权威
  // （否则 `return h.action(c)` 这类委派 handler 拿不到下游 `apphttp.OK` 的 200）。
  const registryLookup = (name, body, callIndex) => {
    const entry = RESPONSE_HELPERS.get(name);
    if (entry) {
      if (entry.codes) return { codes: entry.codes };
      if (entry.statusArg !== undefined) return { codes: helperCodesAt(entry, body, callIndex) || [] };
      return { follow: true };
    }
    if (GUARD_HELPERS.has(name)) return { follow: true };
    return null;
  };

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

      let collected;
      let handlerLabel;
      // 助手函数表按**注册文件所在包**取，内联闭包与具名 handler 共用一份：
      // 内联闭包同样会调用 apphttp.OK / JSON 这类助手（/health、/ready 就是），
      // 早先给闭包传 null 等于对它们关掉助手识别，白留两个退化区条目。
      const helperBodies = new Map();
      for (const bodies of packageOf(dir).bodies) for (const [k, v] of bodies) helperBodies.set(k, v);
      if (/^func\s*\(/.test(handlerExpr)) {
        const body = braceBody(masked, handlerArg.start);
        if (!body) {
          fail(`${relFile}: 内联 handler ${method} ${fullPath} 的函数体无法定位`);
          continue;
        }
        collected = collectStatuses(body, helperBodies, {
          dir,
          // 内联闭包没有具名 receiver：只走裸调用与 apphttp.*，不猜 receiver 名。
          receiverName: null,
          indexOf: sourceIndexOf,
          appHttpDir,
          registry: registryLookup,
          attribute: (nodeBody, nodeDir) => attribution.codesFor(nodeBody, nodeDir),
        });
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
        collected = collectStatuses(body, helperBodies, {
          dir,
          // `h.hideTopic` 的 `h` 就是 receiver 变量名：带上它，F1 才能把
          // `h.topicAction(...)` 解析成本包方法，而不是去猜同名方法的并集。
          receiverName: handlerExpr.includes('.') ? handlerExpr.split('.').slice(0, -1).pop() : null,
          indexOf: sourceIndexOf,
          appHttpDir,
          registry: registryLookup,
          attribute: (nodeBody, nodeDir) => attribution.codesFor(nodeBody, nodeDir),
        });
        handlerLabel = name;
      }

      // 路由按归一化路径建索引：实现侧是 ":id"，契约侧是 "{id}"，同一段参数统一成 ":param"。
      const key = `${method} ${normalizePath(fullPath)}`;
      const existing = routes.get(key);
      if (existing) {
        for (const code of collected.codes) existing.codes.add(code);
        for (const code of collected.attributed) existing.attributed.add(code);
        if (!existing.handlers.includes(handlerLabel)) existing.handlers.push(handlerLabel);
      } else {
        routes.set(key, {
          method,
          path: fullPath,
          codes: new Set(collected.codes),
          // 收窄归因出来的映射器业务码单独存一份：它参与比对，但要能单独打印规模，
          // 否则「归因到底生效没有」在输出里看不出来。
          attributed: new Set(collected.attributed),
          handlers: [handlerLabel],
          file: relFile,
        });
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
/**
 * 取一次调用能确定的状态码：
 *   - `codes`     直接用固定码；
 *   - `statusArg` 从该调用的第 N 个实参里取 `fiber.StatusXxx` 或 3 位数字；
 *   - `follow`    交给调用方跟随函数体（返回 null）。
 * 参数取不到（变量/表达式）时返回空数组 —— 静态上判不出来就不猜。
 */
function helperCodesAt(entry, body, callIndex) {
  if (entry.codes) return entry.codes;
  if (entry.statusArg === undefined) return null;
  const openIndex = body.indexOf('(', callIndex);
  if (openIndex < 0) return [];
  const { args } = splitTopLevelArgs(body, openIndex);
  const arg = args[entry.statusArg];
  if (!arg) return [];
  const codes = [];
  for (const m of arg.text.matchAll(/\b(Status[A-Za-z]+)\b/g)) {
    const value = STATUS_CONSTANTS[m[1]];
    if (value !== undefined) codes.push(value);
    else unknownConstants.add(m[1]);
  }
  for (const m of arg.text.matchAll(/(?<![A-Za-z0-9_.])(\d{3})(?![A-Za-z0-9_])/g)) codes.push(Number(m[1]));
  return codes;
}

/**
 * handler 的状态码集合 = 白名单形态（上表）+ 调用边识别（F1 同包委派 / F2 跟包 apphttp.*
 * / F3 依赖故障 503 出口）。
 *
 * 两条来源**并列相加**而不是互相替代：白名单形态按名字精确认固定码（`apphttp.OK` → 200
 * 不看函数体），调用边形态按真实调用链认委派与守卫。任一条单独都不够 —— 只有白名单时
 * 一行委派的 handler 会得到空集（G3 的 30 条退化区就是这么来的）。
 *
 * `reach` 缺省时只跑白名单形态（fixture 里没有包索引上下文时用）。
 */
function collectStatuses(body, helperBodies, reach) {
  const codes = statusesIn(body);
  const attributed = new Set();
  if (reach) {
    const closure = collectReachableStatuses({
      body,
      dir: reach.dir,
      receiverName: reach.receiverName,
      indexOf: reach.indexOf,
      appHttpDir: reach.appHttpDir,
      unknownConstants,
      // 白名单形态对同名函数是权威口径：`apphttp.OK`/`NoData`/`JSON` 的码由登记表
      // 按名字给出，调用边形态不再重复跟随，避免同一函数出现两套口径。
      registry: reach.registry,
      // 映射器业务码的**按 handler 收窄归因**（#237）：只有「handler 调用的 service
      // 方法直接 return 的哨兵」才折算成状态码，映射器全量并集绝不并入。
      attribute: reach.attribute,
      depth: SOURCE_DEPTH,
    });
    for (const code of closure.codes) codes.add(code);
    for (const code of closure.attributed) attributed.add(code);
    for (const name of closure.mappers) mapperNames.add(name);
    for (const name of closure.appHttpHits) appHttpSourceHits.add(name);
    for (const name of closure.prunedBranches) prunedBranchHits.add(name);
  }
  if (!helperBodies) return { codes, attributed };
  const visited = new Set();
  let frontier = [body];
  for (let depth = 0; depth < HELPER_DEPTH && frontier.length > 0; depth++) {
    const next = [];
    for (const current of frontier) {
      for (const call of current.matchAll(/\b([A-Za-z_]\w*)\s*\(/g)) {
        const name = call[1];
        if (visited.has(name)) continue;
        const entry = RESPONSE_HELPERS.get(name);
        if (!entry && !GUARD_HELPERS.has(name)) continue;
        if (entry) {
          const fixed = helperCodesAt(entry, current, call.index);
          if (fixed !== null) {
            visited.add(name);
            for (const code of fixed) codes.add(code);
            continue;
          }
        }
        const helper = helperBodies.get(name);
        if (!helper) continue;
        visited.add(name);
        next.push(helper);
        for (const code of statusesIn(helper)) codes.add(code);
      }
    }
    frontier = next;
  }
  return { codes, attributed };
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
  const reverseGaps = [];
  const reverseSkipped = [];
  let exactOperations = 0;

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
    // produced = 直出码 ∪ 收窄归因到的映射器业务码。归因是「handler 调用的 service
    // 方法直接 return 的哨兵」折算出来的，不是映射器全量并集（见 mapper-attribution.mjs）。
    const producedSet = new Set([...hit.codes, ...(hit.attributed || [])]);
    const produced = [...producedSet].filter((code) => code >= 200 && code <= 599).sort((a, b) => a - b);
    const lacking = produced.filter((code) => !isDeclared(code));
    if (lacking.length > 0) {
      gaps.push({ operation, route: hit, lacking, declared: operation.codes });
    }
    // 反向：契约声明的码必须真被实现产出。契约用了 `default`/`4XX` 这类范围键时
    // 声明面无法枚举，不参与反向比对（记进 reverseSkipped，门打印规模）。
    if (declaredRanges.size > 0) {
      reverseSkipped.push(operation);
      continue;
    }
    if (produced.length === 0) {
      // 空集路由的比对退化成 ∅ ⊆ 任意，反向也没有意义（另有棘轮盯它只降不升）。
      reverseSkipped.push(operation);
      continue;
    }
    const missing = [...declared].filter((code) => !producedSet.has(code)).sort((a, b) => a - b);
    if (missing.length > 0) reverseGaps.push({ operation, route: hit, missing });
    // 「实现码集合 == 契约码集合」的 operation 数：这是 #237 的起点指标（119/434），
    // 只升不降。它比「实现 ⊆ 契约」严格得多 —— 只有两侧都自证过才算数。
    if (missing.length === 0 && lacking.length === 0) exactOperations += 1;
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
  return {
    mapped,
    unmapped,
    gaps,
    reverseGaps,
    reverseSkipped,
    exactOperations,
    implementationOnly,
    emptyRouteCount,
    emptyMappedCount,
  };
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

// 棘轮计数（只降不升）：实测只能小于等于基线，变大即红（防比对覆盖面悄悄萎缩）。
const RATCHET_COUNTS = new Set(['状态码集合为空的实现侧路由数', '契约声明但实现未产出状态码条数']);
// 棘轮计数（只升不降）：实测只能大于等于基线，变小即红（防覆盖面/自证度悄悄退回去）。
const RATCHET_UP_COUNTS = new Set(['契约实现码完全相等operation数']);

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
    // #237 的起点指标：实现码集合与契约码集合**完全相等**的 operation 数（起点 119/434）。
    // 只升不降 —— 补契约声明或扩归因面都会让它涨，反向退化（契约多声明一个永远不产出的码、
    // 或归因面缩水）会立刻让它掉。
    契约实现码完全相等operation数: result.exactOperations,
    // 反向缺口的规模（契约声明、实现扫不到/归因不到的「码 × operation」条数）。只降不升。
    契约声明但实现未产出状态码条数: result.reverseGaps.reduce((sum, gap) => sum + gap.missing.length, 0),
  };
  for (const [name, value] of Object.entries(actual)) {
    const expected = baseline.counts[name];
    const missing = expected === undefined;
    const message = `基线计数「${name}」=${missing ? '(缺失)' : expected}，实测=${value}`;
    if (RATCHET_COUNTS.has(name) || RATCHET_UP_COUNTS.has(name)) {
      const direction = RATCHET_UP_COUNTS.has(name) ? 'up' : 'down';
      // 棘轮键缺失不是「没配」，而是防护被关掉：它必须显式存在才算数，且不接受 --refresh 自动补。
      if (missing) {
        fail(
          `基线棘轮键「${name}」缺失 = 防护被关掉（这个指标从此没有${
            direction === 'up' ? '下限' : '上限'
          }）。` +
            `请手写回基线 "期望计数" 里（当前实测 ${value}），本门不会用 --refresh 替你补，避免静默失效`,
        );
        continue;
      }
      const violated = direction === 'up' ? value < expected : value > expected;
      if (violated) {
        fail(
          `${message}；棘轮只许${direction === 'up' ? '升不许降' : '降不许升'}——${
            direction === 'up'
              ? '自证过的 operation 变少说明契约多声明了实现产不出的码，或归因面缩水了'
              : '缺口变大说明有 operation 的声明码实现再也产出不了（或归因面缩水），门正失去反向证明力'
          }`,
        );
        continue;
      }
      if (refresh || value !== expected) console.log(`[parity] 待刷新（棘轮）：${message}`);
      if (!refresh && direction === 'down' && value < expected) {
        console.log(`[parity] 提示：「${name}」从 ${expected} 收紧到 ${value}，可跑 --refresh 落盘`);
      }
      if (!refresh && direction === 'up' && value > expected) {
        console.log(`[parity] 提示：「${name}」从 ${expected} 提升到 ${value}，可跑 --refresh 落盘`);
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

function refreshCounts(actual, baseline, histogramRows) {
  const next = {
    ...baseline.raw,
    期望计数: actual,
  };
  if (histogramRows) {
    // 只刷数字：键集合由实测给出，绿run 里不会凭空多出新码（新码会先红，红run 拒绝落盘）。
    const histogram = {};
    for (const row of histogramRows) if (row.actual > 0) histogram[String(row.code)] = row.actual;
    next['缺失码直方图'] = histogram;
  }
  fs.writeFileSync(BASELINE_PATH, `${JSON.stringify(next, null, 2)}\n`);
}

/**
 * 反向检查的**缺口直方图**（#237）：契约声明了、实现却扫不到也归因不到的「码 × operation」。
 *
 * 为什么按码聚合而不是逐条登记：缺口起点是三位数（403/404/409/422 为主），逐条登记会让
 * 基线变成一份没人复核的长名单；按码聚合的直方图能一眼看出「这批修掉了哪一类」，
 * 并且天然是棘轮 —— 每个码的计数只许下降，任何一个码涨了就红。
 *
 * 与「已复核未声明状态码」的分工：
 *   - 那张表是**正向**例外（实现多出、契约没声明），逐条人工复核；
 *   - 这张直方图是**反向**缺口（契约多声明、实现产不出），按码计规模，只降不升。
 * 两者都不能靠 --refresh 增删条目：直方图只刷数字，例外表只人工维护。
 */
function checkMissingCodeHistogram(result, baseline, refresh) {
  const histogram = new Map();
  for (const gap of result.reverseGaps) {
    for (const code of gap.missing) histogram.set(code, (histogram.get(code) || 0) + 1);
  }
  const recorded = baseline.raw['缺失码直方图'];
  const recordedMap = recorded && typeof recorded === 'object' ? recorded : {};
  const keys = new Set([...histogram.keys(), ...Object.keys(recordedMap).map(Number)]);
  const rows = [...keys].sort((a, b) => a - b).map((code) => ({
    code,
    actual: histogram.get(code) || 0,
    expected: recordedMap[String(code)],
  }));
  for (const row of rows) {
    const message = `缺失码 ${row.code}：基线 ${row.expected === undefined ? '(未登记)' : row.expected}，实测 ${row.actual}`;
    if (row.expected === undefined) {
      if (row.actual > 0) {
        fail(
          `反向缺口直方图出现未登记的状态码 ${row.code}（实测 ${row.actual} 条）：契约新声明了这个码，` +
            '但实现侧既扫不到直出、也归因不到对应哨兵。请补实现/补归因面，或在基线的「缺失码直方图」里' +
            '人工登记该码并写明原因',
        );
      }
      continue;
    }
    if (row.actual > row.expected) {
      fail(`${message}；直方图只许下降——缺口变大说明这条码的实现出口或归因面缩水了`);
      continue;
    }
    if (refresh || row.actual !== row.expected) console.log(`[parity] 待刷新（直方图）：${message}`);
  }
  return rows;
}

// ---------------------------------------------------------------------------
// 主流程
// ---------------------------------------------------------------------------

/**
 * 响应助手登记表完整性：app/Http 里凡是「首参是 fiber.Ctx 且函数体产出状态码」的函数，
 * 都必须出现在 RESPONSE_HELPERS 或 GUARD_HELPERS 里。
 *
 * 为什么需要这条：白名单一旦漏登记，那个助手的码就**静默消失**，路由状态码集合变空，
 * 比对退化成 ∅ ⊆ 任意 —— 正是第 14 轮 P1 那一类。漏登记必须红，不能靠人记得。
 *
 * 判定只看形状（首参类型 + 函数体里有状态码写法），不看名字前缀。
 */
function checkResponseHelperRegistry() {
  const dir = path.join(API_ROOT, 'app', 'Http');
  if (!fs.existsSync(dir)) return 0;
  const registered = new Set([...RESPONSE_HELPERS.keys(), ...GUARD_HELPERS.keys()]);
  let checked = 0;
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (!entry.isFile() || !entry.name.endsWith('.go') || entry.name.endsWith('_test.go')) continue;
    const file = path.join(dir, entry.name);
    const { masked } = scanGoSource(fs.readFileSync(file, 'utf8'));
    const signature = /^func\s+([A-Za-z_]\w*)\s*\(([^)]*)\)\s*([^{]*)\{/gm;
    let match;
    while ((match = signature.exec(masked))) {
      const name = match[1];
      if (!/^\s*c\s+fiber\.Ctx\b/.test(match[2])) continue;
      // 只看「自己把响应写出去并返回错误」的助手（返回 error / *APIError）。
      // 返回 (Actor, error) 的是身份守卫，归可达性门的 GUARD_REGISTRY 管，不在这里重复登记。
      if (!/^\s*(error|\*APIError)\s*$/.test(match[3])) continue;
      const body = braceBody(masked, match.index + match[0].length - 1); // 正则已吃掉 '{'
      if (!body) continue;
      if (statusesIn(body).size === 0) continue;
      checked += 1;
      if (!registered.has(name)) {
        fail(
          `app/Http 出现未登记的响应助手：${name}（${path.relative(ROOT, file)}）—— 它首参是 fiber.Ctx 且` +
            '自己产出状态码，不登记就会让调用它的路由状态码集合变空。请把它连同语义说明与状态码' +
            '写进 RESPONSE_HELPERS（响应助手）或 GUARD_HELPERS（守卫）',
        );
      }
    }
  }
  return checked;
}

/**
 * 登记值 ↔ 实现自证：`RESPONSE_HELPERS` 里 `codes` 型条目的登记状态码，必须等于该助手
 * 函数体里**真实写出**的状态码。
 *
 * 为什么需要这条：`checkResponseHelperRegistry` 只做「未登记即红」的正向自检，
 * `checkResponseHelperRegistryShape` 只做「多收也红」的形状自检，两条都不看**值**。
 * 于是登记值可以和实现长期脱钩 —— #235 就是这样：`NoData` 实现早就写 200，
 * 登记表还写着 204，门因为「登记表对同名函数是权威口径」看不见差异，
 * 反而把契约里错误的 204 认证成「一致」。
 *
 * 只约束 `codes` 形态：`statusArg` 的码由调用点实参决定、`follow` 的码在跟随里收，
 * 都不该由登记表钉死。函数体缺席不在这里报红（`checkResponseHelperRegistryShape`
 * 已经会报「白名单条目在实现侧找不到同名函数」），避免同一个成因红两条。
 */
function checkResponseHelperCodes() {
  const dir = path.join(API_ROOT, 'app', 'Http');
  if (!fs.existsSync(dir)) return 0;
  const bodies = new Map();
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (!entry.isFile() || !entry.name.endsWith('.go') || entry.name.endsWith('_test.go')) continue;
    const file = path.join(dir, entry.name);
    const { masked } = scanGoSource(fs.readFileSync(file, 'utf8'));
    for (const [name, body] of functionBodies(masked)) {
      if (!bodies.has(name)) bodies.set(name, { body, file: path.relative(ROOT, file) });
    }
  }
  let checked = 0;
  for (const [name, entry] of RESPONSE_HELPERS) {
    if (!entry.codes) continue;
    const hit = bodies.get(name);
    if (!hit) continue;
    const declared = [...entry.codes].sort((a, b) => a - b);
    const actual = [...statusesIn(hit.body)].filter((code) => code >= 200 && code <= 599).sort((a, b) => a - b);
    checked += 1;
    if (actual.join(',') === declared.join(',')) continue;
    fail(
      `[helper-codes] 响应助手登记值与实现不符：${name} 登记=${declared.join(',') || '(空)'} ` +
        `实现=${actual.join(',') || '(空)'}（${hit.file}）。登记表是门的权威口径，值与函数体脱钩时` +
        '门会把契约的错误声明认证成「一致」——请改成实现真实写出的码',
    );
  }
  return checked;
}

/**
 * 响应 header 的**全局存在性**检查（**不是**逐 operation 绑定，名字如实叫 header-global）。
 *
 * 它只回答两个存在性问题：
 *   1. 实现侧有没有「至少一处」在答 503 时设 `Retry-After`（扫 storage_failure.go 全文）；
 *   2. 共享响应组件 `components/responses.yaml#/ServiceUnavailable` 有没有声明该 header。
 *
 * 已知限制（务必如实转述，不要叫「字段级」）：
 *   - `impl` 是整份源码，不是单个 operation —— 任何一处设了 header，全局这一条就过；
 *   - 反向不成立：组件声明了但实现根本不设，这里**不会**红；
 *   - 不比对取值（实现固定 1 秒），也不覆盖 429 之外的其它 header。
 * 复位条件：把 header 名绑到每个 operation 的 503 出口（`headerChecked` 已是抓手），
 * 那时才能升级成真正的逐 operation 字段级比对。
 */
function checkResponseHeaderFields() {
  const implFile = path.join(API_ROOT, 'app', 'Http', 'storage_failure.go');
  const componentFile = path.join(CONTRACT_ROOT, 'openapi', 'components', 'responses.yaml');
  if (!fs.existsSync(implFile) || !fs.existsSync(componentFile)) return 0;
  const impl = fs.readFileSync(implFile, 'utf8');
  const setsRetryAfter = /HeaderRetryAfter|"Retry-After"/.test(impl);
  if (!setsRetryAfter) return 0;
  // 末尾补一个哨兵顶层键：ServiceUnavailable 可能是文件最后一个组件，没有后继键可做边界。
  const component = `${fs.readFileSync(componentFile, 'utf8')}\nZZZEND:\n`;
  const block = /^ServiceUnavailable:\n([\s\S]*?)(?=^[A-Za-z])/m.exec(component);
  if (!block) {
    fail('[header-global] components/responses.yaml 里找不到 ServiceUnavailable 响应组件');
    return 1;
  }
  if (!/^ {2}headers:\s*$/m.test(block[1]) || !/^ {4}Retry-After:\s*$/m.test(block[1])) {
    fail(
      '[header-global] ' +
        '实现侧在答 503 时设置 Retry-After（app/Http/storage_failure.go），' +
        '但 components/responses.yaml#/ServiceUnavailable 没有声明 headers.Retry-After —— ' +
        '客户端拿不到重试间隔，header 声明缺失',
    );
  }
  return 1;
}

/**
 * 白名单**反向**自检：每一条都必须对得上一个「首参 fiber.Ctx」的实现函数。
 *
 * 只约束「未登记即红」是不够的 —— 多收也是错的：把不写响应的函数塞进白名单，会让
 * 它体内的状态码被当成路由产出的码，凭空给路由加码（`writeRevisionEvent` 首参是
 * *bufio.Writer、`validRouteWebSocketPreflight` 是返回 bool 的谓词，都属于这一类）。
 * 所以两个方向都要钉住：漏登记红，多收也红。
 */
function checkResponseHelperRegistryShape() {
  // 这是**仓库级**不变量：白名单描述的是 apps/api/app/Http 的真实助手集合。
  // 合成树（--root 指向 fixture）只带被测的那几个助手，做这条检查只会红一片，
  // 所以 fixture 模式下跳过 —— 真实仓库永远走完整自检（默认 ROOT 就是仓库）。
  if (ROOT !== path.resolve(SCRIPT_DIR, '..')) return RESPONSE_HELPERS.size;
  const signatures = new Map();
  for (const file of listGoFiles(API_ROOT)) {
    const { masked } = scanGoSource(fs.readFileSync(file, 'utf8'));
    const pattern = /^func\s+([A-Za-z_]\w*)\s*\(([^)]*)\)\s*([^{]*)\{/gm;
    let match;
    while ((match = pattern.exec(masked))) {
      const body = braceBody(masked, match.index + match[0].length - 1) || '';
      if (!signatures.has(match[1])) signatures.set(match[1], []);
      signatures.get(match[1]).push({
        file: path.relative(ROOT, file),
        firstParam: match[2],
        returns: match[3].trim(),
        // 「真的把响应写出去」：`.Status(...)` 调用、`Send*`/`Redirect`、或 fiber.Error 字面量。
        // 单纯比较 `fiber.StatusXxx`（谓词）不算 —— validRouteWebSocketPreflight 就是这种。
        writes: /\.Status\(\s*[^)]*\)|\.(?:Send|SendString|SendStream|Redirect)\s*\(|\bStatus:\s*\d{3}\b/.test(body),
      });
    }
  }
  for (const [name, entry] of RESPONSE_HELPERS) {
    const hits = signatures.get(name) || [];
    if (hits.length === 0) {
      fail(
        `[helper-registry] 白名单条目在实现侧找不到同名函数：${name} —— 名字对不上时该助手的码` +
          '静默消失，空集路由数会悄悄回升。请改名或从 RESPONSE_HELPERS 移除',
      );
      continue;
    }
    // 两条形状要求：① 首参是 fiber.Ctx（拿得到响应）；② 要么返回 error/*APIError
    // （把响应交回框架），要么函数体真的写了响应。只满足「首参 Ctx」的谓词不算。
    const isHelper = (hit) =>
      /^\s*c\s+fiber\.Ctx\b/.test(hit.firstParam) &&
      (/^(error|\*APIError)$/.test(hit.returns) || hit.writes);
    if (!hits.some(isHelper)) {
      const where = hits
        .map((hit) => `${hit.file}(首参 ${hit.firstParam.trim()}，返回 ${hit.returns || '空'}，写响应=${hit.writes})`)
        .join(', ');
      fail(
        `[helper-registry] 白名单多收了非响应助手：${name}（${where}）。` +
          '响应助手必须「首参 fiber.Ctx 且自己把响应写出去或把 error 交回框架」；' +
          '谓词与纯 SSE 写入器不该占白名单 —— 它们体内的状态码会被当成路由产出的码',
      );
    }
  }
  return RESPONSE_HELPERS.size;
}

/**
 * 数字字面量实参的常设检查：`apphttp.NewError(422, ...)` / `apphttp.Abort(403, ...)`。
 *
 * 背景：`Abort*` / `NewError*` 是 `*APIError` 构造器（首参是 status int，不是 fiber.Ctx），
 * 所以不在 RESPONSE_HELPERS 里；它们的码靠调用点的 `fiber.StatusXxx` 常量被 statusesIn 看见。
 * 这条不变量**只在常量实参下成立** —— 一旦有人写数字字面量，那个码对 statusesIn 完全不可见，
 * 路由状态码集合会漏一个码而门不会红。
 *
 * 复位条件：出现第一处数字字面量实参时，把对应构造器加回 RESPONSE_HELPERS（statusArg 形态），
 * 并同步放宽 checkResponseHelperRegistryShape 的「首参 fiber.Ctx」形状规则（给它一个
 * 显式的 statusArg 例外标记），然后把这条检查改成「只对未登记的构造器报红」。
 */
function checkNumericStatusArguments() {
  const dir = path.join(API_ROOT, 'app', 'Http');
  if (!fs.existsSync(dir)) return 0;
  const pattern = /apphttp\.(NewError\w*|Abort\w*)\(\s*(\d{3})\b/g;
  let sites = 0;
  for (const file of listGoFiles(dir)) {
    const { masked } = scanGoSource(fs.readFileSync(file, 'utf8'));
    for (const match of masked.matchAll(pattern)) {
      sites += 1;
      fail(
        `[status-arg] ${path.relative(ROOT, file)} 用数字字面量调用了 apphttp.${match[1]}(${match[2]}, ...)：` +
          '数字字面量实参对 statusesIn 不可见，这个状态码不会进入路由的状态码集合，门不会红。' +
          '请改用 fiber.StatusXxx 常量；确实要用数字时，把该构造器加回 RESPONSE_HELPERS 并放宽形状规则',
      );
    }
  }
  return sites;
}

/**
 * 调用边识别面的**反向自检**：这套识别不许静默失效。
 *
 * 为什么必须钉住：三条形态（同包委派 / 跟包 apphttp.* / 依赖故障 503）一旦整体失灵
 * （app/Http 换目录、包索引读不到文件、正则被改坏），门不会报错 —— 它只是**看不见**
 * 那些状态码，空集路由数会悄悄回升。空集棘轮能兜住「回升」，但兜不住「从未看见」的
 * 中间态，所以这里直接断言每条形态都还有命中面。
 *
 * 只在仓库自身生效：合成树（--root）只带被测的那几个函数，做这条检查只会红一片。
 */
function checkCallEdgeRecognition() {
  if (ROOT !== path.resolve(SCRIPT_DIR, '..')) return null;
  const appHttpDir = path.join(API_ROOT, 'app', 'Http');
  if (!fs.existsSync(appHttpDir)) {
    fail(`[call-edge] 跟包识别失效：${path.relative(ROOT, appHttpDir)} 不存在，apphttp.* 的状态码将全部看不见`);
  }
  if (appHttpSourceHits.size === 0) {
    fail(
      '[call-edge] 跟包识别（F2）一个函数都没命中：控制器里明明有 apphttp.RequireActor / ReadActor 这类调用，' +
        '命中面为 0 说明识别链路已断（包目录/别名/正则），不是「刚好没有」',
    );
  }
  if (mapperNames.size === 0) {
    fail(
      '[call-edge] 错误映射器识别（mapXxxError 族）一个都没命中：控制器包里的 mapForumError / mapError 等' +
        '是必经节点，命中面为 0 说明调用边根本没走到它们，识别链路已断',
    );
  }
  return {
    mappers: [...mapperNames].sort(),
    appHttp: [...appHttpSourceHits].sort(),
    pruned: [...prunedBranchHits].sort(),
  };
}

function main() {
  const started = process.hrtime.bigint();
  const refresh = process.argv.includes('--refresh');
  // 诊断开关，默认关闭：只把「状态码集合为空」的路由逐条打印出来，供
  // tests/validate-contract-reachability-equivalence.test.mjs 与可达性门做集合比对。
  // 它不参与任何判定，也不改变无参数运行时的输出（等价性测试里有一条断言钉住这点）。
  const printEmptyRoutes = process.argv.includes('--print-empty-routes');
  // 第二个诊断开关，同样只输出、不判定：把「每条已映射 operation 的实现侧可达状态码集合」
  // 逐条打印出来，供 tests/validate-contract-parity-sample-equality.test.mjs 做集合相等比对。
  const printOperationCodes = process.argv.includes('--print-operation-codes');

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
  const histogramRows = checkMissingCodeHistogram(result, baseline, refresh);
  const publicReadChecked = checkPublicRead401Ratchet(contractOperations, result);

  if (unknownConstants.size > 0) {
    fail(`出现脚本状态码常量表外的写法，需补表后重跑：${[...unknownConstants].join(', ')}`);
  }
  const helperChecked = checkResponseHelperRegistry();
  const helperRegistrySize = checkResponseHelperRegistryShape();
  const helperCodesChecked = checkResponseHelperCodes();
  const headerChecked = checkResponseHeaderFields();
  const numericStatusSites = checkNumericStatusArguments();
  const callEdge = checkCallEdgeRecognition();
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
  console.log(
    `[parity] 响应助手白名单：${helperRegistrySize} 条（双向自检：漏登记红、多收也红）；app/Http 里首参 fiber.Ctx 且产状态码的函数 ${helperChecked} 个，` +
      '未登记即红',
  );
  console.log(
    `[parity] 响应助手登记值自证：codes 型条目 ${helperCodesChecked} 条的登记值必须等于函数体实测状态码（不一致即红，#235）`,
  );
  console.log(
    `[parity] header 全局存在性（header-global，非逐 operation 绑定）：实现侧 503 设置 Retry-After 的出口 ` +
      `${headerChecked} 个；共享组件 ServiceUnavailable 未声明即红`,
  );
  console.log(
    `[parity] 构造器实参形态：apphttp.NewError*/Abort* 的数字字面量调用点 ${numericStatusSites} 个` +
      '（对 statusesIn 不可见，非 0 即红）',
  );
  console.log(
    `[parity] 调用边识别（F1 同包委派 / F2 跟包 apphttp.* / F3 依赖故障 503）：` +
      `跟包函数命中 ${appHttpSourceHits.size} 个、错误映射器识别 ${mapperNames.size} 个` +
      '（识别后按 handler 收窄归因，不全量并入）、' +
      `按实参剪枝 ${prunedBranchHits.size} 处`,
  );
  if (callEdge) {
    console.log(`[parity]   跟包命中：${callEdge.appHttp.join(', ')}`);
    console.log(`[parity]   识别到的错误映射器（收窄归因口径见下一行）：${callEdge.mappers.join(', ')}`);
    if (callEdge.pruned.length > 0) console.log(`[parity]   剪枝：${callEdge.pruned.join('; ')}`);
  }
  if (printOperationCodes) {
    console.log('[parity-operation-codes]');
    for (const entry of result.mapped) {
      const codes = [...entry.route.codes].filter((code) => code >= 200 && code <= 599).sort((a, b) => a - b);
      console.log(`${entry.operation.method} ${normalizePath(entry.operation.path)} => ${codes.join(',')}`);
    }
  }
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
  if (attributionIndex) {
    const stats = attributionIndex.stats();
    console.log(
      `[parity] 映射器收窄归因（#237）：映射器分支 ${stats.branches} 个、哨兵 ${stats.sentinels} 个、` +
        `归因盲区 ${stats.blind.length} 个（盲区只登记不猜，见 tests/helpers/contract-parity/mapper-attribution.mjs）`,
    );
  }
  console.log(
    `[parity] 反向检查（契约声明的码必须真被实现产出）：缺口 ${result.reverseGaps.length} 条 operation / ` +
      `${actualCounts['契约声明但实现未产出状态码条数']} 个「码 × operation」；` +
      `因契约含 default/4XX 范围键或实现侧空集而跳过 ${result.reverseSkipped.length} 条`,
  );
  console.log(
    `[parity] 契约码集合与实现码集合完全相等的 operation：${result.exactOperations}/${result.mapped.length}` +
      '（#237 起点指标，只升不降；分母是已映射 operation 数，不是全部契约 operation）',
  );
  console.log('[parity] 缺失码直方图（契约声明、实现未产出；每批只许下降）：');
  for (const row of histogramRows) {
    console.log(
      `[parity]   ${row.code}: ${row.actual}（基线 ${row.expected === undefined ? '(未登记)' : row.expected}）`,
    );
  }

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
    refreshCounts(actualCounts, baseline, histogramRows);
    console.log(`[parity] 已刷新计数：${JSON.stringify(actualCounts)}`);
    console.log(`[parity] 已刷新缺失码直方图：${JSON.stringify(baseline.raw['缺失码直方图'] || {})}`);
  }

  console.log(`[parity] 通过：0 个未登记例外，耗时 ${seconds.toFixed(2)}s`);
}

main();
