// 宿主文档 CSP 回归门禁。
//
// 为什么需要它：页面文档 CSP 由 SSR 响应头下发（`Content-Security-Policy`），
// 一旦策略里的 `script-src` / `style-src` 与宿主自己的渲染产物冲突（Nuxt 内联
// 启动脚本、内联 `<style>`、Vue `:style` 绑定、Nuxt UI 设计 token），浏览器会
// 静默丢弃它们——页面看起来"打开了"，实际水合被拦、设计 token 变空。这类回归
// 不会让任何现有测试变红，只有观察 `securitypolicyviolation` 才能发现。
//
// dev 与生产下发同一份文档 CSP（`applyPublicPageDocumentPolicy` 不再有 dev 短路），
// 所以本门禁对 `bun run dev` 与生产构建都有效，且**两边都要求 violation=0**：
// dev 侧允许的唯一差异是 `connect-src` 额外放行 HMR websocket 与 iconify 主机
// （仅开发期），以及内联脚本的 per-request nonce 值本身。dev 若仍有违规，那就是
// 策略与渲染产物没对齐，按真实失败处理，不要按"dev 特性"放过。
//
// 手法与 `tests/browser-public-390x844.mjs` 一致：真实 Chrome + CDP
// （仓库没有 playwright 依赖，root 与 apps/web 都 `require.resolve` 不到），
// 用 `Page.addScriptToEvaluateOnNewDocument` 在文档脚本之前注册监听器。
//
// 每个路径的断言：
//   1. 响应头真的带 `Content-Security-Policy`；没有就 FAIL（"该页没写 CSP，
//      门禁无法生效"）——绝不当成通过，否则本门禁会在没被激活时永远绿。
//      所有公开页都下发文档 CSP，所以任何公开页缺头本身就是回归——前提是策略端点
//      可达；端点不可达说明是环境没起全（见退出码 2）。
//   2. `script-src` 里 nonce 来源恰好 1 个，且页面上打 nonce 的内联 `<script>`
//      用的就是响应头里的那个值（头与 HTML 必须对上，否则内联脚本照样被拦）。
//      头只认**这一次导航**的文档响应；导航响应缺头或 304 命中缓存时，用一次
//      禁用缓存的同 URL 请求补头（`headerSource=cache-bypass`），此时 nonce 来自
//      另一次请求，比对降级为「跳过并标注」，既不算通过也不算失败。
//   3. `style-src` 里不出现 `nonce-` / `'sha`：CSP3 规定同一指令一旦出现 nonce 或
//      hash，其中的 `'unsafe-inline'` 就被忽略，SSR 内联样式会立刻全部被拦。
//   4. 页面内 `securitypolicyviolation` 事件为 0 条；有则逐条打印
//      violatedDirective / blockedURI / sourceFile / sample。
//   5. `--ui-primary` 非空（Nuxt UI 设计 token 没被 `style-src` 拦掉）。
//   6. 至少一个内联样式真的生效（`el.style.length > 0` 且计算值与声明一致）。
//      页面上没有任何带声明式内联样式的元素时，注入一个探针元素测同样的事，
//      并在输出里标注来源，避免"没量到"被写成"通过"。
//
// 退出码：
//   0 全部通过
//   1 有断言失败（含没有 Chrome、Chrome 起不来、harness 异常），以及**页面没头但
//     策略端点可达**——那是代码没写头，真回归
//   2 环境没就绪：web 连不上，或页面没头且策略端点
//     `/api/v1/extensions/runtime/page-policy` 也不可达（web 起着、API 没起）。
//     `./scripts/test.sh` 只把退 2 记成 SKIP
//
// 用法：
//   SFORUM_CSP_PATHS=/t/<topic> node tests/validate-document-csp.mjs   # 对真实服务跑
//   node tests/validate-document-csp.mjs --self-test                   # 自检：本地夹具证明能抓到违规
// 环境变量：
//   SFORUM_WEB_BASE_URL     默认 http://127.0.0.1:3000
//   SFORUM_CSP_PATHS        逗号分隔的路径，默认 "/"。任何公开页都应带文档 CSP，
//                           所以首页就是默认验收目标；要覆盖其它页时显式给。
//   SFORUM_EVIDENCE_OUTPUT  可选：JSON 报告路径
//   SFORUM_CSP_SETTLE_MS    可选：导航后观察 violation 的窗口，默认 8000
//   SFORUM_CSP_HYDRATE_MS   可选：等待 Nuxt 水合的上限，默认 25000（dev 首编译很慢）
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'

