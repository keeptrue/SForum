// post_risk 发帖人机验证的真机浏览器 QA（F-5 / task-13）。
//
// 为什么需要它：`apps/web/tests/**` 的 happy-dom 用例只能证明「状态机把字段拼对了」，
// 证明不了三件只有在真实浏览器 + 真实 API 下才成立的事：
//   1. 开关关闭时 `.sf-forum-altcha` 根本不在 DOM 里（不是渲染了但看不见）；
//   2. 开关打开时 ALTCHA 真实 web component 能加载、能解出一题，且卡片有非零盒子；
//   3. 真实 `POST /api/v1/topics` 的请求体里 humanVerification 键的有无。
//
// ALTCHA 解算在 **Node** 里做（`crypto.pbkdf2Sync`，cost=1000 约 200ms），不在浏览器里点
// 复选框：headless 下 widget 的 worker 路径会把 renderer 拖死（CDP socket 直接断）。解出的
// token 是密码学意义上真解出来的，只是解算器在 Node；随后交给页面里真实的 `@verified`
// 处理器，让真实 submitCurrentDraft 走完。
//
// 与 `tests/browser-public-390x844.mjs` / `tests/browser-admin-390x844.mjs` 同一套手法：
// 真实 Chrome + CDP（`Emulation.setDeviceMetricsOverride` 真的把视口变成 390×844）。
// 仓库没有 playwright 依赖（root 与 apps/web 都 `require.resolve` 不到），所以这里
// 沿用既有 CDP harness，脚本本身零依赖、可直接重跑。
//
// 用法（每种开关态各跑一次；开关值由调用方在数据库里改）：
//   SFORUM_POST_RISK_STATE=off SFORUM_SESSION_COOKIE=... node tests/browser-post-risk-composer.mjs
//   SFORUM_POST_RISK_STATE=on  SFORUM_SESSION_COOKIE=... node tests/browser-post-risk-composer.mjs
//
// 运行前提（本轮实测环境，缺一条都跑不起来）：
//   - web：`cd apps/web && PORT=3100 NUXT_API_INTERNAL_BASE_URL=http://127.0.0.1:8081/api/v1 bun run dev`
//   - API：`cd apps/api` 起在 8081（`EXTENSION_ROOT=../../storage/extensions` 是相对路径，必须
//     cwd=apps/api，否则会指到仓库外的 /home/<user>/storage/extensions）
//   - 本轮 API 以 `SFORUM_SAFE_MODE=1` + `EXTERNAL_EXTENSION_ROOTS=`（空）运行：**插件全部未加载**。
//     原因见交付说明——本机 storage/extensions 里 sforum-shortcodes 1.3.10 的已安装产物陈旧
//     （backend 目录里是 `backend` 而 manifest 声明 `backend/plugin`），API 会进 recoveryActive，
//     /api/v1/web-options 一律 503。post_risk 是核心能力，与插件无关，故用 safe mode 取证。
//   - 开关态由调用方在库里改：`web_options` 的 `human_verification.provider`（altcha|disabled）
//     与 `human_verification.scenarios.post_risk`（enabled|disabled）。改完立即生效，无需重启。
//
// 环境变量：
//   SFORUM_POST_RISK_STATE   必填：off | on（断言哪一侧）
//   SFORUM_SESSION_COOKIE    必填：登录会话 cookie 值（`sforum_session`）
//   SFORUM_WEB_BASE_URL      默认 http://127.0.0.1:3100
//   SFORUM_QA_DIR            截图目录，默认 storage/qa/post-risk（storage/ 已 gitignore）
//   SFORUM_EVIDENCE_OUTPUT   可选：JSON 报告落盘路径
//   SFORUM_SKIP_MOBILE=1     可选：跳过 390×844 那一段（ON 态在 headless 下容易把 renderer
//                            拖死，先单独拿桌面证据时用）
//   SFORUM_CHROME            可选：Chrome 可执行文件路径
import crypto from 'node:crypto'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const web = process.env.SFORUM_WEB_BASE_URL || 'http://127.0.0.1:3100'
const state = (process.env.SFORUM_POST_RISK_STATE || '').trim()
const sessionCookie = (process.env.SFORUM_SESSION_COOKIE || '').trim()
const qaDir = process.env.SFORUM_QA_DIR || 'storage/qa/post-risk'
const evidencePath = process.env.SFORUM_EVIDENCE_OUTPUT || ''

