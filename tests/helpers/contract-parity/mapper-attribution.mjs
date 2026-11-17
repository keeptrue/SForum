/**
 * 错误映射器的**按 handler 收窄归因**（#237）。
 *
 * 背景：`mapXxxError` 族（`mapForumError` / `MapOptionsError` / 裸 `mapError` …）是业务码
 * （403/404/409/422 …）的唯一出口，但它们的结构是「`switch` + `errors.Is/As` 选分支」，
 * 一个具体 handler 只会命中其中几条。所以把整个映射器的码并进每个 handler 就是过近似，
 * 门会为「该 handler 不可能触发」的分支变红 —— 这正是 parity 门当初**识别但不归因**的理由。
 *
 * 这里做的是收窄，不是放宽：
 *
 *   handler 体里的 `h.<field>.<Method>(...)`
 *     → 用同包 struct 字段声明把 `<field>` 解析成具体包（`service *forum.Service` → Forum 包）
 *     → 在该包的索引里找 `<Method>` **直接** `return ErrXxx`（同包哨兵，含 `return T{}, ErrXxx`）
 *     → 再经映射器登记表把 `ErrXxx` 翻成状态码。
 *
 * 归因不到的一律不猜：记进 `blind`（门打印盲区计数），状态码不并入 handler 集合。
 *
 * **刻意不做的事**（越界就是假缺陷）：
 *   - 不把映射器全量并入 handler（那就退化成过近似）；
 *   - 不沿 service 方法内部的调用链继续追（`s.store.X` 是持久层，哨兵由领域层再包装，
 *     静态上无法判定某个 handler 能触发哪些）；只认「方法体里直接 return 的哨兵」；
 *   - 不把 `errors.As` 的类型分支当哨兵（那是类型不是值，无法从返回值反推）。
 */

import fs from 'node:fs';
import path from 'node:path';

import { braceBody, listGoFiles, scanGoSource, statusesIn } from './status-sources.mjs';

/** 映射器函数名族，与 status-sources.mjs 的 ERROR_MAPPER_PATTERN 同义。 */
const MAPPER_NAME = /^(?:map|Map)\w*Error$/;

/** 哨兵名形态：Go 约定 `ErrXxx`。映射器里 `errors.Is(err, forum.ErrTopicNotFound)` 即此类。 */
const SENTINEL = /(?:([a-z]\w*)\s*\.\s*)?(Err[A-Za-z]\w*)/g;

/**
 * 从一段函数体里取「直接 return 的哨兵」。
 *
 * 只认 return 语句自身的表达式，不递归进嵌套调用：`return fmt.Errorf("...: %w", ErrFoo)`
 * 这种包装也算（哨兵仍在表达式里），但 `return s.store.Do(ctx)` 不算 —— 下游的哨兵
 * 属于持久层，handler 能否触发它取决于运行期数据，静态上不可判。
 */
function returnedSentinels(body) {
  const out = new Set();
  const re = /\breturn\b/g;
  let match;
  while ((match = re.exec(body))) {
    const start = match.index + match[0].length;
    let depth = 0;
    let i = start;
    for (; i < body.length; i += 1) {
      const ch = body[i];
      if (ch === '(' || ch === '[' || ch === '{') depth += 1;
      else if (ch === ')' || ch === ']' || ch === '}') {
        if (depth === 0) break;
        depth -= 1;
      } else if (depth === 0 && (ch === ';' || ch === '\n')) break;
    }
    const expression = body.slice(start, i);
    for (const sentinel of expression.matchAll(SENTINEL)) out.add(sentinel[2]);
    re.lastIndex = i;
  }
  return out;
}

/**
 * 把一个映射器函数体拆成「分支 → 状态码」。
 *
 * 只处理 Go 代码里真实存在的两种形态：
 *   - `switch { case errors.Is(err, X): ... }`（行首 `case` 切段）；
 *   - `if errors.Is(err, X) { ... }`（按块取）。
 * 段内的状态码用 statusesIn 取（`fiber.StatusXxx` 常量与数字字面量都看得见）。
 */