const web = (process.env.SFORUM_WEB_BASE_URL || 'http://127.0.0.1:3000').replace(/\/+$/, '')
// 默认目标就是首页：所有公开页都下发文档 CSP，所以 `/` 是合法且最方便的目标。
const paths = (process.env.SFORUM_CSP_PATHS || '/')
  .split(',').map(value => value.trim()).filter(Boolean)
const outputPath = process.env.SFORUM_EVIDENCE_OUTPUT || ''
const settleMs = Number(process.env.SFORUM_CSP_SETTLE_MS || 8000)
const CSP_HEADER = 'content-security-policy'
const SELF_TEST = new Set(process.argv.slice(2)).has('--self-test')

function chromeBinary() {
  for (const candidate of ['/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/opt/google/chrome/chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser']) {
    if (existsSync(candidate)) return candidate
  }
  return ''
}

/** 把响应头拆成 directive -> sources。`default-src 'none'` 这类也原样保留。 */
function parseCsp(headerValue) {
  const directives = new Map()
  for (const segment of headerValue.split(';')) {
    const parts = segment.trim().split(/\s+/).filter(Boolean)
    if (!parts.length) continue
    const [name, ...sources] = parts
    directives.set(name.toLowerCase(), sources)
  }
  return directives
}

const NONCE_SOURCE = /^'nonce-/i
const HASH_SOURCE = /^'sha(256|384|512)-/i

// 页面文档策略的权威来源。页面拿不到它时就不会写文档 CSP 头。
const POLICY_ENDPOINT_PATH = '/api/v1/extensions/runtime/page-policy'

/**
 * 探测策略端点是否可用：web 起着但 API 没起时，这个端点会 5xx/不可达。
 * 只用来区分「环境没就绪」和「代码没写头」，不参与任何通过判定。
 */
async function probePolicyEndpoint(base) {
  const url = `${base}${POLICY_ENDPOINT_PATH}`
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), 5000)
  try {
    const response = await fetch(url, { redirect: 'follow', signal: controller.signal, headers: { accept: 'application/json' } })
    const body = await response.text()
    let parsed = null
    try { parsed = JSON.parse(body) } catch { /* 非 JSON 视为不可用 */ }
    const payload = parsed && typeof parsed === 'object' ? (parsed.data ?? parsed) : null
    const ok = response.ok && Boolean(payload && typeof payload === 'object' && payload.schemaVersion)
    return { ok, detail: `status=${response.status} schemaVersion=${payload?.schemaVersion || '-'}` }
  } catch (error) {
    return { ok: false, detail: `unreachable: ${String(error?.message || error)}` }
  } finally {
    clearTimeout(timer)
  }
}

/** 禁用缓存取一次文档响应头：304 命中缓存时用它补 CSP 头。 */
async function fetchCspHeader(url) {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), 8000)
  try {
    const response = await fetch(url, {
      cache: 'no-store',
      redirect: 'follow',
      signal: controller.signal,
      headers: { accept: 'text/html', 'cache-control': 'no-cache', pragma: 'no-cache' },
    })
    return response.headers.get(CSP_HEADER) || ''
  } catch {
    return ''
  } finally {
    clearTimeout(timer)
  }
}

async function probe(url) {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), 5000)
  try {
    const response = await fetch(url, { redirect: 'follow', signal: controller.signal, headers: { accept: 'text/html' } })
    return { response, body: await response.text() }
  } finally {
    clearTimeout(timer)
  }
}

