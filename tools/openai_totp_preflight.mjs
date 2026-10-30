// Offline Node -> Python/curl_cffi configuration check. Never sends HTTP.
import path from 'node:path'
import { pathToFileURL } from 'node:url'
import { createRequire } from 'node:module'

let transport
try {
  if (Number(process.versions.node.split('.')[0]) < 22) throw Error('unsupported_node')
  const root = process.env.TOSUB2_ROOT
  const require = createRequire(path.join(root, 'package.json'))
  if (require('jsdom/package.json').version !== '26.1.0') throw Error('dependency_version')
  require('jsdom')
  const { TlsFingerprintTransport } = await import(pathToFileURL(path.join(root, 'src/tls-transport.mjs')))
  transport = new TlsFingerprintTransport({ cloudflareSolver: false, sameProxyRiskRetries: 0 })
  await transport.configure(null, { force: true })
  if (transport.profile !== 'chrome146') throw Error('unsupported_profile')
  process.stdout.write('{"ok":true}')
} catch {
  process.stderr.write('OpenAI login runtime unavailable\n')
  process.exitCode = 1
} finally {
  if (transport) await transport.close()
}
