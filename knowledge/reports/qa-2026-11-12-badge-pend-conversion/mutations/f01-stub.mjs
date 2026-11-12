import { readFileSync } from 'node:fs'
import { createServer } from 'node:http'

// 老运：F-01 检查器自证的桩服务器（只在 127.0.0.1 本地，供 verify-badge-m3-manifest.sh 的 curl 使用）
const payloadPath = process.env.F01_PAYLOAD
const port = Number(process.env.F01_PORT ?? 18099)

createServer((_req, res) => {
  res.setHeader('content-type', 'application/json')
  res.end(readFileSync(payloadPath, 'utf8'))
}).listen(port, '127.0.0.1', () => console.log(`listening ${port} <- ${payloadPath}`))
