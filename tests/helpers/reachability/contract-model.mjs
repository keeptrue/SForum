/**
 * 契约模型：把 contracts/openapi.yaml 的 paths 索引展开成「method + 路径 + 状态码集合」
 * 的扁平列表。解析规则与 parity 门刻意等价（同样的 `$ref` 索引、同样的 responses 键
 * 解析、`default` / `2XX` 范围键按整段放行），否则两边的 operation 数对不上。
 */

import fs from 'node:fs';
import path from 'node:path';

import { indentOf, isBlank, readLines } from './go-source.mjs';

// 实现侧路由的统一前缀，与 apps/api/app/Http/server.go 的 app.Group("/api/v1") 对应。
export const SERVER_PREFIX = '/api/v1';

// OpenAPI 里允许出现的 HTTP method 键（小写）。
const HTTP_METHODS = ['get', 'post', 'put', 'patch', 'delete', 'head', 'options', 'trace'];

// ---------------------------------------------------------------------------
// 契约侧（parity 等价）
// ---------------------------------------------------------------------------

export function readContractOperations(contractRoot) {
  const entryFile = path.join(contractRoot, 'openapi.yaml');
  const lines = readLines(entryFile);
  const start = lines.findIndex((line) => /^paths:\s*$/.test(line));
  if (start < 0) throw new Error(`${entryFile} 缺少 paths 段`);
  const entries = [];
  for (let i = start + 1; i < lines.length; i++) {
    const line = lines[i];
    if (!isBlank(line) && indentOf(line) === 0) break;
    if (isBlank(line) || indentOf(line) !== 2) continue;
    const keyMatch = /^ {2}(?:"([^"]+)"|'([^']+)'|(\/[^:]*?))\s*:\s*$/.exec(line);
    if (!keyMatch) throw new Error(`${entryFile} 第 ${i + 1} 行的 paths 条目不是可解析的路径键`);
    const pathKey = keyMatch[1] ?? keyMatch[2] ?? keyMatch[3];
    const refMatch = /^\s{4}"?\$ref"?\s*:\s*"?([^"\s]+)"?\s*$/.exec(lines[i + 1] || '');
    if (!refMatch) throw new Error(`${entryFile} 的 paths."${pathKey}" 没有 $ref`);
    const [file, pointer] = refMatch[1].split('#');
    entries.push({ path: pathKey, file, pointer: (pointer || '').replace(/^\//, '') });
    i += 1;
  }

  const fileCache = new Map();
  const operations = [];
  for (const entry of entries) {
    const file = path.resolve(contractRoot, entry.file);
    if (!fileCache.has(file)) fileCache.set(file, readLines(file));
    const label = `${entry.file}#/${entry.pointer}`;
    const block = slicePathItem(fileCache.get(file), entry.pointer, label);
    for (let i = 0; i < block.length; i++) {
      const m = /^\s*([a-z]+):\s*$/.exec(block[i]);
      if (!m || !HTTP_METHODS.includes(m[1])) continue;
      const declared = responsesOf(block, i, label);
      operations.push({
        method: m[1].toUpperCase(),
        path: SERVER_PREFIX + entry.path,
        codes: [...declared.codes].sort((a, b) => a - b),
        ranges: [...declared.ranges],
        source: label,
      });
    }
  }
  return { operations, pathCount: entries.length };
}

function slicePathItem(lines, pointer, sourceLabel) {
  const pattern = new RegExp(`^${pointer.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}:\\s*$`);
  const start = lines.findIndex((line) => pattern.test(line));
  if (start < 0) throw new Error(`${sourceLabel} 找不到 path item "${pointer}"`);
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
  const childIndent = Math.min(
    ...methodBlock.filter((line) => !isBlank(line)).map((line) => indentOf(line)),
  );
  const responsesIndex = methodBlock.findIndex(
    (line) => indentOf(line) === childIndent && /^\s*responses:\s*\S*/.test(line),
  );
  if (responsesIndex < 0) throw new Error(`${sourceLabel} 的 operation 缺少 responses 段`);
  const responsesIndent = indentOf(methodBlock[responsesIndex]);
  const codes = new Set();
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
