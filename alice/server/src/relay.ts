import { readFile } from 'node:fs/promises'
import z from 'zod'

const SpeakerSchema = z.object({
  area: z.string().default(''),
  media_entity: z.string().nullable().optional(),
  name: z.string().default('')
})
type Speaker = z.infer<typeof SpeakerSchema>

export const RelayRequestSchema = z.object({
  message: z.string().trim().min(1).max(500),
  room: z.string().trim().min(1).max(60)
}).strict()

export async function readSpeakers (file = ''): Promise<Speaker[]> {
  try {
    return z.array(SpeakerSchema).parse(JSON.parse(await readFile(file, 'utf8')))
  } catch {
    return []
  }
}

export function relayRooms (speakers: Speaker[]): string[] {
  const rooms = speakers.filter(speaker => speaker.media_entity).map(speaker => roomStem(speaker.area))
  return [...new Set(rooms)].filter(room => room !== '' && !room.includes(' '))
}

export function relaySpeaker (speakers: Speaker[], room: string): Speaker | undefined {
  const wanted = roomStem(room)
  return speakers.find(speaker => speaker.media_entity && roomStem(speaker.area) === wanted)
}

export function relayText (intro: string, message: string): string {
  const spoken = message.charAt(0).toUpperCase() + message.slice(1)
  const opening = intro.trim().replace(/[.,:;!?\s]+$/u, '')
  return opening === '' ? spoken : `${opening}. ${spoken}`
}

// "кухню" and "кухня" must meet, so the case ending is dropped on both sides.
export function roomStem (name: string): string {
  return name.trim().toLowerCase().replaceAll('ё', 'е').replace(/[аеиоуыюя]+$/u, '')
}
