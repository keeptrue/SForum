// 真机可访问性取证：axe 扫描 / 键盘路径 / 对比度实测。
//
// 与 `tests/browser-admin-390x844.mjs` 同一套 CDP 手法（不引入 Playwright），
// 差别是本脚本把 axe-core 通过 `Page.addScriptToEvaluateOnNewDocument` 注入，
// 因此每次导航后 `window.axe` 都在，扫描的是真实渲染结果而不是静态源码。
//
// axe-core 走 `SFORUM_AXE_PATH`（默认探测本机已装副本）：仓库不新增前端依赖，
// 离线环境也能复现。
//
// 用法：
//   node tests/a11y/scan.mjs --mode=scan --out=work-team/evidence/w4/a11y-before.json
//   node tests/a11y/scan.mjs --mode=keyboard --out=... --path=/control-panel/entity-meta
//   node tests/a11y/scan.mjs --mode=contrast --path=/control-panel/attachments/manager \
//     --selector='[data-attachment-status]'
//
// 环境变量：
//   SFORUM_WEB_BASE_URL   默认 http://127.0.0.1:3000
//   SFORUM_QA_LOGIN       默认 qa_frontend
//   SFORUM_QA_PASSWORD    dev 库口令（不入版本库）
//   SFORUM_AXE_PATH       axe.min.js 路径
//   SFORUM_QA_DIR         截图目录（可选）

import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'

const args = new Map()
for (const raw of process.argv.slice(2)) {
  const match = /^--([^=]+)(?:=(.*))?$/.exec(raw)
  if (!match) continue
  args.set(match[1], match[2] ?? 'true')
}

const web = process.env.SFORUM_WEB_BASE_URL || 'http://127.0.0.1:3000'
const login = process.env.SFORUM_QA_LOGIN || 'qa_frontend'
const password = process.env.SFORUM_QA_PASSWORD || ''
const mode = args.get('mode') || 'scan'
const outPath = args.get('out') || ''
const qaDir = process.env.SFORUM_QA_DIR || ''
const selector = args.get('selector') || ''

const DEFAULT_PATHS = [
  '/control-panel/users',
  '/control-panel/entity-meta',
  '/control-panel/forum/content',
  '/control-panel/attachments/manager',
  '/control-panel/settings',
  '/control-panel/badges',
  '/control-panel/forum/trust-levels',
  '/control-panel/search',
  '/control-panel/jobs'
]
const paths = (args.get('path') || args.get('paths') || '')
  ? (args.get('path') || args.get('paths')).split(',').map(item => item.trim()).filter(Boolean)
  : DEFAULT_PATHS

const VIEWPORTS = [
  { label: 'desktop-1440x900', width: 1440, height: 900, dpr: 1, mobile: false },
  { label: 'mobile-390x844', width: 390, height: 844, dpr: 3, mobile: true }
]

/** axe 的既有违规清单（第 13 轮总表口径），扫描只对这几条做逐条结论。 */
const TRACKED_RULES = ['aria-allowed-attr', 'color-contrast', 'landmark-unique', 'region']

function axePath() {
  if (process.env.SFORUM_AXE_PATH) return process.env.SFORUM_AXE_PATH
  const candidates = [
    '/home/inkedus/Code/wx-factory/node_modules/.pnpm/axe-core@4.14.0/node_modules/axe-core/axe.min.js',
    '/home/inkedus/Code/github/SForum/node_modules/axe-core/axe.min.js',
    '/usr/lib/node_modules/axe-core/axe.min.js'
  ]
  return candidates.find(candidate => existsSync(candidate)) || ''
}

function chromeBinary() {
  for (const candidate of ['/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/opt/google/chrome/chrome', '/usr/bin/chromium']) {
    if (existsSync(candidate)) return candidate
  }
  return ''
}

