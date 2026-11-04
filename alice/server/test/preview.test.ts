import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { buildDevices } from '../src/devices'
import { HomeAssistantApi } from '../src/home-assistant'
import { previewCommand } from '../src/preview'

test('preview resolves exact phrases and speaker restrictions without invoking HA or model', async () => {
  const directory = mkdtempSync(join(tmpdir(), 'alice-preview-'))
  const previous = process.env.ALICE_ASSIGNMENTS_PATH
  const entity = 'automation.kitchen'
  let executions = 0
  const api: HomeAssistantApi = {
    addEntityAlias: async () => { /* unused */ }, removeEntityAlias: async () => 'missing' as const,
    callService: async () => { executions++ }, getEntityHistory: async () => [],
    getEntityState: () => ({ attributes: {}, name: 'Кухня', state: 'on' }),
    getRegistry: async () => [{ entityId: entity, labels: ['voice'], aliases: ['Не включай свет на кухне'] }],
    hasService: () => true, loadServiceIndex: async () => {}
  }
  try {
    const path = join(directory, 'assignments.json')
    writeFileSync(path, JSON.stringify({ [entity]: ['midi'] }))
    process.env.ALICE_ASSIGNMENTS_PATH = path
    const devices = await buildDevices(api, { entities: {}, labels: [], voice_command_label: 'voice' })
    const allowed = previewCommand('Алиса, не включай свет на кухне!', 'Комната', 'midi', devices)
    assert.equal(allowed.matched, true)
    assert.equal(allowed.allowed, true)
    assert.equal(allowed.source, 'voice_phrase')
    assert.equal(allowed.calls[0]?.target, entity)
    const denied = previewCommand('Не включай свет на кухне', 'Кухня', 'lite2', devices)
    assert.equal(denied.allowed, false)
    assert.match(denied.reason, /не назначена/u)
    assert.equal(previewCommand('расскажи сказку', 'Кухня', 'lite2', devices).matched, false)
    assert.equal(executions, 0)
  } finally {
    if (previous === undefined) delete process.env.ALICE_ASSIGNMENTS_PATH
    else process.env.ALICE_ASSIGNMENTS_PATH = previous
    rmSync(directory, { recursive: true })
  }
})
