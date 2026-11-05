import { mkdir, readFile, rename, writeFile } from 'node:fs/promises'
import path from 'node:path'
import z from 'zod'

import { normalizeVoicePhrase } from './control'
import { HomeAssistantApi } from './home-assistant'

export const MusicSettingsSchema = z.object({
  default_media: z.string().trim().max(200).default(''),
  players: z.record(z.string().regex(/^[a-z0-9][a-z0-9_-]{0,63}$/u), z.string().regex(/^media_player\.[a-z0-9_]+$/u)).default({}),
  source: z.enum(['yandex', 'music_assistant']).default('yandex')
}).strict()

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

const VERB = '(?:включи|поставь|запусти|играй)'
const KIND = '(?:песню|трек|альбом|плейлист|группу|исполнителя|артиста)'
const SERVICE = '(?:в|на|из) (?:спотифай|спотифае|спотифая|spotify)'
const MUSIC_PHRASE = new RegExp(`^${VERB} (?:мне |мою )?музыку(?: ${SERVICE})?$`, 'u')
const NAMED_PHRASE = new RegExp(`^${VERB} (${KIND}) (.+?)(?: ${SERVICE})?$`, 'u')
const SERVICE_PHRASE = new RegExp(`^${VERB} (.+) ${SERVICE}$`, 'u')
const OTHER_SERVICE = /(?:^| )(?:в|на|из) (?:яндекс|ютуб|youtube)/u

const CONTROLS: [RegExp, string][] = [
  [/^(?:пауза|стоп|хватит|поставь на паузу|останови музыку|выключи музыку)$/u, 'media_pause'],
  [/^(?:продолжи|продолжи музыку|возобнови|возобнови музыку)$/u, 'media_play'],
  [/^(?:дальше|следующий трек|следующая песня|следующая|переключи трек)$/u, 'media_next_track'],
  [/^(?:назад|предыдущий трек|предыдущая песня|предыдущая)$/u, 'media_previous_track']
]

const PREFIX = String.raw`^\s*(?:алиса[,\s]+)?(?:пожалуйста[,\s]+)?`
const TAIL = String.raw`[,.!?\s]*$`

export interface MusicResult { answer: string; calls: never[]; matched: true }

// Rules the speaker applies before its Yandex rules. Playback controls stay with
// Yandex unless Music Assistant is playing on this very speaker.
export function musicRules (settings: MusicSettings, speaker: string | undefined,
  api: Pick<HomeAssistantApi, 'getEntityState'>): unknown[] {
  const player = speaker ? settings.players[speaker] : undefined
  if (!player || settings.source !== 'music_assistant') return []
  const service = '(?:в|на|из) (?:спотифай|спотифае|спотифая|spotify)'
  const start = String.raw`${VERB}\s+(?:(?:мне|мою)\s+)?музыку(?:\s+${service.replaceAll(' ', String.raw`\s+`)})?${TAIL}`
  const named = String.raw`${VERB}\s+${KIND}\s+`
  const anywhere = String.raw`${VERB}\s+.+\s+${service.replaceAll(' ', String.raw`\s+`)}${TAIL}`
  const rules: unknown[] = [{
    match: { contains_none: ['яндекс', 'ютуб', 'youtube', 'будильник', 'таймер'], regex: `${PREFIX}(?:${start}|${named}|${anywhere})` },
    name: 'music_assistant',
    route: 'home'
  }]
  const state = api.getEntityState(player)?.state
  if (state === 'playing' || state === 'paused') {
    const words = 'пауза|стоп|хватит|поставь на паузу|останови музыку|выключи музыку|продолжи|продолжи музыку|возобнови|возобнови музыку|дальше|следующий трек|следующая песня|следующая|переключи трек|назад|предыдущий трек|предыдущая песня|предыдущая'
    rules.push({ match: { regex: `${PREFIX}(?:${words})${TAIL}` }, name: 'music_assistant_control', route: 'home' })
  }
  return rules
}

// Runs a music command, or returns undefined so the normal resolver takes the text.
export async function resolveMusic (text: string, speaker: string | undefined, settings: MusicSettings,
  api: Pick<HomeAssistantApi, 'callService' | 'getEntityState'>): Promise<MusicResult | undefined> {
  const player = speaker ? settings.players[speaker] : undefined
  if (!player || settings.source !== 'music_assistant') return undefined
  const phrase = normalizeVoicePhrase(text)
  if (OTHER_SERVICE.test(phrase)) return undefined
  const control = CONTROLS.find(([pattern]) => pattern.test(phrase))?.[1]
  if (control) {
    const state = api.getEntityState(player)?.state
    if (state !== 'playing' && state !== 'paused') return undefined
    try {
      await api.callService('media_player', control, { entity_id: player })
      return spoken('Хорошо.')
    } catch {
      return spoken('Не получилось управлять музыкой.')
    }
  }
  const request = mediaRequest(phrase, settings)
  if (!request) return undefined
  if (!request.media_id) return spoken('Выберите в настройках, что включать по умолчанию.')
  try {
    // AirPlay stays at full level, so the speaker's own volume is the only control.
    await api.callService('media_player', 'volume_set', { entity_id: player, volume_level: 1 })
    await api.callService('music_assistant', 'play_media', { ...request, enqueue: 'replace', entity_id: player })
    return spoken('Включаю.')
  } catch {
    return spoken('Не нашла такую музыку или плеер недоступен.')
  }
}

function mediaRequest (phrase: string, settings: MusicSettings): undefined | { media_id: string; media_type?: string } {
  if (MUSIC_PHRASE.test(phrase)) return { media_id: settings.default_media, media_type: 'playlist' }
  const named = NAMED_PHRASE.exec(phrase)
  if (named) {
    const types: Record<string, string> = { альбом: 'album', артиста: 'artist', группу: 'artist', исполнителя: 'artist', песню: 'track', плейлист: 'playlist', трек: 'track' }
    return { media_id: named[2] ?? '', media_type: types[named[1] ?? ''] ?? 'track' }
  }
  const free = SERVICE_PHRASE.exec(phrase)
  return free ? { media_id: free[1] ?? '' } : undefined
}

function spoken (answer: string): MusicResult {
  return { answer, calls: [], matched: true }
}
