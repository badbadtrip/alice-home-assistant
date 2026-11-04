import { EntityConfig, HomeFunctionsConfig } from './config'
import { buildDeviceFunctions } from './domains/build'
import { formatEntityState } from './domains/state'
import {
  DeviceContext,
  DeviceFunction,
  DeviceStateProvider,
  LooseEntityState
} from './domains/types'
import { HomeAssistantApi } from './home-assistant'
import { slugify } from './translit'
import { addVoiceCommands } from './voice-commands'

export interface DeviceEntry {
  aliases?: string[] | undefined;
  areaId?: string | undefined;
  areaName?: string | undefined;
  entityId: string;
  functions: DeviceFunction[];
  isLighting?: boolean;
  name?: string;
  slug: string;
  /**
  Spoken reading of a sensor, built without a model.
  */
  speak?: () => string;
  stateProvider: DeviceStateProvider;
  voicePhrases?: string[];
}

const EXCLUDED_DOMAINS = new Set([
  // Internal domains that should not be exposed to voice control.
  'automation',
  'device_tracker',
  'event',
  'input_datetime',
  'persistent_notification',
  'schedule',
  'sun',
  'text',
  'update',
  'zone'
])

const EMPTY_ENTITY_CONFIG: EntityConfig = { modes: {}, names: [] }

// A wall switch named "... свет на балконе" drives a light. The word must be
// followed by something, so a bare "Свет" on a plug stays a plug.
const LIGHT_SWITCH_NAME = /(?:^|[\s/])свет\s+\S/iu

const SPOKEN_UNITS: Record<string, string> = { '%': 'процентов', '°C': 'градусов', '°С': 'градусов' }

/**
 * Builds the list of exposed devices: every entity carrying one of the configured
 * labels, minus excluded domains. Function/state generation is driven by the live
 * entity state (domain, supported_features, attribute lists) with per-entity
 * mode renames from the config applied on top.
 */
export async function buildDevices (homeAssistant: HomeAssistantApi,
  config: HomeFunctionsConfig): Promise<Map<string, DeviceEntry>> {
  const devices = new Map<string, DeviceEntry>()
  const registry = await homeAssistant.getRegistry()
  const labelSet = new Set(config.labels)
  const usedSlugs = new Set<string>()

  for (const item of registry) {
    const selection = config.entities[item.entityId]?.enabled
    if (selection === false ||
      (selection !== true && !config.expose_all && item.labels.every(label => !labelSet.has(label)))) {
      continue
    }
    const state = homeAssistant.getEntityState(item.entityId)
    if (!state) {
      continue
    }
    const domain = item.entityId.split('.', 1)[0] ?? item.entityId
    if (config.voice_command_label && item.labels.includes(config.voice_command_label) &&
      (domain === 'automation' || domain === 'script')) continue
    if (EXCLUDED_DOMAINS.has(domain)) {
      continue
    }

    const entityConfig = config.entities[item.entityId] ?? EMPTY_ENTITY_CONFIG
    const areaName = entityConfig.area ?? item.areaName
    const areaId = entityConfig.area ? slugify(entityConfig.area) : item.areaId
    const name = entityConfig.description ?? item.name ?? state.name
    const isReadOnly = domain === 'sensor' || domain === 'binary_sensor'
    // Sensors often share names ("Temperature"); entity IDs keep their keys distinct.
    const slug = entityConfig.slug ?? slugify(item.entityId)
    // Fail closed on explicit duplicate slugs instead of calling a different device.
    if (usedSlugs.has(slug)) throw new Error(`Duplicate device slug: ${slug}`)
    usedSlugs.add(slug)

    const context: DeviceContext = {
      callService: (domain_, service, data) => homeAssistant.callService(domain_, service, data),
      config: entityConfig,
      domain,
      entity: {
        ...state,
        entityId: item.entityId,
        features: Number(state.attributes.supported_features ?? 0)
      },
      getHistory: (entityId, days) => homeAssistant.getEntityHistory(entityId, days),
      name,
      services: { has: (domain_, service) => homeAssistant.hasService(domain_, service) },
      slug
    }

    const functions = buildDeviceFunctions(context)

    const stateProvider: DeviceStateProvider = {
      description: `current state of ${name}` +
        (areaName ? `; area: ${areaName}` : '') +
        (isReadOnly ? ` (${item.entityId}; read-only)` : '') +
        ([...entityConfig.names, ...(item.aliases ?? [])].length > 0
          ? `; aliases: ${[...entityConfig.names, ...(item.aliases ?? [])].join(', ')}`
          : ''),
      format: entity => formatEntityState(entityConfig, entity),
      name: `current_${slug}_state`
    }

    devices.set(item.entityId, {
      aliases: [...entityConfig.names, ...(item.aliases ?? [])],
      areaId,
      areaName,
      entityId: item.entityId,
      functions,
      isLighting: domain === 'light' || (domain === 'switch' &&
        (entityConfig.lighting ?? LIGHT_SWITCH_NAME.test(name))),
      name,
      slug,
      ...(isReadOnly && {
        speak: () => spokenReading(name, entityConfig, snapshotEntity(homeAssistant, item.entityId))
      }),
      stateProvider
    })
  }

  addVoiceCommands(homeAssistant, devices, config, registry)
  return devices
}

/**
live snapshot of a device's entity state
*/
export function snapshotEntity (homeAssistant: HomeAssistantApi,
  entityId: string): LooseEntityState | undefined {
  const state = homeAssistant.getEntityState(entityId)
  if (!state) {
    return undefined
  }
  return {
    ...state,
    entityId,
    features: Number(state.attributes.supported_features ?? 0)
  }
}

function spokenReading (name: string, config: EntityConfig, entity: LooseEntityState | undefined): string {
  const [state = 'unavailable', detail] = formatEntityState(config, entity).split(', ', 2)
  if (['not available', 'unavailable', 'unknown'].includes(state)) return `${name}: нет данных.`
  if (entity?.entityId.startsWith('binary_sensor.')) {
    const meaning = detail && !detail.startsWith('device_class') ? detail : (state === 'on' ? 'включено' : 'выключено')
    return `${name}: ${meaning}.`
  }
  const unit = detail && !detail.startsWith('device_class') ? ` ${SPOKEN_UNITS[detail] ?? detail}` : ''
  return `${name}: ${state}${unit}.`
}
