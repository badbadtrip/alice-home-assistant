import { Logger } from 'tslog'

export interface EntityHistoryEntry {
  lastChanged: string;
  state: string;
}

export interface EntityState {
  attributes: Record<string, unknown>;
  name: string;
  state: string;
}

export interface HassServiceDTO {
  domain: string;
  services: Record<string, unknown>;
}

export interface HomeAssistantApi {
  addEntityAlias (entityId: string, alias: string): Promise<void>;
  callService (domain: string, service: string, data: Record<string, unknown>): Promise<void>;
  getEntityHistory (entityId: string, days: number, end?: Date): Promise<EntityHistoryEntry[]>;
  getEntityState (entityId: string): EntityState | undefined;
  getRegistry (): Promise<RegistryEntry[]>;
  hasService (domain: string, service: string): boolean;
  loadServiceIndex (): Promise<void>;
  removeEntityAlias (entityId: string, alias: string): Promise<'missing' | 'removed'>;
}

export interface RegistryEntry {
  aliases?: string[];
  areaAliases?: string[] | undefined;
  areaId?: string | undefined;
  areaName?: string | undefined;
  entityId: string;
  labels: string[];
  name?: string;
}

interface HassLike {
  entity: {
    getCurrentState (entityId: never): unknown;
    registry: {
      current: RawRegistryEntry[];
      list (): Promise<RawRegistryEntry[]>;
    };
  };
  fetch: {
    fetch<T> (options: Record<string, unknown>): Promise<T>;
    listServices (): Promise<HassServiceDTO[]>;
  };
  socket: {
    sendMessage (data: Record<string, unknown>): Promise<unknown>;
  };
}

/**
minimal structural type of the parts of @digital-alchemy/hass used here
*/
interface RawRegistryEntry {
  aliases?: (null | string)[];
  area_id?: string;
  device_id?: string;
  entity_id: string;
  labels?: string[];
  name?: string;
}

/**
 * Thin wrapper around @digital-alchemy/hass exposing exactly what home-functions needs.
 * All entity states come from the library's live websocket-synced cache.
 */
