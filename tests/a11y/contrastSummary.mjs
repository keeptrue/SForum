// 把 axe 报告里的 color-contrast 节点按「前景色 / 背景色 / 字号」聚合成可复核的账。
//
// 为什么要这一步：`a11y-*.json` 里每个节点都有 failureSummary，但「公开页还有多少
// 对比度节点、分别是什么颜色对」这种结论必须能从落盘文件一步复算，不能靠报告作者口述。
//
//   node tests/a11y/contrastSummary.mjs work-team/evidence/w4/a11y-after-public.json
//   node tests/a11y/contrastSummary.mjs <report.json> --out=<summary.json>
import { readFileSync, writeFileSync } from 'node:fs'

const args = process.argv.slice(2)
const input = args.find(arg => !arg.startsWith('--'))
const outArg = args.find(arg => arg.startsWith('--out='))
if (!input) {
  console.error('usage: node tests/a11y/contrastSummary.mjs <axe-report.json> [--out=summary.json]')
  process.exit(1)
}

const report = JSON.parse(readFileSync(input, 'utf8'))
const groups = new Map()
const pages = []

for (const page of report.pages || []) {
  const violation = (page.axe?.violations || []).find(item => item.id === 'color-contrast')
  const nodes = violation?.nodes || []
  pages.push({ viewport: page.viewport, path: page.path, nodes: nodes.length })
  for (const node of nodes) {
    const match = /foreground color: ([^,]+), background color: ([^,]+), font size: ([^,]+), font weight: ([^)]+)\)/.exec(node.summary || '')
    const key = match ? `${match[1].trim()} on ${match[2].trim()} @${match[3].trim()}/${match[4].trim()}` : 'unparsed'
    if (!groups.has(key)) groups.set(key, { count: 0, ratio: '', sample: '', targets: [] })
    const entry = groups.get(key)
    entry.count += 1
    if (match && !entry.ratio) {
      const ratio = /insufficient color contrast of ([\d.]+)/.exec(node.summary || '')
      entry.ratio = ratio ? ratio[1] : ''
    }
    if (!entry.sample) entry.sample = (node.html || '').replace(/\s+/g, ' ').slice(0, 120)
    if (entry.targets.length < 3) entry.targets.push((node.target || []).join(' '))
  }
}

const summary = {
  source: input,
  generatedAt: new Date().toISOString(),
  pages,
  total: pages.reduce((sum, page) => sum + page.nodes, 0),
  groups: [...groups].map(([key, value]) => ({ key, ...value })).sort((a, b) => b.count - a.count)
}

const json = JSON.stringify(summary, null, 2)
if (outArg) {
  writeFileSync(outArg.slice('--out='.length), `${json}\n`)
  console.log(`summary: ${outArg.slice('--out='.length)}`)
} else {
  console.log(json)
}
