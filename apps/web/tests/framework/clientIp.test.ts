import { describe, expect, test } from 'bun:test'

import {
  applyClientIPHeaders,
  clientIPConfig,
  resolveClientIP
} from '../../server/utils/clientIp'

// web 层是客户端 IP 的唯一权威出口：这些用例锁定与 Go 侧 clientip 一致的信任语义。
const scenarios: Array<{
  name: string
  peer: string
  headers?: Record<string, string>
  proxies?: string[]
  expected: string
}> = [
  {
    name: '公网对端忽略一切转发头',
    peer: '198.51.100.20',
    headers: {
      'cf-connecting-ip': '203.0.113.7',
      'true-client-ip': '203.0.113.8',
      'x-real-ip': '203.0.113.9',
      'x-forwarded-for': '203.0.113.10'
    },
    expected: '198.51.100.20'
  },
  {
    name: '公网 IPv6 对端同样不采信转发头',
    peer: '2001:db8:ffff::20',
    headers: { 'x-real-ip': '203.0.113.9' },
    expected: '2001:db8:ffff::20'
  },
  {
    name: 'loopback 对端优先取 X-Real-IP',
    peer: '127.0.0.1',
    headers: {
      'x-real-ip': '203.0.113.9',
      'x-forwarded-for': '198.51.100.7, 203.0.113.9'
    },
    expected: '203.0.113.9'
  },
  {
    name: '私网对端忽略伪造的 CDN 头，落回 X-Real-IP',
    peer: '172.18.0.5',
    headers: {
      'cf-connecting-ip': '203.0.113.7',
      'true-client-ip': '203.0.113.8',
      'x-real-ip': '203.0.113.9',
      'x-forwarded-for': '198.51.100.7'
    },
    expected: '203.0.113.9'
  },
  {
    name: '私网对端忽略伪造的 true-client-ip',
    peer: '172.18.0.5',
    headers: { 'true-client-ip': '203.0.113.8', 'x-real-ip': '203.0.113.9' },
    expected: '203.0.113.9'
  },
  {
    name: '对端本身是显式代理时才采信 cf-connecting-ip',
    peer: '10.9.9.5',
    proxies: ['10.9.9.0/24'],
    headers: { 'cf-connecting-ip': '203.0.113.7', 'x-real-ip': '203.0.113.9' },
    expected: '203.0.113.7'
  },
  {
    name: '对端本身是显式代理时才采信 true-client-ip',
    peer: '10.9.9.5',
    proxies: ['10.9.9.0/24'],
    headers: { 'true-client-ip': '203.0.113.8', 'x-real-ip': '203.0.113.9' },
    expected: '203.0.113.8'
  },
  {
    name: '显式代理对端下非法 CDN 头仍被跳过',
    peer: '10.9.9.5',
    proxies: ['10.9.9.0/24'],
    headers: { 'cf-connecting-ip': 'not-an-ip', 'x-real-ip': '203.0.113.9' },
    expected: '203.0.113.9'
  },
  {
    name: 'ULA 私网 IPv6 对端可信',
    peer: 'fd00::5',
    headers: { 'x-real-ip': '203.0.113.9' },
    expected: '203.0.113.9'
  },
  {
    name: 'IPv4-mapped IPv6 对端归一化后按 loopback 处理',
    peer: '::ffff:127.0.0.1',
    headers: { 'x-real-ip': '203.0.113.9' },
    expected: '203.0.113.9'
  },
  {
    name: 'IPv4-mapped IPv6 公网对端归一化后直接返回',
    peer: '::ffff:198.51.100.20',
    headers: { 'x-real-ip': '203.0.113.9' },
    expected: '198.51.100.20'
  },
  {
    name: 'IPv6 客户端地址按 RFC 5952 压缩输出',
    peer: '::1',
    headers: { 'x-real-ip': '2001:DB8:0:0:0:0:0:1' },
    expected: '2001:db8::1'
  },
  {
    name: 'XFF 从右往左剥掉显式 CIDR 代理',
    peer: '172.18.0.5',
    proxies: ['10.0.0.0/8'],
    headers: { 'x-forwarded-for': '203.0.113.9, 10.0.0.4, 10.0.0.5' },
    expected: '203.0.113.9'
  },
  {
    name: '私网客户端不会被当成代理剥掉',
    peer: '172.18.0.5',
    proxies: ['10.0.0.0/8'],
    headers: { 'x-forwarded-for': '192.168.1.50, 10.0.0.5' },
    expected: '192.168.1.50'
  },
  {
    name: '精确 IPv6 代理条目被剥掉',
    peer: '::1',
    proxies: ['2001:db8::a'],
    headers: { 'x-forwarded-for': '203.0.113.9, 2001:db8::a' },
    expected: '203.0.113.9'
  },
  {
    name: 'IPv6 CIDR 代理条目被剥掉',
    peer: '::1',
    proxies: ['2001:db8:1::/48'],
    headers: { 'x-forwarded-for': '2001:db8:2::5, 2001:db8:1::9' },
    expected: '2001:db8:2::5'
  },
  {
    name: 'loopback 链条目始终按代理剥掉',
    peer: '172.18.0.5',
    headers: { 'x-forwarded-for': '203.0.113.9, 127.0.0.1' },
    expected: '203.0.113.9'
  },
  {
    name: '带端口的 XFF 条目剥端口后判定',
    peer: '127.0.0.1',
    headers: { 'x-forwarded-for': '203.0.113.9:443, [2001:db8::7]:8443' },
    expected: '2001:db8::7'
  },
  {
    name: '非法条目跳过，链条全不可用时回退对端',
    peer: '127.0.0.1',
    proxies: ['10.0.0.0/8'],
    headers: { 'x-forwarded-for': 'unknown, , 10.0.0.5' },
    expected: '127.0.0.1'
  },
  {
    name: '非法代理配置被静默忽略',
    peer: '172.18.0.5',
    proxies: ['not-an-ip', '10.0.0.0/99'],
    headers: { 'x-forwarded-for': '203.0.113.9, 10.0.0.5' },
    expected: '10.0.0.5'
  },
  {
    name: '无转发头时回退对端',
    peer: '127.0.0.1',
    expected: '127.0.0.1'
  },
  {
    name: '对端不可解析时返回空串（调用方 fail-closed）',
    peer: '',
    headers: { 'x-real-ip': '203.0.113.9' },
    expected: ''
  }
]

describe('client IP resolution', () => {
  for (const scenario of scenarios) {
    test(scenario.name, () => {
      const headers = new Headers(scenario.headers ?? {})
      expect(resolveClientIP(scenario.peer, headers, { proxies: scenario.proxies ?? [] }))
        .toBe(scenario.expected)
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
})

describe('client IP config', () => {
  test('TRUSTED_PROXIES 按逗号拆分并忽略空白项', () => {
    expect(clientIPConfig(' 10.0.0.0/8 , 203.0.113.10 , ')).toEqual({
      proxies: ['10.0.0.0/8', '203.0.113.10']
    })
    expect(clientIPConfig('')).toEqual({ proxies: [] })
  })
})
