// 管理端 390 宽的浏览器验收（M5 任务 8）。
//
// 与公开页的 390x844 验收同一个 CDP 手法，唯一区别：管理端需要已登录会话。
// 会话 cookie 是 httpOnly，`document.cookie` 读不到，但 CDP 的 `Network.setCookie`
// 可以设置；因此本脚本从环境变量读取会话值，而不是把它写进仓库。
//
// 用法（会话值从已登录的载体经 CDP `Network.getCookies` 取得）：
//   SFORUM_SESSION_COOKIE=<value> node tests/browser-admin-390x844.mjs
// 环境变量：
//   SFORUM_WEB_BASE_URL   默认 http://127.0.0.1:3000
//   SFORUM_ADMIN_PATH     默认 /control-panel/extensions/route-inspector
//   SFORUM_QA_DIR         可选：截图目录
//   SFORUM_EVIDENCE_OUTPUT 可选：JSON 报告路径
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const web = process.env.SFORUM_WEB_BASE_URL || 'http://127.0.0.1:3000'
const adminPath = process.env.SFORUM_ADMIN_PATH || '/control-panel/extensions/route-inspector'
const session = process.env.SFORUM_SESSION_COOKIE || ''
const qaDir = process.env.SFORUM_QA_DIR || ''
const outputPath = process.env.SFORUM_EVIDENCE_OUTPUT || ''
const WIDTH = 390
const HEIGHT = 844
const DPR = 3

const failures = []
const evidence = []
// SKIP 路径不得打印「全部通过」。
let asserted = false

function fail(message, detail) {
  failures.push(detail ? `${message} :: ${detail}` : message)
}

function chromeBinary() {
  for (const candidate of ['/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/opt/google/chrome/chrome', '/usr/bin/chromium']) {
    if (existsSync(candidate)) return candidate
  }
  return ''
}

