// 390x844 视觉验收：走产品「上传安装」路径，触发可执行扩展的信任弹窗。
//
// 不用任何直接改库捷径：真实 zip 制品 → 安装面板文件输入 → 信任评审 →
// `data-testid="extension-capabilities"` 段 + `[data-capability-disclosure]` 行。
//
// 用法：
//   SFORUM_SESSION_COOKIE=… SFORUM_ARCHIVE=<zip> SFORUM_QA_DIR=<dir> node probe-install-390.mjs
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const web = process.env.SFORUM_WEB_BASE_URL || 'http://127.0.0.1:3000'
const adminPath = process.env.SFORUM_ADMIN_PATH || '/control-panel/extensions/install'
const session = process.env.SFORUM_SESSION_COOKIE || ''
const archive = process.env.SFORUM_ARCHIVE || ''
const qaDir = process.env.SFORUM_QA_DIR || ''
const outputPath = process.env.SFORUM_EVIDENCE_OUTPUT || ''
const profileRoot = process.env.SFORUM_CHROME_PROFILE_ROOT || '/tmp'
const confirmInstall = process.env.SFORUM_CONFIRM_INSTALL === '1'
const WIDTH = 390
const HEIGHT = 844
const DPR = 3

const failures = []
const evidence = []
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))

function fail(message, detail) {
  failures.push(detail ? `${message} :: ${detail}` : message)
}