async function launchChrome(binary) {
  const port = 9400 + Math.floor(Math.random() * 300)
  const profile = `/tmp/sforum-csp-cdp-${Date.now()}`
  mkdirSync(profile, { recursive: true })
  const child = spawn(binary, [
    '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--hide-scrollbars', 'about:blank',
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
  child.kill('SIGKILL')
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
  const loadWaiters = []
  // 文档响应的头，**按导航**收集：per-request nonce 每次请求都不同，用另一次请求
  // （比如外层 probe 的 fetch）的头去比这一次渲染出的脚本 nonce 必然不匹配。
  let documentHeaders = null
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data)
    if (message.id && pending.has(message.id)) {
      const { resolve, reject } = pending.get(message.id)
      pending.delete(message.id)
      if (message.error) reject(new Error(message.error.message))
      else resolve(message.result)
      return
    }
    if (message.method === 'Network.responseReceived' && message.params?.type === 'Document') {
      const headers = {}
      for (const [name, value] of Object.entries(message.params.response.headers || {})) {
        headers[name.toLowerCase()] = value
      }
      // 重定向链里后面的响应覆盖前面的：留下的是浏览器真正拿来渲染文档的那一份。
      // 同时记下状态码：304 命中缓存时这一份响应往往没有 CSP 头。
      documentHeaders = { status: message.params.response.status, headers }
    }
    if (message.method === 'Page.loadEventFired') {
      for (const waiter of loadWaiters.splice(0)) waiter()
    }
  })
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const id = nextId++
    pending.set(id, { resolve, reject })
    socket.send(JSON.stringify({ id, method, params }))
  })
  const waitForLoad = (timeoutMs = 20000) => new Promise(resolve => {
    const timer = setTimeout(() => {
      const index = loadWaiters.indexOf(done)
      if (index >= 0) loadWaiters.splice(index, 1)
      resolve(false)
    }, timeoutMs)
    const done = () => {
      clearTimeout(timer)
      resolve(true)
    }
    loadWaiters.push(done)
  })
  await send('Page.enable')
  await send('Runtime.enable')
  await send('Network.enable')
  return {
    socket,
    send,
    waitForLoad,
    resetDocumentHeaders: () => { documentHeaders = null },
    documentHeaders: () => documentHeaders,
  }
}

// 文档脚本之前注册监听：CSP 违规在解析期就会发生，晚注册会漏掉被拦的内联脚本。
// window 与 document 都挂（捕获阶段），确保拿到宿主与插件注入的每一条。
const INIT_SCRIPT = `(() => {
  window.__sforumCspViolations = []
  const record = event => {
    window.__sforumCspViolations.push({
      disposition: event.disposition,
      violatedDirective: event.violatedDirective,
      effectiveDirective: event.effectiveDirective,
      blockedURI: event.blockedURI,
      sourceFile: event.sourceFile,
      lineNumber: event.lineNumber,
      columnNumber: event.columnNumber,
      sample: event.sample,
    })
  }
  // 事件派发在 Document 上（再冒泡到 window）；只挂 document 一处，避免同一条违规被记两遍。
  document.addEventListener('securitypolicyviolation', record, true)
})()`

const SIGNATURE = `(() => [
  document.readyState,
  (window.__sforumCspViolations || []).length,
  document.querySelector('#__nuxt')?.__vue_app__ ? 1 : 0,
  document.body ? document.body.innerHTML.length : 0,
].join('|'))()`

