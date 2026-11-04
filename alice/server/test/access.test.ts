import assert from 'node:assert/strict'
import test from 'node:test'

import { buildDevices } from '../src/devices'
import { HomeAssistantApi } from '../src/home-assistant'

test('explicit access overrides labels and all-devices mode respects exclusions', async () => {
  let calls = 0
  const api: HomeAssistantApi = {
    addEntityAlias: async () => { /* unused */ }, removeEntityAlias: async () => 'missing' as const, callService: async () => { calls++ },
    getEntityHistory: async () => [],
    getEntityState: () => ({ attributes: {}, name: 'Room light', state: 'off' }),
    getRegistry: async () => [
      { entityId: 'light.room', labels: ['osveshchenie'] },
      { entityId: 'switch.computer', labels: ['alice'] },
      { entityId: 'light.other', labels: [] }
    ],
    hasService: () => true,
    loadServiceIndex: async () => {}
  }
  const labeled = await buildDevices(api, { entities: {}, labels: ['alice', 'osveshchenie'] })
  assert.deepEqual([...labeled.keys()], ['light.room', 'switch.computer'])
  const explicit = await buildDevices(api, { labels: ['alice'], entities: {
    'light.room': { enabled: true, area: 'Room', modes: {}, names: [] },
    'switch.computer': { enabled: false, modes: {}, names: [] }
  } })
  assert.deepEqual([...explicit.keys()], ['light.room'])
  assert.equal(explicit.get('light.room')?.areaName, 'Room')
  const all = await buildDevices(api, { labels: [], expose_all: true, entities: {
    'switch.computer': { enabled: false, modes: {}, names: [] }
  } })
  assert.deepEqual([...all.keys()], ['light.room', 'light.other'])
  assert.equal(calls, 0)
})