export function createHomeAssistantApi (hass: HassLike, logger: Logger<unknown>): HomeAssistantApi {
  const serviceIndex = new Set<string>()
  let aliasCache: undefined | { aliases: Map<string, (null | string)[]>; until: number }
  let locationCache: undefined | {
    areaAliases: Map<string, string[]>; areas: Map<string, string>; devices: Map<string, string>; until: number;
  }

  async function loadServiceIndex (): Promise<void> {
    const services = await hass.fetch.listServices()
    serviceIndex.clear()
    for (const domain of services) {
      for (const service of Object.keys(domain.services)) {
        serviceIndex.add(`${domain.domain}.${service}`)
      }
    }
    logger.info(`Loaded ${serviceIndex.size} Home Assistant services`)
  }

  function hasService (domain: string, service: string): boolean {
    return serviceIndex.has(`${domain}.${service}`)
  }

  async function getEntityHistory (entityId: string, days: number, end = new Date()): Promise<EntityHistoryEntry[]> {
    const from = new Date(end.getTime() - days * 24 * 60 * 60 * 1000)
    const result = await hass.fetch.fetch<unknown>({
      params: {
        end_time: end.toISOString(),
        filter_entity_id: entityId,
        minimal_response: ''
      },
      url: `/api/history/period/${encodeURIComponent(from.toISOString())}`
    })
    if (!Array.isArray(result) || result.length === 0) {
      return []
    }
    const entries = result[0] as Array<{ last_changed?: string, state?: string }>
    return entries
      .filter(entry => typeof entry.state === 'string')
      .map(entry => ({
        lastChanged: entry.last_changed ?? '',
        state: entry.state as string
      }))
  }

  // The list command omits aliases, so they come from one bulk read.
  async function entityAliases (entries: RawRegistryEntry[]): Promise<Map<string, (null | string)[]>> {
    if (aliasCache && aliasCache.until >= Date.now()) return aliasCache.aliases
    const full = await hass.socket.sendMessage({
      entity_ids: entries.map(item => item.entity_id), type: 'config/entity_registry/get_entries'
    }) as Record<string, null | RawRegistryEntry>
    const aliases = new Map<string, (null | string)[]>()
    for (const [entityId, entry] of Object.entries(full)) aliases.set(entityId, entry?.aliases ?? [])
    aliasCache = { aliases, until: Date.now() + 10_000 }
    return aliases
  }

  async function getRegistry (): Promise<RegistryEntry[]> {
    if (!locationCache || locationCache.until < Date.now()) {
      const [areas, devices] = await Promise.all([
        hass.socket.sendMessage({ type: 'config/area_registry/list' }),
        hass.socket.sendMessage({ type: 'config/device_registry/list' })
      ])
      if (!Array.isArray(areas) || !Array.isArray(devices)) throw new TypeError('Invalid HA location registry')
      locationCache = {
        areaAliases: new Map((areas as { aliases?: string[]; area_id: string }[])
          .map(a => [a.area_id, a.aliases ?? []])),
        areas: new Map((areas as { area_id: string; name: string }[]).map(a => [a.area_id, a.name])),
        devices: new Map((devices as { area_id?: string; id: string }[])
          .filter(d => d.area_id).map(d => [d.id, d.area_id as string])),
        until: Date.now() + 30_000
      }
    }
    const listed = await hass.socket.sendMessage({ type: 'config/entity_registry/list' })
    if (!Array.isArray(listed)) throw new TypeError('Invalid HA entity registry')
    const entries = listed as RawRegistryEntry[]
    const aliases = await entityAliases(entries)
    return entries.map(item => {
      const areaId = item.area_id ?? (item.device_id ? locationCache?.devices.get(item.device_id) : undefined)
      return {
        aliases: (aliases.get(item.entity_id) ?? [])
          .filter((alias): alias is string => typeof alias === 'string'),
        areaAliases: areaId ? locationCache?.areaAliases.get(areaId) : undefined,
        areaId,
        areaName: areaId ? locationCache?.areas.get(areaId) : undefined,
        entityId: item.entity_id,
        labels: item.labels ?? [],
        ...(item.name !== undefined && { name: item.name })
      }
    })
  }
  function getEntityState (entityId: string): EntityState | undefined {
    const raw = hass.entity.getCurrentState(entityId as never) as
      | undefined
      | { attributes?: Record<string, unknown>, state?: string }
    if (!raw || raw.state === undefined) {
      return undefined
    }
    const attributes = raw.attributes ?? {}
    const friendlyName = attributes.friendly_name
    return {
      attributes,
      name: typeof friendlyName === 'string' && friendlyName.length > 0
        ? friendlyName
        : entityId,
      state: raw.state
    }
  }

  async function callService (domain: string, service: string,
    data: Record<string, unknown>): Promise<void> {
    await hass.socket.sendMessage({
      domain,
      service,
      service_data: data,
      type: 'call_service'
    })
  }

  async function addEntityAlias (entityId: string, alias: string): Promise<void> {
    const entry = await hass.socket.sendMessage({
      entity_id: entityId, type: 'config/entity_registry/get'
    }) as RawRegistryEntry
    const aliases = entry.aliases ?? []
    if (aliases.some(item => item?.toLocaleLowerCase('ru') === alias.toLocaleLowerCase('ru'))) return
    await hass.socket.sendMessage({
      aliases: [...aliases, alias], entity_id: entityId, type: 'config/entity_registry/update'
    })
    aliasCache = undefined
  }

  async function removeEntityAlias (entityId: string, alias: string): Promise<'missing' | 'removed'> {
    const entry = await hass.socket.sendMessage({
      entity_id: entityId, type: 'config/entity_registry/get'
    }) as RawRegistryEntry
    const current = entry.aliases ?? []
    // Null is HA's switch for the entity's own name, not an alias.
    const kept = current.filter(item => item?.toLocaleLowerCase('ru') !== alias.toLocaleLowerCase('ru'))
    if (kept.length === current.length) return 'missing'
    await hass.socket.sendMessage({ aliases: kept, entity_id: entityId, type: 'config/entity_registry/update' })
    aliasCache = undefined
    return 'removed'
  }

  return {
    addEntityAlias,
    callService,
    getEntityHistory,
    getEntityState,
    getRegistry,
    hasService,
    loadServiceIndex,
    removeEntityAlias
  }
}
