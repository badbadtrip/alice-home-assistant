import assert from 'node:assert/strict'
import { test } from 'node:test'

import { bridgePhrases, ownRules, PhrasesSchema } from '../src/phrases'

test('owner phrases become first-match rules and bridge words', () => {
  const phrases = PhrasesSchema.parse({
    items: [
      { phrase: 'Разузнай в  интернете', target: 'llm' },
      { phrase: 'расскажи сказку', target: 'yandex' },
      { phrase: 'шпотифай', target: 'spotify' },
      { phrase: 'замолчи', target: 'pause' }
    ],
    replies: { relay_failed: 'Не дошло', youtube_missing: '' }
  })
  const rules = ownRules(phrases) as { match: { regex: string }; route: string }[]
  assert.deepEqual(rules.map(rule => rule.route), ['yandex', 'llm'])
  const llm = new RegExp(rules[1]?.match.regex ?? '', 'u')
  assert.ok(llm.test('алиса, разузнай в интернете курс юаня'))
  assert.ok(!llm.test('разузнайте'))
  const bridge = bridgePhrases(phrases)
  assert.deepEqual(bridge.spotify, ['шпотифай'])
  assert.deepEqual(bridge.commands, { pause: ['замолчи'] })
  assert.deepEqual(bridge.replies, { relay_failed: 'Не дошло' })
})

test('phrases reject symbols that could break a pattern', () => {
  assert.equal(PhrasesSchema.safeParse({ items: [{ phrase: 'a(b', target: 'llm' }] }).success, false)
})
