import assert from 'node:assert/strict'
import test from 'node:test'

import { buildDeviceFunctions } from '../src/domains/build'
import { DeviceContext } from '../src/domains/types'

function functions (domain: string, attributes: Record<string, unknown>, features: number) {
  const calls: Array<{ data: Record<string, unknown>; service: string }> = []
  const context: DeviceContext = {
    callService: async (_, service, data) => { calls.push({ data, service }) },
    config: { modes: {}, names: [] },
    domain,
    entity: { attributes, entityId: `${domain}.test`, features, state: 'off' },
    getHistory: async () => [],
    name: 'Test',
    services: { has: () => false },
    slug: 'test'
  }
  return { calls, list: buildDeviceFunctions(context) }
}

async function invoke (built: ReturnType<typeof functions>, name: string, values: Record<string, number | string>) {
  const function_ = built.list.find(item => item.name === name)
  assert.ok(function_, name)
  await function_.invoke(values)
  return built.calls.at(-1)
}

test('valves use valve services', async () => {
  const built = functions('valve', {}, 12)
  assert.equal((await invoke(built, 'set_test_state', { state: 1 }))?.service, 'open_valve')
  assert.equal((await invoke(built, 'set_test_state', { state: 0 }))?.service, 'close_valve')
  assert.equal((await invoke(built, 'test_stop', {}))?.service, 'stop_valve')
  assert.equal((await invoke(built, 'set_test_position', { position: 40 }))?.service, 'set_valve_position')
})

test('lawn mowers expose only existing services', async () => {
  const built = functions('lawn_mower', {}, 0)
  assert.equal(built.list.some(item => item.name === 'set_test_state'), false)
  assert.equal((await invoke(built, 'test_pause', {}))?.service, 'pause')
})

test('light color temperature uses kelvin limits and field', async () => {
  const built = functions('light', { max_color_temp_kelvin: 6000, min_color_temp_kelvin: 2500, supported_color_modes: ['color_temp'] }, 0)
  const function_ = built.list.find(item => item.name === 'set_test_color_temperature')
  const schema = function_?.arguments.properties as Record<string, Record<string, unknown>>
  assert.equal(schema.temperature?.minimum, 2500)
  assert.equal(schema.temperature?.maximum, 6000)
  assert.deepEqual((await invoke(built, 'set_test_color_temperature', { temperature: 3000 }))?.data, { color_temp_kelvin: 3000, entity_id: 'light.test' })
})
