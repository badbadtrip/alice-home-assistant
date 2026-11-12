import z from 'zod'

// One phrase base for the panel and every speaker. Built-in phrases ship with the app;
// the owner's edits are stored on top: own phrases, built-ins switched off, rules switched off.
export const PHRASE_TARGETS = [
  'llm', 'yandex', 'pause', 'resume', 'next', 'previous', 'stop', 'relay'
] as const
export const REPLY_KEYS = [
  'home_done', 'relay_sent', 'relay_failed', 'ma_not_found', 'ma_down', 'llm_down'
] as const
type Target = typeof PHRASE_TARGETS[number]

// A trailing "*" stands for any word ending: "нейросет*" also hears "нейросети".
export const BUILTIN: Record<Target, string[]> = {
  llm: [
    'спроси у нейросет*', 'спроси нейросет*', 'спроси у модел*', 'спроси свою модель', 'узнай у нейросет*',
    'уточни у нейросет*', 'задай вопрос нейросет*', 'ответь через нейросет*', 'скажи через нейросет*', 'нейросет*',
    'найди в интернете', 'поищи в интернете', 'посмотри в интернете', 'глянь в интернете', 'проверь в интернете',
    'узнай в интернете', 'найди в сети', 'поищи в сети', 'поищи в гугле', 'загугли'
  ],
  next: ['следующий трек', 'следующий', 'следующую песню', 'следующая песня', 'дальше', 'вперед', 'включи следующий трек'],
  pause: ['пауза', 'поставь на паузу', 'поставь музыку на паузу', 'поставь песню на паузу', 'приостанови', 'приостанови музыку'],
  previous: ['предыдущий трек', 'предыдущий', 'предыдущую песню', 'предыдущая песня', 'назад', 'включи предыдущий трек'],
  relay: ['скажи на', 'скажи в', 'скажи во', 'передай на', 'передай в', 'передай во'],
  resume: ['продолжи', 'продолжай', 'возобнови', 'продолжи музыку', 'возобнови музыку', 'сними с паузы'],
  stop: ['стоп', 'хватит', 'останови', 'останови музыку', 'выключи музыку', 'выключи песню'],
  yandex: []
}

// Routing rules that are not plain phrases: shown as one line each, switched off whole.
export const RULE_LABELS: Record<string, string> = {
  alarms_timers_reminders: 'Будильники, таймеры и напоминания → Алиса',
  clock_date: 'Время и дата → Алиса',
  devices_and_modes: 'Устройства и режимы Яндекса → Алиса',
  direct_play: '«Включи …» без сервиса → Алиса',
  home_functions: 'Свет и устройства дома → умный дом',
  home_state: '«Что включено», «горит ли» → умный дом',
  home_word_order: '«Свет включи» → умный дом',
  playback_and_volume: 'Плеер и громкость голосом → Алиса',
  weather_forecast: 'Погода → Алиса'
}
// Replaced by the AI phrases above.
const REPLACED_RULES = new Set(['ask_model'])

const PhraseText = z.string().trim().toLowerCase().min(2).max(60).regex(/^[\p{L}\p{N} -]+\*?$/u)
const PhraseItem = z.object({ phrase: PhraseText, target: z.enum(PHRASE_TARGETS) }).strict()

// Saved settings may still hold phrases and replies of removed services (Spotify, YouTube).
// They are dropped one by one, so the owner's other phrases survive the upgrade.
const TARGET_NAMES: readonly string[] = PHRASE_TARGETS
const REPLY_NAMES: readonly string[] = REPLY_KEYS
const knownTargets = (value: unknown): unknown =>
  Array.isArray(value) ? value.filter(item => TARGET_NAMES.includes((item as null | { target?: string })?.target ?? '')) : value
const knownReplies = (value: unknown): unknown => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return value
  const kept = Object.entries(value).filter(([key]) => REPLY_NAMES.includes(key))
  return Object.fromEntries(kept)
}
const PhraseList = (limit: number) => z.preprocess(knownTargets, z.array(PhraseItem).max(limit))
const Replies = z.preprocess(knownReplies, z.partialRecord(z.enum(REPLY_KEYS), z.string().trim().max(200)))

export const PhrasesSchema = z.object({
  disabled: PhraseList(300).default([]),
  disabled_rules: z.array(z.string().max(60)).max(50).default([]),
  items: PhraseList(200).default([]),
  replies: Replies.default({})
}).strict()

export type Phrases = z.infer<typeof PhrasesSchema>
type BridgePhrases = {
  commands: Record<string, string[]>
  relay: string[]
  replace: boolean
  replies: Record<string, string>
}

const COMMANDS = ['next', 'pause', 'previous', 'resume', 'stop'] as const

// Built-ins still on plus the owner's own, without repeats.
export function effective (phrases: Phrases, target: Target): string[] {
  const off = new Set(phrases.disabled.filter(item => item.target === target).map(item => item.phrase))
  const own = phrases.items.filter(item => item.target === target).map(item => item.phrase)
  return [...new Set([...BUILTIN[target].filter(phrase => !off.has(phrase)), ...own])]
}

// "е" also matches "ё", spaces match any spacing, a trailing "*" any word ending.
const pattern = (phrase: string): string => {
  const isStem = phrase.endsWith('*')
  const text = (isStem ? phrase.slice(0, -1) : phrase)
    .replaceAll(/[.*+?^${}()|[\]\\]/gu, String.raw`\$&`)
    .replaceAll(/[её]/gu, '[её]')
    .replaceAll(/\s+/gu, String.raw`\s+`)
  return isStem ? text + String.raw`\S*` : text
}

const PREFIX = String.raw`^\s*(?:алиса[,\s]+)?(?:пожалуйста[,\s]+)?(?:`
const END = String.raw`)(?:\s|[,.!?]|$)`

// What the bridge needs: full lists, so a built-in switched off here is off on the speaker.
export function bridgePhrases (phrases: Phrases): BridgePhrases {
  const commands: Record<string, string[]> = {}
  for (const command of COMMANDS) commands[command] = effective(phrases, command)
  const replies = Object.fromEntries(Object.entries(phrases.replies).filter(([, text]) => text !== ''))
  return {
    commands,
    relay: effective(phrases, 'relay'),
    replace: true,
    replies
  }
}

// The built-in rules the owner kept, without the ones phrases replace.
export function keptRules (rules: unknown[], phrases: Phrases): unknown[] {
  const off = new Set(phrases.disabled_rules)
  return rules.filter(rule => {
    const name = (rule as { name?: unknown }).name
    return typeof name !== 'string' || (!REPLACED_RULES.has(name) && !off.has(name))
  })
}

// The phrase rules go first, so "leave to Alice" and AI phrases win over the built-in rules.
export function phraseRules (phrases: Phrases): unknown[] {
  const rules: unknown[] = []
  for (const route of ['yandex', 'llm'] as const) {
    const list = effective(phrases, route).map(phrase => pattern(phrase))
    if (list.length > 0) rules.push({ match: { regex: PREFIX + list.join('|') + END }, name: 'phrases_' + route, route })
  }
  return rules
}

// The rule list for the panel: every built-in rule that has a label, on or off.
export function ruleList (rules: unknown[], phrases: Phrases): { enabled: boolean; label: string; name: string }[] {
  const off = new Set(phrases.disabled_rules)
  return rules
    .map(rule => (rule as { name?: unknown }).name)
    .filter((name): name is string => typeof name === 'string' && Object.hasOwn(RULE_LABELS, name))
    .map(name => ({ enabled: !off.has(name), label: RULE_LABELS[name] ?? name, name }))
}
