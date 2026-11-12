// 一次性调试：为什么 390 验收页看不到徽章列表与回扫进度行。
// 只读：打印页面内 fetch 结果与 Nuxt 状态键，不改任何文件。
import { spawn } from 'node:child_process'
import { mkdirSync } from 'node:fs'

const web = 'http://127.0.0.1:3000'
const path = process.env.SFORUM_ADMIN_PATH || '/control-panel/badges?tab=badges'
const session = process.env.SFORUM_SESSION_COOKIE || ''
const port = 9300 + Math.floor(Math.random() * 200)
const profile = `/tmp/sforum-debug-cdp-${Date.now()}`
mkdirSync(profile, { recursive: true })
const child = spawn('/usr/bin/google-chrome', [
  '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
  '--no-proxy-server', '--no-first-run', '--default-browser-check', '--disable-gpu', 'about:blank'
], { stdio: 'ignore' })

let version = null
for (let attempt = 0; attempt < 100; attempt += 1) {
  try {
    const response = await fetch(`http://127.0.0.1:${port}/json/version`)
    if (response.ok) { version = await response.json(); break }
  } catch {}
  await new Promise(resolve => setTimeout(resolve, 100))
}
const target = await (await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: 'PUT' })).json()
const socket = new WebSocket(target.webSocketDebuggerUrl)
await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, { once: true }); socket.addEventListener('error', reject, { once: true }) })
let nextId = 1
const pending = new Map()
const requests = []
const consoleErrors = []
socket.addEventListener('message', event => {
  const message = JSON.parse(event.data)
  if (message.id && pending.has(message.id)) {
    const { resolve, reject } = pending.get(message.id)
    pending.delete(message.id)
    message.error ? reject(new Error(message.error.message)) : resolve(message.result)
    return
  }
  if (message.method === 'Network.responseReceived' && message.params.response.url.includes('/api/')) {
    requests.push({ url: message.params.response.url, status: message.params.response.status })
  }
  if (message.method === 'Runtime.exceptionThrown') consoleErrors.push(message.params.exceptionDetails?.text || 'exception')
  if (message.method === 'Runtime.consoleAPICalled' && message.params.type === 'error') {
    consoleErrors.push((message.params.args || []).map(a => a.value ?? a.description ?? '').join(' '))
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
await send('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 3, mobile: true })
await send('Network.setCookie', { name: 'sforum_session', value: session, domain: '127.0.0.1', path: '/', httpOnly: true, sameSite: 'Lax' })
await send('Page.navigate', { url: `${web}${path}` })
await new Promise(resolve => setTimeout(resolve, 5000))

const probe = await send('Runtime.evaluate', {
  expression: `(async () => {
    const list = await fetch('/api/v1/admin/badges?limit=5', { credentials: 'include' })
    const listBody = await list.json().catch(() => null)
    const backfill = await fetch('/api/v1/admin/badges/backfill-status', { credentials: 'include' })
    const backfillBody = await backfill.json().catch(() => null)
    return {
      title: document.title,
      url: location.pathname + location.search,
      cookieVisible: document.cookie,
      listStatus: list.status,
      listCount: listBody?.data?.items?.length ?? null,
      backfillStatus: backfill.status,
      backfillItems: backfillBody?.data?.items?.length ?? null,
      backfillJson: JSON.stringify(backfillBody).slice(0, 200),
      nuxtStateKeys: Object.keys(window.__NUXT__?.state ?? {}).slice(0, 20),
      body: document.body.innerText.slice(0, 160)
    }
  })()`,
  returnByValue: true,
  awaitPromise: true
})
console.log(JSON.stringify({ version: version?.Browser, requests, consoleErrors, probe: probe.result.value }, null, 2))
socket.close()
child.kill('SIGKILL')