const MEASURE = `(() => {
  const root = getComputedStyle(document.documentElement)
  // 内联样式是否真的生效：声明存在、计算值非空。CSP 的 style-src 拦截 inline
  // style 时，声明仍在 el.style 上，但计算值不会生效。
  // 判定标准是「声明被真的应用」：计算值必须等于声明值。只看计算值非空不够——
  // 被 CSP 拦掉的 padding-left:42px 计算值仍是 0px（非空但没生效）。
  // 自定义属性无法用计算值验证，跳过，让标准属性说话。
  const effective = element => {
    if (!element.style || element.style.length === 0) return null
    for (let index = 0; index < element.style.length; index += 1) {
      const property = element.style[index]
      if (property.startsWith('--')) continue
      const declared = element.style.getPropertyValue(property).trim()
      const computed = getComputedStyle(element).getPropertyValue(property).trim()
      if (declared && computed && computed === declared) {
        return { property, declared, computed, tag: element.tagName.toLowerCase() }
      }
    }
    return null
  }
  const candidates = [...document.querySelectorAll('[style]')]
  let inline = null
  for (const element of candidates) {
    const hit = effective(element)
    if (hit) { inline = { ...hit, source: 'page', candidates: candidates.length }; break }
  }
  if (!inline) {
    // 页面本身没有声明式内联样式时，用探针量同一件事（CSP 是否允许 inline style），
    // 并标注 source，避免把"没量到"写成"通过"。
    const probe = document.createElement('div')
    probe.setAttribute('data-sforum-csp-probe', '1')
    // 必须走 style 属性（setAttribute）而不是 CSSOM：CSP 的 style-src-attr 拦的是
    // 属性，el.style.setProperty 属于 CSSOM，拦不住，用它当探针会永远"通过"。
    probe.setAttribute('style', 'padding-left:42px')
    document.body.appendChild(probe)
    const hit = effective(probe)
    probe.remove()
    inline = hit ? { ...hit, source: 'synthetic', candidates: candidates.length } : { source: 'none', candidates: candidates.length }
  }
  return {
    url: location.pathname,
    readyState: document.readyState,
    violations: window.__sforumCspViolations || [],
    uiPrimary: root.getPropertyValue('--ui-primary').trim(),
    uiPrimarySample: root.getPropertyValue('--ui-primary').trim().slice(0, 40),
    inlineStyle: inline,
    // 内联 <script> 的 nonce：getAttribute('nonce') 在 Chrome 里恒为空（属性被隐藏），
    // 只能读 script.nonce 属性值。头里的 nonce 与这里的值必须一致，否则内联脚本被拦。
    scripts: [...document.scripts].map(script => ({
      src: script.src ? 'yes' : '',
      nonce: script.nonce || '',
    })),
    hydrated: !!document.querySelector('#__nuxt')?.__vue_app__,
    bodyText: document.body ? document.body.innerText.replace(/\\s+/g, ' ').slice(0, 120) : '',
  }
})()`

/**
 * 对一个 base URL 下的若干路径跑完整门禁。
 * 返回 { failures, evidence, unreachable }；unreachable 非空表示目标服务不可达（退出码 2）。
 */
