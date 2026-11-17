// 管理端视觉回归真机 QA（issue #238）。
//
// 为什么是 CDP 而不是 happy-dom：`apps/web/tests/**` 只能证明组件把类名拼对了，证明不了
// 「1633×810 下没有横向溢出」「390×844 下控件没被裁切」「暗色下边框没消失」「Toast 图标
// 吃的是主题令牌」这类只在真实排版里成立的事。仓库没有 playwright 依赖，所以沿用
// `tests/browser-post-risk-composer.mjs` 的零依赖 CDP 手法：真实 Chrome +
// `Emulation.setDeviceMetricsOverride` 真的把视口切成 1633×810 / 390×844。
//
// 用法：
//   SFORUM_SESSION_COOKIE=<sforum_session 的值> node tests/browser-admin-visual-regression.mjs
//
// 环境变量：
//   SFORUM_SESSION_COOKIE  必填：登录会话 cookie 值（super_admin 账号）
//   SFORUM_WEB_BASE_URL    默认 http://127.0.0.1:3000
//   SFORUM_QA_DIR          截图目录，默认 storage/qa/admin-visual（storage/ 已 gitignore）
//   SFORUM_EVIDENCE_OUTPUT 可选：evidence.json 落盘路径；同时写同名 .md 汇总
//   SFORUM_CHROME          可选：Chrome 可执行文件路径
//   SFORUM_SKIP_DARK=1 / SFORUM_SKIP_TOAST=1 / SFORUM_SKIP_DIALOG=1  分段调试用
//   SFORUM_SETTLE_MS       每次导航后的额外等待，默认 1200
//   SFORUM_ONLY_PHASES     调试用：只跑指定后置阶段，例如 "568" 只跑暗色/Toast/弹窗
//
// 退出码：0 无 fail（skipped 允许）/ 1 有 fail / 2 用法或环境错误。
//
// 页面内探针一律是**静态函数声明字面量**，动态值只经 `Runtime.callFunctionOn` 的
// `arguments` 传值 —— 表达式里零字符串插值（同 `tests/browser-post-risk-composer.mjs`
// 的修法，避免 js/bad-code-sanitization 那一类代码构造问题）。
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, readdirSync, readFileSync, realpathSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'

const web = (process.env.SFORUM_WEB_BASE_URL || 'http://127.0.0.1:3000').replace(/\/+$/, '')
const sessionCookie = (process.env.SFORUM_SESSION_COOKIE || '').trim()
const qaDir = process.env.SFORUM_QA_DIR || 'storage/qa/admin-visual'
const evidencePath = process.env.SFORUM_EVIDENCE_OUTPUT || ''
const settleMs = Number(process.env.SFORUM_SETTLE_MS || 1200)
const onlyPhases = (process.env.SFORUM_ONLY_PHASES || '').trim()
const repoRoot = join(dirname(new URL(import.meta.url).pathname), '..')

if (!sessionCookie) {
  console.error('SFORUM_SESSION_COOKIE is required (value of the sforum_session cookie)')
  process.exit(2)
}

/* ── 规格常量 ─────────────────────────────────────────────────────────── */

const DESKTOP = { id: 'D', width: 1633, height: 810, dpr: 1 }
const MOBILE = { id: 'M', width: 390, height: 844, dpr: 3 }

const SETTINGS_TABS = [
  { id: 'basic', anchor: '基础信息' },
  { id: 'accountSecurity', anchor: '账号安全' },
  { id: 'registration', anchor: '控制谁可以注册' },
  { id: 'newcomers', anchor: '新注册用户在信任期内' },
  { id: 'maintenance', anchor: '开启后普通访客的写操作会被拦截' },
  { id: 'client', anchor: '原生 App 与桌面端读取公共选项' },
  { id: 'verification', anchor: '配置注册等风险操作使用的验证码服务' },
  { id: 'updates', anchor: '当前版本' }
]

const PAGES = [
  ...SETTINGS_TABS.map(tab => ({
    id: `settings:${tab.id}`,
    path: `/control-panel/settings?tab=${tab.id}`,
    kind: 'settings',
    tab: tab.id,
    anchor: tab.anchor
  })),
  { id: 'users', path: '/control-panel/users', kind: 'list' },
  { id: 'jobs', path: '/control-panel/jobs', kind: 'list' },
  { id: 'schedules', path: '/control-panel/schedules', kind: 'list' },
  { id: 'extensions', path: '/control-panel/extensions', kind: 'list' }
]

const DEFAULT_GREEN = ['rgb(34, 197, 94)', 'rgb(74, 222, 128)', 'rgb(22, 163, 74)']

/* ── 结果账本 ─────────────────────────────────────────────────────────── */

const results = []
const screenshots = []
const runNotes = []

function pushResult(entry) {
  results.push({ note: '', ...entry })
}

function pass(id, meta, actual, note = '') {
  pushResult({ id, title: TITLES[id] || id, status: 'pass', expected: EXPECTED[id] || '', actual, note, ...meta })
}

function fail(id, meta, actual, note = '', kind = '') {
  pushResult({ id, title: TITLES[id] || id, status: 'fail', kind, expected: EXPECTED[id] || '', actual, note, ...meta })
}

function skip(id, meta, reason) {
  pushResult({ id, title: TITLES[id] || id, status: 'skipped', expected: EXPECTED[id] || '', actual: null, note: reason, ...meta })
}

function judge(id, meta, ok, actual, note = '') {
  if (ok) pass(id, meta, actual, note)
  else fail(id, meta, actual, note)
}

const TITLES = {
  P1: '依赖为抬档后版本',
  P2: '视口覆盖真的生效',
  P3: '无 console error',
  P4: 'locale 为 zh-CN',
  '1.1': '设置页标题 text-xl + 注册表图标',
  '1.2': '无营销式大标题',
  '1.3': 'toolbar 有标准边框',
  '1.4': '唯一 tab 系统',
  '1.5': '单活动面板',
  '2.1': '文档无横向溢出',
  '2.2': '主列无内部横向滚动',
  '3.1': '无被裁切控件',
  '3.2': 'icon-only 按钮带 aria-label + title',
  '4.1': 'URL query 与选中 tab 同步',
  '4.2': '面板内容正确且无残留',
  '4.3': 'tab 切换是 SPA 过渡',
  '4.4': '深链直达正确 tab',
  '5.1': '暗色生效',
  '5.2': '卡片与页面分层',
  '5.3': '暗色边框不消失',
  '5.4': '静音文本对比度达标',
  '6.1': '成功 Toast 出现',
  '6.2': 'Toast 图标色 == 主题令牌',
  '6.3': '不回落默认绿',
  '6.4': '默认主题期望值',
  '7.1': '无 emoji 当图标',
  '7.2': '图标来自 Tabler/Lucide',
  '7.3': '无手写 SVG',
  '8.1': '弹窗开合',
  '8.2': 'M 下弹窗不溢出',
  '8.3': '焦点困在弹窗内',
  '8.4': '关闭后焦点归还',
  '8.5': 'dropdown 不越界',
  '8.6': 'dialog 带 aria-modal',
  '8.4b': '关闭后焦点归还（行内「编辑」入口）'
}