function mapperBranches(body) {
  const branches = [];
  const caseMarkers = [...body.matchAll(/^[ \t]*(?:case\b[^\n]*|default:)/gm)];
  for (let index = 0; index < caseMarkers.length; index += 1) {
    const from = caseMarkers[index].index;
    const to = index + 1 < caseMarkers.length ? caseMarkers[index + 1].index : body.length;
    branches.push(body.slice(from, to));
  }
  for (const match of body.matchAll(/^[ \t]*if\b[^\n]*errors\.(?:Is|As)\([^\n]*$/gm)) {
    // 行尾通常就是 `{`：从行尾前一个字符开始找花括号，否则会跳到**下一个**块，
    // 把相邻分支的状态码算到这一条条件上（`if ErrPermissionDenied` 曾因此拿到 404）。
    const block = braceBody(body, match.index + match[0].length - 1);
    if (block) branches.push(`${match[0]}\n${block}`);
  }
  if (branches.length === 0) branches.push(body);
  return branches;
}

/** 解析一份 Go 文件的 import 别名表（alias → import path）。 */
function parseImports(masked) {
  const imports = new Map();
  const block = /^import\s*\(\n([\s\S]*?)\n\)/m.exec(masked);
  const addLine = (line) => {
    const m = /^\s*(?:([A-Za-z_]\w*)\s+)?"([^"]+)"\s*$/.exec(line);
    if (!m) return;
    const importPath = m[2];
    const alias = m[1] || importPath.split('/').pop();
    if (alias === '_' || alias === '.') return;
    imports.set(alias, importPath);
  };
  if (block) for (const line of block[1].split('\n')) addLine(line);
  for (const m of masked.matchAll(/^import\s+(?:([A-Za-z_]\w*)\s+)?"([^"]+)"\s*$/gm)) addLine(m[0]);
  return imports;
}

