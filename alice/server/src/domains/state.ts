import { EntityConfig } from '../config'
import { LooseEntityState } from './types'

// Meanings follow Home Assistant's binary sensor device classes: [off, on].
const BINARY_STATES: Record<string, readonly [string, string]> = {
  battery: ['заряд батареи нормальный', 'низкий заряд батареи'],
  carbon_monoxide: ['угарный газ не обнаружен', 'обнаружен угарный газ'],
  connectivity: ['нет подключения', 'подключено'],
  door: ['закрыто', 'открыто'],
  garage_door: ['закрыто', 'открыто'],
  gas: ['газ не обнаружен', 'обнаружен газ'],
  lock: ['заперто', 'не заперто'],
  moisture: ['протечка не обнаружена', 'обнаружена протечка'],
  motion: ['движение не обнаружено', 'обнаружено движение'],
  occupancy: ['присутствие не обнаружено', 'обнаружено присутствие'],
  opening: ['закрыто', 'открыто'],
  presence: ['отсутствует', 'присутствует'],
  problem: ['неисправность не обнаружена', 'обнаружена неисправность'],
  safety: ['безопасно', 'небезопасно'],
  smoke: ['дым не обнаружен', 'обнаружен дым'],
  vibration: ['вибрация не обнаружена', 'обнаружена вибрация'],
  window: ['закрыто', 'открыто']
}

/**
 * Build a short human-readable summary of the current entity state.
 * Domain-aware for the interesting attributes (brightness, temperature,
 * volume, media title, ...), with a reasonable generic fallback.
 */
export function formatEntityState (config: EntityConfig,
  entity: LooseEntityState | undefined): string {
  if (!entity) {
    return 'not available'
  }
  if (entity.state === 'unknown' || entity.state === 'unavailable') {
    return entity.state
  }
  const attributes = entity.attributes
  const parts: string[] = [entity.state]

  const stringAttribute = (key: string): string | undefined => {
    const value = attributes[key]
    return typeof value === 'string' && value.length > 0 ? value : undefined
  }
  const numberAttribute = (key: string): number | undefined => {
    const value = attributes[key]
    return typeof value === 'number' && Number.isFinite(value) ? value : undefined
  }

  const renameOf = (listName: string, value: string): string => {
    const renames = config.modes[listName] ?? {}
    const rename = renames[value]
    if (typeof rename === 'string') {
      return rename
    }
    return Array.isArray(rename) && typeof rename[0] === 'string' ? rename[0] : value
  }

  switch (entity.entityId.split('.', 1)[0]) {
    case 'binary_sensor': {
      const deviceClass = stringAttribute('device_class')
      const meanings = deviceClass ? BINARY_STATES[deviceClass] : undefined
      if (meanings && (entity.state === 'on' || entity.state === 'off')) {
        parts.push(meanings[entity.state === 'on' ? 1 : 0])
      }
      if (deviceClass) {
        parts.push(`device_class ${deviceClass}`)
      }
      break
    }
    case 'climate':
    case 'water_heater': {
      const temperature = numberAttribute('temperature') ?? numberAttribute('current_temperature')
      if (temperature !== undefined) {
        parts.push(`${temperature}°`)
      }
      const hvacMode = stringAttribute('hvac_mode') ?? stringAttribute('operation_mode')
      if (hvacMode) {
        parts.push(renameOf('hvac_mode', hvacMode))
      }
      break
    }
    case 'cover':
    case 'valve': {
      const position = numberAttribute('current_position')
      if (position !== undefined) {
        parts.push(`position ${Math.round(position)}%`)
      }
      break
    }
    case 'fan': {
      const percentage = numberAttribute('percentage')
      if (percentage !== undefined) {
        parts.push(`speed ${Math.round(percentage)}%`)
      }
      break
    }
    case 'humidifier': {
      const humidity = numberAttribute('humidity')
      if (humidity !== undefined) {
        parts.push(`humidity ${Math.round(humidity)}%`)
      }
      break
    }
    case 'light': {
      const brightness = numberAttribute('brightness')
      if (brightness !== undefined) {
        parts.push(`brightness ${Math.round(brightness / 255 * 100)}%`)
      }
      const effect = stringAttribute('effect')
      if (effect) {
        parts.push(`effect ${renameOf('effect', effect)}`)
      }
      break
    }
    case 'media_player': {
      const volume = numberAttribute('volume_level')
      if (volume !== undefined) {
        parts.push(`volume ${Math.round(volume * 100)}%`)
      }
      const title = stringAttribute('media_title')
      if (title) {
        parts.push(`playing '${title}'`)
      }
      const source = stringAttribute('source')
      if (source) {
        parts.push(`source ${renameOf('source', source)}`)
      }
      break
    }
    case 'sensor': {
      const unit = stringAttribute('unit_of_measurement')
      if (unit) {
        parts.push(unit)
      }
      const deviceClass = stringAttribute('device_class')
      if (deviceClass) {
        parts.push(`device_class ${deviceClass}`)
      }
      break
    }
    case 'vacuum': {
      const battery = numberAttribute('battery_level')
      if (battery !== undefined) {
        parts.push(`battery ${Math.round(battery)}%`)
      }
      const fanSpeed = stringAttribute('fan_speed')
      if (fanSpeed) {
        parts.push(`fan speed ${renameOf('fan_speed', fanSpeed)}`)
      }
      break
    }
    default: {
      // select/input_select: current option
      const option = stringAttribute('option')
      if (option) {
        parts.push(renameOf('option', option))
      }
      break
    }
  }

  return parts.join(', ')
}