const EXPECTED = {
  P1: '@nuxt/ui 4.12.2 / reka-ui 2.11.0',
  P2: 'D: innerWidth 1633 & dpr 1；M: innerWidth 390 & dpr 3',
  P3: '0 条 error/exception',
  P4: "document.documentElement.lang === 'zh-CN'",
  '1.1': 'h2 字号 20px、字重 >= 700、文案「站点设置」',
  '1.2': '#sforum-admin-main 内 h1 === 0、h2 === 1、无 fontSize > 20px',
  '1.3': '[data-slot="left"] 父节点含 [data-slot="right"]、borderTopWidth 1px、borderTopColor 不透明',
  '1.4': '[role="tablist"] === 1 且固定 tab === 8',
  '1.5': '[role="tabpanel"] === 1',
  '2.1': 'documentElement.scrollWidth <= clientWidth',
  '2.2': '#sforum-admin-main scrollWidth <= clientWidth + 1',
  '3.1': '越界且不在横向滚动容器内的可见控件 === 0',
  '3.2': 'innerText 为空的 [data-slot="right"] button 必须同时有 aria-label 与 title',
  '4.1': "URL tab === T；选中固定 tab 唯一且 === T",
  '4.2': '主区含 anchor[T]，不含其余 tab 的 anchor',
  '4.3': '点击后 window.__qaSpaMarker 仍为 1',
  '4.4': '?tab=verification 深链直达且锚点出现',
  '5.1': "classList.contains('dark') === true",
  '5.2': '卡片 backgroundColor !== 页面背景',
  '5.3': 'borderTopColor !== backgroundColor 且对比度 >= 1.15',
  '5.4': '可见 p/td/.text-muted 对比度 >= 4.5（text-xs 放宽到 3.0）',
  '6.1': '点保存后 3s 内 toast >= 1',
  '6.2': 'toast 图标色 === --ui-success 实色',
  '6.3': '实测色不在默认绿集合内',
  '6.4': '亮色 rgb(15, 118, 110) / 暗色 rgb(45, 212, 191)',
  '7.1': '主区文本不命中 emoji 区间',
  '7.2': 'span.iconify 全部来自 lucide/tabler，且计数 > 0',
  '7.3': '#sforum-admin-main svg:not([aria-hidden="true"]) === 0',
  '8.1': '点编辑后 dialog === 1，Escape 后 === 0',
  '8.2': 'dialog 四边都在视口内',
  '8.3': '连按 6 次 Tab，焦点始终在 dialog 内',
  '8.4': 'Escape 后 activeElement 是打开前的触发按钮',
  '8.5': '[role="menu"] 四边都在视口内',
  '8.6': 'role="dialog" 上 aria-modal === "true"',
  '8.4b': 'Escape 后 activeElement 是行内「编辑」按钮'
}

/* ── CDP 基础 ─────────────────────────────────────────────────────────── */

function chromeBinary() {
  const explicit = process.env.SFORUM_CHROME
  if (explicit && existsSync(explicit)) return explicit
  for (const candidate of ['/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/opt/google/chrome/chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser']) {
    if (existsSync(candidate)) return candidate
  }
  return ''
}

