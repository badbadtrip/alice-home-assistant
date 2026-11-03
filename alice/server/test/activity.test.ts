import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { ActivityJournal } from '../src/activity'

const event = (id: number) => ({ at: new Date().toISOString(), calls: [], duration_ms: 12,
  id: id.toString(16).padStart(32, '0'), route: 'home', source: 'voice_phrase',
  speaker_id: 'midi', status: 'accepted', text: 'Тестовая фраза' })

test('journal serializes concurrent writes, bounds storage, deduplicates and survives reload', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'alice-journal-'))
  const path = join(directory, 'events.json')
  try {
    const journal = new ActivityJournal(path, 3)
    await Promise.all([1, 2, 3, 4].map(id => journal.append(event(id))))
    await journal.append(event(4))
    const items = await new ActivityJournal(path, 3).list()
    assert.deepEqual(items.map(item => item.id), [4, 3, 2].map(id => event(id).id))
    items[0]!.text = 'mutated'
    assert.equal((await journal.list())[0]!.text, 'Тестовая фраза')
    await journal.append({ ...event(5), at: new Date(Date.now() - 8 * 86400000).toISOString() })
    assert.equal((await journal.list()).some(item => item.id === event(5).id), false)
  } finally { await rm(directory, { recursive: true }) }
})

test('journal rejects unexpected sensitive fields and preserves corrupt storage for recovery', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'alice-journal-'))
  const path = join(directory, 'events.json')
  try {
    await assert.rejects(new ActivityJournal().append({ ...event(1), token: 'secret' }))
    await writeFile(path, '{broken')
    const journal = new ActivityJournal(path)
    await assert.rejects(journal.list())
    await assert.rejects(journal.append(event(1)))
    assert.equal(await readFile(path, 'utf8'), '{broken')
  } finally { await rm(directory, { recursive: true }) }
})