const DESKTOP = { width: 1440, height: 900, dpr: 1 }
const MOBILE = { width: 390, height: 844, dpr: 3 }
const TOPIC_PATH = '/topics/new'
const SUBMIT_LABEL = '发布'

const failures = []
function fail(message) {
  failures.push(message)
}
function assert(condition, message) {
  if (!condition) fail(message)
}

if (state !== 'on' && state !== 'off') {
  console.error('SFORUM_POST_RISK_STATE must be "on" or "off"')
  process.exit(2)
}
if (!sessionCookie) {
  console.error('SFORUM_SESSION_COOKIE is required (value of the sforum_session cookie)')
  process.exit(2)
}

function chromeBinary() {
  const explicit = process.env.SFORUM_CHROME
  if (explicit && existsSync(explicit)) return explicit
  for (const candidate of ['/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/opt/google/chrome/chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser']) {
    if (existsSync(candidate)) return candidate
  }
  return ''
}

async function launchChrome(binary) {
  const port = 9500 + Math.floor(Math.random() * 300)
  const profile = `/tmp/sforum-postrisk-cdp-${Date.now()}`
  mkdirSync(profile, { recursive: true })
  const child = spawn(binary, [
    '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--hide-scrollbars',
    '--disable-dev-shm-usage', 'about:blank'
  ], { stdio: 'ignore' })
  for (let attempt = 0; attempt < 150; attempt += 1) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/version`)
      if (response.ok) return { child, port }
    } catch {
      // 等 Chrome 起来
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
  const topicPosts = []
  const responses = new Map()
  const consoleErrors = []
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data)
    if (message.id && pending.has(message.id)) {
      const { resolve, reject } = pending.get(message.id)
      pending.delete(message.id)
      if (message.error) reject(new Error(message.error.message))
      else resolve(message.result)
      return
    }
    const params = message.params || {}
    if (message.method === 'Network.requestWillBeSent') {
      const { request, requestId } = params
      if (request?.method === 'POST' && /\/api\/v1\/topics(\?|$)/.test(request.url)) {
        topicPosts.push({ requestId, url: request.url, body: request.postData || '', hasPostData: Boolean(request.hasPostData) })
      }
    }
    if (message.method === 'Network.responseReceived' && responses.has(params.requestId)) {
      responses.set(params.requestId, params.response?.status ?? 0)
    }
    if (message.method === 'Runtime.consoleAPICalled' && params.type === 'error') {
      consoleErrors.push((params.args || []).map(argument => argument.value ?? argument.description ?? '').join(' ').slice(0, 200))
    }
    if (message.method === 'Runtime.exceptionThrown') {
      consoleErrors.push(`exception: ${params.exceptionDetails?.text || ''}`.slice(0, 200))
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
  // headless 下焦点不保证在页面上；不 bringToFront 时 Input.insertText 偶尔整段丢失。
  try { await send('Page.bringToFront') } catch { /* ignore */ }
  await send('Network.setCookie', {
    name: 'sforum_session', value: sessionCookie, domain: '127.0.0.1', path: '/', httpOnly: true, sameSite: 'Lax'
  })
  return { socket, send, topicPosts, responses, consoleErrors }
}

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))

/** 阶段级硬上限：真机上偶发 renderer 卡死，不能让一次挂起吞掉已经拿到的证据。 */
function withTimeout(promise, ms, label) {
  return Promise.race([
    promise,
    new Promise((_, reject) => setTimeout(() => reject(new Error(`${label} timed out after ${ms}ms`)), ms))
  ])
}

async function evaluate(send, expression) {
  const result = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true })
  if (result.exceptionDetails) {
    throw new Error(`evaluate failed: ${result.exceptionDetails.text} :: ${expression.slice(0, 80)}`)
  }
  return result.result?.value
}

async function waitFor(send, expression, label, timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (await evaluate(send, expression)) return true
    await sleep(150)
  }
  throw new Error(`timeout waiting for ${label}`)
}

async function setViewport(send, viewport) {
  await send('Emulation.setDeviceMetricsOverride', {
    width: viewport.width, height: viewport.height, deviceScaleFactor: viewport.dpr, mobile: viewport.width < 600
  })
}

async function gotoComposer(send, viewport) {
  console.error(`→ navigate ${web}${TOPIC_PATH} @ ${viewport.width}x${viewport.height}`)
  await setViewport(send, viewport)
  await send('Page.navigate', { url: `${web}${TOPIC_PATH}` })
  await waitFor(send, `document.readyState === 'complete'`, 'document complete')
  await waitFor(send, `Boolean(document.querySelector('#topic-composer-title-input'))`, 'composer title input')
  await waitFor(send, `[...document.querySelectorAll('button')].some(button => button.textContent.trim() === ${JSON.stringify(SUBMIT_LABEL)})`, 'submit button')
  // SSR 就有输入框，但水合可能迟到很久（dev 模式客户端 chunk 很大）。以 Vue 是否接管了这个
  // 节点为准：`__vueParentComponent` 只有在水合/挂载后才挂上。不等到它，后面打字只改 DOM
  // 值、组件状态仍是空，ALTCHA 的 ClientOnly 也还停在 fallback 上。
  await waitFor(
    send,
    `Boolean(document.querySelector('#topic-composer-title-input')?.__vueParentComponent)`,
    'vue hydration of the composer',
    180000
  )
}

async function screenshot(send, file) {
  mkdirSync(qaDir, { recursive: true })
  const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false })
  writeFileSync(file, Buffer.from(shot.data, 'base64'))
}

const VERIFICATION_PROBE = `(() => {
  const widget = document.querySelector('.sf-forum-altcha')
  const wrapper = document.querySelector('#topic-composer-human-verification')
  const label = document.querySelector('#topic-composer-human-verification-label')
  const visible = element => {
    if (!element) return false
    const style = getComputedStyle(element)
    const box = element.getBoundingClientRect()
    return style.display !== 'none' && style.visibility !== 'hidden' && box.width > 0 && box.height > 0
  }
  const box = widget ? widget.getBoundingClientRect() : null
  return {
    wrapperPresent: Boolean(wrapper),
    widgetPresent: Boolean(widget),
    widgetVisible: visible(widget),
    box: box ? { x: Math.round(box.x), y: Math.round(box.y), width: Math.round(box.width), height: Math.round(box.height) } : null,
    insideViewport: box ? (box.left >= -1 && box.top >= -1 && box.right <= innerWidth + 1 && box.bottom <= innerHeight + 1) : false,
    labelText: label ? label.textContent.trim() : '',
    labelVisible: visible(label),
    shadowRootOpen: Boolean(widget && widget.shadowRoot),
    payload: widget && typeof widget.payload === 'string' ? widget.payload.length : 0,
    widgetState: widget ? (widget.state || widget.getAttribute('state') || '') : '',
    innerWidth, innerHeight,
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
    dockPosition: (() => {
      const dock = document.querySelector('.sforum-topic-composer__dock')
      return dock ? getComputedStyle(dock).position : ''
    })()
  }
})()`

/**
 * 在 Node 里**真解**这道 ALTCHA 挑战，再把解出的 payload 交给页面里真实的 widget/组件。
 *
 * 为什么不在浏览器里点复选框：headless 下 widget 的 worker 路径会把 renderer 拖死
 * （CDP socket 直接断，`unsettled top-level await`）。解算本身与浏览器无关——服务端
 * `altcha-lib-go` 的 `VerifySolution` 要的是 `{challenge, solution:{counter, derivedKey}}`
 * 的 base64，Node 用 `crypto.pbkdf2Sync` 几百毫秒就能穷举出 counter（cost=1000）。
 *
 * 所以这里解出来的 token 是**密码学意义上真实解出的**，只是解算器在 Node 而不是 widget
 * 的 worker 里；随后把 token 交给真实的 `@verified` 处理器，让真实的 submitCurrentDraft
 * 走完。浏览器里拿到的那条请求体因此是真实链路产物。
 */
async function solveAltcha(send) {
  const response = await fetch(`${web}/api/v1/human-verification/challenge?purpose=post_risk`, {
    headers: { cookie: `sforum_session=${sessionCookie}` }
  })
  const body = await response.json()
  const challenge = body.parameters ? body : (body.data?.payload ?? body.data ?? body)
  const params = challenge.parameters
  if (!params) throw new Error(`unexpected challenge shape: ${JSON.stringify(body).slice(0, 200)}`)

  const nonceBuf = Buffer.from(params.nonce, 'hex')
  const saltBuf = Buffer.from(params.salt, 'hex')
  const prefixBytes = Math.floor(params.keyPrefix.length / 2)
  const started = Date.now()
  let counter = 0
  let derivedKey = null
  for (;;) {
    const counterBuf = Buffer.alloc(4)
    counterBuf.writeUInt32BE(counter, 0)
    const derived = crypto.pbkdf2Sync(Buffer.concat([nonceBuf, counterBuf]), saltBuf, params.cost, params.keyLength || 32, 'sha256')
    if (derived.subarray(0, prefixBytes).equals(Buffer.from(params.keyPrefix, 'hex'))) {
      derivedKey = derived
      break
    }
    counter += 1
    if (counter > 5_000_000) throw new Error('altcha: no solution within 5e6 counters')
  }
  const took = Date.now() - started
  const token = Buffer.from(JSON.stringify({
    challenge,
    solution: { counter, derivedKey: derivedKey.toString('hex'), time: took }
  })).toString('base64')

  // 交给页面里真实的 widget + 组件：组件监听 `verified` 事件读 detail.payload。
  await evaluate(send, `(() => {
    const widget = document.querySelector('.sf-forum-altcha')
    if (!widget) return 'no-widget'
    widget.payload = ${JSON.stringify(token)}
    widget.dispatchEvent(new CustomEvent('verified', { detail: { payload: ${JSON.stringify(token)} } }))
    return 'dispatched'
  })()`)
  await sleep(400)
  return { counter, took, tokenLength: token.length, token }
}

// SSR 已经渲染出输入框，但 Vue 的 `@input` 监听要等水合才挂上；没等水合就打字，值只落在
// DOM 上、组件状态还是空，提交按钮永远 disabled。真机上没有可靠的「水合完成」标记
// （Nuxt 4 的 `#__nuxt` 上没有 `__vue_app__`），所以这里以**组件状态**为准：打字后
// 反复核对标题字数计数器，计数器跟上才算输入真的进了 Vue。
async function typeUntilCounted(send, { selector, counterSelector, text, label, click = false }) {
  for (let attempt = 0; attempt < 40; attempt += 1) {
    if (attempt % 2 === 0) {
      if (click) {
        const point = await evaluate(send, `(() => {
          const el = document.querySelector(${JSON.stringify(selector)})
          if (!el) return null
          const rect = el.getBoundingClientRect()
          return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + Math.min(24, rect.height / 2)) }
        })()`)
        if (point) {
          await send('Input.dispatchMouseEvent', { type: 'mousePressed', x: point.x, y: point.y, button: 'left', clickCount: 1 })
          await send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: point.x, y: point.y, button: 'left', clickCount: 1 })
        }
      } else {
        await evaluate(send, `(() => {
          const el = document.querySelector(${JSON.stringify(selector)})
          if (!el) return false
          el.focus()
          return true
        })()`)
      }
    }
    if (attempt % 2 === 0) {
      await send('Input.insertText', { text })
    } else {
      // 备用路径：合成 InputEvent（Vue 的 @input 只读 event.target.value，不要求 isTrusted）。
      await evaluate(send, `(() => {
        const el = document.querySelector(${JSON.stringify(selector)})
        if (!el) return false
        el.focus()
        const value = ${JSON.stringify(text)}
        if (el.isContentEditable) {
          el.textContent = value
        } else {
          const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
          Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, value)
        }
        el.dispatchEvent(new InputEvent('input', { bubbles: true, data: value, inputType: 'insertText' }))
        return true
      })()`)
    }
    await sleep(250)
    // 只断言「输入真的进了组件状态」（计数器 > 0）。精确字数会受 runeLength 去空白、
    // 富文本序列化影响，用等值比较会把用例绑死在实现细节上。
    const count = await evaluate(send, `(() => {
      const node = document.querySelector(${JSON.stringify(counterSelector)})
      if (!node) return -1
      const matched = node.textContent.match(/(\\d+)\\s*\\//)
      return matched ? Number(matched[1]) : -1
    })()`)
    if (count > 0) return count
    await sleep(500)
  }
  throw new Error(`${label}: typed text never reached component state (hydration?)`)
}

