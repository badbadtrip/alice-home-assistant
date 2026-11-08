import z from 'zod'

// The owner's own words. They add to the built-in phrases and never replace them.
export const PHRASE_TARGETS = [
  'llm', 'yandex', 'spotify', 'youtube', 'pause', 'resume', 'next', 'previous', 'stop', 'relay'
] as const
export const REPLY_KEYS = [
  'relay_sent', 'relay_failed', 'youtube_missing', 'youtube_not_found',
  'spotify_playlist_missing', 'spotify_down', 'spotify_not_found', 'llm_down'
] as const

const PhraseText = z.string().trim().toLowerCase().min(2).max(60).regex(/^[\p{L}\p{N} -]+$/u)
const PhraseItem = z.object({ phrase: PhraseText, target: z.enum(PHRASE_TARGETS) }).strict()

export const PhrasesSchema = z.object({
  items: z.array(PhraseItem).max(200).default([]),
  replies: z.partialRecord(z.enum(REPLY_KEYS), z.string().trim().max(200)).default({})
}).strict()

export type Phrases = z.infer<typeof PhrasesSchema>
type BridgePhrases = {
  commands: Record<string, string[]>
  relay: string[]
  replies: Record<string, string>
  spotify: string[]
  youtube: string[]
}

const COMMANDS = new Set(['next', 'pause', 'previous', 'resume', 'stop'])

// "е" also matches "ё", and any run of spaces matches the speaker's spacing.
const pattern = (phrase: string): string => phrase
  .replaceAll(/[.*+?^${}()|[\]\\]/gu, String.raw`\$&`)
  .replaceAll(/[её]/gu, '[её]')
  .replaceAll(/\s+/gu, String.raw`\s+`)

const PREFIX = String.raw`^\s*(?:алиса[,\s]+)?(?:пожалуйста[,\s]+)?(?:`
const END = String.raw`)(?:\s|[,.!?]|$)`

// What the bridge needs to understand the owner's music, player and intercom words.
export function bridgePhrases (phrases: Phrases): BridgePhrases {
  const of = (target: string): string[] => phrases.items.filter(item => item.target === target).map(item => item.phrase)
  const commands: Record<string, string[]> = {}
  for (const item of phrases.items) if (COMMANDS.has(item.target)) (commands[item.target] ??= []).push(item.phrase)
  const replies = Object.fromEntries(Object.entries(phrases.replies).filter(([, text]) => text !== ''))
  return { commands, relay: of('relay'), replies, spotify: of('spotify'), youtube: of('youtube') }
}

// The owner's AI and "leave to Alice" phrases go first, so they win over built-in rules.
export function ownRules (phrases: Phrases): unknown[] {
  const rules: unknown[] = []
  for (const [target, route] of [['yandex', 'yandex'], ['llm', 'llm']] as const) {
    const list = phrases.items.filter(item => item.target === target).map(item => pattern(item.phrase))
    if (list.length > 0) rules.push({ match: { regex: PREFIX + list.join('|') + END }, name: 'owner_' + route, route })
  }
  return rules
}
