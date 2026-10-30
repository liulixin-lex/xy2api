// Offline journal tests; adapted from Sub2API 12a49ef4 journal tests (LGPL-3.0).
import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { openJournal } from '../openai_totp_journal.mjs'
import { disableReplay } from '../openai_totp_runner.mjs'

function setup(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'xy2api-totp-journal-'))
  t.after(() => fs.rmSync(root, { recursive: true, force: true }))
  return root
}

test('candidate survives restart; files contain no plaintext secrets and permissions are private', t => {
  const root = setup(t)
  const record = { task_id: 7, email: 'owner@example.test', password: 'synthetic-password', previous: 'OLD-SECRET', candidate: 'NEW-SECRET' }
  openJournal(root).write(record)
  assert.deepEqual(openJournal(root).read(7), record)
  for (const name of fs.readdirSync(root)) {
    assert.equal(fs.statSync(path.join(root, name)).mode & 0o777, 0o600)
    const text = fs.readFileSync(path.join(root, name)).toString()
    for (const secret of [record.password, record.previous, record.candidate, record.email]) assert.ok(!text.includes(secret))
  }
})

test('candidate and account credentials cannot be replaced; ACK is monotonic', t => {
  const journal = openJournal(setup(t))
  const record = { task_id: 7, email: 'owner@example.test', password: 'synthetic', previous: 'OLD', candidate: 'NEW' }
  journal.write(record)
  assert.throws(() => journal.write({ ...record, candidate: 'OTHER' }), /overwrite/)
  assert.throws(() => journal.write({ ...record, password: 'OTHER' }), /overwrite/)
  assert.deepEqual(journal.pending(), [7])
  journal.acknowledge(7)
  journal.write(record)
  assert.deepEqual(journal.pending(), [])
  assert.equal(journal.read(7).database_acknowledged, true)
})

test('missing key is never regenerated over existing records', t => {
  const root = setup(t)
  openJournal(root).write({ task_id: 7, candidate: 'NEW' })
  fs.unlinkSync(path.join(root, 'key.bin'))
  assert.throws(() => openJournal(root), /key_missing/)
  assert.equal(fs.existsSync(path.join(root, 'key.bin')), false)
})

test('ciphertext corruption fails authentication; preflight fails closed', t => {
  const root = setup(t), journal = openJournal(root)
  journal.write({ task_id: 7, candidate: 'NEW' })
  const file = path.join(root, '7.sealed'), raw = fs.readFileSync(file)
  raw[raw.length - 1] ^= 1
  fs.writeFileSync(file, raw)
  assert.throws(() => journal.read(7))
  assert.throws(() => journal.check())
})

test('record identity is authenticated as AAD', t => {
  const root = setup(t), journal = openJournal(root)
  journal.write({ task_id: 7, candidate: 'NEW' })
  fs.renameSync(path.join(root, '7.sealed'), path.join(root, '8.sealed'))
  assert.throws(() => journal.read(8))
})

test('unsafe root/key/file permissions, symlinks and hardlinks fail closed', t => {
  const root = setup(t)
  fs.chmodSync(root, 0o755)
  assert.throws(() => openJournal(root), /permissions/)
  fs.chmodSync(root, 0o700)
  const journal = openJournal(root)
  fs.chmodSync(path.join(root, 'key.bin'), 0o644)
  assert.throws(() => openJournal(root), /permissions/)
  fs.chmodSync(path.join(root, 'key.bin'), 0o600)
  journal.write({ task_id: 7, candidate: 'NEW' })
  fs.linkSync(path.join(root, '7.sealed'), path.join(root, 'extra'))
  assert.throws(() => journal.read(7), /permissions/)
  fs.unlinkSync(path.join(root, 'extra'))
  fs.unlinkSync(path.join(root, '7.sealed'))
  fs.symlinkSync(path.join(root, 'key.bin'), path.join(root, '7.sealed'))
  assert.throws(() => journal.read(7))
})

test('untrusted task IDs cannot escape the journal directory', t => {
  const journal = openJournal(setup(t))
  for (const id of ['../key.bin', 0, -1, 0.5, Number.MAX_SAFE_INTEGER + 1, true]) {
    assert.throws(() => journal.read(id), /invalid_task/)
    assert.throws(() => journal.write({ task_id: id }), /invalid_task/)
  }
})

