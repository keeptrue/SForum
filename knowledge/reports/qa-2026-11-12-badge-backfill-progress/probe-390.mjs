// 390x844 视觉验收探针：后台 /control-panel/badges 的「回扫进度行」。
//
// 手法与 tests/browser-admin-390x844.mjs 一致：真实 Chrome + CDP
// `Emulation.setDeviceMetricsOverride` + `Network.setCookie`（会话是 httpOnly，
// document.cookie 读不到）。本脚本只针对回扫进度行与被测面，不修改应用代码。
//
// 用法：
//   SFORUM_SESSION_COOKIE=<value> SFORUM_STATE=running|failed \
//   SFORUM_QA_DIR=<dir> SFORUM_EVIDENCE_OUTPUT=<json> node probe-390.mjs
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const web = process.env.SFORUM_WEB_BASE_URL || 'http://127.0.0.1:3000'
const adminPath = process.env.SFORUM_ADMIN_PATH || '/control-panel/badges'
const session = process.env.SFORUM_SESSION_COOKIE || ''
const state = process.env.SFORUM_STATE || 'running'
const qaDir = process.env.SFORUM_QA_DIR || ''
const outputPath = process.env.SFORUM_EVIDENCE_OUTPUT || ''
const chromeProfileRoot = process.env.SFORUM_CHROME_PROFILE_ROOT || ''
const WIDTH = 390
const HEIGHT = 844
const DPR = 3

