// 390x844 视觉验收：已有扩展的「启用」信任弹窗里的能力披露段。
//
// 路径纯产品行为：上传安装（已 staged/installed）→ 插件列表点「启用」→
// 信任评审弹窗 →`data-testid="extension-capabilities"` + `[data-capability-disclosure]`。
// 不做任何直接改库造证据。
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const web = process.env.SFORUM_WEB_BASE_URL || 'http://127.0.0.1:3000'
const adminPath = process.env.SFORUM_ADMIN_PATH || '/control-panel/extensions/plugins'
const session = process.env.SFORUM_SESSION_COOKIE || ''
const qaDir = process.env.SFORUM_QA_DIR || ''
const outputPath = process.env.SFORUM_EVIDENCE_OUTPUT || ''
const profileRoot = process.env.SFORUM_CHROME_PROFILE_ROOT || '/tmp'
const rowMatch = process.env.SFORUM_EXTENSION_MATCH || 'SForum Query Surface Reference'
const enableIt = process.env.SFORUM_ENABLE === '1'
const WIDTH = 390
const HEIGHT = 844
const DPR = 3
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))
const failures = []
const evidence = []

function fail(message, detail) {
  failures.push(detail ? `${message} :: ${detail}` : message)
}

async function main() {
  if (!session) { console.log('SKIP need SFORUM_SESSION_COOKIE'); return finish() }
  const binary = ['/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/opt/google/chrome/chrome', '/usr/bin/chromium'].find(existsSync)
  if (!binary) { console.log('SKIP no Chrome'); return finish() }
  const port = 9800 + Math.floor(Math.random() * 300)
  mkdirSync(profileRoot, { recursive: true })
  const profile = join(profileRoot, `sforum-qa-enable-${Date.now()}`)
  mkdirSync(profile, { recursive: true })
  const child = spawn(binary, [
    '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--hide-scrollbars', '--no-proxy-server', 'about:blank'
  ], { stdio: 'ignore' })
  let version = null
  for (let attempt = 0; attempt < 150; attempt += 1) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/version`)
      if (response.ok) { version = await response.json(); break }
    } catch { /* 等待就绪 */ }
    await sleep(100)
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
  const exceptions = []
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data)
    if (message.id && pending.has(message.id)) {
      const { resolve, reject } = pending.get(message.id)
      pending.delete(message.id)
      if (message.error) reject(new Error(message.error.message))
      else resolve(message.result)
      return
    }
    if (message.method === 'Runtime.exceptionThrown') {
      exceptions.push((message.params.exceptionDetails?.exception?.description || message.params.exceptionDetails?.text || '').slice(0, 200))
    }
  })
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const id = nextId++
    pending.set(id, { resolve, reject })
    socket.send(JSON.stringify({ id, method, params }))
  })
  const evaluate = async expression => {
    const result = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })
    return result.result.value
  }
  await send('Page.enable')
  await send('Runtime.enable')
  await send('Emulation.setDeviceMetricsOverride', { width: WIDTH, height: HEIGHT, deviceScaleFactor: DPR, mobile: true })
  evidence.push({ browser: version.Browser, viewport: `${WIDTH}x${HEIGHT} dpr ${DPR}`, path: adminPath, match: rowMatch })
  try {
    await send('Network.setCookie', { name: 'sforum_session', value: session, domain: '127.0.0.1', path: '/', httpOnly: true, sameSite: 'Lax' })
    await send('Page.navigate', { url: `${web}${adminPath}` })
    await sleep(9000)
    // 列表可能分页：先按名称过滤。
    const typed = await evaluate(`(() => {
      const input = document.querySelector('input[type="search"], input[placeholder]')
      if (!input) return 'no-input'
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set
      setter.call(input, ${JSON.stringify('query-surface')})
      input.dispatchEvent(new Event('input', { bubbles: true }))
      return 'typed'
    })()`)
    await sleep(2500)
    const click = await evaluate(`(() => {
      const match = ${JSON.stringify(rowMatch)}
      const rows = [...document.querySelectorAll('li, article, tr, section, div')]
        .filter(node => node.innerText && node.innerText.includes(match) && node.querySelectorAll('button').length > 0)
      const row = rows.sort((a, b) => a.innerText.length - b.innerText.length)[0]
      if (!row) return { ok: false, reason: 'row-not-found', body: document.body.innerText.slice(0, 200) }
      const buttons = [...row.querySelectorAll('button')]
      const enable = buttons.find(button => /启用/.test(button.innerText)) || buttons.find(button => !button.disabled)
      if (!enable) return { ok: false, reason: 'no-button', buttons: buttons.map(b => b.innerText.trim()).slice(0, 6) }
      enable.click()
      return { ok: true, label: enable.innerText.trim(), buttons: buttons.map(b => b.innerText.trim()).slice(0, 6) }
    })()`)
    evidence[evidence.length - 1].typed = typed
    evidence[evidence.length - 1].click = click
    let seen = false
    for (let attempt = 0; attempt < 60; attempt += 1) {
      const ready = await evaluate('!!document.querySelector(\'[data-testid="extension-capabilities"]\')')
      if (ready) { seen = true; break }
      await sleep(1000)
    }
    evidence[evidence.length - 1].sectionSeen = seen
    if (!seen) {
      fail('capabilities section did not render', JSON.stringify({ click, body: await evaluate('document.body.innerText.slice(0, 400)') }).slice(0, 500))
    } else {
      const measurement = await evaluate(`(() => {
        const section = document.querySelector('[data-testid="extension-capabilities"]')
        const items = [...section.querySelectorAll('li')]
        const box = section.getBoundingClientRect()
        return {
          innerWidth, innerHeight,
          horizontalOverflow: document.documentElement.scrollWidth > innerWidth,
          scrollWidth: document.documentElement.scrollWidth,
          sectionBox: { top: Math.round(box.top), left: Math.round(box.left), right: Math.round(box.right), width: Math.round(box.width), height: Math.round(box.height) },
          sectionScrollWidth: section.scrollWidth,
          sectionClientWidth: section.clientWidth,
          capabilityTexts: items.map(item => item.innerText.replace(/\\s*\\n\\s*/g, ' | ')),
          disclosures: [...section.querySelectorAll('[data-capability-disclosure]')].map(node => node.innerText.trim()),
          dialogText: (document.querySelector('[role="dialog"]') || document.body).innerText.slice(0, 700)
        }
      })()`)
      evidence.push({ ...measurement, exceptions })
      if (measurement.innerWidth !== WIDTH) fail('viewport override did not apply', `innerWidth=${measurement.innerWidth}`)
      if (measurement.horizontalOverflow) fail('horizontal overflow', `scrollWidth=${measurement.scrollWidth}`)
      if (measurement.sectionScrollWidth > measurement.sectionClientWidth || measurement.sectionBox.right > WIDTH + 1) fail('capabilities section overflows', `${measurement.sectionScrollWidth}>${measurement.sectionClientWidth}`)
      if (!measurement.capabilityTexts.some(text => text.includes('badges.fact.write'))) fail('badges.fact.write row missing')
      if (!measurement.disclosures.some(text => text.length > 0)) fail('capability disclosure sentence missing')
      if (qaDir) {
        mkdirSync(qaDir, { recursive: true })
        const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false })
        writeFileSync(join(qaDir, 'extension-enable-trust-390x844.png'), Buffer.from(shot.data, 'base64'))
        evidence[evidence.length - 1].screenshot = join(qaDir, 'extension-enable-trust-390x844.png')
        const clip = await send('Page.captureScreenshot', {
          format: 'png',
          clip: {
            x: Math.max(0, measurement.sectionBox.left - 6),
            y: Math.max(0, measurement.sectionBox.top - 6),
            width: Math.min(WIDTH, measurement.sectionBox.width + 12),
            height: Math.max(1, measurement.sectionBox.height + 12),
            scale: 1
          }
        })
        writeFileSync(join(qaDir, 'extension-capabilities-390x844.png'), Buffer.from(clip.data, 'base64'))
        evidence[evidence.length - 1].sectionScreenshot = join(qaDir, 'extension-capabilities-390x844.png')
      }
      if (enableIt) {
        const outcome = await evaluate(`(() => {
          const dialog = document.querySelector('[role="dialog"]')
          if (!dialog) return { ok: false, reason: 'no-dialog' }
          const buttons = [...dialog.querySelectorAll('button')].filter(button => !button.disabled)
          const confirm = buttons.find(button => /确认|启用|我了解|授权/.test(button.innerText))
          if (!confirm) return { ok: false, reason: 'no-confirm', buttons: buttons.map(button => button.innerText.trim()) }
          confirm.click()
          return { ok: true, label: confirm.innerText.trim() }
        })()`)
        evidence[evidence.length - 1].confirm = outcome
        await sleep(8000)
        evidence[evidence.length - 1].afterConfirm = await evaluate('document.body.innerText.slice(0, 300)')
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
    if (entry.browser) { console.log(`  browser: ${entry.browser} viewport=${entry.viewport}`); continue }
    if (entry.capabilityTexts) {
      console.log(`  caps=${entry.capabilityTexts.length} overflow=${entry.horizontalOverflow} disclosures=${JSON.stringify(entry.disclosures)}`)
      console.log(`  texts=${JSON.stringify(entry.capabilityTexts)}`)
      if (entry.confirm) console.log(`  confirm=${JSON.stringify(entry.confirm)} after=${JSON.stringify((entry.afterConfirm || '').slice(0, 160))}`)
      continue
    }
    console.log(`  ${JSON.stringify(entry).slice(0, 300)}`)
  }
  if (failures.length > 0) {
    for (const failure of failures) console.error(`  FAIL ${failure}`)
    process.exitCode = 1
    return
  }
  console.log('  enable trust dialog 390x844 assertions passed')
}

try {
  await main()
} catch (error) {
  fail('harness error', String(error?.message || error))
  finish()
}