async function runGate(base, pathList, options = {}) {
  // 真实 Nuxt 页一定会水合；自检夹具是纯静态 HTML，没有 Vue，所以显式关掉。
  const expectHydration = options.expectHydration !== false
  const hydrateBudgetMs = Number(options.hydrateBudgetMs || process.env.SFORUM_CSP_HYDRATE_MS || 25000)
  const failures = []
  const evidence = []
  const missingHeaderPaths = []
  const fail = (message, detail) => failures.push(detail ? `${message} :: ${detail}` : message)

  const pages = []
  for (const path of pathList) {
    try {
      pages.push({ path, ...(await probe(`${base}${path}`)) })
    } catch (error) {
      return { failures, evidence, unreachable: `${base}${path}: ${String(error?.message || error)}` }
    }
  }

  const binary = chromeBinary()
  if (!binary) {
    // 没有浏览器就没法观察 violation。静默跳过会让门禁永远绿——那正是本次回归
    // 能上线的形状，所以这里判失败。
    fail('no Chrome/Chromium binary found', 'install google-chrome or chromium; the gate needs a real browser')
    return { failures, evidence, unreachable: '' }
  }

  const { child, port, version } = await launchChrome(binary)
  const client = await connect(port)
  evidence.push({ browser: version.Browser })
  try {
    await client.send('Page.addScriptToEvaluateOnNewDocument', { source: INIT_SCRIPT })
    for (const page of pages) {
      const { path, response, body } = page
      client.resetDocumentHeaders()
      await client.send('Page.navigate', { url: `${base}${path}` })
      const loaded = await client.waitForLoad()
      // 响应头一律取**这一次导航**的文档响应：外层 probe 的 fetch 是另一次请求，
      // per-request nonce 与它无关，拿它比 nonce 必然假红。
      const navigation = client.documentHeaders()
      const navigationHeader = navigation ? (navigation.headers[CSP_HEADER] || '') : ''
      // 304 命中缓存时浏览器拿到的文档响应通常不带 CSP 头（头来自缓存条目而不是
      // 这次响应）。此时用一次**禁用缓存**的同 URL 请求补头，否则会误报"没写 CSP"。
      // 代价是这份头来自另一次请求，per-request nonce 必然不同，所以 nonce 比对
      // 降级为"跳过并标注"——既不当通过，也不当失败。
      let headerSource = 'navigation'
      let headerValue = navigationHeader
      let nonceComparable = true
      const cacheHit = Boolean(navigation && (navigation.status === 304 || !navigationHeader))
      if (cacheHit) {
        headerSource = 'cache-bypass'
        nonceComparable = false
        headerValue = await fetchCspHeader(`${base}${path}`)
        if (!headerValue) {
          headerSource = 'probe-fetch'
          headerValue = response.headers.get(CSP_HEADER) || ''
        }
      }
      // 1. 头必须真的在。没有头的页面里 violation 永远是 0，任何"通过"都是假的。
      if (!headerValue.trim()) {
        missingHeaderPaths.push(path)
        fail(`${path}: response carries no Content-Security-Policy header`,
          `status=${response.status} navigationStatus=${navigation ? navigation.status : '-'} source=${headerSource} — this page does not emit the document policy, so the gate cannot observe anything`)
      }
      const directives = parseCsp(headerValue)
      const scriptSources = directives.get('script-src') || []
      const styleSources = directives.get('style-src') || []
      const headerNonces = scriptSources.filter(source => NONCE_SOURCE.test(source))
      const headerNonce = headerNonces.length === 1 ? headerNonces[0].slice(7, -1) : ''
      // 2. 内联脚本只能靠 nonce 放行：恰好一个 nonce 来源，多了说明策略在叠床架屋。
      if (headerValue.trim() && headerNonces.length !== 1) {
        fail(`${path}: script-src must carry exactly one nonce source`,
          `got ${headerNonces.length} (${JSON.stringify(headerNonces)}) in ${JSON.stringify(scriptSources)}`)
      }
      // 3. style-src 里出现 nonce/hash 会让同指令的 'unsafe-inline' 失效（CSP3），
      //    SSR 内联样式会全部被拦——这是最容易被"顺手加个 nonce"打回原形的形状。
      const styleLeaks = styleSources.filter(source => NONCE_SOURCE.test(source) || HASH_SOURCE.test(source))
      if (styleLeaks.length > 0) {
        fail(`${path}: style-src must not carry nonce/hash sources`,
          `${JSON.stringify(styleLeaks)} disables 'unsafe-inline' in the same directive; inline styles would be blocked`)
      }

      // 导航后观察窗口：水合与后续请求触发的 violation 都在这一段里发生。
      // 轮询到「readyState=complete + 水合标记出现 + 签名稳定」，而不是固定等一小段：
      // 旧写法在 Nuxt 尚未水合时签名就已经稳定，会提前收敛成假阴性。
      const settleStart = Date.now()
      let signature = ''
      let stablePolls = 0
      let hydratedSeen = false
      while (Date.now() - settleStart < Math.max(settleMs, hydrateBudgetMs)) {
        await new Promise(resolve => setTimeout(resolve, 250))
        const probeResult = await client.send('Runtime.evaluate', { expression: SIGNATURE, returnByValue: true })
        const next = String(probeResult.result.value)
        // 签名是 readyState|violations|hydrated|bodyLength，水合标记是第 3 段。
        // 曾经用 endsWith('|1') 判断，而末段是 body 长度，于是水合永远"没发生"。
        const fields = next.split('|')
        hydratedSeen = fields[2] === '1'
        const settled = fields[0] === 'complete' && (!expectHydration || hydratedSeen)
        stablePolls = settled && next === signature ? stablePolls + 1 : 0
        signature = next
        if (stablePolls >= 3) break
      }
      const hydrationTimedOut = expectHydration && !hydratedSeen
      const measured = await client.send('Runtime.evaluate', { expression: MEASURE, returnByValue: true, awaitPromise: true })
      const measurement = measured.result.value
      const entry = {
        path,
        url: `${base}${path}`,
        status: response.status,
        loaded,
        settleMsSpent: Date.now() - settleStart,
        cspHeader: headerValue,
        headerNonce,
        headerSource,
        nonceCheck: nonceComparable ? 'compared' : 'skipped',
        navigationStatus: navigation ? navigation.status : null,
        hydrated: measurement.hydrated,
        hydrationTimedOut,
        htmlBytes: body.length,
        ...measurement,
      }
      evidence.push(entry)

      // 4. 真实 Nuxt 页必须水合：内联启动脚本被拦时页面照样 200，但 Vue 永远不会挂载。
      if (hydrationTimedOut) {
        fail(`${path}: page never hydrated within ${Math.max(settleMs, hydrateBudgetMs)}ms`,
          `hydrated=${measurement.hydrated} — the client bundle did not run (blocked inline bootstrap script?)`)
      }
      // 5. 头里的 nonce 必须真的落到内联 <script> 上；对不上等于没有 nonce。
      //    只有「头来自这次导航」时才能比：cache-bypass / probe-fetch 的头属于另一次
      //    请求，per-request nonce 天然不同，比了必然假红，所以跳过并标注。
      const nonceScripts = measurement.scripts.filter(script => script.nonce)
      const inlineScripts = measurement.scripts.filter(script => !script.src)
      if (!nonceComparable) {
        console.log(`  NOTE ${path}: nonce comparison skipped — header came from ${headerSource}, not from the rendered navigation`)
      } else {
        const mismatched = nonceScripts.filter(script => !headerNonce || script.nonce !== headerNonce)
        if (headerNonce && inlineScripts.length > 0 && nonceScripts.length === 0) {
          fail(`${path}: header nonce never reached an inline <script>`,
            `inlineScripts=${inlineScripts.length} nonceScripts=0 — the document policy nonce and the rendered HTML are out of sync`)
        }
        if (mismatched.length > 0) {
          fail(`${path}: inline <script> nonce does not match the header nonce`,
            `${mismatched.length} of ${nonceScripts.length} mismatched (header=${headerNonce || '-'})`)
        }
      }
      // 6. 逐条打印 violation：这是本门禁存在的理由，输出要能直接定位到被拦的资源。
      if (measurement.violations.length > 0) {
        fail(`${path}: ${measurement.violations.length} securitypolicyviolation event(s)`,
          measurement.violations.map(violation =>
            `${violation.violatedDirective} blocked=${violation.blockedURI || '-'} source=${violation.sourceFile || '-'}:${violation.lineNumber || 0} sample=${JSON.stringify((violation.sample || '').slice(0, 80))}`,
          ).join(' | '))
      }
      // 7. 设计 token 没被 style-src 拦掉。
      if (!measurement.uiPrimary) {
        fail(`${path}: --ui-primary resolved to an empty value`, 'Nuxt UI design tokens are not applied (style-src likely blocked the host stylesheet)')
      }
      // 8. 内联样式真的生效。
      const inline = measurement.inlineStyle || {}
      if (!inline.property) {
        fail(`${path}: no inline style took effect`, `source=${inline.source || 'none'} candidates=${inline.candidates ?? 0}`)
      }
    }
  } finally {
    client.socket.close()
    child.kill('SIGKILL')
  }
  // 「页面没有头」有两种成因，必须分开：
  //   - 策略端点也不可达 → 环境没就绪（web 起着、API 没起），退 2 让 static 线 SKIP；
  //   - 策略端点可达 → 代码没写头，是真回归，保持退 1。
  let environmentNotReady = ''
  if (missingHeaderPaths.length > 0) {
    const policy = await probePolicyEndpoint(base)
    if (!policy.ok) {
      environmentNotReady = `${base}${POLICY_ENDPOINT_PATH} (${policy.detail})`
    }
  }
  return { failures, evidence, unreachable: '', environmentNotReady }
}

