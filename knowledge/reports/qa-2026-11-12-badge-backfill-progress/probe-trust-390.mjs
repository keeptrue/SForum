// 390x844 视觉验收探针：扩展启用弹窗里的「能力披露段」。
//
// 与 tests/browser-admin-390x844.mjs 同一 CDP 手法（真实 Chrome + device metrics +
// Network.setCookie）。只读页面，不改应用代码。
//
// 用法：
//   SFORUM_SESSION_COOKIE=<value> SFORUM_QA_DIR=<dir> node probe-trust-390.mjs
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const web = process.env.SFORUM_WEB_BASE_URL || 'http://127.0.0.1:3000'
const adminPath = process.env.SFORUM_ADMIN_PATH || '/control-panel/extensions'
const session = process.env.SFORUM_SESSION_COOKIE || ''
const qaDir = process.env.SFORUM_QA_DIR || ''
const outputPath = process.env.SFORUM_EVIDENCE_OUTPUT || ''
const profileRoot = process.env.SFORUM_CHROME_PROFILE_ROOT || '/tmp'
const rowMatch = process.env.SFORUM_EXTENSION_MATCH || 'sforum.gamification'
const WIDTH = 390
const HEIGHT = 844
const DPR = 3

const failures = []
const evidence = []

function fail(message, detail) {
  failures.push(detail ? `${message} :: ${detail}` : message)
}

