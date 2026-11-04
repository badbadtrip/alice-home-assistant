import { DeviceEntry } from './devices'

export interface ResolvedCommand {
  answer?: string;
  calls: { arguments: Record<string, number | string>; name: string }[];
  matched: boolean;
}

export function normalizeCommand (text: string): string {
  return normalize(text).replace(/^алиса[, ]+/u, '').replace(/[.!]$/u, '')
}

export function normalizeVoicePhrase (text: string): string {
  return text.toLocaleLowerCase('ru').replaceAll('ё', 'е').replaceAll(/\p{P}+/gu, ' ')
    .replaceAll(/\s+/gu, ' ').trim().replace(/^алиса(?: |$)/u, '').trim()
}

// Exact grammar only: negations, conditions, compound commands and unknown
// modifiers go to the existing planner, never to a guessed device.
export function resolveLightCommand (text: string, speakerArea: string | undefined,
  devices: Map<string, DeviceEntry>, isVoiceOnly = false): ResolvedCommand {
  const voicePhrase = normalizeVoicePhrase(text)
  const configured: DeviceEntry[] = []
  for (const device of devices.values()) {
    if (device.voicePhrases?.some(p => normalizeVoicePhrase(p) === voicePhrase)) configured.push(device)
  }
  if (configured.length > 1) throw new Error('Ambiguous configured voice command')
  const custom = configured[0]
  if (custom) {
    const function_ = custom.functions[0]
    if (!function_) throw new Error('Configured command has no function')
    return { calls: [{ arguments: {}, name: function_.name }], matched: true }
  }
  // The speaker strips punctuation before matching, so a question reaches this
  // point as a known phrase. It must not switch anything on that basis.
  const isQuestion = text.includes('?')
  const command = isVoiceOnly ? voicePhrase : normalizeCommand(text)
  const lightCommand = command.replace(/^(включи|выключи|отключи|погаси|зажги) (здесь|тут) (свет|освещение)$/u, '$1 $3 $2')
  const match = LIGHT_GRAMMAR.exec(lightCommand)
  const lights = match ? resolveLights(match, speakerArea, devices) : undefined
  const intent = lights ? undefined : localIndex(devices).get(voicePhrase) ?? undefined
  const needsCalls = (lights?.calls.length ?? 0) > 0 || intent?.state !== undefined
  if (isQuestion && needsCalls) {
    return isVoiceOnly ? { answer: 'Скажите команду ещё раз, без вопроса.', calls: [], matched: true } : { calls: [], matched: false }
  }
  if (lights) return lights
  if (intent?.state !== undefined) {
    return { calls: [{ arguments: { state: intent.state }, name: `set_${intent.device.slug}_state` }], matched: true }
  }
  if (intent?.device.speak) return { answer: intent.device.speak(), calls: [], matched: true }
  // A dictionary phrase has no planner fallback on the speaker, so answer here.
  if (isVoiceOnly && match) return { answer: 'Не нашла подходящий свет. Назовите комнату или объедините светильники в группу.', calls: [], matched: true }
  return { calls: [], matched: false }
}

const LIGHT_GRAMMAR = /^(включи|выключи|отключи|погаси|зажги) (свет|освещение)(?: (?:в|на) ([а-яa-z0-9 -]+)| (здесь|тут)| ([а-яa-z0-9-]+))?$/u

function resolveLights (match: RegExpExecArray, speakerArea: string | undefined,
  devices: Map<string, DeviceEntry>): ResolvedCommand | undefined {
  const area = match[3] ?? match[5] ?? speakerArea
  if (!area) return { answer: 'В какой комнате?', calls: [], matched: true }
  const normalizedArea = normalize(area)
  const areas = new Map<string, string>()
  for (const device of devices.values()) {
    if (device.areaId && device.areaName &&
      (device.areaId === normalizedArea || areaNames(device.areaName).includes(normalizedArea))) {
      areas.set(device.areaId, device.areaName)
    }
  }
  if (areas.size !== 1) return undefined
  const [areaId] = areas.keys()
  const lights: DeviceEntry[] = []
  for (const device of devices.values()) {
    if (device.isLighting && device.areaId === areaId) lights.push(device)
  }
  if (lights.length === 0 || lights.length > 4) return undefined
  const calls: ResolvedCommand['calls'] = []
  for (const light of lights) {
    const function_ = light.functions.find(f => f.name === `set_${light.slug}_state` && f.expectedState)
    if (!function_) return undefined
    calls.push({ arguments: { state: ['включи', 'зажги'].includes(match[1] ?? '') ? 1 : 0 }, name: function_.name })
  }
  return { calls, matched: true }
}

interface LocalIntent { device: DeviceEntry; state?: 0 | 1 }

