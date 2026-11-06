import assert from 'node:assert/strict'
import test from 'node:test'

import { fetchPlaylists, finishLogin, loginRequest, mergePlaylists, refreshAccess, REDIRECT_URI, searchSpotify } from '../src/spotify'

const json = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), { status })

test('login url uses PKCE and the loopback redirect', () => {
  const { state, url } = loginRequest('a'.repeat(32))
  const query = new URL(url).searchParams
  assert.equal(query.get('redirect_uri'), REDIRECT_URI)
  assert.equal(query.get('code_challenge_method'), 'S256')
  assert.equal(query.get('state'), state.oauth_state)
  assert.ok(!url.includes(state.verifier))
})

test('finishing login rejects a foreign state and stores the refresh token', async () => {
  const { state } = loginRequest('a'.repeat(32))
  await assert.rejects(finishLogin(state, `${REDIRECT_URI}?code=x&state=other`))
  const done = await finishLogin(state, `${REDIRECT_URI}?code=x&state=${state.oauth_state}`,
    () => Promise.resolve(json({ access_token: 'a', refresh_token: 'r1' })))
  assert.equal(done.refresh_token, 'r1')
  assert.equal(done.verifier, '')
})

test('refresh keeps the rotated token', async () => {
  const state = { client_id: 'c', oauth_state: '', refresh_token: 'r1', verifier: '' }
  const next = await refreshAccess(state, () => Promise.resolve(json({ access_token: 'a', refresh_token: 'r2' })))
  assert.equal(next.state.refresh_token, 'r2')
})

test('playlists follow pages and skip empty entries', async () => {
  const pages = [
    { items: [{ name: 'Утро', uri: 'spotify:playlist:a' }, null], next: 'https://api.spotify.com/next' },
    { items: [{ name: 'Бег', uri: 'spotify:playlist:b' }], next: null }
  ]
  const found = await fetchPlaylists('a', () => Promise.resolve(json(pages.shift())))
  assert.deepEqual(found.map(item => item.name), ['Утро', 'Бег'])
})

test('merge keeps user names and order', () => {
  const merged = mergePlaylists([{ name: 'Моё', uri: 'u1' }], [{ name: 'Their', uri: 'u1' }, { name: 'Новый', uri: 'u2' }])
  assert.deepEqual(merged, [{ name: 'Моё', uri: 'u1' }, { name: 'Новый', uri: 'u2' }])
})

test('search prefers the exact artist, then a track', async () => {
  const body = {
    artists: { items: [{ name: 'Кино', uri: 'spotify:artist:a' }] },
    tracks: { items: [null, { name: 'Кино', uri: 'spotify:track:t' }] }
  }
  const request = (): Promise<Response> => Promise.resolve(json(body))
  assert.equal((await searchSpotify('x', { kind: 'any', query: 'кино' }, request))?.uri, 'spotify:artist:a')
  assert.equal((await searchSpotify('x', { kind: 'any', query: 'кино группа' }, request))?.uri, 'spotify:track:t')
  assert.equal((await searchSpotify('x', { kind: 'track', query: 'кино' }, request))?.uri, 'spotify:track:t')
})

test('search reports nothing found and failures apart', async () => {
  const empty = (): Promise<Response> => Promise.resolve(json({ playlists: { items: [] } }))
  assert.equal(await searchSpotify('x', { kind: 'playlist', query: 'нет' }, empty), null)
  await assert.rejects(searchSpotify('x', { kind: 'track', query: 'a' }, () => Promise.resolve(json({}, 403))))
})

test('liked songs use the account collection', async () => {
  const found = await searchSpotify('x', { kind: 'liked', query: '' }, () => Promise.resolve(json({ id: 'me' })))
  assert.equal(found?.uri, 'spotify:user:me:collection')
})

test('a found track brings the artist tracks that follow it', async () => {
  const track = { artists: [{ name: 'Кино' }], name: 'Звезда', uri: 'spotify:track:t' }
  const other = { artists: [{ name: 'Кино' }], name: 'Кукушка', uri: 'spotify:track:o' }
  const request = (address: unknown): Promise<Response> => Promise.resolve(json({
    tracks: { items: String(address).includes('artist') ? [track, other] : [track] }
  }))
  const found = await searchSpotify('x', { kind: 'track', query: 'звезда' }, request as typeof fetch)
  assert.deepEqual(found?.next, ['spotify:track:o'])
})

test('a failed following search keeps the found track', async () => {
  const track = { artists: [{ name: 'Кино' }], name: 'Звезда', uri: 'spotify:track:t' }
  const request = (address: unknown): Promise<Response> => Promise.resolve(
    String(address).includes('artist') ? json({}, 500) : json({ tracks: { items: [track] } }))
  const found = await searchSpotify('x', { kind: 'track', query: 'звезда' }, request as typeof fetch)
  assert.equal(found?.uri, 'spotify:track:t')
  assert.deepEqual(found?.next, [])
})