async function fillComposer(send, title) {
  // 用真实按键事件（Input.insertText）而不是改 DOM 值：Tiptap 与 Vue 的 v-model 都只认
  // 真实输入事件，`el.value = x` 会让 UI 显示有值、组件状态仍是空。
  await typeUntilCounted(send, {
    selector: '#topic-composer-title-input',
    counterSelector: '#topic-composer-title-field',
    text: title,
    label: 'title'
  })

  // 编辑器是 LazySFEditor（异步 chunk），等它真正挂载再输入。
  await waitFor(send, `Boolean(document.querySelector('.ProseMirror'))`, 'tiptap editor', 30000)
  await typeUntilCounted(send, {
    selector: '.ProseMirror',
    counterSelector: '#topic-composer-body',
    text: `F-5 post_risk 真机取证 ${title}`,
    label: 'body',
    click: true
  })
  await waitFor(
    send,
    `(() => { const button = [...document.querySelectorAll('button')].find(item => item.textContent.trim() === ${JSON.stringify(SUBMIT_LABEL)}); return Boolean(button) && !button.disabled })()`,
    'enabled submit button',
    15000
  )
}

async function submitComposer(send) {
  await evaluate(send, `(() => {
    const button = [...document.querySelectorAll('button')].find(item => item.textContent.trim() === ${JSON.stringify(SUBMIT_LABEL)})
    if (!button) return 'missing'
    if (button.disabled) return 'disabled'
    button.click()
    return 'clicked'
  })()`).then(result => {
    assert(result === 'clicked', `submit button not clickable: ${result}`)
  })
}

