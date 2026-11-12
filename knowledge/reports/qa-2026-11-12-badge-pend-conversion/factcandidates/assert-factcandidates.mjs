// F-01 / GAP-C 值级断言：`GET /admin/badges/conditions` 的 HTTP 原始响应。
// node assert-factcandidates.mjs <conditions.json> <expectKey...>
import { readFileSync } from 'node:fs'

const [, , file, ...expectKeys] = process.argv
if (!file) {
  console.error('usage: node assert-factcandidates.mjs <conditions.json> <expectKey...>')
  process.exit(2)
}

const raw = JSON.parse(readFileSync(file, 'utf8'))
// 端点包了一层 {code,message,data}；值级断言针对 data。
const payload = raw && typeof raw === 'object' && raw.data ? raw.data : raw
const fails = []
const oks = []

function ok(m) { oks.push(m); console.log(`  ok   ${m}`) }
function bad(m) { fails.push(m); console.log(`  FAIL ${m}`) }
function flag(cond, goodMsg, badMsg) { cond ? ok(goodMsg) : bad(badMsg) }

const len = (s) => (typeof s === 'string' ? s.trim().length : -1)

// ---- 顶层形状 ----
flag(Array.isArray(payload.items), 'items 是数组', `items 不是数组: ${typeof payload.items}`)
flag(Array.isArray(payload.factCandidates), 'factCandidates 是数组（不是 null/缺席）',
  `factCandidates 不是数组: ${String(JSON.stringify(payload.factCandidates)).slice(0, 120)}`)

const items = payload.items ?? []
const facts = payload.factCandidates ?? []

// ---- items 键集合仍等于冻结 10 键（M2 契约没被 factCandidates 带漂）----
const wantItemKeys = 'compare,describeEn,describeZh,key,labelEn,labelZh,params,unitEn,unitZh,valueKind'
const gotItemKeys = items.length ? Object.keys(items[0]).sort().join(',') : ''
flag(gotItemKeys === wantItemKeys, `items[].键集合 == 冻结 10 键（${gotItemKeys}）`,
  `items 键漂移: got=[${gotItemKeys}] want=[${wantItemKeys}]`)

// ---- core 事实 `fact.count` 仍在 items 里 ----
const coreKeys = items.map((it) => it.key)
flag(coreKeys.includes('fact.count'), 'items 里含 fact.count',
  `items 里没有 fact.count；实际 keys=${JSON.stringify(coreKeys).slice(0, 200)}`)

// ---- 每个 factCandidate 值级 ----
flag(facts.length >= expectKeys.length, `factCandidates 条数 ${facts.length} >= 期望 ${expectKeys.length}`,
  `factCandidates 条数 ${facts.length} < 期望 ${expectKeys.length}（扩展没进候选？）`)

for (const f of facts) {
  const tag = f?.key || '<空 key>'
  flag(!!f?.key, `候选 key 非空（${tag}）`, '候选存在空 key')
  flag(len(f?.label?.zh) > 0, `候选 ${tag} label.zh 长度=${len(f?.label?.zh)}`,
    `候选 ${tag} label.zh 为空（${JSON.stringify(f?.label)}）`)
  flag(len(f?.label?.en) > 0, `候选 ${tag} label.en 长度=${len(f?.label?.en)}`,
    `候选 ${tag} label.en 为空（${JSON.stringify(f?.label)}）`)
  flag(Array.isArray(f?.compare) && f.compare.length > 0, `候选 ${tag} compare 非空数组`,
    `候选 ${tag} compare 非法: ${JSON.stringify(f?.compare)}`)
  for (const v of f?.variants ?? []) {
    const vt = `${tag}/${v?.value ?? '<空 value>'}`
    flag(len(v?.value) > 0, `variant ${vt} value 非空`,
      `variant ${tag} 存在空 value: ${JSON.stringify(v)}`)
    flag(len(v?.label?.zh) > 0, `variant ${vt} label.zh 长度=${len(v?.label?.zh)}`,
      `variant ${vt} label.zh 为空`)
    flag(len(v?.label?.en) > 0, `variant ${vt} label.en 长度=${len(v?.label?.en)}`,
      `variant ${vt} label.en 为空`)
  }
}

// ---- 本次启用的扩展的每一条声明都必须在候选里，且 variants 值非空 ----
const byKey = new Map(facts.map((f) => [f?.key, f]))
for (const key of expectKeys) {
  const found = byKey.get(key)
  if (!found) {
    bad(`缺少期望候选: ${key}（实际 ${JSON.stringify([...byKey.keys()])}）`)
    continue
  }
  ok(`期望候选在位: ${key}`)
  const variants = found.variants ?? []
  if (variants.length > 0) {
    const emptyValues = variants.filter((v) => !String(v?.value ?? '').trim())
    flag(emptyValues.length === 0,
      `${key} variants[].value 全部非空（${variants.map((v) => v.value).join(',')}）`,
      `${key} 有 ${emptyValues.length} 个空 variant value`)
  } else {
    ok(`${key} 无 variants（按 spec 合法）`)
  }
}

// ---- 反向：候选里没有任何 label 为空的对象（避免「多出来的假候选」）----
const emptyLabelFacts = facts.filter((f) => len(f?.label?.zh) <= 0 || len(f?.label?.en) <= 0)
flag(emptyLabelFacts.length === 0, '所有候选双语 label 非空（无假候选）',
  `${emptyLabelFacts.length} 个候选 label 为空: ${emptyLabelFacts.map((f) => f?.key).join(',')}`)

console.log(`\n  候选总览: ${facts.map((f) => `${f.key}(zh=${len(f.label?.zh)},en=${len(f.label?.en)},variants=${(f.variants ?? []).length})`).join(' | ')}`)
console.log(`  RESULT ok=${oks.length} fail=${fails.length}`)
process.exit(fails.length === 0 ? 0 : 1)
