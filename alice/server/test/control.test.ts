import assert from 'node:assert/strict'
import test from 'node:test'

import { resolveLightCommand } from '../src/control'
import { buildDevices } from '../src/devices'
import { executeFunction } from '../src/execution'
import { HomeAssistantApi } from '../src/home-assistant'

function fixture () {
  let calls = 0
  let state = 'on'
  let shouldUpdate = true
  const api: HomeAssistantApi = {
    callService: async () => { calls++; if (shouldUpdate) state = 'off' },
    getEntityHistory: async () => [],
    getEntityState: () => ({ attributes: {}, name: 'Свет', state }),
    getRegistry: async () => [
      { areaId: 'room', areaName: 'Комната', entityId: 'light.room', labels: ['alice'] },
      { areaId: 'kitchen', areaName: 'Кухня', entityId: 'light.kitchen', labels: ['alice'] },
      { areaId: 'room', areaName: 'Комната', entityId: 'switch.pc', labels: ['alice'] }
    ],
    hasService: () => true,
    loadServiceIndex: async () => {}
  }
  return { api, calls: () => calls, setState: (s: string) => { state = s },
    stopUpdates: () => { shouldUpdate = false } }
}

test('two speakers resolve here to their own room, explicit area overrides and plugs are excluded', async () => {
  const { api } = fixture()
  const devices = await buildDevices(api, { entities: {}, labels: ['alice'] })
  const room = resolveLightCommand('выключи свет здесь', 'Комната', devices)
  const kitchen = resolveLightCommand('выключи свет здесь', 'Кухня', devices)
  for (const area of ['Комната', 'Кухня']) {
    for (const place of ['здесь', 'тут']) {
      for (const noun of ['свет', 'освещение']) {
        for (const verb of ['выключи', 'включи', 'погаси', 'зажги']) {
          assert.deepEqual(resolveLightCommand(`${verb} ${place} ${noun}`, area, devices),
            resolveLightCommand(`${verb} ${noun} ${place}`, area, devices))
        }
      }
    }
  }
  for (const text of ['не выключи здесь свет', 'выключи здесь свет?',
    'выключи здесь свет через час', 'выключи тут свет и компьютер']) {
    assert.equal(resolveLightCommand(text, 'Комната', devices).matched, false, text)
  }
  assert.equal(room.calls.length, 1)
  assert.equal(kitchen.calls.length, 1)
  assert.notEqual(room.calls[0]?.name, kitchen.calls[0]?.name)
  assert.deepEqual(resolveLightCommand('выключи свет на кухне', 'Комната', devices).calls, kitchen.calls)
  assert.deepEqual(resolveLightCommand('погаси свет в комнате', 'Кухня', devices).calls, room.calls)
  assert.equal(resolveLightCommand('выключи свет', undefined, devices).answer, 'В какой комнате?')
  for (const text of ['не выключи свет', 'выключи свет через час', 'выключи свет?',
    'выключи свет и включи компьютер', 'если я уйду выключи свет', 'выключи свет в неизвестной комнате']) {
    assert.equal(resolveLightCommand(text, 'Комната', devices).matched, false, text)
  }
})

test('execution checks schema and availability, skips already-off devices and verifies HA state', async () => {
  const f = fixture()
  const devices = await buildDevices(f.api, { entities: {}, labels: ['alice'] })
  const device = devices.get('light.room')!
  const fn = device.functions.find(f => f.expectedState)!
  for (const args of [{ state: 2 }, {}, { state: '0' }, { state: 0, surprise: 1 }]) {
    await assert.rejects(executeFunction(f.api, device, fn, args))
  }
  assert.equal(f.calls(), 0)
  f.setState('unavailable')
  assert.equal((await executeFunction(f.api, device, fn, { state: 0 })).execution?.status, 'unavailable')
  f.setState('off')
  assert.equal((await executeFunction(f.api, device, fn, { state: 0 })).execution?.status, 'already')
  assert.equal(f.calls(), 0)
  f.setState('on')
  assert.equal((await executeFunction(f.api, device, fn, { state: 0 })).execution?.status, 'confirmed')
  assert.equal(f.calls(), 1)
  f.setState('on'); f.stopUpdates()
  assert.equal((await executeFunction(f.api, device, fn, { state: 0 }, 10)).execution?.status, 'accepted')
  assert.equal(f.calls(), 2)
})

test('service errors are never retried', async () => {
  const f = fixture()
  let attempts = 0
  f.api.callService = async () => { attempts++; throw new Error('Connection lost after sending') }
  const device = (await buildDevices(f.api, { entities: {}, labels: ['alice'] })).get('light.room')!
  const fn = device.functions.find(f => f.expectedState)!
  await assert.rejects(executeFunction(f.api, device, fn, { state: 0 }))
  assert.equal(attempts, 1)
})
