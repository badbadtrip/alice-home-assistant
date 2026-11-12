import assert from 'node:assert/strict'
import test from 'node:test'

import { MusicSettingsSchema, speakerSource } from '../src/music'

test('a speaker follows its own choice, then the default', () => {
  const settings = MusicSettingsSchema.parse({ default_source: 'yandex', speaker_sources: { da0f9d59a1e1ad66: 'music_assistant' } })
  assert.equal(speakerSource(settings, 'da0f9d59a1e1ad66'), 'music_assistant')
  assert.equal(speakerSource(settings, 'f9459e66ca6904f7'), 'yandex')
  assert.equal(speakerSource(settings, undefined), 'yandex')
  assert.equal(MusicSettingsSchema.safeParse({ speaker_sources: { a: 'spotify' } }).success, false)
})

test('settings from before 0.58 keep the owner phrases and map the old choice', () => {
  const old = {
    phrases: { items: [{ phrase: 'шпотифай', target: 'spotify' }, { phrase: 'расскажи сказку', target: 'yandex' }], replies: { llm_down: 'Нет связи', spotify_down: 'x' } },
    provider: 'music_assistant',
    source: 'alternative',
    spotify_playlists: [{ name: 'Утро', uri: 'spotify:playlist:a' }]
  }
  const parsed = MusicSettingsSchema.parse(old)
  assert.equal(parsed.default_source, 'music_assistant')
  assert.deepEqual(parsed.phrases.items, [{ phrase: 'расскажи сказку', target: 'yandex' }])
  assert.deepEqual(parsed.phrases.replies, { llm_down: 'Нет связи' })
  assert.ok(!('spotify_playlists' in parsed))
  assert.equal(MusicSettingsSchema.parse({ provider: 'youtube', source: 'alternative' }).default_source, 'yandex')
  assert.equal(MusicSettingsSchema.parse({ source: 'spotify' }).default_source, 'yandex')
})
