// Adapter for poxiao33/toSub2 8548397e89bf80e508eda64a87e0d556d43abc84 (MIT).
// Enforce one send per mutation, including upstream Cloudflare/risk-control paths.
import path from 'node:path'
import fs from 'node:fs'
import { fileURLToPath, pathToFileURL } from 'node:url'

export function disableReplay(Transport) {
  const request = Transport.prototype.request
  const send = Transport.prototype.send
  Transport.prototype.send = function (message) {
    // The pinned transport weakens certificate verification for loopback HTTP
    // proxies. A proxy must never disable validation of OpenAI certificates.
    return send.call(this, message.operation === 'configure' ? { ...message, verifyTls: true } : message)
  }
  Transport.prototype.request = function (method, url, options = {}) {
    this.cloudflareSolver = false
    this.sameProxyRiskRetries = 0
    return request.call(this, method, url, { ...options, retryRiskControl: false })
  }
}

export async function run(root) {
  const { TlsFingerprintTransport } = await import(pathToFileURL(path.join(root, 'src/tls-transport.mjs')))
  disableReplay(TlsFingerprintTransport)
  // protocol-login runs on import. argv retains its documented CLI arguments.
  await import(pathToFileURL(path.join(root, 'src/protocol-login.mjs')))
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  run(process.env.TOSUB2_ROOT).catch(() => {
    process.stderr.write('OpenAI login runner unavailable\n')
    process.exitCode = 1
  })
}
