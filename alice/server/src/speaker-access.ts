import { readFileSync } from 'node:fs'

import { DeviceEntry } from './devices'

export function isSpeakerAllowed (target: string, speaker: string | undefined,
  path = process.env.ALICE_ASSIGNMENTS_PATH): boolean {
  if (!path) return true
  try {
    const assignments: unknown = JSON.parse(readFileSync(path, 'utf8'))
    if (!assignments || typeof assignments !== 'object' || Array.isArray(assignments)) return false
    const selected = (assignments as Record<string, unknown>)[target]
    return selected === undefined || (Array.isArray(selected) && typeof speaker === 'string' && selected.includes(speaker))
  } catch { return false }
}

export function speakerDevices (devices: Map<string, DeviceEntry>,
  speaker: string | undefined): Map<string, DeviceEntry> {
  return new Map([...devices].filter(([id, device]) => !device.voicePhrases || isSpeakerAllowed(id, speaker)))
}
