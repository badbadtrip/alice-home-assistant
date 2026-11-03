import { normalizeVoicePhrase, resolveLightCommand } from './control'
import { DeviceEntry } from './devices'
import { isSpeakerAllowed, speakerDevices } from './speaker-access'

// Describes the existing deterministic resolver. Never invokes a device or LLM.
export function previewCommand (text: string, area: string | undefined, speaker: string,
  devices: Map<string, DeviceEntry>) {
  let exact: DeviceEntry | undefined
  for (const device of devices.values()) {
    if (device.voicePhrases?.some(phrase => normalizeVoicePhrase(phrase) === normalizeVoicePhrase(text))) {
      exact = device; break
    }
  }
  const isAllowed = !exact || isSpeakerAllowed(exact.entityId, speaker)
  const plan = resolveLightCommand(text, area, isAllowed ? speakerDevices(devices, speaker) : devices, !!exact)
  return {
    allowed: isAllowed,
    answer: plan.answer,
    calls: plan.calls.map(call => {
      let device: DeviceEntry | undefined
      for (const item of devices.values()) {
        if (item.functions.some(f => f.name === call.name)) { device = item; break }
      }
      return { ...call, label: device?.name ?? device?.entityId ?? call.name, target: device?.entityId }
    }),
    matched: plan.matched,
    reason: isAllowed
      ? plan.answer ??
      (plan.matched
        ? 'Найден точный план. Действия не выполнялись.'
        : 'Точного сопоставления нет. Голосовой запрос может перейти к Яндексу или модели; эта проверка их не вызывает.')
      : 'Команда не назначена этой колонке.',
    route: plan.matched ? 'home' : 'unmatched',
    source: exact ? 'voice_phrase' : (plan.matched ? 'rule' : 'none')
  }
}
