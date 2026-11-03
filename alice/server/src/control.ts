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

// Exact grammar only: negations, questions, conditions, compound commands and
// unknown modifiers go to the existing planner, never to a guessed device.
export function resolveLightCommand (text: string, speakerArea: string | undefined,
  devices: Map<string, DeviceEntry>, isVoiceOnly = false): ResolvedCommand {
  const command = normalizeCommand(text)
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
  if (isVoiceOnly) return { calls: [], matched: false }
  const match = /^(включи|выключи|погаси|зажги) (свет|освещение)(?: (?:в|на) ([а-яa-z0-9 -]+)| (здесь|тут))?$/u.exec(command)
  if (!match) return { calls: [], matched: false }
  const area = match[3] ?? speakerArea
  if (!area) return { answer: 'В какой комнате?', calls: [], matched: true }
  const normalizedArea = normalize(area)
  const areas = new Map<string, string>()
  for (const device of devices.values()) {
    if (device.areaId && device.areaName &&
      (device.areaId === normalizedArea || areaNames(device.areaName).includes(normalizedArea))) {
      areas.set(device.areaId, device.areaName)
    }
  }
  if (areas.size !== 1) return { calls: [], matched: false }
  const [areaId] = areas.keys()
  const lights: DeviceEntry[] = []
  for (const device of devices.values()) {
    if (device.isLighting && device.areaId === areaId) lights.push(device)
  }
  if (lights.length === 0 || lights.length > 4) return { calls: [], matched: false }
  const calls: ResolvedCommand['calls'] = []
  for (const light of lights) {
    const function_ = light.functions.find(f => f.name === `set_${light.slug}_state` && f.expectedState)
    if (!function_) return { calls: [], matched: false }
    calls.push({ arguments: { state: ['включи', 'зажги'].includes(match[1] ?? '') ? 1 : 0 }, name: function_.name })
  }
  return { calls, matched: true }
}

function areaNames (area: string): string[] {
  const name = normalize(area)
  const forms: Record<string, string[]> = {
    гостиная: ['гостиной', 'гостиную'],
    детская: ['детской', 'детскую'],
    комната: ['комнате', 'комнату'],
    кухня: ['кухне', 'кухню'],
    спальня: ['спальне', 'спальню']
  }
  return [name, ...(forms[name] ?? [])]
}

function normalize (text: string): string {
  return text.toLocaleLowerCase('ru').replaceAll('ё', 'е').replaceAll(/\s+/gu, ' ').trim()
}