async function main() {
  if (!session) {
    console.log('SKIP set SFORUM_SESSION_COOKIE to run the trust-impact 390 pass')
    finish()
    return
  }
  const binary = ['/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/opt/google/chrome/chrome', '/usr/bin/chromium'].find(existsSync)
  if (!binary) {
    console.log('SKIP no Chrome/Chromium binary found')
    finish()
    return
  }
  const port = 9800 + Math.floor(Math.random() * 300)
  mkdirSync(profileRoot, { recursive: true })
  const profile = join(profileRoot, `sforum-qa-trust-${Date.now()}`)
  mkdirSync(profile, { recursive: true })
  const child = spawn(binary, [
    '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--hide-scrollbars',
    '--no-proxy-server', 'about:blank'
  ], { stdio: 'ignore' })
  let version = null
  for (let attempt = 0; attempt < 150; attempt += 1) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/version`)
      if (response.ok) { version = await response.json(); break }
    } catch { /* 等待就绪 */ }
    await new Promise(resolve => setTimeout(resolve, 100))
  }
  if (!version) throw new Error('headless Chrome did not expose CDP')
  const target = await (await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: 'PUT' })).json()
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
    if (message.method === 'Runtime.consoleAPICalled' && (message.params.type === 'error' || message.params.type === 'warning')) {
      consoleMessages.push({ type: message.params.type, text: (message.params.args || []).map(a => a.value ?? a.description ?? '').join(' ') })
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
  await send('Emulation.setDeviceMetricsOverride', { width: WIDTH, height: HEIGHT, deviceScaleFactor: DPR, mobile: true })
  evidence.push({ browser: version.Browser, viewport: `${WIDTH}x${HEIGHT} dpr ${DPR}`, path: adminPath, extension: rowMatch })
  try {
    await send('Network.setCookie', { name: 'sforum_session', value: session, domain: '127.0.0.1', path: '/', httpOnly: true, sameSite: 'Lax' })
    await send('Page.navigate', { url: `${web}${adminPath}` })
    await new Promise(resolve => {
      const timer = setTimeout(resolve, 25000)
      const onMessage = event => {
        const message = JSON.parse(event.data)
        if (message.method === 'Page.loadEventFired') {
          clearTimeout(timer)
          socket.removeEventListener('message', onMessage)
          resolve()
        }
      }
      socket.addEventListener('message', onMessage)
    })
    await new Promise(resolve => setTimeout(resolve, 4000))
    // 列表分页：先用搜索框过滤到目标扩展。
    await send('Runtime.evaluate', {
      expression: `(() => {
        const input = document.querySelector('input[type="search"], input[placeholder]')
        if (!input) return 'no-input'
        const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set
        setter.call(input, ${JSON.stringify(rowMatch)})
        input.dispatchEvent(new Event('input', { bubbles: true }))
        return 'typed'
      })()`, returnByValue: true
    })
    await new Promise(resolve => setTimeout(resolve, 2500))
    // 找到目标扩展行，点它的「启用」按钮。
    const click = await send('Runtime.evaluate', {
      expression: `(() => {
        const match = ${JSON.stringify(rowMatch)}
        const rows = [...document.querySelectorAll('li, article, tr, .rounded-lg, [data-testid]')]
          .filter(node => node.innerText && node.innerText.toLowerCase().includes(match.toLowerCase()))
        const row = rows.sort((a, b) => a.innerText.length - b.innerText.length)[0]
        if (!row) return { ok: false, reason: 'row-not-found', sample: document.body.innerText.slice(0, 200) }
        const buttons = [...row.querySelectorAll('button')]
        const enable = buttons.find(button => /启用|enable/i.test(button.innerText))
        if (!enable) return { ok: false, reason: 'enable-button-not-found', buttons: buttons.map(button => button.innerText.trim()).slice(0, 8) }
        enable.click()
        return { ok: true, button: enable.innerText.trim() }
      })()`,
      returnByValue: true
    })
    evidence[evidence.length - 1].click = click.result.value
    let section = null
    for (let attempt = 0; attempt < 60; attempt += 1) {
      const probe = await send('Runtime.evaluate', {
        expression: `(() => {
          const section = document.querySelector('[data-testid="extension-capabilities"]')
          return section ? { ready: true } : { ready: false, body: document.body.innerText.slice(0, 120) }
        })()`,
        returnByValue: true
      })
      if (probe.result.value.ready) { section = probe.result.value; break }
      await new Promise(resolve => setTimeout(resolve, 500))
    }
    const measurement = await send('Runtime.evaluate', {
      expression: `(() => {
        const section = document.querySelector('[data-testid="extension-capabilities"]')
        const items = section ? [...section.querySelectorAll('li')] : []
        const sbox = section ? section.getBoundingClientRect() : null
        return {
          innerWidth, innerHeight,
          horizontalOverflow: document.documentElement.scrollWidth > innerWidth,
          scrollWidth: document.documentElement.scrollWidth,
          sectionPresent: !!section,
          sectionBox: sbox ? { top: Math.round(sbox.top), left: Math.round(sbox.left), right: Math.round(sbox.right), width: Math.round(sbox.width), height: Math.round(sbox.height) } : null,
          sectionScrollWidth: section ? section.scrollWidth : 0,
          sectionClientWidth: section ? section.clientWidth : 0,
          capabilityCount: items.length,
          capabilityTexts: items.map(item => item.innerText.replace(/\\n+/g, ' | ')),
          disclosures: section ? [...section.querySelectorAll('[data-capability-disclosure]')].map(node => node.innerText) : [],
          hasBadgesFactWrite: section ? section.innerText.includes('badges.fact.write') : false,
          dialogBody: (document.querySelector('[role="dialog"]') || document.body).innerText.slice(0, 400)
        }
      })()`,
      returnByValue: true
    })
    const value = measurement.result.value
    evidence.push({ ...value, consoleErrors: consoleMessages.filter(entry => entry.type === 'exception') })
    if (value.innerWidth !== WIDTH) fail('viewport override did not apply', `innerWidth=${value.innerWidth}`)
    if (!value.sectionPresent) fail('capabilities section did not render', JSON.stringify(value.dialogBody).slice(0, 200))
    if (value.horizontalOverflow) fail('horizontal overflow', `scrollWidth=${value.scrollWidth}`)
    if (value.sectionPresent && (value.sectionScrollWidth > value.sectionClientWidth || value.sectionBox.right > value.innerWidth + 1)) {
      fail('capabilities section overflows', `${value.sectionScrollWidth}>${value.sectionClientWidth} right=${value.sectionBox?.right}`)
    }
    if (qaDir) {
      mkdirSync(qaDir, { recursive: true })
      const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false })
      const file = join(qaDir, 'extension-capabilities-390x844.png')
      writeFileSync(file, Buffer.from(shot.data, 'base64'))
      evidence[evidence.length - 1].screenshot = file
      if (value.sectionBox) {
        const clip = await send('Page.captureScreenshot', {
          format: 'png',
          clip: {
            x: Math.max(0, value.sectionBox.left - 8),
            y: Math.max(0, value.sectionBox.top - 8),
            width: Math.min(WIDTH, value.sectionBox.width + 16),
            height: Math.max(1, value.sectionBox.height + 16),
            scale: 1
          }
        })
        const sectionFile = join(qaDir, 'extension-capabilities-section-390x844.png')
        writeFileSync(sectionFile, Buffer.from(clip.data, 'base64'))
        evidence[evidence.length - 1].sectionScreenshot = sectionFile
      }
    }
  } finally {
    socket.close()
    child.kill('SIGKILL')
  }
  finish()
}

function finish() {
  if (outputPath) writeFileSync(outputPath, `${JSON.stringify({ failures, evidence }, null, 2)}\n`, 'utf8')
  for (const entry of evidence) {
    if (entry.browser) { console.log(`  browser: ${entry.browser} viewport=${entry.viewport} ext=${entry.extension}`); continue }
    console.log(`  section=${entry.sectionPresent} caps=${entry.capabilityCount} overflow=${entry.horizontalOverflow} badgesFactWrite=${entry.hasBadgesFactWrite}`)
    console.log(`  texts=${JSON.stringify(entry.capabilityTexts)}`)
    console.log(`  disclosures=${JSON.stringify(entry.disclosures)}`)
  }
  if (failures.length > 0) {
    for (const failure of failures) console.error(`  FAIL ${failure}`)
    process.exitCode = 1
    return
  }
  console.log('  capabilities section 390x844 assertions passed')
}

try {
  await main()
} catch (error) {
  fail('harness error', String(error?.message || error))
  finish()
}
