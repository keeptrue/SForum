import { describe, expect, test } from 'bun:test'

import {
  applyClientIPHeaders,
  normalizeUpstreamClientIP,
  resolveEdgeClientIP
} from '../../server/utils/clientIp'

// web 层只做「入口头 → 单值」的规范化。运行时拿不到 TCP 对端（Nitro 的 mock socket
// remoteAddress 恒为空串），所以这里不锁定任何 peer 语义——那套判定在真实管线里是死代码，
// 还会把入口代理写好的真实 IP 删掉。信任边界由部署拓扑保证：web 只在 loopback/内网发布。
const scenarios: Array<{
  name: string
  headers: Record<string, string>
  expected: string
}> = [
  {
    name: 'x-real-ip 是入口代理的权威值，优先于 XFF',
    headers: {
      'cf-connecting-ip': '203.0.113.7',
      'true-client-ip': '203.0.113.8',
      'x-real-ip': '203.0.113.9',
      'x-forwarded-for': '198.51.100.7, 10.9.9.9'
    },
    expected: '203.0.113.9'
  },
  {
    name: '没有 x-real-ip 时取 XFF 最右有效项（最近一跳代理追加的值）',
    headers: { 'x-forwarded-for': '198.51.100.7, 10.9.9.9' },
    expected: '10.9.9.9'
  },
  {
    name: 'XFF 最右条目非法时继续向左取',
    headers: { 'x-forwarded-for': '198.51.100.7, not-an-ip,  ' },
    expected: '198.51.100.7'
  },
  {
    name: 'x-real-ip 非法时回退 XFF',
    headers: { 'x-real-ip': 'unknown', 'x-forwarded-for': '198.51.100.7' },
    expected: '198.51.100.7'
  },
  {
    name: 'CDN 单值头在这里一律不采信（由 Caddy 负责解析）',
    headers: { 'cf-connecting-ip': '203.0.113.7', 'true-client-ip': '203.0.113.8' },
    expected: ''
  },
  {
    name: '带端口的 x-real-ip 剥端口',
    headers: { 'x-real-ip': '203.0.113.9:443' },
    expected: '203.0.113.9'
  },
  {
    name: '方括号 IPv6 带端口剥壳',
    headers: { 'x-real-ip': '[2001:db8::7]:8443' },
    expected: '2001:db8::7'
  },
  {
    name: 'IPv4-mapped IPv6 归一成 IPv4',
    headers: { 'x-real-ip': '::ffff:203.0.113.9' },
    expected: '203.0.113.9'
  },
  {
    name: 'IPv6 地址按 RFC 5952 压缩输出',
    headers: { 'x-real-ip': '2001:DB8:0:0:0:0:0:1' },
    expected: '2001:db8::1'
  },
  {
    name: '没有任何客户端 IP 头时返回空串',
    headers: {},
    expected: ''
  },
  {
    name: '两个头都不可解析时返回空串',
    headers: { 'x-real-ip': 'unknown', 'x-forwarded-for': 'also-unknown' },
    expected: ''
  }
]

describe('edge client IP resolution', () => {
  for (const scenario of scenarios) {
    test(scenario.name, () => {
      expect(resolveEdgeClientIP(new Headers(scenario.headers))).toBe(scenario.expected)
    })
  }
})

describe('client IP upstream headers', () => {
  test('覆盖成单值并删除其余可伪造的客户端 IP 头', () => {
    const headers = new Headers({
      'cf-connecting-ip': '203.0.113.7',
      'true-client-ip': '203.0.113.8',
      'x-real-ip': '203.0.113.9',
      'x-forwarded-for': '198.51.100.7, 10.9.9.9'
    })

    applyClientIPHeaders(headers, '198.51.100.20')

    expect(headers.get('x-forwarded-for')).toBe('198.51.100.20')
    expect(headers.get('x-real-ip')).toBe('198.51.100.20')
    expect(headers.get('cf-connecting-ip')).toBeNull()
    expect(headers.get('true-client-ip')).toBeNull()
  })

  test('解析失败时删除全部四个头，不转发未经校验的声明', () => {
    const headers = new Headers({
      'cf-connecting-ip': '203.0.113.7',
      'true-client-ip': '203.0.113.8',
      'x-real-ip': '203.0.113.9',
      'x-forwarded-for': '198.51.100.7'
    })

    applyClientIPHeaders(headers, '')

    expect([...headers.keys()]).toEqual([])
  })

  test('normalizeUpstreamClientIP 从入口头一路收敛到单值', () => {
    const headers = new Headers({
      'cf-connecting-ip': '203.0.113.7',
      'true-client-ip': '203.0.113.8',
      'x-real-ip': '::ffff:203.0.113.9',
      'x-forwarded-for': '198.51.100.7, 10.9.9.9'
    })

    normalizeUpstreamClientIP(headers)

    expect(headers.get('x-real-ip')).toBe('203.0.113.9')
    expect(headers.get('x-forwarded-for')).toBe('203.0.113.9')
    expect(headers.get('cf-connecting-ip')).toBeNull()
    expect(headers.get('true-client-ip')).toBeNull()
  })

  test('normalizeUpstreamClientIP 在入口头缺失时保持 fail-closed', () => {
    const headers = new Headers({ 'cf-connecting-ip': '203.0.113.7' })

    normalizeUpstreamClientIP(headers)

    expect([...headers.keys()]).toEqual([])
  })
})
