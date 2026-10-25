import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const root = process.cwd()
const read = path => readFileSync(resolve(root, path), 'utf8')
const assert = (condition, message) => {
  if (!condition) throw new Error(message)
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
const server = read('apps/api/sdk/plugin/v2/transport.go')
const hostRuntime = read('apps/api/app/Support/Extensions/protocol_v2_host.go')
const clientRuntime = read('apps/api/app/Support/Extensions/protocol_v2_client.go')
// The handshake request literal moved out of protocol_v2_client.go to keep that
// legacy-capped file from growing; the wire invariants still belong to the v2
// runtime as a whole.
const handshakeRuntime = read('apps/api/app/Support/Extensions/protocol_v2_identity_runtime.go')
const protocolV2Runtime = `${clientRuntime}\n${handshakeRuntime}`
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
