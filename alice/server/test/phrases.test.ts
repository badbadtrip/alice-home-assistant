import assert from 'node:assert/strict'
import { test } from 'node:test'

import { bridgePhrases, effective, keptRules, phraseRules, PhrasesSchema, ruleList } from '../src/phrases'

test('one base: built-ins on by default, owner edits on top', () => {
  const phrases = PhrasesSchema.parse({
    disabled: [{ phrase: 'загугли', target: 'llm' }, { phrase: 'стоп', target: 'stop' }],
    disabled_rules: ['weather_forecast'],
    items: [
      { phrase: 'Разузнай в  интернете', target: 'llm' },
      { phrase: 'расскажи сказку', target: 'yandex' },
      { phrase: 'шпотифай', target: 'spotify' }
    ],
    replies: { relay_failed: 'Не дошло', youtube_missing: '' }
  })
  assert.ok(!effective(phrases, 'llm').includes('загугли'))
  assert.ok(effective(phrases, 'llm').includes('найди в интернете'))
  const rules = phraseRules(phrases) as { match: { regex: string }; route: string }[]
  assert.deepEqual(rules.map(rule => rule.route), ['yandex', 'llm'])
  const llm = new RegExp(rules[1]?.match.regex ?? '', 'u')
  assert.ok(llm.test('алиса, разузнай в интернете курс юаня'))
  assert.ok(llm.test('спроси у нейросети что такое сон'))
  assert.ok(!llm.test('загугли курс'))
  const bridge = bridgePhrases(phrases)
  assert.equal(bridge.replace, true)
  // A phrase of a removed service is dropped on its own; the rest of the base survives.
  assert.deepEqual(phrases.items.map(item => item.target), ['llm', 'yandex'])
  assert.ok(!('spotify' in bridge))
  assert.ok(!bridge.commands.stop?.includes('стоп'))
  assert.deepEqual(bridge.replies, { relay_failed: 'Не дошло' })
  const shipped = [{ name: 'weather_forecast' }, { name: 'ask_model' }, { name: 'clock_date' }]
  assert.deepEqual((keptRules(shipped, phrases) as { name: string }[]).map(rule => rule.name), ['clock_date'])
  assert.deepEqual(ruleList(shipped, phrases).map(rule => [rule.name, rule.enabled]), [['weather_forecast', false], ['clock_date', true]])
})

test('phrases reject symbols that could break a pattern', () => {
  assert.equal(PhrasesSchema.safeParse({ items: [{ phrase: 'a(b', target: 'llm' }] }).success, false)
  assert.equal(PhrasesSchema.safeParse({ items: [{ phrase: 'нейро*сеть', target: 'llm' }] }).success, false)
})
