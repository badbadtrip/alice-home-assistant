import { createHash, randomBytes } from 'node:crypto'
import { mkdir, readFile, rename, writeFile } from 'node:fs/promises'
import path from 'node:path'
import z from 'zod'

// Spotify allows plain HTTP redirects only on loopback, so the user pastes the final address back.
export const REDIRECT_URI = 'http://127.0.0.1:8888/callback'
const ACCOUNTS = 'https://accounts.spotify.com'
const PLAYLISTS = 'https://api.spotify.com/v1/me/playlists?limit=50'
const SCOPES = 'playlist-read-private playlist-read-collaborative'
const MAX_PAGES = 10

const StateSchema = z.object({
  client_id: z.string().default(''),
  oauth_state: z.string().default(''),
  refresh_token: z.string().default(''),
  verifier: z.string().default('')
})
export type SpotifyState = z.infer<typeof StateSchema>
type Fetch = typeof fetch

const TokenSchema = z.object({ access_token: z.string(), refresh_token: z.string().optional() })
const ItemSchema = z.object({ name: z.string(), uri: z.string() })
const PageSchema = z.object({
  items: z.array(ItemSchema.nullable()),
  next: z.string().nullable().optional()
})

export const ClientIdSchema = z.string().trim().regex(/^[a-f0-9]{32}$/u)

export async function fetchPlaylists (
  access: string, request: Fetch = fetch
): Promise<{ name: string; uri: string }[]> {
  const found: { name: string; uri: string }[] = []
  let url: null | string | undefined = PLAYLISTS
  for (let page = 0; url && page < MAX_PAGES; page++) {
    const headers = { Authorization: `Bearer ${access}` }
    const response: Response = await request(url, { headers, signal: AbortSignal.timeout(4000) })
    if (!response.ok) throw new Error('Spotify не отдал плейлисты.')
    const data = PageSchema.parse(await response.json())
    for (const item of data.items) {
      if (item && item.name.trim() !== '') found.push({ name: item.name.trim().slice(0, 80), uri: item.uri })
    }
    url = data.next
  }
  return found
}

export async function finishLogin (
  state: SpotifyState, address: string, request: Fetch = fetch
): Promise<SpotifyState> {
  const query = new URL(address).searchParams
  const code = query.get('code')
  if (!code || !state.verifier || query.get('state') !== state.oauth_state) {
    throw new Error('Адрес не подходит. Начните вход заново.')
  }
  const result = await token({
    client_id: state.client_id,
    code,
    code_verifier: state.verifier,
    grant_type: 'authorization_code',
    redirect_uri: REDIRECT_URI
  }, request)
  return { client_id: state.client_id, oauth_state: '', refresh_token: result.refresh_token ?? '', verifier: '' }
}

export function loginRequest (clientId: string): { state: SpotifyState; url: string } {
  const verifier = randomBytes(48).toString('base64url')
  const oauthState = randomBytes(16).toString('hex')
  const query = new URLSearchParams({
    client_id: clientId,
    code_challenge: createHash('sha256').update(verifier).digest('base64url'),
    code_challenge_method: 'S256',
    redirect_uri: REDIRECT_URI,
    response_type: 'code',
    scope: SCOPES,
    state: oauthState
  })
  return {
    state: { client_id: clientId, oauth_state: oauthState, refresh_token: '', verifier },
    url: `${ACCOUNTS}/authorize?${query.toString()}`
  }
}

// Keeps the user's order and names; only playlists not listed yet are appended.
export function mergePlaylists <T extends { uri: string }> (current: T[], fresh: T[]): T[] {
  const known = new Set(current.map(item => item.uri))
  return [...current, ...fresh.filter(item => !known.has(item.uri))].slice(0, 50)
}

export async function readSpotify (file = ''): Promise<SpotifyState> {
  try {
    return StateSchema.parse(JSON.parse(await readFile(file, 'utf8')))
  } catch {
    return StateSchema.parse({})
  }
}

// The refresh token is single use: callers must store the returned state before anything else.
export async function refreshAccess (
  state: SpotifyState, request: Fetch = fetch
): Promise<{ access: string; state: SpotifyState }> {
  const body = { client_id: state.client_id, grant_type: 'refresh_token', refresh_token: state.refresh_token }
  const result = await token(body, request)
  const refreshToken = result.refresh_token ?? state.refresh_token
  return { access: result.access_token, state: { ...state, refresh_token: refreshToken } }
}

export async function writeSpotify (file: string | undefined, state: SpotifyState): Promise<void> {
  if (!file) throw new Error('Spotify state path is not configured')
  await mkdir(path.dirname(file), { mode: 0o700, recursive: true })
  const temporary = file + '.tmp'
  await writeFile(temporary, JSON.stringify(state), { mode: 0o600 })
  await rename(temporary, file)
}

async function token (body: Record<string, string>, request: Fetch): Promise<z.infer<typeof TokenSchema>> {
  const response = await request(`${ACCOUNTS}/api/token`, {
    body: new URLSearchParams(body),
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    method: 'POST',
    signal: AbortSignal.timeout(4000)
  })
  if (!response.ok) throw new Error('Spotify отклонил вход.')
  return TokenSchema.parse(await response.json())
}
