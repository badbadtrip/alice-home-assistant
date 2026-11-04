import assert from 'node:assert/strict'
import test from 'node:test'

import { HomeFunctionsConfig } from '../src/config'
import { resolveLightCommand } from '../src/control'
import { buildDevices } from '../src/devices'
import { executeFunction } from '../src/execution'
import { HomeAssistantApi } from '../src/home-assistant'

const automation = 'automation.alisa_ne_vkliuchai_svet_na_kukhne'
const helper = 'input_boolean.kitchen_light_block'
const light = 'switch.kitchen'
const config: HomeFunctionsConfig = {
  entities: {}, labels: [],
  voice_commands: {
    kitchen_block: {
      automation, description: 'Block kitchen auto lighting until 04:00',
      phrases: ['Не включай свет на кухне'],
      verify: [{ entity_id: helper, state: 'on' }, { entity_id: light, state: 'off' }]
    }
  }
}

function fixture () {
  const states = new Map([[automation, 'on'], [helper, 'off'], [light, 'on']])
  const calls: unknown[] = []
  const api: HomeAssistantApi = {
    addEntityAlias: async () => { /* unused */ }, removeEntityAlias: async () => 'missing' as const,
    callService: async (domain, service, data) => {
      calls.push({ domain, service, data })
      states.set(helper, 'on'); states.set(light, 'off')
    },
    getEntityHistory: async () => [],
    getEntityState: id => states.has(id) ? { attributes: {}, name: id, state: states.get(id)! } : undefined,
    getRegistry: async () => [],
    hasService: () => true,
    loadServiceIndex: async () => {}
  }
  return { api, calls, states }
}

test('exact configured negative command triggers only the existing HA automation from either speaker', async () => {
  const f = fixture()
  const devices = await buildDevices(f.api, config)
  for (const area of ['Комната', 'Кухня']) {
    const plan = resolveLightCommand('Алиса, не включай свет на кухне!', area, devices)
    assert.equal(plan.matched, true)
    assert.deepEqual(plan.calls, [{ name: 'run_voice_kitchen_block', arguments: {} }])
  }
  assert.equal(f.calls.length, 0, 'resolution must not execute')
  for (const text of ['не выключай свет на кухне', 'не включай свет в комнате',
    'не включай свет на кухне через час', 'не включай свет на кухне и в комнате']) {
    assert.equal(resolveLightCommand(text, 'Кухня', devices).matched, false, text)
  }
  const device = devices.get(automation)!
  const reading = JSON.parse(device.stateProvider.format(undefined))
  assert.equal(reading.checks[0].entity_id, helper)
  assert.equal(reading.checks[0].current, 'off', 'enabled automation must not imply active block')
  const fn = device.functions[0]!
  await assert.rejects(executeFunction(f.api, device, fn, { extra: 1 }))
  const result = await executeFunction(f.api, device, fn, {})
  assert.equal(result.execution?.status, 'confirmed')
  assert.deepEqual(f.calls, [{ domain: 'automation', service: 'trigger',
    data: { entity_id: automation, skip_condition: false } }])
  assert.equal((await executeFunction(f.api, device, fn, {})).execution?.status, 'already')
  assert.equal(f.calls.length, 1)
})

test('configured commands respect disabled automations and reject duplicate phrases', async () => {
  const f = fixture()
  f.states.set(automation, 'off')
  const devices = await buildDevices(f.api, config)
  const device = devices.get(automation)!
  await assert.rejects(executeFunction(f.api, device, device.functions[0]!, {}), /disabled/u)
  assert.equal(f.calls.length, 0)
  const duplicate = structuredClone(config)
  duplicate.voice_commands!.another = duplicate.voice_commands!.kitchen_block!
  await assert.rejects(buildDevices(f.api, duplicate), /Duplicate/u)
})

test('HA labels and aliases are authoritative, support scripts, and never revive removed YAML phrases', async () => {
  const f = fixture()
  const registry = [
    { entityId: automation, labels: ['voice'], aliases: ['Не включай свет на кухне', 'АЛИСА, не включай свет на кухне!'] },
    { entityId: 'script.night', labels: ['voice'], aliases: ['Я ушёл спать'] }
  ]
  f.states.set('script.night', 'off')
  f.api.getRegistry = async () => registry
  const settings = { ...config, voice_command_label: 'voice' }
  let devices = await buildDevices(f.api, settings)
  assert.equal(devices.get(automation)?.voicePhrases?.length, 1)
  assert.equal(resolveLightCommand('Алиса, я ушел спать!', 'Кухня', devices, true).matched, true)
  const script = devices.get('script.night')!
  await executeFunction(f.api, script, script.functions[0]!, {})
  assert.deepEqual(f.calls[0], { domain: 'script', service: 'turn_on', data: { entity_id: 'script.night' } })
  registry[0]!.labels = []
  devices = await buildDevices(f.api, settings)
  assert.equal(resolveLightCommand('Не включай свет на кухне', 'Кухня', devices, true).matched, false)
  registry[0]!.labels = ['voice']
  registry[1]!.aliases = ['Не включай свет на кухне']
  devices = await buildDevices(f.api, settings)
  assert.equal(resolveLightCommand('Не включай свет на кухне', 'Кухня', devices, true).matched, false,
    'ambiguous phrases must execute neither action')
})