async function main() {
  if (!session || !archive) {
    console.log('SKIP need SFORUM_SESSION_COOKIE and SFORUM_ARCHIVE')
    return finish()
  }
  const binary = ['/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/opt/google/chrome/chrome', '/usr/bin/chromium'].find(existsSync)
  if (!binary) {
    console.log('SKIP no Chrome/Chromium binary found')
    return finish()
  }
  const port = 9800 + Math.floor(Math.random() * 300)
  mkdirSync(profileRoot, { recursive: true })
  const profile = join(profileRoot, `sforum-qa-install-${Date.now()}`)
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
    if (message.method === 'Runtime.exceptionThrown') {
      consoleMessages.push({ type: 'exception', text: message.params.exceptionDetails?.exception?.description || message.params.exceptionDetails?.text || 'exception' })
    }
    if (message.method === 'Network.responseReceived' && message.params.response.url.includes('/api/')) {
      requests.push({ status: message.params.response.status, url: message.params.response.url })
    }
  })
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const id = nextId++
    pending.set(id, { resolve, reject })
    socket.send(JSON.stringify({ id, method, params }))
  })
  const evaluate = async expression => {
    const result = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })
    if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description || 'evaluate failed')
    return result.result.value
  }
  await send('Page.enable')
  await send('Runtime.enable')
  await send('Network.enable')
  await send('DOM.enable')
  await send('Emulation.setDeviceMetricsOverride', { width: WIDTH, height: HEIGHT, deviceScaleFactor: DPR, mobile: true })
  evidence.push({ browser: version.Browser, viewport: `${WIDTH}x${HEIGHT} dpr ${DPR}`, path: adminPath, archive })
  try {
    await send('Network.setCookie', { name: 'sforum_session', value: session, domain: '127.0.0.1', path: '/', httpOnly: true, sameSite: 'Lax' })
    await send('Page.navigate', { url: `${web}${adminPath}` })
    await sleep(7000)
    // 文件输入是隐藏的，只能经 CDP 直接塞路径。
    const document = await send('DOM.getDocument', { depth: -1 })
    const query = await send('DOM.querySelector', { nodeId: document.root.nodeId, selector: '[data-testid="extension-archive-input"]' })
    if (!query.nodeId) throw new Error('archive file input not found')
    await send('DOM.setFileInputFiles', { files: [archive], nodeId: query.nodeId })
    await sleep(1500)
    const submitted = await evaluate(`(() => {
      const button = document.querySelector('[data-testid="extension-archive-submit"]')
      if (!button) return 'no-submit'
      button.click()
      return 'clicked'
    })()`)
    evidence[evidence.length - 1].submitted = submitted
    // 等信任评审段出现（安装 → requiresTrust → trust-review）。
    let sectionSeen = false
    let states = []
    for (let attempt = 0; attempt < 90; attempt += 1) {
      const snapshot = await evaluate(`(() => {
        const dialog = document.querySelector('[data-testid="admin-page-install-dialog"]')
        const section = document.querySelector('[data-testid="extension-capabilities"]')
        return {
          dialog: !!dialog,
          dialogText: dialog ? dialog.innerText.slice(0, 260) : '',
          section: !!section,
          buttons: dialog ? [...dialog.querySelectorAll('button')].map(b => b.innerText.trim()).filter(Boolean) : []
        }
      })()`)
      if (attempt % 6 === 0 || snapshot.section) states.push(snapshot)
      if (snapshot.section) { sectionSeen = true; break }
      await sleep(1000)
    }
    evidence.push({ states, sectionSeen })
    if (!sectionSeen) {
      fail('capabilities section did not render in the install trust review', JSON.stringify(states.slice(-2)).slice(0, 400))
    } else {
      const measurement = await evaluate(`(() => {
        const section = document.querySelector('[data-testid="extension-capabilities"]')
        const items = section ? [...section.querySelectorAll('li')] : []
        const sbox = section ? section.getBoundingClientRect() : null
        const disclosures = section ? [...section.querySelectorAll('[data-capability-disclosure]')].map(node => node.innerText.trim()) : []
        return {
          innerWidth, innerHeight,
          horizontalOverflow: document.documentElement.scrollWidth > innerWidth,
          scrollWidth: document.documentElement.scrollWidth,
          sectionBox: sbox ? { top: Math.round(sbox.top), left: Math.round(sbox.left), right: Math.round(sbox.right), width: Math.round(sbox.width), height: Math.round(sbox.height) } : null,
          sectionScrollWidth: section ? section.scrollWidth : 0,
          sectionClientWidth: section ? section.clientWidth : 0,
          capabilityCount: items.length,
          capabilityTexts: items.map(item => item.innerText.replace(/\\s*\\n\\s*/g, ' | ')),
          disclosures,
          dialogText: (document.querySelector('[data-testid="admin-page-install-dialog"]') || document.body).innerText.slice(0, 600)
        }
      })()`)
      evidence.push({ ...measurement, consoleErrors: consoleMessages })
      if (measurement.innerWidth !== WIDTH) fail('viewport override did not apply', `innerWidth=${measurement.innerWidth}`)
      if (measurement.horizontalOverflow) fail('horizontal overflow', `scrollWidth=${measurement.scrollWidth}`)
      if (measurement.sectionScrollWidth > measurement.sectionClientWidth) fail('capabilities section overflows', `${measurement.sectionScrollWidth}>${measurement.sectionClientWidth}`)
      if (!measurement.capabilityTexts.some(text => text.includes('badges.fact.write'))) fail('badges.fact.write row missing')
      if (!measurement.disclosures.some(text => text.length > 0)) fail('capability disclosure sentence missing')
      if (qaDir) {
        mkdirSync(qaDir, { recursive: true })
        const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false })
        const file = join(qaDir, 'extension-trust-install-390x844.png')
        writeFileSync(file, Buffer.from(shot.data, 'base64'))
        evidence[evidence.length - 1].screenshot = file
        if (measurement.sectionBox) {
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
          const sectionFile = join(qaDir, 'extension-capabilities-390x844.png')
          writeFileSync(sectionFile, Buffer.from(clip.data, 'base64'))
          evidence[evidence.length - 1].sectionScreenshot = sectionFile
        }
      }
      if (confirmInstall) {
        const outcome = await evaluate(`(async () => {
          const dialog = document.querySelector('[data-testid="admin-page-install-dialog"]')
          if (!dialog) return { confirmed: false, reason: 'no-dialog' }
          const buttons = [...dialog.querySelectorAll('button')]
          const confirm = buttons.find(button => /确认|继续|我了解|安装|confirm|install/i.test(button.innerText) && !button.disabled)
          if (!confirm) return { confirmed: false, reason: 'no-confirm', buttons: buttons.map(button => button.innerText.trim()) }
          confirm.click()
          return { confirmed: true, label: confirm.innerText.trim() }
        })()`)
        evidence[evidence.length - 1].confirm = outcome
        await sleep(6000)
        evidence[evidence.length - 1].afterConfirmText = await evaluate(`(document.querySelector('[data-testid="admin-page-install-dialog"]') || document.body).innerText.slice(0, 500)`)
        evidence[evidence.length - 1].apiCalls = requests.filter(entry => entry.url.includes('/admin/extensions')).slice(-8)
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
    if (entry.states) { console.log(`  sectionSeen=${entry.sectionSeen} states=${JSON.stringify(entry.states.slice(-2)).slice(0, 300)}`); continue }
    console.log(`  caps=${entry.capabilityCount} overflow=${entry.horizontalOverflow} disclosures=${JSON.stringify(entry.disclosures)} texts=${JSON.stringify(entry.capabilityTexts)}`)
  }
  if (failures.length > 0) {
    for (const failure of failures) console.error(`  FAIL ${failure}`)
    process.exitCode = 1
    return
  }
  console.log('  extension trust install 390x844 assertions passed')
}

try {
  await main()
} catch (error) {
  fail('harness error', String(error?.message || error))
  finish()
}
