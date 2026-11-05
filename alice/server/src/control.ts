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
    .replaceAll(/(?:^| )пожалуйста(?= |$)/gu, '').trim()
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
  const lightCommand = placeLast(command.replace(/^(включи|выключи|отключи|погаси|зажги) (здесь|тут) (свет|освещение)$/u, '$1 $3 $2'))
  const match = LIGHT_GRAMMAR.exec(lightCommand)
  const lights = match ? resolveLights(match, speakerArea, devices) : undefined
  const index = localIndex(devices)
  const intent = lights ? undefined : index.get(voicePhrase) ?? index.get(placeLast(voicePhrase)) ?? speakerLight(match, speakerArea, index)
  const hasCalls = (lights?.calls.length ?? 0) > 0 || intent?.state !== undefined
  if (isQuestion && hasCalls) {
    return isVoiceOnly ? { answer: 'Скажите команду ещё раз, без вопроса.', calls: [], matched: true } : { calls: [], matched: false }
  }
  if (lights) return lights
  if (intent?.state !== undefined) {
    return { calls: [{ arguments: { state: intent.state }, name: `set_${intent.device.slug}_state` }], matched: true }
  }
  if (intent?.device.speak) return { answer: intent.device.speak(), calls: [], matched: true }
  const reading = READINGS.get(voicePhrase)
  if (reading) return resolveReading(reading, speakerArea, devices)
  // A dictionary phrase has no planner fallback on the speaker, so answer here.
  return isVoiceOnly && match ? { answer: 'Не нашла подходящий свет. Назовите комнату или объедините светильники в группу.', calls: [], matched: true } : { calls: [], matched: false }
}

// «Включи на кухне свет» means the same as «включи свет на кухне». Only the word
// order changes: anything else in the phrase still fails the exact grammar.
function placeLast (command: string): string {
  return command.replace(/^(включи|выключи|отключи|погаси|зажги|открой|закрой) ((?:в|во|на) [а-яa-z0-9-]+) (.+)$/u, '$1 $3 $2')
}

const LIGHT_GRAMMAR = /^(включи|выключи|отключи|погаси|зажги) (свет|освещение)(?: (?:в|на) ([а-яa-z0-9 -]+)| (здесь|тут)| ([а-яa-z0-9-]+))?$/u

interface LocalIntent { device: DeviceEntry; state?: 0 | 1 }

// Questions without a place: the answer comes from the speaker's room.
const READINGS = new Map<string, string>([
  ...['температура', 'какая температура', 'сколько градусов'].flatMap(phrase =>
    [phrase, `${phrase} дома`].map((key): [string, string] => [key, 'temperature'])),
  ...['влажность', 'какая влажность'].flatMap(phrase =>
    [phrase, `${phrase} дома`].map((key): [string, string] => [key, 'humidity']))
])

// The same table serves /voice-phrases and the resolver, so a phrase the speaker
// keeps locally always has a meaning here. Shared phrases are dropped, not guessed.
function localIndex (devices: Map<string, DeviceEntry>): Map<string, LocalIntent | undefined> {
  const index = new Map<string, LocalIntent | undefined>()
  const add = (phrase: string, intent: LocalIntent): void => {
    const previous = index.get(phrase)
    const isSame = previous?.device === intent.device && previous.state === intent.state
    index.set(phrase, !isSame && index.has(phrase) ? undefined : intent)
  }
  for (const device of devices.values()) {
    if (device.voicePhrases) continue
    const places = device.areaName ? areaPlaces(device.areaName, device.areaAliases) : []
    if (device.speak) {
      for (const name of spokenNames(device)) {
        const degrees = name.replace(/^температура /u, 'сколько градусов ')
        const spoken = device.sensorClass === 'temperature' ? [name, degrees] : [name]
        const forms = new Set(spoken)
        for (const form of forms) {
          const suffixes = NAMED_PLACE.test(form) ? [''] : ['', ...places.map(place => ` ${place}`)]
          for (const suffix of suffixes) {
            if (suffix || form.includes(' ')) add(form + suffix, { device })
            if (FEMININE_NAME.test(form)) add(`какая ${form}${suffix}`, { device })
          }
        }
      }
    }
    if (device.functions.every(f => f.name !== `set_${device.slug}_state`)) continue
    const isCover = device.entityId.startsWith('cover.')
    const verbs: [string, 0 | 1][] = isCover
      ? [['открой', 1], ['закрой', 0]]
      : [['включи', 1], ['выключи', 0], ['отключи', 0], ...(device.isLighting ? LIGHT_ONLY_VERBS : [])]
    for (const name of spokenNames(device)) {
      const forms = new Set([inflect(name).accusative, name])
      if (name.startsWith('свет ')) forms.add(name.replace(/^свет /u, 'освещение '))
      for (const form of forms) {
        for (const [verb, state] of verbs) {
          add(`${verb} ${form}`, { device, state })
          if (NAMED_PLACE.test(form)) continue
          for (const place of places) add(`${verb} ${form} ${place}`, { device, state })
        }
      }
    }
  }
  return index
}