function parseBody(raw) {
  try {
    return JSON.parse(raw)
  } catch {
    return null
  }
}

function maskToken(token) {
  if (typeof token !== 'string' || token.length === 0) return ''
  return `${token.slice(0, 6)}…${token.slice(-4)} (len=${token.length})`
}

const evidence = { state, web, topicPath: TOPIC_PATH, desktop: {}, mobile: {}, posts: [], screenshots: [], consoleErrors: [] }
let emitted = false

function emitEvidence(reason) {
  if (emitted) return
  emitted = true
  evidence.failures = failures
  evidence.abortReason = reason || null
  if (evidencePath) {
    mkdirSync(join(evidencePath, '..'), { recursive: true })
    writeFileSync(evidencePath, `${JSON.stringify(evidence, null, 2)}\n`)
  }
  const summary = {
    state,
    pass: failures.length === 0,
    abortReason: reason || null,
    failures,
    screenshots: evidence.screenshots,
    desktop: evidence.desktop,
    mobile: evidence.mobile,
    posts: evidence.posts.map(post => ({
      url: post.url,
      status: post.status,
      bodyKeys: post.bodyKeys,
      humanVerification: post.humanVerification
        ? { provider: post.humanVerification.provider, token: maskToken(post.humanVerification.token) }
        : null
    })),
    solve: evidence.solve || null,
    consoleErrors: evidence.consoleErrors
  }
  console.log(JSON.stringify(summary, null, 2))
}

