import assert from 'node:assert/strict'
import test from 'node:test'

import { buildDevices } from '../src/devices'
import { buildDeviceFunctions } from '../src/domains/build'
import { formatEntityState } from '../src/domains/state'
import { DeviceContext, LooseEntityState } from '../src/domains/types'
import { HomeAssistantApi } from '../src/home-assistant'

const config = { modes: {}, names: [] }
function entity (entityId: string, state: string,
  attributes: Record<string, unknown> = {}): LooseEntityState {
  return { attributes, entityId, features: 0, state }
}

test('sensor values retain units, class and unavailable state', () => {
  assert.equal(formatEntityState(config, entity('sensor.temperature', '21.7', {
    device_class: 'temperature', unit_of_measurement: '°C'
  })), '21.7, °C, device_class temperature')
  assert.equal(formatEntityState(config, entity('sensor.feeder_weight', '0', {
    unit_of_measurement: 'g'
  })), '0, g')
  for (const state of ['unknown', 'unavailable']) {
    assert.equal(formatEntityState(config, entity('sensor.temperature', state, {
      unit_of_measurement: '°C'
    })), state)
    assert.equal(formatEntityState(config, entity('binary_sensor.leak', state, {
      device_class: 'moisture'
    })), state)
  }
})

test('binary sensor polarity follows device class, including lock and battery', () => {
  const cases: [string, string, string][] = [
    ['door', 'off', 'закрыто'], ['door', 'on', 'открыто'],
    ['moisture', 'off', 'протечка не обнаружена'], ['moisture', 'on', 'обнаружена протечка'],
    ['smoke', 'off', 'дым не обнаружен'], ['smoke', 'on', 'обнаружен дым'],
    ['lock', 'on', 'не заперто'], ['battery', 'on', 'низкий заряд батареи']
  ]
  for (const [deviceClass, state, meaning] of cases) {
    assert.equal(formatEntityState(config, entity('binary_sensor.test', state, {
      device_class: deviceClass
    })), `${state}, ${meaning}, device_class ${deviceClass}`)
  }
  assert.equal(formatEntityState(config, entity('binary_sensor.test', 'on')), 'on')
})

test('labelled sensors expose state, remain distinct and never create commands', async () => {
  let serviceCalls = 0
  const api: HomeAssistantApi = {
    addEntityAlias: async () => { /* unused */ }, removeEntityAlias: async () => 'missing' as const,
    callService: async () => { serviceCalls++ },
    getEntityHistory: async () => [],
    getEntityState: () => ({ attributes: { device_class: 'temperature' }, name: 'Температура', state: '22' }),
    getRegistry: async () => [
      { entityId: 'sensor.room_temperature', labels: ['alice'] },
      { entityId: 'sensor.outdoor_temperature', labels: ['alice'] },
      { entityId: 'binary_sensor.leak', labels: ['alice'] },
      { entityId: 'sensor.private', labels: [] },
      { entityId: 'automation.internal', labels: ['alice'] }
    ],
    // Even a misleading service index must not give sensors on/off commands.
    hasService: () => true,
    loadServiceIndex: async () => {}
  }
  const devices = await buildDevices(api, { entities: {}, labels: ['alice'] })
  assert.equal(devices.size, 3)
  assert.equal(new Set([...devices.values()].map(d => d.stateProvider.name)).size, 3)
  assert.ok(!devices.has('sensor.private'))
  for (const device of devices.values()) {
    assert.equal(device.functions.length, 1)
    assert.equal(device.functions[0]?.hasResponse, true)
    assert.match(device.stateProvider.description, /read-only/u)
  }
  for (const domain of ['sensor', 'binary_sensor']) {
    const context: DeviceContext = {
      callService: api.callService, config, domain,
      entity: entity(`${domain}.test`, 'on'), getHistory: api.getEntityHistory,
      name: 'test', services: { has: () => true }, slug: 'test'
    }
    assert.ok(buildDeviceFunctions(context).every(f => f.hasResponse === true))
  }
  assert.equal(serviceCalls, 0)
})