const FEMININE_NAME = /^(?!сколько )\S+(?:[ая]|ость)(?: |$)/u
const NAMED_PLACE = /(?:^| )(?:в|во|на) /u
const LIGHT_ONLY_VERBS: [string, 0 | 1][] = [['зажги', 1], ['погаси', 0]]

function resolveLights (match: RegExpExecArray, speakerArea: string | undefined,
  devices: Map<string, DeviceEntry>): ResolvedCommand | undefined {
  const area = match[3] ?? match[5] ?? speakerArea
  if (!area) return { answer: 'В какой комнате?', calls: [], matched: true }
  const normalizedArea = normalize(area)
  const areas = new Map<string, string>()
  for (const device of devices.values()) {
    if (device.areaId && device.areaName &&
      (device.areaId === normalizedArea || areaNames(device.areaName, device.areaAliases).includes(normalizedArea))) {
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

function resolveReading (sensorClass: string, speakerArea: string | undefined,
  devices: Map<string, DeviceEntry>): ResolvedCommand {
  const here: DeviceEntry[] = []
  const placed: DeviceEntry[] = []
  for (const device of devices.values()) {
    if (!device.speak || device.sensorClass !== sensorClass || !device.areaName) continue
    // An area-less sensor is outdoors or unplaced, so it never answers for the home.
    placed.push(device)
    if (speakerArea && normalize(device.areaName) === normalize(speakerArea)) here.push(device)
  }
  const indoor = here.length > 0 ? here : placed
  const answer = indoor.length === 1 ? indoor[0]?.speak?.() : undefined
  return { answer: answer ?? 'Назовите комнату.', calls: [], matched: true }
}

// Wall switches without an HA area still name their room, so "включи свет" on a
// speaker means the same as saying that speaker's room aloud.
function speakerLight (match: null | RegExpExecArray, speakerArea: string | undefined,
  index: Map<string, LocalIntent | undefined>): LocalIntent | undefined {
  if (!speakerArea || !match || match[3] || match[5]) return undefined
  let found: LocalIntent | undefined
  for (const place of areaPlaces(speakerArea)) {
    const intent = index.get(`${match[1] ?? ''} свет ${place}`)
    if (found && intent && found.device !== intent.device) return undefined
    found ??= intent
  }
  return found
}

function spokenNames (device: DeviceEntry): string[] {
  const names = new Set<string>()
  const raws = [device.name ?? '', ...(device.aliases ?? [])]
  for (const [position, raw] of raws.entries()) {
    const name = normalizeVoicePhrase(raw)
    if (!name) continue
    // "Выключатель кухня/балкон свет на балконе" is said as "свет на балконе".
    const tail = /(?:^| )((?:свет|сколько) .+)$/u.exec(name)?.[1]
    if (tail) names.add(tail)
    // Nobody says a hardware name, but an alias is what the user chose to say.
    else if (position > 0 || !name.startsWith('выключатель')) names.add(name)
  }
  return [...names]
}

// Bridge limits: 256 KiB response, 5000 phrases. Exceeding either discards the
// whole table on the speaker, so stay well below them and drop the tail instead.
const MAX_LOCAL_PHRASES = 3500
const MAX_LOCAL_BYTES = 150_000

export interface DictionaryMiss {
  alias: string;
  count: number;
  lastAt: string;
  phrase: string;
}

export interface DictionaryReport {
  aliases: { alias: string; entityId: string; name: string }[];
  bytes: number;
  groups: { phrases: string[]; title: string }[];
  maxBytes: number;
  maxPhrases: number;
  misses: DictionaryMiss[];
  targets: { entityId: string; name: string }[];
  total: number;
  truncated: boolean;
  warnings: string[];
}

interface MissEvent { at: string; route: string; text: string }

// Read-only view of what a speaker keeps locally, for the settings page.
export function dictionaryReport (devices: Map<string, DeviceEntry>,
  events: MissEvent[] = []): DictionaryReport {
  const { candidates, phrases } = selectPhrases(devices)
  const index = localIndex(devices)
  const kept = new Set(phrases)
  const byDevice = new Map<DeviceEntry, string[]>()
  const shared: string[] = []
  const warnings: string[] = []
  for (const phrase of phrases) {
    const intent = index.get(phrase)
    if (!intent) {
      shared.push(phrase)
      continue
    }
    byDevice.set(intent.device, [...byDevice.get(intent.device) ?? [], phrase])
  }
  const groups = [...byDevice].map(([device, list]) => ({ phrases: list, title: device.name || device.entityId }))
  if (shared.length > 0) groups.unshift({ phrases: shared, title: 'Свет и датчики по комнате' })
  for (const device of devices.values()) {
    if (device.voicePhrases) continue
    const title = device.name || device.entityId
    if (device.isLighting && !device.areaId) warnings.push(`${title}: свет не привязан к комнате.`)
    if (spokenNames(device).length === 0) warnings.push(`${title}: нет устного имени, добавьте алиас в Home Assistant.`)
  }
  for (const [phrase, intent] of index) {
    if (!intent) warnings.push(`«${phrase}» подходит нескольким устройствам и пропущена.`)
  }
  const aliases: DictionaryReport['aliases'] = []
  for (const device of devices.values()) {
    if (device.voicePhrases) continue
    const owned = device.aliases ?? []
    for (const alias of owned) {
      aliases.push({ alias, entityId: device.entityId, name: device.name || device.entityId })
    }
  }
  return {
    aliases,
    bytes: phrases.reduce((sum, phrase) => sum + Buffer.byteLength(phrase) + 3, 0),
    groups,
    maxBytes: MAX_LOCAL_BYTES,
    maxPhrases: MAX_LOCAL_PHRASES,
    misses: dictionaryMisses(events, kept),
    targets: aliasTargets(devices),
    total: phrases.length,
    truncated: candidates.some(phrase => !kept.has(phrase) && !isReserved(devices, phrase)),
    warnings
  }
}

const HOME_QUESTION = /^(?:включи|выключи|отключи|погаси|зажги|открой|закрой|какая|какой|какое|какие|сколько) (.+)$/u
// History questions and arithmetic are real requests, but no alias can answer them.
const NOT_HOME = /музык|песн|радио|подкаст|фильм|мультик|сказк|погод|новост|время|будильник|таймер|\d| за |будет|посмотри/u
const MAX_MISSES = 20

// A new alias must not silently take a name that another device answers to.
export function aliasConflict (alias: string, entityId: string, devices: Map<string, DeviceEntry>): string | undefined {
  const name = normalizeVoicePhrase(alias)
  for (const device of devices.values()) {
    if (device.entityId !== entityId && !device.voicePhrases && spokenNames(device).includes(name)) return device.name
  }
  return undefined
}

// What a device answers to, for routing only: naming one keeps a command at home.
export function deviceVoiceNames (devices: Map<string, DeviceEntry>): string[] {
  const names = new Set<string>()
  for (const device of devices.values()) {
    if (device.voicePhrases) continue
    for (const name of spokenNames(device)) names.add(name).add(inflect(name).accusative)
  }
  return [...names].slice(0, 2000)
}

export function localVoicePhrases (devices: Map<string, DeviceEntry>): string[] {
  return selectPhrases(devices).phrases
}

function aliasTargets (devices: Map<string, DeviceEntry>): { entityId: string; name: string }[] {
  const targets: { entityId: string; name: string }[] = []
  for (const device of devices.values()) {
    if (!device.voicePhrases) targets.push({ entityId: device.entityId, name: device.name || device.entityId })
  }
  return targets
}

function areaNames (area: string, aliases: string[] = []): string[] {
  const name = normalize(area)
  const { accusative, prepositional } = inflect(name)
  const spoken = aliases.map(alias => normalizeVoicePhrase(alias).replace(PLACE_PREPOSITION, ''))
  return [name, prepositional, accusative, ...spoken]
}

// Commands the speaker sent to a model or Yandex although they look meant for the home.
function dictionaryMisses (events: MissEvent[], known: Set<string>): DictionaryMiss[] {
  const misses = new Map<string, DictionaryMiss>()
  const sorted: DictionaryMiss[] = []
  for (const event of events) {
    const phrase = normalizeVoicePhrase(event.text)
    const alias = HOME_QUESTION.exec(phrase)?.[1]
    if (!alias || !['llm', 'yandex'].includes(event.route) || known.has(phrase) || NOT_HOME.test(phrase)) continue
    const miss = misses.get(phrase)
    if (miss) {
      miss.count++
      if (event.at > miss.lastAt) miss.lastAt = event.at
    } else {
      const created = { alias, count: 1, lastAt: event.at, phrase }
      misses.set(phrase, created)
      sorted.push(created)
    }
  }
  sorted.sort((a, b) => b.count - a.count || b.lastAt.localeCompare(a.lastAt))
  return sorted.slice(0, MAX_MISSES)
}

function isReserved (devices: Map<string, DeviceEntry>, phrase: string): boolean {
  for (const device of devices.values()) {
    if (device.voicePhrases?.some(owned => normalizeVoicePhrase(owned) === phrase)) return true
  }
  return false
}

function selectPhrases (devices: Map<string, DeviceEntry>): { candidates: string[]; phrases: string[] } {
  const reserved = new Set<string>()
  for (const device of devices.values()) {
    const owned = device.voicePhrases ?? []
    for (const phrase of owned) reserved.add(normalizeVoicePhrase(phrase))
  }
  const candidates: string[] = []
  const lit: DeviceEntry[] = []
  for (const device of devices.values()) if (device.isLighting) lit.push(device)
  if (lit.length > 0) {
    const places = [...new Set(lit.flatMap(device =>
      device.areaName ? areaPlaces(device.areaName, device.areaAliases) : []))]
    for (const verb of ['включи', 'выключи', 'отключи', 'погаси', 'зажги']) {
      for (const noun of ['свет', 'освещение']) {
        candidates.push(`${verb} ${noun}`, `${verb} ${noun} здесь`, `${verb} ${noun} тут`,
          `${verb} здесь ${noun}`, `${verb} тут ${noun}`, ...places.map(place => `${verb} ${noun} ${place}`))
      }
    }
  }
  for (const [phrase, intent] of localIndex(devices)) if (intent) candidates.push(phrase)
  const classes = new Set<string>()
  for (const device of devices.values()) if (device.speak && device.sensorClass) classes.add(device.sensorClass)
  for (const [phrase, sensorClass] of READINGS) if (classes.has(sensorClass)) candidates.push(phrase)
  const phrases = new Set<string>()
  let bytes = 0
  for (const phrase of candidates) {
    if (reserved.has(phrase) || phrases.has(phrase)) continue
    bytes += Buffer.byteLength(phrase) + 3
    if (phrases.size >= MAX_LOCAL_PHRASES || bytes > MAX_LOCAL_BYTES) break
    phrases.add(phrase)
  }
  return { candidates, phrases: [...phrases] }
}

const PLACE_PREPOSITION = /^(?:в|во|на) /u
const ON_ROOMS = /^(?:балкон|веранд|дач|лоджи|террас|улиц|чердак)/u

// An HA area alias may hold the exact spoken form ("на кухне"); endings are the fallback.
function areaPlaces (area: string, aliases: string[] = []): string[] {
  const places = new Set<string>()
  for (const raw of [area, ...aliases]) {
    const name = normalizeVoicePhrase(raw)
    if (PLACE_PREPOSITION.test(name)) {
      places.add(name)
      continue
    }
    const place = inflect(name).prepositional
    if (name.startsWith('кухн')) places.add(`на ${place}`)
    places.add(`${ON_ROOMS.test(name) ? 'на' : 'в'} ${place}`)
  }
  return [...places]
}

// Regular Russian endings for room and device names. A wrong form is only a
// phrase nobody says; it can never select a different device.
function inflect (phrase: string): { accusative: string; prepositional: string } {
  const accusative: string[] = []
  const prepositional: string[] = []
  let isFixed = false
  for (const word of phrase.split(' ')) {
    // Words after a preposition already carry their case.
    isFixed ||= ['в', 'во', 'на', 'у'].includes(word)
    const forms = isFixed ? { accusative: word, prepositional: word } : inflectWord(word)
    accusative.push(forms.accusative)
    prepositional.push(forms.prepositional)
  }
  return { accusative: accusative.join(' '), prepositional: prepositional.join(' ') }
}

function inflectWord (word: string): { accusative: string; prepositional: string } {
  const rules: [RegExp, string, string][] = [
    [/(?<=[жшчщ])ая$/u, 'ей', 'ую'], [/[ая]я$/u, 'ой', 'ую'], [/ый$/u, 'ом', 'ый'], [/ой$/u, 'ом', 'ой'], [/ий$/u, 'ем', 'ий'],
    [/ия$/u, 'ии', 'ию'], [/я$/u, 'е', 'ю'], [/а$/u, 'е', 'у'], [/й$/u, 'е', 'й'], [/ь$/u, 'е', 'ь']
  ]
  for (const [ending, prepositional, accusative] of rules) {
    if (ending.test(word)) {
      const stem = word.replace(ending, '')
      return { accusative: stem + accusative, prepositional: stem + prepositional }
    }
  }
  const isConsonant = /[бвгджзклмнпрстфхцчшщ]$/u.test(word)
  return { accusative: word, prepositional: isConsonant ? `${word}е` : word }
}

function normalize (text: string): string {
  return text.toLocaleLowerCase('ru').replaceAll('ё', 'е').replaceAll(/\s+/gu, ' ').trim()
}
