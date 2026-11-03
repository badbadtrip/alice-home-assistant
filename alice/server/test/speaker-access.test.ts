import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import { isSpeakerAllowed } from '../src/speaker-access'

test('speaker assignments distinguish all, selected, none and fail closed on invalid storage', () => {
  const directory = mkdtempSync(join(tmpdir(), 'alice-speakers-'))
  const path = join(directory, 'assignments.json')
  try {
    writeFileSync(path, JSON.stringify({ 'automation.kitchen': ['midi'], 'script.night': [] }))
    assert.equal(isSpeakerAllowed('automation.kitchen', 'midi', path), true)
    assert.equal(isSpeakerAllowed('automation.kitchen', 'lite2', path), false)
    assert.equal(isSpeakerAllowed('automation.kitchen', undefined, path), false)
    assert.equal(isSpeakerAllowed('script.night', 'midi', path), false)
    assert.equal(isSpeakerAllowed('script.new', 'future', path), true)
    writeFileSync(path, '{')
    assert.equal(isSpeakerAllowed('script.new', 'midi', path), false)
  } finally { rmSync(directory, { recursive: true }) }
})
