import { mkdir, readFile, rename, writeFile } from 'node:fs/promises'
import path from 'node:path'
import z from 'zod'

// Accepts a spotify: URI or an open.spotify.com link and keeps the URI.
const SpotifyUri = z.string().trim().max(300).transform((value, context) => {
  const match = /^spotify:(playlist|album):([A-Za-z0-9]+)$/u.exec(value) ??
    /^https:\/\/open\.spotify\.com\/(?:intl-[a-z]+\/)?(playlist|album)\/([A-Za-z0-9]+)(?:[/?#].*)?$/u.exec(value)
  if (!match) { context.addIssue({ code: 'custom', message: 'Invalid Spotify link' }); return z.NEVER }
  return `spotify:${match[1] ?? ''}:${match[2] ?? ''}`
})

const SpotifyPlaylist = z.object({ name: z.string().trim().min(1).max(80), uri: SpotifyUri }).strict()

// "spotify" is the older name of the alternative sources; anything else is stock music.
export const MusicSettingsSchema = z.object({
  provider: z.preprocess(value => value === 'youtube' ? value : 'spotify', z.enum(['spotify', 'youtube'])),
  relay_intro: z.string().trim().max(100).default(''),
  source: z.preprocess(
    value => value === 'spotify' || value === 'alternative' ? 'alternative' : 'yandex',
    z.enum(['alternative', 'yandex'])
  ),
  spotify_playlists: z.array(SpotifyPlaylist).max(50).default([])
})

export type MusicSettings = z.infer<typeof MusicSettingsSchema>

export async function readMusicSettings (file: string | undefined): Promise<MusicSettings> {
  if (!file) return MusicSettingsSchema.parse({})
  try {
    return MusicSettingsSchema.parse(JSON.parse(await readFile(file, 'utf8')))
  } catch {
    return MusicSettingsSchema.parse({})
  }
}

export async function writeMusicSettings (file: string | undefined, settings: MusicSettings): Promise<void> {
  if (!file) throw new Error('Music settings path is not configured')
  await mkdir(path.dirname(file), { mode: 0o700, recursive: true })
  const temporary = file + '.tmp'
  await writeFile(temporary, JSON.stringify(settings), { mode: 0o600 })
  await rename(temporary, file)
}