test('check actually fsyncs a new file and cleans probes', t => {
  const root = setup(t), journal = openJournal(root)
  journal.check()
  assert.deepEqual(fs.readdirSync(root), ['key.bin'])
})

test('fsync failure does not replace committed ciphertext', t => {
  const root = setup(t), journal = openJournal(root)
  const record = { task_id: 7, candidate: 'NEW' }
  journal.write(record)
  const original = fs.fsyncSync
  fs.fsyncSync = () => { throw Error('synthetic disk failure') }
  try { assert.throws(() => journal.acknowledge(7), /disk failure/) }
  finally { fs.fsyncSync = original }
  assert.deepEqual(journal.read(7), record)
  assert.deepEqual(fs.readdirSync(root).sort(), ['7.sealed', 'key.bin'])
})

test('CLI via current symlink supports check, write, read, pending and ACK', t => {
  const root = setup(t), release = path.join(root, 'release with spaces')
  fs.mkdirSync(release)
  const script = 'openai_totp_journal.mjs'
  fs.copyFileSync(fileURLToPath(new URL(`../${script}`, import.meta.url)), path.join(release, script))
  const current = path.join(root, 'current')
  fs.symlinkSync(release, current)
  const invoke = value => spawnSync(process.execPath, [path.join(current, script)], {
    input: JSON.stringify(value), encoding: 'utf8', env: { OPENAI_TOTP_JOURNAL_DIR: path.join(root, 'journal') }
  })
  assert.equal(invoke({ action: 'check' }).status, 0)
  assert.equal(invoke({ action: 'write', record: { task_id: 7, candidate: 'NEW' } }).status, 0)
  assert.deepEqual(JSON.parse(invoke({ action: 'pending' }).stdout), [7])
  assert.equal(JSON.parse(invoke({ action: 'read', task_id: 7 }).stdout).candidate, 'NEW')
  assert.equal(invoke({ action: 'acknowledge', task_id: 7 }).status, 0)
  assert.deepEqual(JSON.parse(invoke({ action: 'pending' }).stdout), [])
  const invalid = invoke({ action: 'read', task_id: '../secret-value' })
  assert.equal(invalid.status, 1)
  assert.equal(invalid.stderr, 'TOTP recovery journal unavailable\n')
})

test('adapter disables security-page replay and preserves TLS certificate checks', async () => {
  let received, sent
  class FakeTransport {
    request(method, url, options) { received = { method, url, options, solver: this.cloudflareSolver, retries: this.sameProxyRiskRetries } }
    send(message) { sent = message }
  }
  disableReplay(FakeTransport)
  const client = new FakeTransport()
  client.request('POST', 'https://auth.openai.com/api/accounts/mfa/verify', { retryRiskControl: true })
  assert.equal(received.options.retryRiskControl, false)
  assert.equal(received.solver, false)
  assert.equal(received.retries, 0)
  client.send({ operation: 'configure', verifyTls: false })
  assert.equal(sent.verifyTls, true)
})

test('runner through a current symlink actually runs the pinned protocol entrypoint', t => {
  const root = setup(t), src = path.join(root, 'runtime/src'), release = path.join(root, 'release with spaces')
  fs.mkdirSync(src, { recursive: true })
  fs.mkdirSync(release)
  fs.writeFileSync(path.join(src, 'tls-transport.mjs'), 'export class TlsFingerprintTransport { request() {} send() {} }')
  fs.writeFileSync(path.join(src, 'protocol-login.mjs'), 'process.stdout.write("synthetic-runner-ok")')
  fs.copyFileSync(fileURLToPath(new URL('../openai_totp_runner.mjs', import.meta.url)), path.join(release, 'runner.mjs'))
  fs.symlinkSync(release, path.join(root, 'current'))
  const result = spawnSync(process.execPath, [path.join(root, 'current/runner.mjs')], {
    encoding: 'utf8', env: { TOSUB2_ROOT: path.join(root, 'runtime') }
  })
  assert.equal(result.status, 0)
  assert.equal(result.stdout, 'synthetic-runner-ok')
})
