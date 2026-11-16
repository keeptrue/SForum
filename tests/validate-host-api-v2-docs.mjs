import { readdirSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const root = process.cwd()
const read = path => readFileSync(resolve(root, path), 'utf8')
const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

// 剥掉 Go 注释（字符串字面量内部不动），再拿去做不变量匹配。
// 不剥的话，一段写在注释里的 `base.AutoMTLS = true` 就能顶替真实代码，把这条断言变成
// 空断言 —— 断言的意义是「代码里还在」，不是「文件里某处出现过这几个字」。
const stripGoComments = source => {
  const out = source.split('')
  let index = 0
  while (index < source.length) {
    const char = source[index]
    if (char === '"' || char === '`' || char === "'") {
      const quote = char
      index += 1
      while (index < source.length) {
        if (source[index] === '\\' && quote !== '`') {
          index += 2
          continue
        }
        if (source[index] === quote) {
          index += 1
          break
        }
        index += 1
      }
      continue
    }
    if (char === '/' && source[index + 1] === '/') {
      while (index < source.length && source[index] !== '\n') {
        out[index] = ' '
        index += 1
      }
      continue
    }
    if (char === '/' && source[index + 1] === '*') {
      out[index] = ' '
      out[index + 1] = ' '
      index += 2
      while (index < source.length && !(source[index] === '*' && source[index + 1] === '/')) {
        if (source[index] !== '\n') out[index] = ' '
        index += 1
      }
      if (index < source.length) {
        out[index] = ' '
        out[index + 1] = ' '
        index += 2
      }
      continue
    }
    index += 1
  }
  return out.join('')
}

const doc = read('docs/extensions/host-api-v2.md')
const guide = read('docs/extensions/authoring-guide.md')
const commonProto = read('contracts/proto/sforum/protocol/v2/common.proto')
const controlProto = read('contracts/proto/sforum/protocol/v2/control.proto')
const pluginProto = read('contracts/proto/sforum/plugin/v2/runtime.proto')
const hostProtoPaths = [
  'contracts/proto/sforum/host/v2/data_work.proto',
  'contracts/proto/sforum/host/v2/platform.proto',
  'contracts/proto/sforum/host/v2/query_command.proto',
  'contracts/proto/sforum/host/v2/resources.proto',
  'contracts/proto/sforum/host/v2/session.proto'
]
const hostProto = hostProtoPaths.map(read).join('\n')
const server = stripGoComments(read('apps/api/sdk/plugin/v2/transport.go'))
// The v2 wire invariants belong to the runtime **as a whole**, and that runtime keeps
// being split so no single file outgrows the legacy size cap (the handshake literal
// already moved out of protocol_v2_client.go, and AutoMTLS followed it into
// protocol_v2_server.go). Naming individual files here therefore goes stale on every
// split — and a stale list makes documented invariants silently unchecked. Read the
// whole protocol_v2_*.go set instead, so a new runtime file cannot hide one.
const PROTOCOL_V2_RUNTIME_DIR = 'apps/api/app/Support/Extensions'
const protocolV2RuntimeFiles = readdirSync(resolve(root, PROTOCOL_V2_RUNTIME_DIR))
  .filter(name => name.startsWith('protocol_v2_') && name.endsWith('.go') && !name.endsWith('_test.go'))
  .sort()
assert(
  protocolV2RuntimeFiles.length > 0,
  `no protocol_v2_*.go runtime files found under ${PROTOCOL_V2_RUNTIME_DIR}`
)
const protocolV2Runtime = stripGoComments(
  protocolV2RuntimeFiles.map(name => read(`${PROTOCOL_V2_RUNTIME_DIR}/${name}`)).join('\n')
)
const testScript = read('scripts/test.sh')

for (const [source, packageName] of [
  [commonProto + controlProto, 'sforum.protocol.v2'],
  [pluginProto, 'sforum.plugin.v2'],
  [hostProto, 'sforum.host.v2']
]) {
  assert(source.includes(`package ${packageName};`), `missing Protobuf package ${packageName}`)
  assert(doc.includes(`\`${packageName}\``), `Host API v2 docs omit package ${packageName}`)
}

// S9: the plugin session-state surface is served, so it must be documented and
// its authority property must stay documented too.
for (const field of [
  'PluginSessionService', 'PluginSessionGetRequest', 'PluginSessionSetRequest',
  'PluginSessionDeleteRequest', 'namespace_key', 'host_session_delegation',
  'HostSessionDelegation', 'Actor.session_id'
]) {
  assert(hostProto.includes(field) || commonProto.includes(field), `wire contract no longer defines ${field}`)
  assert(doc.includes(field), `Host API v2 docs omit wire field ${field}`)
}

for (const field of [
  'host_broker_id', 'runtime_token', 'host_api_version', 'host_build', 'RequestContext',
  'ExtensionIdentity', 'AuthorityGrant', 'Actor', 'deadline'
]) {
  assert(commonProto.includes(field) || controlProto.includes(field), `wire contract no longer defines ${field}`)
  assert(doc.includes(field), `Host API v2 docs omit wire field ${field}`)
}

const metadata = server.match(/RuntimeTokenMetadataKey\s*=\s*"([^"]+)"/)?.[1]
assert(metadata === 'x-sforum-runtime-token-bin', `unexpected runtime token metadata key ${metadata}`)
assert(doc.includes(`\`${metadata}\``), 'Host API v2 docs omit the runtime token metadata key')