async function launchChrome(binary) {
  const port = 9500 + Math.floor(Math.random() * 400)
  const profile = `/tmp/sforum-admin-qa-${Date.now()}`
  mkdirSync(profile, { recursive: true })
  const child = spawn(binary, [
    '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--hide-scrollbars',
    '--disable-dev-shm-usage', '--force-device-scale-factor=1', 'about:blank'
  ], { stdio: 'ignore' })
  for (let attempt = 0; attempt < 200; attempt += 1) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/version`)
      if (response.ok) return { child, port }
    } catch { /* 等 Chrome 起来 */ }
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
    if (message.method === 'Runtime.consoleAPICalled' && params.type === 'error') {
      consoleErrors.push((params.args || []).map(argument => argument.value ?? argument.description ?? '').join(' ').slice(0, 240))
    }
    if (message.method === 'Runtime.exceptionThrown') {
      const details = params.exceptionDetails || {}
      consoleErrors.push(`exception: ${details.text || ''} ${details.exception?.description || ''}`.slice(0, 240))
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
  try { await send('Page.bringToFront') } catch { /* ignore */ }
  await send('Network.setCookie', {
    name: 'sforum_session', value: sessionCookie, domain: '127.0.0.1', path: '/', httpOnly: true, sameSite: 'Lax'
  })
  return { socket, send, consoleErrors }
}

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))

/* ── 页面内探针（静态声明 + arguments 传值） ─────────────────────────── */

const PAGE_FACTS = `function () {
  const main = document.querySelector('#sforum-admin-main')
  const visible = element => {
    if (!element) return false
    const style = getComputedStyle(element)
    const rect = element.getBoundingClientRect()
    return style.display !== 'none' && style.visibility !== 'hidden' && rect.width > 0 && rect.height > 0
  }
  const inHorizontalScroller = element => {
    let parent = element.parentElement
    while (parent && parent !== document.body) {
      const style = getComputedStyle(parent)
      if ((style.overflowX === 'auto' || style.overflowX === 'scroll') && parent.scrollWidth > parent.clientWidth + 1) return true
      parent = parent.parentElement
    }
    return false
  }
  const clipped = [...document.querySelectorAll('button,a,input,select,textarea,[role="button"]')]
    .filter(visible)
    .filter(element => {
      const rect = element.getBoundingClientRect()
      return (rect.right > innerWidth + 1 || rect.left < -1) && !inHorizontalScroller(element)
    })
    .map(element => ({
      tag: element.tagName,
      text: (element.innerText || element.value || element.getAttribute('aria-label') || '').trim().slice(0, 28),
      left: Math.round(element.getBoundingClientRect().left),
      right: Math.round(element.getBoundingClientRect().right)
    }))
  const bigText = [...document.querySelectorAll('#sforum-admin-main *')]
    .filter(element => {
      const style = getComputedStyle(element)
      const rect = element.getBoundingClientRect()
      return parseFloat(style.fontSize) > 20 && rect.width > 0 && style.display !== 'none' && element.children.length === 0 && (element.innerText || '').trim()
    })
    .map(element => ({ tag: element.tagName, size: getComputedStyle(element).fontSize, text: (element.innerText || '').trim().slice(0, 24) }))
  const tablists = [...document.querySelectorAll('[role="tablist"]')].map(list => ({
    label: list.getAttribute('aria-label') || '',
    tabs: list.querySelectorAll('[role="tab"]').length,
    fixed: list.querySelectorAll('[role="tab"][data-sf-fixed-tab-id]').length
  }))
  const toolbarLeft = document.querySelector('[data-slot="left"]')
  const toolbarBar = toolbarLeft ? toolbarLeft.parentElement : null
  const iconClasses = [...document.querySelectorAll('span.iconify')].map(span => String(span.className))
  const rightButtons = [...document.querySelectorAll('[data-slot="right"] button')].map(button => ({
    text: (button.innerText || '').trim(),
    aria: button.getAttribute('aria-label'),
    title: button.getAttribute('title')
  }))
  return {
    url: location.pathname + location.search,
    lang: document.documentElement.lang,
    innerWidth,
    innerHeight,
    dpr: devicePixelRatio,
    hasMain: Boolean(main),
    mainText: main ? main.innerText : '',
    // 4.2 的面板正文：SFAdminFixedTabNav 的 8 个 tab 按钮就在 main 内部，
    // 直接读 main.innerText 会把 8 个 tab 文案都读进来（「账号安全」等），
    // 于是「其余 tab 的锚点不得出现」必然误报。这里剔除 [role="tablist"] 子树。
    panelText: (() => {
      if (!main) return ''
      const clone = main.cloneNode(true)
      clone.querySelectorAll('[role="tablist"]').forEach(node => node.remove())
      return clone.textContent || ''
    })(),
    doc: {
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth
    },
    main: main ? { scrollWidth: main.scrollWidth, clientWidth: main.clientWidth } : null,
    h1: document.querySelectorAll('#sforum-admin-main h1').length,
    h2: [...document.querySelectorAll('#sforum-admin-main h2')].map(heading => ({
      text: (heading.innerText || '').trim(),
      size: getComputedStyle(heading).fontSize,
      weight: getComputedStyle(heading).fontWeight
    })),
    bigText,
    tablists,
    fixedTabs: document.querySelectorAll('[role="tab"][data-sf-fixed-tab-id]').length,
    selectedFixed: [...document.querySelectorAll('[role="tab"][data-sf-fixed-tab-id][aria-selected="true"]')].map(tab => tab.dataset.sfFixedTabId),
    selectedAll: [...document.querySelectorAll('[role="tab"][aria-selected="true"]')].map(tab => tab.dataset.sfFixedTabId || (tab.innerText || '').trim().slice(0, 12)),
    panels: document.querySelectorAll('[role="tabpanel"]').length,
    clipped,
    toolbar: toolbarBar ? {
      hasRight: Boolean(toolbarBar.querySelector('[data-slot="right"]')),
      borderTopWidth: getComputedStyle(toolbarBar).borderTopWidth,
      borderTopColor: getComputedStyle(toolbarBar).borderTopColor
    } : null,
    rightButtons,
    iconifyCount: iconClasses.length,
    iconifyNonLibrary: iconClasses.filter(name => !/i-(lucide|tabler)[:-]/.test(name)).length,
    iconifyLegacyLiteral: iconClasses.filter(name => !/i-(lucide|tabler)-/.test(name)).length,
    svgNotHidden: document.querySelectorAll('#sforum-admin-main svg:not([aria-hidden="true"])').length,
    emojiHits: ((main ? main.innerText : '') || '').match(/[\\u{1F000}-\\u{1FAFF}\\u{2600}-\\u{27BF}\\u{FE0F}]/gu) || [],
    tabHrefs: [...document.querySelectorAll('[role="tab"][data-sf-fixed-tab-id]')].map(tab => ({
      id: tab.dataset.sfFixedTabId,
      controls: tab.getAttribute('aria-controls'),
      label: (tab.innerText || '').trim()
    }))
  }
}`

const PAGE_RESOLVE_COLORS = `function (colors) {
  const canvas = document.createElement('canvas')
  canvas.width = 1
  canvas.height = 1
  const context = canvas.getContext('2d')
  const probe = document.createElement('span')
  document.body.appendChild(probe)
  const out = colors.map(color => {
    probe.style.color = color
    const resolved = getComputedStyle(probe).color
    context.clearRect(0, 0, 1, 1)
    context.fillStyle = resolved
    context.fillRect(0, 0, 1, 1)
    const data = context.getImageData(0, 0, 1, 1).data
    return { input: color, resolved, rgb: [data[0], data[1], data[2]], alpha: data[3] / 255 }
  })
  probe.remove()
  return out
}`

const PAGE_THEME_FACTS = `function () {
  const main = document.querySelector('#sforum-admin-main')
  const opaque = element => {
    let node = element
    while (node && node !== document.documentElement) {
      const style = getComputedStyle(node)
      const value = style.backgroundColor
      const match = value.match(/rgba?\\(([^)]+)\\)/)
      if (match) {
        const parts = match[1].split(',').map(Number)
        const alpha = parts.length > 3 ? parts[3] : 1
        if (alpha > 0.5) return { color: value, from: node.tagName + '.' + String(node.className).slice(0, 24) }
      }
      node = node.parentElement
    }
    return { color: getComputedStyle(document.body).backgroundColor, from: 'body' }
  }
  const alphaOf = value => {
    const text = String(value)
    if (text === 'transparent' || text === 'rgba(0, 0, 0, 0)') return 0
    const rgba = text.match(/rgba?\\(([^)]+)\\)/)
    if (rgba) {
      const parts = rgba[1].split(',').map(Number)
      return parts.length > 3 ? parts[3] : 1
    }
    // oklch(... / 0.5) 这类现代颜色语法：没有斜杠 alpha 就是完全不透明
    const slashAlpha = text.match(/\\/\\s*([0-9.]+)\\s*\\)/)
    if (slashAlpha) return Number(slashAlpha[1])
    return text ? 1 : 0
  }
  const colorIn = value => {
    const match = String(value).match(/(rgba?\([^)]*\)|oklch\([^)]*\)|#[0-9a-fA-F]{3,8})/)
    return match ? match[1] : null
  }
  // 卡片判据：**不透明背景** + （可见边框 或 ring 阴影）的块。
  // Nuxt UI 4 的卡片用 ring（box-shadow）而不是 border，borderTopWidth 恒为 0；
  // 只按 border 找会一个都命中不了（5.2/5.3 曾因此假失败）。
  const cards = [...document.querySelectorAll('#sforum-admin-main *')]
    .filter(element => {
      const style = getComputedStyle(element)
      const rect = element.getBoundingClientRect()
      if (rect.width < 120 || rect.height < 40) return false
      if (alphaOf(style.backgroundColor) <= 0.5) return false
      const hasBorder = style.borderTopStyle !== 'none' && style.borderTopWidth !== '0px'
      const hasRing = style.boxShadow !== 'none' && /0px 0px 0px [1-9]/.test(style.boxShadow)
      return hasBorder || hasRing
    })
  const card = cards[0] || null
  const mutedSelector = 'p, td, .text-muted'
  const muted = [...document.querySelectorAll('#sforum-admin-main ' + mutedSelector)]
    .filter(element => {
      const rect = element.getBoundingClientRect()
      return rect.width > 0 && rect.height > 0 && (element.innerText || '').trim()
    })
    .slice(0, 12)
    .map(element => ({
      tag: element.tagName,
      size: parseFloat(getComputedStyle(element).fontSize),
      color: getComputedStyle(element).color,
      text: (element.innerText || '').trim().slice(0, 20)
    }))
  const cardStyle = card ? getComputedStyle(card) : null
  const borderWidth = cardStyle ? cardStyle.borderTopWidth : null
  const ringColor = cardStyle ? colorIn(cardStyle.boxShadow) : null
  return {
    dark: document.documentElement.classList.contains('dark'),
    bodyBg: getComputedStyle(document.body).backgroundColor,
    mainBg: main ? getComputedStyle(main).backgroundColor : null,
    mainEffectiveBg: main ? opaque(main).color : null,
    cardBg: cardStyle ? cardStyle.backgroundColor : null,
    cardBorder: cardStyle
      ? (borderWidth && borderWidth !== '0px'
        ? { kind: 'border', width: borderWidth, color: cardStyle.borderTopColor }
        : { kind: 'ring', width: '1px', color: ringColor })
      : null,
    muted
  }
}`

const PAGE_TOAST_FACTS = `function () {
  if (!document.body) return { count: 0, ready: false, tokenColor: null, texts: [], iconColor: null, dark: false }
  const toasts = [...document.querySelectorAll('[data-slot="viewport"] [data-slot="base"]')]
  const readToken = name => {
    const probe = document.createElement('span')
    probe.style.color = 'var(' + name + ')'
    document.body.appendChild(probe)
    const value = getComputedStyle(probe).color
    probe.remove()
    return value
  }
  const tokenColor = readToken('--ui-success')
  // 亮色下 sforum-components.css 把 .text-success 覆盖成 --sf-accent-strong（文本专用
  // token，解决 soft 表面上的对比度）。所以亮色要拿它比，暗色才是 --ui-success。
  const accentStrong = readToken('--sf-accent-strong')
  const first = toasts[0] || null
  const icon = first ? first.querySelector('span.iconify') : null
  return {
    count: toasts.length,
    dark: document.documentElement.classList.contains('dark'),
    tokenColor,
    accentStrong,
    texts: toasts.map(toast => (toast.innerText || '').trim().slice(0, 60)),
    iconColor: icon ? getComputedStyle(icon).color : null,
    iconClass: icon ? String(icon.className) : null
  }
}`

const PAGE_DIALOG_FACTS = `function () {
  const isShown = element => {
    const style = getComputedStyle(element)
    const rect = element.getBoundingClientRect()
    return style.display !== 'none' && style.visibility !== 'hidden' && style.opacity !== '0' && rect.width > 0 && rect.height > 0
  }
  const dialogs = [...document.querySelectorAll('[role="dialog"]')]
  const dialog = dialogs[0] || null
  const rect = dialog ? dialog.getBoundingClientRect() : null
  const active = document.activeElement
  return {
    count: dialogs.length,
    // reka-ui 关闭后会把节点留在 DOM 里跑退场动画，所以「关没关」看 data-state + 可见性，
    // 不看节点数。注意弹窗是 position: fixed，offsetParent 恒为 null，不能用它判可见。
    state: dialog ? dialog.getAttribute('data-state') : null,
    visible: dialog ? isShown(dialog) : false,
    ariaModal: dialog ? dialog.getAttribute('aria-modal') : null,
    openDialogs: dialogs.filter(node => node.getAttribute('data-state') !== 'closed' && isShown(node)).length,
    rect: rect ? { left: Math.round(rect.left), top: Math.round(rect.top), right: Math.round(rect.right), bottom: Math.round(rect.bottom) } : null,
    innerWidth,
    innerHeight,
    containsActive: Boolean(dialog && active && dialog.contains(active)),
    activeText: active ? (active.innerText || active.getAttribute('aria-label') || active.tagName || '').trim().slice(0, 24) : null,
    text: dialog ? (dialog.innerText || '').trim().slice(0, 60) : null
  }
}`

const PAGE_MENU_FACTS = `function () {
  const menus = [...document.querySelectorAll('[role="menu"]')]
  const menu = menus[0] || null
  const rect = menu ? menu.getBoundingClientRect() : null
  return {
    count: menus.length,
    rect: rect ? { left: Math.round(rect.left), top: Math.round(rect.top), right: Math.round(rect.right), bottom: Math.round(rect.bottom) } : null,
    innerWidth,
    innerHeight,
    items: menu ? menu.querySelectorAll('[role="menuitem"]').length : 0
  }
}`

const PAGE_POINT_BY_TEXT = `function (text, scope) {
  const root = scope ? document.querySelector(scope) : document
  if (!root) return null
  const candidates = [...root.querySelectorAll('button,a,[role="button"],[role="menuitem"],[role="tab"]')]
  const element = candidates.find(node => (node.innerText || '').trim() === text || node.getAttribute('aria-label') === text)
  if (!element) return null
  element.scrollIntoView({ block: 'center', inline: 'center' })
  const rect = element.getBoundingClientRect()
  return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + rect.height / 2), text: (element.innerText || element.getAttribute('aria-label') || '').trim().slice(0, 30) }
}`

const PAGE_POINT_BY_SELECTOR = `function (selector) {
  const element = document.querySelector(selector)
  if (!element) return null
  element.scrollIntoView({ block: 'center', inline: 'center' })
  const rect = element.getBoundingClientRect()
  return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + rect.height / 2) }
}`

const PAGE_TEXT_HITS = `function (needle) {
  const root = document.querySelector('#sforum-admin-main')
  if (!root) return []
  const hits = []
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  while (walker.nextNode()) {
    const node = walker.currentNode
    const value = node.nodeValue || ''
    if (!value.includes(needle)) continue
    const element = node.parentElement
    if (!element) continue
    hits.push({
      tag: element.tagName,
      cls: String(element.className || '').slice(0, 70),
      testid: element.getAttribute('data-testid'),
      inTablist: Boolean(element.closest('[role="tablist"]')),
      parentTag: element.parentElement ? element.parentElement.tagName : null,
      parentText: (element.parentElement ? (element.parentElement.innerText || '') : '').trim().slice(0, 70),
      text: value.trim().slice(0, 70)
    })
    if (hits.length >= 4) break
  }
  return hits
}`

const PAGE_SET_MARKER = `function () { window.__qaSpaMarker = 1; return true }`
const PAGE_READ_MARKER = `function () { return window.__qaSpaMarker === 1 }`
const PAGE_ACTIVE_ELEMENT = `function () {
  const active = document.activeElement
  return active ? { tag: active.tagName, text: (active.innerText || '').trim().slice(0, 24), aria: active.getAttribute('aria-label') } : null
}`

/* ── 页面驱动 ─────────────────────────────────────────────────────────── */

function makeDriver(send) {
  const handles = new WeakMap()

  const pageHandle = async () => {
    const cached = handles.get(send)
    if (cached) return cached
    const handle = await send('Runtime.evaluate', { expression: 'globalThis' })
    const objectId = handle.result?.objectId
    if (!objectId) throw new Error('Runtime.evaluate(globalThis) did not return an objectId')
    handles.set(send, objectId)
    return objectId
  }

  const invalidate = () => handles.delete(send)

  const callInPage = async (declaration, args = []) => {
    const invoke = async () => {
      const objectId = await pageHandle()
      return send('Runtime.callFunctionOn', {
        objectId,
        functionDeclaration: declaration,
        arguments: args.map(value => (value === undefined ? { unserializableValue: 'undefined' } : { value })),
        awaitPromise: true,
        returnByValue: true
      })
    }
    let result
    try {
      result = await invoke()
    } catch (error) {
      if (!/context/i.test(error?.message || '')) throw error
      invalidate()
      result = await invoke()
    }
    if (result.exceptionDetails) {
      const details = result.exceptionDetails
      const description = details.exception?.description || ''
      throw new Error(`callInPage failed: ${details.text}${description ? ` (${description.split('\n')[0]})` : ''} :: ${declaration.slice(0, 80)}`)
    }
    return result.result?.value
  }

  const waitForCall = async (declaration, args, label, timeoutMs = 30000) => {
    const deadline = Date.now() + timeoutMs
    while (Date.now() < deadline) {
      if (await callInPage(declaration, args)) return true
      await sleep(150)
    }
    throw new Error(`timeout waiting for ${label}`)
  }

  const setViewport = async viewport => {
    await send('Emulation.setDeviceMetricsOverride', {
      width: viewport.width, height: viewport.height, deviceScaleFactor: viewport.dpr, mobile: viewport.width < 600
    })
  }

  const clickPoint = async point => {
    if (!point) return false
    await send('Input.dispatchMouseEvent', { type: 'mousePressed', x: point.x, y: point.y, button: 'left', clickCount: 1 })
    await send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: point.x, y: point.y, button: 'left', clickCount: 1 })
    return true
  }

  const clickByText = async (text, scope) => clickPoint(await callInPage(PAGE_POINT_BY_TEXT, [text, scope ?? '']))
  const clickBySelector = async selector => clickPoint(await callInPage(PAGE_POINT_BY_SELECTOR, [selector]))

  const pressKey = async (key, code, windowsVirtualKeyCode) => {
    await send('Input.dispatchKeyEvent', { type: 'keyDown', key, code, windowsVirtualKeyCode })
    await send('Input.dispatchKeyEvent', { type: 'keyUp', key, code, windowsVirtualKeyCode })
  }
  const pressEscape = () => pressKey('Escape', 'Escape', 27)
  const pressTab = () => pressKey('Tab', 'Tab', 9)

  const navigate = async path => {
    invalidate()
    await send('Page.navigate', { url: `${web}${path}` })
    await waitForCall(`function () { return document.readyState === 'complete' }`, [], 'document complete', 60000)
    await waitForCall(`function () { return Boolean(document.querySelector('#sforum-admin-main')) }`, [], 'admin main container', 60000)
    await waitForCall(
      `function () { const main = document.querySelector('#sforum-admin-main'); return Boolean(main) && (main.innerText || '').trim().length > 10 }`,
      [], 'admin main content', 60000
    )
    // dev 模式下客户端 chunk 很大，水合可能比首屏文本晚几秒。没等水合就点按钮，
    // 点击只落在 DOM 上、Vue 的 @click 还没挂，于是「点了没反应」（Toast 那次就是这么丢的）。
    try {
      await waitForCall(
        `function () { return Boolean(document.querySelector('#sforum-admin-main')?.__vueParentComponent) }`,
        [], 'vue hydration of admin main', 20000
      )
    } catch {
      runNotes.push(`水合标记 20s 内未出现：${path}`)
    }
    await sleep(settleMs)
  }

  const screenshot = async (file) => {
    mkdirSync(dirname(file), { recursive: true })
    const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false })
    writeFileSync(file, Buffer.from(shot.data, 'base64'))
    screenshots.push(file)
  }

  return { send, callInPage, waitForCall, setViewport, clickPoint, clickByText, clickBySelector, pressEscape, pressTab, navigate, screenshot, invalidate }
}

/* ── 对比度 ───────────────────────────────────────────────────────────── */

function luminance([r, g, b]) {
  const channel = value => {
    const scaled = value / 255
    return scaled <= 0.03928 ? scaled / 12.92 : ((scaled + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

function contrastRatio(a, b) {
  const [high, low] = [luminance(a), luminance(b)].sort((left, right) => right - left)
  return (high + 0.05) / (low + 0.05)
}

/* ── 断言实现 ─────────────────────────────────────────────────────────── */

function installedVersions() {
  const appsWeb = join(repoRoot, 'apps/web')
  const readVersion = file => JSON.parse(readFileSync(file, 'utf8')).version
  const nuxtUiDir = realpathSync(join(appsWeb, 'node_modules/@nuxt/ui'))
  const nuxtUi = readVersion(join(nuxtUiDir, 'package.json'))
  // @nuxt/ui 的真实路径在 node_modules/.bun/<entry>/node_modules/@nuxt/ui，
  // 它的兄弟目录就是它实际解析到的 reka-ui（顶层没有 hoist 的 node_modules/reka-ui）。
  let reka = null
  const sibling = join(dirname(dirname(nuxtUiDir)), 'reka-ui')
  if (existsSync(sibling)) {
    try { reka = readVersion(join(realpathSync(sibling), 'package.json')) } catch { reka = null }
  }
  // bun 升级后旧版本目录可能残留（不参与解析，但要如实记账）。
  let staleReka = []
  const bunStore = join(appsWeb, 'node_modules/.bun')
  if (existsSync(bunStore)) {
    try {
      staleReka = readdirSync(bunStore)
        .filter(name => name.startsWith('reka-ui@'))
        .map(name => name.split('+')[0].replace('reka-ui@', ''))
        .filter(version => version !== reka)
    } catch { staleReka = [] }
  }
  return { nuxtUi, reka, staleReka }
}

async function checkPreflight(driver) {
  const versions = installedVersions()
  judge('P1', { viewport: '-', page: '-' }, versions.nuxtUi === '4.12.2' && versions.reka === '2.11.0',
    { nuxtUi: versions.nuxtUi, rekaUi: versions.reka, staleRekaUiDirs: versions.staleReka },
    versions.reka === '2.11.0'
      ? (versions.staleReka.length ? `附注：node_modules/.bun 里残留 ${versions.staleReka.join(', ')} 的旧目录（不参与解析，可清理）` : '')
      : 'reka-ui 解析到的版本与抬档目标不一致')
}

function checkViewport(viewport, facts) {
  const ok = viewport.id === 'D'
    ? facts.innerWidth === 1633 && facts.dpr === 1
    : facts.innerWidth === 390 && facts.dpr === 3
  judge('P2', { viewport: viewport.id, page: '-' }, ok, { innerWidth: facts.innerWidth, dpr: facts.dpr })
}

function checkPageBasics(page, viewport, facts, consoleErrors) {
  const meta = { viewport: viewport.id, page: page.id }
  judge('P3', meta, consoleErrors.length === 0, consoleErrors.slice(0, 3))
  judge('P4', meta, facts.lang === 'zh-CN', facts.lang)
  judge('2.1', meta, facts.doc.scrollWidth <= facts.doc.clientWidth, facts.doc)
  judge('2.2', meta, facts.main ? facts.main.scrollWidth <= facts.main.clientWidth + 1 : false, facts.main)
  judge('3.1', meta, facts.clipped.length === 0, facts.clipped.slice(0, 5))
  if (viewport.id === 'M') {
    const iconOnly = facts.rightButtons.filter(button => button.text === '')
    const missing = iconOnly.filter(button => !button.aria || !button.title)
    if (!facts.toolbar) skip('3.2', meta, '本页没有 [data-slot="right"] toolbar 槽位')
    else judge('3.2', meta, missing.length === 0, { iconOnly: iconOnly.length, missing: missing.slice(0, 4) })
  }
}

function checkSettingsGeometry(page, facts) {
  const meta = { viewport: 'D', page: page.id }
  const title = facts.h2[0]
  judge('1.1', meta,
    Boolean(title) && title.text === '站点设置' && title.size === '20px' && Number(title.weight) >= 700,
    { text: title?.text ?? null, size: title?.size ?? null, weight: title?.weight ?? null })

  const h2Count = facts.h2.length
  // 判据是「没有营销式大标题」：h1 === 0 且没有 >20px 文本。
  // h2 计数不作为判据 —— 活动 tab 的卡片标题本来就该是 h2（16px）；h2Texts 留作证据。
  judge('1.2', meta, facts.h1 === 0 && facts.bigText.length === 0,
    { h1: facts.h1, h2: h2Count, h2Texts: facts.h2.map(item => `${item.text}@${item.size}`), over20px: facts.bigText.slice(0, 3) },
    `判据为 h1 === 0 且无 >20px 文本；h2 = ${h2Count}（第 2 个是活动 tab 的卡片标题 ${facts.h2[1] ? facts.h2[1].size : 'n/a'}，不是营销大标题）`)

  const toolbar = facts.toolbar
  judge('1.3', meta,
    Boolean(toolbar) && toolbar.hasRight && toolbar.borderTopWidth === '1px' && toolbar.borderTopColor !== 'rgba(0, 0, 0, 0)',
    toolbar)

  const tablists = facts.tablists
  const settingsTablist = tablists.filter(item => item.label === '设置分类')
  judge('1.4', meta, settingsTablist.length === 1 && settingsTablist[0].fixed === 8 && facts.fixedTabs === 8,
    { tablistCount: tablists.length, tablists, settingsTablist, fixedTabs: facts.fixedTabs },
    'KNOWN GAP（spec 表述）：后台 shell 本来就有 SFAdminTabStrip（aria-label「已打开的后台页面」），所以 [role=tablist] 全局计数恒为 2；「设置分类」这一个才是设置页的固定 tab 系统，唯一且 fixed = 8')

  judge('1.5', meta, facts.panels === 1, facts.panels)
}

function checkIcons(page, facts) {
  const meta = { viewport: 'D', page: page.id }
  judge('7.1', meta, facts.emojiHits.length === 0, facts.emojiHits.slice(0, 5))
  judge('7.2', meta, facts.iconifyCount > 0 && facts.iconifyNonLibrary === 0,
    { total: facts.iconifyCount, nonLibrary: facts.iconifyNonLibrary, specLiteralMismatch: facts.iconifyLegacyLiteral },
    facts.iconifyLegacyLiteral > 0
      ? `spec 写的是 i-lucide-* / i-tabler-*，实测类名是 i-lucide:* / i-tabler:*（Nuxt Icon 冒号写法）；按「图标来自 lucide/tabler」的意图判定，字面匹配会命中 ${facts.iconifyLegacyLiteral}/${facts.iconifyCount}`
      : '')
  judge('7.3', meta, facts.svgNotHidden === 0, facts.svgNotHidden)
}

async function checkTabSwitching(driver) {
  await driver.navigate('/control-panel/settings?tab=basic')
  for (const tab of SETTINGS_TABS) {
    const meta = { viewport: 'D', page: `settings:${tab.id}` }
    await driver.callInPage(PAGE_SET_MARKER)
    const clicked = await driver.clickBySelector(`[role="tab"][data-sf-fixed-tab-id="${tab.id}"]`)
    if (!clicked) {
      fail('4.1', meta, { clicked: false }, `找不到 tab=${tab.id} 的按钮`)
      continue
    }
    let synced = false
    const deadline = Date.now() + 8000
    while (Date.now() < deadline) {
      const state = await driver.callInPage(`function (tabId) {
        return {
          query: new URLSearchParams(location.search).get('tab'),
          selected: [...document.querySelectorAll('[role="tab"][data-sf-fixed-tab-id][aria-selected="true"]')].map(tab => tab.dataset.sfFixedTabId),
          selectedAll: [...document.querySelectorAll('[role="tab"][aria-selected="true"]')].length
        }
      }`, [tab.id])
      if (state.query === tab.id && state.selected.length === 1 && state.selected[0] === tab.id) {
        synced = true
        judge('4.1', meta, true, state, state.selectedAll > 1 ? `全局 aria-selected=true 有 ${state.selectedAll} 个（侧栏导航也是 tablist），固定 tab 内唯一` : '')
        break
      }
      await sleep(200)
    }
    if (!synced) {
      const state = await driver.callInPage(`function () {
        return {
          query: new URLSearchParams(location.search).get('tab'),
          selected: [...document.querySelectorAll('[role="tab"][data-sf-fixed-tab-id][aria-selected="true"]')].map(tab => tab.dataset.sfFixedTabId)
        }
      }`)
      fail('4.1', meta, state)
    }
    await sleep(400)
    const marker = await driver.callInPage(PAGE_READ_MARKER)
    judge('4.3', meta, marker === true, marker)

    const facts = await driver.callInPage(PAGE_FACTS)
    const text = facts.panelText || ''
    const present = text.includes(tab.anchor)
    const foreign = SETTINGS_TABS.filter(other => other.id !== tab.id && text.includes(other.anchor)).map(other => other.id)
    const foreignHits = []
    for (const foreignId of foreign) {
      const anchorText = SETTINGS_TABS.find(item => item.id === foreignId).anchor
      const hits = await driver.callInPage(PAGE_TEXT_HITS, [anchorText])
      foreignHits.push({ id: foreignId, anchor: anchorText, hits })
    }
    judge('4.2', meta, present && foreign.length === 0,
      { anchor: tab.anchor, present, foreignAnchors: foreign, foreignHits, scope: 'main-minus-tablist' })
  }

  // 4.4 深链直达
  await driver.navigate('/control-panel/settings?tab=verification')
  const deep = await driver.callInPage(`function () {
    return {
      query: new URLSearchParams(location.search).get('tab'),
      selected: [...document.querySelectorAll('[role="tab"][data-sf-fixed-tab-id][aria-selected="true"]')].map(tab => tab.dataset.sfFixedTabId),
      hasAnchor: (document.querySelector('#sforum-admin-main').innerText || '').includes('配置注册等风险操作使用的验证码服务')
    }
  }`)
  judge('4.4', { viewport: 'D', page: 'settings:verification' },
    deep.query === 'verification' && deep.selected.length === 1 && deep.selected[0] === 'verification' && deep.hasAnchor, deep)
}

async function checkDark(driver) {
  await driver.setViewport(DESKTOP)
  await driver.navigate('/control-panel/settings?tab=basic')
  const before = await driver.callInPage(`function () {
    const trigger = [...document.querySelectorAll('button')].find(button => (button.getAttribute('aria-label') || '').startsWith('外观'))
    return trigger ? trigger.getAttribute('aria-label') : null
  }`)
  let clicks = 0
  for (; clicks < 4; clicks += 1) {
    const isDark = await driver.callInPage(`function () { return document.documentElement.classList.contains('dark') }`)
    if (isDark) break
    const point = await driver.callInPage(PAGE_POINT_BY_TEXT, [before || '', ''])
    const ariaPoint = point || await driver.callInPage(`function (prefix) {
      const trigger = [...document.querySelectorAll('button')].find(button => (button.getAttribute('aria-label') || '').startsWith(prefix))
      if (!trigger) return null
      trigger.scrollIntoView({ block: 'center' })
      const rect = trigger.getBoundingClientRect()
      return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + rect.height / 2) }
    }`, ['外观'])
    if (!ariaPoint) break
    await driver.clickPoint(ariaPoint)
    await sleep(900)
  }

  for (const page of [PAGES[0], PAGES.find(item => item.id === 'users')]) {
    const meta = { viewport: 'D', page: page.id }
    if (page.id !== 'settings:basic') await driver.navigate(page.path)
    else await driver.navigate(page.path)
    const facts = await driver.callInPage(PAGE_THEME_FACTS)
    judge('5.1', meta, facts.dark === true, facts.dark)

    const cardBg = facts.cardBg
    const pageBg = facts.mainEffectiveBg || facts.bodyBg
    judge('5.2', meta, Boolean(cardBg) && cardBg !== pageBg && cardBg !== facts.mainBg,
      { cardBg, mainBg: facts.mainBg, mainEffectiveBg: pageBg },
      facts.mainBg === 'rgba(0, 0, 0, 0)' ? 'main 本身背景透明，实际分层对象是 body；按有效背景比较' : '')

    const border = facts.cardBorder
    let borderOk = false
    let borderDetail = border
    if (border && border.color && cardBg) {
      const [borderRgb, cardRgb] = await driver.callInPage(PAGE_RESOLVE_COLORS, [[border.color, cardBg]])
      borderDetail = { ...border, ratio: Number(contrastRatio(borderRgb.rgb, cardRgb.rgb).toFixed(2)), cardBg }
      borderOk = border.color !== cardBg && contrastRatio(borderRgb.rgb, cardRgb.rgb) >= 1.15
    }
    judge('5.3', meta, borderOk, borderDetail)

    if (!facts.muted.length) {
      skip('5.4', meta, '本页没有可见的 p/td/.text-muted 文本')
      continue
    }
    const colors = await driver.callInPage(PAGE_RESOLVE_COLORS, [facts.muted.map(item => item.color).concat([facts.mainEffectiveBg || facts.bodyBg])])
    const background = colors[colors.length - 1]
    const samples = facts.muted.map((item, index) => {
      const ratio = Number(contrastRatio(colors[index].rgb, background.rgb).toFixed(2))
      const threshold = item.size <= 12 ? 3.0 : 4.5
      return { text: item.text, size: item.size, color: item.color, ratio, threshold, ok: ratio >= threshold }
    })
    const bad = samples.filter(sample => !sample.ok)
    judge('5.4', meta, bad.length === 0, { background: background.resolved, samples: samples.slice(0, 6), belowThreshold: bad.slice(0, 4) })
  }

  await driver.screenshot(join(qaDir, 'dark-settings-basic-D.png'))

  // 恢复原外观偏好，别把开发环境留在暗色
  for (let attempt = 0; attempt < 4; attempt += 1) {
    const label = await driver.callInPage(`function () {
      const trigger = [...document.querySelectorAll('button')].find(button => (button.getAttribute('aria-label') || '').startsWith('外观'))
      return trigger ? trigger.getAttribute('aria-label') : null
    }`)
    if (!before || label === before) break
    const point = await driver.callInPage(`function (prefix) {
      const trigger = [...document.querySelectorAll('button')].find(button => (button.getAttribute('aria-label') || '').startsWith(prefix))
      if (!trigger) return null
      trigger.scrollIntoView({ block: 'center' })
      const rect = trigger.getBoundingClientRect()
      return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + rect.height / 2) }
    }`, ['外观'])
    if (!point) break
    await driver.clickPoint(point)
    await sleep(700)
  }
  runNotes.push(`暗色切换：点击「外观」循环按钮 ${clicks} 次后进入 dark（原偏好 ${before || '未知'}，结束后已尝试恢复）`)
}

async function checkToast(driver) {
  await driver.setViewport(DESKTOP)
  await driver.navigate('/control-panel/settings?tab=basic')
  const meta = { viewport: 'D', page: 'settings:basic' }
  await driver.waitForCall(
    `function () {
      return [...document.querySelectorAll('#sforum-admin-main button')].some(node => (node.innerText || '').trim() === '保存设置')
    }`,
    [], '保存设置按钮', 60000
  )
  const clicked = await driver.clickByText('保存设置', '#sforum-admin-main')
  if (!clicked) {
    fail('6.1', meta, { clicked: false }, '找不到「保存设置」按钮')
    return
  }
  let facts = null
  let probeError = ''
  const deadline = Date.now() + 3000
  while (Date.now() < deadline) {
    try {
      facts = await driver.callInPage(PAGE_TOAST_FACTS)
    } catch (error) {
      probeError = String(error?.message || error)
      facts = null
    }
    if (facts && facts.count > 0) break
    await sleep(200)
  }
  if (probeError && !facts) throw new Error(`toast 探针异常：${probeError}`)
  judge('6.1', meta, Boolean(facts) && facts.count >= 1, { count: facts?.count ?? 0, texts: facts?.texts ?? [] })
  if (!facts || facts.count === 0) {
    skip('6.2', meta, '没有 toast，无法比对图标色')
    skip('6.3', meta, '没有 toast，无法比对图标色')
    skip('6.4', meta, '没有 toast，无法比对图标色')
    return
  }
  const effectiveToken = facts.dark ? facts.tokenColor : facts.accentStrong
  const tokenName = facts.dark ? '--ui-success' : '--sf-accent-strong'
  const colors = await driver.callInPage(PAGE_RESOLVE_COLORS, [[effectiveToken, facts.iconColor || effectiveToken]])
  const token = colors[0]
  const icon = colors[1]
  judge('6.2', meta, Boolean(facts.iconColor) && icon.rgb.join(',') === token.rgb.join(','),
    { tokenName, tokenColor: effectiveToken, uiSuccess: facts.tokenColor, accentStrong: facts.accentStrong, iconColor: facts.iconColor, tokenRgb: token.rgb, iconRgb: icon.rgb },
    facts.dark ? '' : '亮色下 .text-success 被 sforum-components.css:1045 覆盖为 --sf-accent-strong（文本专用 token），所以比对的是它而不是 --ui-success')

  const iconResolved = `rgb(${icon.rgb.join(', ')})`
  judge('6.3', meta, !DEFAULT_GREEN.includes(iconResolved), iconResolved)

  const expectedDefault = facts.dark ? 'rgb(45, 212, 191)' : 'rgb(15, 118, 110)'
  const expectedRgb = await driver.callInPage(PAGE_RESOLVE_COLORS, [[expectedDefault]])
  const matchesDefault = expectedRgb[0].rgb.join(',') === icon.rgb.join(',')
  pushResult({
    id: '6.4', title: TITLES['6.4'], status: matchesDefault ? 'pass' : 'fail', kind: matchesDefault ? '' : 'known-gap',
    expected: EXPECTED['6.4'], viewport: 'D', page: 'settings:basic',
    actual: { iconColor: iconResolved, expectedDefault, tokenSource: tokenName, uiSuccess: facts.tokenColor, accentStrong: facts.accentStrong },
    note: matchesDefault ? '' : `KNOWN GAP：本实例 --ui-success = ${facts.tokenColor}（= --sf-primary-500 #2563eb），不是 spec 假设的默认 teal。令牌映射机制成立（6.2 通过），但「默认主题期望值」在这套主题下不适用`
  })
  await driver.screenshot(join(qaDir, 'toast-settings-basic-D.png'))
}

