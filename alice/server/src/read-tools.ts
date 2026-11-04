import { DeviceEntry, snapshotEntity } from './devices'
import { summarizeHistory } from './history'
import { HomeAssistantApi } from './home-assistant'

const annotations = { destructiveHint: false, idempotentHint: true, readOnlyHint: true }
export const READ_TOOLS = [
  {
    annotations,
    description: 'Discover the live Home Assistant devices and sensors explicitly exposed to Alice. Resolve names before reading state/history. Never guess entity IDs.',
    inputSchema: { additionalProperties: false, properties: {}, type: 'object' },
    name: 'home_catalog'
  },
  {
    annotations,
    description: 'Read current Home Assistant states. Omit entity_id to read all exposed devices, or choose one from home_catalog. Not history. No device commands.',
    inputSchema: { additionalProperties: false, properties: { entity_id: { type: 'string' } }, type: 'object' },
    name: 'home_state'
  },
  {
    annotations,
    description: 'Read recorded Home Assistant history. Numeric sensors: min/max and time-weighted mean. Lights/switches/binary sensors: active hours, activations and recent transitions. Ask for period if missing. last_week means the previous complete Monday-to-Monday calendar week, not the last seven days. Calendar periods use the home timezone. Missing data is not zero. No device commands.',
    inputSchema: {
      additionalProperties: false,
      properties: {
        days: { description: 'Required for last_days only', maximum: 31, minimum: 1, type: 'integer' },
        entity_id: { type: 'string' },
        period: { enum: ['last_days', 'today', 'yesterday', 'last_week'], type: 'string' }
      },
      required: ['entity_id', 'period'],
      type: 'object'
    },
    name: 'home_history'
  }
]

export async function callReadTool (name: string, arguments_: Record<string, unknown>,
  api: HomeAssistantApi, devices: Map<string, DeviceEntry>): Promise<unknown> {
  if (name === 'home_catalog') {
    return Array.from(devices.values(), d => ({
      aliases: d.aliases,
      area_id: d.areaId,
      area_name: d.areaName,
      description: d.stateProvider.description,
      entity_id: d.entityId,
      history: d.functions.some(f => f.hasResponse),
      name: d.name
    }))
  }
  if (name === 'home_state' && arguments_.entity_id === undefined) {
    return Array.from(devices.values(), device => ({
      description: device.stateProvider.description,
      entity_id: device.entityId,
      value: device.stateProvider.format(snapshotEntity(api, device.entityId))
    }))
  }
  const id = arguments_.entity_id
  if (typeof id !== 'string' || !devices.has(id)) throw new Error('Entity is not exposed to Alice; use home_catalog')
  const device = devices.get(id)
  if (!device) throw new Error('Entity not exposed')
  const state = snapshotEntity(api, id)
  if (name === 'home_state') {
    return {
      description: device.stateProvider.description,
      entity_id: id,
      value: device.stateProvider.format(state)
    }
  }
  if (name !== 'home_history') throw new Error('Unknown read tool')
  if (device.functions.every(f => !f.hasResponse)) throw new Error('History is not supported for this entity')
  const { end, start } = historyWindow(arguments_.period, arguments_.days,
    new Date(), process.env.HOME_TIMEZONE ?? 'Europe/Minsk')
  const entries = await api.getEntityHistory(id, (end.getTime() - start.getTime()) / 86_400_000, end)
  return {
    device_class: state?.attributes.device_class,
    entity_id: id,
    unit: state?.attributes.unit_of_measurement,
    ...summarizeHistory(entries, start, end, id.startsWith('sensor.'))
  }
}

export function historyWindow (period: unknown, days: unknown, now = new Date(),
  timezone = 'Europe/Minsk'): { end: Date; start: Date; } {
  if (period === 'last_days') {
    if (!Number.isSafeInteger(days) || Number(days) < 1 || Number(days) > 31) throw new Error('days must be an integer from 1 to 31')
    return { end: now, start: new Date(now.getTime() - Number(days) * 86_400_000) }
  }
  if (period !== 'today' && period !== 'yesterday' && period !== 'last_week') {
    throw new Error('Choose last_days, today, yesterday or last_week')
  }
  const parts = Object.fromEntries(new Intl.DateTimeFormat('en-CA', {
    day: '2-digit',
    month: '2-digit',
    timeZone: timezone,
    year: 'numeric'
  }).formatToParts(now).map(v => [v.type, v.value]))
  const year = Number(parts.year); const month = Number(parts.month); const day = Number(parts.day)
  if (period === 'last_week') {
    const weekday = new Date(Date.UTC(year, month - 1, day)).getUTCDay()
    const monday = day - (weekday + 6) % 7
    return { end: midnight(year, month, monday, timezone), start: midnight(year, month, monday - 7, timezone) }
  }
  const today = midnight(year, month, day, timezone)
  return period === 'today' ? { end: now, start: today } : { end: today, start: midnight(year, month, day - 1, timezone) }
}

// Convert local calendar midnight to UTC, including DST offsets.
function midnight (year: number, month: number, day: number, timezone: string): Date {
  const target = Date.UTC(year, month - 1, day)
  let guess = target
  const formatter = new Intl.DateTimeFormat('en-CA', {
    day: '2-digit',
    hour: '2-digit',
    hourCycle: 'h23',
    minute: '2-digit',
    month: '2-digit',
    second: '2-digit',
    timeZone: timezone,
    year: 'numeric'
  })
  for (let index = 0; index < 3; index++) {
    const p = Object.fromEntries(formatter.formatToParts(guess).map(v => [v.type, v.value]))
    const actual = Date.UTC(Number(p.year), Number(p.month) - 1, Number(p.day),
      Number(p.hour), Number(p.minute), Number(p.second))
    guess += target - actual
  }
  return new Date(guess)
}