const failures = []
const evidence = []

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
  const root = chromeProfileRoot || '/tmp'
  mkdirSync(root, { recursive: true })
  const profile = join(root, `sforum-qa-cdp-${Date.now()}`)
  mkdirSync(profile, { recursive: true })
  const child = spawn(binary, [
    '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--hide-scrollbars',
    // 本机请求直连，别走系统代理。
    '--no-proxy-server', 'about:blank'
  ], { stdio: 'ignore' })
  for (let attempt = 0; attempt < 150; attempt += 1) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/version`)
      if (response.ok) return { child, port, version: await response.json(), profile }
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
  const requests = []
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
    if (message.method === 'Network.requestWillBeSent') {
      requests.push({ url: message.params.request.url, method: message.params.request.method })
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
  return { socket, send, consoleMessages, requests, targetId: target.id }
}

const measure = `(() => {
  const visible = element => {
    const style = getComputedStyle(element)
    const box = element.getBoundingClientRect()
    return style.display !== 'none' && style.visibility !== 'hidden' && box.width > 0 && box.height > 0
  }
  // 表格/工具条自带横向滚动容器时，控件在容器外沿被裁是预期行为；
  // 真正要断言的是「没有任何控件被**页面**裁掉」。
  const inScrollContainer = element => {
    for (let node = element.parentElement; node && node !== document.body; node = node.parentElement) {
      const style = getComputedStyle(node)
      if (/(auto|scroll)/.test(style.overflowX) && node.scrollWidth > node.clientWidth + 1) return true
    }
    return false
  }
  const clippedAll = [...document.querySelectorAll('button, a, input, select, textarea, [role="button"]')]
    .filter(visible)
    .filter(element => { const box = element.getBoundingClientRect(); return box.right > innerWidth + 1 || box.left < -1 })
  const clipped = clippedAll.filter(element => !inScrollContainer(element))
  const row = document.querySelector('[data-testid="badge-backfill-progress"]')
  const rowBox = row ? row.getBoundingClientRect() : null
  const refresh = row ? row.querySelector('button') : null
  const search = document.querySelector('input[placeholder]')
  const searchBox = search ? search.getBoundingClientRect() : null
  const alert = row ? row.querySelector('[role="alert"], .rounded-md, [class*="rounded"]') : null
  return {
    url: location.pathname,
    innerWidth, innerHeight, dpr: devicePixelRatio,
    horizontalOverflow: document.documentElement.scrollWidth > innerWidth,
    scrollWidth: document.documentElement.scrollWidth,
    clippedControls: clipped.length,
    clippedSample: clipped.slice(0, 3).map(element => element.tagName + '.' + (element.className || '').toString().slice(0, 40)),
    clippedInScrollContainer: clippedAll.length - clipped.length,
    rowPresent: !!row,
    rowText: row ? row.innerText : '',
    rowBox: rowBox ? { top: Math.round(rowBox.top), left: Math.round(rowBox.left), width: Math.round(rowBox.width), height: Math.round(rowBox.height), right: Math.round(rowBox.right) } : null,
    rowClientWidth: row ? row.clientWidth : 0,
    rowScrollWidth: row ? row.scrollWidth : 0,
    rowAboveFilters: !!(rowBox && searchBox && rowBox.bottom <= searchBox.top),
    rowIsSingleLine: rowBox ? rowBox.height <= 96 : false,
    alertHeight: alert ? Math.round(alert.getBoundingClientRect().height) : 0,
    refresh: refresh ? { ariaLabel: refresh.getAttribute('aria-label') || '', title: refresh.getAttribute('title') || '', width: Math.round(refresh.getBoundingClientRect().width) } : null,
    bodyText: document.body.innerText.slice(0, 160)
  }
})()`

async function waitForRow(client, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const result = await client.send('Runtime.evaluate', {
      expression: '!!document.querySelector(\'[data-testid="badge-backfill-progress"]\')', returnByValue: true
    })
    if (result.result.value) return true
    await new Promise(resolve => setTimeout(resolve, 500))
  }
  return false
}

async function main() {
  if (!session) {
    console.log('SKIP set SFORUM_SESSION_COOKIE to run the badges 390 pass')
    finish()
    return
  }
  const binary = chromeBinary()
  if (!binary) {
    console.log('SKIP no Chrome/Chromium binary found')
    finish()
    return
  }
  const { child, port, version, profile } = await launchChrome(binary)
  const client = await connect(port)
  evidence.push({ browser: version.Browser, viewport: `${WIDTH}x${HEIGHT} dpr ${DPR}`, path: adminPath, state, profile })
  try {
    await client.send('Network.setCookie', {
      name: 'sforum_session', value: session, domain: '127.0.0.1', path: '/', httpOnly: true, sameSite: 'Lax'
    })
    await client.send('Page.navigate', { url: `${web}${adminPath}` })
    await new Promise(resolve => {
      const timer = setTimeout(resolve, 25000)
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
    await new Promise(resolve => setTimeout(resolve, 1500))
    const rowAppeared = await waitForRow(client, 20000)

    const before = client.requests.filter(entry => entry.url.includes('/backfill-status')).length
    const clickResult = await client.send('Runtime.evaluate', {
      expression: `(() => {
        const row = document.querySelector('[data-testid="badge-backfill-progress"]')
        const button = row ? row.querySelector('button') : null
        if (!button) return 'no-button'
        button.click()
        return 'clicked'
      })()`,
      returnByValue: true
    })
    await new Promise(resolve => setTimeout(resolve, 2000))
    const after = client.requests.filter(entry => entry.url.includes('/backfill-status')).length

    const result = await client.send('Runtime.evaluate', { expression: measure, returnByValue: true, awaitPromise: true })
    const measurement = result.result.value
    const consoleErrors = client.consoleMessages.filter(message => message.type === 'error' || message.type === 'exception')
    evidence.push({
      ...measurement,
      state,
      rowAppeared,
      refreshClick: clickResult.result.value,
      backfillRequestsBefore: before,
      backfillRequestsAfter: after,
      backfillRequestsDelta: after - before,
      consoleErrors
    })
    if (measurement.innerWidth !== WIDTH) fail('viewport override did not apply', `innerWidth=${measurement.innerWidth}`)
    if (!rowAppeared) fail('backfill progress row did not render', JSON.stringify(measurement).slice(0, 300))
    if (measurement.horizontalOverflow) fail('horizontal overflow', `scrollWidth=${measurement.scrollWidth}`)
    if (measurement.clippedControls !== 0) fail('clipped controls', JSON.stringify(measurement.clippedSample))
    if (measurement.rowPresent && !measurement.rowAboveFilters) fail('progress row is not above the filter row')
    if (measurement.rowPresent && measurement.rowScrollWidth > measurement.rowClientWidth) fail('progress row overflows horizontally', `${measurement.rowScrollWidth}>${measurement.rowClientWidth}`)
    if (measurement.rowPresent && !measurement.rowIsSingleLine) fail('progress row taller than one line', `height=${measurement.rowBox?.height}`)
    if (measurement.rowPresent && !measurement.refresh?.ariaLabel) fail('refresh button missing aria-label')
    if (measurement.rowPresent && !measurement.refresh?.title) fail('refresh button missing title')
    if (measurement.rowPresent && after - before < 1) fail('refresh click did not issue a backfill-status request', `before=${before} after=${after}`)
    if (consoleErrors.length > 0) fail('console errors', JSON.stringify(consoleErrors.slice(0, 3)))
    if (qaDir) {
      mkdirSync(qaDir, { recursive: true })
      const shot = await client.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false })
      const file = join(qaDir, `badge-backfill-390x844-${state}.png`)
      writeFileSync(file, Buffer.from(shot.data, 'base64'))
      evidence[evidence.length - 1].screenshot = file
      if (measurement.rowPresent && measurement.rowBox) {
        const clip = await client.send('Page.captureScreenshot', {
          format: 'png',
          clip: {
            x: Math.max(0, measurement.rowBox.left),
            y: Math.max(0, measurement.rowBox.top),
            width: Math.min(WIDTH, Math.max(1, measurement.rowBox.width)),
            height: Math.max(1, measurement.rowBox.height),
            scale: 1
          }
        })
        const rowFile = join(qaDir, `badge-backfill-row-390x844-${state}.png`)
        writeFileSync(rowFile, Buffer.from(clip.data, 'base64'))
        evidence[evidence.length - 1].rowScreenshot = rowFile
      }
    }
  } finally {
    client.socket.close()
    child.kill('SIGKILL')
  }
  finish()
}

function finish() {
  if (outputPath) writeFileSync(outputPath, `${JSON.stringify({ web, adminPath, state, failures, evidence }, null, 2)}\n`, 'utf8')
  for (const entry of evidence) {
    if (entry.browser) {
      console.log(`  browser: ${entry.browser} viewport=${entry.viewport} state=${entry.state}`)
      continue
    }
    console.log(`  ${entry.url}: ${entry.innerWidth}x${entry.innerHeight} row=${entry.rowPresent} overflow=${entry.horizontalOverflow} ` +
      `clipped=${entry.clippedControls} aboveFilters=${entry.rowAboveFilters} singleLine=${entry.rowIsSingleLine} ` +
      `refreshRequests=${entry.backfillRequestsDelta} text=${JSON.stringify(entry.rowText)}`)
  }
  if (failures.length > 0) {
    for (const failure of failures) console.error(`  FAIL ${failure}`)
    process.exitCode = 1
    return
  }
  console.log(`  all badges 390x844 assertions passed (state=${state})`)
}

try {
  await main()
} catch (error) {
  fail('harness error', String(error?.message || error))
  finish()
}