async function run() {
  // 真机上 renderer 偶发卡死会让某个 CDP promise 永不 resolve；看门狗保证已拿到的证据不丢。
  const watchdog = setTimeout(() => {
    console.error('! watchdog fired; emitting partial evidence')
    emitEvidence('watchdog')
    process.exit(1)
  }, 330000)
  watchdog.unref?.()
  const binary = chromeBinary()
  if (!binary) throw new Error('no Chrome/Chromium binary found; set SFORUM_CHROME')
  const { child, port } = await launchChrome(binary)
  let send
  try {
    const connection = await connect(port)
    send = connection.send
    evidence.consoleErrors = connection.consoleErrors

    /* ── 桌面 ─────────────────────────────────────────────── */
    await gotoComposer(send, DESKTOP)
    if (state === 'on') {
      // ClientOnly 的客户端替换发生在上一次 tick 之后，ALTCHA 的 chunk 还要下载；
      // 直接探针会读到 SSR 的「正在加载人机验证…」fallback 并误判为「组件没渲染」。
      await waitFor(send, `Boolean(document.querySelector('.sf-forum-altcha'))`, 'altcha widget mount', 60000)
      await waitFor(send, `Boolean(customElements.get('altcha-widget'))`, 'altcha custom element definition', 30000)
      await evaluate(send, `(() => { document.querySelector('.sf-forum-altcha').scrollIntoView({ block: 'center' }); return true })()`)
      await sleep(400)
    }
    const desktopProbe = await evaluate(send, VERIFICATION_PROBE)
    evidence.desktop = desktopProbe

    if (state === 'off') {
      assert(desktopProbe.wrapperPresent === false, 'off: #topic-composer-human-verification must not be in the DOM')
      assert(desktopProbe.widgetPresent === false, 'off: .sf-forum-altcha must not be in the DOM')
    } else {
      assert(desktopProbe.wrapperPresent === true, 'on: #topic-composer-human-verification must be rendered')
      assert(desktopProbe.widgetPresent === true, 'on: .sf-forum-altcha must be rendered')
      assert(desktopProbe.widgetVisible === true, 'on: .sf-forum-altcha must be visible')
      assert(desktopProbe.box && desktopProbe.box.width > 0 && desktopProbe.box.height > 0, 'on: .sf-forum-altcha boundingBox must be non-zero')
      assert(desktopProbe.insideViewport === true, 'on: .sf-forum-altcha must sit inside the viewport')
      assert(desktopProbe.labelVisible === true, 'on: #topic-composer-human-verification-label must be visible')
      // ALTCHA 3.x 用 closed shadow root 也是合法实现，不能拿 open shadowRoot 当「真组件」判据。
      assert(await evaluate(send, `Boolean(customElements.get('altcha-widget'))`) === true, 'on: altcha-widget custom element must be defined')
    }

    const desktopShot = join(qaDir, `post-risk-${state}-desktop-1440x900.png`)
    await screenshot(send, desktopShot)
    evidence.screenshots.push(desktopShot)

    let solvedToken = ''
    if (state === 'on') {
      const solved = await solveAltcha(send)
      solvedToken = solved.token
      evidence.solve = { counter: solved.counter, tookMs: solved.took, tokenLength: solved.tokenLength, solver: 'node-pbkdf2' }
      evidence.desktop.payloadLength = await evaluate(send, `(() => { const w = document.querySelector('.sf-forum-altcha'); return w && typeof w.payload === 'string' ? w.payload.length : 0 })()`)
      assert(evidence.desktop.payloadLength > 0, 'on: the widget must hold the solved payload before submit')
    }

    console.error('→ verification probe done, filling composer')
    const title = `F5 ${state} ${Date.now().toString(36)}`
    await fillComposer(send, title)
    console.error('→ submitting')
    const before = evidence.posts.length
    await submitComposer(send)

    const deadline = Date.now() + 20000
    let posted = null
    while (Date.now() < deadline) {
      const posts = connection.topicPosts
      if (posts.length > before) { posted = posts[posts.length - 1]; break }
      await sleep(200)
    }
    assert(Boolean(posted), 'no real POST /api/v1/topics was observed')

    if (posted) {
      const body = parseBody(posted.body)
      evidence.posts.push({
        url: posted.url,
        bodyParsed: body !== null,
        bodyKeys: body ? Object.keys(body) : [],
        humanVerification: body && Object.prototype.hasOwnProperty.call(body, 'humanVerification') ? body.humanVerification : null,
        status: connection.responses.get(posted.requestId) ?? null
      })
      assert(body !== null, 'POST body was not JSON')
      if (body) {
        if (state === 'off') {
          assert(!Object.prototype.hasOwnProperty.call(body, 'humanVerification'), 'off: request body must not carry a humanVerification key')
        } else {
          assert(Object.prototype.hasOwnProperty.call(body, 'humanVerification'), 'on: request body must carry humanVerification')
          assert(body.humanVerification?.provider === 'altcha', 'on: humanVerification.provider must be altcha')
          assert(typeof body.humanVerification?.token === 'string' && body.humanVerification.token.length > 0, 'on: humanVerification.token must be non-empty')
          assert(body.humanVerification.token === solvedToken, 'on: the posted token must be the one solved in Node (no rewrite in between)')
        }
      }
    }

    if (process.env.SFORUM_SKIP_MOBILE === '1') {
      console.error('→ mobile pass skipped (SFORUM_SKIP_MOBILE=1)')
      emitEvidence(null)
      return
    }
    console.error('→ mobile pass')
    /* ── 390×844 ──────────────────────────────────────────── */
    try { await send('Page.stopLoading') } catch { /* ignore */ }
    await withTimeout((async () => {
    await gotoComposer(send, MOBILE)
    const mobileProbe = await evaluate(send, VERIFICATION_PROBE)
    evidence.mobile = mobileProbe

    assert(mobileProbe.innerWidth === MOBILE.width, `mobile: viewport override did not apply (innerWidth=${mobileProbe.innerWidth})`)
    assert(mobileProbe.scrollWidth <= mobileProbe.clientWidth, `mobile: horizontal overflow (scrollWidth=${mobileProbe.scrollWidth} > clientWidth=${mobileProbe.clientWidth})`)
    assert(mobileProbe.dockPosition === 'fixed', `mobile: composer dock must stay fixed (position=${mobileProbe.dockPosition})`)
    if (state === 'on') {
      assert(mobileProbe.widgetVisible === true, 'mobile on: .sf-forum-altcha must be visible')
      assert(mobileProbe.box && mobileProbe.box.width > 0 && mobileProbe.box.height > 0, 'mobile on: .sf-forum-altcha boundingBox must be non-zero')
      assert(mobileProbe.labelVisible === true, 'mobile on: verification label must be visible')
    } else {
      assert(mobileProbe.widgetPresent === false, 'mobile off: .sf-forum-altcha must not be in the DOM')
    }

    const mobileShot = join(qaDir, `post-risk-${state}-mobile-390x844.png`)
    await screenshot(send, mobileShot)
    evidence.screenshots.push(mobileShot)

    // 标题/提示在窄屏的换行：卡片内 label 与 hint 都不得横向溢出容器。
    evidence.mobile.wrap = await evaluate(send, `(() => {
      const wrapper = document.querySelector('#topic-composer-human-verification')
      if (!wrapper) return { present: false }
      const overflowing = [...wrapper.querySelectorAll('*')].filter(element => element.scrollWidth > element.clientWidth + 1)
      return { present: true, overflowing: overflowing.length, scrollWidth: wrapper.scrollWidth, clientWidth: wrapper.clientWidth }
    })()`)
    if (state === 'on') {
      assert(evidence.mobile.wrap.present === true, 'mobile on: verification card must exist for wrap measurement')
      assert(evidence.mobile.wrap.overflowing === 0, `mobile on: verification card has ${evidence.mobile.wrap?.overflowing} overflowing descendants`)
    }
    })(), 120000, 'mobile pass')
  } catch (error) {
    fail(`phase error: ${error?.message || error}`)
    console.error(`! ${error?.message || error}`)
  } finally {
    try { send && await send('Browser.close') } catch { /* ignore */ }
    try { child.kill('SIGKILL') } catch { /* ignore */ }
  }

  emitEvidence(null)
  if (failures.length > 0) process.exitCode = 1
}

try {
  await run()
} catch (error) {
  console.error(`post-risk QA aborted: ${error?.stack || error}`)
  process.exitCode = 1
}