function printReport(title, result) {
  console.log(`${title} (web=${title.includes('self-test') ? 'local fixture' : web})`)
  for (const entry of result.evidence) {
    if (entry.browser) {
      console.log(`  browser: ${entry.browser}`)
      continue
    }
    const inline = entry.inlineStyle || {}
    const scripts = entry.scripts || []
    const nonceScripts = scripts.filter(script => script.nonce).length
    const inlineScripts = scripts.filter(script => !script.src).length
    console.log(
      `  ${entry.path}: status=${entry.status} csp=${entry.cspHeader ? `${entry.cspHeader.length}B` : 'MISSING'} ` +
        `header=${entry.headerSource || '-'} nonceCheck=${entry.nonceCheck || '-'} ` +
        `nonce=${entry.headerNonce ? `${entry.headerNonce.slice(0, 8)}…` : '-'} ` +
        `inlineScripts=${inlineScripts} nonceScripts=${nonceScripts} ` +
        `violations=${entry.violations.length} uiPrimary=${JSON.stringify(entry.uiPrimarySample || '')} ` +
        `inlineStyle=${inline.source || 'none'}:${inline.property || '-'}=${JSON.stringify(inline.computed || '')} ` +
        `hydrated=${entry.hydrated} settle=${entry.settleMsSpent}ms`,
    )
    for (const violation of entry.violations) {
      console.log(
        `    violation: ${violation.violatedDirective} blocked=${violation.blockedURI || '-'} ` +
          `source=${violation.sourceFile || '-'}:${violation.lineNumber || 0}:${violation.columnNumber || 0} ` +
          `sample=${JSON.stringify((violation.sample || '').slice(0, 120))}`,
      )
    }
  }
  if (result.failures.length > 0) {
    for (const failure of result.failures) console.error(`  FAIL ${failure}`)
    return false
  }
  console.log('  all document CSP assertions passed')
  return true
}