async function checkDialogs(driver) {
  await driver.setViewport(MOBILE)
  await driver.navigate('/control-panel/roles')
  const meta = { viewport: 'M', page: 'roles' }

  const openByText = async (label) => {
    await driver.waitForCall(
      `function (label) {
        return [...document.querySelectorAll('#sforum-admin-main button')].some(node => (node.innerText || '').trim() === label || (node.getAttribute('aria-label') || '').trim() === label)
      }`,
      [label], `${label} 按钮`, 30000
    )
    return driver.callInPage(`function (label) {
      const button = [...document.querySelectorAll('#sforum-admin-main button')].find(node => (node.innerText || '').trim() === label || (node.getAttribute('aria-label') || '').trim() === label)
      if (!button) return null
      button.scrollIntoView({ block: 'center' })
      window.__qaTrigger = button
      const rect = button.getBoundingClientRect()
      return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + rect.height / 2) }
    }`, [label])
  }

  const trigger = await openByText('新建用户组')
  if (!trigger) {
    fail('8.1', meta, { trigger: false }, 'roles 页找不到「新建用户组」按钮')
    return
  }
  await driver.clickPoint(trigger)
  await sleep(1200)
  let dialog = await driver.callInPage(PAGE_DIALOG_FACTS)
  judge('8.1', meta, dialog.openDialogs === 1 && dialog.state !== 'closed', { opened: dialog.openDialogs, state: dialog.state })

  if (dialog.openDialogs === 1 && dialog.rect) {
    const inside = dialog.rect.left >= -1 && dialog.rect.top >= -1 && dialog.rect.right <= dialog.innerWidth + 1 && dialog.rect.bottom <= dialog.innerHeight + 1
    judge('8.2', meta, inside, { rect: dialog.rect, viewport: [dialog.innerWidth, dialog.innerHeight] })
    judge('8.6', meta, dialog.ariaModal === 'true', { ariaModal: dialog.ariaModal, role: 'dialog' },
      dialog.ariaModal === 'true' ? '' : 'role="dialog" 上缺 aria-modal="true"：读屏软件不会把弹窗外内容当背景忽略')
    await driver.screenshot(join(qaDir, 'dialog-roles-M.png'))

    const trap = []
    for (let index = 0; index < 6; index += 1) {
      await driver.pressTab()
      await sleep(120)
      const state = await driver.callInPage(PAGE_DIALOG_FACTS)
      trap.push({ step: index + 1, inside: state.containsActive, active: state.activeText })
    }
    judge('8.3', meta, trap.every(step => step.inside), trap)
  } else {
    skip('8.2', meta, '弹窗没有打开，无法测量')
    skip('8.3', meta, '弹窗没有打开，无法测焦点')
    skip('8.6', meta, '弹窗没有打开，无法测 aria-modal')
  }

  await driver.pressEscape()
  await sleep(900)
  dialog = await driver.callInPage(PAGE_DIALOG_FACTS)
  judge('8.1', meta, dialog.openDialogs === 0 || dialog.state === 'closed',
    { openDialogs: dialog.openDialogs, state: dialog.state, visible: dialog.visible })
  const restored = await driver.callInPage(`function () {
    const trigger = window.__qaTrigger
    return {
      same: Boolean(trigger) && document.activeElement === trigger,
      triggerVisible: Boolean(trigger) && trigger.offsetParent !== null,
      active: document.activeElement ? (document.activeElement.innerText || document.activeElement.tagName || '').trim().slice(0, 24) : null,
      activeTag: document.activeElement ? document.activeElement.tagName : null
    }
  }`)
  judge('8.4', meta, restored.same === true, restored,
    restored.same === true ? '' : 'WAI-ARIA APG 要求模态 dialog 关闭后焦点回到触发元素；实测焦点落在别处')

  // 8.4 补测：行内「编辑」按钮这条触发路径（与「新建用户组」不是同一个组件入口）
  const rowTrigger = await openByText('编辑')
  if (rowTrigger) {
    await driver.clickPoint(rowTrigger)
    await sleep(1000)
    await driver.pressEscape()
    await sleep(900)
    const rowRestored = await driver.callInPage(`function () {
      const trigger = window.__qaTrigger
      return {
        same: Boolean(trigger) && document.activeElement === trigger,
        active: document.activeElement ? (document.activeElement.innerText || document.activeElement.tagName || '').trim().slice(0, 24) : null
      }
    }`)
    pushResult({
      id: '8.4b', title: '关闭后焦点归还（行内「编辑」入口）', status: rowRestored.same ? 'pass' : 'fail',
      expected: 'Escape 后 activeElement 是打开前的「编辑」按钮', viewport: 'M', page: 'roles',
      actual: rowRestored, note: rowRestored.same ? '' : '行内编辑弹窗关闭后焦点没有回到触发的「编辑」按钮'
    })
  } else {
    skip('8.4b', meta, 'roles 页找不到行内「编辑」按钮')
  }

  // 8.5 行内 dropdown
  await driver.setViewport(DESKTOP)
  await driver.navigate('/control-panel/extensions')
  const menuMeta = { viewport: 'D', page: 'extensions' }
  const menuTrigger = await driver.callInPage(`function () {
    const button = [...document.querySelectorAll('#sforum-admin-main button')].find(node => (node.getAttribute('aria-label') || '').startsWith('更多操作'))
    if (!button) return null
    button.scrollIntoView({ block: 'center' })
    const rect = button.getBoundingClientRect()
    return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + rect.height / 2) }
  }`)
  if (!menuTrigger) {
    skip('8.5', menuMeta, 'extensions 页找不到「更多操作」按钮')
    return
  }
  await driver.clickPoint(menuTrigger)
  await sleep(1000)
  const menu = await driver.callInPage(PAGE_MENU_FACTS)
  const inViewport = menu.rect
    ? menu.rect.left >= -1 && menu.rect.top >= -1 && menu.rect.right <= menu.innerWidth + 1 && menu.rect.bottom <= menu.innerHeight + 1
    : false
  judge('8.5', menuMeta, menu.count >= 1 && inViewport, { count: menu.count, rect: menu.rect, viewport: [menu.innerWidth, menu.innerHeight], items: menu.items })
  await driver.screenshot(join(qaDir, 'menu-extensions-D.png'))
  await driver.pressEscape()
}