async function launchChrome(binary) {
  const port = 9800 + Math.floor(Math.random() * 300)
  const profile = `/tmp/sforum-admin-cdp-${Date.now()}`
  mkdirSync(profile, { recursive: true })
  const child = spawn(binary, [
    '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--hide-scrollbars', 'about:blank'
  ], { stdio: 'ignore' })
  for (let attempt = 0; attempt < 120; attempt += 1) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/version`)
      if (response.ok) return { child, port, version: await response.json() }
    } catch {
      // 等待 Chrome 就绪。
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
  const consoleMessages = []
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data)
    if (message.id && pending.has(message.id)) {
      const { resolve, reject } = pending.get(message.id)
      pending.delete(message.id)
      if (message.error) reject(new Error(message.error.message))
      else resolve(message.result)
      return
    }
    if (message.method === 'Runtime.consoleAPICalled') {
      consoleMessages.push({
        type: message.params.type,
        text: (message.params.args || []).map(argument => argument.value ?? argument.description ?? '').join(' ')
      })
    }
    if (message.method === 'Runtime.exceptionThrown') {
      consoleMessages.push({ type: 'exception', text: message.params.exceptionDetails?.text || 'exception' })
    }
  })
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const id = nextId++
    pending.set(id, { resolve, reject })
    socket.send(JSON.stringify({ id, method, params }))
  })
  await send('Page.enable')
  await send('Runtime.enable')
  await send('Network.enable')
  await send('Emulation.setDeviceMetricsOverride', { width: WIDTH, height: HEIGHT, deviceScaleFactor: DPR, mobile: true })
  return { socket, send, consoleMessages, targetId: target.id }
}

const measure = `(() => {
  const visible = element => {
    const style = getComputedStyle(element)
    const box = element.getBoundingClientRect()
    return style.display !== 'none' && style.visibility !== 'hidden' && box.width > 0 && box.height > 0
  }
  const clipped = [...document.querySelectorAll('button, a, input, select, textarea, [role="button"]')]
    .filter(visible)
    .filter(element => { const box = element.getBoundingClientRect(); return box.right > innerWidth + 1 || box.left < -1 })
  const inspect = document.querySelector('[data-testid="admin-route-inspector"]')
  const incidents = document.querySelector('[data-testid="route-incidents"]')
  const loading = document.querySelector('[data-testid="route-incidents-loading"]')
  return {
    url: location.pathname,
    innerWidth, innerHeight, dpr: devicePixelRatio,
    horizontalOverflow: document.documentElement.scrollWidth > innerWidth,
    scrollWidth: document.documentElement.scrollWidth,
    clippedControls: clipped.length,
    clippedSample: clipped.slice(0, 3).map(element => element.tagName + '.' + (element.className || '').toString().slice(0, 40)),
    inspectorPresent: !!inspect,
    incidentsPanelPresent: !!incidents,
    incidentsLoading: !!loading,
    incidentsSummary: (document.querySelector('[data-testid="route-incidents-summary"]') || {}).textContent || '',
    tabs: document.querySelectorAll('[role="tab"], [data-testid^="route-inspector-tab"]').length,
    bodyText: document.body.innerText.slice(0, 120)
  }
})()`

async function main() {
  if (!session) {
    console.log('SKIP set SFORUM_SESSION_COOKIE (from CDP Network.getCookies on a logged-in session) to run the admin pass')
    return finish()
  }
  const binary = chromeBinary()
  if (!binary) {
    console.log('SKIP no Chrome/Chromium binary found')
    return finish()
  }
  asserted = true
  const { child, port, version } = await launchChrome(binary)
  const client = await connect(port)
  evidence.push({ browser: version.Browser, viewport: `${WIDTH}x${HEIGHT} dpr ${DPR}`, path: adminPath })
  try {
    await client.send('Network.setCookie', {
      name: 'sforum_session', value: session, domain: '127.0.0.1', path: '/', httpOnly: true, sameSite: 'Lax'
    })
    await client.send('Page.navigate', { url: `${web}${adminPath}` })
    await new Promise(resolve => {
      const timer = setTimeout(resolve, 20000)
      const onMessage = event => {
        const message = JSON.parse(event.data)
        if (message.method === 'Page.loadEventFired') {
          clearTimeout(timer)
          client.socket.removeEventListener('message', onMessage)
          resolve()
        }
      }
      client.socket.addEventListener('message', onMessage)
    })
    // 面板是异步加载的：给它一点时间，再测量。
    await new Promise(resolve => setTimeout(resolve, 2500))
    const result = await client.send('Runtime.evaluate', { expression: measure, returnByValue: true, awaitPromise: true })
    const measurement = result.result.value
    const consoleErrors = client.consoleMessages.filter(message => message.type === 'error' || message.type === 'exception')
    evidence.push({ ...measurement, consoleErrors })
    if (measurement.innerWidth !== WIDTH) fail('viewport override did not apply', `innerWidth=${measurement.innerWidth}`)
    if (!measurement.inspectorPresent) fail('route inspector page did not render (auth or route problem)', JSON.stringify(measurement).slice(0, 200))
    if (!measurement.incidentsPanelPresent) fail('route incidents panel missing')
    if (measurement.horizontalOverflow) fail('horizontal overflow', `scrollWidth=${measurement.scrollWidth}`)
    if (measurement.clippedControls !== 0) fail('clipped controls', JSON.stringify(measurement.clippedSample))
    if (consoleErrors.length > 0) fail('console errors', JSON.stringify(consoleErrors.slice(0, 3)))
    if (qaDir) {
      mkdirSync(qaDir, { recursive: true })
      const shot = await client.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false })
      const file = join(qaDir, `2026-10-16-admin-390x844${adminPath.replaceAll('/', '-')}.png`)
      writeFileSync(file, Buffer.from(shot.data, 'base64'))
      evidence[evidence.length - 1].screenshot = file
    }
  } finally {
    client.socket.close()
    child.kill('SIGKILL')
  }
  finish()
}

function finish() {
  if (outputPath) writeFileSync(outputPath, `${JSON.stringify({ web, adminPath, failures, evidence }, null, 2)}\n`, 'utf8')
  console.log(`admin 390x844 browser evidence (web=${web} path=${adminPath})`)
  for (const entry of evidence) {
    if (entry.browser) {
      console.log(`  browser: ${entry.browser} viewport=${entry.viewport}`)
      continue
    }
    console.log(
      `  ${entry.url}: ${entry.innerWidth}x${entry.innerHeight} dpr=${entry.dpr} inspector=${entry.inspectorPresent} ` +
        `incidentsPanel=${entry.incidentsPanelPresent} overflow=${entry.horizontalOverflow} clipped=${entry.clippedControls} ` +
        `consoleErrors=${entry.consoleErrors.length} summary=${JSON.stringify(entry.incidentsSummary)}`
    )
  }
  if (failures.length > 0) {
    for (const failure of failures) console.error(`  FAIL ${failure}`)
    process.exitCode = 1
    return
  }
  if (!asserted) {
    console.log('  skipped: no assertion ran')
    return
  }
  console.log('  all admin 390x844 acceptance assertions passed')
}

try {
  await main()
} catch (error) {
  fail('harness error', String(error?.message || error))
  finish()
}
