// UX2-17 剩余面清单：85 个页面 × 列表三态（加载 / 空 / 错误）现状。
//
// 为什么要机器生成：审计的剩余面是「85 页里其余 bespoke 状态区未逐页扫」，
// 靠人肉清单下一次改动就失真。这里把口径写成可复跑的脚本：
//
//   node tests/a11y/listStateInventory.mjs            # markdown 表格
//   node tests/a11y/listStateInventory.mjs --json     # 机器可读
//
// 判定口径（与 `apps/web/tests/a11y/adminListStates.test.ts` 共用）：
//   - 列表面：页面模板里直接出现 `<table` / `<tbody` / `SFPagination`，或模板里
//     `v-for` 遍历一个名字像集合的绑定；页面若把列表委托给 `SFAdmin*` / `SF*`
//     组件，则跟随到组件文件再判一次（委托面记 `delegated:<组件>`）。
//   - 加载态：`SFSkeleton` / `USkeleton` / `aria-busy`。
//   - 空态：`SFEmptyState`（共享组件）。
//   - 错误态：`UAlert`/`SFAlert` 带 error 配色，或 `v-if` 直接绑 `*Error` 状态。
//   - bespoke：模板里用 `t('...loading...')` / `t('...empty...')` 纯文本自己拼状态区。
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const WEB_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../../apps/web')
const PAGES_ROOT = `${WEB_ROOT}/app/pages/`

/** 结构化列表面（不再用裸 `v-for` 判，见 classify 里的说明）。 */
export const LIST_VFOR = /<table|<tbody|SFPagination|role="table"/
export const LOADING_MARKERS = /SFSkeleton|USkeleton|aria-busy/
export const EMPTY_MARKER = /SFEmptyState/
export const ERROR_MARKERS = [
  /(?:UAlert|SFAlert)[^>]*(?:color="error"|:color="[^"]*error)/,
  /v-if="[^"]*(?:loadError|errorMessage|statusError|detailError|loadFailed|\berror\b)"/,
  /v-else-if="[^"]*(?:Error|Failed|\berror\b)"/
]
/**
 * 后台列表的**约定**错误通道是 error toast（`color: 'error'` + `duration: 0`，
 * 见 `adminErrorToastContract`）：行内 Alert 只在"整页不可用"时出现。两种都算错误态已收口。
 */
export const ERROR_TOAST = /color:\s*'error'/
export const BESPOKE_LOADING = /t\('(?:admin|common|search)[^']*(?:loading|Loading)[^']*'\)/
export const BESPOKE_EMPTY = /t\('(?:admin|common|forum|search)[^']*(?:\.empty|noData|emptyState|noResults)[^']*'\)/

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const full = `${dir.replace(/\/$/, '')}/${name}`
    if (statSync(full).isDirectory()) walk(full, out)
    else if (name.endsWith('.vue')) out.push(full)
  }
  return out
}

function templateOf(source) {
  const start = source.indexOf('<template>')
  return start === -1 ? source : source.slice(start)
}

function importedComponents(template) {
  return [...new Set([...template.matchAll(/<((?:SF|U)[A-Z][A-Za-z0-9]*)\b/g)].map(match => match[1]))]
}

let componentIndex = null

function componentPath(name) {
  if (!componentIndex) {
    componentIndex = new Map()
    for (const file of walk(`${WEB_ROOT}/app/components`)) {
      const base = file.slice(file.lastIndexOf('/') + 1, -'.vue'.length)
      if (!componentIndex.has(base)) componentIndex.set(base, file)
    }
  }
  return componentIndex.get(name)
}

export function classify(source) {
  const template = templateOf(source)
  const script = source.slice(0, source.indexOf('<template>'))
  return {
    // 只认结构化列表：`<table>` / `<tbody>` / `SFPagination` / `role="table"`。
    // 纯 `v-for` 会把页签条、图例这类非列表也吞进来（SFAdminFixedTabNav 就是这种）。
    inlineList: /<table|<tbody|SFPagination|role="table"/.test(template),
    loading: LOADING_MARKERS.test(template),
    empty: EMPTY_MARKER.test(template),
    error: ERROR_MARKERS.some(marker => marker.test(template)) || ERROR_TOAST.test(script),
    inlineError: ERROR_MARKERS.some(marker => marker.test(template)),
    bespokeLoading: BESPOKE_LOADING.test(template) && !LOADING_MARKERS.test(template),
    bespokeEmpty: BESPOKE_EMPTY.test(template) && !EMPTY_MARKER.test(template)
  }
}

/** 页面若有委托组件承载列表，跟随一层（只跟一层，避免把整棵组件树算成页面的状态面）。 */
export function delegatedSurface(source) {
  const template = templateOf(source)
  const owners = []
  for (const name of importedComponents(template)) {
    if (!/^SF/.test(name)) continue
    const file = componentPath(name)
    if (!file) continue
    const state = classify(readFileSync(file, 'utf8'))
    if (state.inlineList) owners.push({ component: name, ...state })
  }
  return owners
}

export function inventory() {
  return walk(PAGES_ROOT).sort().map((file) => {
    const source = readFileSync(file, 'utf8')
    const page = relative(PAGES_ROOT, file)
    const state = classify(source)
    const delegated = state.inlineList ? [] : delegatedSurface(source)
    return { page, ...state, delegated }
  })
}

function missingStates(state) {
  const missing = []
  if (!state.loading) missing.push('加载')
  if (!state.empty) missing.push('空')
  if (!state.error) missing.push('错误')
  return missing
}

function verdict(row) {
  if (row.inlineList) {
    const missing = missingStates(row)
    return missing.length === 0 ? 'inline 三态齐' : `inline：缺 ${missing.join('/')}`
  }
  if (row.delegated.length) {
    const gaps = row.delegated
      .map(owner => ({ component: owner.component, missing: missingStates(owner) }))
      .filter(entry => entry.missing.length > 0)
    return gaps.length === 0
      ? `delegated 三态齐（${row.delegated.map(owner => owner.component).join(',')}）`
      : `delegated：${gaps.map(entry => `${entry.component} 缺 ${entry.missing.join('/')}`).join('；')}`
  }
  return '无列表面'
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  const rows = inventory().map(row => ({ ...row, verdict: verdict(row) }))
  if (process.argv.includes('--json')) {
    console.log(JSON.stringify(rows, null, 2))
  } else {
    console.log('| # | 页面 | 列表面 | 加载 | 空 | 错误 | 现状 |')
    console.log('|---|---|---|---|---|---|---|')
    rows.forEach((row, index) => {
      const surface = row.inlineList ? 'inline' : row.delegated.length ? `delegated:${row.delegated.map(item => item.component).join(',')}` : '—'
      const mark = value => (value ? '共享' : '—')
      console.log(`| ${index + 1} | \`${row.page}\` | ${surface} | ${mark(row.loading)} | ${mark(row.empty)} | ${mark(row.error)} | ${row.verdict} |`)
    })
    const counts = rows.reduce((acc, row) => {
      const key = row.verdict.endsWith('三态齐') ? 'ok' : row.verdict === '无列表面' ? 'none' : 'gap'
      acc[key] += 1
      return acc
    }, { ok: 0, gap: 0, none: 0 })
    console.log(`\n合计：${rows.length} 页 · 三态齐 ${counts.ok} · 有缺口 ${counts.gap} · 无列表面 ${counts.none}`)
  }
}