async function main() {
  const result = await runGate(web, paths)
  if (result.unreachable) {
    // 2 = 服务不在（唯一会被 test.sh 记成 SKIP 的情形）。页面能打开但断言不过
    // （含缺 CSP 头）一律是 1，不会被 SKIP 掩盖。
    console.error(`CSP gate cannot reach ${result.unreachable}`)
    console.error('Start the dev stack first (dev API + web), or set SFORUM_WEB_BASE_URL to the running web.')
    process.exitCode = 2
    return
  }
  if (outputPath) writeFileSync(outputPath, `${JSON.stringify({ web, paths, failures: result.failures, evidence: result.evidence }, null, 2)}\n`, 'utf8')
  const passed = printReport('document CSP gate', result)
  if (result.environmentNotReady) {
    // 页面能开却没有文档 CSP，且策略端点也不可达 = 环境没就绪（web 在、API 不在），
    // 不是回归。退 2 交给 static 线 SKIP；策略端点可达时不会走到这里（仍是 1）。
    console.error(`  NOTE environment not ready: pages sent no document CSP and the policy endpoint is unavailable — ${result.environmentNotReady}`)
    console.error('  Start the API as well (the gate needs both web and API), or set SFORUM_WEB_BASE_URL to a complete stack.')
    process.exitCode = 2
    return
  }
  process.exitCode = passed ? 0 : 1
}

/**
 * 自检：用本地夹具证明「门禁真的能抓到违规」，而不是只会在健康页面上变绿。
 * 两份文档只差 CSP 策略与 nonce：
 *   /prefix  修复前策略（style-src 'self'；script-src 'self'）→ 必须抓到 ≥2 条 violation
 *   /fixed   修复后策略（style-src 'self' 'unsafe-inline'；script-src 'self' 'nonce-…'）→ 必须通过
 * 文档本身刻意包含 Nuxt SSR 会输出的三样东西：内联 `<style>`（设计 token）、
 * 内联 `<script>`（启动数据）、`style="…"` 属性（Vue :style 绑定）。
 */
