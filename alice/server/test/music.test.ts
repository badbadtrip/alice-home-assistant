import assert from 'node:assert/strict'
import test from 'node:test'

import { musicRules, MusicSettingsSchema, resolveMusic } from '../src/music'

const settings = MusicSettingsSchema.parse({ default_media: 'Любимое', players: { kitchen: 'media_player.lite_2_airplay' }, source: 'music_assistant' })

function api (state = 'idle') {
  const calls: [string, string, Record<string, unknown>][] = []
  return {
    calls,
    callService: async (domain: string, service: string, data: Record<string, unknown>) => { calls.push([domain, service, data]) },
    getEntityState: () => ({ attributes: {}, name: 'Lite', state })
  }
}

function matches (rules: unknown[], text: string): boolean {
  return rules.some(rule => {
    const { match } = rule as { match: { contains_none?: string[]; regex: string } }
    return new RegExp(match.regex, 'iu').test(text) && !match.contains_none?.some(word => text.includes(word))
  })
}

test('music commands reach the speaker player', async () => {
  const home = api()
  assert.equal((await resolveMusic('Алиса, включи музыку', 'kitchen', settings, home))?.answer, 'Включаю.')
  await resolveMusic('включи группу кино', 'kitchen', settings, home)
  await resolveMusic('включи кино в спотифай', 'kitchen', settings, home)
  assert.deepEqual(home.calls[0]?.slice(0, 2), ['media_player', 'volume_set'])
  assert.deepEqual(home.calls.filter(call => call[1] === 'play_media').map(call => [call[2].media_id, call[2].media_type, call[2].entity_id]), [
    ['Любимое', 'playlist', 'media_player.lite_2_airplay'],
    ['кино', 'artist', 'media_player.lite_2_airplay'],
    ['кино', undefined, 'media_player.lite_2_airplay']
  ])
})

test('other phrases and sources are left alone', async () => {
  const home = api()
  assert.equal(await resolveMusic('включи свет', 'kitchen', settings, home), undefined)
  assert.equal(await resolveMusic('включи музыку в яндексе', 'kitchen', settings, home), undefined)
  assert.equal(await resolveMusic('включи музыку', 'other', settings, home), undefined)
  assert.equal(await resolveMusic('включи музыку', 'kitchen', { ...settings, source: 'yandex' }, home), undefined)
  assert.equal(home.calls.length, 0)
})

test('playback controls act only while Music Assistant plays here', async () => {
  const playing = api('playing')
  await resolveMusic('пауза', 'kitchen', settings, playing)
  assert.deepEqual(playing.calls[0]?.slice(0, 2), ['media_player', 'media_pause'])
  assert.equal(await resolveMusic('пауза', 'kitchen', settings, api('idle')), undefined)
  await resolveMusic('Алиса, следующую', 'kitchen', settings, playing)
  assert.equal(playing.calls[1]?.[1], 'media_next_track')
  assert.ok(matches(musicRules(settings, 'kitchen', playing), 'Алиса, следующую песню'))
})

test('a missing default asks for the setting and a failure is spoken', async () => {
  const home = api()
  assert.match((await resolveMusic('включи музыку', 'kitchen', { ...settings, default_media: '' }, home))?.answer ?? '', /настройках/u)
  const broken = { ...api(), callService: async () => { throw new Error('no') } }
  assert.match((await resolveMusic('включи музыку', 'kitchen', settings, broken))?.answer ?? '', /Не нашла/u)
})

test('published rules follow the source and the player state', () => {
  assert.deepEqual(musicRules({ ...settings, source: 'yandex' }, 'kitchen', api()), [])
  assert.deepEqual(musicRules(settings, 'other', api()), [])
  const idle = musicRules(settings, 'kitchen', api())
  assert.equal(idle.length, 1)
  for (const text of ['Алиса, включи музыку', 'поставь песню кино', 'включи кино в спотифай']) assert.ok(matches(idle, text), text)
  for (const text of ['включи свет', 'включи музыку в яндексе', 'поставь таймер на песню', 'пауза']) assert.ok(!matches(idle, text), text)
  assert.ok(matches(musicRules(settings, 'kitchen', api('playing')), 'пауза'))
})

test('spotify playlists keep a URI from a shared link and reject others', () => {
  const parsed = MusicSettingsSchema.parse({ spotify_playlists: [{ name: 'Утро', uri: 'https://open.spotify.com/intl-ru/playlist/abc123?si=x' }, { name: 'Альбом', uri: 'spotify:album:def' }] })
  assert.deepEqual(parsed.spotify_playlists.map(item => item.uri), ['spotify:playlist:abc123', 'spotify:album:def'])
  assert.equal(MusicSettingsSchema.safeParse({ spotify_playlists: [{ name: 'Нет', uri: 'https://example.com/playlist/abc' }] }).success, false)
})
