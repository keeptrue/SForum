// 一次性验收账号：注册 → 输出 id + sforum_session（供 CDP Network.setCookie 使用）。
// 只服务 G-05（390 无横向溢出）这一条 pend 转 ok；跑完必须删干净（见 cleanup.sql）。
import { writeFileSync } from 'node:fs'

const api = process.env.SFORUM_API_BASE_URL || 'http://127.0.0.1:8080'
const webOrigin = process.env.SFORUM_WEB_ORIGIN || 'http://127.0.0.1:3000'
const out = process.env.SFORUM_ACCOUNT_OUT || 'account.json'
const stamp = Date.now().toString(36)
const username = `qa_pend_${stamp}`
const email = `${username}@example.test`
const password = `Qa!${stamp}Aa1`

const jar = new Map()

function absorb(response) {
  const values = typeof response.headers.getSetCookie === 'function'
    ? response.headers.getSetCookie()
    : [response.headers.get('set-cookie')].filter(Boolean)
  for (const value of values) {
    const pair = value.split(';', 1)[0]
    const index = pair.indexOf('=')
    if (index > 0) jar.set(pair.slice(0, index), pair.slice(index + 1))
  }
}

async function request(path, { method = 'GET', json } = {}) {
  const headers = {
    Origin: webOrigin,
    Referer: `${webOrigin}/`,
    Accept: 'application/json'
  }
  if (jar.size > 0) headers.Cookie = [...jar.entries()].map(([name, value]) => `${name}=${value}`).join('; ')
  if (json !== undefined) {
    headers['Content-Type'] = 'application/json'
    if (method !== 'GET' && method !== 'HEAD') headers['X-Csrf-Token'] = jar.get('csrf_') || ''
  }
  const response = await fetch(`${api}${path}`, { method, headers, body: json === undefined ? undefined : JSON.stringify(json) })
  absorb(response)
  const text = await response.text()
  let body = null
  try { body = text ? JSON.parse(text) : null } catch { body = text }
  return { status: response.status, body }
}

// 先打一个 GET 让 double-submit 的 csrf_ cookie 落地。
const seed = await request('/api/v1/ready')
const register = await request('/api/v1/auth/register', {
  method: 'POST',
  json: {
    username,
    email,
    password,
    displayName: 'QA Pend Conversion',
    locale: 'zh-CN',
    humanVerification: { provider: 'disabled', token: '' }
  }
})

const session = jar.get('sforum_session') || ''
const account = {
  username,
  email,
  password,
  userId: register.body?.data?.id ?? null,
  registerStatus: register.status,
  readyStatus: seed.status,
  csrfCookiePresent: jar.has('csrf_'),
  sessionCookieLength: session.length,
  sessionCookie: session,
  registerBody: register.status === 201 ? { id: register.body?.data?.id ?? null } : register.body
}
writeFileSync(out, `${JSON.stringify(account, null, 2)}\n`, 'utf8')
console.log(JSON.stringify({ out, registerStatus: register.status, userId: account.userId, username, sessionCookieLength: session.length }))
if (register.status !== 201) {
  console.error('register did not return 201')
  process.exitCode = 1
}
