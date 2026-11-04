import assert from 'node:assert/strict'
import test from 'node:test'
import { summarizeHistory } from '../src/history'
import { callReadTool, historyWindow } from '../src/read-tools'
import { HomeAssistantApi } from '../src/home-assistant'

const start = new Date('2026-11-01T00:00:00Z')
const end = new Date('2026-11-01T04:00:00Z')
test('history clips initial state, ignores duplicate on states, and marks gaps', () => {
  const s = summarizeHistory([
    { lastChanged: '2026-10-20T00:00:00Z', state: 'on' },
    { lastChanged: '2026-11-01T01:00:00Z', state: 'on' },
    { lastChanged: '2026-11-01T02:00:00Z', state: 'unavailable' },
    { lastChanged: '2026-11-01T03:00:00Z', state: 'off' }
  ], start, end, false)
  assert.equal(s.active_hours, 2)
  assert.equal(s.activations, 0)
  assert.equal(s.coverage_percent, 75)
  assert.equal(s.available, true)
})
test('numeric mean is weighted by duration and unavailable is not zero', () => {
  const s = summarizeHistory([
    { lastChanged: start.toISOString(), state: '10' },
    { lastChanged: '2026-11-01T01:00:00Z', state: '20' },
    { lastChanged: '2026-11-01T03:00:00Z', state: 'unavailable' }
  ], start, end, true)
  assert.equal(s.minimum, 10); assert.equal(s.maximum, 20)
  assert.equal(s.time_weighted_mean, 16.67); assert.equal(s.coverage_percent, 75)
  assert.equal(summarizeHistory([], start, end, false).available, false)
})
test('calendar windows use home timezone and normalize month boundary', () => {
  const now = new Date('2026-11-01T08:00:00Z')
  const w = historyWindow('yesterday', undefined, now)
  assert.equal(w.start.toISOString(), '2026-10-30T21:00:00.000Z')
  assert.equal(w.end.toISOString(), '2026-10-31T21:00:00.000Z')
  assert.throws(() => historyWindow('last_days', 99))
  assert.throws(() => historyWindow('last_days', undefined))
})
test('read tools reject non-exposed IDs before accessing history', async () => {
  let calls = 0
  const api = { getEntityHistory: async () => { calls++; return [] } } as unknown as HomeAssistantApi
  await assert.rejects(callReadTool('home_history', { entity_id: 'switch.private', period: 'today' }, api, new Map()))
  assert.equal(calls, 0)
})

test('last week is the previous full local calendar week across month and DST boundaries', () => {
  const minsk = historyWindow('last_week', undefined, new Date('2026-11-04T08:00:00Z'))
  assert.equal(minsk.start.toISOString(), '2026-10-25T21:00:00.000Z')
  assert.equal(minsk.end.toISOString(), '2026-11-01T21:00:00.000Z')
  const monday = historyWindow('last_week', undefined, new Date('2027-01-04T08:00:00Z'))
  assert.equal(monday.start.toISOString(), '2026-12-27T21:00:00.000Z')
  assert.equal(monday.end.toISOString(), '2027-01-03T21:00:00.000Z')
  const dst = historyWindow('last_week', undefined, new Date('2026-03-30T12:00:00Z'), 'Europe/Berlin')
  assert.equal(dst.start.toISOString(), '2026-03-22T23:00:00.000Z')
  assert.equal(dst.end.toISOString(), '2026-03-29T22:00:00.000Z')
})