async function launchChrome(binary) {
  const port = 9600 + Math.floor(Math.random() * 300)
  const profile = `/tmp/sforum-a11y-cdp-${Date.now()}`
  mkdirSync(profile, { recursive: true })
  const child = spawn(binary, [
    '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    // 本机带 http_proxy 时 Chrome 会把 127.0.0.1 也送进代理 → ERR_CONNECTION_REFUSED。
    '--no-proxy-server',
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--hide-scrollbars', 'about:blank'
  ], { stdio: 'ignore' })
  for (let attempt = 0; attempt < 150; attempt += 1) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/version`)
      if (response.ok) return { child, port, version: await response.json() }
    } catch {
      // 等 Chrome 就绪
    }
    await new Promise(resolve => setTimeout(resolve, 100))
  }
  throw new Error(`headless Chrome did not expose CDP on port ${port}`)
}

async function connect(port) {
  const response = await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: 'PUT' })
  const target = await response.json()
  const socket = new WebSocket(target.webSocketDebuggerUrl)
  await new Promise((resolve, reject) => {
    socket.addEventListener('open', resolve, { once: true })
    socket.addEventListener('error', reject, { once: true })
  })
  let nextId = 1
  const pending = new Map()
  const events = []
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data)
    if (message.id && pending.has(message.id)) {
      const { resolve, reject } = pending.get(message.id)
      pending.delete(message.id)
      if (message.error) reject(new Error(message.error.message))
      else resolve(message.result)
      return
    }
    events.push(message)
  })
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const id = nextId++
    pending.set(id, { resolve, reject })
    socket.send(JSON.stringify({ id, method, params }))
  })
  await send('Page.enable')
  await send('Runtime.enable')
  await send('Network.enable')
  return { socket, send, events, targetId: target.id }
}

async function evaluate(send, expression) {
  const result = await send('Runtime.evaluate', {
    expression,
    awaitPromise: true,
    returnByValue: true,
    userGesture: true
  })
  if (result.exceptionDetails) {
    throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text || 'evaluate failed')
  }
  return result.result?.value
}

async function navigate(send, url, waitFor) {
  await send('Page.navigate', { url })
  for (let attempt = 0; attempt < 400; attempt += 1) {
    const state = await evaluate(send, `(() => {
      if (document.readyState !== 'complete') return null
      ${waitFor ? `if (!(${waitFor})) return null` : ''}
      return { url: location.href, title: document.title }
    })()`)
    if (state) return state
    await new Promise(resolve => setTimeout(resolve, 100))
  }
  throw new Error(`timed out waiting for ${url}`)
}

async function settle(send, ms = 700) {
  await new Promise(resolve => setTimeout(resolve, ms))
}

/**
 * 等客户端水合完成再测交互。
 *
 * 页面是 SSR 出来的：DOM 里已经有 id/tabindex/aria，但**事件监听要等水合**。
 * 700ms 的固定 settle 在 entity-meta 这类页面上不够（实测 ~935ms），
 * 会得出「方向键没反应」这种假阴性。
 */
async function waitForHydration(send) {
  for (let attempt = 0; attempt < 60; attempt += 1) {
    const ready = await evaluate(send, `Boolean((document.querySelector('#__nuxt') || document.body.firstElementChild)?.__vue_app__)`)
    if (ready) return true
    await new Promise(resolve => setTimeout(resolve, 200))
  }
  return false
}

async function loginViaApi(send) {
  const payload = JSON.stringify({ login, password })
  const outcome = await evaluate(send, `(async () => {
    const headers = { 'Content-Type': 'application/json' }
    await fetch('/api/v1/health', { credentials: 'include' }).catch(() => null)
    const cookie = document.cookie.split(';').map(part => part.trim())
      .find(part => part.startsWith('csrf_='))
    if (cookie) headers['X-Csrf-Token'] = decodeURIComponent(cookie.slice('csrf_='.length))
    const response = await fetch('/api/v1/auth/login', {
      method: 'POST', credentials: 'include', headers,
      body: ${JSON.stringify(payload)}
    })
    return { status: response.status, body: await response.text() }
  })()`)
  if (outcome.status !== 200) throw new Error(`login failed: ${outcome.status} ${outcome.body.slice(0, 300)}`)
  return outcome
}

const AXE_RUN = `(async () => {
  const result = await window.axe.run(document, {
    resultTypes: ['violations', 'incomplete'],
    runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice'] }
  })
  return {
    violations: result.violations.map(item => ({
      id: item.id,
      impact: item.impact,
      help: item.help,
      nodes: item.nodes.map(node => ({ target: node.target, html: node.html.slice(0, 240), summary: node.failureSummary }))
    })),
    incomplete: result.incomplete.map(item => ({ id: item.id, nodes: item.nodes.length }))
  }
})()`

const LANDMARK_PROBE = `(() => {
  const describe = element => ({
    tag: element.tagName.toLowerCase(),
    role: element.getAttribute('role') || '',
    label: element.getAttribute('aria-label') || '',
    labelledby: element.getAttribute('aria-labelledby') || '',
    id: element.id || '',
    cls: (element.className || '').toString().slice(0, 120)
  })
  const mains = [...document.querySelectorAll('main, [role="main"]')].map(describe)
  const navs = [...document.querySelectorAll('nav, [role="navigation"]')].map(describe)
  const regions = [...document.querySelectorAll('section[aria-label], section[aria-labelledby], [role="region"]')].map(describe)
  const banners = [...document.querySelectorAll('header, [role="banner"]')].map(describe)
  return {
    mainCount: mains.length,
    mains,
    navCount: navs.length,
    navs,
    regionCount: regions.length,
    banners,
    bodyScrollWidth: document.documentElement.scrollWidth,
    innerWidth: window.innerWidth,
    horizontalOverflow: document.documentElement.scrollWidth > window.innerWidth
  }
})()`

const COLOR_HELPERS = `
  const canvas = document.createElement('canvas')
  canvas.width = canvas.height = 1
  const ctx = canvas.getContext('2d', { willReadFrequently: true })
  /** 任意 CSS 颜色（含 oklab()/color-mix() 的计算值）→ sRGB；alpha 混合按给定底色。 */
  const resolve = (value, backdrop) => {
    ctx.clearRect(0, 0, 1, 1)
    ctx.fillStyle = backdrop
    ctx.fillRect(0, 0, 1, 1)
    ctx.fillStyle = value
    ctx.fillRect(0, 0, 1, 1)
    const data = ctx.getImageData(0, 0, 1, 1).data
    return { r: data[0], g: data[1], b: data[2], a: data[3] / 255 }
  }
  const paintLayers = layers => {
    let color = 'rgb(255, 255, 255)'
    for (const layer of layers) color = 'rgb(' + resolve(layer, color).r + ',' + resolve(layer, color).g + ',' + resolve(layer, color).b + ')'
    return color
  }
  const channel = v => { const n = v / 255; return n <= 0.03928 ? n / 12.92 : ((n + 0.055) / 1.055) ** 2.4 }
  const luminance = c => 0.2126 * channel(c.r) + 0.7152 * channel(c.g) + 0.0722 * channel(c.b)
  const contrast = (a, b) => { const x = luminance(a), y = luminance(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05) }
  const backgroundLayers = element => {
    const layers = []
    let node = element
    while (node && node.nodeType === 1) {
      const color = getComputedStyle(node).backgroundColor
      if (color && color !== 'rgba(0, 0, 0, 0)' && color !== 'transparent') {
        layers.unshift(color)
        const resolved = resolve(color, 'rgb(255, 255, 255)')
        if (resolved.a >= 0.999) break
      }
      node = node.parentElement
    }
    return layers
  }
`

const CONTRAST_PROBE = (target) => `(() => {
  ${COLOR_HELPERS}
  const elements = [...document.querySelectorAll(${JSON.stringify(target)})]
  return elements.slice(0, 5).map(element => {
    const style = getComputedStyle(element)
    const background = paintLayers(backgroundLayers(element))
    const foreground = resolve(style.color, background)
    const box = element.getBoundingClientRect()
    return {
      found: true,
      text: (element.textContent || '').trim().slice(0, 60),
      cls: (element.className || '').toString().slice(0, 160),
      color: style.color,
      foreground: 'rgb(' + foreground.r + ', ' + foreground.g + ', ' + foreground.b + ')',
      background,
      fontSize: style.fontSize,
      fontWeight: style.fontWeight,
      ratio: Math.round(contrast(foreground, resolve(background, 'rgb(255,255,255)')) * 100) / 100,
      largeText: parseFloat(style.fontSize) >= 24 || (parseFloat(style.fontSize) >= 18.66 && Number(style.fontWeight) >= 700),
      box: { width: Math.round(box.width), height: Math.round(box.height) }
    }
  })
})()`

const KEYBOARD_PROBE = `(() => {
  const element = document.activeElement
  if (!element || element === document.body) return { tag: 'body' }
  const style = getComputedStyle(element)
  const pseudoIndicator = (name) => {
    const pseudo = getComputedStyle(element, name)
    const outline = pseudo.outlineStyle !== 'none' && parseFloat(pseudo.outlineWidth || '0') > 0
    const shadow = pseudo.boxShadow && pseudo.boxShadow !== 'none'
    const background = pseudo.backgroundColor && pseudo.backgroundColor !== 'rgba(0, 0, 0, 0)'
    return outline || shadow || background
  }
  const focusVisible = element.matches(':focus-visible')
  const ownRing = (style.outlineStyle !== 'none' && parseFloat(style.outlineWidth || '0') > 0) || (style.boxShadow && style.boxShadow !== 'none')
  // Nuxt UI / Tailwind 的焦点环常画在 ::before（before:outline-3），只看元素本身会误判「无焦点可见」。
  const indicator = Boolean(ownRing) || pseudoIndicator('::before') || pseudoIndicator('::after')
  const box = element.getBoundingClientRect()
  return {
    tag: element.tagName.toLowerCase(),
    id: element.id || '',
    role: element.getAttribute('role') || '',
    label: element.getAttribute('aria-label') || '',
    text: (element.textContent || '').trim().replace(/\\s+/g, ' ').slice(0, 60),
    href: element.getAttribute('href') || '',
    focusVisible,
    indicator,
    visible: box.width > 0 && box.height > 0,
    box: { x: Math.round(box.x), y: Math.round(box.y), width: Math.round(box.width), height: Math.round(box.height) }
  }
})()`

async function capture(send, path) {
  if (!path) return
  const shot = await send('Page.captureScreenshot', { format: 'png' })
  mkdirSync(dirname(path), { recursive: true })
  writeFileSync(path, Buffer.from(shot.data, 'base64'))
}

async function main() {
  const binary = chromeBinary()
  if (!binary) throw new Error('no chrome binary found')
  const axeSource = readFileSync(axePath(), 'utf8')
  const { child, port } = await launchChrome(binary)
  const { socket, send } = await connect(port)
  const report = { generatedAt: new Date().toISOString(), base: web, mode, axe: axePath(), pages: [] }
  try {
    await send('Page.addScriptToEvaluateOnNewDocument', { source: axeSource })
    await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false })
    await navigate(send, `${web}/login`, `document.querySelector('#login-input')`)
    await loginViaApi(send)
    await navigate(send, `${web}/`, `document.readyState === 'complete'`)

    for (const viewport of VIEWPORTS) {
      if (mode === 'keyboard' && viewport.mobile) continue
      await send('Emulation.setDeviceMetricsOverride', {
        width: viewport.width, height: viewport.height, deviceScaleFactor: viewport.dpr, mobile: viewport.mobile
      })
      for (const path of paths) {
        const entry = { path, viewport: viewport.label, url: '', title: '' }
        try {
          let state = await navigate(send, `${web}${path}`, `document.body && document.body.innerText.trim().length > 0`)
          // dev 会话偶尔在长扫描中途失效（页面被重定向到 /login）：重新登录并重试一次，
          // 否则会把「登录页的违规」记到被扫页面上，证据失真。
          if (path.startsWith('/control-panel') && state.url.includes('/login')) {
            await loginViaApi(send)
            state = await navigate(send, `${web}${path}`, `document.body && document.body.innerText.trim().length > 0`)
            entry.relogged = true
          }
          entry.url = state.url
          entry.title = state.title
          await settle(send)
          entry.hydrated = await waitForHydration(send)
          if (process.env.SFORUM_PREP_EXPR) {
            await evaluate(send, process.env.SFORUM_PREP_EXPR)
            await settle(send, 600)
          }
          entry.landmarks = await evaluate(send, LANDMARK_PROBE)
          if (mode === 'scan') {
            entry.axe = await evaluate(send, AXE_RUN)
            entry.tracked = entry.axe.violations.filter(item => TRACKED_RULES.includes(item.id))
          }
          if (mode === 'keyboard') {
            const steps = []
            const tabCount = Number(args.get('tabs') || 24)
            for (let index = 0; index < tabCount; index += 1) {
              await send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 })
              await send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 })
              await new Promise(resolve => setTimeout(resolve, 60))
              steps.push(await evaluate(send, KEYBOARD_PROBE))
            }
            entry.tabOrder = steps
            if (args.get('focus-selector')) {
              const focused = await evaluate(send, `(() => {
                const element = document.querySelector(${JSON.stringify(args.get('focus-selector'))})
                if (!element) return false
                element.focus()
                return document.activeElement === element
              })()`)
              entry.focused = focused
              await settle(send, 300)
              if (qaDir) {
                const shotPath = `${qaDir}/focus-target-${viewport.label}-${path.replace(/[^a-z0-9]+/gi, '-')}.png`
                await capture(send, shotPath)
                entry.focusScreenshot = shotPath
              }
            }
            if (qaDir) {
              const shotPath = `${qaDir}/focus-${viewport.label}-${path.replace(/[^a-z0-9]+/gi, '-')}.png`
              await capture(send, shotPath)
              entry.screenshot = shotPath
            }
          }
          if (mode === 'tabs') {
            const tablistSelector = selector || '[role="tablist"]'
            const readState = `(() => {
              const list = document.querySelector(${JSON.stringify(tablistSelector)})
              if (!list) return null
              const tabs = [...list.querySelectorAll('[role="tab"]')]
              return {
                selected: tabs.map(tab => tab.getAttribute('aria-selected')),
                tabindex: tabs.map(tab => tab.getAttribute('tabindex')),
                labels: tabs.map(tab => (tab.textContent || '').trim().slice(0, 20)),
                focused: document.activeElement ? (document.activeElement.textContent || '').trim().slice(0, 20) : '',
                focusIsTab: document.activeElement ? document.activeElement.getAttribute('role') === 'tab' : false
              }
            })()`
            const press = async (key, code, virtualKeyCode) => {
              await send('Input.dispatchKeyEvent', { type: 'keyDown', key, code, windowsVirtualKeyCode: virtualKeyCode })
              await send('Input.dispatchKeyEvent', { type: 'keyUp', key, code, windowsVirtualKeyCode: virtualKeyCode })
              await new Promise(resolve => setTimeout(resolve, 120))
            }
            const focusActive = `(() => {
              const list = document.querySelector(${JSON.stringify(tablistSelector)})
              const active = list && list.querySelector('[role="tab"][aria-selected="true"]')
              if (!active) return false
              active.focus()
              return document.activeElement === active
            })()`
            entry.tabs = { selector: tablistSelector, focusedActive: await evaluate(send, focusActive), before: await evaluate(send, readState) }
            await press('ArrowRight', 'ArrowRight', 39)
            entry.tabs.afterRight = await evaluate(send, readState)
            await press('End', 'End', 35)
            entry.tabs.afterEnd = await evaluate(send, readState)
            await press('Home', 'Home', 36)
            entry.tabs.afterHome = await evaluate(send, readState)
          }
          if (mode === 'contrast') {
            entry.contrast = await evaluate(send, CONTRAST_PROBE(selector))
          }
          if (mode === 'probe') {
            entry.probe = await evaluate(send, process.env.SFORUM_PROBE_EXPR || 'null')
          }
        } catch (error) {
          entry.error = String(error && error.message ? error.message : error)
        }
        report.pages.push(entry)
        const line = mode === 'scan'
          ? `${viewport.label} ${path} tracked=${(entry.tracked || []).map(item => `${item.id}:${item.nodes.length}`).join(',') || '0'} main=${entry.landmarks?.mainCount ?? '?'} overflow=${entry.landmarks?.horizontalOverflow ?? '?'}`
          : `${viewport.label} ${path} ${mode}`
        console.log(line)
      }
    }
  } finally {
    socket.close()
    child.kill('SIGKILL')
  }
  const json = JSON.stringify(report, null, 2)
  if (outPath) {
    mkdirSync(dirname(outPath), { recursive: true })
    writeFileSync(outPath, json)
    console.log(`report: ${outPath}`)
  } else {
    console.log(json)
  }
}

main().catch(error => {
  console.error(error)
  process.exitCode = 1
})
