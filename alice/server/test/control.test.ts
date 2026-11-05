import assert from 'node:assert/strict'
import test from 'node:test'

import { aliasConflict, dictionaryReport, localVoicePhrases, normalizeVoicePhrase, resolveLightCommand } from '../src/control'
import { buildDevices, DeviceEntry } from '../src/devices'
import { executeFunction } from '../src/execution'
import { HomeAssistantApi } from '../src/home-assistant'

function fixture () {
  let calls = 0
  let state = 'on'
  let shouldUpdate = true
  const api: HomeAssistantApi = {
    addEntityAlias: async () => { /* unused */ }, removeEntityAlias: async () => 'missing' as const,
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

test('a place before the device means the same, and nothing else is guessed', async () => {
  const { api } = fixture()
  const devices = await buildDevices(api, { entities: {}, labels: ['alice'] })
  for (const verb of ['включи', 'выключи']) {
    const direct = resolveLightCommand(`${verb} свет на кухне`, 'Комната', devices)
    assert.equal(direct.matched, true)
    assert.deepEqual(resolveLightCommand(`${verb} на кухне свет`, 'Комната', devices), direct)
  }
  for (const text of ['не включи на кухне свет', 'включи на кухне свет через час',
    'включи на кухне свет и компьютер']) {
    assert.equal(resolveLightCommand(text, 'Комната', devices).matched, false, text)
  }
  assert.equal(resolveLightCommand('включи на кухне свет?', 'Комната', devices).calls.length, 0)
})

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

function homeFixture () {
  const items: Record<string, { area?: string; attributes?: Record<string, unknown>; name: string; state?: string }> = {
    'binary_sensor.door': { attributes: { device_class: 'door' }, name: 'Входная дверь', state: 'off' },
    'cover.blind': { area: 'Спальня', name: 'Штора' },
    'light.bath': { area: 'Ванная', name: 'Лампа' },
    'light.hall': { area: 'Второй этаж', name: 'Люстра' },
    'sensor.feeder': { attributes: { unit_of_measurement: 'г' }, name: 'Кормушка сколько кот скушал', state: '40' },
    'sensor.humidity': { attributes: { unit_of_measurement: '%' }, name: 'Влажность на улице', state: 'unavailable' },
    'sensor.outdoor': { attributes: { unit_of_measurement: '°C' }, name: 'Температура на улице', state: '7' },
    'switch.balcony': { area: 'Балкон', name: 'Выключатель кухня/балкон свет на балконе', state: 'on' },
    'switch.pc': { name: 'Компьютер', state: 'on' },
    'switch.plug_a': { area: 'Кухня', name: 'Розетка', state: 'on' },
    'switch.plug_b': { area: 'Спальня', name: 'Розетка', state: 'on' }
  }
  const api: HomeAssistantApi = {
    addEntityAlias: async () => { /* unused */ }, removeEntityAlias: async () => 'missing' as const,
    callService: async () => {},
    getEntityHistory: async () => [],
    getEntityState: id => {
      const item = items[id]
      return item ? { attributes: item.attributes ?? {}, name: item.name, state: item.state ?? 'on' } : undefined
    },
    getRegistry: async () => Object.entries(items).map(([entityId, item]) => ({
      areaId: item.area, areaName: item.area, entityId, labels: ['alice'], name: item.name
    })),
    hasService: () => true,
    loadServiceIndex: async () => {}
  }
  return buildDevices(api, { entities: {}, labels: ['alice'] })
}

test('every dictionary phrase has a meaning, so the speaker never hears a disabled command', async () => {
  const devices = await homeFixture()
  const phrases = localVoicePhrases(devices)
  assert.ok(phrases.length > 100)
  for (const phrase of phrases) {
    assert.equal(normalizeVoicePhrase(phrase), phrase)
    assert.equal(resolveLightCommand(phrase, 'Кухня', devices, true).matched, true, phrase)
  }
})

test('dictionary covers names, rooms and word order from the entities themselves', async () => {
  const devices = await homeFixture()
  const target = (text: string, area?: string) => resolveLightCommand(text, area, devices, true).calls
  assert.deepEqual(target('выключи свет на балконе'), [{ arguments: { state: 0 }, name: 'set_switch_balcony_state' }])
  assert.deepEqual(target('включи свет в ванной'), [{ arguments: { state: 1 }, name: 'set_light_bath_state' }])
  assert.deepEqual(target('выключи свет на втором этаже'), [{ arguments: { state: 0 }, name: 'set_light_hall_state' }])
  assert.deepEqual(target('выключи здесь свет', 'Ванная'), [{ arguments: { state: 0 }, name: 'set_light_bath_state' }])
  assert.deepEqual(target('выключи компьютер'), [{ arguments: { state: 0 }, name: 'set_switch_pc_state' }])
  assert.deepEqual(target('открой штору'), [{ arguments: { state: 1 }, name: 'set_cover_blind_state' }])
  assert.deepEqual(target('выключи розетку на кухне'), [{ arguments: { state: 0 }, name: 'set_switch_plug_a_state' }])
})

test('sensors answer without a model and report missing data honestly', async () => {
  const devices = await homeFixture()
  const answer = (text: string) => resolveLightCommand(text, undefined, devices, true).answer
  assert.equal(answer('какая температура на улице'), 'Температура на улице: 7 градусов.')
  assert.equal(answer('Какая температура на улице?'), 'Температура на улице: 7 градусов.')
  assert.equal(answer('сколько кот скушал'), 'Кормушка сколько кот скушал: 40 г.')
  assert.equal(answer('какая влажность на улице'), 'Влажность на улице: нет данных.')
  assert.equal(answer('входная дверь'), 'Входная дверь: закрыто.')
})

test('dictionary refuses to guess', async () => {
  const devices = await homeFixture()
  const phrases = localVoicePhrases(devices)
  assert.ok(!phrases.includes('выключи розетку'), 'two sockets share the name')
  assert.equal(resolveLightCommand('выключи розетку', 'Кухня', devices).matched, false)
  for (const text of ['не выключай компьютер', 'выключи компьютер через час', 'выключи компьютер и свет']) {
    assert.equal(resolveLightCommand(text, 'Кухня', devices, true).matched, false, text)
  }
  const question = resolveLightCommand('выключи компьютер?', undefined, devices, true)
  assert.equal(question.calls.length, 0)
  assert.equal(resolveLightCommand('выключи компьютер?', undefined, devices).matched, false)
  assert.equal(resolveLightCommand('выключи свет?', 'Ванная', devices, true).calls.length, 0)
  assert.equal(devices.get('switch.pc')?.isLighting, false)
  assert.equal(devices.get('switch.balcony')?.isLighting, true)
})

test('dictionary stays inside the bridge limits and leaves user phrases alone', async () => {
  const devices = await homeFixture()
  const base = devices.get('switch.pc')
  assert.ok(base)
  devices.set('script.own', { ...base, entityId: 'script.own', functions: [], name: 'own', slug: 'own', voicePhrases: ['выключи компьютер'] })
  for (let index = 0; index < 900; index++) {
    const slug = `x${index}`
    devices.set(`switch.${slug}`, { ...base, aliases: [], areaName: 'Кабинет', entityId: `switch.${slug}`,
      functions: [{ ...base.functions[0], name: `set_${slug}_state` } as never], name: `длинное устройство номер ${index}`, slug })
  }
  const phrases = localVoicePhrases(devices)
  assert.ok(phrases.length <= 3500)
  assert.ok(Buffer.byteLength(JSON.stringify(phrases)) < 200_000)
  assert.ok(!phrases.includes('выключи компьютер'))
})

function realHome (): Map<string, DeviceEntry> {
  const entry = (entityId: string, name: string, extra: Partial<DeviceEntry> = {}): [string, DeviceEntry] => {
    const slug = entityId.replace('.', '_')
    const isSensor = entityId.startsWith('sensor.')
    return [entityId, {
      entityId,
      functions: isSensor ? [] : [{ expectedState: true, name: `set_${slug}_state` } as never],
      isLighting: !isSensor && /свет\s+\S/iu.test(name),
      name,
      slug,
      stateProvider: {} as never,
      ...(isSensor && { speak: () => `${name}: 1.` }),
      ...extra
    }]
  }
  return new Map([
    entry('switch.kitchen', 'Выключатель кухня/балкон свет на кухне'),
    entry('switch.pc', 'Компьютер'),
    entry('light.room', 'Свет в комнате', { aliases: ['Люстра'] }),
    entry('light.hall', 'Свет в прихожей', { areaId: 'hall', areaName: 'Прихожая' }),
    entry('sensor.room_t', 'Температура в комнате', { areaId: 'room', areaName: 'Комната', sensorClass: 'temperature' }),
    entry('sensor.out_t', 'Температура на улице', { sensorClass: 'temperature' }),
    entry('sensor.food', 'Кормушка сколько кот скушал')
  ])
}

test('dictionary holds only phrases a person says', () => {
  const junk = /выключатель| ну |прихожой|на комнате|в комнате в комнате|кормушка|скажи/u
  assert.deepEqual(localVoicePhrases(realHome()).filter(phrase => junk.test(phrase)), [])
})

test('dictionary covers light verbs, aliases and sensor questions', () => {
  const phrases = localVoicePhrases(realHome())
  for (const phrase of ['погаси свет на кухне', 'зажги свет в комнате', 'включи люстру', 'включи свет в прихожей',
    'сколько градусов на улице', 'какая температура', 'сколько кот скушал']) assert.ok(phrases.includes(phrase), phrase)
  assert.ok(!phrases.includes('погаси компьютер'))
})

test('a bare reading uses the speaker room, then the only indoor sensor', () => {
  for (const area of ['Кухня', 'Комната']) {
    assert.equal(resolveLightCommand('какая температура', area, realHome(), true).answer, 'Температура в комнате: 1.')
  }
})

test('a bare light command follows the room named by a switch', () => {
  assert.deepEqual(resolveLightCommand('включи свет', 'Кухня', realHome(), true).calls,
    [{ arguments: { state: 1 }, name: 'set_switch_kitchen_state' }])
  assert.deepEqual(resolveLightCommand('погаси свет', 'Комната', realHome(), true).calls,
    [{ arguments: { state: 0 }, name: 'set_light_room_state' }])
})

test('dictionary report groups phrases by device and flags what cannot be said', () => {
  const home = realHome()
  home.set('switch.hidden', { ...home.get('switch.pc') as DeviceEntry, entityId: 'switch.hidden', name: 'Выключатель прихожей', slug: 'hidden' })
  const report = dictionaryReport(home)
  assert.equal(report.total, localVoicePhrases(home).length)
  assert.equal(report.truncated, false)
  assert.deepEqual(report.aliases, [{ alias: 'Люстра', entityId: 'light.room', name: 'Свет в комнате' }])
  assert.ok(report.groups.find(group => group.title === 'Компьютер')?.phrases.includes('включи компьютер'))
  assert.ok(report.groups[0]?.phrases.includes('какая температура'))
  assert.ok(report.warnings.some(text => text.startsWith('Выключатель прихожей: нет устного имени')))
  assert.ok(report.warnings.some(text => text.includes('свет не привязан к комнате')))
})

test('misses keep home commands the speaker sent elsewhere and skip the rest', () => {
  const event = (text: string, route = 'yandex', at = '2026-11-04T20:00:00.000Z') => ({ at, route, text })
  const { misses } = dictionaryReport(realHome(), [
    event('Включи торшер'), event('включи торшер.', 'llm', '2026-11-04T21:00:00.000Z'),
    event('включи компьютер'), event('включи музыку'), event('какая погода'),
    event('расскажи анекдот'), event('включи ночник', 'home'), event('сколько будет 2 у 2'),
    event('сколько горел свет на кухне за прошлую неделю')
  ])
  assert.deepEqual(misses, [{ alias: 'торшер', count: 2, lastAt: '2026-11-04T21:00:00.000Z', phrase: 'включи торшер' }])
})

test('an alias cannot take a name another device answers to', () => {
  assert.equal(aliasConflict('Компьютер', 'light.room', realHome()), 'Компьютер')
  assert.equal(aliasConflict('Компьютер', 'switch.pc', realHome()), undefined)
  assert.equal(aliasConflict('торшер', 'light.room', realHome()), undefined)
})

test('a warning names an entity that has an empty HA name', () => {
  const device = { entityId: 'switch.unnamed', functions: [], name: '' } as unknown as DeviceEntry
  const { warnings } = dictionaryReport(new Map([['unnamed', device]]))
  assert.deepEqual(warnings, ['switch.unnamed: нет устного имени, добавьте алиас в Home Assistant.'])
})
