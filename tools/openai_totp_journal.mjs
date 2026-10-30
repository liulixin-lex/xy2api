// Adapted from ranxi2001/sub2api 12a49ef4, tools/openai_totp_journal.mjs (LGPL-3.0).
// Secrets arrive only on stdin. Atomic AES-GCM records and directory fsync.
import fs from 'node:fs'
import path from 'node:path'
import crypto from 'node:crypto'
import { fileURLToPath } from 'node:url'

const LIMIT = 1024 * 1024
function taskId(id) {
  if (!Number.isSafeInteger(id) || id <= 0) throw Error('invalid_task')
  return id
}
function syncDirectory(root) {
  const fd = fs.openSync(root, 'r')
  try { fs.fsyncSync(fd) } finally { fs.closeSync(fd) }
}
function safeInfo(info, directory = false) {
  if ((directory ? !info.isDirectory() : !info.isFile()) || info.isSymbolicLink()
      || (info.mode & 0o077) || (process.getuid && info.uid !== process.getuid())
      || (!directory && info.nlink !== 1)) throw Error('unsafe_journal_permissions')
}
function readPrivate(file) {
  const fd = fs.openSync(file, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW)
  try {
    const info = fs.fstatSync(fd)
    safeInfo(info)
    if (info.size > LIMIT) throw Error('oversized_record')
    return fs.readFileSync(fd)
  } finally { fs.closeSync(fd) }
}
function atomicWrite(root, target, data) {
  const tmp = path.join(root, `.write-${crypto.randomUUID()}`)
  try {
    const fd = fs.openSync(tmp, 'wx', 0o600)
    try { fs.writeFileSync(fd, data); fs.fsyncSync(fd) } finally { fs.closeSync(fd) }
    fs.renameSync(tmp, target)
    syncDirectory(root)
  } finally {
    if (fs.existsSync(tmp)) fs.unlinkSync(tmp)
  }
}
export function openJournal(root) {
  if (typeof root !== 'string' || !path.isAbsolute(root)) throw Error('invalid_journal_directory')
  fs.mkdirSync(root, { recursive: true, mode: 0o700 })
  safeInfo(fs.lstatSync(root), true)
  syncDirectory(path.dirname(root))
  const keyPath = path.join(root, 'key.bin')
  if (!fs.existsSync(keyPath)) {
    if (fs.readdirSync(root).some(name => name.endsWith('.sealed'))) throw Error('journal_key_missing')
    try {
      const fd = fs.openSync(keyPath, 'wx', 0o600)
      try { fs.writeFileSync(fd, crypto.randomBytes(32)); fs.fsyncSync(fd) } finally { fs.closeSync(fd) }
      syncDirectory(root)
    } catch (error) { if (error.code !== 'EEXIST') throw error }
  }
  const key = readPrivate(keyPath)
  if (key.length !== 32) throw Error('invalid_journal_key')
  function read(id) {
    const raw = readPrivate(path.join(root, `${taskId(id)}.sealed`))
    if (raw.length < 28) throw Error('invalid_record')
    const decipher = crypto.createDecipheriv('aes-256-gcm', key, raw.subarray(0, 12))
    decipher.setAAD(Buffer.from(String(id)))
    decipher.setAuthTag(raw.subarray(12, 28))
    const record = JSON.parse(Buffer.concat([decipher.update(raw.subarray(28)), decipher.final()]))
    if (record.task_id !== id) throw Error('invalid_record_identity')
    return record
  }
  function write(record) {
    const id = taskId(record.task_id)
    const target = path.join(root, `${id}.sealed`)
    let old
    try { old = read(id) } catch (error) { if (error.code !== 'ENOENT') throw error }
    if (old?.candidate && old.candidate !== record.candidate) throw Error('candidate_overwrite_refused')
    if (old && ['account_id', 'email', 'password', 'previous', 'session_id'].some(
      key => old[key] && old[key] !== record[key])) throw Error('record_overwrite_refused')
    const serialized = Buffer.from(JSON.stringify({ ...record,
      ...(old?.database_acknowledged ? { database_acknowledged: true } : {}) }))
    if (serialized.length > LIMIT - 28) throw Error('oversized_record')
    const nonce = crypto.randomBytes(12)
    const cipher = crypto.createCipheriv('aes-256-gcm', key, nonce)
    cipher.setAAD(Buffer.from(String(id)))
    const data = Buffer.concat([cipher.update(serialized), cipher.final()])
    atomicWrite(root, target, Buffer.concat([nonce, cipher.getAuthTag(), data]))
  }
  function pending() {
    return fs.readdirSync(root).filter(name => /^[1-9]\d*\.sealed$/.test(name))
      .map(name => Number(name.split('.')[0])).filter(id => {
        const record = read(id)
        return record.candidate && !record.database_acknowledged
      })
  }
  function check() {
    // Key existence alone does not prove the volume remains writable/fsyncable.
    const probe = path.join(root, `.probe-${crypto.randomUUID()}`)
    try { atomicWrite(root, probe, crypto.randomBytes(32)) }
    finally { if (fs.existsSync(probe)) fs.unlinkSync(probe); syncDirectory(root) }
    pending() // Fail closed on corrupted recovery material.
  }
  return { read, write, pending, check,
    acknowledge: id => write({ ...read(id), database_acknowledged: true }) }
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const chunks = [], chunk = Buffer.alloc(64 * 1024)
    let size = 0, count
    while ((count = fs.readSync(0, chunk)) > 0) {
      size += count
      if (size > LIMIT) throw Error('oversized_input')
      chunks.push(Buffer.from(chunk.subarray(0, count)))
    }
    const input = JSON.parse(Buffer.concat(chunks))
    const journal = openJournal(process.env.OPENAI_TOTP_JOURNAL_DIR)
    let result = { ok: true }
    if (input.action === 'check') journal.check()
    else if (input.action === 'write') journal.write(input.record)
    else if (input.action === 'pending') result = journal.pending()
    else if (input.action === 'read') result = journal.read(input.task_id)
    else if (input.action === 'acknowledge') journal.acknowledge(input.task_id)
    else throw Error('invalid_action')
    process.stdout.write(JSON.stringify(result))
  } catch {
    process.stderr.write('TOTP recovery journal unavailable\n')
    process.exitCode = 1
  }
}
