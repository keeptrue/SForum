// 扩展未启用时 `GET /admin/badges/conditions` 的空候选形态断言。
// 依据：`AdminService.BadgeConditions` → `conditions.Core().PublishableDefinitions(candidates)`
// 是**展示面**——objectRef 没有候选的条件不出现，所以没有候选时 `fact.count` 应当消失。
import { readFileSync } from 'node:fs'

const raw = JSON.parse(readFileSync(process.argv[2], 'utf8'))
const p = raw?.data ?? raw
let fails = 0
const t = (cond, good, bad) => { cond ? (console.log(`  ok   ${good}`)) : (console.log(`  FAIL ${bad}`), fails++) }

// ① 必须是数组、length 0、且不是 null
t(Array.isArray(p.factCandidates), 'factCandidates 是数组', `factCandidates 类型=${p.factCandidates === null ? 'null' : typeof p.factCandidates}`)
t(p.factCandidates !== null, 'factCandidates 不是 null（未初始化切片会序列化成 null）', 'factCandidates 是 null')
t(Array.isArray(p.factCandidates) && p.factCandidates.length === 0,
  'factCandidates == []（length 0）',
  `factCandidates 非空：${JSON.stringify(p.factCandidates)?.slice(0, 200)}`)

const items = p.items
t(Array.isArray(items), 'items 是数组', 'items 不是数组')

// ② items 里不得再出现 fact.count（展示面：有候选才出现）
const keys = Array.isArray(items) ? items.map((i) => i?.key) : []
t(!keys.includes('fact.count'), 'items[].key 不含 fact.count（无候选 → 该条件退出展示面）',
  `items 里仍有 fact.count；keys=${JSON.stringify(keys)}`)

// ③ items 条数 == 12
t(keys.length === 12, `items 条数 == 12（实测 ${keys.length}）`, `items 条数 ${keys.length} != 12`)

// ④ 每一项键集合仍在冻结集合内：`unitZh/unitEn` 在 DTO 里是 omitempty
//（没有计量单位的条件不出现这两个键），所以合法集合只有「9 键」与「9 键 + unit」两种。
const base = ['compare', 'describeEn', 'describeZh', 'key', 'labelEn', 'labelZh', 'params', 'valueKind']
const withUnit = [...base, 'unitEn', 'unitZh'].sort().join(',')
const baseJoined = [...base].sort().join(',')
const sets = (items ?? []).map((i) => Object.keys(i).sort().join(','))
const illegal = sets.filter((k) => k !== baseJoined && k !== withUnit)
t(illegal.length === 0,
  `每一项键集合 ∈ {9 键, 9 键 + unit}（${sets.filter((k) => k === withUnit).length} 项带 unit）`,
  `有 ${illegal.length} 项键集合越界：${illegal[0]}`)
t(sets.filter((k) => k === withUnit).length + sets.filter((k) => k === baseJoined).length === sets.length,
  '所有项都落在两种合法形态之一', '存在既非 9 键也非 9+unit 的项')

console.log(`\n  items keys: ${JSON.stringify(keys)}`)
console.log(`  RESULT fail=${fails}`)
process.exit(fails === 0 ? 0 : 1)
