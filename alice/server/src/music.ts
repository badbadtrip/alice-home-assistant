import { mkdir, readFile, rename, writeFile } from 'node:fs/promises'
import path from 'node:path'
import z from 'zod'

import { PhrasesSchema } from './phrases'

// Music on each speaker is stock Yandex Music or Music Assistant; services live in Music Assistant.
export const MUSIC_SOURCES = ['yandex', 'music_assistant'] as const
const Source = z.enum(MUSIC_SOURCES)
const SpeakerId = z.string().regex(/^[a-z0-9][a-z0-9_-]{0,63}$/u)

// Before 0.58 a single "source"/"provider" pair chose the music for every speaker.
const legacySource = (value: unknown): unknown => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return value
  const old = value as Record<string, unknown>
  if (old.default_source !== undefined) return value
  return { ...old, default_source: old.source === 'alternative' && old.provider === 'music_assistant' ? 'music_assistant' : 'yandex' }
}

export const MusicSettingsSchema = z.preprocess(legacySource, z.object({
  default_source: Source.catch('yandex').default('yandex'),
  phrases: PhrasesSchema.default({ disabled: [], disabled_rules: [], items: [], replies: {} }),
  relay_intro: z.string().trim().max(100).default(''),
  // Speaker id -> its own choice; a speaker without one follows default_source.
  speaker_sources: z.record(SpeakerId, Source).default({})
}))

export type MusicSettings = z.infer<typeof MusicSettingsSchema>

export async function readMusicSettings (file: string | undefined): Promise<MusicSettings> {
  if (!file) return MusicSettingsSchema.parse({})
  try {
    return MusicSettingsSchema.parse(JSON.parse(await readFile(file, 'utf8')))
  } catch {
    return MusicSettingsSchema.parse({})
  }
}

export function speakerSource (settings: MusicSettings, speaker: string | undefined): typeof MUSIC_SOURCES[number] {
  return (speaker && settings.speaker_sources[speaker]) || settings.default_source
}

export async function writeMusicSettings (file: string | undefined, settings: MusicSettings): Promise<void> {
  if (!file) throw new Error('Music settings path is not configured')
  await mkdir(path.dirname(file), { mode: 0o700, recursive: true })
  const temporary = file + '.tmp'
  await writeFile(temporary, JSON.stringify(settings), { mode: 0o600 })
  await rename(temporary, file)
}
