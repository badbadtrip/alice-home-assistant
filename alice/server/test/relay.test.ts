import assert from 'node:assert/strict'
import test from 'node:test'

import { relayRooms, relaySpeaker, relayText, roomStem } from '../src/relay'

const speakers = [
  { area: 'Кухня', media_entity: 'media_player.lite', name: 'Лайт' },
  { area: 'Комната', media_entity: 'media_player.midi', name: 'Миди' },
  { area: 'Балкон', media_entity: null, name: 'Без плеера' }
]

test('a spoken case finds the room', () => {
  assert.equal(roomStem('кухню'), roomStem('Кухня'))
  assert.equal(relaySpeaker(speakers, 'кухню')?.name, 'Лайт')
  assert.equal(relaySpeaker(speakers, 'комнату')?.name, 'Миди')
  assert.equal(relaySpeaker(speakers, 'балкон'), undefined)
})

test('only rooms with a player are published', () => {
  assert.deepEqual(relayRooms(speakers), ['кухн', 'комнат'])
})

test('the intro goes before the message', () => {
  assert.equal(relayText('Царь изволит глаголить!', 'скоро там кушать'), 'Царь изволит глаголить. Скоро там кушать')
  assert.equal(relayText('  ', 'ужин готов'), 'Ужин готов')
})
