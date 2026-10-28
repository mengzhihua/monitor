import assert from 'node:assert/strict'
import { test } from 'node:test'

async function fixture(id) {
  const savedStorage = globalThis.sessionStorage, savedWindow = globalThis.window
  const values = new Map()
  const storage = { getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key) }
  const target = new EventTarget()
  let listenerCount = 0
  globalThis.sessionStorage = storage
  globalThis.window = {
    addEventListener: (...args) => { listenerCount++; target.addEventListener(...args) },
    removeEventListener: (...args) => { listenerCount--; target.removeEventListener(...args) },
  }
  const { auth } = await import(`../src/api/auth.ts?${id}`)
  return { auth, storage, values, listenerCount: () => listenerCount,
    changed(key = 'monitor.token') { const event = new Event('storage'); Object.assign(event, { key, storageArea: storage }); target.dispatchEvent(event) },
    restore() { globalThis.sessionStorage = savedStorage; globalThis.window = savedWindow } }
}

test('credential setters notify once with coherent caches and unsubscribe cleanly', async () => {
  const f = await fixture('setters')
  try {
    const seen = []
    const unsubscribe = f.auth.onChange(() => seen.push(f.auth.token))
    f.auth.token = 'first'
    f.auth.token = 'first'
    f.auth.token = 'second'
    assert.deepEqual(seen, ['first', 'second'])
    assert.equal(f.listenerCount(), 1)
    unsubscribe()
    f.auth.token = ''
    assert.deepEqual(seen, ['first', 'second'])
    assert.equal(f.listenerCount(), 0)
  } finally { f.restore() }
})

test('storage changes and getter reconciliation publish after caching, without reentrant notifications', async () => {
  const f = await fixture('storage')
  try {
    let calls = 0
    const unsubscribe = f.auth.onChange(() => { calls++; void f.auth.token })
    f.values.set('monitor.token', 'external')
    f.changed()
    assert.equal(calls, 1)
    f.changed()
    assert.equal(calls, 1)
    f.values.set('monitor.token', 'same-window')
    assert.equal(f.auth.token, 'same-window')
    assert.equal(calls, 2)
    f.values.clear()
    f.changed(null)
    assert.equal(f.auth.token, '')
    assert.equal(calls, 3)
    unsubscribe()
  } finally { f.restore() }
})

test('memory-only credentials notify and remain readable when storage rejects writes', async () => {
  const f = await fixture('fallback')
  try {
    f.storage.setItem = () => { throw new Error('storage unavailable') }
    const seen = []
    const unsubscribe = f.auth.onChange(() => seen.push(f.auth.token))
    f.auth.token = 'memory-only'
    f.auth.token = 'memory-only'
    assert.equal(f.auth.token, 'memory-only')
    f.auth.token = ''
    assert.deepEqual(seen, ['memory-only', ''])
    unsubscribe()
  } finally { f.restore() }
})