/** 解析一份 Go 文件里的 struct 字段（字段名 → `pkgAlias.TypeName`）。 */
function parseStructFields(masked) {
  const fields = [];
  const pattern = /^type\s+([A-Za-z_]\w*)\s+struct\s*\{/gm;
  let match;
  while ((match = pattern.exec(masked))) {
    const body = braceBody(masked, match.index + match[0].length - 1);
    if (!body) continue;
    for (const line of body.split('\n')) {
      const field = /^\s*([A-Za-z_]\w*)\s+\*?\s*([A-Za-z_]\w*)\s*\.\s*([A-Za-z_]\w*)\s*$/.exec(line);
      if (field) fields.push({ struct: match[1], name: field[1], alias: field[2], typeName: field[3] });
    }
  }
  return fields;
}

export function createMapperAttribution({ apiRoot }) {
  const controllerRoot = path.join(apiRoot, 'app', 'Http');
  const modelsRoot = path.join(apiRoot, 'app', 'Models');

  /** 映射器登记表：哨兵名 → 该哨兵在映射器里被翻成的状态码集合。 */
  const sentinelStatus = new Map();
  /** 领域包方法索引：包目录 → `Type.Method` / 顶层 `func` → 直接 return 的哨兵集合。 */
  const packageIndex = new Map();
  /** 控制器包字段索引：包目录 → 字段名 → 候选 import path 集合。 */
  const fieldIndex = new Map();
  const blind = new Set();
  const passthrough = new Set();
  let mappedBranches = 0;

  const readPackage = (dir) => {
    if (packageIndex.has(dir)) return packageIndex.get(dir);
    const methods = new Map();
    const note = (key, sentinels) => {
      if (!methods.has(key)) methods.set(key, new Set());
      for (const sentinel of sentinels) methods.get(key).add(sentinel);
    };
    for (const file of listGoFiles(dir)) {
      const { masked } = scanGoSource(fs.readFileSync(file, 'utf8'));
      // 方法与顶层函数分开登记：方法键是 `Type.Method`。只按方法名索引会在同一个包内
      // 撞名（`Get`/`List`/`Create` 一堆类型都有），把别的类型的哨兵算到 handler 头上。
      const pattern = /^func\s*(?:\(\s*\w+\s+\*?([A-Za-z_]\w*)\s*\)\s*)?([A-Za-z_]\w*)\s*\(/gm;
      let match;
      while ((match = pattern.exec(masked))) {
        const body = braceBody(masked, match.index + match[0].length);
        if (!body) continue;
        const sentinels = returnedSentinels(body);
        if (sentinels.size === 0) continue;
        note(match[1] ? `${match[1]}.${match[2]}` : match[2], sentinels);
      }
    }
    packageIndex.set(dir, methods);
    return methods;
  };

  const readFieldIndex = (dir) => {
    if (fieldIndex.has(dir)) return fieldIndex.get(dir);
    const fields = new Map();
    for (const file of listGoFiles(dir)) {
      const { stripped, masked } = scanGoSource(fs.readFileSync(file, 'utf8'));
      // import 路径是**字符串字面量**：masked 把它换成空格，必须用 stripped 才读得到。
      const imports = parseImports(stripped);
      for (const field of parseStructFields(masked)) {
        const importPath = imports.get(field.alias);
        if (!importPath) continue;
        if (!fields.has(field.name)) fields.set(field.name, new Map());
        fields.get(field.name).set(importPath, field.typeName);
      }
    }
    fieldIndex.set(dir, fields);
    return fields;
  };

  // 映射器登记表：扫一遍 app/Http 下所有控制器包（映射器散落在各包的 error_mapping.go）。
  for (const file of listGoFiles(controllerRoot)) {
    const { masked } = scanGoSource(fs.readFileSync(file, 'utf8'));
    const pattern = /^func\s+([A-Za-z_]\w*)\s*\(/gm;
    let match;
    while ((match = pattern.exec(masked))) {
      if (!MAPPER_NAME.test(match[1])) continue;
      const body = braceBody(masked, match.index + match[0].length);
      if (!body) continue;
      for (const branch of mapperBranches(body)) {
        const codes = [...statusesIn(branch)].filter((code) => code >= 400 && code <= 599);
        if (codes.length === 0) continue;
        mappedBranches += 1;
        for (const sentinel of branch.matchAll(SENTINEL)) {
          if (!sentinelStatus.has(sentinel[2])) sentinelStatus.set(sentinel[2], new Set());
          for (const code of codes) sentinelStatus.get(sentinel[2]).add(code);
        }
      }
    }
  }

  // 领域包一律挂在 `<module>/app/Models/...` 下：按路径中段识别，不写死 module path，
  // 这样合成树（--root fixture）里的 `example.com/fixture/apps/api/app/Models/...` 同样成立。
  const modelsMarker = '/app/Models/';

  // 状态**透传**型映射器（`mapAuthProviderHTTPError` 这类：上游答什么就原样回写什么）
  // 会让一个哨兵对应一长串状态码。那不是「这个 handler 可能答这些码」，而是「上游答什么
  // 就透什么」——并进 handler 只会造出 10 个假缺陷。超过阈值即整条不归因，记进 passthrough。
  const PASSTHROUGH_LIMIT = 4;
  for (const [sentinel, statuses] of [...sentinelStatus]) {
    if (statuses.size <= PASSTHROUGH_LIMIT) continue;
    sentinelStatus.delete(sentinel);
    passthrough.add(`${sentinel}→${[...statuses].sort((a, b) => a - b).join('/')}`);
  }

  /**
   * 归因一个函数体（handler 或同包委派节点）：返回该体里「能确定触发」的状态码。
   *
   * 解析路径：`<recv>.<field>.<Method>(` → 同包 struct 字段声明 → 领域包 → 方法体直接
   * return 的哨兵 → 映射器登记表。任一步解析不出来就记盲区，不猜。
   */
  const codesFor = (body, dir) => {
    const codes = new Set();
    const fields = readFieldIndex(dir);
    for (const call of body.matchAll(/(?<![\w.])([a-z]\w*)\s*\.\s*([a-z]\w*)\s*\.\s*([A-Za-z_]\w*)\s*\(/g)) {
      const field = call[2];
      const method = call[3];
      const candidates = fields.get(field);
      if (!candidates || candidates.size === 0) continue; // 不是控制器字段，不归因也不记盲区
      let resolved = false;
      for (const [importPath, typeName] of candidates) {
        const marker = importPath.indexOf(modelsMarker);
        if (marker < 0) continue;
        // `.../app/Models/Forum` → `<apiRoot>/app/Models/Forum`（apiRoot 是 apps/api）。
        const pkgDir = path.join(apiRoot, 'app', 'Models', ...importPath.slice(marker + modelsMarker.length).split('/'));
        if (!fs.existsSync(pkgDir)) continue;
        const methods = readPackage(pkgDir);
        // 精确到「字段声明的类型 + 方法名」。接口类型（`identity.ActorStore`）在实现包里
        // 没有方法体，自然归因不到 —— 这正是要的：它的实现由注入决定，静态不可判。
        const sentinels = methods.get(`${typeName}.${method}`);
        if (!sentinels) continue;
        resolved = true;
        for (const sentinel of sentinels) {
          const statuses = sentinelStatus.get(sentinel);
          if (!statuses) {
            blind.add(`${method}→${sentinel}`);
            continue;
          }
          for (const code of statuses) codes.add(code);
        }
      }
      if (!resolved) blind.add(`${field}.${method}`);
    }
    return codes;
  };

  return {
    codesFor,
    /** 诊断面：映射器分支数、登记到的哨兵数、归因盲区（门会打印）。 */
    stats: () => ({
      branches: mappedBranches,
      sentinels: sentinelStatus.size,
      passthrough: [...passthrough].sort(),
      blind: [...blind].sort(),
    }),
  };
}