const maxMessageParts = server.match(/DefaultMaxMessageBytes\s*=\s*(\d+)\s*<<\s*(\d+)/)
assert(maxMessageParts, 'cannot resolve protocol v2 message limit')
const maxMessage = Number(maxMessageParts[1]) * (2 ** Number(maxMessageParts[2]))
const concurrency = Number(server.match(/DefaultConcurrentCalls\s*=\s*(\d+)/)?.[1])
const timeout = Number(server.match(/DefaultRequestTimeout\s*=\s*(\d+)\s*\*\s*time\.Second/)?.[1])

// 上限随 2026-10-19 的上下文预算重定标抬到 16 MiB（必须 >= MaxTotalInputBytes=12 MiB），
// 这里钉「足够大且与 Host 输入预算同量级」，而不是钉一个会被下一次调参打脸的历史数字。
assert(maxMessage >= 12 * 1024 * 1024, `protocol v2 message limit ${maxMessage} is below the Host input budget (12 MiB)`)
assert(concurrency === 16, `unexpected protocol v2 concurrency limit ${concurrency}`)
assert(timeout === 5, `unexpected protocol v2 request timeout ${timeout}`)
for (const value of [String(maxMessage), String(concurrency), `${timeout} seconds`]) {
  assert(doc.includes(value), `Host API v2 docs omit current safety value ${value}`)
}

// Match constants by identity, not gofmt column alignment (alignment drifts
// when nearby consts are renamed/added).
for (const [label, pattern] of [
  ['base.AutoMTLS = true', /base\.AutoMTLS\s*=\s*true/],
  ['plugin.ProtocolGRPC', /plugin\.ProtocolGRPC/],
  ['hostAPIV2Version = "sforum.host/v2"', /hostAPIV2Version\s*=\s*"sforum\.host\/v2"/],
  ['hostAPIV2Contract = "sforum.host@2"', /hostAPIV2Contract\s*=\s*"sforum\.host@2"/],
  ['hostAPIV2Legacy = "sforum.host-api@2"', /hostAPIV2Legacy\s*=\s*"sforum\.host-api@2"/],
  ['HostBrokerId:', /HostBrokerId\s*:/],
  ['RuntimeToken:', /RuntimeToken\s*:/],
  ['HostBuild:', /HostBuild\s*:/]
]) {
  assert(pattern.test(protocolV2Runtime), `runtime no longer contains documented invariant: ${label}`)
}

for (const phrase of [
  'AutoMTLS',
  'no downgrade',
  'go-plugin v1.8.0',
  'PLUGIN_CLIENT_CERT',
  'SFORUM_PLUGIN=sforum-plugin-v1',
  'plugin.GRPCBroker/StartStream',
  'v2 mismatch fails closed and never starts or reconnects through v1',
  'thin Go launcher built against',
  '"hostApiVersion": "sforum.host@2"',
  '`sforum.host@2`',
  '`sforum.host-api@2`',
  '`sforum.host/v2`'
]) {
  assert(doc.includes(phrase), `Host API v2 docs omit required interoperability rule: ${phrase}`)
}

assert(guide.includes('[Host API v2 and non-Go runtimes](./host-api-v2.md)'), 'authoring guide must link the Host API v2 guide')
assert(testScript.includes('node tests/validate-host-api-v2-docs.mjs'), 'repository test gate must run Host API v2 documentation validation')

console.log('Host API v2 SDK and non-Go interoperability documentation validated.')