// The same table serves /voice-phrases and the resolver, so a phrase the speaker
// keeps locally always has a meaning here. Shared phrases are dropped, not guessed.
function localIndex (devices: Map<string, DeviceEntry>): Map<string, LocalIntent | undefined> {
  const index = new Map<string, LocalIntent | undefined>()
  const add = (phrase: string, intent: LocalIntent): void => {
    const previous = index.get(phrase)
    const isSame = previous?.device === intent.device && previous.state === intent.state
    index.set(phrase, index.has(phrase) && !isSame ? undefined : intent)
  }
  for (const device of devices.values()) {
    if (device.voicePhrases) continue
    const places = device.areaName ? areaPlaces(device.areaName) : []
    if (device.speak) {
      for (const name of spokenNames(device)) {
        if (name.includes(' ')) add(name, { device })
        for (const suffix of ['', ...places.map(place => ` ${place}`)]) {
          if (suffix) add(name + suffix, { device })
          if (name.startsWith('сколько ')) continue
          for (const prefix of ['какая', 'какой', 'какое', 'какие', 'скажи']) add(`${prefix} ${name}${suffix}`, { device })
        }
      }
    }
    if (!device.functions.some(f => f.name === `set_${device.slug}_state`)) continue
    const isCover = device.entityId.startsWith('cover.')
    const verbs: [string, 0 | 1][] = isCover
      ? [['открой', 1], ['закрой', 0]]
      : [['включи', 1], ['выключи', 0], ['отключи', 0]]
    for (const name of spokenNames(device)) {
      for (const form of new Set([name, inflect(name).accusative])) {
        for (const [verb, state] of verbs) {
          add(`${verb} ${form}`, { device, state })
          for (const place of places) add(`${verb} ${form} ${place}`, { device, state })
        }
      }
    }
  }
  return index
}

function spokenNames (device: DeviceEntry): string[] {
  const names = new Set<string>()
  for (const raw of [device.name ?? '', ...(device.aliases ?? [])]) {
    const name = normalizeVoicePhrase(raw)
    if (!name) continue
    names.add(name)
    // "Выключатель кухня/балкон свет на балконе" is said as "свет на балконе".
    const tail = /(?:^| )((?:свет|сколько) .+)$/u.exec(name)?.[1]
    if (tail) names.add(tail)
  }
  return [...names]
}

// Bridge limits: 256 KiB response, 5000 phrases. Exceeding either discards the
// whole table on the speaker, so stay well below them and drop the tail instead.
const MAX_LOCAL_PHRASES = 3500
const MAX_LOCAL_BYTES = 150_000

export function localVoicePhrases (devices: Map<string, DeviceEntry>): string[] {
  const reserved = new Set<string>()
  for (const device of devices.values()) {
    for (const phrase of device.voicePhrases ?? []) reserved.add(normalizeVoicePhrase(phrase))
  }
  const candidates: string[] = []
  const lit = [...devices.values()].filter(device => device.isLighting)
  if (lit.length > 0) {
    const places = [...new Set(lit.flatMap(device => device.areaName ? areaPlaces(device.areaName) : []))]
    for (const verb of ['включи', 'выключи', 'отключи', 'погаси', 'зажги']) {
      for (const noun of ['свет', 'освещение']) {
        candidates.push(`${verb} ${noun}`, `${verb} ${noun} здесь`, `${verb} ${noun} тут`,
          `${verb} здесь ${noun}`, `${verb} тут ${noun}`, ...places.map(place => `${verb} ${noun} ${place}`))
      }
    }
  }
  for (const [phrase, intent] of localIndex(devices)) if (intent) candidates.push(phrase)
  const phrases = new Set<string>()
  let bytes = 0
  for (const phrase of candidates) {
    if (reserved.has(phrase) || phrases.has(phrase)) continue
    bytes += Buffer.byteLength(phrase) + 3
    if (phrases.size >= MAX_LOCAL_PHRASES || bytes > MAX_LOCAL_BYTES) break
    phrases.add(phrase)
  }
  return [...phrases]
}

function areaPlaces (area: string): string[] {
  const place = inflect(normalize(area)).prepositional
  return [`в ${place}`, `на ${place}`]
}

function areaNames (area: string): string[] {
  const name = normalize(area)
  const { accusative, prepositional } = inflect(name)
  return [name, prepositional, accusative]
}

// Regular Russian endings for room and device names. A wrong form is only a
// phrase nobody says; it can never select a different device.
function inflect (phrase: string): { accusative: string; prepositional: string } {
  const words = phrase.split(' ').map(inflectWord)
  return {
    accusative: words.map(word => word.accusative).join(' '),
    prepositional: words.map(word => word.prepositional).join(' ')
  }
}

function inflectWord (word: string): { accusative: string; prepositional: string } {
  const rules: [RegExp, string, string][] = [
    [/[ая]я$/u, 'ой', 'ую'], [/ый$/u, 'ом', 'ый'], [/ой$/u, 'ом', 'ой'], [/ий$/u, 'ем', 'ий'],
    [/ия$/u, 'ии', 'ию'], [/я$/u, 'е', 'ю'], [/а$/u, 'е', 'у'], [/й$/u, 'е', 'й'], [/ь$/u, 'е', 'ь']
  ]
  for (const [ending, prepositional, accusative] of rules) {
    if (ending.test(word)) {
      return { accusative: word.replace(ending, accusative), prepositional: word.replace(ending, prepositional) }
    }
  }
  return /[бвгджзклмнпрстфхцчшщ]$/u.test(word) ? { accusative: word, prepositional: `${word}е` } : { accusative: word, prepositional: word }
}

function normalize (text: string): string {
  return text.toLocaleLowerCase('ru').replaceAll('ё', 'е').replaceAll(/\s+/gu, ' ').trim()
}