/* ── 主流程 ───────────────────────────────────────────────────────────── */

// 每个后置阶段独立兜底：某个探针炸了只影响它自己那几条断言，
// 不允许把 5/6/8 整组悄悄吞掉（曾经就是 Toast 探针异常把三组一起带走了）。
async function runPhase(name, ids, meta, run) {
  try {
    await run()
  } catch (error) {
    const message = String(error?.message || error)
    runNotes.push(`${name} 阶段异常：${message}`)
    for (const id of ids) {
      if (results.some(entry => entry.id === id)) continue
      fail(id, meta, { error: message.slice(0, 300) }, `${name} 阶段异常，未取得实测值：${message.slice(0, 120)}`)
    }
  }
}

function summarize() {
  const counts = { pass: 0, fail: 0, skipped: 0, knownGap: 0 }
  for (const entry of results) {
    counts[entry.status] += 1
    if (entry.kind === 'known-gap') counts.knownGap += 1
  }
  return counts
}

function renderMarkdown(env) {
  const counts = summarize()
  const lines = []
  lines.push('## 管理端视觉回归 QA（#238）实测结果')
  lines.push('')
  lines.push(`- 环境：\`${env.web}\`（web dev）· API safe mode · 依赖 @nuxt/ui ${env.versions.nuxtUi} / reka-ui ${env.versions.rekaUi}`)
  lines.push(`- 视口：D = 1633×810@1x，M = 390×844@3x`)
  lines.push(`- 结果：**pass ${counts.pass} / fail ${counts.fail} / skipped ${counts.skipped}**（fail 中 ${counts.knownGap} 条是 spec 与当前代码不符的 KNOWN GAP，见下节）`)
  lines.push('')
  const order = ['P', '1', '2', '3', '4', '5', '6', '7', '8']
  for (const group of order) {
    const entries = results.filter(entry => entry.id.split('.')[0] === group)
    if (!entries.length) continue
    lines.push(`### ${group}. 断言`)
    lines.push('')
    lines.push('| # | 断言 | 视口 | 页面 | 结果 | 实测 | 备注 |')
    lines.push('|---|---|---|---|---|---|---|')
    for (const entry of entries) {
      const actual = typeof entry.actual === 'object' && entry.actual !== null ? `\`${JSON.stringify(entry.actual).slice(0, 220)}\`` : `\`${String(entry.actual)}\``
      lines.push(`| ${entry.id} | ${entry.title} | ${entry.viewport} | ${entry.page} | ${entry.status === 'pass' ? 'PASS' : entry.status === 'fail' ? '**FAIL**' : 'skipped'} | ${actual} | ${entry.note || ''} |`)
    }
    lines.push('')
  }
  const knownGaps = results.filter(entry => entry.kind === 'known-gap')
  lines.push('### KNOWN GAP（断言按 spec 字面不成立，需 PM/Lead 决策）')
  lines.push('')
  if (knownGaps.length === 0) lines.push('- 无')
  else for (const entry of knownGaps) lines.push(`- **${entry.id}** ${entry.title}（${entry.page} / ${entry.viewport}）：${entry.note} 实测 \`${JSON.stringify(entry.actual).slice(0, 240)}\``)
  lines.push('')
  const skippedSafeMode = results.filter(entry => entry.status === 'skipped' && /safe mode/i.test(entry.note))
  lines.push('### skipped（safe mode）')
  lines.push('')
  lines.push(skippedSafeMode.length
    ? skippedSafeMode.map(entry => `- ${entry.id} ${entry.title}：${entry.note}`).join('\n')
    : '- 设置页「插件设置分区」（运行时扩展 tab）—— safe mode 下插件不加载，取不到该分区，未计入 pass。')
  lines.push('')
  lines.push('### 人眼判断项（未伪装成断言）')
  lines.push('')
  lines.push('1. tab 栏在 390 下换行后的视觉密度 —— 见 `tabstrip-M` 截图')
  lines.push('2. 暗色下卡片阴影与边框的层次 —— 见 `dark-settings-basic-D` 截图')
  lines.push('3. Toast 是否遮挡底部按钮 —— 见 `toast-settings-basic-D` 截图')
  lines.push('4. 插件设置分区在 390 下的字段排布 —— safe mode 取不到，skipped')
  lines.push('')
  if (runNotes.length) {
    lines.push('### 运行备注')
    lines.push('')
    for (const note of runNotes) lines.push(`- ${note}`)
  }
  return lines.join('\n')
}

