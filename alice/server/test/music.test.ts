import assert from 'node:assert/strict'
import test from 'node:test'

import { MusicSettingsSchema } from '../src/music'

test('spotify playlists keep a URI from a shared link and reject others', () => {
  const parsed = MusicSettingsSchema.parse({ spotify_playlists: [{ name: 'Утро', uri: 'https://open.spotify.com/intl-ru/playlist/abc123?si=x' }, { name: 'Альбом', uri: 'spotify:album:def' }] })
  assert.deepEqual(parsed.spotify_playlists.map(item => item.uri), ['spotify:playlist:abc123', 'spotify:album:def'])
  assert.equal(MusicSettingsSchema.safeParse({ spotify_playlists: [{ name: 'Нет', uri: 'https://example.com/playlist/abc' }] }).success, false)
})

test('settings from the Music Assistant era keep playlists and fall back to stock', () => {
  const parsed = MusicSettingsSchema.parse({ default_media: 'x', players: {}, source: 'music_assistant', spotify_playlists: [{ name: 'Утро', uri: 'spotify:playlist:a' }] })
  assert.equal(parsed.source, 'yandex')
  assert.equal(parsed.spotify_playlists.length, 1)
  assert.equal(MusicSettingsSchema.parse({ source: 'spotify' }).source, 'spotify')
})