async function selfTest() {
  const nonce = 'c2VsZi10ZXN0LW5vbmNl'
  const prefixCsp = "default-src 'none'; script-src 'self'; style-src 'self'"
  const fixedCsp = `default-src 'none'; script-src 'self' 'nonce-${nonce}'; style-src 'self' 'unsafe-inline'`
  const document = scriptNonce => `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>csp fixture</title>
<style id="nuxt-ui-colors">:root{--ui-primary:#00a86b;--ui-color-primary-50:#eef7f4}</style>
</head><body><div id="app"><p style="padding-left:42px">fixture</p>
<script${scriptNonce ? ` nonce="${scriptNonce}"` : ''}>window.__NUXT_DATA__=[1,2,3]</script>
</div></body></html>`

  const server = createServer((request, response) => {
    const isPrefix = (request.url || '').startsWith('/prefix')
    response.writeHead(200, {
      'content-type': 'text/html; charset=utf-8',
      'content-security-policy': isPrefix ? prefixCsp : fixedCsp,
      'cache-control': 'no-store',
    })
    response.end(document(isPrefix ? '' : nonce))
  })
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  const base = `http://127.0.0.1:${server.address().port}`
  const checks = []
  const check = (label, ok, detail) => {
    checks.push({ label, ok, detail })
    if (!ok) console.error(`  FAIL ${label}${detail ? ` :: ${detail}` : ''}`)
    else console.log(`  ok   ${label}${detail ? ` (${detail})` : ''}`)
  }
  try {
    // 夹具是纯静态 HTML，没有 Nuxt/Vue，所以不要求水合。
    const result = await runGate(base, ['/prefix', '/fixed'], { expectHydration: false })
    printReport('document CSP gate self-test', result)
    const failuresFor = path => result.failures.filter(failure => failure.startsWith(`${path}:`))
    const prefix = result.evidence.find(entry => entry.path === '/prefix') || {}
    const fixed = result.evidence.find(entry => entry.path === '/fixed') || {}
    check('pre-fix fixture is rejected by the gate', failuresFor('/prefix').length > 0,
      `${failuresFor('/prefix').length} failure(s)`)
    check('pre-fix fixture yields >=2 securitypolicyviolation events',
      (prefix.violations || []).length >= 2, `got ${(prefix.violations || []).length}`)
    // 三类必须都在：内联样式表（style-src-elem）、Vue :style 属性（style-src-attr）、
    // 内联启动脚本（script-src-elem）。只覆盖其中一类会漏掉另一种回归形状。
    const covered = new Set((prefix.violations || []).map(violation => violation.violatedDirective))
    const required = ['style-src-elem', 'style-src-attr', 'script-src-elem']
    const missing = required.filter(name => !covered.has(name))
    check('pre-fix fixture covers style-src-elem / style-src-attr / script-src-elem',
      missing.length === 0, `captured=${JSON.stringify([...covered].sort())} missing=${JSON.stringify(missing)}`)
    check('pre-fix fixture blocks the design tokens', !prefix.uiPrimary)
    check('pre-fix fixture leaves no effective inline style', !(prefix.inlineStyle || {}).property)
    check('fixed fixture is accepted by the gate', failuresFor('/fixed').length === 0,
      JSON.stringify(failuresFor('/fixed')))
    check('fixed fixture has no violations', (fixed.violations || []).length === 0)
    check('fixed fixture resolves --ui-primary', Boolean(fixed.uiPrimary), JSON.stringify(fixed.uiPrimarySample || ''))
    check('fixed fixture has an effective inline style', Boolean((fixed.inlineStyle || {}).property),
      `${(fixed.inlineStyle || {}).property || '-'}`)
    check('fixed fixture nonce reaches the inline script',
      (fixed.scripts || []).some(script => script.nonce === nonce))
  } finally {
    await new Promise(resolve => server.close(resolve))
  }
  const failed = checks.filter(entry => !entry.ok).length
  console.log(failed === 0 ? '  self-test passed: the gate detects the regression and clears the fix' : `  self-test failed: ${failed} check(s)`)
  process.exitCode = failed === 0 ? 0 : 1
}

try {
  if (SELF_TEST) await selfTest()
  else await main()
} catch (error) {
  console.error(`  FAIL harness error :: ${String(error?.message || error)}`)
  process.exitCode = 1
}