async function main() {
  const versions = installedVersions()
  const env = { web, versions: { nuxtUi: versions.nuxtUi, rekaUi: versions.reka } }
  mkdirSync(qaDir, { recursive: true })

  const binary = chromeBinary()
  if (!binary) throw new Error('no Chrome/Chromium binary found; set SFORUM_CHROME')
  const { child, port } = await launchChrome(binary)
  let driver = null
  try {
    const connection = await connect(port)
    driver = makeDriver(connection.send)
    checkPreflight(driver)

    for (const viewport of onlyPhases ? [] : [DESKTOP, MOBILE]) {
      await driver.setViewport(viewport)
      checkViewport(viewport, await driver.callInPage(`function () { return { innerWidth, dpr: devicePixelRatio } }`))
      for (const page of PAGES) {
        connection.consoleErrors.length = 0
        await driver.navigate(page.path)
        const facts = await driver.callInPage(PAGE_FACTS)
        checkPageBasics(page, viewport, facts, connection.consoleErrors.slice())
        if (page.kind === 'settings' && viewport.id === 'D') checkSettingsGeometry(page, facts)
        if (page.id === 'settings:basic' && viewport.id === 'D') checkIcons(page, facts)
        await driver.screenshot(join(qaDir, `${page.id.replace(':', '-')}-${viewport.id}.png`))
      }
      if (viewport.id === 'D') await checkTabSwitching(driver)
    }

    if (process.env.SFORUM_SKIP_TOAST !== '1') {
      await runPhase('Toast', ['6.1', '6.2', '6.3', '6.4'], { viewport: 'D', page: 'settings:basic' }, () => checkToast(driver))
    } else {
      for (const id of ['6.1', '6.2', '6.3', '6.4']) skip(id, { viewport: 'D', page: 'settings:basic' }, 'SFORUM_SKIP_TOAST=1')
    }
    if (process.env.SFORUM_SKIP_DARK !== '1') {
      await runPhase('暗色', ['5.1', '5.2', '5.3', '5.4'], { viewport: 'D', page: 'settings:basic' }, () => checkDark(driver))
    } else {
      for (const id of ['5.1', '5.2', '5.3', '5.4']) skip(id, { viewport: 'D', page: 'settings:basic' }, 'SFORUM_SKIP_DARK=1')
    }
    if (process.env.SFORUM_SKIP_DIALOG !== '1') {
      await runPhase('弹窗', ['8.1', '8.2', '8.3', '8.4', '8.4b', '8.5', '8.6'], { viewport: 'M', page: 'roles' }, () => checkDialogs(driver))
    } else {
      for (const id of ['8.1', '8.2', '8.3', '8.4', '8.4b', '8.5', '8.6']) skip(id, { viewport: 'M', page: 'roles' }, 'SFORUM_SKIP_DIALOG=1')
    }

    skip('settings:plugin-tabs', { viewport: 'D', page: 'settings:plugin-tabs' }, 'skipped (safe mode)：插件不加载，运行时扩展 tab 取不到，不计 pass')
  } catch (error) {
    runNotes.push(`运行中断：${error?.message || error}`)
    fail('P0', { viewport: '-', page: '-' }, { error: String(error?.message || error) })
  } finally {
    try { driver?.send && await driver.send('Browser.close') } catch { /* ignore */ }
    try { child.kill('SIGKILL') } catch { /* ignore */ }
  }

  const counts = summarize()
  const evidence = {
    generatedAt: new Date().toISOString(),
    web,
    viewports: { D: DESKTOP, M: MOBILE },
    versions: env.versions,
    counts,
    screenshots,
    assertions: results
  }
  const markdown = renderMarkdown(env)
  if (evidencePath) {
    mkdirSync(dirname(evidencePath), { recursive: true })
    writeFileSync(evidencePath, `${JSON.stringify(evidence, null, 2)}\n`)
    writeFileSync(evidencePath.replace(/\.json$/, '') + '.md', `${markdown}\n`)
  }
  console.log(markdown)
  console.error(`\n[admin-visual-qa] pass ${counts.pass} / fail ${counts.fail} (known-gap ${counts.knownGap}) / skipped ${counts.skipped}`)
  if (counts.fail > 0) process.exitCode = 1
}

await main()
